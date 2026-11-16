package gateway

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/frostyard/lodge/internal/events"
	"github.com/frostyard/lodge/internal/oauth"
)

// fakeEvents keeps subscriptions in memory, and answers 1 test's error.
type fakeEvents struct {
	mu     sync.Mutex
	subs   map[string]events.Subscription
	asked  [][2]string
	delete []string
	err    error
}

func newFakeEvents() *fakeEvents { return &fakeEvents{subs: map[string]events.Subscription{}} }

// fail makes every call answer err.
func (f *fakeEvents) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeEvents) Subscribe(_ context.Context, access oauth.Access, sub, topic string) (events.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, [2]string{sub, topic})
	if f.err != nil {
		return events.Subscription{}, f.err
	}
	s := events.Subscription{Name: sub, App: strings.Split(topic, ".")[0], Topic: topic,
		Stream: "app_" + strings.Split(topic, ".")[0], Consumer: access.Client + "-" + sub, Filters: []string{topic}}
	f.subs[sub] = s
	return s, nil
}

func (f *fakeEvents) Unsubscribe(_ context.Context, _ oauth.Access, sub string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delete = append(f.delete, sub)
	if f.err != nil {
		return f.err
	}
	if _, ok := f.subs[sub]; !ok {
		return events.ErrNotFound
	}
	delete(f.subs, sub)
	return nil
}

func (f *fakeEvents) List(oauth.Access) []events.Subscription {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []events.Subscription{}
	for _, s := range f.subs {
		out = append(out, s)
	}
	return out
}

// eventsToken signs a client in with the scopes, and answers its access
// token.
func eventsToken(t *testing.T, h *oauthHarness, scopes ...string) string {
	t.Helper()
	_, tok := h.signin("pi-pocket", scopes...)
	if tok.AccessToken == "" {
		t.Fatalf("the sign-in answered no token: %+v", tok)
	}
	return tok.AccessToken
}

// TestEventsWithoutAService checks that /gateway/events is 404 without the
// broker's subscriptions, and without an OAuth server.
func TestEventsWithoutAService(t *testing.T) {
	h := newHarness(t)
	expect(t, h.get(eventsPath, nil), http.StatusNotFound, "not found")

	o := newOAuthHarness(t)
	a, _ := o.lodgeRequest(http.MethodGet, eventsPath, "", nil)
	expect(t, a, http.StatusNotFound, "not found")
	o.g.cfg.Events = newFakeEvents()
	a, _ = o.lodgeRequest(http.MethodGet, eventsPath, "", nil)
	expect(t, a, http.StatusUnauthorized, "unauthorized")
}

// TestEventsRefusals checks each check of /gateway/events, in its order
// (docs/specs/lodge-http.md, Event subscriptions).
func TestEventsRefusals(t *testing.T) {
	h := newOAuthHarness(t)
	f := newFakeEvents()
	h.g.cfg.Events = f
	good := eventsToken(t, h, "events")
	noScope := eventsToken(t, h, "mcp")
	resource := `Bearer resource_metadata="` + h.oauth.Issuer() + `/.well-known/oauth-protected-resource"`
	for name, c := range map[string]struct {
		method, path, body string
		hdr                map[string]string
		status             int
		part               string
	}{
		"another Host": {method: http.MethodGet, path: eventsPath, hdr: map[string]string{"Host": "other.example"},
			status: http.StatusForbidden, part: "this is not lodge's address"},
		"an Origin": {method: http.MethodGet, path: eventsPath, hdr: map[string]string{"Origin": lodgeOrigin},
			status: http.StatusForbidden, part: "browsers may not keep event subscriptions"},
		"a POST on the list": {method: http.MethodPost, path: eventsPath,
			status: http.StatusMethodNotAllowed, part: "is not allowed here"},
		"a PUT on the list": {method: http.MethodPut, path: eventsPath, body: `{"topic":"marks"}`,
			status: http.StatusMethodNotAllowed, part: "is not allowed here"},
		"a GET on 1 subscription": {method: http.MethodGet, path: eventsPath + "/s1",
			status: http.StatusMethodNotAllowed, part: "is not allowed here"},
		"no token": {method: http.MethodGet, path: eventsPath, hdr: map[string]string{"Authorization": ""},
			status: http.StatusUnauthorized, part: "unauthorized"},
		"a token without the scope": {method: http.MethodGet, path: eventsPath,
			hdr:    map[string]string{"Authorization": "Bearer " + noScope},
			status: http.StatusForbidden, part: "the token has no scope events"},
		"an upgrade": {method: http.MethodGet, path: eventsPath,
			hdr:    map[string]string{"Authorization": "Bearer " + good, "Connection": "upgrade", "Upgrade": "websocket"},
			status: http.StatusBadRequest, part: "this route takes no upgrade"},
	} {
		t.Run(name, func(t *testing.T) {
			hdr := map[string]string{"Authorization": "Bearer " + good}
			for k, v := range c.hdr {
				hdr[k] = v
			}
			a, hh := h.lodgeRequest(c.method, c.path, c.body, hdr)
			expect(t, a, c.status, c.part)
			if c.status == http.StatusMethodNotAllowed && hh.Get("Allow") == "" {
				t.Error("the answer names no Allow header")
			}
			if c.status == http.StatusUnauthorized && hh.Get("WWW-Authenticate") != resource {
				t.Errorf("WWW-Authenticate is %q, want %q", hh.Get("WWW-Authenticate"), resource)
			}
			if c.status == http.StatusForbidden && strings.Contains(c.part, "scope") {
				want := `Bearer error="insufficient_scope", scope="events", resource_metadata="` +
					h.oauth.Issuer() + `/.well-known/oauth-protected-resource"`
				if got := hh.Get("WWW-Authenticate"); got != want {
					t.Errorf("WWW-Authenticate is %q, want %q", got, want)
				}
			}
			if a.header.Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control is %q, want no-store", a.header.Get("Cache-Control"))
			}
		})
	}
	if len(f.asked) != 0 {
		t.Errorf("the service was asked for %q, want nothing", f.asked)
	}
}

// TestEventsSubscriptions checks the 3 calls: put, list and delete.
func TestEventsSubscriptions(t *testing.T) {
	h := newOAuthHarness(t)
	f := newFakeEvents()
	h.g.cfg.Events = f
	token := eventsToken(t, h, "events")
	auth := map[string]string{"Authorization": "Bearer " + token}

	a, hh := h.lodgeRequest(http.MethodPut, eventsPath+"/s1", `{"topic":"marks.bookmark.created"}`, auth)
	expect(t, a, http.StatusOK, `"name":"s1"`)
	if got := hh.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type is %q, want application/json", got)
	}
	var sub events.Subscription
	if err := json.Unmarshal([]byte(a.body), &sub); err != nil {
		t.Fatalf("the answer %q: %v", a.body, err)
	}
	if sub.App != "marks" || sub.Topic != "marks.bookmark.created" {
		t.Errorf("the subscription is %+v", sub)
	}
	if want := [2]string{"s1", "marks.bookmark.created"}; f.asked[0] != want {
		t.Errorf("the service was asked for %q, want %q", f.asked[0], want)
	}

	a, _ = h.lodgeRequest(http.MethodGet, eventsPath, "", auth)
	expect(t, a, http.StatusOK, `"subscriptions":[`)
	if !strings.Contains(a.body, `"name":"s1"`) {
		t.Errorf("the list is %q, want s1 in it", a.body)
	}

	a, _ = h.lodgeRequest(http.MethodDelete, eventsPath+"/s1", "", auth)
	expect(t, a, http.StatusNoContent, "")
	if want := []string{"s1"}; len(f.delete) != 1 || f.delete[0] != want[0] {
		t.Errorf("the service deleted %q, want %q", f.delete, want)
	}
	a, _ = h.lodgeRequest(http.MethodGet, eventsPath, "", auth)
	expect(t, a, http.StatusOK, `"subscriptions":[]`)
}

// TestEventsErrors checks the answers of the service's errors, and of a
// broken body.
func TestEventsErrors(t *testing.T) {
	h := newOAuthHarness(t)
	f := newFakeEvents()
	h.g.cfg.Events = f
	auth := map[string]string{"Authorization": "Bearer " + eventsToken(t, h, "events")}
	for name, c := range map[string]struct {
		body   string
		err    error
		status int
	}{
		"a body that is not JSON":  {body: `{`, status: http.StatusBadRequest},
		"an unknown member":        {body: `{"topic":"marks","more":1}`, status: http.StatusBadRequest},
		"no topic":                 {body: `{}`, status: http.StatusBadRequest},
		"a body that is no object": {body: `"marks"`, status: http.StatusBadRequest},
		"a name that breaks its form": {body: `{"topic":"marks"}`, err: events.ErrInvalid,
			status: http.StatusBadRequest},
		"a grant that reads nothing": {body: `{"topic":"marks"}`, err: events.ErrNothing,
			status: http.StatusForbidden},
		"an app that is not there": {body: `{"topic":"marks"}`, err: events.ErrNotFound,
			status: http.StatusNotFound},
		"another app's subscription": {body: `{"topic":"marks"}`, err: events.ErrConflict,
			status: http.StatusConflict},
		"a broker that does not answer": {body: `{"topic":"marks"}`, err: context.DeadlineExceeded,
			status: http.StatusServiceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			f.fail(c.err)
			a, _ := h.lodgeRequest(http.MethodPut, eventsPath+"/s1", c.body, auth)
			expect(t, a, c.status, "")
			if a.header.Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control is %q, want no-store", a.header.Get("Cache-Control"))
			}
		})
	}

	// A body over 4 KiB is refused.
	f.fail(nil)
	big := `{"topic":"` + strings.Repeat("a", 4<<10) + `"}`
	a, _ := h.lodgeRequest(http.MethodPut, eventsPath+"/s1", big, auth)
	expect(t, a, http.StatusBadRequest, "larger than 4 KiB")
	// A subscription that is not there is 404.
	a, _ = h.lodgeRequest(http.MethodDelete, eventsPath+"/gone", "", auth)
	expect(t, a, http.StatusNotFound, "")
}

// A DELETE of a name that breaks its form is 400, and not 503: the service
// refuses the name as invalid before it reaches the broker.
func TestEventsDeleteName(t *testing.T) {
	h := newOAuthHarness(t)
	f := newFakeEvents()
	h.g.cfg.Events = f
	auth := map[string]string{"Authorization": "Bearer " + eventsToken(t, h, "events")}

	f.fail(events.ErrInvalid)
	a, _ := h.lodgeRequest(http.MethodDelete, eventsPath+"/s-1", "", auth)
	expect(t, a, http.StatusBadRequest, "")
	if a.header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control is %q, want no-store", a.header.Get("Cache-Control"))
	}

	// A name that is well formed, and not there, is 404.
	f.fail(nil)
	a, _ = h.lodgeRequest(http.MethodDelete, eventsPath+"/gone", "", auth)
	expect(t, a, http.StatusNotFound, "")
}
