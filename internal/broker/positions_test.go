package broker

import (
	"context"
	"errors"
	"testing"
)

// gapPassed makes chani's subscription s1, drops 3 events that it never read,
// and pulls the 1 after them on a connection of lodge's own: the gap is behind
// the consumer's position, and no resync check has seen it.
func gapPassed(t *testing.T, h *harness) {
	t.Helper()
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	for range 3 {
		published(t, h.b.js, "marks.x", "e")
	}
	h.purge(t, "app_marks", 4)
	published(t, h.b.js, "marks.x", "e4")
	msgs := pull(t, h.own(t), "app_marks", "chani-s1", 1)
	if got := string(msgs[0].Data); got != "e4" {
		t.Fatalf("the pull answered %q, want e4", got)
	}
	noErr(t, msgs[0].Ack())
	delivered := wait(t, func() (uint64, error) {
		if info := h.consumer(t, "app_marks", "chani-s1"); info.Delivered.Stream != 4 {
			return 0, errNotYet
		}
		return 4, nil
	})
	first := wait(t, func() (uint64, error) {
		state, err := h.b.StreamState(t.Context(), "marks")
		if err != nil {
			return 0, err
		}
		if state.FirstSeq != 4 {
			return 0, errNotYet
		}
		return state.FirstSeq, nil
	})
	if delivered+1 < first {
		t.Fatalf("the consumer is still before the gap: delivered %d, first %d", delivered, first)
	}
}

// TestAResyncWhosePublishFailsKeepsTheRecord checks that the record moves only
// once the resync event is stored: a publish that fails leaves it where it was,
// so the next check sends the event again.
func TestAResyncWhosePublishFailsKeepsTheRecord(t *testing.T) {
	h := newHarness(t)
	gapPassed(t, h)
	before := h.msgs(t, "app_marks")
	fail := errors.New("the publish did not answer")
	h.b.beforeResyncPublish = func() error { return fail }
	if err := h.b.ResyncAll(t.Context()); !errors.Is(err, fail) {
		t.Fatalf("ResyncAll answered %v, want the publish's error", err)
	}
	if after := h.msgs(t, "app_marks"); after != before {
		t.Fatalf("a publish that failed stored %d messages, want none", after-before)
	}
	// The record is where it was, so the next check publishes the event.
	h.b.beforeResyncPublish = nil
	noErr(t, h.b.ResyncAll(t.Context()))
	if after := h.msgs(t, "app_marks"); after != before+1 {
		t.Errorf("the check after a failed publish stored %d messages, want the 1 resync event", after-before)
	}
}

// TestARePutKeepsThePositionRecord checks a subscription that is put again
// before any check: an update of a consumer keeps the position lodge has for
// it, and only a lower 1 replaces it.
func TestARePutKeepsThePositionRecord(t *testing.T) {
	h := newHarness(t)
	gapPassed(t, h)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	before := h.msgs(t, "app_marks")
	noErr(t, h.b.ResyncAll(t.Context()))
	if after := h.msgs(t, "app_marks"); after != before+1 {
		t.Errorf("the check after a re-put stored %d messages, want the 1 resync event", after-before)
	}
}

// TestThePositionRecordSurvivesARestart checks that the record outlives the
// broker: a pull that passed the gap before the stop is still seen after it.
func TestThePositionRecordSurvivesARestart(t *testing.T) {
	h := newHarness(t)
	gapPassed(t, h)
	h.restart(t)
	before := h.msgs(t, "app_marks")
	noErr(t, h.b.ResyncAll(t.Context()))
	if after := h.msgs(t, "app_marks"); after != before+1 {
		t.Errorf("the check after a restart stored %d messages, want the 1 resync event", after-before)
	}
}

// TestAConsumerWithNoRecordGetsTheResync checks a consumer that the bucket has
// no record of: it is taken as being at 0, so the check after the start sends
// the resync event when the stream has dropped anything.
func TestAConsumerWithNoRecordGetsTheResync(t *testing.T) {
	h := newHarness(t)
	gapPassed(t, h)
	ref := consumerRef{stream: "app_marks", consumer: "chani-s1"}
	noErr(t, h.b.kv.Delete(t.Context(), positionKey(ref)))
	h.restart(t)
	before := h.msgs(t, "app_marks")
	noErr(t, h.b.ResyncAll(t.Context()))
	if after := h.msgs(t, "app_marks"); after != before+1 {
		t.Errorf("the check of a consumer with no record stored %d messages, want the 1 resync event", after-before)
	}
}

// TestThePositionBucketIsOutOfReach checks that the bucket of the positions is
// lodge's own stream: no app's, and no client's or app's to read.
func TestThePositionBucketIsOutOfReach(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	// The bucket has no app_ name, so no app holds it.
	if _, err := h.b.js.Stream(t.Context(), "app_"+positionsBucket); err == nil {
		t.Errorf("%s is an app's stream", positionsBucket)
	}
	// A client reaches its own consumer's pull and ack subjects alone.
	nc, e := h.client(t, "chani", "tok-chani")
	for _, subject := range []string{
		"$JS.API.STREAM.INFO.KV_" + positionsBucket,
		"$KV." + positionsBucket + ".app_marks.chani-s1",
	} {
		noErr(t, nc.Publish(subject, []byte("x")))
		noErr(t, nc.Flush())
		e.wait(t, "Permissions Violation for Publish")
	}
	// And an app reaches its own subject alone.
	appConn, appErrs := h.app(t, "marks")
	noErr(t, appConn.Publish("$KV."+positionsBucket+".app_marks.chani-s1", []byte("x")))
	noErr(t, appConn.Flush())
	appErrs.wait(t, "Permissions Violation for Publish")
}

// TestTheBirthPositionIsRecorded checks the record of a consumer at its birth:
// the position it is made at is the stream's last message, which its create
// answers, with no lookup of its own.
func TestTheBirthPositionIsRecorded(t *testing.T) {
	h := newHarness(t)
	published(t, h.b.js, "marks.x", "e1")
	published(t, h.b.js, "marks.x", "e2")
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	ref := consumerRef{stream: "app_marks", consumer: "chani-s1"}
	seq, ok := h.b.storedPosition(t.Context(), ref)
	if !ok || seq != 2 {
		t.Errorf("the bucket holds the position %d (%v) of a consumer made at 2", seq, ok)
	}
	if got := h.b.position(ref); got != 2 {
		t.Errorf("the record is %d, want 2", got)
	}
}

// TestRevokeDoesNotWaitForAResyncCheck checks that a revoke deletes a
// consumer's record without the resync check's lock: a check that is under way
// never holds a revoke up.
func TestRevokeDoesNotWaitForAResyncCheck(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	entered, release := make(chan struct{}), make(chan struct{})
	h.b.duringResyncCheck = func() {
		close(entered)
		<-release
	}
	resynced := make(chan error, 1)
	go func() { resynced <- h.b.ResyncAll(t.Context()) }()
	<-entered

	// The revoke has its own 1 s budget, and the check holds the resync lock
	// for the whole of it.
	ctx, cancel := context.WithTimeout(t.Context(), revokeTimeout)
	defer cancel()
	revoked := make(chan error, 1)
	go func() { revoked <- h.b.Revoke(ctx, "chani") }()
	select {
	case err := <-revoked:
		if err != nil {
			t.Errorf("Revoke: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("Revoke waited for the resync check")
	}
	close(release)
	if err := <-resynced; err != nil {
		t.Errorf("Resync after a revoke: %v", err)
	}
	if names := h.names(t, "app_marks"); len(names) != 0 {
		t.Errorf("the stream holds the consumers %q after a revoke", names)
	}
}

// TestAConsumerThatKeptUpGetsNoResyncAfterARestart checks that a check which
// finds no gap moves the bucket's record too: a bucket that kept the birth
// position would send a consumer that read everything a resync event after
// each start, once its stream has dropped that position.
func TestAConsumerThatKeptUpGetsNoResyncAfterARestart(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	for range 3 {
		published(t, h.b.js, "marks.x", "e")
	}
	for _, m := range pull(t, h.own(t), "app_marks", "chani-s1", 3) {
		noErr(t, m.Ack())
	}
	wait(t, func() (uint64, error) {
		if info := h.consumer(t, "app_marks", "chani-s1"); info.Delivered.Stream != 3 {
			return 0, errNotYet
		}
		return 3, nil
	})
	// A check while the stream is whole: no gap, and the record moves to 3.
	noErr(t, h.b.ResyncAll(t.Context()))
	ref := consumerRef{stream: "app_marks", consumer: "chani-s1"}
	if seq, ok := h.b.storedPosition(t.Context(), ref); !ok || seq != 3 {
		t.Fatalf("the bucket holds the position %d (%v) after a check at 3", seq, ok)
	}
	// Retention drops what it read, and lodge starts again.
	h.purge(t, "app_marks", 4)
	h.restart(t)
	before := h.msgs(t, "app_marks")
	noErr(t, h.b.ResyncAll(t.Context()))
	if after := h.msgs(t, "app_marks"); after != before {
		t.Errorf("a consumer that kept up got %d messages after a start, want no resync event", after-before)
	}
}
