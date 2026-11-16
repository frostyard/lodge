package broker

import (
	"fmt"
	"log/slog"
	"net"
	"strings"
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
