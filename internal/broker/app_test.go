package broker

import (
	"net"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// An app publishes into its own subject, and gets a JetStream PubAck. The
// subject of another app is a permission violation, and its stream stays
// empty (ADR-0022; docs/specs/app-contract.md, Events).
func TestAppPublishesItsOwnSubject(t *testing.T) {
	h := newHarness(t)
	nc, e := h.app(t, "marks")
	js, err := jetstream.New(nc)
	noErr(t, err)

	ack := published(t, js, "marks.x", "e1")
	if ack.Stream != "app_marks" {
		t.Errorf("the PubAck names stream %q, want app_marks", ack.Stream)
	}
	if got := h.msgs(t, "app_marks"); got != 1 {
		t.Errorf("app_marks holds %d messages, want 1", got)
	}

	// Another app's subject is refused, and nothing lands in its stream.
	nc.Publish("notes.x", []byte("y"))
	noErr(t, nc.Flush())
	e.wait(t, "Permissions Violation for Publish")
	if got := h.msgs(t, "app_notes"); got != 0 {
		t.Errorf("app_notes holds %d messages, want 0", got)
	}
	if got := h.msgs(t, "app_marks"); got != 1 {
		t.Errorf("app_marks holds %d messages after a refused publish, want 1", got)
	}
}

// A connection whose peer uid is another app's user is that app: it cannot
// publish this app's subjects (ADR-0022).
func TestAppOfAnotherUserIsRefusedItsSubjects(t *testing.T) {
	h := newHarness(t)
	published(t, h.b.js, "marks.x", "e1")
	before := h.msgs(t, "app_marks")

	h.b.setPeerUID(func(net.Conn) int { return notesUID })
	nc, e := h.app(t, "marks")
	nc.Publish("marks.forged", []byte("y"))
	noErr(t, nc.Flush())
	e.wait(t, "Permissions Violation for Publish")
	if got := h.msgs(t, "app_marks"); got != before {
		t.Errorf("app_marks holds %d messages, want %d", got, before)
	}
}

// A peer uid that is no enabled app's is closed before lodge reads from it.
func TestAppSocketClosesAnUnknownUID(t *testing.T) {
	h := newHarness(t)
	h.b.setPeerUID(func(net.Conn) int { return 4321 })
	if _, err := nats.Connect("nats://broker", nats.SetCustomDialer(unixDialer(h.sock)), nats.SkipHostLookup(),
		nats.UserInfo("marks", ""), nats.NoReconnect(), nats.Timeout(10*time.Second)); err == nil {
		t.Fatal("a connection from a uid that is no app's was admitted")
	}
}

// The guard for ADR-0022's reply-subject finding: a reply subject names an
// account, so a pull request whose reply names another app's subject
// delivers inside the client's own account. The notes stream stays empty,
// another client receives nothing, and the client's own pull still gets the
// message the forged ones did not take.
func TestRepliesStayInsideTheirAccount(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	noErr(t, h.b.Subscribe(t.Context(), "other", "s1", "marks", []string{"marks.>"}))

	// The app publishes one message whose reply names the notes app's
	// subject.
	app, _ := h.app(t, "marks")
	app.PublishRequest("marks.x", "notes.forged", []byte("e1"))
	noErr(t, app.Flush())
	wait(t, func() (uint64, error) {
		if n := h.msgs(t, "app_marks"); n == 1 {
			return n, nil
		}
		return 0, errNotYet
	})

	chani, _ := h.client(t, "chani", "tok-chani")
	other, _ := h.client(t, "other", "tok-other")
	inbox, err := other.SubscribeSync("_INBOX.other.x")
	noErr(t, err)

	// A pull request's reply is a subject in the sender's account: neither
	// the notes app's subject nor another client's inbox receives
	// anything.
	for _, reply := range []string{"notes.forged", "_INBOX.other.x"} {
		noErr(t, chani.PublishRequest(nextPrefix+"app_marks.chani-s1", reply,
			[]byte(`{"batch":1,"expires":1000000000}`)))
		noErr(t, chani.Flush())
	}

	// The client's own pull still gets the message, so the forged replies
	// took nothing, and its delivery count is the one that advanced.
	msgs := pull(t, chani, "app_marks", "chani-s1", 1)
	if got := string(msgs[0].Data); got != "e1" {
		t.Errorf("the client's own pull answered %q, want e1", got)
	}
	wait(t, func() (*jetstream.ConsumerInfo, error) {
		info := h.consumer(t, "app_marks", "chani-s1")
		if info.Delivered.Stream == 0 {
			return nil, errNotYet
		}
		return info, nil
	})
	if got := h.msgs(t, "app_notes"); got != 0 {
		t.Errorf("app_notes holds %d messages, want 0", got)
	}
	if m, err := inbox.NextMsg(250 * time.Millisecond); err == nil {
		t.Errorf("client other received %q, want nothing", m.Data)
	}
}

// errNotYet is a condition that does not hold yet.
var errNotYet = errNotYetError{}

type errNotYetError struct{}

func (errNotYetError) Error() string { return "not yet" }
