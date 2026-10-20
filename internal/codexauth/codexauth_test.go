package codexauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// issuer is a fake auth.openai.com: discovery, keys, authorize (which
// answers at once, as if the owner consented) and the token endpoint.
type issuer struct {
	t     *testing.T
	srv   *httptest.Server
	key   *rsa.PrivateKey
	now   time.Time
	scope string
	// deny makes authorize answer access_denied, and swap names another
	// client in the callback.
	deny bool
	swap string

	mu        sync.Mutex
	nonce     string
	challenge string
	redirect  string
	refresh   string // the live refresh token
	refreshes int
	authorize url.Values
}

func newIssuer(t *testing.T) *issuer {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	is := &issuer{t: t, key: k, now: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC), scope: Scope, refresh: "rt-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.MarshalWrite(w, map[string]string{"issuer": is.srv.URL, "jwks_uri": is.srv.URL + "/jwks"})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		e := big.NewInt(int64(k.E)).Bytes()
		_ = json.MarshalWrite(w, map[string]any{"keys": []map[string]string{
			{"kty": "RSA", "kid": "other", "n": "AQAB", "e": "AQAB"},
			{"kty": "RSA", "kid": "k1", "n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(e)},
		}})
	})
	mux.HandleFunc("GET /api/accounts/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		is.mu.Lock()
		is.authorize, is.nonce, is.challenge, is.redirect = q, q.Get("nonce"), q.Get("code_challenge"), q.Get("redirect_uri")
		is.mu.Unlock()
		back := url.Values{"state": {q.Get("state")}}
		if is.deny {
			back.Set("error", "access_denied")
		} else {
			back.Set("code", "code-1")
			client := q.Get("client_id")
			if client == DynamicClient {
				client = "oaiapp_new"
			}
			if is.swap != "" {
				client = is.swap
			}
			back.Set("client_id", client)
		}
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+back.Encode(), http.StatusFound)
	})
	mux.HandleFunc("POST /api/accounts/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f := r.PostForm
		is.mu.Lock()
		defer is.mu.Unlock()
		fail := func(code string) {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.MarshalWrite(w, map[string]string{"error": code})
		}
		if f.Get("resource") != Resource {
			fail("invalid_request")
			return
		}
		switch f.Get("grant_type") {
		case "authorization_code":
			if f.Get("code") != "code-1" || challenge(f.Get("code_verifier")) != is.challenge || f.Get("redirect_uri") != is.redirect {
				fail("invalid_grant")
				return
			}
			_ = json.MarshalWrite(w, map[string]any{"access_token": "at-1", "refresh_token": is.refresh, "expires_in": 3600,
				"scope": is.scope, "id_token": is.idToken(f.Get("client_id"), is.nonce), "earliest_refresh_at": is.now.Add(30 * time.Minute).Unix()})
		case "refresh_token":
			if f.Get("refresh_token") != is.refresh || f.Get("client_id") != "oaiapp_new" {
				fail("refresh_token_reused")
				return
			}
			is.refreshes++
			is.refresh = "rt-" + string(rune('1'+is.refreshes))
			_ = json.MarshalWrite(w, map[string]any{"access_token": "at-" + string(rune('1'+is.refreshes)), "refresh_token": is.refresh,
				"expires_in": 3600, "scope": is.scope})
		default:
			fail("unsupported_grant_type")
		}
	})
	is.srv = httptest.NewServer(mux)
	t.Cleanup(is.srv.Close)
	return is
}

func (is *issuer) endpoints() Endpoints {
	return Endpoints{Issuer: is.srv.URL, Authorize: is.srv.URL + "/api/accounts/authorize", Token: is.srv.URL + "/api/accounts/oauth/token"}
}

func (is *issuer) idToken(aud, nonce string) string {
	head, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1"})
	claims, _ := json.Marshal(map[string]any{"iss": is.srv.URL, "sub": "user-1", "aud": []string{aud}, "exp": is.now.Add(time.Hour).Unix(),
		"nonce": nonce, "email": "owner@example.com"})
	signed := base64.RawURLEncoding.EncodeToString(head) + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, is.key, crypto.SHA256, sum[:])
	if err != nil {
		is.t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// browser follows the authorize URL to the callback, as the owner's
// browser would; the callback answers before SignIn reads its result.
func browser(t *testing.T) func(string) {
	return func(u string) {
		resp, err := http.Get(u)
		if err != nil {
			t.Errorf("the browser: %v", err)
			return
		}
		resp.Body.Close()
	}
}

func (is *issuer) signIn(t *testing.T, o SignInOptions) (Login, error) {
	o.Endpoints, o.HTTP, o.Open, o.Port = is.endpoints(), is.srv.Client(), browser(t), -1
	o.Now = func() time.Time { return is.now }
	if o.HostID == "" {
		o.HostID = "urn:uuid:00000000-0000-4000-8000-000000000001"
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	return SignIn(ctx, o)
}

func TestSignInRegistersLodge(t *testing.T) {
	is := newIssuer(t)
	l, err := is.signIn(t, SignInOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if l.ClientID != "oaiapp_new" || l.Account != "user-1" || l.Email != "owner@example.com" || l.AccessToken != "at-1" ||
		l.RefreshToken != "rt-1" || !l.ExpiresAt.Equal(is.now.Add(time.Hour)) || !l.EarliestRefreshAt.Equal(is.now.Add(30*time.Minute)) {
		t.Fatalf("login = %+v", l)
	}
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
	a := is.authorize
	if a.Get("client_id") != DynamicClient || a.Get("agent_name_hint") != AgentName || a.Get("scope") != Scope ||
		a.Get("resource") != Resource || a.Get("code_challenge_method") != "S256" || a.Get("ext_agent_host_id") == "" ||
		!strings.HasPrefix(a.Get("redirect_uri"), "http://127.0.0.1:") || !strings.HasSuffix(a.Get("redirect_uri"), CallbackPath) {
		t.Errorf("the authorize request: %v", a)
	}
}

func TestSignInAgainKeepsTheClient(t *testing.T) {
	is := newIssuer(t)
	l, err := is.signIn(t, SignInOptions{ClientID: "oaiapp_new", Email: "owner@example.com"})
	if err != nil || l.ClientID != "oaiapp_new" {
		t.Fatalf("login %+v, %v", l, err)
	}
	if a := is.authorize; a.Get("client_id") != "oaiapp_new" || a.Has("agent_name_hint") || a.Get("login_hint") != "owner@example.com" {
		t.Errorf("the authorize request: %v", a)
	}
}

func TestSignInRefusals(t *testing.T) {
	t.Run("denied", func(t *testing.T) {
		is := newIssuer(t)
		is.deny = true
		if _, err := is.signIn(t, SignInOptions{}); err == nil || !strings.Contains(err.Error(), "access_denied") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no plan scope", func(t *testing.T) {
		is := newIssuer(t)
		is.scope = "openid profile email offline_access"
		if _, err := is.signIn(t, SignInOptions{}); err == nil || !strings.Contains(err.Error(), PlanScope) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("another client", func(t *testing.T) {
		is := newIssuer(t)
		is.swap = "oaiapp_other"
		if _, err := is.signIn(t, SignInOptions{ClientID: "oaiapp_new"}); err == nil || !strings.Contains(err.Error(), "oaiapp_other") {
			t.Fatalf("a callback for another client: %v", err)
		}
	})
}

func TestVerifyIDTokenRefusesForgeries(t *testing.T) {
	is := newIssuer(t)
	ep, hc := is.endpoints(), is.srv.Client()
	good := is.idToken("oaiapp_new", "n1")
	if _, err := VerifyIDToken(t.Context(), hc, ep, good, "oaiapp_new", "n1", is.now); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(good, ".")
	claims, _ := json.Marshal(map[string]any{"iss": is.srv.URL, "sub": "intruder", "aud": "oaiapp_new", "exp": is.now.Add(time.Hour).Unix(), "nonce": "n1"})
	for name, c := range map[string]struct {
		raw, client, nonce string
		now                time.Time
	}{
		"tampered":     {parts[0] + "." + base64.RawURLEncoding.EncodeToString(claims) + "." + parts[2], "oaiapp_new", "n1", is.now},
		"other client": {good, "oaiapp_x", "n1", is.now},
		"other nonce":  {good, "oaiapp_new", "n2", is.now},
		"expired":      {good, "oaiapp_new", "n1", is.now.Add(3 * time.Hour)},
		"not a JWT":    {"abc", "oaiapp_new", "n1", is.now},
		"wrong issuer": {good, "oaiapp_new", "n1", is.now},
	} {
		ep := ep
		if name == "wrong issuer" {
			ep.Issuer = is.srv.URL + "/"
		}
		if _, err := VerifyIDToken(t.Context(), hc, ep, c.raw, c.client, c.nonce, c.now); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
}

func TestRefreshRotatesAndSignsOut(t *testing.T) {
	is := newIssuer(t)
	l := Login{Type: Type, ClientID: "oaiapp_new", Account: "user-1", Scope: Scope, AccessToken: "at-1", RefreshToken: "rt-1",
		ExpiresAt: is.now.Add(time.Minute)}
	next, err := Refresh(t.Context(), is.srv.Client(), is.endpoints(), l, is.now)
	if err != nil {
		t.Fatal(err)
	}
	if next.AccessToken != "at-2" || next.RefreshToken != "rt-2" || !next.ExpiresAt.Equal(is.now.Add(time.Hour)) || next.ClientID != "oaiapp_new" {
		t.Fatalf("refreshed = %+v", next)
	}
	// The old refresh token is spent: using it again signs out.
	_, err = Refresh(t.Context(), is.srv.Client(), is.endpoints(), l, is.now)
	var out *SignedOutError
	if !errors.As(err, &out) || out.Code != "refresh_token_reused" {
		t.Fatalf("a reused refresh token: %v", err)
	}
	gone := next.SignOut()
	if gone.SignedIn() || gone.ClientID != "oaiapp_new" || gone.Email != next.Email {
		t.Errorf("signed out = %+v", gone)
	}
}

func TestDue(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		expires, earliest time.Duration
		want              bool
	}{
		{time.Hour, 0, false},
		{time.Minute, 0, true},
		{-time.Minute, 0, true},
		{time.Minute, 5 * time.Minute, false},    // not yet allowed, and still valid
		{5 * time.Second, 5 * time.Minute, true}, // about to expire: refresh anyway
	} {
		l := Login{ExpiresAt: now.Add(c.expires)}
		if c.earliest != 0 {
			l.EarliestRefreshAt = now.Add(c.earliest)
		}
		if got := l.Due(now); got != c.want {
			t.Errorf("expires in %v, earliest in %v: due %v", c.expires, c.earliest, got)
		}
	}
}

func TestValidateRefusesIncompleteLogins(t *testing.T) {
	good := Login{Type: Type, ClientID: "oaiapp_new", Account: "user-1", Scope: Scope, AccessToken: "a", RefreshToken: "r",
		ExpiresAt: time.Now()}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*Login){
		"type":    func(l *Login) { l.Type = "openai" },
		"client":  func(l *Login) { l.ClientID = DynamicClient },
		"account": func(l *Login) { l.Account = "" },
		"scope":   func(l *Login) { l.Scope = "openid" },
		"tokens":  func(l *Login) { l.RefreshToken = "" },
		"expiry":  func(l *Login) { l.ExpiresAt = time.Time{} },
	} {
		l := good
		mod(&l)
		if l.Validate() == nil {
			t.Errorf("%s: valid", name)
		}
	}
}

func TestHostIDIsStable(t *testing.T) {
	dir := t.TempDir()
	a, err := HostID(dir)
	if err != nil || !strings.HasPrefix(a, "urn:uuid:") || len(a) != len("urn:uuid:")+36 {
		t.Fatalf("host id %q, %v", a, err)
	}
	if b, _ := HostID(dir); b != a {
		t.Errorf("the host id changed: %s, then %s", a, b)
	}
}
