package broker

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// LoadConfig reads the broker member: a missing file or member is a broker
// that is off, and an unknown member is refused.
func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "none.json")
	if c, err := LoadConfig(missing); err != nil || c.Enabled {
		t.Errorf("a missing file: %+v %v, want a broker that is off", c, err)
	}
	for name, tt := range map[string]struct {
		body    string
		enabled bool
		fails   bool
	}{
		"empty":          {body: `{}`},
		"no member":      {body: `{"inference": {}}`},
		"off":            {body: `{"broker": {"enabled": false}}`},
		"on":             {body: `{"broker": {"enabled": true}}`, enabled: true},
		"unknown member": {body: `{"broker": {"enabled": true, "port": 4222}}`, fails: true},
		"not an object":  {body: `{"broker": true}`, fails: true},
		"not json":       {body: `{`, fails: true},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			noErr(t, os.WriteFile(path, []byte(tt.body), 0o600))
			c, err := LoadConfig(path)
			switch {
			case tt.fails && err == nil:
				t.Fatalf("%s was admitted: %+v", tt.body, c)
			case !tt.fails && err != nil:
				t.Fatalf("%s: %v", tt.body, err)
			case !tt.fails && c.Enabled != tt.enabled:
				t.Errorf("%s: enabled is %v, want %v", tt.body, c.Enabled, tt.enabled)
			}
		})
	}
}

// A subscription's names and filters are checked before anything is made.
func TestSubscribeValidation(t *testing.T) {
	h := newHarness(t)
	for name, tt := range map[string]struct {
		client, sub, app string
		filters          []string
		want             string
	}{
		"a client with a dash": {"chani-1", "s1", "marks", []string{"marks.>"}, "client"},
		"an empty sub":         {"chani", "", "marks", []string{"marks.>"}, "sub"},
		"a sub with a dash":    {"chani", "s-1", "marks", []string{"marks.>"}, "sub"},
		"an app id of none":    {"chani", "s1", "Marks", []string{"marks.>"}, "app"},
		"no filter":            {"chani", "s1", "marks", nil, "at least one filter"},
		"another app's":        {"chani", "s1", "marks", []string{"notes.>"}, "not inside"},
		"an empty token":       {"chani", "s1", "marks", []string{"marks..x"}, "empty token"},
		"a space":              {"chani", "s1", "marks", []string{"marks.a b"}, "space"},
		"a > too early":        {"chani", "s1", "marks", []string{"marks.>.x"}, ">"},
		"an app not enabled":   {"chani", "s1", "mail", []string{"mail.>"}, "not enabled"},
	} {
		t.Run(name, func(t *testing.T) {
			err := h.b.Subscribe(t.Context(), tt.client, tt.sub, tt.app, tt.filters)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Subscribe answered %v, want an error saying %q", err, tt.want)
			}
		})
	}
	if err := h.b.Unsubscribe(t.Context(), "chani", "nosuch"); err == nil {
		t.Error("Unsubscribe of a subscription that is not there was admitted")
	}
}

// The socket is lodge's: mode 0660, in the group lodge names.
func TestListenMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run/lodge/nats.sock")
	ln, err := Listen(path, "")
	noErr(t, err)
	defer ln.Close()
	info, err := os.Stat(path)
	noErr(t, err)
	if got := info.Mode().Perm(); got != 0o660 {
		t.Errorf("the socket's mode is %o, want 660", got)
	}
}

// An app that leaves the enabled list loses its connections.
func TestSyncAppsClosesAnAppsConnections(t *testing.T) {
	h := newHarness(t)
	e := newErrs()
	gone := make(chan struct{})
	nc, err := nats.Connect("nats://broker", nats.SetCustomDialer(unixDialer(h.sock)), nats.SkipHostLookup(),
		nats.UserInfo("marks", ""), nats.NoReconnect(), nats.Timeout(10*time.Second),
		nats.ErrorHandler(e.handler), nats.ClosedHandler(func(*nats.Conn) { close(gone) }))
	noErr(t, err)
	t.Cleanup(nc.Close)

	noErr(t, h.b.SyncApps(t.Context(), []string{"notes"}))
	select {
	case <-gone:
	case <-time.After(time.Second):
		t.Fatal("the app's connection is still open 1 s after it left the enabled list")
	}
	if err := h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}); err == nil {
		t.Error("a subscription of an app that is not enabled was admitted")
	}
}

// Under --root every app runs as the developer, which names its app in
// CONNECT's user field, and that app must be enabled. lodge itself must
// refuse the other app, as its log shows: the NATS server answers an
// authorization violation of its own for an account that it lacks.
func TestDevelopmentUIDNamesItsApp(t *testing.T) {
	dir := t.TempDir()
	apps := &fakeApps{byUID: map[int]string{}, enabled: map[string]bool{"marks": true}}
	logged := &logLines{}
	b := New(Options{StoreDir: dir, Apps: apps, DevUID: os.Getuid(), Log: slog.New(logged)})
	noErr(t, b.Start(t.Context()))
	t.Cleanup(b.Close)
	ln, err := Listen(filepath.Join(dir, "nats.sock"), "")
	noErr(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() { _ = b.ServeApps(t.Context(), ln) }()
	noErr(t, b.SyncApps(t.Context(), []string{"marks"}))
	sock := ln.Addr().String()

	nc, err := connectApp(t, sock, "marks")
	noErr(t, err)
	defer nc.Close()
	published(t, jsFor(t, nc), "marks.x", "e1")
	nc.Close()
	other, err := connectApp(t, sock, "mail")
	if err == nil {
		other.Close()
		t.Fatal("a developer connection that names an app which is not enabled was admitted")
	}
	refusedUnnamed(t, err, logged)
}

// unnamedRefusal is the broker's log line for a CONNECT line that names no
// enabled app.
const unnamedRefusal = "the broker refused a connection: lodge cannot name it"

// refusedUnnamed checks the error of a nats.go connection that the broker
// refused because lodge cannot name it. lodge writes the authorization
// violation and closes the connection. nats.go writes CONNECT and PING in 2
// writes, so the PING can reach a socket that lodge has already closed: the
// client then fails with EPIPE or ECONNRESET and never reads the refusal
// line. Such an error counts as this refusal only together with the
// broker's log line, which lodge writes before it closes. Any other error,
// or another answer from the server, fails the test.
func refusedUnnamed(t *testing.T, err error, logged *logLines) {
	t.Helper()
	if !logged.has(unnamedRefusal) {
		t.Errorf("the broker did not log %q; the connection ended with %v", unnamedRefusal, err)
	}
	switch {
	case strings.Contains(err.Error(), "Authorization Violation"):
	case errors.Is(err, syscall.EPIPE), errors.Is(err, syscall.ECONNRESET):
		t.Logf("the client wrote after the refusal closed the socket: %v", err)
	default:
		t.Errorf("the refusal was %v, want an authorization violation", err)
	}
}

// logLines records the messages of the broker's log, for a test to check
// which refusal the broker made.
type logLines struct {
	mu   sync.Mutex
	msgs []string
}

func (l *logLines) Enabled(context.Context, slog.Level) bool { return true }

func (l *logLines) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, r.Message)
	return nil
}

func (l *logLines) WithAttrs([]slog.Attr) slog.Handler { return l }

func (l *logLines) WithGroup(string) slog.Handler { return l }

// has reports whether the log holds msg.
func (l *logLines) has(msg string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Contains(l.msgs, msg)
}

// connectApp dials the broker's socket as user, whose CONNECT names an app
// only under --root.
func connectApp(t *testing.T, sock, user string) (*nats.Conn, error) {
	t.Helper()
	return nats.Connect("nats://broker", nats.SetCustomDialer(unixDialer(sock)), nats.SkipHostLookup(),
		nats.UserInfo(user, ""), nats.NoReconnect(), nats.Timeout(10*time.Second))
}
