package gateway

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"

	"github.com/frostyard/lodge/internal/events"
	"github.com/frostyard/lodge/internal/oauth"
	"github.com/frostyard/lodge/internal/peer"
	"github.com/frostyard/lodge/internal/tokens"
)

// eventsPath is the event subscriptions' path on the lodge node
// (docs/specs/lodge-http.md, Event subscriptions).
const eventsPath = "/gateway/events"

// eventsBody bounds a subscription's body: 4 KiB.
const eventsBody = 4 << 10

// Events are the event subscriptions of the broker: /gateway/events serves
// them (docs/specs/lodge-http.md, Event subscriptions). A nil Events, or a
// nil OAuth, serves no route.
type Events interface {
	// Subscribe gives the client of access the subscription sub.
	Subscribe(ctx context.Context, access oauth.Access, sub, topic string) (events.Subscription, error)
	// Unsubscribe deletes the client's subscription sub.
	Unsubscribe(ctx context.Context, access oauth.Access, sub string) error
	// List answers the client's subscriptions.
	List(access oauth.Access) []events.Subscription
}

// eventsHeaders sets the confined set of the subscription routes' answers,
// which no browser reads and which no cache keeps.
func eventsHeaders(h http.Header) {
	apartHeaders(h)
	h.Del("Cross-Origin-Resource-Policy")
	dropCredentialsAndCORS(h)
	h.Set("Cache-Control", "no-store")
}

// eventsList is the body of a list answer.
type eventsList struct {
	Subscriptions []events.Subscription `json:"subscriptions"`
}

// eventsRoute serves /gateway/events and /gateway/events/<sub>: the list of
// a client's subscriptions, and the put and delete of 1 of them
// (docs/specs/lodge-http.md, Event subscriptions).
func (g *Gateway) eventsRoute(w http.ResponseWriter, r *http.Request, who peer.WhoIs, segs []string) {
	sw := &statusWriter{ResponseWriter: w}
	var p peer.Peer
	defer func() {
		g.log.Info("event subscriptions", "node", p.NodeName, "method", r.Method, "status", sw.status)
	}()
	w = sw
	// It is 404 with no broker, or with no OAuth server.
	if g.cfg.Events == nil || g.cfg.OAuth == nil {
		fail(w, http.StatusNotFound, "not found", eventsHeaders)
		return
	}
	if len(segs) > 3 {
		fail(w, http.StatusNotFound, "not found", eventsHeaders)
		return
	}
	// 1. Host.
	if !g.Names().lodgeHost(r.Host) {
		fail(w, http.StatusForbidden, "this is not lodge's address", eventsHeaders)
		return
	}
	// 2. No Origin.
	if _, ok := r.Header["Origin"]; ok {
		fail(w, http.StatusForbidden, "browsers may not keep event subscriptions", eventsHeaders)
		return
	}
	// 3. Method: the list alone, or 1 subscription.
	var sub string
	if len(segs) == 3 {
		sub = segs[2]
	}
	allow := "GET"
	if sub != "" {
		allow = "PUT, DELETE"
	}
	switch {
	case sub == "" && r.Method == http.MethodGet:
	case sub != "" && (r.Method == http.MethodPut || r.Method == http.MethodDelete):
	default:
		w.Header().Set("Allow", allow)
		fail(w, http.StatusMethodNotAllowed, "method "+r.Method+" is not allowed here", eventsHeaders)
		return
	}
	// 4. Peer: identity checks a and b.
	var d *denial
	if p, d = g.peerChecks(r, who); d != nil {
		fail(w, d.status, d.line, eventsHeaders)
		return
	}
	// 5. Token, with the events scope. A live token without the scope is
	// 403: the client signs in again for more scope.
	// lodge serves 1 protected-resource document for all of its routes.
	resource := g.cfg.OAuth.Issuer() + "/.well-known/oauth-protected-resource"
	access, err := g.cfg.OAuth.Check(r.Context(), tokens.Bearer(r.Header.Get("Authorization")), oauth.ScopeEvents)
	switch {
	case errors.Is(err, oauth.ErrScope):
		w.Header().Set("WWW-Authenticate",
			`Bearer error="insufficient_scope", scope="`+oauth.ScopeEvents+`", resource_metadata="`+resource+`"`)
		fail(w, http.StatusForbidden, "the token has no scope "+oauth.ScopeEvents, eventsHeaders)
		return
	case err != nil:
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+resource+`"`)
		fail(w, http.StatusUnauthorized, "unauthorized", eventsHeaders)
		return
	}
	// 6. No upgrade.
	if refuseUpgrade(w, r, eventsHeaders) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeEvents(w, http.StatusOK, eventsList{Subscriptions: nonNil(g.cfg.Events.List(access))})
	case http.MethodPut:
		topic, ok := eventsTopic(w, r)
		if !ok {
			return
		}
		s, err := g.cfg.Events.Subscribe(r.Context(), access, sub, topic)
		if err != nil {
			g.eventsFail(w, err)
			return
		}
		writeEvents(w, http.StatusOK, s)
	case http.MethodDelete:
		if err := g.cfg.Events.Unsubscribe(r.Context(), access, sub); err != nil {
			g.eventsFail(w, err)
			return
		}
		eventsHeaders(w.Header())
		w.WriteHeader(http.StatusNoContent)
	}
}

// eventsTopic reads a subscription's body: a JSON object of the form
// {"topic": "..."}, at most 4 KiB.
func eventsTopic(w http.ResponseWriter, r *http.Request) (string, bool) {
	body := struct {
		Topic string `json:"topic"`
	}{}
	data, err := io.ReadAll(io.LimitReader(r.Body, eventsBody+1))
	if err != nil {
		fail(w, http.StatusBadRequest, "the subscription's body cannot be read", eventsHeaders)
		return "", false
	}
	if len(data) > eventsBody {
		fail(w, http.StatusBadRequest, "the subscription's body is larger than 4 KiB", eventsHeaders)
		return "", false
	}
	if err := json.Unmarshal(data, &body, json.RejectUnknownMembers(true)); err != nil || body.Topic == "" {
		fail(w, http.StatusBadRequest, `a subscription is a JSON object of the form {"topic": "..."}`, eventsHeaders)
		return "", false
	}
	return body.Topic, true
}

// eventsFail answers the error a subscription's call made.
func (g *Gateway) eventsFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, events.ErrInvalid):
		fail(w, http.StatusBadRequest, err.Error(), eventsHeaders)
	case errors.Is(err, events.ErrNothing):
		fail(w, http.StatusForbidden, "the grant gives no event under the topic", eventsHeaders)
	case errors.Is(err, events.ErrNotFound):
		fail(w, http.StatusNotFound, err.Error(), eventsHeaders)
	case errors.Is(err, events.ErrConflict):
		fail(w, http.StatusConflict, "another app's subscription, or a revoke, holds it", eventsHeaders)
	default:
		g.log.Error("an event subscription", "err", err)
		fail(w, http.StatusServiceUnavailable, "the broker cannot keep the subscription", eventsHeaders)
	}
}

// writeEvents writes body as a JSON answer.
func writeEvents(w http.ResponseWriter, status int, body any) {
	b, err := json.Marshal(body)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "the broker cannot answer", eventsHeaders)
		return
	}
	h := w.Header()
	eventsHeaders(h)
	h.Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// nonNil answers a list that is JSON's [], not null.
func nonNil(subs []events.Subscription) []events.Subscription {
	if subs == nil {
		return []events.Subscription{}
	}
	return subs
}
