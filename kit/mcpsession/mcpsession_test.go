package mcpsession

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/frostyard/lodge/kit/lodgeapp"
)

// fake is a Streamable HTTP handler: POST without a session opens one,
// DELETE ends it, and a request for an ended session answers 404.
type fake struct {
	mu      sync.Mutex
	n       int
	live    map[string]bool
	deleted []string
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.live == nil {
		f.live = map[string]bool{}
	}
	id := r.Header.Get(Header)
	switch {
	case id == "" && r.Method == http.MethodPost:
		f.n++
		id = fmt.Sprintf("s%d", f.n)
		f.live[id] = true
		w.Header().Set(Header, id)
		w.WriteHeader(http.StatusOK)
	case !f.live[id]:
		http.Error(w, "session not found", http.StatusNotFound)
	case r.Method == http.MethodDelete:
		delete(f.live, id)
		f.deleted = append(f.deleted, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func do(t *testing.T, h http.Handler, method, id string, c lodgeapp.Caller) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(lodgeapp.WithCaller(t.Context(), c), method, "/mcp", nil)
	if id != "" {
		req.Header.Set(Header, id)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func agent(token, scope string) lodgeapp.Caller {
	return lodgeapp.Caller{Kind: lodgeapp.Agent, Token: token, Scope: scope}
}

func open(t *testing.T, s *Sessions, c lodgeapp.Caller) string {
	t.Helper()
	w := do(t, s, http.MethodPost, "", c)
	id := w.Header().Get(Header)
	if w.Code != http.StatusOK || id == "" {
		t.Fatalf("initialize = %d, session %q", w.Code, id)
	}
	return id
}

func TestASessionIsBoundToItsTokenAndScope(t *testing.T) {
	s := New(&fake{}, Options{})
	a := agent("laptop", "vault=1;write")
	id := open(t, s, a)
	if w := do(t, s, http.MethodPost, id, a); w.Code != http.StatusAccepted {
		t.Errorf("its own token = %d", w.Code)
	}
	for _, other := range []lodgeapp.Caller{agent("phone", "vault=1;write"), agent("laptop", "vault=1")} {
		if w := do(t, s, http.MethodPost, id, other); w.Code != http.StatusNotFound {
			t.Errorf("as %+v = %d, want 404", other, w.Code)
		}
	}
	if w := do(t, s, http.MethodGet, "nope", a); w.Code != http.StatusNotFound {
		t.Errorf("an unknown session = %d, want 404", w.Code)
	}
	if w := do(t, s, http.MethodDelete, id, a); w.Code != http.StatusNoContent {
		t.Errorf("DELETE = %d", w.Code)
	}
	if w := do(t, s, http.MethodPost, id, a); w.Code != http.StatusNotFound || s.Len() != 0 {
		t.Errorf("after DELETE = %d with %d sessions, want 404 and none", w.Code, s.Len())
	}
}

func TestTheLeastRecentlyUsedSessionIsEvicted(t *testing.T) {
	f := &fake{}
	clk := &clock{t: time.Unix(1e9, 0)}
	s := New(f, Options{Max: 3, Now: clk.now})
	a := agent("laptop", "s")
	var ids []string
	for range 3 {
		clk.t = clk.t.Add(time.Minute)
		ids = append(ids, open(t, s, a))
	}
	clk.t = clk.t.Add(time.Minute)
	do(t, s, http.MethodPost, ids[0], a) // s1 is now the most recent
	other := open(t, s, agent("phone", "s"))
	if s.Len() != 4 {
		t.Fatalf("another token's session evicted one: %d live", s.Len())
	}
	clk.t = clk.t.Add(time.Minute)
	fourth := open(t, s, a)
	if !slices.Equal(f.deleted, []string{ids[1]}) {
		t.Errorf("evicted %v, want [%s], the least recently used", f.deleted, ids[1])
	}
	for _, id := range []string{ids[0], ids[2], fourth} {
		if w := do(t, s, http.MethodPost, id, a); w.Code != http.StatusAccepted {
			t.Errorf("%s = %d after the eviction", id, w.Code)
		}
	}
	if w := do(t, s, http.MethodPost, ids[1], a); w.Code != http.StatusNotFound {
		t.Errorf("the evicted session = %d, want 404", w.Code)
	}
	if w := do(t, s, http.MethodPost, other, agent("phone", "s")); w.Code != http.StatusAccepted {
		t.Errorf("the other token's session = %d", w.Code)
	}
}

func TestTheDefaultBoundIsEight(t *testing.T) {
	f := &fake{}
	s := New(f, Options{})
	for range 9 {
		open(t, s, agent("laptop", "s"))
	}
	if s.Len() != 8 || len(f.deleted) != 1 {
		t.Errorf("%d live, %d evicted; want 8 and 1", s.Len(), len(f.deleted))
	}
}

func TestAnIdleSessionEnds(t *testing.T) {
	f := &fake{}
	clk := &clock{t: time.Unix(1e9, 0)}
	s := New(f, Options{Now: clk.now})
	a := agent("laptop", "s")
	id := open(t, s, a)
	clk.t = clk.t.Add(time.Hour - time.Second)
	if w := do(t, s, http.MethodPost, id, a); w.Code != http.StatusAccepted {
		t.Fatalf("before the hour = %d", w.Code)
	}
	clk.t = clk.t.Add(time.Hour)
	if w := do(t, s, http.MethodPost, id, a); w.Code != http.StatusNotFound {
		t.Errorf("after an idle hour = %d, want 404", w.Code)
	}
	if !slices.Equal(f.deleted, []string{id}) {
		t.Errorf("deleted %v, want the idle session ended in the handler", f.deleted)
	}
}

func TestScopesAndCallers(t *testing.T) {
	s := New(&fake{}, Options{CheckScope: func(scope string) error {
		if scope != "vault=1" {
			return errors.New("want vault=<id>")
		}
		return nil
	}})
	if w := do(t, s, http.MethodPost, "", agent("laptop", "everything")); w.Code != http.StatusForbidden {
		t.Errorf("an unparsable scope = %d, want 403", w.Code)
	}
	open(t, s, agent("laptop", "vault=1"))
	if w := do(t, s, http.MethodPost, "", lodgeapp.Caller{Kind: lodgeapp.Session}); w.Code != http.StatusForbidden {
		t.Errorf("a session caller = %d, want 403", w.Code)
	}
}

func TestTheHandlersOwnNotFoundForgetsTheSession(t *testing.T) {
	f := &fake{}
	s := New(f, Options{})
	a := agent("laptop", "s")
	id := open(t, s, a)
	f.mu.Lock()
	delete(f.live, id) // the handler ended it on its own
	f.mu.Unlock()
	if w := do(t, s, http.MethodPost, id, a); w.Code != http.StatusNotFound || s.Len() != 0 {
		t.Errorf("= %d with %d live, want 404 and none", w.Code, s.Len())
	}
}
