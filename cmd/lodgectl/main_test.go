package main

import (
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/frostyard/lodge/internal/allow"
	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/apptest"
	"github.com/frostyard/lodge/internal/control"
	"github.com/frostyard/lodge/internal/inference"
	"github.com/frostyard/lodge/internal/sessions"
	"github.com/frostyard/lodge/internal/store"
	"github.com/frostyard/lodge/internal/tokens"
)

func serve(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(root, "lodge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	apptest.Install(t, root, "notes", map[string]any{"name": "Notes", "mcp": true, "key": "n"}, nil)
	reg := apps.New(root, nil)
	reg.Load()
	inf := inference.New(inference.Options{DB: db, DevUID: -1})
	inf.Load(inference.Config{
		Providers: map[string]inference.ProviderConfig{"home": {Type: inference.TypeOpenAI, BaseURL: "http://127.0.0.1:1/v1", Slots: 4, Home: true}},
		Aliases:   map[string]inference.AliasConfig{"local": {Provider: "home", Model: "MODEL"}},
	}, nil)
	srv := control.New(control.Deps{Inference: inf, Allow: allow.New(db, nil), Tokens: tokens.New(db, nil), Sessions: sessions.NewRegistry(), Apps: reg,
		Reload: reg.Load, MCPURL: func(id string) string { return "https://LODGE/mcp/" + id },
		Nodes: func(context.Context) []control.Node {
			return []control.Node{
				{Name: "lodge", State: "NeedsLogin", LoginURL: "https://login.example/a/1"},
				{Name: "lodge-content", DNSName: "lodge-content.example", State: "Running", Up: true},
			}
		}})
	ln, err := control.Listen(filepath.Join(root, "run/lodge/control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return root
}

func lodgectl(t *testing.T, root string, args ...string) (int, string, string) {
	t.Helper()
	return lodgectlIn(t, root, "", args...)
}

func lodgectlIn(t *testing.T, root, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(append([]string{"--root", root}, args...), strings.NewReader(stdin), &stdout, &stderr, func(string) string { return "" })
	return code, stdout.String(), stderr.String()
}

// The key comes from stdin, and never shows again.
func TestProviderKeyAndUsage(t *testing.T) {
	root := serve(t)
	if code, out, errs := lodgectlIn(t, root, "sk-secret\n", "provider", "key", "home"); code != 0 || !strings.Contains(out, "key set for provider home") {
		t.Fatalf("provider key: %d %q %q", code, out, errs)
	}
	if code, _, errs := lodgectlIn(t, root, "k", "provider", "key", "nosuch"); code != 1 || !strings.Contains(errs, "no provider") {
		t.Errorf("an unknown provider: %d %q", code, errs)
	}
	if code, out, _ := lodgectlIn(t, root, "", "provider", "key", "home"); code != 0 || !strings.Contains(out, "removed") {
		t.Errorf("removing the key: %d %q", code, out)
	}
	code, out, _ := lodgectl(t, root, "status")
	if code != 0 || !regexp.MustCompile(`provider\s+home\s+unavailable\s+openai: not checked yet`).MatchString(out) || strings.Contains(out, "sk-secret") {
		t.Errorf("status: %d %q", code, out)
	}
	if code, out, _ := lodgectl(t, root, "usage", "--since", "2026-10-01"); code != 0 || !strings.Contains(out, "APP") {
		t.Errorf("usage: %d %q", code, out)
	}
	if code, _, _ := lodgectl(t, root, "usage", "extra"); code != 2 {
		t.Errorf("usage with an argument: exit %d", code)
	}
}

func TestAllowAndList(t *testing.T) {
	root := serve(t)
	if code, out, errs := lodgectl(t, root, "allow", "login", "alice@example.com"); code != 0 || !strings.Contains(out, "allow login alice@example.com") {
		t.Fatalf("allow login: %d %q %q", code, out, errs)
	}
	if code, _, errs := lodgectl(t, root, "allow", "login", "alice@example.com"); code != 1 || !strings.Contains(errs, "already allowed") {
		t.Errorf("allowing twice: %d %q", code, errs)
	}
	if code, out, _ := lodgectl(t, root, "logins"); code != 0 || !strings.Contains(out, "alice@example.com") {
		t.Errorf("logins: %d %q", code, out)
	}
	if code, out, _ := lodgectl(t, root, "--json", "logins"); code != 0 || !strings.HasPrefix(out, `[{"login":"alice@example.com"`) {
		t.Errorf("logins --json: %d %q", code, out)
	}
	if code, out, _ := lodgectl(t, root, "apps"); code != 0 || !regexp.MustCompile(`notes\s+Notes\s+1.0.0\s+n\s+down\s+mcp`).MatchString(out) {
		t.Errorf("apps: %d %q", code, out)
	}
	if code, out, _ := lodgectl(t, root, "allow", "node", "nPhone1CNTRL"); code != 0 || !strings.Contains(out, "allow node") {
		t.Errorf("allow node: %d %q", code, out)
	}
	code, out, _ := lodgectl(t, root, "status")
	if code != 0 || !strings.Contains(out, "sessions") ||
		!regexp.MustCompile(`node\s+lodge\s+needs login\s+log in at https://login.example/a/1`).MatchString(out) ||
		!regexp.MustCompile(`node\s+lodge-content\s+up\s+lodge-content.example`).MatchString(out) {
		t.Errorf("status: %d %q", code, out)
	}
}

// The token goes alone to stdout; the hint goes to stderr and never says
// --scope project.
func TestTokenIssue(t *testing.T) {
	root := serve(t)
	code, out, errs := lodgectl(t, root, "token", "issue", "claude-code@laptop", "--app", "notes", "--scope", "vault=1;write")
	if code != 0 {
		t.Fatalf("issue: %d %q", code, errs)
	}
	if !regexp.MustCompile(`^lgm_[A-Za-z0-9_-]{43}\n$`).MatchString(out) {
		t.Errorf("stdout = %q, want the token alone", out)
	}
	want := `claude mcp add --transport http notes https://LODGE/mcp/notes --header "Authorization: Bearer <token>"`
	if !strings.Contains(errs, want) || strings.Contains(errs, "--scope project") || strings.Contains(errs, strings.TrimSpace(out)) {
		t.Errorf("stderr = %q", errs)
	}
	if code, out, _ := lodgectl(t, root, "tokens"); code != 0 || !strings.Contains(out, "claude-code@laptop") || strings.Contains(out, "lgm_") {
		t.Errorf("tokens: %d %q", code, out)
	}
	if code, out, _ := lodgectl(t, root, "token", "revoke", "claude-code@laptop"); code != 0 || !strings.Contains(out, "revoked") {
		t.Errorf("revoke: %d %q", code, out)
	}
	if code, _, errs := lodgectl(t, root, "token", "issue", "x", "--app", "nosuch", "--scope", "s"); code != 1 || !strings.Contains(errs, "MCP") {
		t.Errorf("an app without MCP: %d %q", code, errs)
	}
}

func TestUsageErrors(t *testing.T) {
	root := serve(t)
	for _, args := range [][]string{{}, {"nope"}, {"allow", "user", "x"}, {"allow", "login"}, {"token", "issue", "x", "--app", "notes"}, {"session", "end"}, {"status", "extra"}} {
		if code, _, _ := lodgectl(t, root, args...); code != 2 {
			t.Errorf("lodgectl %q: exit %d, want 2", args, code)
		}
	}
	if code, _, errs := lodgectl(t, t.TempDir(), "status"); code != 1 || !strings.Contains(errs, "sudo lodgectl") {
		t.Errorf("no lodge: %d %q", code, errs)
	}
}
