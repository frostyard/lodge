package tokens

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/frostyard/lodge/internal/store"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newStore(t *testing.T) (*Store, *clock, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(dir, "lodge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	c := &clock{time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	return New(db, c.now), c, dir
}

func TestIssueAndCheck(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := t.Context()
	secret, err := s.Issue(ctx, "claude-code@laptop", "notes", "vault=1;write")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^lgm_[A-Za-z0-9_-]{43}$`).MatchString(secret) {
		t.Fatalf("secret %q has the wrong form", secret)
	}
	tok, err := s.Check(ctx, secret, "notes")
	if err != nil {
		t.Fatal(err)
	}
	if tok.Name != "claude-code@laptop" || tok.Scope != "vault=1;write" || tok.App != "notes" {
		t.Errorf("Check = %+v", tok)
	}
	// Another app, a changed secret, a malformed one: all unauthorized.
	flipped := secret[:len(secret)-1] + map[bool]string{true: "A", false: "B"}[secret[len(secret)-1] != 'A']
	for _, c := range []struct{ secret, app string }{
		{secret, "mail"},
		{flipped, "notes"},
		{"", "notes"},
		{"lgm_short", "notes"},
		{strings.TrimPrefix(secret, Prefix), "notes"},
		{Hash(secret), "notes"},
	} {
		if _, err := s.Check(ctx, c.secret, c.app); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("Check(%q, %s) = %v, want ErrUnauthorized", c.secret, c.app, err)
		}
	}
}

func TestSecretNeverStored(t *testing.T) {
	s, _, dir := newStore(t)
	secret, err := s.Issue(t.Context(), "agent", "notes", "vault=1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), secret) || strings.Contains(string(b), strings.TrimPrefix(secret, Prefix)) {
			t.Errorf("%s holds the token", e.Name())
		}
	}
	list, _ := s.List(t.Context())
	for _, tok := range list {
		if strings.Contains(tok.Name+tok.Scope+tok.App, secret) {
			t.Errorf("List shows the token: %+v", tok)
		}
	}
}

func TestNamesAndScopes(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := t.Context()
	for _, bad := range []string{"", strings.Repeat("a", 65), "has space", "a/b", "ü"} {
		if _, err := s.Issue(ctx, bad, "notes", "x"); !errors.Is(err, ErrInvalid) {
			t.Errorf("name %q: %v, want ErrInvalid", bad, err)
		}
	}
	if _, err := s.Issue(ctx, strings.Repeat("a", 64), "notes", "x"); err != nil {
		t.Errorf("a 64-character name: %v", err)
	}
	for _, bad := range []string{"", strings.Repeat("s", 257), "has space", "tab\t", "é"} {
		if _, err := s.Issue(ctx, "n", "notes", bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("scope %q: %v, want ErrInvalid", bad, err)
		}
	}
	if _, err := s.Issue(ctx, "s256", "notes", strings.Repeat("s", 256)); err != nil {
		t.Errorf("a 256-character scope: %v", err)
	}
}

func TestRevokeAndReuseName(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := t.Context()
	old, _ := s.Issue(ctx, "agent", "notes", "vault=1")
	if _, err := s.Issue(ctx, "agent", "notes", "vault=2"); !errors.Is(err, ErrConflict) {
		t.Errorf("a second live token named agent: %v, want ErrConflict", err)
	}
	if err := s.Revoke(ctx, "agent"); err != nil {
		t.Fatal(err)
	}
	if live, _ := s.Live(ctx, "agent"); live {
		t.Error("a revoked token is live")
	}
	if _, err := s.Check(ctx, old, "notes"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("a revoked token checks: %v", err)
	}
	if err := s.Revoke(ctx, "agent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoking twice: %v, want ErrNotFound", err)
	}
	fresh, err := s.Issue(ctx, "agent", "notes", "vault=2")
	if err != nil {
		t.Fatalf("reusing a revoked name: %v", err)
	}
	if tok, err := s.Check(ctx, fresh, "notes"); err != nil || tok.Scope != "vault=2" {
		t.Errorf("the new token: %+v, %v", tok, err)
	}
	list, _ := s.List(ctx)
	if len(list) != 2 || list[0].Revoked.IsZero() || !list[1].Revoked.IsZero() {
		t.Errorf("List = %+v", list)
	}
}

func TestTouchAtMostOncePerMinute(t *testing.T) {
	s, c, _ := newStore(t)
	ctx := t.Context()
	if _, err := s.Issue(ctx, "agent", "notes", "x"); err != nil {
		t.Fatal(err)
	}
	lastUsed := func() time.Time {
		list, _ := s.List(ctx)
		return list[0].LastUsed
	}
	start := c.t
	_ = s.Touch(ctx, "agent")
	if !lastUsed().Equal(start) {
		t.Fatalf("last use = %v, want %v", lastUsed(), start)
	}
	c.t = start.Add(TouchEvery - time.Second)
	_ = s.Touch(ctx, "agent")
	if !lastUsed().Equal(start) {
		t.Errorf("a touch inside the minute was recorded: %v", lastUsed())
	}
	c.t = start.Add(TouchEvery)
	_ = s.Touch(ctx, "agent")
	if !lastUsed().Equal(c.t) {
		t.Errorf("a touch after the minute was not recorded: %v", lastUsed())
	}
}

func TestBearer(t *testing.T) {
	for in, want := range map[string]string{
		"Bearer lgm_x":  "lgm_x",
		"bearer lgm_x":  "lgm_x",
		"Bearer  lgm_x": "lgm_x",
		"Basic abc":     "",
		"lgm_x":         "",
		"":              "",
	} {
		if got := Bearer(in); got != want {
			t.Errorf("Bearer(%q) = %q, want %q", in, got, want)
		}
	}
}
