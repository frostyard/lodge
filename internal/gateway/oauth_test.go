package gateway

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/frostyard/lodge/internal/allow"
	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/apptest"
	"github.com/frostyard/lodge/internal/hearthtest"
	"github.com/frostyard/lodge/internal/mcpgw"
	"github.com/frostyard/lodge/internal/oauth"
	"github.com/frostyard/lodge/internal/peer"
	"github.com/frostyard/lodge/internal/sessions"
	"github.com/frostyard/lodge/internal/store"
	"github.com/frostyard/lodge/internal/tokens"
)

// oauthHarness is lodge with its OAuth server: the fake WhoIs peer is alice,
// an allowed login and node, and an admin.
type oauthHarness struct {
	t      *testing.T
	root   string
	g      *Gateway
	who    *fakeWhoIs
	lodge  *httptest.Server
	allow  *allow.Lists
	oauth  *oauth.Server
	tokens *tokens.Store
	db     *sql.DB
	clock  *clock
	apps   *apps.Registry
	// granted, revoked and revokedClients record the hooks' calls.
	granted        [][2]string
	revoked        []string
	revokedClients [][]string
	// notes is the app notes, which an MCP token reaches.
	notes *fakeApp
	// seamClient is the client a test's seam works on.
	seamClient string
	// mcp is lodge's MCP gateway, which mcpHook holds for the OAuth hooks: a
	// test may make it again, with other options.
	mcp     *mcpgw.Gateway
	mcpHook atomic.Pointer[mcpgw.Gateway]
	// hearth is hearth's example app, when a test serves it.
	hearth *hearthtest.App
}

// setClient names the client a test's OAuth seam works on.
func (h *oauthHarness) setClient(id string) {
	h.seamClient = id
}

// clientID is the client a test's seam works on.
func (h *oauthHarness) clientID() string { return h.seamClient }

const redirectURI = "https://client.example/callback"

func newOAuthHarness(t *testing.T) *oauthHarness {
	t.Helper()
	return newOAuthHarnessWith(t, nil)
}

// newOAuthHarnessWith builds the harness, after opts changed the OAuth
// server's options.
func newOAuthHarnessWith(t *testing.T, opts func(*oauth.Options)) *oauthHarness {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "lodge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := &oauthHarness{t: t, root: root, who: &fakeWhoIs{peer: alice()}, db: db, notes: &fakeApp{},
		clock: &clock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}}
	apptest.InstallShell(t, root, map[string]string{"index.html": "<!doctype html><title>Lodge</title>"})
	apptest.Install(t, root, "notes", map[string]any{"name": "Notes", "mcp": true}, nil)
	dir := apptest.Install(t, root, "marks", map[string]any{"name": "Marks", "hearth": "hearth.json"}, nil)
	if err := os.WriteFile(filepath.Join(dir, "hearth.json"), []byte(`{"hearth":1,"app":"marks",`+
		`"capabilities":["marks:read","marks:write"],"selectors":["folder","tag"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	apptest.Serve(t, root, "notes", h.notes)
	h.apps = apps.New(root, nil)
	if err := h.apps.Load(); err != nil {
		t.Fatal(err)
	}
	h.apps.Poll(t.Context())
	h.allow = allow.New(db, h.clock.now)
	for _, login := range []string{"alice@example.com"} {
		if err := h.allow.AllowLogin(t.Context(), login); err != nil {
			t.Fatal(err)
		}
		if err := h.allow.AllowAdmin(t.Context(), login); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.allow.AllowNode(t.Context(), "nPhone1CNTRL", "phone"); err != nil {
		t.Fatal(err)
	}
	options := oauth.Options{DB: db, Apps: h.apps,
		Issuer: func() string { return lodgeOrigin },
		Now:    h.clock.now,
		OnGrant: func(principal, app string) {
			h.granted = append(h.granted, [2]string{principal, app})
			if gw := h.mcpHook.Load(); gw != nil {
				gw.GrantChanged(principal, app)
			}
		},
		OnRevoke: func(principal string, clients []string) {
			h.revoked = append(h.revoked, principal)
			h.revokedClients = append(h.revokedClients, clients)
			if gw := h.mcpHook.Load(); gw != nil {
				gw.Revoked(principal, clients)
			}
		}}
	if opts != nil {
		opts(&options)
	}
	h.oauth, err = oauth.New(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	h.tokens = tokens.New(db, h.clock.now)
	h.g = New(Config{Names: TailnetNames(lodgeHost, contentHost), Root: root, Apps: h.apps, Allow: h.allow,
		Tokens: h.tokens, Sessions: sessions.NewRegistry(), OAuth: h.oauth, Now: h.clock.now})
	h.newMCPGateway(nil)
	h.lodge = start(t, h.g.Lodge(h.who))
	return h
}

// req builds a request to the lodge node with a body and headers.
func (h *oauthHarness) req(method, path, body string, hdr map[string]string) *http.Request {
	h.t.Helper()
	var r *http.Request
	var err error
	if body == "" {
		r, err = http.NewRequestWithContext(h.t.Context(), method, h.lodge.URL+path, nil)
	} else {
		r, err = http.NewRequestWithContext(h.t.Context(), method, h.lodge.URL+path, strings.NewReader(body))
	}
	if err != nil {
		h.t.Fatal(err)
	}
	r.Host = lodgeHost
	r.Close = true
	for k, v := range hdr {
		if v == "" {
			r.Header.Del(k)
			continue
		}
		r.Header.Set(k, v)
	}
	return r
}

// post sends an OAuth POST with a form body and no Origin.
func (h *oauthHarness) post(path string, form url.Values) answer {
	h.t.Helper()
	return h.do(h.req(http.MethodPost, path, form.Encode(),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"}))
}

// postJSON sends an OAuth POST with a JSON body and no Origin.
func (h *oauthHarness) postJSON(path, body string) answer {
	h.t.Helper()
	return h.do(h.req(http.MethodPost, path, body, map[string]string{"Content-Type": "application/json"}))
}

// do sends a request and reads the answer.
func (h *oauthHarness) do(r *http.Request) answer {
	h.t.Helper()
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return answer{resp.StatusCode, resp.Header, string(b)}
}

// pageHeaders are the headers a browser sends a consent page's GET.
func pageHeaders() map[string]string {
	return map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": lodgeOrigin}
}

// formHeaders are the headers a browser sends a consent page's POST.
func formHeaders() map[string]string {
	return map[string]string{"Content-Type": "application/x-www-form-urlencoded",
		"Origin": lodgeOrigin, "Sec-Fetch-Site": "same-origin"}
}

var (
	csrfField = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
	hashField = regexp.MustCompile(`name="hash" value="([^"]+)"`)
	tsField   = regexp.MustCompile(`name="ts" value="([^"]+)"`)
)

// register registers a client and returns its answer.
func (h *oauthHarness) register(name string, uris ...string) registrationAnswer {
	h.t.Helper()
	body, err := json.Marshal(map[string]any{"redirect_uris": uris})
	if err != nil {
		h.t.Fatal(err)
	}
	if name != "" {
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			h.t.Fatal(err)
		}
		m["client_name"] = name
		if body, err = json.Marshal(m); err != nil {
			h.t.Fatal(err)
		}
	}
	a := h.postJSON("/oauth/register", string(body))
	if a.status != http.StatusCreated {
		h.t.Fatalf("register %s: %d %q", name, a.status, a.body)
	}
	var out registrationAnswer
	if err := json.Unmarshal([]byte(a.body), &out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

// registrationAnswer is /oauth/register's answer.
type registrationAnswer struct {
	ClientID                string   `json:"client_id"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope"`
}

// verifier is a PKCE code verifier, and its S256 challenge.
const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

// wrongVerifier is another verifier of the same, valid length: fosite refuses
// it on its S256 challenge, not on its form.
const wrongVerifier = "M25iVXpKU3puUjFaYWg3T1NDTDQtcW1ROUY5YXlwalNoc0lfYzJK"

// authorize makes an authorization request as a browser or an MCP client
// would, and returns its answer.
func (h *oauthHarness) authorize(clientID, redirect string, extra map[string]string) answer {
	h.t.Helper()
	q := url.Values{"client_id": {clientID}, "redirect_uri": {redirect}, "response_type": {"code"},
		"state": {"st-1234567890"}, "code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}}
	for k, v := range extra {
		if v == "" {
			q.Del(k)
			continue
		}
		q.Set(k, v)
	}
	return h.do(h.req(http.MethodGet, "/oauth/authorize?"+q.Encode(), "", map[string]string{
		"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}))
}

// consentID is the request ID a 303 to the consent page names.
func consentID(t *testing.T, a answer) string {
	t.Helper()
	loc := a.header.Get("Location")
	id, ok := strings.CutPrefix(loc, "/oauth/consent/")
	if !ok || id == "" {
		t.Fatalf("Location %q is not a consent page", loc)
	}
	return id
}

// consentPage reads a consent page, and its CSRF token and hash.
func (h *oauthHarness) consentPage(id string) (answer, string, string) {
	h.t.Helper()
	a := h.do(h.req(http.MethodGet, "/oauth/consent/"+id, "", pageHeaders()))
	csrf, hash := "", ""
	if m := csrfField.FindStringSubmatch(a.body); m != nil {
		csrf = m[1]
	}
	if m := hashField.FindStringSubmatch(a.body); m != nil {
		hash = m[1]
	}
	return a, csrf, hash
}

// approve decides a consent request with the form a browser sends.
func (h *oauthHarness) approve(id, csrf, hash string, fields url.Values) answer {
	h.t.Helper()
	form := url.Values{"csrf": {csrf}, "hash": {hash}, "decision": {"approve"}}
	for k, v := range fields {
		form[k] = v
	}
	return h.do(h.req(http.MethodPost, "/oauth/consent/"+id, form.Encode(), formHeaders()))
}

// code extracts the authorization code of a redirect answer.
func code(t *testing.T, a answer) string {
	t.Helper()
	loc, err := url.Parse(a.header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	c := loc.Query().Get("code")
	if c == "" {
		t.Fatalf("no code in %q", a.header.Get("Location"))
	}
	return c
}

// tokens is a token endpoint answer.
type tokenAnswer struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}

// exchange trades a code for tokens.
func (h *oauthHarness) exchange(clientID, redirect, code, verifier string) (answer, tokenAnswer) {
	h.t.Helper()
	return h.token(url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID},
		"code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
}

// refresh trades a refresh token for a new pair.
func (h *oauthHarness) refresh(clientID, refresh string) (answer, tokenAnswer) {
	h.t.Helper()
	return h.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID},
		"refresh_token": {refresh}})
}

// token posts to the token endpoint, and decodes its answer, or its OAuth
// error.
func (h *oauthHarness) token(form url.Values) (answer, tokenAnswer) {
	h.t.Helper()
	a := h.post("/oauth/token", form)
	return a, decodeToken(h.t, a)
}

// decodeToken decodes a token answer, or its OAuth error.
func decodeToken(t *testing.T, a answer) tokenAnswer {
	t.Helper()
	var out tokenAnswer
	if err := json.Unmarshal([]byte(a.body), &out); err != nil {
		t.Fatalf("the token answer is not JSON: %v: %q", err, a.body)
	}
	return out
}

// tokenError checks that a token answer is 400 with the OAuth error code.
func tokenError(t *testing.T, a answer, tok tokenAnswer, want string) {
	t.Helper()
	if a.status != http.StatusBadRequest || tok.Error != want {
		t.Errorf("the token answer: %d %q, want 400 with error %q", a.status, a.body, want)
	}
}

// signin is a whole sign-in: a registration, an authorization, an approval
// and the code's exchange, with the scope asked for.
func (h *oauthHarness) signin(name string, scopes ...string) (registrationAnswer, tokenAnswer) {
	h.t.Helper()
	client := h.register(name, redirectURI)
	a := h.authorize(client.ClientID, redirectURI, map[string]string{"scope": strings.Join(scopes, " ")})
	if a.status != http.StatusSeeOther {
		h.t.Fatalf("authorize: %d %q", a.status, a.body)
	}
	id := consentID(h.t, a)
	page, csrf, hash := h.consentPage(id)
	if page.status != http.StatusOK {
		h.t.Fatalf("consent page: %d %q", page.status, page.body)
	}
	fields := url.Values{"principal": {"chani"}}
	for _, sc := range scopes {
		fields.Add("scope", sc)
	}
	approved := h.approve(id, csrf, hash, fields)
	if approved.status != http.StatusSeeOther {
		h.t.Fatalf("approve: %d %q", approved.status, approved.body)
	}
	ans, tok := h.exchange(client.ClientID, redirectURI, code(h.t, approved), verifier)
	if ans.status != http.StatusOK {
		h.t.Fatalf("exchange: %d %q", ans.status, ans.body)
	}
	return client, tok
}

// challenge is the S256 PKCE challenge of a verifier.
func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// TestOAuthDiscovery checks both metadata documents and their URLs.
func TestOAuthDiscovery(t *testing.T) {
	h := newOAuthHarness(t)
	a := h.do(h.req(http.MethodGet, "/.well-known/oauth-authorization-server", "", nil))
	if a.status != http.StatusOK || a.header.Get("Content-Type") != "application/json" ||
		a.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("metadata: %d %q %v", a.status, a.body, a.header)
	}
	want := `{"issuer":"` + lodgeOrigin + `",` +
		`"authorization_endpoint":"` + lodgeOrigin + `/oauth/authorize",` +
		`"token_endpoint":"` + lodgeOrigin + `/oauth/token",` +
		`"registration_endpoint":"` + lodgeOrigin + `/oauth/register",` +
		`"revocation_endpoint":"` + lodgeOrigin + `/oauth/revoke",` +
		`"scopes_supported":["mcp","events"],"response_types_supported":["code"],` +
		`"grant_types_supported":["authorization_code","refresh_token"],` +
		`"code_challenge_methods_supported":["S256"],` +
		`"token_endpoint_auth_methods_supported":["none"],` +
		`"revocation_endpoint_auth_methods_supported":["none"]}`
	if a.body != want {
		t.Errorf("metadata\n got %s\nwant %s", a.body, want)
	}
	resource := `{"resource":"` + lodgeOrigin + `/gateway/mcp",` +
		`"authorization_servers":["` + lodgeOrigin + `"],` +
		`"scopes_supported":["mcp","events"],"bearer_methods_supported":["header"],` +
		`"resource_name":"lodge"}`
	for _, path := range []string{"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/gateway/mcp"} {
		a := h.do(h.req(http.MethodGet, path, "", nil))
		if a.status != http.StatusOK || a.body != resource {
			t.Errorf("%s: %d %s", path, a.status, a.body)
		}
	}
	// A HEAD answer carries the headers and no body.
	head := h.do(h.req(http.MethodHead, "/.well-known/oauth-authorization-server", "", nil))
	if head.status != http.StatusOK || head.body != "" || head.header.Get("Cache-Control") != "no-store" {
		t.Errorf("HEAD: %d %q %v", head.status, head.body, head.header)
	}
	// Another OAuth path is 404, and a nil OAuth server serves none.
	if a := h.do(h.req(http.MethodGet, "/oauth/nope", "", nil)); a.status != http.StatusNotFound {
		t.Errorf("/oauth/nope: %d", a.status)
	}
}

// TestOAuthRegister checks registration: a good request, and each rule.
func TestOAuthRegister(t *testing.T) {
	h := newOAuthHarness(t)
	good := h.postJSON("/oauth/register", `{"client_name":"pi-pocket","redirect_uris":["https://client.example/cb"],`+
		`"token_endpoint_auth_method":"client_secret_basic","grant_types":["authorization_code"],"extra":1}`)
	if good.status != http.StatusCreated {
		t.Fatalf("register: %d %q", good.status, good.body)
	}
	var got registrationAnswer
	if err := json.Unmarshal([]byte(good.body), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.ClientID, "lgc_") || len(got.ClientID) != len("lgc_")+22 {
		t.Errorf("client_id %q is not lgc_ and 22 base64url characters", got.ClientID)
	}
	if got.ClientIDIssuedAt != h.clock.now().Unix() || got.ClientName != "pi-pocket" ||
		strings.Join(got.RedirectURIs, " ") != "https://client.example/cb" ||
		strings.Join(got.GrantTypes, " ") != "authorization_code refresh_token" ||
		strings.Join(got.ResponseTypes, " ") != "code" ||
		got.TokenEndpointAuthMethod != "none" || got.Scope != "mcp events" {
		t.Errorf("register answer = %+v", got)
	}
	// The default name.
	unnamed := h.register("", "https://client.example/cb")
	if unnamed.ClientName != "unnamed client" {
		t.Errorf("client_name = %q, want the default", unnamed.ClientName)
	}
	for name, body := range map[string]string{
		"not an object":       `[]`,
		"no redirect_uris":    `{"client_name":"x"}`,
		"empty redirect_uris": `{"redirect_uris":[]}`,
		"http not loopback":   `{"redirect_uris":["http://client.example/cb"]}`,
		"fragment":            `{"redirect_uris":["https://client.example/cb#f"]}`,
		"not absolute":        `{"redirect_uris":["/cb"]}`,
		"nine redirect uris":  `{"redirect_uris":["https://a.example/1","https://a.example/2","https://a.example/3","https://a.example/4","https://a.example/5","https://a.example/6","https://a.example/7","https://a.example/8","https://a.example/9"]}`,
		"long redirect uri":   `{"redirect_uris":["https://client.example/` + strings.Repeat("a", 512) + `"]}`,
		"long client name":    `{"redirect_uris":["https://a.example/cb"],"client_name":"` + strings.Repeat("n", 65) + `"}`,
		"control in name":     `{"redirect_uris":["https://a.example/cb"],"client_name":"a\u0000b"}`,
		"bad grant type":      `{"redirect_uris":["https://a.example/cb"],"grant_types":["password"]}`,
		"bad response type":   `{"redirect_uris":["https://a.example/cb"],"response_types":["token"]}`,
		"bad scope":           `{"redirect_uris":["https://a.example/cb"],"scope":"admin"}`,
	} {
		a := h.postJSON("/oauth/register", body)
		want := "invalid_client_metadata"
		if strings.Contains(name, "redirect uri") || name == "http not loopback" || name == "fragment" || name == "not absolute" {
			want = "invalid_redirect_uri"
		}
		if a.status != http.StatusBadRequest || !strings.Contains(a.body, want) {
			t.Errorf("%s: %d %q, want 400 %s", name, a.status, a.body, want)
		}
	}
	// Loopback redirect URIs may be http.
	for _, uri := range []string{"http://127.0.0.1/cb", "http://127.0.0.1:1234/cb", "http://[::1]:9/cb", "http://localhost:7/cb"} {
		if a := h.postJSON("/oauth/register", `{"redirect_uris":["`+uri+`"]}`); a.status != http.StatusCreated {
			t.Errorf("%s: %d %q", uri, a.status, a.body)
		}
	}
	// lodge holds at most 256 clients that no consent bound.
	if _, err := h.db.ExecContext(t.Context(), `DELETE FROM oauth_clients`); err != nil {
		t.Fatal(err)
	}
	for i := range oauth.MaxUnboundClients - 1 {
		if _, err := h.db.ExecContext(t.Context(),
			`INSERT INTO oauth_clients (id, name, redirect_uris, registered_at) VALUES (?, 'c', '["https://a.example/cb"]', ?)`,
			"lgc_bulk"+strconv.Itoa(i), time.Now().UTC().Format(store.TimeFormat)); err != nil {
			t.Fatal(err)
		}
	}
	if a := h.postJSON("/oauth/register", `{"redirect_uris":["https://a.example/cb"]}`); a.status != http.StatusCreated {
		t.Fatalf("the 256th client: %d %q", a.status, a.body)
	}
	if a := h.postJSON("/oauth/register", `{"redirect_uris":["https://a.example/cb"]}`); a.status != http.StatusTooManyRequests ||
		!strings.Contains(a.body, "temporarily_unavailable") {
		t.Errorf("the 257th unbound client: %d %q", a.status, a.body)
	}
}

// TestOAuthSignIn is a whole sign-in: registration, the authorization, the
// consent page, the approval, the code's exchange and the token's use.
func TestOAuthSignIn(t *testing.T) {
	h := newOAuthHarness(t)
	client := h.register("pi-pocket", redirectURI)
	a := h.authorize(client.ClientID, redirectURI, map[string]string{"scope": "mcp"})
	if a.status != http.StatusSeeOther {
		t.Fatalf("authorize: %d %q", a.status, a.body)
	}
	id := consentID(t, a)
	page, csrf, hash := h.consentPage(id)
	if page.status != http.StatusOK {
		t.Fatalf("consent page: %d %q", page.status, page.body)
	}
	for _, want := range []string{"pi-pocket", client.ClientID, "https://client.example", "mcp",
		`name="principal" value="pi-pocket"`} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the consent page does not show %q", want)
		}
	}
	// `plain` and a missing challenge are refused.
	q := url.Values{"client_id": {client.ClientID}, "redirect_uri": {redirectURI}, "response_type": {"code"},
		"state": {"st-1234567890"}}
	missing := h.do(h.req(http.MethodGet, "/oauth/authorize?"+q.Encode(), "", nil))
	if missing.status != http.StatusSeeOther || !strings.Contains(missing.header.Get("Location"), "error=invalid_request") ||
		!strings.Contains(missing.header.Get("Location"), "st-1234567890") {
		t.Errorf("a request without code_challenge: %d %q %q", missing.status, missing.header.Get("Location"), missing.body)
	}
	plain := h.authorize(client.ClientID, redirectURI, map[string]string{"code_challenge_method": "plain"})
	if plain.status != http.StatusSeeOther || !strings.Contains(plain.header.Get("Location"), "error=invalid_request") {
		t.Errorf("code_challenge_method plain: %d %q", plain.status, plain.header.Get("Location"))
	}
	// The decision.
	approved := h.approve(id, csrf, hash, url.Values{"principal": {"chani"}, "scope": {"mcp"}})
	if approved.status != http.StatusSeeOther && approved.status != http.StatusFound {
		t.Fatalf("approve: %d %q", approved.status, approved.body)
	}
	loc, err := url.Parse(approved.header.Get("Location"))
	if err != nil || !strings.HasPrefix(approved.header.Get("Location"), redirectURI) ||
		loc.Query().Get("state") != "st-1234567890" || loc.Query().Get("code") == "" {
		t.Fatalf("the approval's Location %q", approved.header.Get("Location"))
	}
	if !strings.HasPrefix(loc.Query().Get("code"), "lgo_ac_") {
		t.Errorf("the code %q has no lodge prefix", loc.Query().Get("code"))
	}
	// A wrong verifier of a valid length is invalid_grant, and the right one
	// still exchanges the same code: the challenge is kept.
	a2, tok2 := h.exchange(client.ClientID, redirectURI, loc.Query().Get("code"), wrongVerifier)
	tokenError(t, a2, tok2, "invalid_grant")
	ans, tok := h.exchange(client.ClientID, redirectURI, loc.Query().Get("code"), verifier)
	if ans.status != http.StatusOK {
		t.Fatalf("exchange: %d %q", ans.status, ans.body)
	}
	if !strings.HasPrefix(tok.AccessToken, "lgo_at_") || !strings.HasPrefix(tok.RefreshToken, "lgo_rt_") ||
		tok.TokenType != "bearer" || tok.ExpiresIn != 3600 || tok.Scope != "mcp" {
		t.Errorf("tokens = %+v", tok)
	}
	// The access token works for its scope, and for no other.
	access, err := h.oauth.Check(t.Context(), tok.AccessToken, "mcp")
	if err != nil || access.Principal != "chani" || access.Client != client.ClientID {
		t.Errorf("Check = %+v, %v", access, err)
	}
	if _, err := h.oauth.Check(t.Context(), tok.AccessToken, "events"); err == nil {
		t.Error("the token works for a scope the consent did not approve")
	}
	// The refresh token's row expires about 30 days later.
	var expires string
	if err := h.db.QueryRowContext(t.Context(),
		`SELECT expires_at FROM oauth_sessions WHERE kind = 'refresh'`).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	exp, err := time.Parse(store.TimeFormat, expires)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(exp); d < 29*24*time.Hour || d > 30*24*time.Hour+time.Minute {
		t.Errorf("the refresh token expires in %v, want about 30 days", d)
	}
	// The code is good once: a second exchange revokes what it issued.
	a3, tok3 := h.exchange(client.ClientID, redirectURI, loc.Query().Get("code"), verifier)
	tokenError(t, a3, tok3, "invalid_grant")
	if _, err := h.oauth.Check(t.Context(), tok.AccessToken, "mcp"); err == nil {
		t.Error("the access token works after its code was used twice")
	}
}

// freePort is a port a client could listen on: a loopback one nobody uses.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestOAuthLoopbackRedirect checks a native client's redirect on a port it
// picks at each sign-in (RFC 8252, section 7.3).
func TestOAuthLoopbackRedirect(t *testing.T) {
	h := newOAuthHarness(t)
	for _, registered := range []string{"http://127.0.0.1:1/callback", "http://127.0.0.1/callback"} {
		client := h.register("native", registered)
		want := fmt.Sprintf("http://127.0.0.1:%d/callback", freePort(t))
		a := h.authorize(client.ClientID, want, map[string]string{"scope": "mcp"})
		if a.status != http.StatusSeeOther || !strings.HasPrefix(a.header.Get("Location"), "/oauth/consent/") {
			t.Fatalf("%s: authorize with %s: %d %q", registered, want, a.status, a.body)
		}
		id := consentID(t, a)
		_, csrf, hash := h.consentPage(id)
		approved := h.approve(id, csrf, hash, url.Values{"principal": {"phone"}, "scope": {"mcp"}})
		loc, err := url.Parse(approved.header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(approved.header.Get("Location"), want) || loc.Port() == "" ||
			"http://"+loc.Host != strings.TrimSuffix(want, "/callback") ||
			loc.Query().Get("code") == "" || loc.Query().Get("state") == "" {
			t.Errorf("%s: the approval's Location %q, want %s with a code and state",
				registered, approved.header.Get("Location"), want)
		}
		// Another port is another redirect URI for http://localhost: it
		// matches exactly, as the spec says.
		if a := h.authorize(client.ClientID, "http://localhost:9/callback", nil); a.status != http.StatusBadRequest ||
			!strings.Contains(a.body, "invalid_request") {
			t.Errorf("%s: an unregistered redirect URI: %d %q %q", registered, a.status, a.body, a.header.Get("Location"))
		}
	}
	localhost := h.register("native-localhost", "http://localhost:1/callback")
	if a := h.authorize(localhost.ClientID, "http://localhost:2/callback", nil); a.status != http.StatusBadRequest {
		t.Errorf("http://localhost matches on any port: %d %q", a.status, a.body)
	}
}

// TestOAuthRefreshRotation checks that a refresh rotates the refresh token,
// and that the old one is refused.
func TestOAuthRefreshRotation(t *testing.T) {
	h := newOAuthHarness(t)
	client, first := h.signin("pi-pocket", "mcp")
	if first.RefreshToken == "" {
		t.Fatal("the first grant has no refresh token")
	}
	a, second := h.refresh(client.ClientID, first.RefreshToken)
	if a.status != http.StatusOK {
		t.Fatalf("refresh: %d %q", a.status, a.body)
	}
	if second.RefreshToken == "" || second.RefreshToken == first.RefreshToken ||
		second.AccessToken == first.AccessToken || second.ExpiresIn != 3600 || second.Scope != "mcp" {
		t.Errorf("the refresh's answer = %+v", second)
	}
	if _, err := h.oauth.Check(t.Context(), second.AccessToken, "mcp"); err != nil {
		t.Errorf("the new access token: %v", err)
	}
	old, oldTok := h.refresh(client.ClientID, first.RefreshToken)
	tokenError(t, old, oldTok, "invalid_grant")
}

// TestOAuthRefreshReuseRevokesTheFamily checks that a refresh token used
// twice revokes every token of its family.
func TestOAuthRefreshReuseRevokesTheFamily(t *testing.T) {
	h := newOAuthHarness(t)
	client, first := h.signin("pi-pocket", "mcp")
	_, second := h.refresh(client.ClientID, first.RefreshToken)
	if _, err := h.oauth.Check(t.Context(), second.AccessToken, "mcp"); err != nil {
		t.Fatalf("the new access token: %v", err)
	}
	// The old refresh token again: reuse, and the family goes.
	old, oldTok := h.refresh(client.ClientID, first.RefreshToken)
	tokenError(t, old, oldTok, "invalid_grant")
	if a, tok := h.refresh(client.ClientID, second.RefreshToken); true {
		tokenError(t, a, tok, "invalid_grant")
	}
	if _, err := h.oauth.Check(t.Context(), second.AccessToken, "mcp"); err == nil {
		t.Error("the new access token works after a refresh token was reused")
	}
}

// TestOAuthRevocation checks /oauth/revoke: a refresh token revokes its
// family, and a token lodge does not know is fine.
func TestOAuthRevocation(t *testing.T) {
	h := newOAuthHarness(t)
	client, tok := h.signin("pi-pocket", "mcp")
	a := h.post("/oauth/revoke", url.Values{"client_id": {client.ClientID}, "token": {tok.RefreshToken},
		"token_type_hint": {"refresh_token"}})
	if a.status != http.StatusOK {
		t.Fatalf("revoke: %d %q", a.status, a.body)
	}
	if _, err := h.oauth.Check(t.Context(), tok.AccessToken, "mcp"); err == nil {
		t.Error("the access token works after its refresh token was revoked")
	}
	if a := h.post("/oauth/revoke", url.Values{"client_id": {client.ClientID}, "token": {"lgo_rt_nonsense"}}); a.status != http.StatusOK {
		t.Errorf("a token lodge does not know: %d %q", a.status, a.body)
	}
}

// pendingSignin makes a client and a sign-in request that waits for a
// decision, and returns its ID, its CSRF token and its hash.
func (h *oauthHarness) pendingSignin() (string, string, string, string) {
	h.t.Helper()
	client := h.register("pi-pocket", redirectURI)
	a := h.authorize(client.ClientID, redirectURI, map[string]string{"scope": "mcp"})
	if a.status != http.StatusSeeOther {
		h.t.Fatalf("authorize: %d %q", a.status, a.body)
	}
	id := consentID(h.t, a)
	_, csrf, hash := h.consentPage(id)
	return client.ClientID, id, csrf, hash
}

// TestOAuthConsentChecks checks each consent check that fails: the Origin,
// the fetch metadata, the CSRF token and the request hash.
func TestOAuthConsentChecks(t *testing.T) {
	cases := []struct {
		name   string
		method string
		hdr    map[string]string
		form   url.Values
		status int
		line   string
	}{
		{name: "POST without Origin", method: http.MethodPost,
			hdr: map[string]string{"Sec-Fetch-Site": "same-origin"}, status: http.StatusForbidden,
			line: "a decision needs lodge's Origin"},
		{name: "POST from another origin", method: http.MethodPost,
			hdr:    map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://evil.example"},
			status: http.StatusForbidden, line: "requests from another origin are refused"},
		{name: "POST without Sec-Fetch-Site", method: http.MethodPost,
			hdr:    map[string]string{"Origin": lodgeOrigin},
			status: http.StatusForbidden, line: "a decision needs Sec-Fetch-Site"},
		{name: "POST from the same site", method: http.MethodPost,
			hdr:    map[string]string{"Origin": lodgeOrigin, "Sec-Fetch-Site": "same-site"},
			status: http.StatusForbidden, line: "requests from another site are refused"},
		{name: "POST with another CSRF token", method: http.MethodPost,
			hdr:    formHeaders(),
			status: http.StatusForbidden, line: "the CSRF token is not this request's"},
		{name: "POST with another hash", method: http.MethodPost,
			hdr:    formHeaders(),
			status: http.StatusConflict, line: "the page shows another request"},
		{name: "GET from another site", method: http.MethodGet,
			hdr:    map[string]string{"Sec-Fetch-Site": "cross-site"},
			status: http.StatusForbidden, line: "requests from another site are refused"},
		{name: "GET with another Origin", method: http.MethodGet,
			hdr:    map[string]string{"Origin": "https://evil.example"},
			status: http.StatusForbidden, line: "requests from another origin are refused"},
		{name: "POST twice", method: http.MethodPost,
			hdr: formHeaders(), status: http.StatusConflict, line: "this request was decided"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newOAuthHarness(t)
			_, id, csrf, hash := h.pendingSignin()
			form := url.Values{"csrf": {csrf}, "hash": {hash}, "principal": {"chani"},
				"scope": {"mcp"}, "decision": {"approve"}}
			switch c.name {
			case "POST with another CSRF token":
				form.Set("csrf", "not-the-token")
			case "POST with another hash":
				form.Set("hash", "not-the-hash")
			case "POST twice":
				if a := h.do(h.req(http.MethodPost, "/oauth/consent/"+id, form.Encode(), formHeaders())); a.status != http.StatusSeeOther {
					t.Fatalf("the first decision: %d %q", a.status, a.body)
				}
			}
			var body string
			if c.method == http.MethodPost {
				body = form.Encode()
			}
			a := h.do(h.req(c.method, "/oauth/consent/"+id, body, c.hdr))
			if a.status != c.status || !strings.Contains(a.body, c.line) {
				t.Errorf("%d %q, want %d with %q", a.status, a.body, c.status, c.line)
			}
			// A refused decision leaves the request pending.
			if c.name != "POST twice" {
				page := h.do(h.req(http.MethodGet, "/oauth/consent/"+id, "", pageHeaders()))
				if page.status != http.StatusOK {
					t.Errorf("the request is not pending after a refused decision: %d %q", page.status, page.body)
				}
			}
		})
	}
}

// TestOAuthConsentExpired checks that a request expires 24 hours after it
// was made.
func TestOAuthConsentExpired(t *testing.T) {
	h := newOAuthHarness(t)
	_, id, csrf, hash := h.pendingSignin()
	h.clock.advance(oauth.RequestLifespan + time.Second)
	a := h.approve(id, csrf, hash, url.Values{"principal": {"chani"}, "scope": {"mcp"}})
	if a.status != http.StatusGone || !strings.Contains(a.body, "expired") {
		t.Errorf("a decision after 24 hours: %d %q", a.status, a.body)
	}
	if page := h.do(h.req(http.MethodGet, "/oauth/consent/"+id, "", pageHeaders())); page.status != http.StatusGone {
		t.Errorf("the page of an expired request: %d %q", page.status, page.body)
	}
}

// TestOAuthConsentNotAllowed checks that a peer whose login is not allowed
// cannot read a consent page.
func TestOAuthConsentNotAllowed(t *testing.T) {
	h := newOAuthHarness(t)
	_, id, _, _ := h.pendingSignin()
	h.who.set(func(p *peer.Peer) { p.Login = "bob@example.com" })
	a := h.do(h.req(http.MethodGet, "/oauth/consent/"+id, "", pageHeaders()))
	if a.status != http.StatusForbidden || !strings.Contains(a.body, "login bob@example.com is not allowed") {
		t.Errorf("a login that is not allowed: %d %q", a.status, a.body)
	}
}

// TestOAuthAdminOnly checks that an allowed login that is not an admin
// cannot read or decide a request, see the grants page, or revoke.
func TestOAuthAdminOnly(t *testing.T) {
	h := newOAuthHarness(t)
	_, id, csrf, hash := h.pendingSignin()
	if err := h.allow.DisallowAdmin(t.Context(), "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	line := "login alice@example.com is not an admin: lodgectl allow admin alice@example.com"
	form := url.Values{"csrf": {csrf}, "hash": {hash}, "decision": {"decline"}}
	for _, c := range []struct {
		name, method, path, body string
		hdr                      map[string]string
	}{
		{"consent page", http.MethodGet, "/oauth/consent/" + id, "", pageHeaders()},
		{"consent decision", http.MethodPost, "/oauth/consent/" + id, form.Encode(), formHeaders()},
		{"grants page", http.MethodGet, "/oauth/grants", "", pageHeaders()},
		{"revoke", http.MethodPost, "/oauth/grants/revoke",
			url.Values{"csrf": {"x"}, "ts": {strconv.FormatInt(h.clock.now().Unix(), 10)}, "client": {"lgc_x"}}.Encode(), formHeaders()},
	} {
		a := h.do(h.req(c.method, c.path, c.body, c.hdr))
		if a.status != http.StatusForbidden || !strings.Contains(a.body, line) {
			t.Errorf("%s: %d %q, want 403 with %q", c.name, a.status, a.body, line)
		}
	}
	// A login that is not on the allow-list at all is refused before the
	// admin check.
	h.who.set(func(p *peer.Peer) { p.Login = "bob@example.com" })
	if a := h.do(h.req(http.MethodGet, "/oauth/grants", "", pageHeaders())); a.status != http.StatusForbidden ||
		!strings.Contains(a.body, "is not allowed") {
		t.Errorf("a login that is not allowed: %d %q", a.status, a.body)
	}
}

// TestOAuthDecline checks that a declined sign-in goes back to the client
// with error=access_denied and its state.
func TestOAuthDecline(t *testing.T) {
	h := newOAuthHarness(t)
	client := h.register("pi-pocket", redirectURI)
	a := h.authorize(client.ClientID, redirectURI, map[string]string{"scope": "mcp"})
	id := consentID(t, a)
	_, csrf, hash := h.consentPage(id)
	declined := h.do(h.req(http.MethodPost, "/oauth/consent/"+id,
		url.Values{"csrf": {csrf}, "hash": {hash}, "decision": {"decline"}}.Encode(), formHeaders()))
	loc, err := url.Parse(declined.header.Get("Location"))
	if err != nil || !strings.HasPrefix(declined.header.Get("Location"), redirectURI) ||
		loc.Query().Get("error") != "access_denied" || loc.Query().Get("state") != "st-1234567890" {
		t.Fatalf("the decline's Location %q (%d %q)", declined.header.Get("Location"), declined.status, declined.body)
	}
	// The client is not bound, and the request is decided.
	var principal sql.NullString
	if err := h.db.QueryRowContext(t.Context(), `SELECT principal FROM oauth_clients WHERE id = ?`,
		client.ClientID).Scan(&principal); err != nil || principal.Valid {
		t.Errorf("principal after a decline = %v, %v", principal, err)
	}
}

// marksItems is what an agent asks for app marks.
func marksItems() []oauth.Item {
	return []oauth.Item{
		{Cap: "marks:read", Sel: map[string]string{"folder": "/Reading"}},
		{Cap: "marks:write", Sel: map[string]string{"folder": "/Reading/Inbox"}},
	}
}

// TestOAuthAccessRequest checks an access request: its page, an approved
// narrowing, and the grant.
func TestOAuthAccessRequest(t *testing.T) {
	h := newOAuthHarness(t)
	client, _ := h.signin("pi-pocket", "mcp")
	req, err := h.oauth.RequestAccess(t.Context(), client.ClientID, "marks", marksItems())
	if err != nil {
		t.Fatal(err)
	}
	if req.ID == "" || req.URL != lodgeOrigin+"/oauth/consent/"+req.ID || req.Kind != oauth.KindAccess ||
		req.Principal != "chani" || req.App != "marks" {
		t.Fatalf("RequestAccess = %+v", req)
	}
	page, csrf, hash := h.consentPage(req.ID)
	if page.status != http.StatusOK {
		t.Fatalf("the consent page: %d %q", page.status, page.body)
	}
	for _, want := range []string{"pi-pocket", client.ClientID, "chani", "Marks", "marks", "marks:read", "marks:write",
		"folder", "/Reading", "/Reading/Inbox", `name="path.0.folder"`, `name="path.1.folder"`} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the page does not show %q", want)
		}
	}
	// An admin narrows the first item, and keeps the second.
	approved := h.approve(req.ID, csrf, hash, url.Values{"item": {"0", "1"},
		"path.0.folder": {"/Reading/Fiction"}, "path.1.folder": {"/Reading/Inbox"}})
	if approved.status != http.StatusSeeOther || approved.header.Get("Location") != "/oauth/grants" {
		t.Fatalf("the approval: %d %q %q", approved.status, approved.body, approved.header.Get("Location"))
	}
	granted, err := h.oauth.Grant(t.Context(), "chani", "marks")
	if err != nil {
		t.Fatal(err)
	}
	if len(granted) != 2 || granted[0].Cap != "marks:read" || granted[0].Sel["folder"] != "/Reading/Fiction" ||
		granted[1].Cap != "marks:write" || granted[1].Sel["folder"] != "/Reading/Inbox" {
		t.Errorf("Grant = %+v", granted)
	}
	if len(h.granted) != 1 || h.granted[0] != [2]string{"chani", "marks"} {
		t.Errorf("OnGrant was called with %+v", h.granted)
	}
}

// TestOAuthAccessNarrowing checks that an approval may narrow a path, never
// widen it, and that a refused approval leaves the request pending.
func TestOAuthAccessNarrowing(t *testing.T) {
	h := newOAuthHarness(t)
	client, _ := h.signin("pi-pocket", "mcp")
	items := []oauth.Item{{Cap: "marks:read", Sel: map[string]string{"folder": "/Chani"}},
		{Cap: "marks:write", Sel: map[string]string{"folder": "/Reading"}}}
	req, err := h.oauth.RequestAccess(t.Context(), client.ClientID, "marks", items)
	if err != nil {
		t.Fatal(err)
	}
	_, csrf, hash := h.consentPage(req.ID)
	// A wider path, and a sibling one.
	for _, worse := range []string{"/", "/Chan"} {
		a := h.approve(req.ID, csrf, hash, url.Values{"item": {"0"}, "path.0.folder": {worse}})
		if a.status != http.StatusBadRequest || !strings.Contains(a.body, "is not inside") {
			t.Errorf("the path %s: %d %q", worse, a.status, a.body)
		}
		if page := h.do(h.req(http.MethodGet, "/oauth/consent/"+req.ID, "", pageHeaders())); page.status != http.StatusOK {
			t.Errorf("the request after a refused approval: %d %q", page.status, page.body)
		}
	}
	// Its descendant is approved, and unchecking the other item approves
	// only the rest.
	a := h.approve(req.ID, csrf, hash, url.Values{"item": {"0"}, "path.0.folder": {"/Chani/Notes"}})
	if a.status != http.StatusSeeOther {
		t.Fatalf("the approval: %d %q", a.status, a.body)
	}
	granted, err := h.oauth.Grant(t.Context(), "chani", "marks")
	if err != nil {
		t.Fatal(err)
	}
	if len(granted) != 1 || granted[0].Cap != "marks:read" || granted[0].Sel["folder"] != "/Chani/Notes" {
		t.Errorf("Grant = %+v", granted)
	}
	// A path that is not a path is refused too.
	req2, err := h.oauth.RequestAccess(t.Context(), client.ClientID, "marks", marksItems())
	if err != nil {
		t.Fatal(err)
	}
	_, csrf2, hash2 := h.consentPage(req2.ID)
	for _, bad := range []string{"", "Reading", "/Reading/", "/Reading/../etc", "/ṫ"} {
		a := h.approve(req2.ID, csrf2, hash2, url.Values{"item": {"0"}, "path.0.folder": {bad}})
		if a.status != http.StatusBadRequest {
			t.Errorf("the path %q: %d %q", bad, a.status, a.body)
		}
	}
}

// grantsPage reads the grants page, with its CSRF token and its time.
func (h *oauthHarness) grantsPage() (answer, string, string) {
	h.t.Helper()
	a := h.do(h.req(http.MethodGet, "/oauth/grants", "", pageHeaders()))
	csrf, ts := "", ""
	if m := csrfField.FindStringSubmatch(a.body); m != nil {
		csrf = m[1]
	}
	if m := tsField.FindStringSubmatch(a.body); m != nil {
		ts = m[1]
	}
	return a, csrf, ts
}

// revokeTarget posts a revocation from the grants page.
func (h *oauthHarness) revoke(field, value, csrf, ts string) answer {
	h.t.Helper()
	return h.do(h.req(http.MethodPost, "/oauth/grants/revoke",
		url.Values{"csrf": {csrf}, "ts": {ts}, field: {value}}.Encode(), formHeaders()))
}

// TestOAuthGrantsPage checks the grants page: its lists, and the
// revocations.
func TestOAuthGrantsPage(t *testing.T) {
	h := newOAuthHarness(t)
	client, tok := h.signin("pi-pocket", "mcp")
	if _, err := h.oauth.Check(t.Context(), tok.AccessToken, "mcp"); err != nil {
		t.Fatal(err)
	}
	req, err := h.oauth.RequestAccess(t.Context(), client.ClientID, "marks", marksItems())
	if err != nil {
		t.Fatal(err)
	}
	page, csrf, ts := h.grantsPage()
	if page.status != http.StatusOK || csrf == "" || ts == "" {
		t.Fatalf("the grants page: %d %q", page.status, page.body)
	}
	for _, want := range []string{"chani", "pi-pocket", client.ClientID, "mcp", "access", req.URL} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the grants page does not show %q", want)
		}
	}
	// An approved request shows as a grant.
	_, reqCSRF, reqHash := h.consentPage(req.ID)
	if a := h.approve(req.ID, reqCSRF, reqHash, url.Values{"item": {"0"},
		"path.0.folder": {"/Reading"}}); a.status != http.StatusSeeOther {
		t.Fatalf("approving the access request: %d %q", a.status, a.body)
	}
	page, csrf, ts = h.grantsPage()
	for _, want := range []string{"marks", "marks:read", "/Reading", "Marks"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the grants page does not show the grant's %q", want)
		}
	}
	// The last use was recorded by Check.
	var lastUsed sql.NullString
	if err := h.db.QueryRowContext(t.Context(), `SELECT last_used_at FROM oauth_clients WHERE id = ?`, client.ClientID).Scan(&lastUsed); err != nil || !lastUsed.Valid {
		t.Errorf("last_used_at = %v, %v", lastUsed, err)
	}
	// A revocation needs the page's token and its time.
	if a := h.revoke("client", client.ClientID, "wrong", ts); a.status != http.StatusForbidden {
		t.Errorf("a revocation with a bad CSRF token: %d %q", a.status, a.body)
	}
	if a := h.revoke("client", client.ClientID, csrf, "1"); a.status != http.StatusForbidden {
		t.Errorf("a revocation with an expired ts: %d %q", a.status, a.body)
	}
	if a := h.revoke("client", "lgc_unknown", csrf, ts); a.status != http.StatusNotFound {
		t.Errorf("an unknown client: %d %q", a.status, a.body)
	}
	// Revoking the client ends its tokens at once.
	a := h.revoke("client", client.ClientID, csrf, ts)
	if a.status != http.StatusSeeOther || a.header.Get("Location") != "/oauth/grants" {
		t.Fatalf("the revocation: %d %q %q", a.status, a.body, a.header.Get("Location"))
	}
	if _, err := h.oauth.Check(t.Context(), tok.AccessToken, "mcp"); err == nil {
		t.Error("the access token works after its client was revoked")
	}
	if len(h.revoked) != 1 || h.revoked[0] != "chani" || len(h.revokedClients[0]) != 1 || h.revokedClients[0][0] != client.ClientID {
		t.Errorf("OnRevoke was called with %+v, %+v", h.revoked, h.revokedClients)
	}
}

// TestOAuthRevokePrincipal checks that revoking a principal ends every
// client's tokens, and deletes its grants.
func TestOAuthRevokePrincipal(t *testing.T) {
	h := newOAuthHarness(t)
	client, tok := h.signin("pi-pocket", "mcp")
	other, otherTok := h.signin("pi-pocket-2", "mcp", "events")
	// A grant of the principal, which the revocation must delete.
	req, err := h.oauth.RequestAccess(t.Context(), client.ClientID, "marks", marksItems())
	if err != nil {
		t.Fatal(err)
	}
	_, reqCSRF, reqHash := h.consentPage(req.ID)
	if a := h.approve(req.ID, reqCSRF, reqHash, url.Values{"item": {"0", "1"},
		"path.0.folder": {"/Reading"}, "path.1.folder": {"/Reading/Inbox"}}); a.status != http.StatusSeeOther {
		t.Fatalf("the access approval: %d %q", a.status, a.body)
	}
	if granted, err := h.oauth.Grant(t.Context(), "chani", "marks"); err != nil || len(granted) != 2 {
		t.Fatalf("the grant before the revocation = %+v, %v", granted, err)
	}
	page, csrf, ts := h.grantsPage()
	if page.status != http.StatusOK {
		t.Fatalf("the grants page: %d %q", page.status, page.body)
	}
	a := h.revoke("principal", "chani", csrf, ts)
	if a.status != http.StatusSeeOther {
		t.Fatalf("revoking the principal: %d %q", a.status, a.body)
	}
	for name, tok := range map[string]tokenAnswer{"first": tok, "second": otherTok} {
		if _, err := h.oauth.Check(t.Context(), tok.AccessToken, "mcp"); err == nil {
			t.Errorf("the %s client's token works after its principal was revoked", name)
		}
	}
	if granted, err := h.oauth.Grant(t.Context(), "chani", "marks"); err != nil || len(granted) != 0 {
		t.Errorf("the grant after a revocation = %+v, %v", granted, err)
	}
	if len(h.revoked) != 1 || h.revoked[0] != "chani" || len(h.revokedClients[0]) != 2 {
		t.Errorf("OnRevoke was called with %+v, %+v", h.revoked, h.revokedClients)
	}
	// Its clients are gone, and so is the principal.
	if a := h.revoke("principal", "chani", csrf, ts); a.status != http.StatusNotFound {
		t.Errorf("revoking a revoked principal: %d %q", a.status, a.body)
	}
	if a := h.revoke("client", other.ClientID, csrf, ts); a.status != http.StatusNotFound {
		t.Errorf("revoking a revoked client: %d %q", a.status, a.body)
	}
}

// TestOAuthRemoveGrantItem checks the grants page's removal of 1 item of a
// grant through lodge: the page holds the item's form, its POST narrows the
// grant, and a form that names another mix of fields is 400.
func TestOAuthRemoveGrantItem(t *testing.T) {
	h := newOAuthHarness(t)
	client, _ := h.signin("pi-pocket", "mcp")
	items := marksItems()
	req, err := h.oauth.RequestAccess(t.Context(), client.ClientID, "marks", items)
	if err != nil {
		t.Fatal(err)
	}
	_, reqCSRF, reqHash := h.consentPage(req.ID)
	if a := h.approve(req.ID, reqCSRF, reqHash, url.Values{"item": {"0", "1"},
		"path.0.folder": {"/Reading"}, "path.1.folder": {"/Reading/Inbox"}}); a.status != http.StatusSeeOther {
		t.Fatalf("the access approval: %d %q", a.status, a.body)
	}
	granted, err := h.oauth.Grant(t.Context(), "chani", "marks")
	if err != nil || len(granted) != 2 {
		t.Fatalf("the grant = %+v, %v", granted, err)
	}
	hash := oauth.ItemHash(granted[0])
	page, csrf, ts := h.grantsPage()
	if !strings.Contains(page.body, `name="item" value="`+hash+`"`) {
		t.Fatalf("the grants page holds no form for the item %q", hash)
	}
	// A form that names another mix of fields is 400, and a wrong CSRF token
	// is 403.
	if a := h.do(h.req(http.MethodPost, "/oauth/grants/revoke",
		url.Values{"csrf": {csrf}, "ts": {ts}, "principal": {"chani"}, "app": {"marks"}}.Encode(), formHeaders())); a.status != http.StatusBadRequest {
		t.Errorf("an item without its hash: %d %q", a.status, a.body)
	}
	if a := h.do(h.req(http.MethodPost, "/oauth/grants/revoke",
		url.Values{"csrf": {"wrong"}, "ts": {ts}, "principal": {"chani"}, "app": {"marks"}, "item": {hash}}.Encode(), formHeaders())); a.status != http.StatusForbidden {
		t.Errorf("a bad CSRF token: %d %q", a.status, a.body)
	}
	if a := h.do(h.req(http.MethodPost, "/oauth/grants/revoke",
		url.Values{"csrf": {csrf}, "ts": {ts}, "principal": {"chani"}, "app": {"marks"}, "item": {strings.Repeat("0", 64)}}.Encode(), formHeaders())); a.status != http.StatusNotFound {
		t.Errorf("an unknown item: %d %q", a.status, a.body)
	}
	// The removal narrows the grant, and the hook runs.
	before := len(h.granted)
	if a := h.do(h.req(http.MethodPost, "/oauth/grants/revoke",
		url.Values{"csrf": {csrf}, "ts": {ts}, "principal": {"chani"}, "app": {"marks"}, "item": {hash}}.Encode(), formHeaders())); a.status != http.StatusSeeOther ||
		a.header.Get("Location") != "/oauth/grants" {
		t.Fatalf("the removal: %d %q %q", a.status, a.body, a.header.Get("Location"))
	}
	if got, err := h.oauth.Grant(t.Context(), "chani", "marks"); err != nil || len(got) != 1 || oauth.ItemHash(got[0]) == hash {
		t.Errorf("the grant after the removal = %+v, %v", got, err)
	}
	if len(h.granted) != before+1 || h.granted[len(h.granted)-1] != [2]string{"chani", "marks"} {
		t.Errorf("OnGrant was called with %+v, want 1 more call for chani and marks", h.granted)
	}
}

// TestOAuthHeaders checks the headers of both kinds of response.
func TestOAuthHeaders(t *testing.T) {
	h := newOAuthHarness(t)
	_, id, _, _ := h.pendingSignin()
	page := h.do(h.req(http.MethodGet, "/oauth/consent/"+id, "", pageHeaders()))
	want := map[string]string{
		"Content-Security-Policy":      oauth.PagePolicy("https://client.example"),
		"X-Frame-Options":              "DENY",
		"X-Content-Type-Options":       "nosniff",
		"Referrer-Policy":              "same-origin",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Permissions-Policy":           "camera=(), microphone=(), geolocation=()",
		"Cache-Control":                "no-store",
	}
	for k, v := range want {
		if got := page.header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if page.header.Get("Set-Cookie") != "" {
		t.Error("a consent response sets a cookie")
	}
	// An error carries them too.
	h.who.set(func(p *peer.Peer) { p.Login = "bob@example.com" })
	refused := h.do(h.req(http.MethodGet, "/oauth/grants", "", pageHeaders()))
	if refused.status != http.StatusForbidden {
		t.Fatalf("the refused page: %d", refused.status)
	}
	for k, v := range want {
		if k == "Content-Security-Policy" {
			v = oauth.PagePolicy("")
		}
		if got := refused.header.Get(k); got != v {
			t.Errorf("a refused page: %s = %q, want %q", k, got, v)
		}
	}
	h.who.set(func(p *peer.Peer) { p.Login = alice().Login })

	// An OAuth response: JSON, no-store, and neither a cookie nor a CORS
	// header.
	meta := h.do(h.req(http.MethodGet, "/.well-known/oauth-authorization-server", "", nil))
	if meta.header.Get("Cache-Control") != "no-store" || meta.header.Get("Content-Type") != "application/json" {
		t.Errorf("metadata headers: %v", meta.header)
	}
	for k := range meta.header {
		if strings.HasPrefix(strings.ToLower(k), "access-control-") || k == "Set-Cookie" ||
			k == "Cross-Origin-Resource-Policy" {
			t.Errorf("an OAuth response carries %s", k)
		}
	}
	// An OAuth request with an Origin is refused.
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/oauth/token", "/oauth/register"} {
		a := h.do(h.req(http.MethodGet, path, "", map[string]string{"Origin": lodgeOrigin}))
		if a.status != http.StatusForbidden || !strings.Contains(a.body, "browsers may not call") {
			t.Errorf("%s with an Origin: %d %q", path, a.status, a.body)
		}
		if a.header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s with an Origin: Cache-Control %q", path, a.header.Get("Cache-Control"))
		}
	}
	// The wrong method is 405 with Allow, and another Host is 403.
	if a := h.do(h.req(http.MethodGet, "/oauth/token", "", nil)); a.status != http.StatusMethodNotAllowed ||
		a.header.Get("Allow") != "POST" {
		t.Errorf("GET /oauth/token: %d %v", a.status, a.header)
	}
	other := h.req(http.MethodGet, "/.well-known/oauth-authorization-server", "", nil)
	other.Host = "other.example"
	if a := h.do(other); a.status != http.StatusForbidden {
		t.Errorf("another Host: %d %q", a.status, a.body)
	}
}

// TestOAuthTokensAreSeparate checks that an OAuth token is no MCP token, and
// an MCP token no OAuth token.
func TestOAuthTokensAreSeparate(t *testing.T) {
	h := newOAuthHarness(t)
	_, tok := h.signin("pi-pocket", "mcp")
	secret, err := h.tokens.Issue(t.Context(), "agent", "notes", "vault=1", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, "lgm_") {
		t.Fatalf("an MCP token is %q", secret)
	}
	if _, err := h.oauth.Check(t.Context(), secret, "mcp"); err == nil {
		t.Error("Check accepts an lgm_ token")
	}
	// An OAuth token is refused on /mcp/<id>.
	a := h.do(h.req(http.MethodPost, "/mcp/notes", "{}", map[string]string{
		"Authorization": "Bearer " + tok.AccessToken, "Content-Type": "application/json"}))
	if a.status != http.StatusUnauthorized || a.header.Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("an OAuth token on /mcp/notes: %d %q", a.status, a.body)
	}
}

// TestOAuthNoServer checks that lodge without an OAuth server serves none of
// its routes.
func TestOAuthNoServer(t *testing.T) {
	h := newOAuthHarness(t)
	h.g.cfg.OAuth = nil
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/oauth/register", "/oauth/grants"} {
		if a := h.do(h.req(http.MethodGet, path, "", nil)); a.status != http.StatusNotFound {
			t.Errorf("%s without an OAuth server: %d", path, a.status)
		}
	}
}

// TestOAuthAuthorizeRefusals checks the authorization endpoint's refusals: a
// scope lodge does not have, a resource that is not lodge's, and 64 pending
// requests.
func TestOAuthAuthorizeRefusals(t *testing.T) {
	h := newOAuthHarness(t)
	client := h.register("pi-pocket", redirectURI)
	scope := h.authorize(client.ClientID, redirectURI, map[string]string{"scope": "admin"})
	if !strings.Contains(scope.header.Get("Location"), "error=invalid_scope") {
		t.Errorf("scope=admin: %d %q %q", scope.status, scope.header.Get("Location"), scope.body)
	}
	resource := h.authorize(client.ClientID, redirectURI, map[string]string{"resource": "https://elsewhere.example/mcp"})
	if !strings.Contains(resource.header.Get("Location"), "error=invalid_request") {
		t.Errorf("another resource: %d %q %q", resource.status, resource.header.Get("Location"), resource.body)
	}
	// A state shorter than 8 characters is refused: it must be unguessable.
	short := h.authorize(client.ClientID, redirectURI, map[string]string{"state": "short"})
	if short.status != http.StatusSeeOther || !strings.Contains(short.header.Get("Location"), "error=") ||
		!strings.Contains(short.header.Get("Location"), "state=short") {
		t.Errorf("a short state: %d %q %q", short.status, short.header.Get("Location"), short.body)
	}
	// lodge's own resource is fine, at the issuer and below it.
	for _, r := range []string{lodgeOrigin, lodgeOrigin + "/gateway/mcp"} {
		if a := h.authorize(client.ClientID, redirectURI, map[string]string{"resource": r}); a.status != http.StatusSeeOther {
			t.Errorf("resource=%s: %d %q", r, a.status, a.body)
		}
	}
	// 64 requests pending, and the authorization answers 429 with one line.
	bound, _ := h.signin("pi-pocket", "mcp")
	var pending int
	if err := h.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM consent_requests WHERE decided_at IS NULL`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	for range oauth.MaxPending - pending {
		if _, err := h.oauth.RequestAccess(t.Context(), bound.ClientID, "marks",
			[]oauth.Item{{Cap: "marks:read"}}); err != nil {
			t.Fatal(err)
		}
	}
	a := h.authorize(client.ClientID, redirectURI, nil)
	if a.status != http.StatusTooManyRequests || strings.Count(strings.TrimSpace(a.body), "\n") != 0 {
		t.Errorf("a 65th pending request: %d %q", a.status, a.body)
	}
}

// TestOAuthRequestBodyLimit checks that a body over 16 KiB is refused.
func TestOAuthRequestBodyLimit(t *testing.T) {
	h := newOAuthHarness(t)
	a := h.postJSON("/oauth/register", `{"redirect_uris":["https://a.example/cb"],"padding":"`+
		strings.Repeat("p", 17<<10)+`"}`)
	if a.status != http.StatusBadRequest || !strings.Contains(a.body, "invalid_client_metadata") {
		t.Errorf("a body over 16 KiB: %d %q", a.status, a.body)
	}
}

// TestOAuthOneRedirectURI checks a client with one redirect URI that leaves
// it out of the authorization request: lodge answers that URI.
func TestOAuthOneRedirectURI(t *testing.T) {
	h := newOAuthHarness(t)
	client := h.register("pi-pocket", redirectURI)
	q := url.Values{"client_id": {client.ClientID}, "response_type": {"code"}, "scope": {"mcp"},
		"state": {"st-1234567890"}, "code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}}
	a := h.do(h.req(http.MethodGet, "/oauth/authorize?"+q.Encode(), "", nil))
	if a.status != http.StatusSeeOther || !strings.HasPrefix(a.header.Get("Location"), "/oauth/consent/") {
		t.Fatalf("authorize without redirect_uri: %d %q %q", a.status, a.header.Get("Location"), a.body)
	}
	id := consentID(t, a)
	page, csrf, hash := h.consentPage(id)
	if page.status != http.StatusOK || !strings.Contains(page.body, "https://client.example") {
		t.Fatalf("the page: %d %q", page.status, page.body)
	}
	approved := h.approve(id, csrf, hash, url.Values{"principal": {"chani"}, "scope": {"mcp"}})
	if !strings.HasPrefix(approved.header.Get("Location"), redirectURI) || code(t, approved) == "" {
		t.Errorf("the approval's Location %q", approved.header.Get("Location"))
	}
}

// TestOAuthConsentHead checks that a HEAD of the pages answers their
// headers, and no body.
func TestOAuthConsentHead(t *testing.T) {
	h := newOAuthHarness(t)
	_, id, _, _ := h.pendingSignin()
	a := h.do(h.req(http.MethodHead, "/oauth/consent/"+id, "", pageHeaders()))
	if a.status != http.StatusOK || a.body != "" || a.header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("HEAD of a consent page: %d %q %v", a.status, a.body, a.header)
	}
	g := h.do(h.req(http.MethodHead, "/oauth/grants", "", pageHeaders()))
	if g.status != http.StatusOK || g.body != "" {
		t.Errorf("HEAD of the grants page: %d %q", g.status, g.body)
	}
	// The method table holds: a GET of the revocation route is 405.
	if a := h.do(h.req(http.MethodGet, "/oauth/grants/revoke", "", pageHeaders())); a.status != http.StatusMethodNotAllowed ||
		a.header.Get("Allow") != "POST" {
		t.Errorf("GET /oauth/grants/revoke: %d %v", a.status, a.header)
	}
}

// TestOAuthAuthorizeIsAConsentRoute checks that /oauth/authorize runs the
// consent checks: an admin's allowed login, lodge's Origin, and the fetch
// metadata, with the consent headers on its answers
// (docs/specs/lodge-http.md, OAuth routes).
func TestOAuthAuthorizeIsAConsentRoute(t *testing.T) {
	h := newOAuthHarness(t)
	client := h.register("pi-pocket", redirectURI)
	q := url.Values{"client_id": {client.ClientID}, "redirect_uri": {redirectURI}, "response_type": {"code"},
		"scope": {"mcp"}, "state": {"st-1234567890"}, "code_challenge": {challenge(verifier)},
		"code_challenge_method": {"S256"}}.Encode()
	line := "login alice@example.com is not an admin: lodgectl allow admin alice@example.com"

	// A login that is not an admin.
	if err := h.allow.DisallowAdmin(t.Context(), "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	a := h.do(h.req(http.MethodGet, "/oauth/authorize?"+q, "", pageHeaders()))
	if a.status != http.StatusForbidden || !strings.Contains(a.body, line) {
		t.Errorf("a login that is not an admin: %d %q", a.status, a.body)
	}
	if a.header.Get("Referrer-Policy") != "same-origin" || a.header.Get("Content-Security-Policy") != oauth.PagePolicy("") {
		t.Errorf("a refused authorization's headers: %v", a.header)
	}
	if err := h.allow.AllowAdmin(t.Context(), "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	// A login that is not allowed at all.
	h.who.set(func(p *peer.Peer) { p.Login = "bob@example.com" })
	if a := h.do(h.req(http.MethodGet, "/oauth/authorize?"+q, "", pageHeaders())); a.status != http.StatusForbidden ||
		!strings.Contains(a.body, "login bob@example.com is not allowed") {
		t.Errorf("a login that is not allowed: %d %q", a.status, a.body)
	}
	h.who.set(func(p *peer.Peer) { p.Login = alice().Login })
	// Another site's Origin.
	a = h.do(h.req(http.MethodGet, "/oauth/authorize?"+q, "",
		map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site",
			"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}))
	if a.status != http.StatusForbidden || !strings.Contains(a.body, "requests from another origin are refused") {
		t.Errorf("another site's Origin: %d %q", a.status, a.body)
	}
	// A top-level navigation from another site passes, with the consent
	// headers on its redirect.
	nav := h.do(h.req(http.MethodGet, "/oauth/authorize?"+q, "",
		map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}))
	if nav.status != http.StatusSeeOther || !strings.HasPrefix(nav.header.Get("Location"), "/oauth/consent/") {
		t.Fatalf("a top-level navigation: %d %q %q", nav.status, nav.header.Get("Location"), nav.body)
	}
	want := map[string]string{
		"Content-Security-Policy":      oauth.PagePolicy(""),
		"X-Frame-Options":              "DENY",
		"X-Content-Type-Options":       "nosniff",
		"Referrer-Policy":              "same-origin",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Permissions-Policy":           "camera=(), microphone=(), geolocation=()",
		"Cache-Control":                "no-store",
	}
	for k, v := range want {
		if got := nav.header.Get(k); got != v {
			t.Errorf("the authorization's %s = %q, want %q", k, got, v)
		}
	}
	// A same-origin request with no fetch metadata passes too.
	if a := h.do(h.req(http.MethodGet, "/oauth/authorize?"+q, "", map[string]string{"Origin": lodgeOrigin})); a.status != http.StatusSeeOther {
		t.Errorf("a same-origin GET: %d %q", a.status, a.body)
	}
}

// TestOAuthApproveAfterClientRevoked checks that a sign-in whose client was
// revoked cannot be approved: revoking a client deletes its pending
// requests, so the decision is 404.
func TestOAuthApproveAfterClientRevoked(t *testing.T) {
	h := newOAuthHarness(t)
	client, id, csrf, hash := h.pendingSignin()
	if err := h.oauth.RevokeClient(t.Context(), client); err != nil {
		t.Fatal(err)
	}
	a := h.approve(id, csrf, hash, url.Values{"principal": {"chani"}, "scope": {"mcp"}})
	if a.status != http.StatusNotFound {
		t.Errorf("an approval after the client was revoked: %d %q", a.status, a.body)
	}
	// Nor can a request whose authorization request lodge cannot rebuild be
	// approved: 400, and it stays pending.
	_, id, csrf, hash = h.pendingSignin()
	if _, err := h.db.ExecContext(t.Context(),
		`UPDATE consent_requests SET query = '{"client_id":["lgc_gone"]}' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	a = h.approve(id, csrf, hash, url.Values{"principal": {"chani"}, "scope": {"mcp"}})
	if a.status != http.StatusBadRequest || !strings.Contains(a.body, "cannot be answered") {
		t.Errorf("a request lodge cannot rebuild: %d %q", a.status, a.body)
	}
	var decided sql.NullString
	if err := h.db.QueryRowContext(t.Context(), `SELECT decided_at FROM consent_requests WHERE id = ?`, id).Scan(&decided); err != nil {
		t.Fatal(err)
	}
	if decided.Valid {
		t.Errorf("the request was decided at %s, though the approval was refused", decided.String)
	}
}

// boundTo is the principal a client is bound to, and whether one is.
func (h *oauthHarness) boundTo(clientID string) (string, bool) {
	h.t.Helper()
	var principal sql.NullString
	if err := h.db.QueryRowContext(h.t.Context(), `SELECT principal FROM oauth_clients WHERE id = ?`,
		clientID).Scan(&principal); err != nil {
		h.t.Fatal(err)
	}
	return principal.String, principal.Valid
}

// decidedAt is when a request was decided, or the zero time while it is
// pending.
func (h *oauthHarness) decidedAt(id string) time.Time {
	h.t.Helper()
	var decided sql.NullString
	if err := h.db.QueryRowContext(h.t.Context(), `SELECT decided_at FROM consent_requests WHERE id = ?`,
		id).Scan(&decided); err != nil {
		h.t.Fatal(err)
	}
	return parseStamp(h.t, decided.String)
}

// TestOAuthSigninBindingRace checks that a client is bound by its first
// approval and never rebound: the second sign-in of 1 client answers 409,
// and stays pending.
func TestOAuthSigninBindingRace(t *testing.T) {
	h := newOAuthHarness(t)
	client := h.register("pi-pocket", redirectURI)
	_, first, csrf1, hash1 := h.pendingSigninOf(t, client.ClientID)
	_, second, csrf2, hash2 := h.pendingSigninOf(t, client.ClientID)
	ok := h.approve(first, csrf1, hash1, url.Values{"principal": {"alice"}, "scope": {"mcp"}})
	if ok.status != http.StatusSeeOther || code(t, ok) == "" {
		t.Fatalf("the first approval: %d %q", ok.status, ok.body)
	}
	if bound, set := h.boundTo(client.ClientID); !set || bound != "alice" {
		t.Fatalf("the client is bound to %q (%v), want alice", bound, set)
	}
	// The second sign-in's page cannot be decided either way, and its
	// approval is 409: the client stays with alice, and no code is issued.
	page := h.do(h.req(http.MethodGet, "/oauth/consent/"+second, "", pageHeaders()))
	if page.status != http.StatusConflict ||
		!strings.Contains(page.body, "client "+client.ClientID+" is bound to principal alice: decline this request") {
		t.Errorf("the second sign-in's page: %d %q", page.status, page.body)
	}
	a := h.approve(second, csrf2, hash2, url.Values{"principal": {"bob"}, "scope": {"mcp"}})
	if a.status != http.StatusConflict ||
		!strings.Contains(a.body, "client "+client.ClientID+" is bound to principal alice: decline this request") {
		t.Fatalf("the second approval: %d %q", a.status, a.body)
	}
	if a.header.Get("Location") != "" {
		t.Errorf("the second approval redirects to %q", a.header.Get("Location"))
	}
	if bound, _ := h.boundTo(client.ClientID); bound != "alice" {
		t.Errorf("the client is bound to %q after the second approval", bound)
	}
	if !h.decidedAt(second).IsZero() {
		t.Error("the second request was decided, though its approval was refused")
	}
}

// pendingSigninOf makes a sign-in request of client clientID that waits for
// a decision.
func (h *oauthHarness) pendingSigninOf(t *testing.T, clientID string) (string, string, string, string) {
	t.Helper()
	a := h.authorize(clientID, redirectURI, map[string]string{"scope": "mcp"})
	if a.status != http.StatusSeeOther {
		t.Fatalf("authorize: %d %q %q", a.status, a.header.Get("Location"), a.body)
	}
	id := consentID(t, a)
	_, csrf, hash := h.consentPage(id)
	return clientID, id, csrf, hash
}

// parseStamp reads a stored time, or the zero time.
func parseStamp(t *testing.T, text string) time.Time {
	t.Helper()
	if text == "" {
		return time.Time{}
	}
	at, err := time.Parse(store.TimeFormat, text)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// TestOAuthApproveRefusals checks the forms an approval refuses: 400, with
// the request still pending and nothing changed.
func TestOAuthApproveRefusals(t *testing.T) {
	h := newOAuthHarness(t)
	client := h.register("pi-pocket", redirectURI)
	_, signin, csrf, hash := h.pendingSigninOf(t, client.ClientID)
	for name, fields := range map[string]url.Values{
		"a principal that is not a name": {"principal": {"Bad Name"}, "scope": {"mcp"}},
		"no scope":                       {"principal": {"chani"}},
		"a scope not asked for":          {"principal": {"chani"}, "scope": {"events"}},
		"a scope asked and another":      {"principal": {"chani"}, "scope": {"mcp", "events"}},
	} {
		a := h.approve(signin, csrf, hash, fields)
		if a.status != http.StatusBadRequest {
			t.Errorf("%s: %d %q, want 400", name, a.status, a.body)
		}
		if !h.decidedAt(signin).IsZero() {
			t.Errorf("%s: the request was decided", name)
		}
		if bound, set := h.boundTo(client.ClientID); set {
			t.Errorf("%s: the client is bound to %q", name, bound)
		}
	}
	// An access request's items.
	bound, _ := h.signin("pi-pocket", "mcp")
	for name, fields := range map[string]url.Values{
		"no item":                  {},
		"an item not there":        {"item": {"9"}},
		"an item that is no index": {"item": {"x"}},
		"no item but a path":       {"path.0.folder": {"/Reading"}},
	} {
		req, err := h.oauth.RequestAccess(t.Context(), bound.ClientID, "marks", marksItems())
		if err != nil {
			t.Fatal(err)
		}
		_, reqCSRF, reqHash := h.consentPage(req.ID)
		a := h.approve(req.ID, reqCSRF, reqHash, fields)
		if a.status != http.StatusBadRequest {
			t.Errorf("%s: %d %q, want 400", name, a.status, a.body)
		}
		if !h.decidedAt(req.ID).IsZero() {
			t.Errorf("%s: the request was decided", name)
		}
	}
	if granted, err := h.oauth.Grant(t.Context(), "chani", "marks"); err != nil || len(granted) != 0 {
		t.Errorf("the grant after refused approvals = %+v, %v", granted, err)
	}
}

// TestOAuthRegisterLimitRace checks that the limit on unbound clients holds
// when registrations come at once: exactly 1 gets past it.
func TestOAuthRegisterLimitRace(t *testing.T) {
	h := newOAuthHarness(t)
	for i := range oauth.MaxUnboundClients - 1 {
		if _, err := h.db.ExecContext(t.Context(),
			`INSERT INTO oauth_clients (id, name, redirect_uris, registered_at) VALUES (?, 'c', '["https://a.example/cb"]', ?)`,
			"lgc_bulk"+strconv.Itoa(i), time.Now().UTC().Format(store.TimeFormat)); err != nil {
			t.Fatal(err)
		}
	}
	statuses := make([]int, 8)
	errs := make([]error, len(statuses))
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Go(func() {
			body := `{"redirect_uris":["https://a.example/cb"]}`
			r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.lodge.URL+"/oauth/register",
				strings.NewReader(body))
			if err != nil {
				errs[i] = err
				return
			}
			r.Host = lodgeHost
			r.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultTransport.RoundTrip(r)
			if err != nil {
				errs[i] = err
				return
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			statuses[i] = resp.StatusCode
		})
	}
	wg.Wait()
	created, limited := 0, 0
	for i, status := range statuses {
		if errs[i] != nil {
			t.Fatalf("registration %d: %v", i, errs[i])
		}
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusTooManyRequests:
			limited++
		default:
			t.Errorf("registration %d answered %d", i, status)
		}
	}
	if created != 1 || limited != len(statuses)-1 {
		t.Errorf("%d registrations were created and %d limited, want 1 and %d", created, limited, len(statuses)-1)
	}
	var unbound int
	if err := h.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM oauth_clients WHERE principal IS NULL AND revoked_at IS NULL`).Scan(&unbound); err != nil {
		t.Fatal(err)
	}
	if unbound != oauth.MaxUnboundClients {
		t.Errorf("%d unbound clients, want %d", unbound, oauth.MaxUnboundClients)
	}
}

// concurrent runs fn n times at once, and returns what each returned.
func concurrent(t *testing.T, n int, fn func(i int) (answer, error)) []answer {
	t.Helper()
	out := make([]answer, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			out[i], errs[i] = fn(i)
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	return out
}

// TestOAuthRefreshSingleUse checks that 2 refreshes with 1 refresh token at
// once give at most 1 pair, and that no token of the family works after.
func TestOAuthRefreshSingleUse(t *testing.T) {
	h := newOAuthHarness(t)
	client, first := h.signin("pi-pocket", "mcp")
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {client.ClientID},
		"refresh_token": {first.RefreshToken}}.Encode()
	answers := concurrent(t, 2, func(int) (answer, error) {
		r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.lodge.URL+"/oauth/token",
			strings.NewReader(form))
		if err != nil {
			return answer{}, err
		}
		r.Host = lodgeHost
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			return answer{}, err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return answer{}, err
		}
		return answer{resp.StatusCode, resp.Header, string(b)}, nil
	})
	pairs := 0
	var winner tokenAnswer
	for _, a := range answers {
		tok := decodeToken(t, a)
		if a.status == http.StatusOK && tok.AccessToken != "" && tok.RefreshToken != "" {
			pairs++
			winner = tok
			continue
		}
		tokenError(t, a, tok, "invalid_grant")
	}
	if pairs != 1 {
		t.Fatalf("%d refreshes got a pair, want 1", pairs)
	}
	// The reuse revokes the family: none of its tokens works.
	if _, err := h.oauth.Check(t.Context(), winner.AccessToken, "mcp"); err == nil {
		t.Error("the winner's access token works after the refresh token was used twice")
	}
	twice, twiceTok := h.refresh(client.ClientID, winner.RefreshToken)
	tokenError(t, twice, twiceTok, "invalid_grant")
	if _, err := h.oauth.Check(t.Context(), first.AccessToken, "mcp"); err == nil {
		t.Error("the first access token works after the reuse")
	}
}

// TestOAuthCodeSingleUse checks that 2 exchanges of 1 code at once give at
// most 1 pair, and that no token of the family works after.
func TestOAuthCodeSingleUse(t *testing.T) {
	h := newOAuthHarness(t)
	client := h.register("pi-pocket", redirectURI)
	a := h.authorize(client.ClientID, redirectURI, map[string]string{"scope": "mcp"})
	id := consentID(t, a)
	_, csrf, hash := h.consentPage(id)
	approved := h.approve(id, csrf, hash, url.Values{"principal": {"chani"}, "scope": {"mcp"}})
	if approved.status != http.StatusSeeOther {
		t.Fatalf("the approval: %d %q", approved.status, approved.body)
	}
	code := code(t, approved)
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {client.ClientID},
		"code": {code}, "redirect_uri": {redirectURI}, "code_verifier": {verifier}}.Encode()
	answers := concurrent(t, 2, func(int) (answer, error) {
		r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.lodge.URL+"/oauth/token",
			strings.NewReader(form))
		if err != nil {
			return answer{}, err
		}
		r.Host = lodgeHost
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			return answer{}, err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return answer{}, err
		}
		return answer{resp.StatusCode, resp.Header, string(b)}, nil
	})
	pairs := 0
	var winner tokenAnswer
	for _, a := range answers {
		tok := decodeToken(t, a)
		if a.status == http.StatusOK && tok.AccessToken != "" && tok.RefreshToken != "" {
			pairs++
			winner = tok
			continue
		}
		tokenError(t, a, tok, "invalid_grant")
	}
	if pairs != 1 {
		t.Fatalf("%d exchanges got a pair, want 1", pairs)
	}
	// The code was exchanged twice: the family is revoked, the pair too.
	if _, err := h.oauth.Check(t.Context(), winner.AccessToken, "mcp"); err == nil {
		t.Error("the winner's access token works after the code was exchanged twice")
	}
	twice, twiceTok := h.refresh(client.ClientID, winner.RefreshToken)
	tokenError(t, twice, twiceTok, "invalid_grant")
}

// TestOAuthRevokeAccessRevokesFamily checks RFC 7009, section 2.1: revoking
// an access token revokes its family too.
func TestOAuthRevokeAccessRevokesFamily(t *testing.T) {
	h := newOAuthHarness(t)
	client, tok := h.signin("pi-pocket", "mcp")
	a := h.post("/oauth/revoke", url.Values{"client_id": {client.ClientID}, "token": {tok.AccessToken},
		"token_type_hint": {"access_token"}})
	if a.status != http.StatusOK {
		t.Fatalf("revoke: %d %q", a.status, a.body)
	}
	if _, err := h.oauth.Check(t.Context(), tok.AccessToken, "mcp"); err == nil {
		t.Error("the access token works after it was revoked")
	}
	after, afterTok := h.refresh(client.ClientID, tok.RefreshToken)
	tokenError(t, after, afterTok, "invalid_grant")
}

// TestOAuthSweepKeepsReplayEvidence checks that the sweep keeps a consumed
// refresh token while its family holds an unexpired one: presenting it again
// revokes the family.
func TestOAuthSweepKeepsReplayEvidence(t *testing.T) {
	h := newOAuthHarness(t)
	client, first := h.signin("pi-pocket", "mcp")
	_, second := h.refresh(client.ClientID, first.RefreshToken)
	// The family's 2 refresh rows: the one the refresh consumed, and the one
	// it issued. Their expiries are the harness clock's, which is the clock
	// the sweep reads.
	now := h.clock.now().UTC()
	oldExpiry, newExpiry := now.Add(time.Hour), now.Add(3*time.Hour)
	oldRow := `UPDATE oauth_sessions SET expires_at = ? WHERE kind = 'refresh' AND active = 0`
	if _, err := h.db.ExecContext(t.Context(), oldRow, oldExpiry.Format(store.TimeFormat)); err != nil {
		t.Fatal(err)
	}
	newRow := `UPDATE oauth_sessions SET expires_at = ? WHERE kind = 'refresh' AND active = 1`
	if _, err := h.db.ExecContext(t.Context(), newRow, newExpiry.Format(store.TimeFormat)); err != nil {
		t.Fatal(err)
	}
	// The sweep runs 2 hours in: the consumed row has expired, the live one
	// has not.
	h.clock.advance(2 * time.Hour)
	sweepAt := h.clock.now().UTC()
	if !oldExpiry.Before(sweepAt) || !sweepAt.Before(newExpiry) {
		t.Fatalf("the sweep at %s does not fall between %s and %s", sweepAt, oldExpiry, newExpiry)
	}
	if err := h.oauth.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The consumed row is still there, though it expired: it is the evidence
	// that revokes the family.
	var rows int
	if err := h.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM oauth_sessions WHERE kind = 'refresh' AND active = 0`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("%d consumed refresh rows after the sweep, want 1", rows)
	}
	// Presenting it again is a reuse, and the family goes.
	replayed, replayedTok := h.refresh(client.ClientID, first.RefreshToken)
	tokenError(t, replayed, replayedTok, "invalid_grant")
	descendant, descendantTok := h.refresh(client.ClientID, second.RefreshToken)
	tokenError(t, descendant, descendantTok, "invalid_grant")
}

// TestOAuthRevokePrincipalThenRevive checks that revoking a principal ends
// every client of it, and that approving the name again starts with no grant
// and no working old token.
func TestOAuthRevokePrincipalThenRevive(t *testing.T) {
	h := newOAuthHarness(t)
	first, firstTok := h.signin("pi-pocket", "mcp")
	second, secondTok := h.signin("pi-pocket-2", "mcp")
	// A grant of the principal, which a revocation deletes.
	req, err := h.oauth.RequestAccess(t.Context(), first.ClientID, "marks", marksItems())
	if err != nil {
		t.Fatal(err)
	}
	_, csrf, hash := h.consentPage(req.ID)
	if a := h.approve(req.ID, csrf, hash, url.Values{"item": {"0"},
		"path.0.folder": {"/Reading"}}); a.status != http.StatusSeeOther {
		t.Fatalf("the access approval: %d %q", a.status, a.body)
	}
	if err := h.oauth.RevokePrincipal(t.Context(), "chani"); err != nil {
		t.Fatal(err)
	}
	// Every client of the principal is revoked, at once.
	for _, id := range []string{first.ClientID, second.ClientID} {
		var revoked sql.NullString
		if err := h.db.QueryRowContext(t.Context(), `SELECT revoked_at FROM oauth_clients WHERE id = ?`, id).Scan(&revoked); err != nil {
			t.Fatal(err)
		}
		if !revoked.Valid {
			t.Errorf("client %s is not revoked", id)
		}
	}
	// Approving the name again revives it, with no grant, and the old tokens
	// stay refused.
	_, thirdTok := h.signin("pi-pocket-3", "mcp")
	if _, err := h.oauth.Check(t.Context(), thirdTok.AccessToken, "mcp"); err != nil {
		t.Errorf("the revived principal's new token: %v", err)
	}
	for name, old := range map[string]struct {
		client string
		tok    tokenAnswer
	}{"first": {first.ClientID, firstTok}, "second": {second.ClientID, secondTok}} {
		if _, err := h.oauth.Check(t.Context(), old.tok.AccessToken, "mcp"); err == nil {
			t.Errorf("the %s client's old token works after the principal was revoked", name)
		}
		if a, _ := h.refresh(old.client, old.tok.RefreshToken); a.status == http.StatusOK {
			t.Errorf("the %s client's old refresh token works after the principal was revoked", name)
		}
	}
	if granted, err := h.oauth.Grant(t.Context(), "chani", "marks"); err != nil || len(granted) != 0 {
		t.Errorf("the grant after a revival = %+v, %v", granted, err)
	}
}

// TestOAuthLegacyTokensWithOAuth checks that an lgm_ token still reaches an
// app through /mcp/<id> while lodge serves OAuth: the tokens serve their own
// routes.
func TestOAuthLegacyTokensWithOAuth(t *testing.T) {
	h := newOAuthHarness(t)
	if _, tok := h.signin("pi-pocket", "mcp"); tok.AccessToken == "" {
		t.Fatal("no OAuth access token")
	}
	secret, err := h.tokens.Issue(t.Context(), "agent", "notes", "vault=1", "")
	if err != nil {
		t.Fatal(err)
	}
	a := h.do(h.req(http.MethodPost, "/mcp/notes", "{}", map[string]string{
		"Authorization": "Bearer " + secret, "Content-Type": "application/json"}))
	if a.status != http.StatusOK || a.body != "app /mcp" {
		t.Fatalf("an lgm_ token on /mcp/notes: %d %q", a.status, a.body)
	}
	seen := h.notes.last(t)
	switch {
	case seen.Path != "/mcp":
		t.Errorf("the app got the path %q", seen.Path)
	case seen.Header.Get("Lodge-Caller") != "agent":
		t.Errorf("Lodge-Caller = %q", seen.Header.Get("Lodge-Caller"))
	case seen.Header.Get("Lodge-Token") != "agent":
		t.Errorf("Lodge-Token = %q", seen.Header.Get("Lodge-Token"))
	case seen.Header.Get("Lodge-Scope") != "vault=1":
		t.Errorf("Lodge-Scope = %q", seen.Header.Get("Lodge-Scope"))
	case seen.Header.Get("Authorization") != "":
		t.Error("the app got the token's Authorization header")
	}
	// The OAuth token is still refused there.
	b := h.do(h.req(http.MethodPost, "/mcp/notes", "{}", map[string]string{
		"Authorization": "Bearer " + h.signinToken(t), "Content-Type": "application/json"}))
	if b.status != http.StatusUnauthorized {
		t.Errorf("an OAuth token on /mcp/notes: %d %q", b.status, b.body)
	}
}

// signinToken makes a new sign-in, and returns its access token.
func (h *oauthHarness) signinToken(t *testing.T) string {
	t.Helper()
	_, tok := h.signin("pi-pocket-token", "mcp")
	return tok.AccessToken
}

// TestOAuthWrongHash checks a decision whose hash is not the request's: it
// changes nothing, and a valid decision afterwards still works.
func TestOAuthWrongHash(t *testing.T) {
	h := newOAuthHarness(t)
	client := h.register("pi-pocket", redirectURI)
	_, id, csrf, hash := h.pendingSigninOf(t, client.ClientID)
	a := h.approve(id, csrf, "not-the-hash", url.Values{"principal": {"chani"}, "scope": {"mcp"}})
	if a.status != http.StatusConflict || !strings.Contains(a.body, "the page shows another request") {
		t.Fatalf("a wrong hash: %d %q", a.status, a.body)
	}
	// Nothing changed: pending, and the client unbound.
	if !h.decidedAt(id).IsZero() {
		t.Error("the request was decided")
	}
	if bound, set := h.boundTo(client.ClientID); set {
		t.Errorf("the client is bound to %q after a refused hash", bound)
	}
	if a := h.approve(id, csrf, hash, url.Values{"principal": {"chani"}, "scope": {"mcp"}}); a.status != http.StatusSeeOther || code(t, a) == "" {
		t.Errorf("the valid approval after a wrong hash: %d %q", a.status, a.body)
	}
	if bound, _ := h.boundTo(client.ClientID); bound != "chani" {
		t.Errorf("the client is bound to %q", bound)
	}
}

// TestOAuthScopeNarrowing checks that an approval of fewer scopes than the
// request asked for gives a token with only the approved one.
func TestOAuthScopeNarrowing(t *testing.T) {
	h := newOAuthHarness(t)
	client := h.register("pi-pocket", redirectURI)
	a := h.authorize(client.ClientID, redirectURI, map[string]string{"scope": "mcp events"})
	if a.status != http.StatusSeeOther {
		t.Fatalf("authorize: %d %q %q", a.status, a.header.Get("Location"), a.body)
	}
	id := consentID(t, a)
	page, csrf, hash := h.consentPage(id)
	if page.status != http.StatusOK {
		t.Fatalf("the page: %d %q", page.status, page.body)
	}
	// Both scopes are offered, and the admin keeps mcp.
	for _, sc := range []string{"mcp", "events"} {
		if !strings.Contains(page.body, `name="scope" value="`+sc+`"`) {
			t.Errorf("the page does not offer the scope %s", sc)
		}
	}
	approved := h.approve(id, csrf, hash, url.Values{"principal": {"chani"}, "scope": {"mcp"}})
	if approved.status != http.StatusSeeOther {
		t.Fatalf("the approval: %d %q", approved.status, approved.body)
	}
	ans, tok := h.exchange(client.ClientID, redirectURI, code(t, approved), verifier)
	if ans.status != http.StatusOK {
		t.Fatalf("the exchange: %d %q", ans.status, ans.body)
	}
	if tok.Scope != "mcp" {
		t.Errorf("the token's scope = %q, want mcp", tok.Scope)
	}
	if _, err := h.oauth.Check(t.Context(), tok.AccessToken, "mcp"); err != nil {
		t.Errorf("Check(mcp) = %v", err)
	}
	if _, err := h.oauth.Check(t.Context(), tok.AccessToken, "events"); err == nil {
		t.Error("Check(events) passes for a token the consent did not grant it")
	}
}

// postConsent sends a decision, and returns its error instead of failing the
// test: 2 of them run at once.
func (h *oauthHarness) postConsent(id, csrf, hash string, fields url.Values) (answer, error) {
	form := url.Values{"csrf": {csrf}, "hash": {hash}, "decision": {"approve"}}
	for k, v := range fields {
		form[k] = v
	}
	r, err := http.NewRequestWithContext(h.t.Context(), http.MethodPost, h.lodge.URL+"/oauth/consent/"+id,
		strings.NewReader(form.Encode()))
	if err != nil {
		return answer{}, err
	}
	r.Host = lodgeHost
	for k, v := range formHeaders() {
		r.Header.Set(k, v)
	}
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		return answer{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return answer{}, err
	}
	return answer{resp.StatusCode, resp.Header, string(b)}, nil
}

// TestOAuthConcurrentBinding checks that 2 approvals of 1 unbound client,
// both past the unbound read, bind it once: 1 code is issued, and the other
// approval answers 409.
func TestOAuthConcurrentBinding(t *testing.T) {
	var (
		mu      sync.Mutex
		arrived int
	)
	release := make(chan struct{})
	h := newOAuthHarnessWith(t, func(o *oauth.Options) {
		o.BeforeDecision = func() {
			mu.Lock()
			arrived++
			if arrived == 2 {
				close(release)
			}
			mu.Unlock()
			// A watchdog, so that a missing approval fails rather than hangs.
			select {
			case <-release:
			case <-time.After(20 * time.Second):
				t.Error("the second approval did not come")
			}
		}
	})
	client := h.register("pi-pocket", redirectURI)
	_, first, csrf1, hash1 := h.pendingSigninOf(t, client.ClientID)
	_, second, csrf2, hash2 := h.pendingSigninOf(t, client.ClientID)
	answers := make([]answer, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Go(func() {
		answers[0], errs[0] = h.postConsent(first, csrf1, hash1, url.Values{"principal": {"alice"}, "scope": {"mcp"}})
	})
	wg.Go(func() {
		answers[1], errs[1] = h.postConsent(second, csrf2, hash2, url.Values{"principal": {"bob"}, "scope": {"mcp"}})
	})
	wg.Wait()
	codes, conflicts := 0, 0
	loser := ""
	for i, a := range answers {
		if errs[i] != nil {
			t.Fatalf("approval %d: %v", i, errs[i])
		}
		switch a.status {
		case http.StatusSeeOther:
			if code(t, a) == "" {
				t.Errorf("approval %d redirects without a code: %q", i, a.header.Get("Location"))
			}
			codes++
		case http.StatusConflict:
			if !strings.Contains(a.body, "is bound to principal") || !strings.Contains(a.body, "decline this request") {
				t.Errorf("the losing approval: %d %q", a.status, a.body)
			}
			conflicts++
			loser = []string{first, second}[i]
		default:
			t.Errorf("approval %d: %d %q", i, a.status, a.body)
		}
	}
	if codes != 1 || conflicts != 1 {
		t.Fatalf("%d codes and %d conflicts, want 1 and 1", codes, conflicts)
	}
	// One binding, and no rebind.
	bound, set := h.boundTo(client.ClientID)
	if !set || (bound != "alice" && bound != "bob") {
		t.Fatalf("the client is bound to %q (%v)", bound, set)
	}
	var codeRows int
	if err := h.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM oauth_sessions WHERE kind = 'code' AND client_id = ?`, client.ClientID).Scan(&codeRows); err != nil {
		t.Fatal(err)
	}
	if codeRows != 1 {
		t.Errorf("%d codes were stored for the client, want 1", codeRows)
	}
	// The request of the losing approval stays pending.
	if !h.decidedAt(loser).IsZero() {
		t.Error("the losing approval decided its request")
	}
}

// TestOAuthAccessAfterRevocation checks that an access request whose client
// was revoked cannot be approved, and that the grant it would have made
// changes nothing.
func TestOAuthAccessAfterRevocation(t *testing.T) {
	h := newOAuthHarness(t)
	client, _ := h.signin("pi-pocket", "mcp")
	// One approved request, which stays as it is.
	first, err := h.oauth.RequestAccess(t.Context(), client.ClientID, "marks",
		[]oauth.Item{{Cap: "marks:read", Sel: map[string]string{"folder": "/Reading"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, firstCSRF, firstHash := h.consentPage(first.ID)
	if a := h.approve(first.ID, firstCSRF, firstHash, url.Values{"item": {"0"},
		"path.0.folder": {"/Reading"}}); a.status != http.StatusSeeOther {
		t.Fatalf("the first approval: %d %q", a.status, a.body)
	}
	before, err := h.oauth.Grant(t.Context(), "chani", "marks")
	if err != nil || len(before) != 1 {
		t.Fatalf("the grant = %+v, %v", before, err)
	}
	// A second request, whose client is revoked: the revocation deletes the
	// request, so the decision is 404.
	second, err := h.oauth.RequestAccess(t.Context(), client.ClientID, "marks", marksItems())
	if err != nil {
		t.Fatal(err)
	}
	_, csrf, hash := h.consentPage(second.ID)
	if err := h.oauth.RevokeClient(t.Context(), client.ClientID); err != nil {
		t.Fatal(err)
	}
	if a := h.approve(second.ID, csrf, hash, url.Values{"item": {"0"}, "path.0.folder": {"/Reading"}}); a.status != http.StatusNotFound {
		t.Errorf("the approval after the client was revoked: %d %q", a.status, a.body)
	}
	if after, err := h.oauth.Grant(t.Context(), "chani", "marks"); err != nil || len(after) != 1 {
		t.Errorf("the grant after the refused approval = %+v, %v", after, err)
	}

	// The approval's own re-check: a client revoked behind the cascade's
	// back leaves its request, and the approval refuses it.
	other, _ := h.signin("pi-pocket-2", "mcp")
	third, err := h.oauth.RequestAccess(t.Context(), other.ClientID, "marks", marksItems())
	if err != nil {
		t.Fatal(err)
	}
	_, thirdCSRF, thirdHash := h.consentPage(third.ID)
	if _, err := h.db.ExecContext(t.Context(), `UPDATE oauth_clients SET revoked_at = ? WHERE id = ?`,
		time.Now().UTC().Format(store.TimeFormat), other.ClientID); err != nil {
		t.Fatal(err)
	}
	a := h.approve(third.ID, thirdCSRF, thirdHash, url.Values{"item": {"0"}, "path.0.folder": {"/Reading"}})
	if a.status != http.StatusGone || !strings.Contains(a.body, "is revoked: decline this request") {
		t.Errorf("the approval of a revoked client's request: %d %q", a.status, a.body)
	}
	if !h.decidedAt(third.ID).IsZero() {
		t.Error("the request was decided, though its approval was refused")
	}
	if after, err := h.oauth.Grant(t.Context(), "chani", "marks"); err != nil || len(after) != 1 {
		t.Errorf("the grant after the refused approval = %+v, %v", after, err)
	}
	// A revoked client makes no new request.
	if _, err := h.oauth.RequestAccess(t.Context(), other.ClientID, "marks", marksItems()); !errors.Is(err, oauth.ErrNotFound) {
		t.Errorf("a revoked client's request: %v, want ErrNotFound", err)
	}
}

// TestOAuthAccessTwoSelectors checks a round trip where an item names 2
// selectors: the page offers both, and the approval keeps what the admin
// narrowed.
func TestOAuthAccessTwoSelectors(t *testing.T) {
	h := newOAuthHarness(t)
	client, _ := h.signin("pi-pocket", "mcp")
	req, err := h.oauth.RequestAccess(t.Context(), client.ClientID, "marks", []oauth.Item{
		{Cap: "marks:read", Sel: map[string]string{"folder": "/Reading", "tag": "/a"}}})
	if err != nil {
		t.Fatal(err)
	}
	page, csrf, hash := h.consentPage(req.ID)
	if page.status != http.StatusOK {
		t.Fatalf("the page: %d %q", page.status, page.body)
	}
	for _, want := range []string{`name="path.0.folder"`, `name="path.0.tag"`, "/Reading", "/a"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the page does not show %q", want)
		}
	}
	a := h.approve(req.ID, csrf, hash, url.Values{"item": {"0"},
		"path.0.folder": {"/Reading/Inbox"}, "path.0.tag": {"/a"}})
	if a.status != http.StatusSeeOther {
		t.Fatalf("the approval: %d %q", a.status, a.body)
	}
	granted, err := h.oauth.Grant(t.Context(), "chani", "marks")
	if err != nil || len(granted) != 1 || granted[0].Cap != "marks:read" ||
		granted[0].Sel["folder"] != "/Reading/Inbox" || granted[0].Sel["tag"] != "/a" {
		t.Errorf("the grant = %+v, %v", granted, err)
	}
}

// TestOAuthGrantHookAfterCommit checks that the grant hook runs after the
// approval commits: a reader in the hook sees the grant.
func TestOAuthGrantHookAfterCommit(t *testing.T) {
	var (
		mu      sync.Mutex
		seen    []oauth.Item
		hookErr error
	)
	var h *oauthHarness
	h = newOAuthHarnessWith(t, func(o *oauth.Options) {
		o.OnGrant = func(principal, app string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			items, err := h.oauth.Grant(ctx, principal, app)
			mu.Lock()
			defer mu.Unlock()
			seen, hookErr = items, err
		}
	})
	client, _ := h.signin("pi-pocket", "mcp")
	req, err := h.oauth.RequestAccess(t.Context(), client.ClientID, "marks",
		[]oauth.Item{{Cap: "marks:read", Sel: map[string]string{"folder": "/Reading"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, csrf, hash := h.consentPage(req.ID)
	if a := h.approve(req.ID, csrf, hash, url.Values{"item": {"0"}, "path.0.folder": {"/Reading"}}); a.status != http.StatusSeeOther {
		t.Fatalf("the approval: %d %q", a.status, a.body)
	}
	mu.Lock()
	defer mu.Unlock()
	if hookErr != nil {
		t.Fatalf("Grant in the hook: %v", hookErr)
	}
	if len(seen) != 1 || seen[0].Cap != "marks:read" || seen[0].Sel["folder"] != "/Reading" {
		t.Errorf("the hook saw the grant %+v", seen)
	}
}

// TestOAuthRevokePrincipalIsAtomic checks that a failure inside the cascade
// rolls every part of it back.
func TestOAuthRevokePrincipalIsAtomic(t *testing.T) {
	fail := false
	h := newOAuthHarnessWith(t, func(o *oauth.Options) {
		o.BeforePrincipalRevoked = func() error {
			if fail {
				return errors.New("the write of the principal's revocation failed")
			}
			return nil
		}
	})
	client, tok := h.signin("pi-pocket", "mcp")
	req, err := h.oauth.RequestAccess(t.Context(), client.ClientID, "marks", marksItems())
	if err != nil {
		t.Fatal(err)
	}
	_, csrf, hash := h.consentPage(req.ID)
	if a := h.approve(req.ID, csrf, hash, url.Values{"item": {"0", "1"},
		"path.0.folder": {"/Reading"}, "path.1.folder": {"/Reading/Inbox"}}); a.status != http.StatusSeeOther {
		t.Fatalf("the access approval: %d %q", a.status, a.body)
	}
	page, pageCSRF, ts := h.grantsPage()
	if page.status != http.StatusOK {
		t.Fatalf("the grants page: %d %q", page.status, page.body)
	}
	fail = true
	a := h.revoke("principal", "chani", pageCSRF, ts)
	if a.status != http.StatusInternalServerError {
		t.Fatalf("a failed revocation: %d %q", a.status, a.body)
	}
	// Every part of the cascade rolled back.
	if _, err := h.oauth.Check(t.Context(), tok.AccessToken, "mcp"); err != nil {
		t.Errorf("the client's token after a failed revocation: %v", err)
	}
	var revoked sql.NullString
	if err := h.db.QueryRowContext(t.Context(), `SELECT revoked_at FROM oauth_clients WHERE id = ?`, client.ClientID).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if revoked.Valid {
		t.Errorf("the client is revoked at %s, though the cascade failed", revoked.String)
	}
	if err := h.db.QueryRowContext(t.Context(), `SELECT revoked_at FROM principals WHERE name = 'chani'`).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if revoked.Valid {
		t.Errorf("the principal is revoked at %s, though the cascade failed", revoked.String)
	}
	if granted, err := h.oauth.Grant(t.Context(), "chani", "marks"); err != nil || len(granted) != 2 {
		t.Errorf("the grant after a failed revocation = %+v, %v", granted, err)
	}
	if len(h.revoked) != 0 {
		t.Errorf("OnRevoke was called with %+v, though the cascade failed", h.revoked)
	}
}

// TestOAuthRequestRevokedDuringCreation checks that no request survives for
// a client that is revoked between the check and the insert: the insert
// re-checks the client itself, and writes nothing.
func TestOAuthRequestRevokedDuringCreation(t *testing.T) {
	var h *oauthHarness
	h = newOAuthHarnessWith(t, func(o *oauth.Options) {
		o.BeforeRequestInsert = func() {
			// The pre-check has passed, and no transaction holds the
			// database: the revocation goes through at once.
			if err := h.oauth.RevokeClient(t.Context(), h.clientID()); err != nil {
				t.Errorf("the revocation in the seam: %v", err)
			}
		}
	})
	client, _ := h.signin("pi-pocket", "mcp")
	h.setClient(client.ClientID)
	req, err := h.oauth.RequestAccess(t.Context(), client.ClientID, "marks", marksItems())
	if err == nil {
		t.Errorf("the creation = %+v, want an error for a revoked client", req)
	}
	// No request waits for a decision, for this client or any other.
	var pending int
	if err := h.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM consent_requests WHERE client_id = ? AND decided_at IS NULL`, client.ClientID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Errorf("%d requests wait for a decision for a revoked client", pending)
	}
	var revokedAt sql.NullString
	if err := h.db.QueryRowContext(t.Context(), `SELECT revoked_at FROM oauth_clients WHERE id = ?`, client.ClientID).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	if !revokedAt.Valid {
		t.Error("the client is not revoked")
	}
}
