package oauth

import (
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ory/fosite"

	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/store"
)

// fakeApps are the installed apps of a test, by ID.
type fakeApps map[string]apps.App

func (f fakeApps) Get(id string) (apps.App, bool) {
	a, ok := f[id]
	return a, ok
}

// testServer is a Server over a temporary database, with app marks.
func testServer(t *testing.T) (*Server, *clock) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "lodge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	c := &clock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	marks := apps.App{ID: "marks", Manifest: apps.Manifest{ID: "marks", Name: "Marks"},
		Hearth: &apps.Hearth{Capabilities: []string{"marks:read", "marks:write"}, Selectors: []string{"folder"}}}
	plain := apps.App{ID: "plain", Manifest: apps.Manifest{ID: "plain", Name: "Plain"}}
	s, err := New(t.Context(), Options{DB: db, Apps: fakeApps{"marks": marks, "plain": plain},
		Issuer: func() string { return "https://lodge.example" }, Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

// clock is a test's clock.
type clock struct {
	t time.Time
}

func (c *clock) now() time.Time { return c.t }

// client inserts a client, and returns its ID.
func insertClient(t *testing.T, s *Server, id, name string, principal any) {
	t.Helper()
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO oauth_clients (id, name, redirect_uris, principal, scope, registered_at)
		VALUES (?, ?, '["https://client.example/cb"]', ?, 'mcp', ?)`, id, name, principal,
		stamp(s.now())); err != nil {
		t.Fatal(err)
	}
}

func TestValidPath(t *testing.T) {
	for _, p := range []string{"/", "/a", "/a/b", "/Reading/Inbox", "/a b", "/ünïcode", "/a-b_c.d"} {
		if !ValidPath(p) {
			t.Errorf("ValidPath(%q) = false", p)
		}
	}
	for name, p := range map[string]string{
		"empty":           "",
		"no leading /":    "a",
		"empty segment":   "/a//b",
		"dot":             "/a/./b",
		"dot dot":         "/a/../b",
		"trailing slash":  "/a/",
		"only a slash x2": "//",
		"NFC":             "/e\u0301", // e + combining acute, not NFC
		"too long":        "/" + strings.Repeat("a", MaxPath),
	} {
		if ValidPath(p) {
			t.Errorf("%s: ValidPath(%q) = true", name, p)
		}
	}
	// The NFC form itself is a path.
	if !ValidPath("/é") {
		t.Error("ValidPath of the composed form is false")
	}
}

func TestCovers(t *testing.T) {
	for _, c := range []struct {
		g, p string
		want bool
	}{
		{"/Reading", "/Reading", true},
		{"/Reading", "/Reading/Inbox", true},
		{"/Reading", "/Reading/Inbox/Deep", true},
		{"/Reading", "/Readings", false},
		{"/Reading", "/Reading2/Inbox", false},
		{"/Chani", "/Chan", false},
		{"/", "/anything", true},
		{"/", "/", true},
		{"/a/b", "/a", false},
	} {
		if got := Covers(c.g, c.p); got != c.want {
			t.Errorf("Covers(%q, %q) = %v, want %v", c.g, c.p, got, c.want)
		}
	}
	if Narrow("/Reading", "/Writing") {
		t.Error("Narrow admits a sibling path")
	}
	if !Narrow("/Reading", "/Reading/Inbox") {
		t.Error("Narrow refuses a descendant path")
	}
}

func TestRequestHash(t *testing.T) {
	access := Request{ID: "0123456789abcdef0123456789abcdef", Kind: KindAccess,
		Client: "lgc_abcdefghijklmnopqrstuv", Principal: "chani", App: "marks", Scopes: []string{},
		Items: []Item{{Cap: "marks:read", Sel: map[string]string{"folder": "/Reading"}}}}
	const canonicalAccess = `{"id":"0123456789abcdef0123456789abcdef","kind":"access",` +
		`"client":"lgc_abcdefghijklmnopqrstuv","principal":"chani","app":"marks","scopes":[],` +
		`"items":[{"cap":"marks:read","sel":{"folder":"/Reading"}}]}`
	b, err := access.canonical()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != canonicalAccess {
		t.Errorf("canonical =\n%s\nwant\n%s", b, canonicalAccess)
	}
	if got := access.Hash(); got != "7df8a16febd5a6889cb45789fc482eab8f88200c139fed8979ee0daadc0074ac" {
		t.Errorf("Hash() = %s", got)
	}
	// A sign-in sorts its scopes, and has no app and no items.
	signin := Request{ID: access.ID, Kind: KindSignin, Client: access.Client, Principal: "chani",
		Scopes: []string{"mcp", "events"}, Items: []Item{}}
	if b, _ := signin.canonical(); !strings.Contains(string(b), `"scopes":["events","mcp"],"items":[]`) ||
		!strings.Contains(string(b), `"app":""`) {
		t.Errorf("a sign-in's canonical = %s", b)
	}
	if got := signin.Hash(); got != "a42a8ee1af3dcadc6269006dd07835923e6aeb3d82a46f5fa290f5f55cc6f090" {
		t.Errorf("a sign-in's Hash() = %s", got)
	}
	// An item without a selector has no sel member.
	bare := Request{ID: access.ID, Kind: KindAccess, Items: []Item{{Cap: "marks:read"}}}
	if b, _ := bare.canonical(); !strings.Contains(string(b), `"items":[{"cap":"marks:read"}]`) {
		t.Errorf("an item without a selector = %s", b)
	}
	// A changed principal is another hash.
	other := access
	other.Principal = "bob"
	if other.Hash() == access.Hash() {
		t.Error("two requests with different principals have the same hash")
	}
}

// TestSessionStorage checks the session a row keeps.
func TestSessionStorage(t *testing.T) {
	s, _ := testServer(t)
	ctx := t.Context()
	sess := NewSession()
	sess.Subject = "chani"
	sess.SetExpiresAt(fosite.AccessToken, s.now().Add(time.Hour))
	sess.SetExpiresAt(fosite.RefreshToken, s.now().Add(RefreshTokenLifespan))
	insertClient(t, s, "lgc_test", "test", "chani")
	req := fosite.NewRequest()
	req.ID = "request-1"
	req.RequestedAt = s.now()
	client, err := s.store.GetClient(ctx, "lgc_test")
	if err != nil {
		t.Fatal(err)
	}
	req.Client = client
	req.Session = sess
	req.GrantedScope = fosite.Arguments{"mcp"}
	if err := s.store.CreateRefreshTokenSession(ctx, "signature", "at-signature", req); err != nil {
		t.Fatal(err)
	}
	got, err := s.store.GetRefreshTokenSession(ctx, "signature", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.GetID() != "request-1" || got.GetSession().GetSubject() != "chani" ||
		!slices.Equal([]string(got.GetGrantedScopes()), []string{"mcp"}) ||
		got.GetClient().GetID() != "lgc_test" {
		t.Errorf("the stored request = %+v", got)
	}
	// Rotating the family inactivates the refresh row.
	if err := s.store.RotateRefreshToken(ctx, "request-1", "signature"); err != nil {
		t.Fatal(err)
	}
	inactive, err := s.store.GetRefreshTokenSession(ctx, "signature", nil)
	if !errors.Is(err, fosite.ErrInactiveToken) || inactive == nil || inactive.GetID() != "request-1" {
		t.Errorf("an inactive refresh token = %+v, %v", inactive, err)
	}
	if _, err := s.store.GetRefreshTokenSession(ctx, "missing", nil); !errors.Is(err, fosite.ErrNotFound) {
		t.Errorf("an unknown refresh token = %v, want ErrNotFound", err)
	}
}

// TestCodeStorage checks the code's session, and its single use.
func TestCodeStorage(t *testing.T) {
	s, _ := testServer(t)
	ctx := t.Context()
	insertClient(t, s, "lgc_test", "test", "chani")
	client, err := s.store.GetClient(ctx, "lgc_test")
	if err != nil {
		t.Fatal(err)
	}
	req := fosite.NewRequest()
	req.ID = "request-1"
	req.Client = client
	req.Session = NewSession()
	req.Session.SetExpiresAt(fosite.AuthorizeCode, s.now().Add(AuthorizeCodeLifespan))
	if err := s.store.CreateAuthorizeCodeSession(ctx, "code-sig", req); err != nil {
		t.Fatal(err)
	}
	got, err := s.store.GetAuthorizeCodeSession(ctx, "code-sig", nil)
	if err != nil || got.GetID() != "request-1" {
		t.Fatalf("the code's session = %+v, %v", got, err)
	}
	if err := s.store.InvalidateAuthorizeCodeSession(ctx, "code-sig"); err != nil {
		t.Fatal(err)
	}
	invalid, err := s.store.GetAuthorizeCodeSession(ctx, "code-sig", nil)
	if !errors.Is(err, fosite.ErrInvalidatedAuthorizeCode) || invalid == nil {
		t.Errorf("an invalidated code = %+v, %v", invalid, err)
	}
	if err := s.store.InvalidateAuthorizeCodeSession(ctx, "missing"); !errors.Is(err, fosite.ErrNotFound) {
		t.Errorf("invalidating an unknown code = %v", err)
	}
}

// TestTransaction checks that fosite's transaction covers its queries.
func TestTransaction(t *testing.T) {
	s, _ := testServer(t)
	ctx := t.Context()
	insertClient(t, s, "lgc_test", "test", "chani")
	client, err := s.store.GetClient(ctx, "lgc_test")
	if err != nil {
		t.Fatal(err)
	}
	req := fosite.NewRequest()
	req.ID = "request-1"
	req.Client = client
	req.Session = NewSession()
	tx, err := s.store.BeginTX(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.CreateAccessTokenSession(tx, "sig", req); err != nil {
		t.Fatal(err)
	}
	// Rolled back, the row is gone.
	if err := s.store.Rollback(tx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.GetAccessTokenSession(ctx, "sig", nil); !errors.Is(err, fosite.ErrNotFound) {
		t.Errorf("a rolled back row = %v", err)
	}
	// Committed, it stays.
	tx, err = s.store.BeginTX(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.CreateAccessTokenSession(tx, "sig", req); err != nil {
		t.Fatal(err)
	}
	// A nested transaction joins the outer one.
	inner, err := s.store.BeginTX(tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.Commit(inner); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Commit(tx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.GetAccessTokenSession(ctx, "sig", nil); err != nil {
		t.Errorf("a committed row: %v", err)
	}
}

// TestSweep checks what lodge deletes every hour.
func TestSweep(t *testing.T) {
	s, c := testServer(t)
	ctx := t.Context()
	// A session that expired, and one that did not.
	insertClient(t, s, "lgc_live", "live", "chani")
	insertClient(t, s, "lgc_unbound", "unbound", nil)
	client, err := s.store.GetClient(ctx, "lgc_live")
	if err != nil {
		t.Fatal(err)
	}
	for name, expires := range map[string]time.Time{"old": c.t.Add(-time.Minute), "new": c.t.Add(time.Hour)} {
		req := fosite.NewRequest()
		req.ID = name
		req.Client = client
		req.Session = NewSession()
		req.Session.SetExpiresAt(fosite.AccessToken, expires)
		if err := s.store.CreateAccessTokenSession(ctx, name, req); err != nil {
			t.Fatal(err)
		}
	}
	// An unbound client registered 25 hours ago.
	if _, err := s.db.ExecContext(ctx, `UPDATE oauth_clients SET registered_at = ? WHERE id = 'lgc_unbound'`,
		stamp(c.t.Add(-25*time.Hour))); err != nil {
		t.Fatal(err)
	}
	// A request decided 25 hours ago, and one decided now.
	for id, decided := range map[string]time.Time{"old": c.t.Add(-25 * time.Hour), "new": c.t.Add(-time.Minute)} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO consent_requests (id, kind, client_id, principal, app, scopes, items, query, created_at, expires_at, decided_at, decision)
			VALUES (?, 'signin', 'lgc_live', 'chani', '', '[]', '[]', '', ?, ?, ?, 'approve')`,
			id, stamp(c.t), stamp(c.t.Add(time.Hour)), stamp(decided)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.sweep(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_sessions WHERE signature = 'old'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("the expired session is still there: %d, %v", n, err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_sessions WHERE signature = 'new'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("the live session is gone: %d, %v", n, err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_clients WHERE id = 'lgc_unbound'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("the old unbound client is still there: %d, %v", n, err)
	}
	if _, err := s.store.GetClient(ctx, "lgc_live"); err != nil {
		t.Errorf("a bound client went with the unbound ones: %v", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM consent_requests WHERE id = 'old'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("the old decided request is still there: %d, %v", n, err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM consent_requests WHERE id = 'new'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("the recently decided request is gone: %d, %v", n, err)
	}
}

// TestKeys checks that lodge makes its two keys once.
func TestKeys(t *testing.T) {
	s, _ := testServer(t)
	if len(s.tokensKey) != 32 || len(s.pagesKey) != 32 || string(s.tokensKey) == string(s.pagesKey) {
		t.Fatalf("keys %x %x", s.tokensKey, s.pagesKey)
	}
	again, err := New(t.Context(), Options{DB: s.db, Apps: fakeApps{}, Issuer: func() string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	if string(again.tokensKey) != string(s.tokensKey) || string(again.pagesKey) != string(s.pagesKey) {
		t.Error("a second start makes other keys")
	}
}

// TestCSRF checks the two CSRF tokens.
func TestCSRF(t *testing.T) {
	s, c := testServer(t)
	token := s.requestCSRF("abc")
	if token == "" || token == s.requestCSRF("other") {
		t.Fatal("a request's CSRF token is empty or shared")
	}
	if err := s.checkRequestCSRF("abc", token); err != nil {
		t.Errorf("its own token: %v", err)
	}
	if err := s.checkRequestCSRF("abc", token[:len(token)-1]+"x"); err == nil {
		t.Error("another token passes")
	}
	ts := c.t.Unix()
	page := s.pageCSRF("alice@example.com", ts)
	if err := s.checkPageCSRF("alice@example.com", page, decimal(ts)); err != nil {
		t.Errorf("the page's own token: %v", err)
	}
	if err := s.checkPageCSRF("bob@example.com", page, decimal(ts)); err == nil {
		t.Error("another admin's token passes")
	}
	// It is good from a minute before its time until an hour after it.
	c.t = c.t.Add(-pageSkew + time.Second)
	if err := s.checkPageCSRF("alice@example.com", page, decimal(ts)); err != nil {
		t.Errorf("a token a minute early: %v", err)
	}
	c.t = c.t.Add(-2 * pageSkew)
	if err := s.checkPageCSRF("alice@example.com", page, decimal(ts)); err == nil {
		t.Error("a token from two minutes before its time passes")
	}
	c.t = time.Unix(ts, 0).Add(pageLifetime + time.Second)
	if err := s.checkPageCSRF("alice@example.com", page, decimal(ts)); err == nil {
		t.Error("an expired token passes")
	}
}

// decimal is the decimal text of a page's time.
func decimal(n int64) string { return strconv.FormatInt(n, 10) }

// TestRequestAccess checks what an access request refuses.
func TestRequestAccess(t *testing.T) {
	s, _ := testServer(t)
	ctx := t.Context()
	insertClient(t, s, "lgc_bound", "bound", "chani")
	if err := s.makePrincipal(ctx, "chani"); err != nil {
		t.Fatal(err)
	}
	insertClient(t, s, "lgc_unbound", "unbound", nil)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO principals (name, created_at) VALUES ('gone', ?)`, stamp(s.now())); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE principals SET revoked_at = ? WHERE name = 'gone'`, stamp(s.now())); err != nil {
		t.Fatal(err)
	}
	insertClient(t, s, "lgc_gone", "gone", "gone")
	items := []Item{{Cap: "marks:read", Sel: map[string]string{"folder": "/Reading"}}}
	if req, err := s.RequestAccess(ctx, "lgc_bound", "marks", items); err != nil || req.ID == "" ||
		req.URL != "https://lodge.example/oauth/consent/"+req.ID {
		t.Fatalf("a good request = %+v, %v", req, err)
	}
	for name, c := range map[string]struct {
		client, app string
		items       []Item
		want        error
	}{
		"unknown client":     {"lgc_none", "marks", items, ErrNotFound},
		"unbound client":     {"lgc_unbound", "marks", items, ErrNotFound},
		"revoked principal":  {"lgc_gone", "marks", items, ErrNotFound},
		"unknown app":        {"lgc_bound", "nope", items, ErrNotFound},
		"app without hearth": {"lgc_bound", "plain", items, ErrNotFound},
		"unknown capability": {"lgc_bound", "marks", []Item{{Cap: "marks:admin"}}, ErrInvalid},
		"unknown selector":   {"lgc_bound", "marks", []Item{{Cap: "marks:read", Sel: map[string]string{"tag": "/a"}}}, ErrInvalid},
		"bad path":           {"lgc_bound", "marks", []Item{{Cap: "marks:read", Sel: map[string]string{"folder": "Reading"}}}, ErrInvalid},
		"no items":           {"lgc_bound", "marks", nil, ErrInvalid},
		"too many items": {"lgc_bound", "marks", []Item{
			{Cap: "marks:read"}, {Cap: "marks:read"}, {Cap: "marks:read"}, {Cap: "marks:read"},
			{Cap: "marks:read"}, {Cap: "marks:read"}, {Cap: "marks:read"}, {Cap: "marks:read"},
			{Cap: "marks:read"}, {Cap: "marks:read"}, {Cap: "marks:read"}, {Cap: "marks:read"},
			{Cap: "marks:read"}, {Cap: "marks:read"}, {Cap: "marks:read"}, {Cap: "marks:read"},
			{Cap: "marks:read"}, {Cap: "marks:read"}, {Cap: "marks:read"}, {Cap: "marks:read"},
			{Cap: "marks:read"}, {Cap: "marks:read"}, {Cap: "marks:read"}, {Cap: "marks:read"},
			{Cap: "marks:read"}, {Cap: "marks:read"}, {Cap: "marks:read"}, {Cap: "marks:read"},
			{Cap: "marks:read"}, {Cap: "marks:read"}, {Cap: "marks:read"}, {Cap: "marks:read"},
			{Cap: "marks:read"}}, ErrInvalid},
	} {
		if _, err := s.RequestAccess(ctx, c.client, c.app, c.items); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
}

// TestRequestAccessPending checks that at most 64 requests are pending.
func TestRequestAccessPending(t *testing.T) {
	s, _ := testServer(t)
	ctx := t.Context()
	insertClient(t, s, "lgc_bound", "bound", "chani")
	if err := s.makePrincipal(ctx, "chani"); err != nil {
		t.Fatal(err)
	}
	items := []Item{{Cap: "marks:read"}}
	for i := range MaxPending {
		if _, err := s.RequestAccess(ctx, "lgc_bound", "marks", items); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if _, err := s.RequestAccess(ctx, "lgc_bound", "marks", items); !errors.Is(err, ErrTooMany) {
		t.Errorf("the 65th pending request = %v, want ErrTooMany", err)
	}
	// A decided request is no longer pending.
	pending, err := s.pendingRequests(ctx)
	if err != nil || len(pending) != MaxPending {
		t.Fatalf("the pending requests = %d, %v", len(pending), err)
	}
	if ok, err := s.decide(ctx, pending[0].ID, DecisionDecline); !ok || err != nil {
		t.Fatalf("decide = %v, %v", ok, err)
	}
	if _, err := s.RequestAccess(ctx, "lgc_bound", "marks", items); err != nil {
		t.Errorf("a request after one was decided: %v", err)
	}
	// A request is decided once.
	if ok, _ := s.decide(ctx, pending[0].ID, DecisionApprove); ok {
		t.Error("a decided request is decided twice")
	}
}

// TestCheck checks what a token gives, and what it does not.
func TestCheck(t *testing.T) {
	s, _ := testServer(t)
	ctx := t.Context()
	if _, err := s.Check(ctx, "", "mcp"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an empty token = %v", err)
	}
	if _, err := s.Check(ctx, "lgo_at_nonsense", "mcp"); err == nil {
		t.Error("a made-up token passes")
	}
}

// TestGrantAdd checks that a grant keeps its items, without duplicates.
func TestGrantAdd(t *testing.T) {
	s, _ := testServer(t)
	ctx := t.Context()
	if err := s.makePrincipal(ctx, "chani"); err != nil {
		t.Fatal(err)
	}
	if granted, err := s.Grant(ctx, "chani", "marks"); err != nil || len(granted) != 0 {
		t.Fatalf("an absent grant = %+v, %v", granted, err)
	}
	one := Item{Cap: "marks:read", Sel: map[string]string{"folder": "/Reading"}}
	if err := s.addGrant(ctx, "chani", "marks", []Item{one}); err != nil {
		t.Fatal(err)
	}
	if err := s.addGrant(ctx, "chani", "marks", []Item{one, {Cap: "marks:write"}}); err != nil {
		t.Fatal(err)
	}
	granted, err := s.Grant(ctx, "chani", "marks")
	if err != nil || len(granted) != 2 || granted[1].Cap != "marks:write" {
		t.Errorf("Grant = %+v, %v", granted, err)
	}
	// A narrowed path is another item.
	narrowed := Item{Cap: "marks:read", Sel: map[string]string{"folder": "/Reading/Inbox"}}
	if err := s.addGrant(ctx, "chani", "marks", []Item{narrowed}); err != nil {
		t.Fatal(err)
	}
	if granted, _ := s.Grant(ctx, "chani", "marks"); len(granted) != 3 {
		t.Errorf("a narrowed path did not add an item: %+v", granted)
	}
}

// TestProposePrincipal checks the principal lodge suggests for a name.
func TestProposePrincipal(t *testing.T) {
	for name, want := range map[string]string{
		"pi-pocket":             "pi-pocket",
		"Chani":                 "chani",
		"Chani's Agent":         "chanisagent",
		"1password":             "",
		"-dash":                 "",
		"":                      "",
		strings.Repeat("a", 40): strings.Repeat("a", 32),
		"Notes (laptop)":        "noteslaptop",
	} {
		if got := ProposePrincipal(name); got != want {
			t.Errorf("ProposePrincipal(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestPagePolicy checks the consent page's policy, with and without a
// redirect origin.
func TestPagePolicy(t *testing.T) {
	if got := PagePolicy(""); !strings.Contains(got, "form-action 'self';") ||
		!strings.Contains(got, "frame-ancestors 'none'") || !strings.Contains(got, "default-src 'none'") ||
		strings.Contains(got, "script-src") {
		t.Errorf("PagePolicy(\"\") = %q", got)
	}
	if got := PagePolicy("http://127.0.0.1:1234"); !strings.Contains(got, "form-action 'self' http://127.0.0.1:1234;") {
		t.Errorf("PagePolicy(origin) = %q", got)
	}
}

// TestNoTokenInRows checks that no row holds a token, a code or a CSRF token.
func TestNoTokenInRows(t *testing.T) {
	s, _ := testServer(t)
	ctx := t.Context()
	insertClient(t, s, "lgc_bound", "bound", "chani")
	client, err := s.store.GetClient(ctx, "lgc_bound")
	if err != nil {
		t.Fatal(err)
	}
	req := fosite.NewRequest()
	req.ID = "request-1"
	req.Client = client
	sess := NewSession()
	sess.Subject = "chani"
	sess.SetExpiresAt(fosite.AccessToken, s.now().Add(AccessTokenLifespan))
	req.Session = sess
	access, signature, err := s.strategy.GenerateAccessToken(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.CreateAccessTokenSession(ctx, signature, req); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT signature, form, session FROM oauth_sessions`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var sig, form, sessionText string
		if err := rows.Scan(&sig, &form, &sessionText); err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{sig, form, sessionText} {
			if strings.Contains(text, access) {
				t.Errorf("a row holds the access token: %q", text)
			}
		}
	}
	if !strings.HasPrefix(access, accessTokenPrefix) {
		t.Errorf("the token %q has no lodge prefix", access)
	}
	// A CSRF token is in no row either.
	csrf := s.requestCSRF("request-1")
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM consent_requests WHERE id = ? OR scopes = ? OR items = ?`,
		csrf, csrf, csrf).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d requests hold a CSRF token", n)
	}
}

// TestRedirectURIs checks the redirect URIs lodge takes, and the origin a
// page's Content-Security-Policy may hold.
func TestRedirectURIs(t *testing.T) {
	for _, uri := range []string{
		"https://client.example/cb",
		"https://a.example:8443/cb?x=1",
		"https://127.0.0.1/cb",
		"https://[::1]:99/cb",
		"http://127.0.0.1:1234/callback",
		"http://[::1]/callback",
		"http://localhost:7/cb",
		"https://sub.domain.example/cb",
	} {
		if err := checkRedirectURI(uri); err != nil {
			t.Errorf("checkRedirectURI(%q) = %v", uri, err)
		}
	}
	for _, uri := range []string{
		"https://a;script-src/cb",
		"https://a'b/cb",
		"https://a,b/cb",
		"https://user@example.com/cb",
		"https://a b/cb",
		"https://a..example/cb",
		"https://-a.example/cb",
		"https://a.example:80x/cb",
		"https:///cb",
		"https://client.example/cb#f",
		"ftp://client.example/cb",
		"http://client.example/cb",
	} {
		if err := checkRedirectURI(uri); err == nil {
			t.Errorf("checkRedirectURI(%q) = nil", uri)
		}
	}
	// The origin of a page's policy is built only from such a URI.
	if origin, err := redirectOrigin("https://client.example:8443/cb"); err != nil || origin != "https://client.example:8443" {
		t.Errorf("redirectOrigin = %q, %v", origin, err)
	}
	if origin, err := redirectOrigin("http://[::1]:9/callback"); err != nil || origin != "http://[::1]:9" {
		t.Errorf("redirectOrigin of a loopback client = %q, %v", origin, err)
	}
	for _, uri := range []string{"https://a;script-src/cb", "https://a'b/cb", "https://user@example.com/cb"} {
		if origin, err := redirectOrigin(uri); err == nil {
			t.Errorf("redirectOrigin(%q) = %q", uri, origin)
		}
	}
}
