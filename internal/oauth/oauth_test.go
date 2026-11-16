package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	if err := s.makePrincipal(ctx, s.db, "chani"); err != nil {
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
	if err := s.makePrincipal(ctx, s.db, "chani"); err != nil {
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
	if ok, err := s.decide(ctx, s.db, pending[0].ID, DecisionDecline); !ok || err != nil {
		t.Fatalf("decide = %v, %v", ok, err)
	}
	if _, err := s.RequestAccess(ctx, "lgc_bound", "marks", items); err != nil {
		t.Errorf("a request after one was decided: %v", err)
	}
	// A request is decided once.
	if ok, _ := s.decide(ctx, s.db, pending[0].ID, DecisionApprove); ok {
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
	if err := s.makePrincipal(ctx, s.db, "chani"); err != nil {
		t.Fatal(err)
	}
	if granted, err := s.Grant(ctx, "chani", "marks"); err != nil || len(granted) != 0 {
		t.Fatalf("an absent grant = %+v, %v", granted, err)
	}
	one := Item{Cap: "marks:read", Sel: map[string]string{"folder": "/Reading"}}
	if err := s.addGrant(ctx, s.db, "chani", "marks", []Item{one}); err != nil {
		t.Fatal(err)
	}
	if err := s.addGrant(ctx, s.db, "chani", "marks", []Item{one, {Cap: "marks:write"}}); err != nil {
		t.Fatal(err)
	}
	granted, err := s.Grant(ctx, "chani", "marks")
	if err != nil || len(granted) != 2 || granted[1].Cap != "marks:write" {
		t.Errorf("Grant = %+v, %v", granted, err)
	}
	// A narrowed path is another item.
	narrowed := Item{Cap: "marks:read", Sel: map[string]string{"folder": "/Reading/Inbox"}}
	if err := s.addGrant(ctx, s.db, "chani", "marks", []Item{narrowed}); err != nil {
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

// flow drives a Server through its own handlers, as the gateway drives it:
// registration, the authorization, the consent page, the decision and the
// token endpoint.
type flow struct {
	t        *testing.T
	s        *Server
	clientID string
}

// result is a handler's answer.
type result struct {
	status int
	header http.Header
	body   string
}

// newFlow builds a Server, and registers a client on it.
func newFlow(t *testing.T) *flow {
	t.Helper()
	s, _ := testServer(t)
	f := &flow{t: t, s: s}
	r := httptest.NewRequest(http.MethodPost, "/oauth/register",
		strings.NewReader(`{"client_name":"pi-pocket","redirect_uris":["https://client.example/cb"]}`))
	r.Header.Set("Content-Type", "application/json")
	a := f.serve(r, RouteRegister, "", "")
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := jsonUnmarshal([]byte(a.body), &out); err != nil || a.status != http.StatusCreated {
		t.Fatalf("register: %d %q", a.status, a.body)
	}
	f.clientID = out.ClientID
	return f
}

// serve runs one of the Server's handlers over r.
func (f *flow) serve(r *http.Request, route, id, admin string) result {
	f.t.Helper()
	w := httptest.NewRecorder()
	if route == RouteAuthorize || route == RouteConsent || route == RouteGrants {
		f.s.ServeConsent(w, r, route, id, admin)
	} else {
		f.s.ServeOAuth(w, r, route)
	}
	return result{w.Code, w.Header(), w.Body.String()}
}

// form builds a form POST with a body.
func formRequest(method, path, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return r
}

// signin makes a sign-in request, and reads its page: the request's ID, its
// CSRF token, its hash and the page.
func (f *flow) signin(scopes string) (id, csrf, hash, page string) {
	f.t.Helper()
	q := url.Values{"client_id": {f.clientID}, "redirect_uri": {redirectURI}, "response_type": {"code"},
		"scope": {scopes}, "state": {"st-1234567890"}, "code_challenge": {challenge(verifier)},
		"code_challenge_method": {"S256"}}
	a := f.serve(formRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), ""), RouteAuthorize, "", adminLogin)
	id, ok := strings.CutPrefix(a.header.Get("Location"), "/oauth/consent/")
	if a.status != http.StatusSeeOther || !ok {
		f.t.Fatalf("authorize: %d %q %q", a.status, a.header.Get("Location"), a.body)
	}
	pageResult := f.serve(formRequest(http.MethodGet, "/oauth/consent/"+id, ""), RouteConsent, id, adminLogin)
	if pageResult.status != http.StatusOK {
		f.t.Fatalf("the consent page: %d %q", pageResult.status, pageResult.body)
	}
	return id, field(pageResult.body, "csrf"), field(pageResult.body, "hash"), pageResult.body
}

// approve decides a request as approved.
func (f *flow) approve(id, csrf, hash string, fields url.Values) result {
	f.t.Helper()
	form := url.Values{"csrf": {csrf}, "hash": {hash}, "decision": {DecisionApprove}}
	for k, v := range fields {
		form[k] = v
	}
	return f.serve(formRequest(http.MethodPost, "/oauth/consent/"+id, form.Encode()), RouteConsent, id, adminLogin)
}

// token posts to the token endpoint.
func (f *flow) token(form url.Values) result {
	f.t.Helper()
	return f.serve(formRequest(http.MethodPost, "/oauth/token", form.Encode()), RouteToken, "", "")
}

// signinFlow runs a sign-in and its exchange, and returns the tokens.
func (f *flow) signinFlow(principal string, scopes ...string) (code string, tok tokenJSON) {
	f.t.Helper()
	id, csrf, hash, _ := f.signin(strings.Join(scopes, " "))
	fields := url.Values{"principal": {principal}}
	for _, sc := range scopes {
		fields.Add("scope", sc)
	}
	approved := f.approve(id, csrf, hash, fields)
	if approved.status != http.StatusSeeOther {
		f.t.Fatalf("the approval: %d %q", approved.status, approved.body)
	}
	location, err := url.Parse(approved.header.Get("Location"))
	if err != nil || location.Query().Get("code") == "" {
		f.t.Fatalf("the approval's Location %q", approved.header.Get("Location"))
	}
	code = location.Query().Get("code")
	exchanged := f.token(url.Values{"grant_type": {"authorization_code"}, "client_id": {f.clientID},
		"code": {code}, "redirect_uri": {redirectURI}, "code_verifier": {verifier}})
	if exchanged.status != http.StatusOK {
		f.t.Fatalf("the exchange: %d %q", exchanged.status, exchanged.body)
	}
	if err := jsonUnmarshal([]byte(exchanged.body), &tok); err != nil {
		f.t.Fatal(err)
	}
	return code, tok
}

// tokenJSON is a token endpoint answer.
type tokenJSON struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
}

// verifier is a PKCE code verifier, of 43 characters.
const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

// challenge is the S256 PKCE challenge of a verifier.
func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// redirectURI is the registered redirect URI of a flow's client.
const redirectURI = "https://client.example/cb"

// adminLogin is the login the flow names as an admin.
const adminLogin = "alice@example.com"

// TestNoTokenInRows runs the real flows, and checks that no row of any OAuth
// table holds a code, an access token, a refresh token, a code verifier or a
// CSRF token: only their signatures and hashes.
func TestNoTokenInRows(t *testing.T) {
	f := newFlow(t)
	ctx := t.Context()
	code, tok := f.signinFlow("chani", "mcp")
	// A refresh, and an access request with its page.
	refreshed := f.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {f.clientID},
		"refresh_token": {tok.RefreshToken}})
	if refreshed.status != http.StatusOK {
		t.Fatalf("the refresh: %d %q", refreshed.status, refreshed.body)
	}
	var second tokenJSON
	if err := jsonUnmarshal([]byte(refreshed.body), &second); err != nil {
		t.Fatal(err)
	}
	req, err := f.s.RequestAccess(ctx, f.clientID, "marks", []Item{{Cap: "marks:read", Sel: map[string]string{"folder": "/Reading"}}})
	if err != nil {
		t.Fatal(err)
	}
	// A sign-in with its page, which holds the request's CSRF token and hash.
	_, signinCSRF, signinHash, signinPage := f.signin("mcp")
	if !strings.Contains(signinPage, signinCSRF) || !strings.Contains(signinPage, signinHash) {
		t.Fatal("the sign-in's page does not hold its CSRF token and hash")
	}
	accessCSRF := f.s.requestCSRF(req.ID)
	approved := f.approve(req.ID, accessCSRF, req.Hash(), url.Values{"item": {"0"},
		"path.0.folder": {"/Reading"}})
	if approved.status != http.StatusSeeOther {
		t.Fatalf("the access approval: %d %q", approved.status, approved.body)
	}

	// The rows the flows made are there.
	for _, kind := range []string{kindCode, kindAccess, kindRefresh, kindPKCE} {
		var n int
		if err := f.s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_sessions WHERE kind = ?`, kind).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Errorf("no %s session was stored", kind)
		}
	}
	var requests int
	if err := f.s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM consent_requests`).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if requests < 2 {
		t.Errorf("%d consent requests were stored, want the sign-in and the access request", requests)
	}
	var granted int
	if err := f.s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM grants`).Scan(&granted); err != nil {
		t.Fatal(err)
	}
	if granted == 0 {
		t.Error("no grant was stored")
	}

	// No TEXT column of any OAuth table holds a secret.
	secrets := map[string]string{
		"the code":           code,
		"the access token":   tok.AccessToken,
		"the refresh token":  tok.RefreshToken,
		"the second access":  second.AccessToken,
		"the second refresh": second.RefreshToken,
		"the code verifier":  verifier,
		"the CSRF token":     signinCSRF,
	}
	for _, table := range []string{"oauth_sessions", "oauth_clients", "consent_requests", "grants", "principals"} {
		for where, text := range tableText(t, f.s, table) {
			for name, secret := range secrets {
				if secret == "" || !strings.Contains(text, secret) {
					continue
				}
				t.Errorf("%s holds %s", where, name)
			}
		}
	}
}

// tableText returns the text of every TEXT column of every row of table, by
// a name that says where it was.
func tableText(t *testing.T, s *Server, table string) map[string]string {
	t.Helper()
	rows, err := s.db.QueryContext(t.Context(), `SELECT * FROM `+table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	values := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range values {
		ptrs[i] = &values[i]
	}
	out := map[string]string{}
	for row := 0; rows.Next(); row++ {
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for i, v := range values {
			var text string
			switch x := v.(type) {
			case string:
				text = x
			case []byte:
				text = string(x)
			default:
				continue
			}
			out[fmt.Sprintf("%s row %d column %s", table, row, cols[i])] = text
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
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

// steppingClock advances 1 second on every read, so that 2 reads of it in
// one page disagree.
type steppingClock struct{ t time.Time }

func (c *steppingClock) now() time.Time {
	c.t = c.t.Add(time.Second)
	return c.t
}

// TestGrantsPageOneClockRead checks that a page's time and its CSRF token
// come from 1 read of the clock, so that they agree: a revocation with the
// pair the page holds works.
func TestGrantsPageOneClockRead(t *testing.T) {
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "lodge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	c := &steppingClock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	s, err := New(t.Context(), Options{DB: db, Apps: fakeApps{}, Issuer: func() string { return "https://lodge.example" }, Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	insertClient(t, s, "lgc_test", "test", "chani")
	if err := s.makePrincipal(t.Context(), s.db, "chani"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.grantsPage(rec, httptest.NewRequest(http.MethodGet, "/oauth/grants", nil), "alice@example.com")
	if rec.Code != http.StatusOK {
		t.Fatalf("the grants page: %d %q", rec.Code, rec.Body.String())
	}
	csrf, ts := field(rec.Body.String(), "csrf"), field(rec.Body.String(), "ts")
	if csrf == "" || ts == "" {
		t.Fatalf("the page holds no CSRF token or no time: %q", rec.Body.String())
	}
	if err := s.checkPageCSRF("alice@example.com", csrf, ts); err != nil {
		t.Errorf("the page's token and time disagree: %v", err)
	}
	form := url.Values{"csrf": {csrf}, "ts": {ts}, "client": {"lgc_test"}}
	r := httptest.NewRequest(http.MethodPost, "/oauth/grants/revoke", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.revokeTarget(w, r, "alice@example.com")
	if w.Code != http.StatusSeeOther {
		t.Errorf("the revocation: %d %q", w.Code, w.Body.String())
	}
}

// field is a hidden field's value in a page.
func field(page, name string) string {
	m := regexp.MustCompile(`name="` + name + `" value="([^"]+)"`).FindStringSubmatch(page)
	if m == nil {
		return ""
	}
	return m[1]
}

// TestNestedRollback checks that a rollback inside a transaction poisons it:
// the outer commit rolls back, and says so.
func TestNestedRollback(t *testing.T) {
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
	outer, err := s.store.BeginTX(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := s.store.BeginTX(outer)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.CreateAccessTokenSession(inner, "sig", req); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Rollback(inner); err != nil {
		t.Fatalf("the inner rollback = %v", err)
	}
	if err := s.store.Commit(outer); err == nil {
		t.Fatal("the outer commit of a poisoned transaction = nil")
	}
	if _, err := s.store.GetAccessTokenSession(ctx, "sig", nil); !errors.Is(err, fosite.ErrNotFound) {
		t.Errorf("the write of a poisoned transaction = %v, want it gone", err)
	}
}

// TestReadsUseTheTransaction checks that a read inside fosite's transaction
// goes through it: it sees the transaction's own write, and it finishes on a
// pool of 1 connection, which a queued read could not.
func TestReadsUseTheTransaction(t *testing.T) {
	s, _ := testServer(t)
	ctx := t.Context()
	s.db.SetMaxOpenConns(1)
	insertClient(t, s, "lgc_test", "test", nil)
	tx, err := s.store.BeginTX(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The write is in the transaction, and so is the read that sees it.
	if _, err := s.store.at(tx, nil).ExecContext(tx,
		`UPDATE oauth_clients SET principal = 'chani' WHERE id = 'lgc_test'`); err != nil {
		t.Fatal(err)
	}
	seen, cancel := context.WithTimeout(tx, 10*time.Second)
	defer cancel()
	c, err := s.store.GetClient(seen, "lgc_test")
	if err != nil {
		t.Fatalf("GetClient inside a transaction: %v", err)
	}
	if c.GetID() != "lgc_test" {
		t.Errorf("GetClient = %v", c)
	}
	// A client row read in the transaction sees its write, and one that
	// reads it bound.
	row, err := s.store.clientRowAt(seen, nil, "lgc_test")
	if err != nil || row.Principal != "chani" {
		t.Errorf("the client row in the transaction = %+v, %v", row, err)
	}
	if err := s.store.Commit(tx); err != nil {
		t.Fatal(err)
	}
	// After the commit, everyone sees it.
	after, err := s.store.clientRow(ctx, "lgc_test")
	if err != nil || after.Principal != "chani" {
		t.Errorf("the client row after the commit = %+v, %v", after, err)
	}
}

// TestTokenSingleUseIsAtomic checks the storage's own view of a token used
// twice: the conditional update fails, the error says the token was reused,
// and the family is revoked after the transaction fosite rolls back.
func TestTokenSingleUseIsAtomic(t *testing.T) {
	s, _ := testServer(t)
	ctx := t.Context()
	insertClient(t, s, "lgc_test", "test", "chani")
	if err := s.makePrincipal(ctx, s.db, "chani"); err != nil {
		t.Fatal(err)
	}
	client, err := s.store.GetClient(ctx, "lgc_test")
	if err != nil {
		t.Fatal(err)
	}
	session := func() *Session {
		sess := NewSession()
		sess.Subject = "chani"
		sess.SetExpiresAt(fosite.AccessToken, s.now().Add(AccessTokenLifespan))
		sess.SetExpiresAt(fosite.RefreshToken, s.now().Add(RefreshTokenLifespan))
		sess.SetExpiresAt(fosite.AuthorizeCode, s.now().Add(AuthorizeCodeLifespan))
		return sess
	}
	request := func(name string) fosite.Requester {
		req := fosite.NewRequest()
		req.ID = name
		req.RequestedAt = s.now()
		req.Client = client
		req.Session = session()
		return req
	}
	// A family with a code, an access token and a refresh token.
	write := func(kind, signature, family string) {
		t.Helper()
		req := request(family)
		var err error
		switch kind {
		case kindCode:
			err = s.store.CreateAuthorizeCodeSession(ctx, signature, req)
		case kindAccess:
			err = s.store.CreateAccessTokenSession(ctx, signature, req)
		case kindRefresh:
			err = s.store.CreateRefreshTokenSession(ctx, signature, "", req)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	active := func(kind, signature string) bool {
		t.Helper()
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_sessions WHERE kind = ? AND signature = ? AND active = 1`,
			kind, signature).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	t.Run("refresh token", func(t *testing.T) {
		write(kindRefresh, "rt-1", "family-refresh")
		write(kindAccess, "at-1", "family-refresh")
		// The winner consumes it, and commits.
		tx, err := s.store.BeginTX(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.store.RotateRefreshToken(tx, "family-refresh", "rt-1"); err != nil {
			t.Fatalf("the first rotation = %v", err)
		}
		if err := s.store.Commit(tx); err != nil {
			t.Fatal(err)
		}
		// The winner's new pair, which the reuse must end.
		write(kindRefresh, "rt-2", "family-refresh")
		write(kindAccess, "at-2", "family-refresh")
		// The loser takes the same token, and fails.
		tx, err = s.store.BeginTX(ctx)
		if err != nil {
			t.Fatal(err)
		}
		err = s.store.RotateRefreshToken(tx, "family-refresh", "rt-1")
		if !errors.Is(err, errTokenReused) || !errors.Is(err, fosite.ErrInactiveToken) {
			t.Fatalf("the second rotation = %v, want a reuse", err)
		}
		// fosite rolls the transaction back; the family goes with it.
		if err := s.store.Rollback(tx); err != nil {
			t.Fatal(err)
		}
		if active(kindRefresh, "rt-1") || active(kindRefresh, "rt-2") || active(kindAccess, "at-2") {
			t.Error("the family is still live after the reuse")
		}
	})
	t.Run("authorization code", func(t *testing.T) {
		write(kindCode, "ac-1", "family-code")
		write(kindRefresh, "rt-2", "family-code")
		tx, err := s.store.BeginTX(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.store.InvalidateAuthorizeCodeSession(tx, "ac-1"); err != nil {
			t.Fatalf("the first exchange = %v", err)
		}
		if err := s.store.Commit(tx); err != nil {
			t.Fatal(err)
		}
		tx, err = s.store.BeginTX(ctx)
		if err != nil {
			t.Fatal(err)
		}
		err = s.store.InvalidateAuthorizeCodeSession(tx, "ac-1")
		if !errors.Is(err, errTokenReused) {
			t.Fatalf("the second exchange = %v, want a reuse", err)
		}
		if err := s.store.Rollback(tx); err != nil {
			t.Fatal(err)
		}
		if active(kindRefresh, "rt-2") {
			t.Error("the family is still live after the code was exchanged twice")
		}
	})
}

// TestRequestHashIsDeterministic checks that the hash of an item with several
// selectors is the same every time, with its sel keys sorted.
func TestRequestHashIsDeterministic(t *testing.T) {
	req := Request{ID: "0123456789abcdef0123456789abcdef", Kind: KindAccess,
		Client: "lgc_abcdefghijklmnopqrstuv", Principal: "chani", App: "marks", Scopes: []string{},
		Items: []Item{{Cap: "marks:read", Sel: map[string]string{"zone": "/z", "folder": "/Reading", "tag": "/a"}}}}
	const canonical = `{"id":"0123456789abcdef0123456789abcdef","kind":"access",` +
		`"client":"lgc_abcdefghijklmnopqrstuv","principal":"chani","app":"marks","scopes":[],` +
		`"items":[{"cap":"marks:read","sel":{"folder":"/Reading","tag":"/a","zone":"/z"}}]}`
	b, err := req.canonical()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != canonical {
		t.Errorf("canonical =\n%s\nwant\n%s", b, canonical)
	}
	if got, want := req.Hash(), "843e885332f550aae7719b1091faf10f29599cdc396baaab506803b56af37b58"; got != want {
		t.Errorf("Hash() = %s, want %s", got, want)
	}
	for range 100 {
		for i := range req.Items {
			if req.Hash() != "843e885332f550aae7719b1091faf10f29599cdc396baaab506803b56af37b58" {
				t.Fatalf("Hash() changed at iteration %d: %s", i, req.Hash())
			}
		}
	}
}

// TestRequestAccessLimitRace checks that the pending limit holds when
// requests are created at once.
func TestRequestAccessLimitRace(t *testing.T) {
	s, _ := testServer(t)
	ctx := t.Context()
	insertClient(t, s, "lgc_bound", "bound", "chani")
	if err := s.makePrincipal(ctx, s.db, "chani"); err != nil {
		t.Fatal(err)
	}
	items := []Item{{Cap: "marks:read"}}
	for range MaxPending - 1 {
		if _, err := s.RequestAccess(ctx, "lgc_bound", "marks", items); err != nil {
			t.Fatal(err)
		}
	}
	created := make([]error, 8)
	var wg sync.WaitGroup
	for i := range created {
		wg.Go(func() {
			if i%2 == 0 {
				_, created[i] = s.RequestAccess(ctx, "lgc_bound", "marks", items)
				return
			}
			_, created[i] = s.createSigninRequest(ctx, "lgc_bound", []string{"mcp"}, "chani", url.Values{})
		})
	}
	wg.Wait()
	ok := 0
	for _, err := range created {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrTooMany):
			t.Errorf("a creation: %v, want ErrTooMany", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d creations passed the limit, want 1", ok)
	}
	pending, err := s.pendingRequests(ctx)
	if err != nil || len(pending) != MaxPending {
		t.Errorf("%d pending requests, %v; want %d", len(pending), err, MaxPending)
	}
}

// mintAccessToken stores a live access token for a client bound to chani,
// whose subject is subject and whose granted scopes are scopes.
func mintAccessToken(t *testing.T, s *Server, subject string, scopes ...string) string {
	t.Helper()
	ctx := t.Context()
	client, err := s.store.GetClient(ctx, "lgc_test")
	if err != nil {
		t.Fatal(err)
	}
	req := fosite.NewRequest()
	req.ID = "family-" + subject
	req.RequestedAt = s.now()
	req.Client = client
	sess := NewSession()
	sess.Subject = subject
	sess.SetExpiresAt(fosite.AccessToken, time.Now().Add(time.Hour))
	req.Session = sess
	req.GrantedScope = fosite.Arguments(scopes)
	token, signature, err := s.strategy.GenerateAccessToken(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.CreateAccessTokenSession(ctx, signature, req); err != nil {
		t.Fatal(err)
	}
	return token
}

// boundServer is a Server whose client lgc_test is bound to chani, who is
// live.
func boundServer(t *testing.T) *Server {
	t.Helper()
	s, _ := testServer(t)
	insertClient(t, s, "lgc_test", "test", "chani")
	if err := s.makePrincipal(t.Context(), s.db, "chani"); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestCheckGuards checks what a live access token still needs: its subject
// is the client's principal, the client is bound and live, the principal is
// live, and the client's scope still holds the scope Check asks for.
func TestCheckGuards(t *testing.T) {
	ctx := t.Context()
	t.Run("a valid token", func(t *testing.T) {
		s := boundServer(t)
		token := mintAccessToken(t, s, "chani", "mcp")
		access, err := s.Check(ctx, token, "mcp")
		if err != nil || access.Principal != "chani" || access.Client != "lgc_test" {
			t.Fatalf("Check = %+v, %v", access, err)
		}
	})
	cases := map[string]func(t *testing.T, s *Server, token string){
		"the subject is another principal": func(t *testing.T, s *Server, token string) {
			if _, err := s.Check(ctx, token, "mcp"); !errors.Is(err, ErrNotFound) {
				t.Errorf("Check = %v, want ErrNotFound", err)
			}
		},
		"the client is unbound": func(t *testing.T, s *Server, token string) {
			if _, err := s.db.ExecContext(ctx, `UPDATE oauth_clients SET principal = NULL WHERE id = 'lgc_test'`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Check(ctx, token, "mcp"); !errors.Is(err, ErrNotFound) {
				t.Errorf("Check = %v, want ErrNotFound", err)
			}
		},
		"the principal is revoked": func(t *testing.T, s *Server, token string) {
			if _, err := s.db.ExecContext(ctx, `UPDATE principals SET revoked_at = ? WHERE name = 'chani'`, stamp(s.now())); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Check(ctx, token, "mcp"); !errors.Is(err, ErrNotFound) {
				t.Errorf("Check = %v, want ErrNotFound", err)
			}
		},
		"the client is revoked": func(t *testing.T, s *Server, token string) {
			if _, err := s.db.ExecContext(ctx, `UPDATE oauth_clients SET revoked_at = ? WHERE id = 'lgc_test'`, stamp(s.now())); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Check(ctx, token, "mcp"); !errors.Is(err, ErrNotFound) {
				t.Errorf("Check = %v, want ErrNotFound", err)
			}
		},
	}
	for name, vary := range cases {
		t.Run(name, func(t *testing.T) {
			s := boundServer(t)
			subject := "chani"
			if name == "the subject is another principal" {
				subject = "bob"
			}
			token := mintAccessToken(t, s, subject, "mcp")
			vary(t, s, token)
		})
	}
	// A scope the client no longer has is refused, though the token was
	// granted it; the scope it still has passes.
	t.Run("the client's scope is narrowed", func(t *testing.T) {
		s := boundServer(t)
		token := mintAccessToken(t, s, "chani", "mcp", "events")
		if _, err := s.db.ExecContext(ctx, `UPDATE oauth_clients SET scope = 'mcp' WHERE id = 'lgc_test'`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Check(ctx, token, "events"); err == nil {
			t.Error("Check(events) passes for a scope the client lost")
		}
		if _, err := s.Check(ctx, token, "mcp"); err != nil {
			t.Errorf("Check(mcp) = %v", err)
		}
	})
}

// TestLifespans checks the lifespans lodge configures, and that a token, a
// code and a refresh token work just inside them and stop just outside:
// the stored expiry is the clock, so no test waits.
func TestLifespans(t *testing.T) {
	ctx := t.Context()
	if AccessTokenLifespan != time.Hour || AuthorizeCodeLifespan != 10*time.Minute ||
		RefreshTokenLifespan != 30*24*time.Hour {
		t.Errorf("the lifespans are %v, %v and %v", AccessTokenLifespan, AuthorizeCodeLifespan, RefreshTokenLifespan)
	}
	f, ok := newFlow(t).s.provider.(*fosite.Fosite)
	if !ok {
		t.Fatal("the provider is not fosite's")
	}
	if got := f.Config.GetAccessTokenLifespan(ctx); got != AccessTokenLifespan {
		t.Errorf("fosite's access token lifespan = %v", got)
	}
	if got := f.Config.GetAuthorizeCodeLifespan(ctx); got != AuthorizeCodeLifespan {
		t.Errorf("fosite's code lifespan = %v", got)
	}
	if got := f.Config.GetRefreshTokenLifespan(ctx); got != RefreshTokenLifespan {
		t.Errorf("fosite's refresh token lifespan = %v", got)
	}

	// The stored expiry is the clock: a token whose expiry is 1 minute away
	// is the state at 59 minutes, and one whose expiry passed is the state at
	// 61 minutes.
	// An access token: live 59 minutes in, refused 61 minutes in.
	t.Run("an access token", func(t *testing.T) {
		s := boundServer(t)
		token := mintAccessToken(t, s, "chani", "mcp")
		if _, err := s.Check(ctx, token, "mcp"); err != nil {
			t.Fatalf("Check at 0 minutes = %v", err)
		}
		setExpiry(t, s, kindAccess, "family-chani", string(fosite.AccessToken), time.Now().Add(1*time.Minute))
		if _, err := s.Check(ctx, token, "mcp"); err != nil {
			t.Errorf("Check at 59 minutes = %v", err)
		}
		setExpiry(t, s, kindAccess, "family-chani", string(fosite.AccessToken), time.Now().Add(-1*time.Minute))
		if _, err := s.Check(ctx, token, "mcp"); err == nil {
			t.Error("Check at 61 minutes passes")
		}
	})
	// A code: good 9 minutes in, refused 11 minutes in.
	t.Run("a code", func(t *testing.T) {
		f := newFlow(t)
		id, csrf, hash, _ := f.signin("mcp")
		approved := f.approve(id, csrf, hash, url.Values{"principal": {"chani"}, "scope": {"mcp"}})
		if approved.status != http.StatusSeeOther {
			t.Fatalf("the approval: %d %q", approved.status, approved.body)
		}
		location, err := url.Parse(approved.header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		code := location.Query().Get("code")
		exchange := func() result {
			return f.token(url.Values{"grant_type": {"authorization_code"}, "client_id": {f.clientID},
				"code": {code}, "redirect_uri": {redirectURI}, "code_verifier": {verifier}})
		}
		signature := f.s.strategy.AuthorizeCodeSignature(ctx, code)
		setExpiryRaw(t, f.s, kindCode, signature, string(fosite.AuthorizeCode), time.Now().Add(time.Minute))
		// A live code at 9 minutes is still good: try a code that is not
		// due yet in the flow's own time, which is the same row.
		if a := exchange(); a.status != http.StatusOK {
			t.Fatalf("the exchange at 9 minutes: %d %q", a.status, a.body)
		}
		// A second code, expired at 11 minutes, is refused.
		id2, csrf2, hash2, _ := f.signin("mcp")
		approved2 := f.approve(id2, csrf2, hash2, url.Values{"principal": {"chani"}, "scope": {"mcp"}})
		location2, err := url.Parse(approved2.header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		code2 := location2.Query().Get("code")
		setExpiryRaw(t, f.s, kindCode, f.s.strategy.AuthorizeCodeSignature(ctx, code2),
			string(fosite.AuthorizeCode), time.Now().Add(-time.Minute))
		a := f.token(url.Values{"grant_type": {"authorization_code"}, "client_id": {f.clientID},
			"code": {code2}, "redirect_uri": {redirectURI}, "code_verifier": {verifier}})
		var tok tokenJSON
		if err := jsonUnmarshal([]byte(a.body), &tok); err != nil {
			t.Fatal(err)
		}
		if a.status != http.StatusBadRequest || tok.Error != "invalid_grant" {
			t.Errorf("the exchange at 11 minutes: %d %q", a.status, a.body)
		}
	})
	// A refresh token: good 29 days in, refused 31 days in.
	t.Run("a refresh token", func(t *testing.T) {
		f := newFlow(t)
		_, tok := f.signinFlow("chani", "mcp")
		refresh := func(token string) result {
			return f.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {f.clientID},
				"refresh_token": {token}})
		}
		setExpiryRaw(t, f.s, kindRefresh, f.s.strategy.RefreshTokenSignature(ctx, tok.RefreshToken),
			string(fosite.RefreshToken), time.Now().Add(24*time.Hour))
		first := refresh(tok.RefreshToken)
		if first.status != http.StatusOK {
			t.Fatalf("the refresh at 29 days: %d %q", first.status, first.body)
		}
		var renewed tokenJSON
		if err := jsonUnmarshal([]byte(first.body), &renewed); err != nil {
			t.Fatal(err)
		}
		setExpiryRaw(t, f.s, kindRefresh, f.s.strategy.RefreshTokenSignature(ctx, renewed.RefreshToken),
			string(fosite.RefreshToken), time.Now().Add(-24*time.Hour))
		a := refresh(renewed.RefreshToken)
		var tok2 tokenJSON
		if err := jsonUnmarshal([]byte(a.body), &tok2); err != nil {
			t.Fatal(err)
		}
		if a.status != http.StatusBadRequest || tok2.Error != "invalid_grant" {
			t.Errorf("the refresh at 31 days: %d %q", a.status, a.body)
		}
	})
}

// setExpiry moves the stored expiry of a session of kind, whose family is
// family, for the token type given.
func setExpiry(t *testing.T, s *Server, kind, family, tokenType string, at time.Time) {
	t.Helper()
	var signature string
	if err := s.db.QueryRowContext(t.Context(),
		`SELECT signature FROM oauth_sessions WHERE kind = ? AND request_id = ?`, kind, family).Scan(&signature); err != nil {
		t.Fatal(err)
	}
	setExpiryRaw(t, s, kind, signature, tokenType, at)
}

// setExpiryRaw moves the stored expiry of the session of kind and signature:
// in its session column, and in the column the sweep reads.
func setExpiryRaw(t *testing.T, s *Server, kind, signature, tokenType string, at time.Time) {
	t.Helper()
	ctx := t.Context()
	var text string
	if err := s.db.QueryRowContext(ctx, `SELECT session FROM oauth_sessions WHERE kind = ? AND signature = ?`,
		kind, signature).Scan(&text); err != nil {
		t.Fatal(err)
	}
	var stored sessionJSON
	if err := jsonUnmarshal([]byte(text), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.ExpiresAt == nil {
		stored.ExpiresAt = map[string]time.Time{}
	}
	stored.ExpiresAt[tokenType] = at.UTC()
	b, err := jsonMarshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE oauth_sessions SET session = ?, expires_at = ? WHERE kind = ? AND signature = ?`,
		string(b), stamp(at), kind, signature); err != nil {
		t.Fatal(err)
	}
}
