package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/frostyard/lodge/internal/agents"
	"github.com/frostyard/lodge/internal/hearthtest"
	"github.com/frostyard/lodge/internal/hearthtest/notelite"
	"github.com/frostyard/lodge/internal/mcpgw"
	"github.com/frostyard/lodge/internal/oauth"
	"github.com/frostyard/lodge/internal/peer"
)

// newMCPGateway makes the harness's MCP gateway again, after opts changed its
// own, and puts it on the gateway that serves /gateway/mcp.
func (h *oauthHarness) newMCPGateway(opts func(*mcpgw.Options)) *mcpgw.Gateway {
	h.t.Helper()
	o := mcpgw.Options{Apps: h.apps, OAuth: h.oauth, Dial: h.apps.DialContext,
		Log: slog.New(slog.DiscardHandler), Version: "test"}
	if opts != nil {
		opts(&o)
	}
	g := mcpgw.New(o)
	h.mcp = g
	h.mcpHook.Store(g)
	if h.g != nil {
		h.g.cfg.MCP = g
	}
	return g
}

// serveHearth installs hearth's example app under the harness's root, with
// manifest as its hearth manifest when it is not nil, and serves it on its
// lodge socket. It loads the apps again, as a reload does, and tells the
// gateway.
func (h *oauthHarness) serveHearth(manifest []byte) {
	h.t.Helper()
	if err := hearthtest.Install(h.root, "appnotelite"); err != nil {
		h.t.Fatal(err)
	}
	if manifest != nil {
		p := filepath.Join(h.root, "usr/share/lodge/apps", hearthtest.ID, "manifest.json")
		if err := os.WriteFile(p, manifest, 0o644); err != nil {
			h.t.Fatal(err)
		}
	}
	if h.hearth == nil {
		h.hearth = h.startHearth()
	}
	h.reloadApps()
}

// startHearth serves the app on its lodge socket, as this process's user.
func (h *oauthHarness) startHearth() *hearthtest.App {
	h.t.Helper()
	sock := filepath.Join(h.root, "run/lodge/apps", hearthtest.ID+".sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		h.t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		h.t.Fatal(err)
	}
	app, err := hearthtest.Serve(h.t.Context(), l, os.Getuid(), filepath.Join(h.t.TempDir(), "notes.db"))
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() {
		if err := app.Close(); err != nil {
			h.t.Errorf("close hearth's app: %v", err)
		}
	})
	return app
}

// reloadApps loads the apps again, as lodgectl reload does, and tells the
// gateway.
func (h *oauthHarness) reloadApps() {
	h.t.Helper()
	if err := h.apps.Load(); err != nil {
		h.t.Fatal(err)
	}
	h.apps.Poll(h.t.Context())
	h.mcp.AppsChanged()
}

// grant has the admin approve an access request for app's items, as the
// consent page does.
func (h *oauthHarness) grant(clientID, app string, items []oauth.Item) {
	h.t.Helper()
	req, err := h.oauth.RequestAccess(h.t.Context(), clientID, app, items)
	if err != nil {
		h.t.Fatalf("request access: %v", err)
	}
	h.approveAccess(req.ID, items)
}

// approveAccess approves an access request on its consent page, with every
// item and its paths.
func (h *oauthHarness) approveAccess(id string, items []oauth.Item) {
	h.t.Helper()
	page, csrf, hash := h.consentPage(id)
	if page.status != http.StatusOK {
		h.t.Fatalf("the consent page: %d %q", page.status, page.body)
	}
	fields := url.Values{}
	for i, it := range items {
		fields.Add("item", strconv.Itoa(i))
		for name, path := range it.Sel {
			fields.Set("path."+strconv.Itoa(i)+"."+name, path)
		}
	}
	a := h.approve(id, csrf, hash, fields)
	if a.status != http.StatusSeeOther {
		h.t.Fatalf("approving the access request: %d %q", a.status, a.body)
	}
}

// gatewayHTTP is the transport of an MCP client of the harness: it names lodge
// as the Host, and carries 1 access token.
type gatewayHTTP struct {
	base  http.RoundTripper
	token string
}

func (c gatewayHTTP) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Host = lodgeHost
	if c.token == "" {
		r.Header.Del("Authorization")
	} else {
		r.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.base.RoundTrip(r)
}

// connectMCP opens an MCP session on the gateway with token, and signals
// changed on each notifications/tools/list_changed it gets.
func (h *oauthHarness) connectMCP(token string, changed chan<- struct{}) *mcp.ClientSession {
	h.t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "lodge-test", Version: "1"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			if changed == nil {
				return
			}
			select {
			case changed <- struct{}{}:
			default:
			}
		},
	})
	transport := &mcp.StreamableClientTransport{Endpoint: h.lodge.URL + gatewayPath,
		HTTPClient: &http.Client{Transport: gatewayHTTP{base: http.DefaultTransport, token: token}}}
	session, err := client.Connect(h.t.Context(), transport, nil)
	if err != nil {
		h.t.Fatalf("connect to the MCP gateway: %v", err)
	}
	h.t.Cleanup(func() { _ = session.Close() })
	return session
}

// awaitToolChange waits for 1 notifications/tools/list_changed, and fails the
// test when none comes.
func awaitToolChange(t *testing.T, changed <-chan struct{}) {
	t.Helper()
	select {
	case <-changed:
	case <-time.After(30 * time.Second):
		t.Fatal("the session got no notifications/tools/list_changed")
	}
}

// tools are the session's tools, by name.
func tools(t *testing.T, s *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	res, err := s.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	out := make(map[string]*mcp.Tool, len(res.Tools))
	for _, tool := range res.Tools {
		out[tool.Name] = tool
	}
	return out
}

// callTool makes 1 call, and fails the test when the protocol refuses it.
func callTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("tools/call %s: %v", name, err)
	}
	return res
}

// resultText is a result's text content.
func resultText(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range r.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

// lodgeRequest sends 1 request to the lodge node, with a body and its headers.
// A "Host" header replaces the request's Host.
func (h *oauthHarness) lodgeRequest(method, path, body string, hdr map[string]string) (answer, http.Header) {
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
		switch {
		case k == "Host":
			r.Host = v
		case v == "":
			r.Header.Del(k)
		default:
			r.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return answer{resp.StatusCode, resp.Header, string(b)}, resp.Header
}

// TestMCPGatewayTools checks the tools a principal sees: for a grant of 1
// app's read capability, its 2 read operations and lodge's own tool, with the
// hints of each effect (ADR-0024; lodge-http.md, The MCP gateway).
func TestMCPGatewayTools(t *testing.T) {
	h := newOAuthHarness(t)
	h.serveHearth(nil)
	client, tok := h.signin("pi-pocket", "mcp")
	h.grant(client.ClientID, "notelite", []oauth.Item{{Cap: "notelite:read"}})
	s := h.connectMCP(tok.AccessToken, nil)
	got := tools(t, s)
	for _, want := range []string{"notelite_get_note", "notelite_list_notes", "request_access"} {
		if got[want] == nil {
			t.Errorf("the tools miss %q: %v", want, toolNames(got))
		}
	}
	for _, absent := range []string{"notelite_create_note", "notelite_delete_note"} {
		if got[absent] != nil {
			t.Errorf("the tools hold %q, which needs a write the grant does not give", absent)
		}
	}
	// The read tool's hints, from its operation's effect.
	read := got["notelite_list_notes"]
	if read == nil || read.Annotations == nil {
		t.Fatalf("notelite_list_notes = %+v", read)
	}
	if !read.Annotations.ReadOnlyHint || hint(read.Annotations.DestructiveHint) || hint(read.Annotations.OpenWorldHint) {
		t.Errorf("a read tool's hints = %+v", read.Annotations)
	}
	if read.Description == "" || read.InputSchema == nil {
		t.Errorf("the tool's description and schema came from the catalog, not: %+v", read)
	}
	// A write tool, which a wider grant adds, has the write hints.
	h.grant(client.ClientID, "notelite", []oauth.Item{{Cap: "notelite:write"}})
	got = tools(t, s)
	write := got["notelite_create_note"]
	if write == nil || write.Annotations == nil {
		t.Fatalf("notelite_create_note = %+v", write)
	}
	if write.Annotations.ReadOnlyHint || hint(write.Annotations.DestructiveHint) || hint(write.Annotations.OpenWorldHint) {
		t.Errorf("a write tool's hints = %+v", write.Annotations)
	}
}

// hint is a hint's value, and true for an absent one: the schema reads an
// absent destructive or open-world hint as true.
func hint(v *bool) bool {
	if v == nil {
		return true
	}
	return *v
}

// toolNames are a tool set's names, sorted.
func toolNames(tools map[string]*mcp.Tool) []string {
	out := make([]string, 0, len(tools))
	for name := range tools {
		out = append(out, name)
	}
	return out
}

// TestMCPGatewayCallReachesTheApp checks 1 call: it reaches the app on its
// lodge socket with the principal and the grant, and the app's answer is the
// result (ADR-0024).
func TestMCPGatewayCallReachesTheApp(t *testing.T) {
	h := newOAuthHarness(t)
	h.serveHearth(nil)
	client, tok := h.signin("pi-pocket", "mcp")
	items := []oauth.Item{
		{Cap: "notelite:read", Sel: map[string]string{"folder": "/Work"}},
		{Cap: "notelite:write", Sel: map[string]string{"folder": "/Work"}},
	}
	h.grant(client.ClientID, "notelite", items)
	s := h.connectMCP(tok.AccessToken, nil)
	created := callTool(t, s, "notelite_create_note", map[string]any{"folder": "/Work", "title": "buy milk"})
	if created.IsError {
		t.Fatalf("the create: %s", resultText(t, created))
	}
	// The app saw the principal, its own kind of caller, and a grant that is
	// the principal's.
	principal, grant := h.hearth.LastCaller()
	if principal != "chani" {
		t.Errorf("the app saw principal %q, want chani", principal)
	}
	if kind := h.hearth.LastKind(); kind != "principal" {
		t.Errorf("the app saw Lodge-Caller %q, want principal", kind)
	}
	if got := decodeItems(t, grant); !sameItems(got, items) {
		t.Errorf("the app saw the grant %+v, want %+v", got, items)
	}
	// The result is the app's answer, and its text is the same JSON.
	list := callTool(t, s, "notelite_list_notes", map[string]any{"folder": "/Work"})
	if list.IsError {
		t.Fatalf("the list: %s", resultText(t, list))
	}
	object, ok := list.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("the result's structured content = %T", list.StructuredContent)
	}
	notes, ok := object["notes"].([]any)
	if !ok || len(notes) != 1 {
		t.Fatalf("the app's answer = %v", object)
	}
	if !strings.Contains(resultText(t, list), `"title":"buy milk"`) {
		t.Errorf("the result's text = %s", resultText(t, list))
	}
}

// decodeItems is a Lodge-Grant header value's items.
func decodeItems(t *testing.T, header string) []oauth.Item {
	t.Helper()
	data, err := base64.RawURLEncoding.Strict().DecodeString(header)
	if err != nil {
		t.Fatalf("Lodge-Grant %q is not base64url: %v", header, err)
	}
	var items []oauth.Item
	if err := json.Unmarshal(data, &items); err != nil {
		t.Fatalf("Lodge-Grant %q is not the items' JSON: %v", header, err)
	}
	return items
}

// sameItems reports whether 2 grants hold the same items, in any order.
func sameItems(a, b []oauth.Item) bool {
	if len(a) != len(b) {
		return false
	}
	used := make([]bool, len(b))
	for _, x := range a {
		found := false
		for i, y := range b {
			if used[i] || x.Cap != y.Cap || len(x.Sel) != len(y.Sel) {
				continue
			}
			ok := true
			for k, v := range x.Sel {
				if y.Sel[k] != v {
					ok = false
					break
				}
			}
			if ok {
				used[i], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// TestMCPGatewayRequestAccess checks lodge's own tool: it makes an access
// request, the admin approves it on the consent page, and the open session
// learns of the new tool with notifications/tools/list_changed and stays open
// (ADR-0024, A grant change reaches live sessions).
func TestMCPGatewayRequestAccess(t *testing.T) {
	h := newOAuthHarness(t)
	h.serveHearth(nil)
	_, tok := h.signin("pi-pocket", "mcp")
	changed := make(chan struct{}, 4)
	s := h.connectMCP(tok.AccessToken, changed)
	if got := tools(t, s); len(got) != 1 || got["request_access"] == nil {
		t.Fatalf("a session with no grant has the tools %v", toolNames(got))
	}
	items := []oauth.Item{{Cap: "notelite:write", Sel: map[string]string{"folder": "/Work"}}}
	res := callTool(t, s, "request_access", map[string]any{"app": "notelite",
		"items": []any{map[string]any{"cap": "notelite:write", "sel": map[string]any{"folder": "/Work"}}}})
	if res.IsError {
		t.Fatalf("request_access: %s", resultText(t, res))
	}
	object, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("request_access's structured content = %T", res.StructuredContent)
	}
	id, _ := object["request"].(string)
	url, _ := object["url"].(string)
	if id == "" || url != h.oauth.Issuer()+"/oauth/consent/"+id {
		t.Errorf("request_access = %v, want the request's ID and its link", object)
	}
	if expires, _ := object["expires"].(string); expires == "" {
		t.Errorf("request_access has no expiry: %v", object)
	}
	if text := resultText(t, res); !strings.Contains(text, url) {
		t.Errorf("request_access's text %q does not hold the link", text)
	}
	// The admin approves it, and the session's tools change.
	h.approveAccess(id, items)
	awaitToolChange(t, changed)
	if got := tools(t, s); got["notelite_create_note"] == nil {
		t.Fatalf("the session's tools after the approval are %v", toolNames(got))
	}
	// The session stayed open: a call works, and the app sees it.
	if created := callTool(t, s, "notelite_create_note", map[string]any{"folder": "/Work", "title": "x"}); created.IsError {
		t.Fatalf("the call after the approval: %s", resultText(t, created))
	}
}

// TestMCPGatewayGrantNarrowing checks the grants page's removal of 1 item: the
// session learns of it, and the app is not called again (ADR-0024; a call
// reads the grant again).
func TestMCPGatewayGrantNarrowing(t *testing.T) {
	h := newOAuthHarness(t)
	h.serveHearth(nil)
	client, tok := h.signin("pi-pocket", "mcp")
	items := []oauth.Item{{Cap: "notelite:write", Sel: map[string]string{"folder": "/Work"}}}
	h.grant(client.ClientID, "notelite", items)
	changed := make(chan struct{}, 4)
	s := h.connectMCP(tok.AccessToken, changed)
	if got := tools(t, s); got["notelite_create_note"] == nil {
		t.Fatalf("the session's tools are %v", toolNames(got))
	}
	// The app refuses a folder outside the grant, and takes 1 inside it.
	refused := callTool(t, s, "notelite_create_note", map[string]any{"folder": "/Home", "title": "x"})
	if !refused.IsError || !strings.Contains(resultText(t, refused), "permission denied") {
		t.Errorf("a folder outside the grant = %+v, %q", refused, resultText(t, refused))
	}
	if allowed := callTool(t, s, "notelite_create_note", map[string]any{"folder": "/Work", "title": "y"}); allowed.IsError {
		t.Fatalf("a folder inside the grant: %s", resultText(t, allowed))
	}
	// The grants page removes the item.
	page, csrf, ts := h.grantsPage()
	if page.status != http.StatusOK {
		t.Fatalf("the grants page: %d %q", page.status, page.body)
	}
	a, _ := h.lodgeRequest(http.MethodPost, "/oauth/grants/revoke",
		formBody(map[string]string{"csrf": csrf, "ts": ts, "principal": "chani", "app": "notelite",
			"item": oauth.ItemHash(items[0])}), formHeaders())
	if a.status != http.StatusSeeOther {
		t.Fatalf("the removal: %d %q", a.status, a.body)
	}
	awaitToolChange(t, changed)
	if got := tools(t, s); got["notelite_create_note"] != nil {
		t.Fatalf("the session's tools after the removal are %v", toolNames(got))
	}
	// The next call of the tool is refused by lodge, which reads the grant
	// again, and the app sees nothing.
	principal, grant := h.hearth.LastCaller()
	res := callTool(t, s, "notelite_create_note", map[string]any{"folder": "/Work", "title": "z"})
	if !res.IsError {
		t.Fatalf("a call after the narrowing = %+v", res)
	}
	if want := "insufficient_scope: principal chani holds no notelite:write for app notelite: call request_access to ask for it"; resultText(t, res) != want {
		t.Errorf("the refusal = %q, want %q", resultText(t, res), want)
	}
	if again, againGrant := h.hearth.LastCaller(); again != principal || againGrant != grant {
		t.Errorf("the app saw another call: %q, %q", again, againGrant)
	}
}

// formBody is a form's body, as a browser sends it.
func formBody(fields map[string]string) string {
	v := url.Values{}
	for k, field := range fields {
		v.Set(k, field)
	}
	return v.Encode()
}

// TestMCPGatewayRefusesUngrantedTool checks the gateway's own check of the
// grant, which reads it again at each call: without an item of the operation's
// permission the call is a tool error that names request_access, and the app
// sees nothing (ADR-0024).
func TestMCPGatewayRefusesUngrantedTool(t *testing.T) {
	h := newOAuthHarness(t)
	h.serveHearth(nil)
	client, tok := h.signin("pi-pocket", "mcp")
	h.grant(client.ClientID, "notelite", []oauth.Item{{Cap: "notelite:write"}})
	s := h.connectMCP(tok.AccessToken, nil)
	if got := tools(t, s); got["notelite_create_note"] == nil {
		t.Fatalf("the session's tools are %v", toolNames(got))
	}
	if created := callTool(t, s, "notelite_create_note", map[string]any{"folder": "/Work", "title": "x"}); created.IsError {
		t.Fatalf("the first call: %s", resultText(t, created))
	}
	principal, grant := h.hearth.LastCaller()
	// The grant loses the item without a sync's seeing it: the session's tool
	// is still there, and the call is what checks the grant.
	narrowTo(t, h, "chani", "notelite", []oauth.Item{{Cap: "notelite:read"}})
	res := callTool(t, s, "notelite_create_note", map[string]any{"folder": "/Work", "title": "y"})
	if !res.IsError {
		t.Fatalf("a call outside the grant = %+v", res)
	}
	want := "insufficient_scope: principal chani holds no notelite:write for app notelite: call request_access to ask for it"
	if got := resultText(t, res); got != want {
		t.Errorf("the refusal = %q, want %q", got, want)
	}
	if again, againGrant := h.hearth.LastCaller(); again != principal || againGrant != grant {
		t.Errorf("the app saw a call outside the grant: %q, %q", again, againGrant)
	}
}

// narrowTo writes a principal's grant for an app as items, without the hook a
// change through lodge's pages runs.
func narrowTo(t *testing.T, h *oauthHarness, principal, app string, items []oauth.Item) {
	t.Helper()
	b, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.ExecContext(t.Context(), `UPDATE grants SET items = ? WHERE principal = ? AND app = ?`,
		string(b), principal, app); err != nil {
		t.Fatal(err)
	}
}

// TestMCPGatewayChecks checks the gateway's checks in order: the scope, the
// token, the Host, the Origin, the method, the peer and the upgrade
// (docs/specs/lodge-http.md, The MCP gateway).
func TestMCPGatewayChecks(t *testing.T) {
	h := newOAuthHarness(t)
	h.serveHearth(nil)
	client, tok := h.signin("pi-pocket", "mcp")
	h.grant(client.ClientID, "notelite", []oauth.Item{{Cap: "notelite:read"}})
	// A token whose consent approved only the events scope is 403, with the
	// challenge that asks for more scope.
	_, events := h.signin("pi-events", "events")
	a, header := h.lodgeRequest(http.MethodPost, gatewayPath, `{}`, map[string]string{
		"Content-Type": "application/json", "Authorization": "Bearer " + events.AccessToken})
	if a.status != http.StatusForbidden {
		t.Errorf("a token without mcp: %d %q", a.status, a.body)
	}
	challenge := header.Get("WWW-Authenticate")
	for _, want := range []string{`error="insufficient_scope"`, `scope="mcp"`, "resource_metadata="} {
		if !strings.Contains(challenge, want) {
			t.Errorf("the challenge %q does not hold %s", challenge, want)
		}
	}
	// A missing token, a made-up one and an lgm_ token are 401, with the
	// metadata that a client discovers lodge by.
	secret, err := h.tokens.Issue(t.Context(), "phone", "notelite", "mcp", "")
	if err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"none": "", "made up": "lgo_at_nonsense", "an lgm_ token": secret} {
		hdr := map[string]string{"Content-Type": "application/json"}
		if token != "" {
			hdr["Authorization"] = "Bearer " + token
		}
		a, header := h.lodgeRequest(http.MethodPost, gatewayPath, `{}`, hdr)
		if a.status != http.StatusUnauthorized {
			t.Errorf("%s: %d %q, want 401", name, a.status, a.body)
		}
		want := `Bearer resource_metadata="` + h.oauth.Issuer() + `/.well-known/oauth-protected-resource/gateway/mcp"`
		if got := header.Get("WWW-Authenticate"); got != want {
			t.Errorf("%s: the challenge %q, want %q", name, got, want)
		}
		// The answer carries the confined set, without the resource policy the
		// SDK's own clients ignore.
		if csp := header.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
			t.Errorf("%s: the Content-Security-Policy is %q", name, csp)
		}
		if header.Get("Cross-Origin-Resource-Policy") != "" {
			t.Errorf("%s: the answer carries Cross-Origin-Resource-Policy", name)
		}
	}
	good := map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + tok.AccessToken,
		"Accept": "application/json, text/event-stream"}
	// The Host.
	hdr := map[string]string{"Host": "elsewhere.example"}
	for k, v := range good {
		hdr[k] = v
	}
	if a, _ := h.lodgeRequest(http.MethodPost, gatewayPath, `{}`, hdr); a.status != http.StatusForbidden ||
		!strings.Contains(a.body, "not lodge's address") {
		t.Errorf("another Host: %d %q", a.status, a.body)
	}
	// The Origin: a browser may not call it.
	hdr = map[string]string{"Origin": lodgeOrigin}
	for k, v := range good {
		hdr[k] = v
	}
	if a, _ := h.lodgeRequest(http.MethodPost, gatewayPath, `{}`, hdr); a.status != http.StatusForbidden ||
		!strings.Contains(a.body, "browsers may not call") {
		t.Errorf("an Origin: %d %q", a.status, a.body)
	}
	// The method.
	a2, header := h.lodgeRequest(http.MethodPut, gatewayPath, `{}`, good)
	if a2.status != http.StatusMethodNotAllowed || header.Get("Allow") != "POST, GET, DELETE" {
		t.Errorf("a PUT: %d %v", a2.status, header)
	}
	// The peer: WhoIs must name it, from one of its own addresses.
	h.who.fail(errors.New("no peer"))
	if a, _ := h.lodgeRequest(http.MethodPost, gatewayPath, `{}`, good); a.status != http.StatusForbidden ||
		!strings.Contains(a.body, "cannot name this peer") {
		t.Errorf("a peer that WhoIs does not name: %d %q", a.status, a.body)
	}
	h.who.fail(nil)
	h.who.set(func(p *peer.Peer) { p.Addrs = nil })
	if a, _ := h.lodgeRequest(http.MethodPost, gatewayPath, `{}`, good); a.status != http.StatusForbidden ||
		!strings.Contains(a.body, "forwarded traffic is refused") {
		t.Errorf("a forwarded peer: %d %q", a.status, a.body)
	}
	h.who.set(func(p *peer.Peer) { p.Addrs = loopbacks })
	// The upgrade.
	hdr = map[string]string{"Connection": "upgrade", "Upgrade": "websocket"}
	for k, v := range good {
		hdr[k] = v
	}
	if a, _ := h.lodgeRequest(http.MethodPost, gatewayPath, `{}`, hdr); a.status != http.StatusBadRequest ||
		!strings.Contains(a.body, "takes no upgrade") {
		t.Errorf("an upgrade: %d %q", a.status, a.body)
	}
	// And a request that passes every check reaches the gateway.
	if a, _ := h.lodgeRequest(http.MethodPost, gatewayPath,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, good); a.status != http.StatusBadRequest &&
		a.status != http.StatusOK {
		t.Errorf("a request that passes the checks: %d %q", a.status, a.body)
	}
}

// TestMCPGatewayBindsSessionsToClients checks that a session belongs to the
// OAuth client whose token opened it: another client of the same principal is
// refused (docs/specs/lodge-http.md, The MCP gateway).
func TestMCPGatewayBindsSessionsToClients(t *testing.T) {
	h := newOAuthHarness(t)
	h.serveHearth(nil)
	client, tok := h.signin("pi-a", "mcp")
	h.grant(client.ClientID, "notelite", []oauth.Item{{Cap: "notelite:read"}})
	_, other := h.signin("pi-b", "mcp")
	s := h.connectMCP(tok.AccessToken, nil)
	id := s.ID()
	if id == "" {
		t.Fatal("the session has no ID")
	}
	a, _ := h.lodgeRequest(http.MethodPost, gatewayPath, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
			"Mcp-Session-Id": id, "Authorization": "Bearer " + other.AccessToken})
	if a.status != http.StatusForbidden || !strings.Contains(a.body, "session user mismatch") {
		t.Errorf("another client's token on the session: %d %q", a.status, a.body)
	}
	// Its own client's token works.
	b, _ := h.lodgeRequest(http.MethodPost, gatewayPath, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
		map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
			"Mcp-Session-Id": id, "Authorization": "Bearer " + tok.AccessToken})
	if b.status != http.StatusOK {
		t.Errorf("the session's own client: %d %q", b.status, b.body)
	}
}

// TestMCPGatewayRevokeEndsRequests checks that revoking a client ends its open
// stream at once, and that its next request is 401 (ADR-0024).
func TestMCPGatewayRevokeEndsRequests(t *testing.T) {
	h := newOAuthHarness(t)
	h.serveHearth(nil)
	client, tok := h.signin("pi-a", "mcp")
	h.grant(client.ClientID, "notelite", []oauth.Item{{Cap: "notelite:read"}})
	// A session of lodge's own: initialize, then a standalone SSE stream.
	_, header := h.lodgeRequest(http.MethodPost, gatewayPath,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",`+
			`"capabilities":{},"clientInfo":{"name":"raw","version":"1"}}}`,
		map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
			"Authorization": "Bearer " + tok.AccessToken})
	id := header.Get("Mcp-Session-Id")
	if id == "" {
		t.Fatal("initialize set no session ID")
	}
	ended := h.openStream(tok.AccessToken, id)
	// The grants page's CSRF token, and the revocation.
	_, csrf, ts := h.grantsPage()
	a, _ := h.lodgeRequest(http.MethodPost, "/oauth/grants/revoke",
		formBody(map[string]string{"csrf": csrf, "ts": ts, "client": client.ClientID}), formHeaders())
	if a.status != http.StatusSeeOther {
		t.Fatalf("the revocation: %d %q", a.status, a.body)
	}
	select {
	case <-ended:
	case <-time.After(30 * time.Second):
		t.Fatal("the revoked client's stream is still open")
	}
	// Its next request is 401.
	b, _ := h.lodgeRequest(http.MethodPost, gatewayPath, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
			"Mcp-Session-Id": id, "Authorization": "Bearer " + tok.AccessToken})
	if b.status != http.StatusUnauthorized {
		t.Errorf("a revoked client's request: %d %q", b.status, b.body)
	}
}

// openStream opens a standalone SSE stream on the gateway for a session, and
// returns a channel that is closed when the stream ends.
func (h *oauthHarness) openStream(token, session string) <-chan struct{} {
	h.t.Helper()
	done := make(chan struct{})
	r, err := http.NewRequestWithContext(h.t.Context(), http.MethodGet, h.lodge.URL+gatewayPath, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	r.Host = lodgeHost
	r.Header.Set("Accept", "text/event-stream")
	r.Header.Set("Mcp-Session-Id", session)
	r.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		h.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("the stream: %d", resp.StatusCode)
	}
	go func() {
		defer close(done)
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
	}()
	h.t.Cleanup(func() {
		_ = resp.Body.Close()
	})
	return done
}

// TestMCPGatewayAppsChanged checks that a reload which changes the apps
// changes the tools of an open session, and that the session stays open
// (docs/specs/lodge-http.md, The MCP gateway).
func TestMCPGatewayAppsChanged(t *testing.T) {
	h := newOAuthHarness(t)
	h.serveHearth(nil)
	client, tok := h.signin("pi-a", "mcp")
	h.grant(client.ClientID, "notelite", []oauth.Item{{Cap: "notelite:read"}})
	changed := make(chan struct{}, 4)
	s := h.connectMCP(tok.AccessToken, changed)
	if got := tools(t, s); got["notelite_get_note"] == nil {
		t.Fatalf("the session's tools are %v", toolNames(got))
	}
	// The app is removed, and a reload tells the gateway.
	if err := os.RemoveAll(filepath.Join(h.root, "usr/share/lodge/apps", hearthtest.ID)); err != nil {
		t.Fatal(err)
	}
	h.reloadApps()
	awaitToolChange(t, changed)
	if got := tools(t, s); got["notelite_get_note"] != nil {
		t.Errorf("the session still holds an app that is not installed: %v", toolNames(got))
	}
	// It comes back, and so do its tools.
	h.serveHearth(nil)
	awaitToolChange(t, changed)
	if got := tools(t, s); got["notelite_get_note"] == nil {
		t.Errorf("the session's tools after the app came back are %v", toolNames(got))
	}
	// The session still calls the app.
	if res := callTool(t, s, "notelite_list_notes", map[string]any{"folder": "/Work"}); res.IsError {
		t.Errorf("the call after the reload: %s", resultText(t, res))
	}
}

// TestMCPGatewaySupersedesAppMCP checks that an app on hearth has no
// /mcp/<id>, on the lodge node and on the agent socket: agents reach it
// through the gateway (ADR-0024).
func TestMCPGatewaySupersedesAppMCP(t *testing.T) {
	h := newOAuthHarness(t)
	h.serveHearth(nil)
	secret, err := h.tokens.Issue(t.Context(), "phone", "notelite", "mcp", "")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := h.lodgeRequest(http.MethodPost, "/mcp/notelite", `{}`, map[string]string{
		"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
		"Authorization": "Bearer " + secret})
	if a.status != http.StatusNotFound {
		t.Errorf("/mcp/notelite on the lodge node: %d %q", a.status, a.body)
	}
	// The agent socket: a token for app notelite and for the agent's user.
	client := h.agentClient(t)
	agentSecret, err := h.tokens.Issue(t.Context(), "porter", "notelite", "mcp", "frostporter")
	if err != nil {
		t.Fatal(err)
	}
	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://agent/mcp/notelite", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("Authorization", "Bearer "+agentSecret)
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("/mcp/notelite on the agent socket: %d", resp.StatusCode)
	}
}

// agentClient is an HTTP client on an agent socket served by the harness's
// gateway, where this process's uid is frostporter.
func (h *oauthHarness) agentClient(t *testing.T) *http.Client {
	t.Helper()
	uid := os.Getuid()
	ag := agents.New(agents.Options{DevUID: -1, Lookup: func(name string) (int, error) {
		if name == "frostporter" {
			return uid, nil
		}
		return -1, errors.New("no such user")
	}})
	ag.Load([]string{"frostporter"}, nil)
	path := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := agents.Listen(path, "")
	if err != nil {
		t.Fatal(err)
	}
	srv := Server(h.g.Agent())
	perConn := srv.ConnContext
	srv.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		return agents.ConnContext(perConn(ctx, c), c)
	}
	go func() { _ = srv.Serve(ag.Listener(ln)) }()
	t.Cleanup(func() { srv.Close() })
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}, DisableKeepAlives: true}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr}
}

// connectPath is the Connect call of the app's ListNotes operation.
const connectPath = "/app/notelite/connect/example.notes.v1.NoteService/ListNotes"

// frameHeaders are the headers a frame's Connect call carries.
func frameHeaders() map[string]string {
	return map[string]string{"Content-Type": "application/json", "Origin": lodgeOrigin,
		"Sec-Fetch-Site": "same-origin"}
}

// with returns hdr with each of extra set, or deleted when its value is "".
func with(hdr map[string]string, extra map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range hdr {
		out[k] = v
	}
	for k, v := range extra {
		if v == "" {
			delete(out, k)
			continue
		}
		out[k] = v
	}
	return out
}

// TestAppConnectCalls checks an app on hearth's call from its frame: it reaches
// the app with every capability of the manifest on the root path, as 1 signed-in
// person (docs/specs/lodge-http.md, App calls, /app/<id>/connect/).
func TestAppConnectCalls(t *testing.T) {
	h := newOAuthHarness(t)
	h.serveHearth(nil)
	a, header := h.lodgeRequest(http.MethodPost, connectPath, `{"folder":"/Work"}`, frameHeaders())
	if a.status != http.StatusOK {
		t.Fatalf("the call: %d %q", a.status, a.body)
	}
	if ct := header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("the answer's Content-Type is %q, want the app's JSON", ct)
	}
	if !strings.Contains(header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Errorf("the answer has no app policy: %v", header)
	}
	principal, grant := h.hearth.LastCaller()
	if principal != "alice@example.com" || h.hearth.LastKind() != "principal" {
		t.Errorf("the app saw %q as %q, want alice@example.com as a principal", principal, h.hearth.LastKind())
	}
	items := decodeItems(t, grant)
	if len(items) != 2 || items[0].Cap != "notelite:read" || items[1].Cap != "notelite:write" {
		t.Fatalf("the grant = %+v, want every capability of the manifest, in its order", items)
	}
	for _, it := range items {
		if len(it.Sel) != 0 {
			t.Errorf("the item %+v names a selector, want the root path", it)
		}
	}
	// A content type with a parameter is the same JSON.
	if a, _ := h.lodgeRequest(http.MethodPost, connectPath, `{"folder":"/Work"}`,
		with(frameHeaders(), map[string]string{"Content-Type": "application/json; charset=utf-8"})); a.status != http.StatusOK {
		t.Errorf("a JSON content type with a parameter: %d %q", a.status, a.body)
	}
	// A forged Lodge-* header changes nothing: lodge deletes every one.
	a, _ = h.lodgeRequest(http.MethodPost, connectPath, `{"folder":"/Work"}`,
		with(frameHeaders(), map[string]string{"Lodge-Principal": "someone@example.com", "Lodge-Caller": "agent",
			"Lodge-Grant": "e30", "Lodge-Session": "nope"}))
	if a.status != http.StatusOK {
		t.Fatalf("a call with forged Lodge-* headers: %d %q", a.status, a.body)
	}
	if principal, grant := h.hearth.LastCaller(); principal != "alice@example.com" || len(decodeItems(t, grant)) != 2 {
		t.Errorf("a forged header reached the app: %q, %q", principal, grant)
	}
	if kind := h.hearth.LastKind(); kind != "principal" {
		t.Errorf("a forged Lodge-Caller reached the app: %q", kind)
	}
	// The body's limit: 4 MiB passes, and 1 more byte is 413.
	base := `{"folder":"/Work"}`
	exact := base + strings.Repeat(" ", connectBody-len(base))
	if len(exact) != connectBody {
		t.Fatalf("the body is %d bytes, want %d", len(exact), connectBody)
	}
	if a, _ := h.lodgeRequest(http.MethodPost, connectPath, exact, frameHeaders()); a.status == http.StatusRequestEntityTooLarge {
		t.Errorf("a body of exactly 4 MiB: %d %q", a.status, a.body)
	}
	if a, _ := h.lodgeRequest(http.MethodPost, connectPath, exact+" ", frameHeaders()); a.status != http.StatusRequestEntityTooLarge {
		t.Errorf("a body of 4 MiB and 1: %d %q", a.status, a.body)
	}
}

// TestAppConnectChecks checks each of the call route's checks
// (docs/specs/lodge-http.md, App calls, /app/<id>/connect/).
func TestAppConnectChecks(t *testing.T) {
	h := newOAuthHarness(t)
	h.serveHearth(nil)
	for name, c := range map[string]struct {
		method string
		path   string
		body   string
		hdr    map[string]string
		status int
		line   string
		allow  string
	}{
		"no Origin": {"POST", connectPath, `{}`, with(frameHeaders(), map[string]string{"Origin": ""}),
			http.StatusForbidden, "needs lodge's Origin", ""},
		"another Origin": {"POST", connectPath, `{}`, with(frameHeaders(), map[string]string{"Origin": "https://elsewhere.example"}),
			http.StatusForbidden, "from another origin", ""},
		"another site": {"POST", connectPath, `{}`, with(frameHeaders(), map[string]string{"Sec-Fetch-Site": "cross-site"}),
			http.StatusForbidden, "from another site", ""},
		"a GET": {"GET", connectPath, "", frameHeaders(), http.StatusMethodNotAllowed, "is not allowed here", "POST"},
		"an upgrade": {"POST", connectPath, `{}`, with(frameHeaders(), map[string]string{"Connection": "upgrade",
			"Upgrade": "websocket"}), http.StatusBadRequest, "takes no upgrade", ""},
		"a form":            {"POST", connectPath, `{}`, with(frameHeaders(), map[string]string{"Content-Type": "text/plain"}), http.StatusUnsupportedMediaType, "application/json", ""},
		"no such app":       {"POST", "/app/nope/connect/example.notes.v1.NoteService/ListNotes", `{}`, frameHeaders(), http.StatusNotFound, "no app is installed", ""},
		"no such method":    {"POST", "/app/notelite/connect/example.notes.v1.NoteService/Nope", `{}`, frameHeaders(), http.StatusNotFound, "not found", ""},
		"2 methods":         {"POST", "/app/notelite/connect/example.notes.v1.NoteService/ListNotes/More", `{}`, frameHeaders(), http.StatusNotFound, "not found", ""},
		"a bad service":     {"POST", "/app/notelite/connect/example.notes.v1/Nope", `{}`, frameHeaders(), http.StatusNotFound, "not found", ""},
		"no path at all":    {"POST", "/app/notelite/connect", `{}`, frameHeaders(), http.StatusNotFound, "not found", ""},
		"a login with no @": {"POST", connectPath, `{}`, frameHeaders(), http.StatusForbidden, "cannot be a principal", ""},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "a login with no @." {
				return
			}
			if name == "a login with no @" {
				// The login is a person's, and an OAuth principal has none.
				if err := h.allow.AllowLogin(t.Context(), "someone"); err != nil {
					t.Fatal(err)
				}
				h.who.set(func(p *peer.Peer) { p.Login = "someone" })
				defer h.who.set(func(p *peer.Peer) { p.Login = "alice@example.com" })
			}
			a, header := h.lodgeRequest(c.method, c.path, c.body, c.hdr)
			if a.status != c.status || !strings.Contains(a.body, c.line) {
				t.Errorf("%s: %d %q, want %d with %q", name, a.status, a.body, c.status, c.line)
			}
			if c.allow != "" && header.Get("Allow") != c.allow {
				t.Errorf("%s: Allow = %q, want %q", name, header.Get("Allow"), c.allow)
			}
		})
	}
	// The identity checks are the app calls' 5th: a login that is not allowed
	// is 403 before the app is found.
	h.who.set(func(p *peer.Peer) { p.Login = "bob@example.com" })
	defer h.who.set(func(p *peer.Peer) { p.Login = "alice@example.com" })
	if a, _ := h.lodgeRequest(http.MethodPost, connectPath, `{}`, frameHeaders()); a.status != http.StatusForbidden ||
		!strings.Contains(a.body, "is not allowed") {
		t.Errorf("a login that is not allowed: %d %q", a.status, a.body)
	}
}

// TestAppConnectDisabled checks that a disabled app on hearth answers 503 with
// its reason, and that its calls are not files.
func TestAppConnectDisabled(t *testing.T) {
	h := newOAuthHarness(t)
	h.serveHearth([]byte(`{"hearth":2,"app":"notelite"}`))
	a, _ := h.lodgeRequest(http.MethodPost, connectPath, `{}`, frameHeaders())
	if a.status != http.StatusServiceUnavailable || !strings.Contains(a.body, "is disabled: hearth manifest format 2") {
		t.Errorf("a disabled app: %d %q", a.status, a.body)
	}
}

// TestAppConnectUIOnly checks that a call reaches only an operation that the
// manifest exposes to ui: an agent-only operation is 404.
func TestAppConnectUIOnly(t *testing.T) {
	h := newOAuthHarness(t)
	manifest := agentOnly(t, "example.notes.v1.NoteService.CreateNote")
	h.serveHearth(manifest)
	a, _ := h.lodgeRequest(http.MethodPost, "/app/notelite/connect/example.notes.v1.NoteService/CreateNote",
		`{"folder":"/Work","title":"x"}`, frameHeaders())
	if a.status != http.StatusNotFound {
		t.Errorf("an agent-only operation: %d %q", a.status, a.body)
	}
	// The other operations are still the frame's.
	if a, _ := h.lodgeRequest(http.MethodPost, connectPath, `{}`, frameHeaders()); a.status != http.StatusOK {
		t.Errorf("a ui operation: %d %q", a.status, a.body)
	}
}

// agentOnly is hearth's manifest of the example app, with op's expose narrowed
// to agent alone.
func agentOnly(t *testing.T, op string) []byte {
	t.Helper()
	var m struct {
		Operations []map[string]any `json:"operations"`
	}
	if err := json.Unmarshal(notelite.Manifest, &m); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range m.Operations {
		if o["name"] == op {
			o["expose"] = []string{"agent"}
			found = true
		}
	}
	if !found {
		t.Fatalf("hearth's manifest has no operation %q", op)
	}
	var all map[string]any
	if err := json.Unmarshal(notelite.Manifest, &all); err != nil {
		t.Fatal(err)
	}
	out := make([]any, 0, len(m.Operations))
	for _, o := range m.Operations {
		out = append(out, o)
	}
	all["operations"] = out
	b, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestMCPGatewayIdleSessionCloses checks that a session with no request for
// SessionTimeout closes, and that a request with its ID is then 404
// (docs/specs/lodge-http.md, The MCP gateway).
func TestMCPGatewayIdleSessionCloses(t *testing.T) {
	h := newOAuthHarness(t)
	h.serveHearth(nil)
	h.newMCPGateway(func(o *mcpgw.Options) { o.SessionTimeout = 200 * time.Millisecond })
	client, tok := h.signin("pi-a", "mcp")
	h.grant(client.ClientID, "notelite", []oauth.Item{{Cap: "notelite:read"}})
	// A session of lodge's own: only a POST resets an idle session's timer, so
	// the test's own polls do not keep it open.
	_, header := h.lodgeRequest(http.MethodPost, gatewayPath,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",`+
			`"capabilities":{},"clientInfo":{"name":"raw","version":"1"}}}`,
		map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
			"Authorization": "Bearer " + tok.AccessToken})
	id := header.Get("Mcp-Session-Id")
	if id == "" {
		t.Fatal("initialize set no session ID")
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if status := h.sessionStatus(tok.AccessToken, id); status == http.StatusNotFound {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the idle session is still there after %v", 30*time.Second)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// sessionStatus is the status of a GET of a session's stream, which is not
// read.
func (h *oauthHarness) sessionStatus(token, session string) int {
	h.t.Helper()
	r, err := http.NewRequestWithContext(h.t.Context(), http.MethodGet, h.lodge.URL+gatewayPath, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	r.Host = lodgeHost
	r.Header.Set("Accept", "text/event-stream")
	r.Header.Set("Mcp-Session-Id", session)
	r.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		h.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestMCPGatewayOffIsNotFound checks that /gateway/mcp is 404 with no gateway,
// or with no OAuth server (docs/specs/lodge-http.md, The MCP gateway).
func TestMCPGatewayOffIsNotFound(t *testing.T) {
	h := newOAuthHarness(t)
	h.g.cfg.MCP = nil
	if a, _ := h.lodgeRequest(http.MethodPost, gatewayPath, `{}`, map[string]string{"Content-Type": "application/json"}); a.status != http.StatusNotFound {
		t.Errorf("no gateway: %d %q", a.status, a.body)
	}
	h.g.cfg.MCP = h.mcp
	h.g.cfg.OAuth = nil
	if a, _ := h.lodgeRequest(http.MethodPost, gatewayPath, `{}`, map[string]string{"Content-Type": "application/json"}); a.status != http.StatusNotFound {
		t.Errorf("no OAuth server: %d %q", a.status, a.body)
	}
}

// TestMCPGatewayCallOfADisabledApp checks a call of a tool whose app was
// disabled since the session read its tools: the call is a tool error with the
// app's reason, and the app is not called (ADR-0024, A call of <id>_<tool>).
func TestMCPGatewayCallOfADisabledApp(t *testing.T) {
	h := newOAuthHarness(t)
	h.serveHearth(nil)
	client, tok := h.signin("pi-a", "mcp")
	h.grant(client.ClientID, "notelite", []oauth.Item{{Cap: "notelite:read"}})
	s := h.connectMCP(tok.AccessToken, nil)
	if got := tools(t, s); got["notelite_get_note"] == nil {
		t.Fatalf("the session's tools are %v", toolNames(got))
	}
	// The app's hearth manifest breaks, and the app is disabled without the
	// session's tools changing.
	if err := os.WriteFile(filepath.Join(h.root, "usr/share/lodge/apps", hearthtest.ID, "manifest.json"),
		[]byte(`{"hearth":2,"app":"notelite"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.apps.Load(); err != nil {
		t.Fatal(err)
	}
	res := callTool(t, s, "notelite_get_note", map[string]any{"id": "n-1"})
	if !res.IsError || !strings.Contains(resultText(t, res), "is disabled: hearth manifest format 2") {
		t.Errorf("a call of a disabled app = %+v, %q", res, resultText(t, res))
	}
	if principal, _ := h.hearth.LastCaller(); principal != "" {
		t.Errorf("the disabled app saw a call from %q", principal)
	}
}

// TestMCPGatewayUngrantedToolByName checks a call by name of a tool that the
// principal's grant does not hold: the session does not list it, and the call
// is lodge's insufficient_scope tool error, with nothing sent to the app. A
// grant widened before the list is read again lets the same call through
// (docs/specs/lodge-http.md, The MCP gateway).
func TestMCPGatewayUngrantedToolByName(t *testing.T) {
	h := newOAuthHarness(t)
	h.serveHearth(nil)
	client, tok := h.signin("pi-pocket", "mcp")
	h.grant(client.ClientID, "notelite", []oauth.Item{{Cap: "notelite:read"}})
	s := h.connectMCP(tok.AccessToken, nil)
	got := tools(t, s)
	if got["notelite_get_note"] == nil || got["notelite_create_note"] != nil {
		t.Fatalf("the session's tools are %v", toolNames(got))
	}
	res := callTool(t, s, "notelite_create_note", map[string]any{"folder": "/Work", "title": "x"})
	if !res.IsError {
		t.Fatalf("a call of an ungranted tool = %+v", res)
	}
	if want := "insufficient_scope: principal chani holds no notelite:write for app notelite: call request_access to ask for it"; resultText(t, res) != want {
		t.Errorf("the refusal = %q, want %q", resultText(t, res), want)
	}
	if principal, _ := h.hearth.LastCaller(); principal != "" {
		t.Errorf("the app saw a call outside the grant, from %q", principal)
	}
	// A grant widened without a sync's seeing it still makes the call: lodge
	// reads the grant, not the list.
	narrowTo(t, h, "chani", "notelite", []oauth.Item{{Cap: "notelite:read"}, {Cap: "notelite:write"}})
	if created := callTool(t, s, "notelite_create_note", map[string]any{"folder": "/Work", "title": "y"}); created.IsError {
		t.Fatalf("a call of a tool the widened grant holds: %s", resultText(t, created))
	}
	if principal, _ := h.hearth.LastCaller(); principal != "chani" {
		t.Errorf("the app saw %q, want chani", principal)
	}
}
