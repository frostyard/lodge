package wsline

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// conns returns a Conn on each side of a WebSocket: the server's, which
// the test drives as the app, and the raw client, which plays the browser.
func conns(t *testing.T) (*Conn, *websocket.Conn) {
	t.Helper()
	got := make(chan *Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		c := New(context.WithoutCancel(r.Context()), ws)
		got <- c
		<-c.ctx.Done() // hold the handler until the Conn ends
	}))
	t.Cleanup(srv.Close)
	ws, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ws.SetReadLimit(4 * MaxMessage)
	c := <-got
	t.Cleanup(func() {
		_ = ws.CloseNow()
		_ = c.Close()
	})
	return c, ws
}

func TestFramesBecomeLines(t *testing.T) {
	c, browser := conns(t)
	for _, frame := range []string{`{"id":1}`, "{\"id\":2}\n", "", `{"id":3}`} {
		if err := browser.Write(t.Context(), websocket.MessageText, []byte(frame)); err != nil {
			t.Fatal(err)
		}
	}
	r := bufio.NewReader(c)
	for _, want := range []string{"{\"id\":1}\n", "{\"id\":2}\n", "{\"id\":3}\n"} {
		line, err := r.ReadString('\n')
		if err != nil || line != want {
			t.Errorf("read %q, %v; want %q", line, err, want)
		}
	}
}

func TestLinesBecomeFrames(t *testing.T) {
	c, browser := conns(t)
	for _, w := range []string{"{\"a\":1}\n{\"b\"", ":2}\n", "{\"c\":3}\n"} {
		if _, err := io.WriteString(c, w); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"{\"a\":1}\n", "{\"b\":2}\n", "{\"c\":3}\n"} {
		typ, frame, err := browser.Read(t.Context())
		if err != nil || typ != websocket.MessageText || string(frame) != want {
			t.Errorf("frame %v %q, %v; want text %q", typ, frame, err, want)
		}
	}
}

func TestAClosedBrowserEndsReads(t *testing.T) {
	c, browser := conns(t)
	closed := make(chan error, 1)
	go func() { closed <- browser.Close(websocket.StatusNormalClosure, "") }()
	if _, err := c.Read(make([]byte, 8)); err != io.EOF {
		t.Errorf("Read after the browser left = %v, want io.EOF", err)
	}
	if err := <-closed; err != nil {
		t.Errorf("the browser's close handshake: %v", err)
	}
}

// drain reads the browser's side, as a browser does, so close handshakes
// complete.
func drain(ctx context.Context, browser *websocket.Conn) {
	go func() {
		for {
			if _, _, err := browser.Read(ctx); err != nil {
				return
			}
		}
	}()
}

func TestBinaryAndOversizedFramesEndTheConnection(t *testing.T) {
	c, browser := conns(t)
	drain(t.Context(), browser)
	if err := browser.Write(t.Context(), websocket.MessageBinary, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(make([]byte, 8)); err == nil || err == io.EOF {
		t.Errorf("a binary frame read %v, want an error", err)
	}

	c, browser = conns(t)
	drain(t.Context(), browser)
	big := []byte(strings.Repeat("x", MaxMessage+1))
	wrote := make(chan struct{})
	go func() {
		defer close(wrote)
		_ = browser.Write(t.Context(), websocket.MessageText, big)
	}()
	if _, err := io.Copy(io.Discard, c); err == nil {
		t.Error("a frame over MaxMessage was read whole")
	}
	_ = browser.CloseNow()
	<-wrote
	_, err := io.WriteString(c, strings.Repeat("y", MaxMessage+1))
	if err == nil {
		t.Error("a line over MaxMessage was buffered")
	}
}

func TestADeadlineClosesTheConnection(t *testing.T) {
	c, _ := conns(t)
	if err := c.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(make([]byte, 8)); err == nil {
		t.Error("Read past its deadline succeeded")
	} else if !errors.Is(err, io.EOF) && c.ctx.Err() == nil {
		t.Errorf("Read past its deadline = %v, and the Conn is still open", err)
	}
}
