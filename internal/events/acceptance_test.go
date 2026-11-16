package events_test

import (
	"encoding/json/v2"
	"errors"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/frostyard/lodge/internal/broker"
	"github.com/frostyard/lodge/internal/events"
	"github.com/frostyard/lodge/internal/oauth"
)

// marksRead is a grant item on the app marks' read capability, on path.
func marksRead(path string) oauth.Item {
	it := oauth.Item{Cap: "marks:read"}
	if path != "" {
		it.Sel = map[string]string{"folder": path}
	}
	return it
}

// TestSubscribeByTopic checks the subscription of a token with the events
// scope: the consumer's filters are the grant's read prefixes and the
// subscription's resync subject, and a token without the scope is refused.
func TestSubscribeByTopic(t *testing.T) {
	h := newHarness(t)
	_, token := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/f3")})

	sub, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "marks.bookmark.created")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if sub.App != marksApp || sub.Stream != "app_marks" || sub.Name != "s1" {
		t.Errorf("the subscription is %+v", sub)
	}
	state, err := h.b.ConsumerState(t.Context(), "app_marks", sub.Consumer)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"lodge.marks.resync." + sub.Consumer, "marks.bookmark.created.folder.f3",
		"marks.bookmark.created.folder.f3.>"}
	got := slices.Clone(state.Filters)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("the consumer's filters are %q, want %q", got, want)
	}
	if !slices.Equal(sub.Filters, state.Filters) {
		t.Errorf("the subscription answers the filters %q, but the consumer has %q", sub.Filters, state.Filters)
	}
	if got := h.svc.List(h.access(t, token)); len(got) != 1 || got[0].Name != "s1" || got[0].Topic != "marks.bookmark.created" {
		t.Errorf("List answers %+v", got)
	}

	// A token without the events scope is refused, by the door and by the
	// service's token check.
	_, mcpToken := h.auth.newClient(t, "chani", "mcp")
	if _, _, ok := h.svc.Tokens(t.Context(), mcpToken); ok {
		t.Error("the tokens check admits a token without the events scope")
	}
	if _, err := h.connect(t, mcpToken); err == nil {
		t.Error("the client door admits a token without the events scope")
	}

	// A grant that reads nothing under the topic refuses the subscription.
	h.auth.grant("chani", marksApp, []oauth.Item{{Cap: "marks:write"}})
	if _, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s3", "marks"); !errors.Is(err, events.ErrNothing) {
		t.Errorf("a grant that reads nothing answered %v, want ErrNothing", err)
	}
	// An app that is not installed is not found.
	if _, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s3", "notes.bookmark.created"); !errors.Is(err, events.ErrNotFound) {
		t.Errorf("an app that is not there answered %v, want ErrNotFound", err)
	}
	// A name or a topic that breaks its form is invalid.
	if _, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s-1", "marks"); !errors.Is(err, events.ErrInvalid) {
		t.Errorf("a subscription name with a dash answered %v, want ErrInvalid", err)
	}
	if _, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s3", "marks.>"); !errors.Is(err, events.ErrInvalid) {
		t.Errorf("a topic with > answered %v, want ErrInvalid", err)
	}
}

// TestOneFolderOfMany checks that a grant on 1 folder takes its 10 events,
// and no other folder's.
func TestOneFolderOfMany(t *testing.T) {
	h := newHarness(t)
	_, token := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/f3")})
	sub, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "marks.bookmark.created")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	for folder := range 10 {
		for range 10 {
			h.publish(t, "marks.bookmark.created.folder.f"+strconv.Itoa(folder))
		}
	}
	wait(t, func() (int, error) {
		state, err := h.b.StreamState(t.Context(), marksApp)
		if err != nil {
			return 0, err
		}
		if state.Msgs != 100 {
			return 0, errNotYet
		}
		return int(state.Msgs), nil
	})

	nc := h.subscribed(t, token)
	msgs := pull(t, nc, "app_marks", sub.Consumer, 100, 500*time.Millisecond)
	assertOnlyFolder(t, msgs, "f3")
}

// assertOnlyFolder fails the test unless msgs are exactly 10 of the folder's
// events.
func assertOnlyFolder(t *testing.T, msgs []*nats.Msg, folder string) {
	t.Helper()
	if len(msgs) != 10 {
		t.Fatalf("the pull answered %d messages, want 10 of %s", len(msgs), folder)
	}
	for _, m := range msgs {
		if !strings.HasSuffix(m.Subject, ".folder."+folder) {
			t.Errorf("the pull answered %q, want a %s event", m.Subject, folder)
		}
	}
}

// TestNarrowing checks that a narrower grant stops the events that a client
// has not pulled: only the new folder's messages come.
func TestNarrowing(t *testing.T) {
	h := newHarness(t)
	_, token := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/")})
	sub, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "marks.bookmark.created")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	for folder := range 2 {
		for range 10 {
			h.publish(t, "marks.bookmark.created.folder.f"+strconv.Itoa(folder))
		}
	}
	wait(t, func() (int, error) {
		state, err := h.b.StreamState(t.Context(), marksApp)
		if err != nil {
			return 0, err
		}
		if state.Msgs != 20 {
			return 0, errNotYet
		}
		return int(state.Msgs), nil
	})

	// The grant narrows to f1, and the hook refilters the subscription.
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/f1")})
	h.svc.GrantChanged("chani", marksApp)
	state, err := h.b.ConsumerState(t.Context(), "app_marks", sub.Consumer)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(state.Filters, "marks.bookmark.created.folder.f1") {
		t.Errorf("the consumer's filters are %q, want f1's", state.Filters)
	}

	nc := h.subscribed(t, token)
	msgs := pull(t, nc, "app_marks", sub.Consumer, 100, 500*time.Millisecond)
	assertOnlyFolder(t, msgs, "f1")
}

// TestRevoke checks that a revoke closes the client's connection, deletes its
// consumer, and that the old token is refused.
func TestRevoke(t *testing.T) {
	h := newHarness(t)
	id, token := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/f3")})
	sub, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "marks.bookmark.created")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	closed := make(chan struct{})
	nc, err := h.connect(t, token)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	nc.SetClosedHandler(func(*nats.Conn) { close(closed) })
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}

	h.auth.revokeClient(id)
	h.svc.Revoked("chani", []string{id})
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the client's connection is still open after its revoke")
	}
	wait(t, func() (bool, error) {
		if _, err := h.b.ConsumerState(t.Context(), "app_marks", sub.Consumer); err == nil {
			return false, errNotYet
		}
		return true, nil
	})

	// A new connection with the old token is refused.
	nc2, err := h.connect(t, token)
	if err == nil {
		nc2.Close()
		t.Fatal("a connection with a revoked token was admitted")
	}
	if !strings.Contains(err.Error(), "Authorization Violation") &&
		!errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET) {
		t.Errorf("the refusal was %v, want an authorization violation", err)
	}
}

// TestStreamLimits checks what the app's stream keeps and takes.
func TestStreamLimits(t *testing.T) {
	h := newHarness(t)
	state, err := h.b.StreamState(t.Context(), marksApp)
	if err != nil {
		t.Fatal(err)
	}
	if state.MaxAge != 7*24*time.Hour {
		t.Errorf("the stream keeps a message for %s, want 7 days", state.MaxAge)
	}
	if state.MaxBytes != 1<<30 {
		t.Errorf("the stream holds %d bytes, want 1 GiB", state.MaxBytes)
	}
	if !state.DiscardOld {
		t.Error("the stream does not discard its oldest messages")
	}
	want := []string{"lodge.marks.>", "marks.>"}
	got := slices.Clone(state.Subjects)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("the stream takes %q, want %q", got, want)
	}
}

// TestResync checks the resync event: a consumer that retention overtook gets
// 1 event that says so, no second one, and the events after it come normally.
func TestResync(t *testing.T) {
	h := newHarness(t, func(o *broker.Options) {
		o.StreamMaxAge = 200 * time.Millisecond
		o.StreamDuplicates = 100 * time.Millisecond
	})
	_, token := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/f3")})
	sub, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "marks.bookmark.created")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	for range 3 {
		h.publish(t, "marks.bookmark.created.folder.f3")
	}
	// The 3 events go with the stream's max age, and the consumer never saw
	// them: the stream's first message is past its position.
	wait(t, func() (uint64, error) {
		state, err := h.b.StreamState(t.Context(), marksApp)
		if err != nil {
			return 0, err
		}
		if state.FirstSeq <= 3 {
			return 0, errNotYet
		}
		return state.FirstSeq, nil
	})

	// The connection publishes the resync event before it reaches the
	// server, so the client's first pull gets it.
	nc := h.subscribed(t, token)
	msgs := pull(t, nc, "app_marks", sub.Consumer, 1, time.Second)
	if len(msgs) != 1 {
		t.Fatalf("the first pull answered %d messages, want the resync event", len(msgs))
	}
	resync := msgs[0]
	if resync.Subject != "lodge.marks.resync."+sub.Consumer {
		t.Fatalf("the first message is %q, want the resync subject", resync.Subject)
	}
	var body struct {
		SpecVersion string `json:"specversion"`
		ID          string `json:"id"`
		Source      string `json:"source"`
		Type        string `json:"type"`
		Subject     string `json:"subject"`
		Time        string `json:"time"`
	}
	if err := json.Unmarshal(resync.Data, &body); err != nil {
		t.Fatalf("the resync event %q: %v", resync.Data, err)
	}
	if body.SpecVersion != "1.0" || body.Type != broker.ResyncType || body.Source != "/lodge" ||
		body.Subject != marksApp || body.ID == "" || body.Time == "" {
		t.Errorf("the resync event is %+v", body)
	}
	if _, err := time.Parse(time.RFC3339Nano, body.Time); err != nil {
		t.Errorf("the resync event's time %q: %v", body.Time, err)
	}

	// A second sweep publishes no second resync event for the consumer.
	if err := h.svc.Sweep(t.Context()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if msgs := pull(t, nc, "app_marks", sub.Consumer, 1, 300*time.Millisecond); len(msgs) != 0 {
		t.Errorf("a second sweep published %d messages, want none", len(msgs))
	}

	// An acknowledged resync event lets the next event through.
	if err := resync.Ack(); err != nil {
		t.Fatalf("ack the resync event: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	wait(t, func() (uint64, error) {
		state, err := h.b.ConsumerState(t.Context(), "app_marks", sub.Consumer)
		if err != nil {
			return 0, err
		}
		if state.AckFloor == 0 {
			return 0, errNotYet
		}
		return state.AckFloor, nil
	})
	h.publish(t, "marks.bookmark.created.folder.f3")
	msgs = pull(t, nc, "app_marks", sub.Consumer, 1, time.Second)
	if len(msgs) != 1 {
		t.Fatalf("the pull after the resync answered %d messages, want 1", len(msgs))
	}
	if msgs[0].Subject != "marks.bookmark.created.folder.f3" {
		t.Errorf("the pull after the resync answered %q, want the new event", msgs[0].Subject)
	}
	if !slices.Contains(sub.Filters, "lodge.marks.resync."+sub.Consumer) {
		t.Errorf("the subscription's filters %q hold no resync subject", sub.Filters)
	}
}

// TestSweepRevokesAGoneClient checks the sweep's own correction: a client
// whose OAuth client is gone, or has lost the events scope, is revoked, and
// 1 that still holds it is refiltered.
func TestSweepRevokesAGoneClient(t *testing.T) {
	h := newHarness(t)
	gone, goneToken := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/f3")})
	goneSub, err := h.svc.Subscribe(t.Context(), h.access(t, goneToken), "s1", "marks.bookmark.created")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	// The client loses the events scope: its subscription goes with it.
	h.auth.setScope(gone, "mcp")
	// Another client keeps its own subscription, refiltered by the sweep.
	_, liveToken := h.auth.newClient(t, "other", "events")
	h.auth.grant("other", marksApp, []oauth.Item{marksRead("/")})
	sub, err := h.svc.Subscribe(t.Context(), h.access(t, liveToken), "s1", "marks.bookmark.created")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	h.auth.grant("other", marksApp, []oauth.Item{marksRead("/f1")})

	if err := h.svc.Sweep(t.Context()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	wait(t, func() (bool, error) {
		if _, err := h.b.ConsumerState(t.Context(), "app_marks", goneSub.Consumer); err == nil {
			return false, errNotYet
		}
		return true, nil
	})
	state, err := h.b.ConsumerState(t.Context(), "app_marks", sub.Consumer)
	if err != nil {
		t.Fatalf("the live client's consumer: %v", err)
	}
	if !slices.Contains(state.Filters, "marks.bookmark.created.folder.f1") {
		t.Errorf("the sweep left the filters %q, want f1's", state.Filters)
	}
}

// TestRefilterAfterAnEmptyGrant checks the path a grant takes through nothing
// and back: the consumer keeps its resync subject alone, an event published
// while the grant gives nothing is not read, and the events after the grant
// comes back are.
func TestRefilterAfterAnEmptyGrant(t *testing.T) {
	h := newHarness(t)
	_, token := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/f3")})
	sub, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "marks.bookmark.created")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	nc := h.subscribed(t, token)

	// The grant goes: the consumer keeps its resync subject alone. Its
	// single filter is what the server holds in the consumer's own filter
	// subject, and the next refilter replaces it.
	h.auth.grant("chani", marksApp, nil)
	h.svc.GrantChanged("chani", marksApp)
	state, err := h.b.ConsumerState(t.Context(), "app_marks", sub.Consumer)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"lodge.marks.resync." + sub.Consumer}; !slices.Equal(state.Filters, want) {
		t.Fatalf("the consumer's filters are %q, want %q alone", state.Filters, want)
	}

	// An event published while the grant gives nothing is not read: the
	// client's pull moves its position past it.
	h.publish(t, "marks.bookmark.created.folder.f3")
	if msgs := pull(t, nc, "app_marks", sub.Consumer, 1, 300*time.Millisecond); len(msgs) != 0 {
		t.Fatalf("the pull answered %d messages, want none", len(msgs))
	}

	// The grant comes back: the consumer takes the folder again, and the
	// events after it come normally.
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/f3")})
	h.svc.GrantChanged("chani", marksApp)
	state, err = h.b.ConsumerState(t.Context(), "app_marks", sub.Consumer)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(state.Filters, "marks.bookmark.created.folder.f3") {
		t.Fatalf("the consumer's filters are %q, want the folder's", state.Filters)
	}
	h.publish(t, "marks.bookmark.created.folder.f3")
	msgs := pull(t, nc, "app_marks", sub.Consumer, 1, time.Second)
	if len(msgs) != 1 {
		t.Fatalf("the pull after the grant came back answered %d messages, want 1", len(msgs))
	}
	if msgs[0].Subject != "marks.bookmark.created.folder.f3" {
		t.Errorf("the pull answered %q, want the event after the grant came back", msgs[0].Subject)
	}
}

// TestTooManyFilters checks that a grant too wide for 1 consumer is an
// invalid request, and that a narrower topic fits.
func TestTooManyFilters(t *testing.T) {
	h := newHarness(t)
	_, token := h.auth.newClient(t, "chani", "events")
	items := make([]oauth.Item, 0, 16)
	for i := range 16 {
		items = append(items, marksRead("/f"+strconv.Itoa(i)))
	}
	h.auth.grant("chani", marksApp, items)
	// 16 folders of 2 events is 64 read filters, and the subscription's own
	// resync subject is the 65th.
	_, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "marks")
	if !errors.Is(err, events.ErrInvalid) {
		t.Fatalf("a grant of 65 filters answered %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "narrow the topic") {
		t.Errorf("the answer %q does not say to narrow the topic", err)
	}
	// 1 event's topic alone fits: 32 read filters and the resync subject.
	sub, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s2", "marks.bookmark.updated")
	if err != nil {
		t.Fatalf("a narrower topic: %v", err)
	}
	if got := len(sub.Filters); got != 33 {
		t.Errorf("the subscription has %d filters, want 33", got)
	}
}
