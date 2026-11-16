package broker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// pull asks one consumer for a batch, and answers its messages.
func pull(t *testing.T, nc *nats.Conn, stream, consumer string, batch int) []*nats.Msg {
	t.Helper()
	sub, err := nc.SubscribeSync(nc.NewRespInbox())
	noErr(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	noErr(t, sub.AutoUnsubscribe(1+batch))
	req := fmt.Sprintf(`{"batch":%d,"expires":%d}`, batch, int64(2*time.Second))
	noErr(t, nc.PublishRequest(nextPrefix+stream+"."+consumer, sub.Subject, []byte(req)))
	noErr(t, nc.Flush())
	var out []*nats.Msg
	deadline := time.Now().Add(10 * time.Second)
	for len(out) < batch {
		m, err := sub.NextMsg(time.Until(deadline))
		if err != nil {
			t.Fatalf("pull %s/%s: %d of %d messages: %v", stream, consumer, len(out), batch, err)
		}
		out = append(out, m)
	}
	return out
}

// A client pulls and acks from its own consumer, and only from it: another
// client's consumer, the JetStream API and a publish are all refused
// (ADR-0022).
func TestClientPullsFromItsOwnConsumer(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	noErr(t, h.b.Subscribe(t.Context(), "other", "s1", "marks", []string{"marks.>"}))
	published(t, h.b.js, "marks.a", "e1")
	published(t, h.b.js, "marks.b", "e2")

	chani, e := h.client(t, "chani", "tok-chani")
	msgs := pull(t, chani, "app_marks", "chani-s1", 2)
	if got := []string{string(msgs[0].Data), string(msgs[1].Data)}; got[0] != "e1" || got[1] != "e2" {
		t.Errorf("the pull answered %q, want e1 and e2", got)
	}
	for _, m := range msgs {
		if m.Reply == "" {
			t.Fatalf("message %q carries no ack subject", m.Data)
		}
		noErr(t, m.Ack())
	}
	wait(t, func() (*uint64, error) {
		info := h.consumer(t, "app_marks", "chani-s1")
		if info.AckFloor.Stream != 2 {
			return nil, errNotYet
		}
		return &info.AckFloor.Stream, nil
	})

	// Another client's consumer, the JetStream API, a consumer's own
	// info, and a publish are refused.
	for _, subject := range []string{
		nextPrefix + "app_marks.other-s1",
		"$JS.API.STREAM.LIST",
		"$JS.API.CONSUMER.INFO.app_marks.chani-s1",
		"marks.x",
	} {
		noErr(t, chani.Publish(subject, []byte("x")))
		noErr(t, chani.Flush())
		e.wait(t, "Permissions Violation for Publish")
	}
}

// A bad token is refused, and the connection never opens.
func TestBadTokenIsRefused(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	inside, outside := net.Pipe()
	t.Cleanup(func() { outside.Close() })
	t.Cleanup(func() { inside.Close() })
	go h.b.ServeClient(t.Context(), outside)
	nc, err := nats.Connect("nats://broker", nats.SetCustomDialer(&pipeDialer{c: inside}), nats.SkipHostLookup(),
		nats.Token("wrong"), nats.CustomInboxPrefix("_INBOX.other"), nats.NoReconnect(), nats.Timeout(10*time.Second))
	if err == nil {
		nc.Close()
		t.Fatal("a connection with a bad token was admitted")
	}
	if !strings.Contains(err.Error(), "Authorization Violation") {
		t.Errorf("the refusal was %v, want an authorization violation", err)
	}
}

// Revoking a client deletes its consumers and closes its connection within
// 1 s.
func TestRevokeWithinASecond(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	closed := make(chan struct{})
	nc, _ := h.clientOpts(t, "chani", "tok-chani", nats.ClosedHandler(func(*nats.Conn) { close(closed) }))
	noErr(t, nc.Flush())

	start := time.Now()
	noErr(t, h.b.Revoke(t.Context(), "chani"))
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatalf("the client's connection is still open 1 s after its revoke")
	}
	t.Logf("closed after %s", time.Since(start))
	if names := h.names(t, "app_marks"); len(names) != 0 {
		t.Errorf("the stream holds the consumers %q after a revoke", names)
	}
}

// A restart keeps the stream and the consumer's position: 3 messages in,
// 2 acknowledged, and the third is the next the client pulls.
func TestRestartKeepsPositions(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	nc, _ := h.client(t, "chani", "tok-chani")
	for _, m := range []struct{ subject, data string }{{"marks.a", "e1"}, {"marks.b", "e2"}, {"marks.c", "e3"}} {
		published(t, h.b.js, m.subject, m.data)
	}
	msgs := pull(t, nc, "app_marks", "chani-s1", 2)
	for _, m := range msgs {
		noErr(t, m.Ack())
	}
	nc.Close()
	wait(t, func() (*uint64, error) {
		info := h.consumer(t, "app_marks", "chani-s1")
		if info.AckFloor.Stream != 2 {
			return nil, errNotYet
		}
		return &info.AckFloor.Stream, nil
	})

	// A new broker over the same store.
	dir := h.dir
	h.b.Close()
	b := New(Options{StoreDir: dir, Apps: h.apps, DevUID: -1, Tokens: h.token, Log: slog.New(slog.DiscardHandler)})
	noErr(t, b.Start(t.Context()))
	t.Cleanup(b.Close)
	h.b = b

	info := h.consumer(t, "app_marks", "chani-s1")
	if got := info.AckFloor.Stream; got != 2 {
		t.Errorf("the consumer's ack floor is %d after a restart, want 2", got)
	}
	st, err := b.js.Stream(t.Context(), "app_marks")
	noErr(t, err)
	si, err := st.Info(t.Context())
	noErr(t, err)
	if si.State.Msgs != 3 {
		t.Errorf("the stream holds %d messages after a restart, want 3", si.State.Msgs)
	}
	chani, _ := h.client(t, "chani", "tok-chani")
	msgs = pull(t, chani, "app_marks", "chani-s1", 1)
	if got := string(msgs[0].Data); got != "e3" {
		t.Errorf("the pull after a restart answered %q, want e3", got)
	}
}

// TestRestartRestoresIndex fails as a guard for the metadata an index entry
// needs: without the consumer's metadata the client has no permissions
// after a restart.
func TestRestartRestoresIndex(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	dir := h.dir
	h.b.Close()
	b := New(Options{StoreDir: dir, Apps: h.apps, DevUID: -1, Tokens: h.token,
		Log: slog.New(slog.DiscardHandler)})
	noErr(t, b.Start(t.Context()))
	t.Cleanup(b.Close)
	b.mu.Lock()
	ref := b.clients["chani"].subs["s1"]
	b.mu.Unlock()
	if ref == nil || ref.stream != "app_marks" || ref.consumer != "chani-s1" {
		t.Fatalf("the index holds %+v after a restart, want the consumer chani-s1 of app_marks", ref)
	}
	if !b.imports["client_chani"][nextPrefix+"app_marks.chani-s1"] {
		t.Errorf("the restored account imports no pull request: %v", b.imports["client_chani"])
	}
}

// A client connection whose token is revoked closes when the broker checks
// its clients, before the store's own sweep.
func TestCheckClientsClosesARevokedToken(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	closed := make(chan struct{})
	nc, _ := h.clientOpts(t, "chani", "tok-chani", natsClosed(closed))
	noErr(t, nc.Flush())

	h.revokeToken("tok-chani")
	h.b.CheckClients(t.Context())
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the client's connection is still open after its token was revoked")
	}
}

// A token that names another client is refused at the next check.
func TestCheckClientsClosesARenamedToken(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	closed := make(chan struct{})
	nc, _ := h.clientOpts(t, "chani", "tok-chani", natsClosed(closed))
	noErr(t, nc.Flush())

	h.issue("tok-chani", "other")
	h.b.CheckClients(t.Context())
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the client's connection is still open after its token named another client")
	}
}

// A subscription's topic is kept, and its consumer carries it.
func TestSubscribeKeepsItsTopic(t *testing.T) {
	h := newHarness(t)
	_, err := h.b.SubscribeWith(t.Context(), Spec{Client: "chani", Sub: "s1", App: "marks",
		Topic: "marks.bookmark.created"}, func(context.Context) ([]string, error) {
		return []string{"marks.bookmark.created.folder.f3"}, nil
	})
	noErr(t, err)
	subs := h.b.ClientSubscriptions("chani")
	if len(subs) != 1 || subs[0].Topic != "marks.bookmark.created" || subs[0].App != "marks" ||
		subs[0].Stream != "app_marks" || subs[0].Consumer != "chani-s1" {
		t.Errorf("the subscriptions are %+v", subs)
	}
	if info := h.consumer(t, "app_marks", "chani-s1"); info.Config.Metadata[MetadataTopic] != "marks.bookmark.created" {
		t.Errorf("the consumer's metadata is %q", info.Config.Metadata)
	}
	// A topic that is not the app's is refused before anything is made.
	_, err = h.b.SubscribeWith(t.Context(), Spec{Client: "chani", Sub: "s2", App: "marks", Topic: "notes"},
		func(context.Context) ([]string, error) { return []string{"marks.>"}, nil })
	if err == nil || !strings.Contains(err.Error(), "the app's id") {
		t.Errorf("a topic of another app answered %v", err)
	}
	// A callback that answers nothing makes nothing.
	if _, err := h.b.SubscribeWith(t.Context(), Spec{Client: "chani", Sub: "s3", App: "marks", Topic: "marks"},
		func(context.Context) ([]string, error) { return nil, nil }); err == nil {
		t.Error("a subscription whose callback answered nothing was admitted")
	}
	if names := h.names(t, "app_marks"); len(names) != 1 {
		t.Errorf("app_marks holds the consumers %q, want chani-s1 alone", names)
	}
}

// Refilter replaces a consumer's filters without closing the client's
// connection, and an empty answer leaves its resync subject alone.
func TestRefilter(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	nc, _ := h.client(t, "chani", "tok-chani")
	noErr(t, nc.Flush())

	noErr(t, h.b.Refilter(t.Context(), "chani", "s1", func(context.Context, string, string) ([]string, error) {
		return []string{"marks.a"}, nil
	}))
	if want := []string{"lodge.marks.resync.chani-s1", "marks.a"}; !sameFilters(consumerFilters(h.consumer(t, "app_marks", "chani-s1").Config), want) {
		t.Errorf("the consumer's filters are %q, want %q",
			h.consumer(t, "app_marks", "chani-s1").Config.FilterSubjects, want)
	}
	if !nc.IsConnected() {
		t.Error("the refilter closed the client's connection")
	}
	// An empty answer keeps the resync subject alone.
	noErr(t, h.b.Refilter(t.Context(), "chani", "s1", func(context.Context, string, string) ([]string, error) {
		return nil, nil
	}))
	if want := []string{"lodge.marks.resync.chani-s1"}; !sameFilters(consumerFilters(h.consumer(t, "app_marks", "chani-s1").Config), want) {
		t.Errorf("the consumer's filters are %q, want %q",
			h.consumer(t, "app_marks", "chani-s1").Config.FilterSubjects, want)
	}
	// A subscription that is not there is an error.
	if err := h.b.Refilter(t.Context(), "chani", "gone", func(context.Context, string, string) ([]string, error) {
		return nil, nil
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a refilter of a subscription that is not there answered %v", err)
	}
}

// A client connection closes when its token expires, and a token that has
// expired is refused at the door.
func TestClientClosesWhenItsTokenExpires(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	h.expireIn(200 * time.Millisecond)
	closed := make(chan struct{})
	nc, _ := h.clientOpts(t, "chani", "tok-chani", natsClosed(closed))
	noErr(t, nc.Flush())
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the client's connection is still open after its token expired")
	}

	// A token that has expired is refused before its connection opens.
	h.expireIn(-time.Second)
	inside, outside := net.Pipe()
	t.Cleanup(func() { outside.Close() })
	t.Cleanup(func() { inside.Close() })
	go h.b.ServeClient(t.Context(), outside)
	_, err := nats.Connect("nats://broker", nats.SetCustomDialer(&pipeDialer{c: inside}), nats.SkipHostLookup(),
		nats.Token("tok-chani"), nats.CustomInboxPrefix("_INBOX.chani"), nats.NoReconnect(), nats.Timeout(10*time.Second))
	if err == nil || !strings.Contains(err.Error(), "Authorization Violation") {
		t.Errorf("a connection with an expired token answered %v, want an authorization violation", err)
	}
}

// A resync event goes out for a consumer that retention overtook, and only
// once while it is pending.
func TestResyncOnce(t *testing.T) {
	dir := t.TempDir()
	apps := &fakeApps{byUID: map[int]string{os.Getuid(): "marks"}, enabled: map[string]bool{"marks": true}}
	b := New(Options{StoreDir: dir, Apps: apps, DevUID: -1, Log: slog.New(slog.DiscardHandler),
		StreamMaxAge: 200 * time.Millisecond, StreamDuplicates: 100 * time.Millisecond})
	noErr(t, b.Start(t.Context()))
	t.Cleanup(b.Close)
	noErr(t, b.SyncApps(t.Context(), []string{"marks"}))
	noErr(t, b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	for range 3 {
		published(t, b.js, "marks.x", "e")
	}
	// The 3 messages go with the stream's max age, and the consumer read
	// none of them.
	wait(t, func() (uint64, error) {
		st, err := b.js.Stream(t.Context(), "app_marks")
		if err != nil {
			return 0, err
		}
		info, err := st.Info(t.Context())
		if err != nil {
			return 0, err
		}
		if info.State.FirstSeq <= 3 {
			return 0, errNotYet
		}
		return info.State.FirstSeq, nil
	})
	msgs := func() uint64 {
		t.Helper()
		st, err := b.js.Stream(t.Context(), "app_marks")
		noErr(t, err)
		info, err := st.Info(t.Context())
		noErr(t, err)
		return info.State.Msgs
	}
	before := msgs()
	noErr(t, b.Resync(t.Context(), "chani"))
	state, err := b.ConsumerState(t.Context(), "app_marks", "chani-s1")
	noErr(t, err)
	if state.Delivered != 0 {
		t.Fatalf("the consumer's delivered position is %d, want 0", state.Delivered)
	}
	if after := msgs(); after != before+1 {
		t.Errorf("the resync published %d messages, want 1", after-before)
	}
	// A second resync publishes nothing: one is still pending.
	noErr(t, b.Resync(t.Context(), "chani"))
	if after := msgs(); after != before+1 {
		t.Errorf("a second resync published %d messages, want none", after-before-1)
	}
	// A client with no subscription has nothing to resync.
	noErr(t, b.Resync(t.Context(), "other"))
}

// Concurrent resync checks of 1 consumer publish exactly 1 event: the check
// and its publish are 1 step.
func TestResyncPublishesOneEventForConcurrentChecks(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.StreamMaxAge = 200 * time.Millisecond
		o.StreamDuplicates = 100 * time.Millisecond
	})
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	for range 3 {
		published(t, h.b.js, "marks.x", "e")
	}
	// The 3 messages go with the stream's max age, and the consumer read
	// none of them.
	wait(t, func() (uint64, error) {
		st, err := h.b.js.Stream(t.Context(), "app_marks")
		if err != nil {
			return 0, err
		}
		info, err := st.Info(t.Context())
		if err != nil {
			return 0, err
		}
		if info.State.FirstSeq <= 3 {
			return 0, errNotYet
		}
		return info.State.FirstSeq, nil
	})

	// 16 checks of the same client start together.
	const checks = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, checks)
	for range checks {
		wg.Go(func() {
			<-start
			errs <- h.b.Resync(t.Context(), "chani")
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Resync: %v", err)
		}
	}

	// The stream holds the resync event once, on the consumer's own subject.
	st, err := h.b.js.Stream(t.Context(), "app_marks")
	noErr(t, err)
	info, err := st.Info(t.Context())
	noErr(t, err)
	if info.State.Msgs != 1 {
		t.Errorf("the stream holds %d messages, want the 1 resync event", info.State.Msgs)
	}
	last, err := st.GetLastMsgForSubject(t.Context(), ResyncSubject("marks", "chani-s1"))
	noErr(t, err)
	if last.Sequence == 0 || last.Sequence != info.State.LastSeq {
		t.Errorf("the stream's last message is %d, and the resync event is at %d", info.State.LastSeq, last.Sequence)
	}
}
