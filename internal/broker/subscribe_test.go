package broker

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
