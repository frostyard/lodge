package gateway

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/frostyard/lodge/internal/broker"
)

// pullApps is the broker's app registry for the pull test: the test process
// is the app marks.
type pullApps struct{}

func (pullApps) AppForUID(uid int) (string, bool) { return "marks", uid == os.Getuid() }
func (pullApps) AppEnabled(id string) bool        { return id == "marks" }

// natsFrame is 1 message the server sends a client: MSG or HMSG.
type natsFrame struct {
	op, subject, sid, reply string
	header, payload         string
}

// natsWire is a client's raw protocol over /nats, as a WebSocket client
// sends it: no client library decides what a message's subject means.
type natsWire struct {
	t  *testing.T
	c  net.Conn
	br *bufio.Reader
}

func (w *natsWire) send(line string) {
	w.t.Helper()
	if _, err := io.WriteString(w.c, line); err != nil {
		w.t.Fatalf("send %q: %v", line, err)
	}
}

// next answers the next MSG or HMSG, and answers a PING on the way. It
// fails on -ERR.
func (w *natsWire) next() natsFrame {
	w.t.Helper()
	for {
		line, err := w.br.ReadString('\n')
		if err != nil {
			w.t.Fatalf("read the server's next line: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "PING":
			w.send("PONG\r\n")
		case "-ERR":
			w.t.Fatalf("the server answered %s", line)
		case "MSG", "HMSG":
			return w.frame(f)
		}
	}
}

// frame reads a MSG or HMSG's body: MSG <subject> <sid> [reply] <bytes>,
// HMSG <subject> <sid> [reply] <header bytes> <total bytes>.
func (w *natsWire) frame(f []string) natsFrame {
	w.t.Helper()
	m := natsFrame{op: f[0], subject: f[1], sid: f[2]}
	sizes := 1
	if m.op == "HMSG" {
		sizes = 2
	}
	if len(f) == 3+sizes+1 {
		m.reply = f[3]
	}
	total, err := strconv.Atoi(f[len(f)-1])
	if err != nil {
		w.t.Fatalf("a %s line with a bad size: %q", m.op, f)
	}
	hdr := 0
	if m.op == "HMSG" {
		if hdr, err = strconv.Atoi(f[len(f)-2]); err != nil {
			w.t.Fatalf("an HMSG line with a bad header size: %q", f)
		}
	}
	body := make([]byte, total+2)
	if _, err := io.ReadFull(w.br, body); err != nil {
		w.t.Fatalf("read a %s body: %v", m.op, err)
	}
	m.header, m.payload = string(body[:hdr]), string(body[hdr:total])
	return m
}

// A pull over /nats delivers its message on the inbox subscription it
// named, with the event's own subject and an ack subject as its reply: the
// subject is not the pull's reply inbox. A client that finds its request by
// the subject's last token, as the request mux of nats.js does, drops every
// pulled message and sees only the status replies, whose subject is the
// inbox. So a client reads a pull on a subscription of its own
// (docs/specs/lodge-http.md, The broker). nats.go's request mux hides this,
// because it gives a message of an unknown subject to its single request.
func TestNatsPullKeepsTheStreamSubject(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	tokens := func(_ context.Context, token string) (string, time.Time, bool) {
		return "chani", time.Time{}, token == "tok-chani"
	}
	b := broker.New(broker.Options{StoreDir: dir, Apps: pullApps{}, DevUID: -1, Tokens: tokens,
		Log: slog.New(slog.DiscardHandler)})
	if err := b.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	ln, err := broker.Listen(filepath.Join(dir, "nats.sock"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() { _ = b.ServeApps(t.Context(), ln) }()
	if err := b.SyncApps(t.Context(), []string{"marks"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.bookmark.>"}); err != nil {
		t.Fatal(err)
	}
	h.g.cfg.Broker = b

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	ws := dialNats(t, h)
	nc := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	w := &natsWire{t: t, c: nc, br: bufio.NewReader(nc)}
	w.send(`CONNECT {"auth_token":"tok-chani","echo":false,"headers":true,"no_responders":true,"protocol":1,"verbose":false}` + "\r\n")
	// A request mux's subscription, as nats.js makes it: its inbox, then 1
	// token for each request.
	const mux = "_INBOX.chani.mux."
	w.send("SUB " + mux + "* 1\r\nPING\r\n")

	// 1. No event yet: the pull expires, and its status reply comes on the
	// inbox's own subject.
	const next = "$JS.API.CONSUMER.MSG.NEXT.app_marks.chani-s1"
	// A short expiry, so the empty pull ends soon.
	req := `{"batch":1,"expires":300000000}`
	w.send(fmt.Sprintf("PUB %s %s %d\r\n%s\r\n", next, mux+"r1", len(req), req))
	st := w.next()
	if st.subject != mux+"r1" || !strings.Contains(st.header, "408") {
		t.Errorf("the expired pull answered %s %q with header %q, want a 408 on %q", st.op, st.subject, st.header, mux+"r1")
	}

	// 2. The app publishes 1 event, as frostmarks does, with a message ID.
	app, err := nats.Connect("nats://broker", nats.SetCustomDialer(sockDialer(ln.Addr().String())),
		nats.SkipHostLookup(), nats.UserInfo("marks", ""), nats.NoReconnect(), nats.Timeout(10*time.Second))
	if err != nil {
		t.Fatalf("connect as the app: %v", err)
	}
	t.Cleanup(app.Close)
	js, err := jetstream.New(app)
	if err != nil {
		t.Fatal(err)
	}
	const subject = "marks.bookmark.created.folder.pilot"
	if _, err := js.Publish(ctx, subject, []byte(`{"id":"e1"}`), jetstream.WithMsgID("e1")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// 3. The pull gets it on the mux's subscription, with the stream's
	// subject, not the inbox of the request.
	// A long expiry, so a slow delivery cannot turn into a 408.
	req = `{"batch":1,"expires":5000000000}`
	w.send(fmt.Sprintf("PUB %s %s %d\r\n%s\r\n", next, mux+"r2", len(req), req))
	m := w.next()
	if m.sid != "1" || m.payload != `{"id":"e1"}` {
		t.Fatalf("the pull answered %s sid %s %q, want the event on sid 1", m.op, m.sid, m.payload)
	}
	if m.subject != subject {
		t.Errorf("the pulled message's subject is %q, want the stream's %q", m.subject, subject)
	}
	if strings.HasPrefix(m.subject, mux) {
		t.Errorf("the pulled message's subject %q is under the inbox %q: a subject-keyed mux would find it", m.subject, mux)
	}
	if !strings.HasPrefix(m.reply, "$JS.ACK.app_marks.chani-s1.") {
		t.Fatalf("the pulled message's reply is %q, want the consumer's ack subject", m.reply)
	}

	// 4. The ack on that reply moves the consumer's ack floor.
	w.send(fmt.Sprintf("PUB %s 4\r\n+ACK\r\n", m.reply))
	deadline := time.Now().Add(10 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		state, err := b.ConsumerState(ctx, "app_marks", "chani-s1")
		if err != nil {
			t.Fatal(err)
		}
		if state.AckFloor == state.Delivered && state.AckFloor > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the ack floor is %d after the ack, want %d", state.AckFloor, state.Delivered)
		}
		<-tick.C
	}
	ws.Close(websocket.StatusNormalClosure, "")
}

// sockDialer dials the broker's app socket.
type sockDialer string

func (d sockDialer) Dial(_, _ string) (net.Conn, error) {
	c, err := net.Dial("unix", string(d))
	if err != nil {
		return nil, errors.Join(errors.New("dial the broker's socket"), err)
	}
	return c, nil
}
