package sessions

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/frostyard/lodge/internal/peer"
)

var t0 = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

type closed struct {
	mu    sync.Mutex
	codes map[string]int
}

func (c *closed) get(id string) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	code, ok := c.codes[id]
	return code, ok
}

func newSession(t *testing.T, c *closed, app, node string, started time.Time) *Session {
	t.Helper()
	var s *Session
	s = New(app, peer.Peer{NodeID: node, Login: "alice@example.com", NodeName: node}, "192.0.2.1:1", started, func(code int, _ string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if _, dup := c.codes[s.ID]; dup {
			t.Errorf("session %s closed twice", s.ID)
		}
		c.codes[s.ID] = code
	})
	return s
}

func add(t *testing.T, r *Registry, s *Session) {
	t.Helper()
	v, err := r.Reserve()
	if err != nil {
		t.Fatal(err)
	}
	v.Add(s)
}

func TestID(t *testing.T) {
	s := New("a", peer.Peer{}, "", t0, nil)
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(s.ID) {
		t.Errorf("ID %q is not 16 lowercase hex digits", s.ID)
	}
}

func TestPerNodeBoundEvictsOldestSeen(t *testing.T) {
	r := NewRegistry()
	c := &closed{codes: map[string]int{}}
	var ss []*Session
	for i := range PerNodeApp {
		s := newSession(t, c, "notes", "phone", t0.Add(time.Duration(i)*time.Second))
		ss = append(ss, s)
		add(t, r, s)
	}
	// At the bound: nothing evicted. The first session was seen most
	// recently, so the second is now the oldest.
	ss[0].Touch(t0.Add(time.Hour))
	if r.Count() != PerNodeApp || len(c.codes) != 0 {
		t.Fatalf("at the bound: %d sessions, %d closed", r.Count(), len(c.codes))
	}
	// Another node's and another app's sessions do not count.
	add(t, r, newSession(t, c, "notes", "laptop", t0))
	add(t, r, newSession(t, c, "mail", "phone", t0))
	if len(c.codes) != 0 {
		t.Fatalf("another node or app evicted: %v", c.codes)
	}
	// Past the bound: the node's oldest-seen session of the app closes 1008.
	add(t, r, newSession(t, c, "notes", "phone", t0.Add(time.Minute)))
	if code, ok := c.get(ss[1].ID); !ok || code != CodePolicyViolation {
		t.Errorf("the oldest-seen session: closed %v with %d, want 1008", ok, code)
	}
	if _, ok := c.get(ss[0].ID); ok {
		t.Error("the most recently seen session was evicted")
	}
	if r.Count() != PerNodeApp+2 {
		t.Errorf("Count = %d, want %d", r.Count(), PerNodeApp+2)
	}
}

func TestTotalBound(t *testing.T) {
	r := NewRegistry()
	c := &closed{codes: map[string]int{}}
	for i := range Max - 1 {
		add(t, r, newSession(t, c, "notes", fmt.Sprintf("n%d", i), t0))
	}
	// The 64th place is free, then a reservation holds it.
	v, err := r.Reserve()
	if err != nil {
		t.Fatalf("the 64th session: %v", err)
	}
	if _, err := r.Reserve(); !errors.Is(err, ErrFull) {
		t.Errorf("the 65th while one is pending: %v, want ErrFull", err)
	}
	v.Cancel()
	v2, err := r.Reserve()
	if err != nil {
		t.Fatalf("after a cancel: %v", err)
	}
	v2.Add(newSession(t, c, "notes", "last", t0))
	if _, err := r.Reserve(); !errors.Is(err, ErrFull) {
		t.Errorf("the 65th: %v, want ErrFull", err)
	}
	v2.Cancel() // a no-op after Add
	if _, err := r.Reserve(); !errors.Is(err, ErrFull) {
		t.Errorf("Cancel after Add freed a place")
	}
}

func TestSweepAtIdle(t *testing.T) {
	r := NewRegistry()
	c := &closed{codes: map[string]int{}}
	old := newSession(t, c, "notes", "phone", t0)
	fresh := newSession(t, c, "notes", "phone", t0)
	add(t, r, old)
	add(t, r, fresh)
	fresh.Touch(t0.Add(time.Second))
	if n := r.Sweep(t0.Add(Idle - time.Nanosecond)); n != 0 {
		t.Errorf("just inside 90 s, Sweep closed %d", n)
	}
	if n := r.Sweep(t0.Add(Idle)); n != 1 {
		t.Errorf("at 90 s, Sweep closed %d, want 1", n)
	}
	if code, _ := c.get(old.ID); code != CodeGoingAway {
		t.Errorf("idle session closed with %d, want 1001", code)
	}
	if _, ok := c.get(fresh.ID); ok {
		t.Error("the touched session closed")
	}
}

func TestRecheck(t *testing.T) {
	r := NewRegistry()
	c := &closed{codes: map[string]int{}}
	good := newSession(t, c, "notes", "phone", t0)
	good.Recheck = func(context.Context) error { return nil }
	bad := newSession(t, c, "notes", "laptop", t0)
	bad.Recheck = func(context.Context) error { return errors.New("node laptop is not allowed") }
	add(t, r, good)
	add(t, r, bad)
	if n := r.Recheck(t.Context()); n != 1 {
		t.Errorf("Recheck closed %d, want 1", n)
	}
	if code, _ := c.get(bad.ID); code != CodePolicyViolation {
		t.Errorf("failed re-check closed with %d", code)
	}
	if r.Count() != 1 {
		t.Errorf("Count = %d", r.Count())
	}
}

func TestEndLoginNodeAndID(t *testing.T) {
	r := NewRegistry()
	c := &closed{codes: map[string]int{}}
	a := newSession(t, c, "notes", "phone", t0)
	b := New("notes", peer.Peer{NodeID: "laptop", Login: "bob@example.com"}, "", t0, func(int, string) {})
	d := newSession(t, c, "notes", "laptop", t0)
	add(t, r, a)
	add(t, r, b)
	add(t, r, d)
	if n := r.EndLogin("alice@example.com"); n != 2 {
		t.Errorf("EndLogin closed %d, want 2", n)
	}
	if r.Count() != 1 {
		t.Fatalf("Count = %d", r.Count())
	}
	if err := r.End(b.ID); err != nil {
		t.Error(err)
	}
	if err := r.End(b.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("ending twice: %v", err)
	}
	add(t, r, newSession(t, c, "notes", "tablet", t0))
	if n := r.EndNode("tablet"); n != 1 {
		t.Errorf("EndNode closed %d", n)
	}
}

func TestList(t *testing.T) {
	r := NewRegistry()
	c := &closed{codes: map[string]int{}}
	add(t, r, newSession(t, c, "notes", "phone", t0.Add(time.Second)))
	add(t, r, newSession(t, c, "mail", "laptop", t0))
	got := r.List()
	if len(got) != 2 || got[0].App != "mail" || got[0].NodeName != "laptop" || got[1].Login != "alice@example.com" {
		t.Errorf("List = %+v", got)
	}
}
