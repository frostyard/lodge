package broker

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// A subscription creates nothing while a consumer that its client's revoke
// kept is still there: that ref could delete the consumer this call would
// make. Once the deletion works, the subscription goes on.
func TestSubscribeStopsOnAKeptCleanup(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	// A revoke whose deletion hangs keeps its consumer.
	restore := h.slowDeletes(5 * time.Second)
	if err := h.b.Revoke(t.Context(), "chani"); err == nil {
		t.Fatal("a revoke whose deletion hangs answered no error")
	}
	for _, app := range []string{"marks", "notes"} {
		if err := h.b.Subscribe(t.Context(), "chani", "s9", app, []string{app + ".>"}); err == nil {
			t.Errorf("a subscription to %s went on while a consumer its client's revoke kept stayed", app)
		}
	}
	restore()
	if names := h.names(t, "app_notes"); len(names) != 0 {
		t.Errorf("app_notes holds the consumers %q, want none", names)
	}
	if names := h.names(t, "app_marks"); len(names) != 1 || names[0] != "chani-s1" {
		t.Errorf("app_marks holds the consumers %q, want the kept chani-s1 alone", names)
	}
	// The deletion works again: a subscription clears the kept consumer
	// and makes its own.
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s9", "notes", []string{"notes.>"}))
	if names := h.names(t, "app_notes"); len(names) != 1 || names[0] != "chani-s9" {
		t.Errorf("app_notes holds the consumers %q, want chani-s9", names)
	}
	if names := h.names(t, "app_marks"); len(names) != 0 {
		t.Errorf("app_marks holds the consumers %q, want none", names)
	}
}

// A client's subscribes run 1 at a time: the one-app check and the consumer
// it makes are 1 step, so 2 calls for the same client and subscription leave
// 1 consumer, and only 1 of them goes on.
func TestSubscribeIsSerialized(t *testing.T) {
	h := newHarness(t)
	first := atomic.Bool{}
	second := make(chan struct{})
	var secondOnce sync.Once
	entered := make(chan struct{})
	proceed := make(chan struct{})
	// The first subscribe stops here, once its one-app check has passed. A
	// second one says so and goes on: without the client's lock, it passes
	// the same check and makes a second consumer.
	h.b.afterOneAppCheck = func() {
		if first.CompareAndSwap(false, true) {
			close(entered)
			<-proceed
			return
		}
		secondOnce.Do(func() { close(second) })
	}

	results := make(chan error, 2)
	go func() { results <- h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}) }()
	<-entered
	go func() { results <- h.b.Subscribe(t.Context(), "chani", "s1", "notes", []string{"notes.>"}) }()
	// The client's lock holds the second one before its check; without it,
	// the second reaches the hook. The wait is bounded either way.
	select {
	case <-second:
	case <-time.After(500 * time.Millisecond):
	}
	close(proceed)

	passed := 0
	for range 2 {
		if err := <-results; err == nil {
			passed++
		} else {
			t.Logf("one subscribe answered %v", err)
		}
	}
	if passed != 1 {
		t.Fatalf("%d subscriptions went on, want exactly 1", passed)
	}
	if total := len(h.names(t, "app_marks")) + len(h.names(t, "app_notes")); total != 1 {
		t.Errorf("the 2 streams hold %d consumers, want 1", total)
	}
}

// A revoke never waits for a subscription under way: it returns within its 1
// s budget, closes the client, and the subscription it overtook fails and
// deletes the consumer it made.
func TestRevokeDoesNotWaitForASubscribe(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	closed := make(chan struct{})
	nc, _ := h.clientOpts(t, "chani", "tok-chani", natsClosed(closed))
	noErr(t, nc.Flush())

	// A second subscription stops after its one-app check, and holds its
	// client's lifecycle lock until the test lets it go.
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var enteredOnce, released sync.Once
	release := func() { released.Do(func() { close(proceed) }) }
	h.b.afterOneAppCheck = func() {
		enteredOnce.Do(func() { close(entered) })
		<-proceed
	}
	results := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		results <- h.b.Subscribe(t.Context(), "chani", "s2", "marks", []string{"marks.>"})
	}()
	// Never leave that goroutine, or the test, hanging.
	t.Cleanup(func() {
		release()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	})
	<-entered

	// The revoke comes while the subscription holds its client's lock.
	start := time.Now()
	revoked := make(chan error, 1)
	go func() { revoked <- h.b.Revoke(t.Context(), "chani") }()
	select {
	case <-revoked:
	case <-time.After(2 * time.Second):
		release()
		t.Fatal("the revoke did not return while a subscription was under way")
	}
	if d := time.Since(start); d >= time.Second {
		t.Errorf("the revoke took %s, want under 1 s", d)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		release()
		t.Fatal("the client's connection is still open 1 s after its revoke")
	}
	t.Logf("revoked after %s", time.Since(start))

	// The subscription it overtook fails, and leaves no consumer of its
	// client behind.
	release()
	if err := <-results; err == nil {
		t.Fatal("the subscription that a revoke overtook answered no error")
	}
	h.b.mu.Lock()
	_, kept := h.b.clients["chani"]
	h.b.mu.Unlock()
	if kept {
		t.Error("the subscription put its revoked client back in the index")
	}
	wait(t, func() ([]string, error) {
		names := h.names(t, "app_marks")
		if len(names) > 0 {
			return nil, errNotYet
		}
		return names, nil
	})
}

// A subscription of a client whose revoke is still deleting waits for it: the
// revoke's own deletion would take the consumer the subscription makes, and
// the index would then name a consumer that does not exist.
func TestSubscribeWaitsForARevokeInProgress(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))

	// The revoke stops after it takes the client, and before it deletes.
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var released sync.Once
	release := func() { released.Do(func() { close(proceed) }) }
	h.b.beforeRevokeDelete = func() {
		close(entered)
		<-proceed
	}
	t.Cleanup(release)

	revoked := make(chan error, 1)
	go func() { revoked <- h.b.Revoke(t.Context(), "chani") }()
	<-entered

	// A subscription that starts while the revoke deletes makes nothing.
	err := h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"})
	if err == nil {
		t.Fatal("a subscription went on while its client's revoke was deleting")
	}
	if !strings.Contains(err.Error(), "revoke") {
		t.Errorf("the subscription answered %v, want an error naming the revoke", err)
	}

	release()
	if err := <-revoked; err != nil {
		t.Errorf("the revoke answered %v, want nothing", err)
	}
	h.b.mu.Lock()
	_, kept := h.b.clients["chani"]
	h.b.mu.Unlock()
	if kept {
		t.Error("the index holds chani after its revoke")
	}
	wait(t, func() ([]string, error) {
		names := h.names(t, "app_marks")
		if len(names) > 0 {
			return nil, errNotYet
		}
		return names, nil
	})

	// Once the revoke has ended, a subscription goes on, and the index
	// names the consumer it made.
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	if names := h.names(t, "app_marks"); len(names) != 1 || names[0] != "chani-s1" {
		t.Fatalf("app_marks holds the consumers %q, want chani-s1", names)
	}
	h.b.mu.Lock()
	ref := h.b.clients["chani"].subs["s1"]
	h.b.mu.Unlock()
	if ref == nil || ref.stream != "app_marks" || ref.consumer != "chani-s1" {
		t.Errorf("the index holds %+v, want app_marks' chani-s1", ref)
	}
}

// countingJS counts the consumers a broker writes, and passes the rest on.
type countingJS struct {
	jetstream.JetStream
	mu      sync.Mutex
	creates int
}

func (c *countingJS) CreateOrUpdateConsumer(ctx context.Context, stream string, cfg jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	c.mu.Lock()
	c.creates++
	c.mu.Unlock()
	return c.JetStream.CreateOrUpdateConsumer(ctx, stream, cfg)
}

// countCreates replaces the broker's JetStream with a counter, and answers it
// with what restores the real one.
func (h *harness) countCreates() (*countingJS, func()) {
	h.b.mu.Lock()
	real := h.b.js
	c := &countingJS{JetStream: real}
	h.b.js = c
	h.b.mu.Unlock()
	return c, func() {
		h.b.mu.Lock()
		h.b.js = real
		h.b.mu.Unlock()
	}
}

// filterList answers n filters of the app.
func filterList(app string, n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, app+".f"+strconv.Itoa(i))
	}
	return out
}

// A subscription takes at most MaxFilters filters, its own resync subject
// included: the last one that fits is MaxFilters-1 read filters.
func TestSubscribeFilterLimit(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", filterList("marks", MaxFilters-1)))
	if got := len(h.consumer(t, "app_marks", "chani-s1").Config.FilterSubjects); got != MaxFilters {
		t.Errorf("the consumer has %d filters, want %d", got, MaxFilters)
	}
	// 1 more read filter is past the limit, and makes no consumer.
	err := h.b.Subscribe(t.Context(), "chani", "s2", "marks", filterList("marks", MaxFilters))
	if !errors.Is(err, ErrTooManyFilters) {
		t.Fatalf("a subscription with %d filters answered %v, want ErrTooManyFilters", MaxFilters+1, err)
	}
	if names := h.names(t, "app_marks"); len(names) != 1 || names[0] != "chani-s1" {
		t.Errorf("app_marks holds the consumers %q, want chani-s1 alone", names)
	}
}

// A refilter past the limit fails closed: the consumer keeps its resync
// subject alone, never filters that may be wider than the grant.
func TestRefilterPastTheLimitStopsDelivery(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.a"}))
	err := h.b.Refilter(t.Context(), "chani", "s1", func(context.Context, string, string) ([]string, error) {
		return filterList("marks", MaxFilters), nil
	})
	if !errors.Is(err, ErrTooManyFilters) {
		t.Fatalf("a refilter with %d filters answered %v, want ErrTooManyFilters", MaxFilters+1, err)
	}
	// The old filters may be wider than the new grant: the consumer keeps its
	// resync subject alone.
	want := []string{"lodge.marks.resync.chani-s1"}
	if got := consumerFilters(h.consumer(t, "app_marks", "chani-s1").Config); !sameFilters(got, want) {
		t.Errorf("the consumer's filters are %q, want %q", got, want)
	}
}

// A refilter that changes nothing writes nothing: the minute sweep runs it
// over every subscription.
func TestRefilterWritesNothingWhenNothingChanges(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.a"}))
	counter, restore := h.countCreates()
	defer restore()
	same := func(context.Context, string, string) ([]string, error) {
		return []string{"marks.a"}, nil
	}
	noErr(t, h.b.Refilter(t.Context(), "chani", "s1", same))
	if got := counter.creates; got != 0 {
		t.Errorf("an unchanged refilter wrote the consumer %d times, want 0", got)
	}
	// A changed list is written once.
	noErr(t, h.b.Refilter(t.Context(), "chani", "s1", func(context.Context, string, string) ([]string, error) {
		return []string{"marks.b"}, nil
	}))
	if got := counter.creates; got != 1 {
		t.Errorf("a changed refilter wrote the consumer %d times, want 1", got)
	}
	noErr(t, h.b.Refilter(t.Context(), "chani", "s1", func(context.Context, string, string) ([]string, error) {
		return []string{"marks.b"}, nil
	}))
	if got := counter.creates; got != 1 {
		t.Errorf("a second unchanged refilter wrote the consumer %d times, want 1", got)
	}
	// An empty list leaves the resync subject alone, which the server keeps
	// in the consumer's single filter subject.
	none := func(context.Context, string, string) ([]string, error) { return nil, nil }
	noErr(t, h.b.Refilter(t.Context(), "chani", "s1", none))
	info := h.consumer(t, "app_marks", "chani-s1")
	if want := []string{ResyncSubject("marks", "chani-s1")}; !sameFilters(consumerFilters(info.Config), want) {
		t.Fatalf("the consumer's filters are %q, want %q", consumerFilters(info.Config), want)
	}
	noErr(t, h.b.Refilter(t.Context(), "chani", "s1", none))
	if got := counter.creates; got != 2 {
		t.Errorf("an unchanged empty refilter wrote the consumer %d times, want 2", got)
	}
	// A wider list replaces that single filter: the server refuses a config
	// that names both.
	noErr(t, h.b.Refilter(t.Context(), "chani", "s1", func(context.Context, string, string) ([]string, error) {
		return []string{"marks.a"}, nil
	}))
	info = h.consumer(t, "app_marks", "chani-s1")
	want := []string{"lodge.marks.resync.chani-s1", "marks.a"}
	if got := consumerFilters(info.Config); !sameFilters(got, want) || info.Config.FilterSubject != "" {
		t.Errorf("the consumer's filters are %q (single %q), want %q", got, info.Config.FilterSubject, want)
	}
}

// A refilter's config names its filters once: a consumer that held 1 filter in
// FilterSubject is replaced by the list, which the server refuses beside a
// single filter.
func TestRefilterConfigClearsAFilterSubject(t *testing.T) {
	cfg := refilterConfig(jetstream.ConsumerConfig{Durable: "chani-s1", FilterSubject: "marks.a"},
		[]string{"marks.b", "lodge.marks.resync.chani-s1"}, "chani", "s1", "marks")
	if cfg.FilterSubject != "" {
		t.Errorf("the config keeps the single filter %q", cfg.FilterSubject)
	}
	if want := []string{"marks.b", "lodge.marks.resync.chani-s1"}; !sameFilters(cfg.FilterSubjects, want) {
		t.Errorf("the config's filters are %q, want %q", cfg.FilterSubjects, want)
	}
	if cfg.Durable != "chani-s1" || cfg.Metadata[MetadataClient] != "chani" || cfg.Metadata[MetadataSub] != "s1" ||
		cfg.Metadata[MetadataTopic] != "marks" {
		t.Errorf("the config is %+v", cfg)
	}
	// A consumer's filters are read from either member.
	if got := consumerFilters(jetstream.ConsumerConfig{FilterSubject: "marks.a"}); !sameFilters(got, []string{"marks.a"}) {
		t.Errorf("consumerFilters answers %q, want marks.a", got)
	}
	if got := consumerFilters(jetstream.ConsumerConfig{FilterSubjects: []string{"marks.a", "marks.b"}}); len(got) != 2 {
		t.Errorf("consumerFilters answers %q, want 2 filters", got)
	}
}

// A refilter whose callback fails stops the consumer's delivery before the
// error goes back: the filters it had may be wider than the grant.
func TestRefilterStopsDeliveryWhenTheGrantFails(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.a", "marks.b"}))
	nc, _ := h.client(t, "chani", "tok-chani")
	noErr(t, nc.Flush())
	failure := errors.New("the store did not answer")
	err := h.b.Refilter(t.Context(), "chani", "s1", func(context.Context, string, string) ([]string, error) {
		return nil, failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("a refilter whose callback failed answered %v, want the callback's error", err)
	}
	want := []string{"lodge.marks.resync.chani-s1"}
	if got := consumerFilters(h.consumer(t, "app_marks", "chani-s1").Config); !sameFilters(got, want) {
		t.Errorf("the consumer's filters are %q, want %q", got, want)
	}
	// The client's connection stays open: its permissions did not change.
	if !nc.IsConnected() {
		t.Error("a refilter whose callback failed closed the client's connection")
	}
}

// A refilter that a revoke overtakes leaves no consumer: the delivery it stops
// is not written after the revoke deleted the consumer.
func TestAFailedRefilterAfterARevokeLeavesNoConsumer(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.a", "marks.b"}))

	// The refilter stops in its callback, which holds the client's lifecycle
	// lock, and the revoke comes while it is there: a revoke never waits for
	// a subscription.
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var released sync.Once
	release := func() { released.Do(func() { close(proceed) }) }
	t.Cleanup(release)
	failure := errors.New("the store did not answer")
	done := make(chan error, 1)
	go func() {
		done <- h.b.Refilter(t.Context(), "chani", "s1", func(context.Context, string, string) ([]string, error) {
			close(entered)
			<-proceed
			return nil, failure
		})
	}()
	<-entered
	revoked := make(chan error, 1)
	go func() { revoked <- h.b.Revoke(t.Context(), "chani") }()
	select {
	case err := <-revoked:
		noErr(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("the revoke did not return while a refilter was under way")
	}
	release()
	if err := <-done; !errors.Is(err, errRefilterRevoked) {
		t.Fatalf("the refilter answered %v, want the revoke's error", err)
	}
	// The write that stopped the delivery is nobody's: the revoke took it.
	wait(t, func() ([]string, error) {
		names := h.names(t, "app_marks")
		if len(names) > 0 {
			return nil, errNotYet
		}
		return names, nil
	})
}

// Activate's cleanup runs under the lock that its refilter holds: a request
// that deletes the subscription and puts another 1 in its place waits for it,
// and its replacement survives.
func TestActivateCleansUpUnderOneLock(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.a"}))

	entered := make(chan struct{})
	proceed := make(chan struct{})
	var released sync.Once
	release := func() { released.Do(func() { close(proceed) }) }
	t.Cleanup(release)
	failure := errors.New("the store did not answer")
	done := make(chan error, 1)
	go func() {
		done <- h.b.Activate(t.Context(), "chani", "s1", func(context.Context, string, string) ([]string, error) {
			close(entered)
			<-proceed
			return nil, failure
		})
	}()
	<-entered

	// Another request replaces the subscription: its delete and its new
	// subscription both wait for the lock that the activation holds.
	replaced := make(chan error, 1)
	deleted := make(chan error, 1)
	go func() {
		err := h.b.Unsubscribe(t.Context(), "chani", "s1")
		deleted <- err
		if err != nil && !errors.Is(err, ErrNotFound) {
			replaced <- err
			return
		}
		_, serr := h.b.SubscribeWith(t.Context(), Spec{Client: "chani", Sub: "s1", App: "notes", Topic: "notes"},
			func(context.Context) ([]string, error) { return []string{"notes.>"}, nil })
		replaced <- serr
	}()
	release()

	if err := <-done; !errors.Is(err, failure) {
		t.Fatalf("Activate answered %v, want the callback's error", err)
	}
	noErr(t, <-replaced)
	// The activation's cleanup went first: the other request's delete found
	// the subscription gone, which is what 1 lock means here.
	if err := <-deleted; !errors.Is(err, ErrNotFound) {
		t.Errorf("another request's delete answered %v, want the subscription gone by the cleanup", err)
	}
	// The replacement is s1 on notes, and the activation's own subscription
	// is gone.
	subs := h.b.ClientSubscriptions("chani")
	if len(subs) != 1 || subs[0].Sub != "s1" || subs[0].App != "notes" {
		t.Fatalf("the client's subscriptions are %+v, want s1 on notes", subs)
	}
	if names := h.names(t, "app_notes"); len(names) != 1 || names[0] != "chani-s1" {
		t.Errorf("app_notes holds the consumers %q, want chani-s1", names)
	}
	if names := h.names(t, "app_marks"); len(names) != 0 {
		t.Errorf("app_marks holds the consumers %q, want none", names)
	}
}

// An activation whose budget ran out still removes its subscription: the
// cleanup has a budget of its own, so the client is not revoked for it.
func TestActivateCleansUpAfterItsBudget(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.a"}))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := h.b.Activate(ctx, "chani", "s1", func(context.Context, string, string) ([]string, error) {
		return []string{"marks.a"}, nil
	})
	if err == nil {
		t.Fatal("an activation with a cancelled context answered no error")
	}
	if errors.Is(err, ErrCleanup) {
		t.Errorf("the cleanup failed: %v", err)
	}
	if subs := h.b.ClientSubscriptions("chani"); len(subs) != 0 {
		t.Errorf("the client keeps the subscriptions %+v", subs)
	}
}
