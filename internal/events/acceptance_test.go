package events_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// TestAppsChangedRefilters checks a reload's refilter: a grant that a new
// manifest takes out of force, such as an app's next major version, leaves
// each subscription with its resync subject alone at once, without a grant
// change's hook or a sweep, and a grant back in force reads again.
func TestAppsChangedRefilters(t *testing.T) {
	h := newHarness(t)
	_, token := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/")})
	sub, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "marks.bookmark.created")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	// The grant the service reads gives nothing now, as after the app's
	// next major version; only the reload says so.
	h.auth.grant("chani", marksApp, nil)
	h.svc.AppsChanged()
	state, err := h.b.ConsumerState(t.Context(), "app_marks", sub.Consumer)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"lodge.marks.resync." + sub.Consumer}; !slices.Equal(state.Filters, want) {
		t.Fatalf("the consumer's filters after the reload are %q, want %q alone", state.Filters, want)
	}
	// A new consent and a reload: the folder is read again.
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/f1")})
	h.svc.AppsChanged()
	state, err = h.b.ConsumerState(t.Context(), "app_marks", sub.Consumer)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(state.Filters, "marks.bookmark.created.folder.f1") {
		t.Errorf("the consumer's filters are %q, want f1's", state.Filters)
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

// TestSubscribeSeesAGrantChange checks the window a subscription opens: a
// grant change that runs while the subscription reads the grant, and finishes
// before its consumer is registered, still reaches that consumer.
func TestSubscribeSeesAGrantChange(t *testing.T) {
	h := newHarness(t)
	_, token := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/")})
	read, release := h.auth.blockNextGrant()
	var (
		sub events.Subscription
		err error
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		sub, err = h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "marks.bookmark.created")
	}()
	// The subscription has the root grant, and has registered nothing yet.
	<-read
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/f3")})
	h.svc.GrantChanged("chani", marksApp)
	close(release)
	<-done
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// The consumer takes the grant as it is now, not as it was.
	state, err := h.b.ConsumerState(t.Context(), "app_marks", sub.Consumer)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(state.Filters, "marks.bookmark.created.folder.f3") ||
		slices.Contains(state.Filters, "marks.bookmark.created.folder.>") {
		t.Fatalf("the consumer's filters are %q, want f3's alone", state.Filters)
	}
	h.publish(t, "marks.bookmark.created.folder.f1")
	h.publish(t, "marks.bookmark.created.folder.f3")
	nc := h.subscribed(t, token)
	msgs := pull(t, nc, "app_marks", sub.Consumer, 10, 500*time.Millisecond)
	if len(msgs) != 1 {
		t.Fatalf("the pull answered %d messages, want f3's alone", len(msgs))
	}
	if !strings.HasSuffix(msgs[0].Subject, ".folder.f3") {
		t.Errorf("the pull answered %q, want an f3 event", msgs[0].Subject)
	}
}

// TestAFailedRefilterRevokesItsClient checks that a refilter which cannot read
// the grant ends the client's delivery instead of leaving the wider grant it
// had.
func TestAFailedRefilterRevokesItsClient(t *testing.T) {
	h := newHarness(t)
	id, token := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/")})
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

	// The grant narrows, and reading it fails: the client is revoked, so the
	// events outside the grant cannot reach it.
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/f3")})
	h.auth.failGrants(1, errors.New("the store did not answer"))
	h.svc.GrantChanged("chani", marksApp)
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the client's connection is still open after its refilter failed")
	}
	wait(t, func() (bool, error) {
		if _, err := h.b.ConsumerState(t.Context(), "app_marks", sub.Consumer); err == nil {
			return false, errNotYet
		}
		return true, nil
	})

	// The old token is still an OAuth token, so a new connection opens, but
	// the broker keeps no subscription for the client: nothing outside the
	// grant can be delivered.
	name, ok := events.ClientName(id)
	if !ok {
		t.Fatalf("ClientName(%q) is refused", id)
	}
	if subs := h.b.ClientSubscriptions(name); len(subs) != 0 {
		t.Errorf("the broker keeps the subscriptions %+v", subs)
	}
	nc2 := h.subscribed(t, token)
	if err := nc2.Flush(); err != nil {
		t.Fatal(err)
	}
	h.publish(t, "marks.bookmark.created.folder.f1")
	if subs := h.svc.List(h.access(t, token)); len(subs) != 0 {
		t.Errorf("the client lists the subscriptions %+v, want none", subs)
	}
	if subs := h.b.ClientSubscriptions(name); len(subs) != 0 {
		t.Errorf("the client's new connection holds the subscriptions %+v", subs)
	}
}

// TestAStoppedSweepRevokesNoClient checks that a sweep whose context lodge
// cancels, as it does when it stops, takes its failed refilters for what they
// are: they say nothing about a grant, so no client is revoked.
func TestAStoppedSweepRevokesNoClient(t *testing.T) {
	h := newHarness(t)
	id, token := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/")})
	if _, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "marks.bookmark.created"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := h.svc.Sweep(ctx); err == nil {
		t.Error("a cancelled sweep answered no error")
	}
	name, ok := events.ClientName(id)
	if !ok {
		t.Fatalf("ClientName(%q) is refused", id)
	}
	if subs := h.b.ClientSubscriptions(name); len(subs) != 1 {
		t.Errorf("after a cancelled sweep the client holds %d subscriptions, want 1", len(subs))
	}
}

// TestListAnswersTheConsumersFilters checks that a list names the filters the
// consumer has, and not the ones a grant would give: after a refilter that
// fails closed, the consumer keeps its resync subject alone.
func TestListAnswersTheConsumersFilters(t *testing.T) {
	h := newHarness(t)
	_, token := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/f0")})
	sub, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "marks")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	// The grant widens past what 1 consumer takes: the refilter fails closed.
	items := make([]oauth.Item, 0, 16)
	for i := range 16 {
		items = append(items, marksRead("/f"+strconv.Itoa(i)))
	}
	h.auth.grant("chani", marksApp, items)
	h.svc.GrantChanged("chani", marksApp)
	want := []string{"lodge.marks.resync." + sub.Consumer}
	state, err := h.b.ConsumerState(t.Context(), "app_marks", sub.Consumer)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(state.Filters, want) {
		t.Fatalf("the consumer's filters are %q, want %q", state.Filters, want)
	}
	got := h.svc.List(h.access(t, token))
	if len(got) != 1 {
		t.Fatalf("the list holds %d subscriptions, want 1", len(got))
	}
	if !slices.Equal(got[0].Filters, want) {
		t.Errorf("the list answers the filters %q, want %q", got[0].Filters, want)
	}
}

// TestUnsubscribeRefusesABadName checks that a subscription name which breaks
// its form is an invalid request, and not a failure of the broker.
func TestUnsubscribeRefusesABadName(t *testing.T) {
	h := newHarness(t)
	_, token := h.auth.newClient(t, "chani", "events")
	for _, name := range []string{"s-1", "", "S1", "s 1"} {
		if err := h.svc.Unsubscribe(t.Context(), h.access(t, token), name); !errors.Is(err, events.ErrInvalid) {
			t.Errorf("Unsubscribe(%q) answered %v, want ErrInvalid", name, err)
		}
	}
	// A name that is well formed, and not there, is not found.
	if err := h.svc.Unsubscribe(t.Context(), h.access(t, token), "gone"); !errors.Is(err, events.ErrNotFound) {
		t.Errorf("Unsubscribe of a subscription that is not there answered %v, want ErrNotFound", err)
	}
}

// TestANewSubscriptionDeliversNothingBeforeItsGrantIsRead checks the window a
// new subscription opens: it is registered before its grant is read, and it
// delivers nothing until then, so a grant that narrows in between cannot be
// overtaken by the events of the wider 1.
func TestANewSubscriptionDeliversNothingBeforeItsGrantIsRead(t *testing.T) {
	h := newHarness(t)
	_, token := h.auth.newClient(t, "chani", "events")
	// The grant is wide when the subscription's precheck reads it, and
	// narrow when the read that installs the filters runs.
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/")})
	client, _, ok := h.svc.Tokens(t.Context(), token)
	if !ok {
		t.Fatal("the service refuses the token")
	}
	hooks := 0
	events.SetBeforeActivate(h.svc, func() {
		hooks++
		// The grant narrows, and an event of another folder is published
		// while the consumer waits for its filters.
		h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/f3")})
		h.publish(t, "marks.bookmark.created.folder.f1")
		state, err := h.b.ConsumerState(t.Context(), "app_marks", client+"-s1")
		if err != nil {
			t.Errorf("the consumer before its activation: %v", err)
			return
		}
		want := []string{"lodge.marks.resync." + client + "-s1"}
		if !slices.Equal(state.Filters, want) {
			t.Errorf("the consumer's filters before its activation are %q, want %q", state.Filters, want)
		}
		nc := h.subscribed(t, token)
		if msgs := pull(t, nc, "app_marks", client+"-s1", 10, 300*time.Millisecond); len(msgs) != 0 {
			t.Errorf("the pull before the activation answered %d messages, want none", len(msgs))
		}
	})
	sub, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "marks.bookmark.created")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if hooks != 1 {
		t.Fatalf("the hook ran %d times, want 1", hooks)
	}

	// The subscription is active with the grant as it is now.
	state, err := h.b.ConsumerState(t.Context(), "app_marks", sub.Consumer)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(state.Filters, "marks.bookmark.created.folder.f3") ||
		slices.Contains(state.Filters, "marks.bookmark.created.folder.>") {
		t.Fatalf("the consumer's filters are %q, want f3's alone", state.Filters)
	}
	// An event of the grant arrives, and the 1 of another folder never does.
	nc := h.subscribed(t, token)
	h.publish(t, "marks.bookmark.created.folder.f3")
	msgs := pull(t, nc, "app_marks", sub.Consumer, 10, 500*time.Millisecond)
	if len(msgs) != 1 || !strings.HasSuffix(msgs[0].Subject, ".folder.f3") {
		t.Fatalf("the pull answered %d messages, want the f3 event alone", len(msgs))
	}
}

// TestAClientLookupFailureRevokesItsClient checks that a refilter hook which
// cannot name a subscription's client revokes it, instead of leaving the
// filters the grant no longer gives.
func TestAClientLookupFailureRevokesItsClient(t *testing.T) {
	h := newHarness(t)
	id, token := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/")})
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

	// The grant narrows, and the client cannot be read: the hook revokes it.
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/f3")})
	h.auth.failClients(1, errors.New("the store did not answer"))
	h.svc.GrantChanged("chani", marksApp)
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the client's connection is still open after its client could not be read")
	}
	wait(t, func() (bool, error) {
		if _, err := h.b.ConsumerState(t.Context(), "app_marks", sub.Consumer); err == nil {
			return false, errNotYet
		}
		return true, nil
	})

	// It keeps no subscription, and an event outside the grant has no
	// consumer to reach.
	name, ok := events.ClientName(id)
	if !ok {
		t.Fatalf("ClientName(%q) is refused", id)
	}
	h.publish(t, "marks.bookmark.created.folder.f1")
	if subs := h.b.ClientSubscriptions(name); len(subs) != 0 {
		t.Errorf("the broker keeps the subscriptions %+v", subs)
	}
	if subs := h.svc.List(h.access(t, token)); len(subs) != 0 {
		t.Errorf("the client lists the subscriptions %+v, want none", subs)
	}
}

// TestARePutKeepsTheBacklog checks that subscribing a registered subscription
// again keeps the filters its consumer has: it may hold events that its grant
// allows it to read, and a consumer that starts with its resync subject alone
// would pull past them.
func TestARePutKeepsTheBacklog(t *testing.T) {
	h := newHarness(t)
	_, token := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/")})
	sub, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "marks.bookmark.created")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	for range 3 {
		h.publish(t, "marks.bookmark.created.folder.f0")
	}
	// The same subscription is made again, with the same topic: it keeps the
	// filters that its grant gives, and no activation runs for it.
	again, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "marks.bookmark.created")
	if err != nil {
		t.Fatalf("Subscribe again: %v", err)
	}
	want := []string{"marks.bookmark.created.folder", "marks.bookmark.created.folder.>"}
	for _, filter := range want {
		if !slices.Contains(again.Filters, filter) {
			t.Errorf("the subscription's filters are %q, want %q", again.Filters, want)
		}
	}

	// The 3 events that the subscription was allowed to read all come.
	nc := h.subscribed(t, token)
	msgs := pull(t, nc, "app_marks", sub.Consumer, 10, time.Second)
	if len(msgs) != 3 {
		t.Fatalf("the pull answered %d messages, want the 3 events of the backlog", len(msgs))
	}
}

// TestAnUnsubscribeDuringActivationKeepsTheOthers checks that a subscription
// that another request removes while it is activated leaves its client's other
// subscriptions, and their delivery, alone: the client is not revoked.
func TestAnUnsubscribeDuringActivationKeepsTheOthers(t *testing.T) {
	h := newHarness(t)
	_, token := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/")})
	keep, err := h.svc.Subscribe(t.Context(), h.access(t, token), "keep", "marks.bookmark.created")
	if err != nil {
		t.Fatalf("Subscribe keep: %v", err)
	}
	// The new subscription is asked to go while it is activated: the
	// unsubscribe waits for the lock, and finds the subscription once the
	// activation has installed its filters.
	unsubscribed := make(chan error, 1)
	var once sync.Once
	events.SetBeforeActivate(h.svc, func() {
		once.Do(func() {
			go func() { unsubscribed <- h.svc.Unsubscribe(t.Context(), h.access(t, token), "new") }()
		})
	})
	sub, err := h.svc.Subscribe(t.Context(), h.access(t, token), "new", "marks.bookmark.created")
	if err != nil {
		t.Fatalf("Subscribe new: %v", err)
	}
	if err := <-unsubscribed; err != nil {
		t.Fatalf("Unsubscribe new: %v", err)
	}

	// The subscription the other request removed is gone, and the client
	// keeps its other subscription and its delivery.
	client, _, ok := h.svc.Tokens(t.Context(), token)
	if !ok {
		t.Fatal("the service refuses the token")
	}
	subs := h.b.ClientSubscriptions(client)
	if len(subs) != 1 || subs[0].Sub != "keep" {
		t.Errorf("the client's subscriptions are %+v, want keep alone", subs)
	}
	if _, err := h.b.ConsumerState(t.Context(), "app_marks", sub.Consumer); err == nil {
		t.Errorf("the removed subscription's consumer %s is still there", sub.Consumer)
	}
	if _, err := h.b.ConsumerState(t.Context(), "app_marks", keep.Consumer); err != nil {
		t.Errorf("keep's consumer: %v", err)
	}
	nc := h.subscribed(t, token)
	h.publish(t, "marks.bookmark.created.folder.f0")
	msgs := pull(t, nc, "app_marks", keep.Consumer, 10, time.Second)
	if len(msgs) != 1 {
		t.Fatalf("the pull on the subscription the client kept answered %d messages, want 1", len(msgs))
	}
}

// TestAReplacementDuringActivationStays checks that a subscription's
// registration, its activation and the cleanup of a failed activation are 1
// step: a request that deletes it and puts another 1 in its place during the
// activation waits, and its replacement survives.
func TestAReplacementDuringActivationStays(t *testing.T) {
	h := newHarness(t)
	// The app the replacement belongs to.
	h.addApp(t, "notes")
	_, token := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/")})
	h.auth.grant("chani", "notes", []oauth.Item{{Cap: "notes:read"}})

	replaced := make(chan error, 1)
	completedWhileActivated := false
	// The replacement runs this hook too: a flag, and not a sync.Once, lets
	// its call return at once, so that only the lifecycle lock can hold it.
	var first atomic.Bool
	events.SetBeforeActivate(h.svc, func() {
		if !first.CompareAndSwap(false, true) {
			return
		}
		func() {
			// This subscription's own activation cannot read its grant.
			h.auth.failGrantsForApp(marksApp, 1, errors.New("the store did not answer"))
			// Another request replaces the subscription while it is
			// activated: its delete and its new subscription both need the
			// lock that this activation holds.
			go func() {
				if err := h.svc.Unsubscribe(t.Context(), h.access(t, token), "s1"); err != nil &&
					!errors.Is(err, events.ErrNotFound) {
					replaced <- err
					return
				}
				_, serr := h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "notes.note.created")
				replaced <- serr
			}()
			select {
			case err := <-replaced:
				completedWhileActivated = true
				// The result stays for the check below.
				replaced <- err
			case <-time.After(300 * time.Millisecond):
			}
		}()
	})
	if _, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "marks.bookmark.created"); err == nil {
		t.Fatal("an activation that could not read the grant answered no error")
	}
	if completedWhileActivated {
		t.Error("another request replaced the subscription while it was activated")
	}
	if err := <-replaced; err != nil {
		t.Fatalf("the replacement: %v", err)
	}

	// The replacement is s1 on notes, and the activation's own subscription
	// is gone, with its consumer.
	client, _, ok := h.svc.Tokens(t.Context(), token)
	if !ok {
		t.Fatal("the service refuses the token")
	}
	subs := h.b.ClientSubscriptions(client)
	if len(subs) != 1 || subs[0].Sub != "s1" || subs[0].App != "notes" {
		t.Fatalf("the client's subscriptions are %+v, want s1 on notes", subs)
	}
	if _, err := h.b.ConsumerState(t.Context(), "app_notes", client+"-s1"); err != nil {
		t.Errorf("the replacement's consumer: %v", err)
	}
	if _, err := h.b.ConsumerState(t.Context(), "app_marks", client+"-s1"); err == nil {
		t.Errorf("the consumer of the subscription that failed is still there")
	}
}

// TestARevokedClientGetsNoSubscription checks that a client which OAuth revoked
// after lodge's token check keeps no subscription: the activation checks the
// client again, and the consumer it made goes with the failure.
func TestARevokedClientGetsNoSubscription(t *testing.T) {
	h := newHarness(t)
	id, token := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/")})
	var once sync.Once
	events.SetBeforeActivate(h.svc, func() {
		once.Do(func() { h.auth.revokeClient(id) })
	})
	// The token check passes: the client goes after it.
	_, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "marks.bookmark.created")
	if err == nil {
		t.Fatal("a revoked client's subscription was admitted")
	}
	if !errors.Is(err, events.ErrNotFound) {
		t.Errorf("the PUT answered %v, want an error that wraps ErrNotFound", err)
	}
	client, ok := events.ClientName(id)
	if !ok {
		t.Fatalf("ClientName(%q) is refused", id)
	}
	if subs := h.b.ClientSubscriptions(client); len(subs) != 0 {
		t.Errorf("the broker holds the subscriptions %+v", subs)
	}
	if _, err := h.b.ConsumerState(t.Context(), "app_marks", client+"-s1"); err == nil {
		t.Errorf("the broker holds the consumer %q", client+"-s1")
	}
}

// TestAClientLookupFailureDuringActivationIs503 checks that an OAuth client
// lookup that fails for a reason other than a missing client, during a
// subscription's activation, is not taken for a revoked client: its error
// wraps no ErrNotFound, so the gateway answers 503, and the subscription goes.
func TestAClientLookupFailureDuringActivationIs503(t *testing.T) {
	h := newHarness(t)
	id, token := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/")})
	var first atomic.Bool
	events.SetBeforeActivate(h.svc, func() {
		if first.CompareAndSwap(false, true) {
			h.auth.failClients(1, errors.New("the store did not answer"))
		}
	})
	_, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "marks.bookmark.created")
	if err == nil {
		t.Fatal("a subscription whose client could not be read was admitted")
	}
	for _, sentinel := range []error{events.ErrNotFound, events.ErrInvalid, events.ErrNothing, events.ErrConflict} {
		if errors.Is(err, sentinel) {
			t.Errorf("the PUT answered %v, which wraps %v: want an error the gateway answers as 503", err, sentinel)
		}
	}
	client, ok := events.ClientName(id)
	if !ok {
		t.Fatalf("ClientName(%q) is refused", id)
	}
	if subs := h.b.ClientSubscriptions(client); len(subs) != 0 {
		t.Errorf("the broker holds the subscriptions %+v", subs)
	}
	if _, err := h.b.ConsumerState(t.Context(), "app_marks", client+"-s1"); err == nil {
		t.Errorf("the broker holds the consumer %q", client+"-s1")
	}
}

// TestRestartRefiltersBeforeAdmission checks a stop between a grant change's
// commit and its hook: the consumer that the store brings back keeps the
// wider filter, and lodge refilters it from the grant store before /nats
// admits a client.
func TestRestartRefiltersBeforeAdmission(t *testing.T) {
	h := newHarness(t)
	_, token := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/")})
	sub, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "marks.bookmark.created")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	// The grant narrows, and lodge stops between the store's commit and the
	// hook: no GrantChanged runs, so the consumer keeps the wider filter.
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/f3")})
	h.restart(t)

	stored, err := h.b.ConsumerState(t.Context(), "app_marks", sub.Consumer)
	if err != nil {
		t.Fatalf("the restored consumer: %v", err)
	}
	if want := "marks.bookmark.created.folder"; !slices.Contains(stored.Filters, want) {
		t.Fatalf("the restored consumer's filters are %q, want the wider %q", stored.Filters, want)
	}
	// No client is admitted before Restore.
	select {
	case <-h.svc.Ready():
		t.Fatal("the service is ready before Restore")
	default:
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, ok := h.svc.Tokens(cancelled, token); ok {
		t.Fatal("the tokens check named a client before Restore")
	}

	if err := h.svc.Restore(t.Context()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	select {
	case <-h.svc.Ready():
	default:
		t.Fatal("the service is not ready after Restore")
	}
	// The consumer takes the grant as the store has it now.
	state, err := h.b.ConsumerState(t.Context(), "app_marks", sub.Consumer)
	if err != nil {
		t.Fatalf("the consumer after Restore: %v", err)
	}
	if slices.Contains(state.Filters, "marks.bookmark.created.folder") ||
		!slices.Contains(state.Filters, "marks.bookmark.created.folder.f3") {
		t.Fatalf("the consumer's filters after Restore are %q, want f3's alone", state.Filters)
	}
	// The event of a folder outside the grant never reaches the client, and
	// the 1 inside it does.
	h.publish(t, "marks.bookmark.created.folder.f1")
	h.publish(t, "marks.bookmark.created.folder.f3")
	nc := h.subscribed(t, token)
	msgs := pull(t, nc, "app_marks", sub.Consumer, 2, 500*time.Millisecond)
	if len(msgs) != 1 || !strings.HasSuffix(msgs[0].Subject, ".folder.f3") {
		t.Fatalf("the pull answered %d messages, want the f3 event alone", len(msgs))
	}
}

// TestRestartRevokesAClientItCannotRead checks the fail-closed side: a client
// that OAuth revoked while lodge was stopped, with no hook to say so, is
// revoked at the restore, because its stored filter cannot be trusted.
func TestRestartRevokesAClientItCannotRead(t *testing.T) {
	h := newHarness(t)
	id, token := h.auth.newClient(t, "chani", "events")
	h.auth.grant("chani", marksApp, []oauth.Item{marksRead("/f3")})
	sub, err := h.svc.Subscribe(t.Context(), h.access(t, token), "s1", "marks.bookmark.created")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	// The client goes while lodge is stopped: nothing tells the broker.
	h.auth.revokeClient(id)
	h.restart(t)

	if err := h.svc.Restore(t.Context()); err == nil {
		t.Error("Restore answered no error for a client it revoked")
	}
	select {
	case <-h.svc.Ready():
	default:
		t.Error("the service is not ready after Restore")
	}
	name, ok := events.ClientName(id)
	if !ok {
		t.Fatalf("ClientName(%q) is refused", id)
	}
	if subs := h.b.ClientSubscriptions(name); len(subs) != 0 {
		t.Errorf("the broker keeps the subscriptions %+v", subs)
	}
	wait(t, func() (bool, error) {
		if _, err := h.b.ConsumerState(t.Context(), "app_marks", sub.Consumer); err == nil {
			return false, errNotYet
		}
		return true, nil
	})
	// The old token names no client.
	if _, _, ok := h.svc.Tokens(t.Context(), token); ok {
		t.Error("the tokens check names a client that is gone")
	}
}
