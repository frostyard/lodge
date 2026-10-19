package gateway

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/peer"
	"github.com/frostyard/lodge/internal/sessions"
)

// MaxFrame is the largest frame lodge relays (16 MiB).
const MaxFrame = 16 << 20

// dialTimeout bounds opening an app's side of a WebSocket.
const dialTimeout = 10 * time.Second

// session serves GET /app/<id>/api after the browser checks: it opens the
// app's /api, then upgrades the browser, and relays frames between them
// (docs/specs/lodge-http.md, Sessions).
func (g *Gateway) session(w http.ResponseWriter, r *http.Request, who peer.WhoIs, a apps.App, p peer.Peer, headers func(http.Header)) {
	if !isWebSocket(r) {
		w.Header().Set("Upgrade", "websocket")
		fail(w, http.StatusUpgradeRequired, "upgrade required: /app/"+a.ID+"/api is a WebSocket", headers)
		return
	}
	res, err := g.cfg.Sessions.Reserve()
	if err != nil {
		fail(w, http.StatusTooManyRequests, "lodge holds too many sessions", headers)
		return
	}
	defer res.Cancel()

	var browser, app *websocket.Conn
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	s := sessions.New(a.ID, p, r.RemoteAddr, g.now(), func(code int, reason string) {
		go closeBoth(browser, app, websocket.StatusCode(code), reason)
	})
	s.Recheck = func(ctx context.Context) error {
		if d := g.recheck(ctx, who, s.RemoteAddr, s.Peer); d != nil {
			return errors.New(d.line)
		}
		return nil
	}

	h := http.Header{}
	setCaller(h, proxyReq{kind: kindSession, peer: p})
	h.Set("Lodge-Session", s.ID)
	app, err = g.dialApp(ctx, a.ID, "/api", h, nil)
	if err != nil {
		g.log.Warn("open an app's session", "app", a.ID, "err", err)
		fail(w, http.StatusBadGateway, "app "+a.ID+" did not open the session", headers)
		return
	}
	headers(w.Header())
	browser, err = websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // Origin is checked exactly, above.
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		app.Close(websocket.StatusGoingAway, "the browser did not upgrade")
		return
	}
	res.Add(s)
	defer g.cfg.Sessions.Remove(s.ID)
	g.log.Info("session opened", "app", a.ID, "session", s.ID, "node", p.NodeName, "login", p.Login)
	code, reason := relay(ctx, browser, app, func() { s.Touch(g.now()) }, false)
	s.Close(int(code), reason)
	g.log.Info("session closed", "app", a.ID, "session", s.ID, "code", int(code))
}

// dialApp opens a WebSocket to path on app id's socket.
func (g *Gateway) dialApp(ctx context.Context, id, path string, h http.Header, subprotocols []string) (*websocket.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws://"+id+path, &websocket.DialOptions{
		HTTPClient:      g.ws,
		HTTPHeader:      h,
		Host:            id,
		Subprotocols:    subprotocols,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return nil, err
	}
	c.SetReadLimit(MaxFrame)
	return c, nil
}

// relay moves messages both ways, one to one, until either side closes
// or breaks a rule, and returns the code to close both with. touch runs
// on every frame from the browser. Binary frames close the relay (1003);
// a frame over MaxFrame closes it (1009).
func relay(ctx context.Context, browser, app *websocket.Conn, touch func(), binaryOK bool) (websocket.StatusCode, string) {
	browser.SetReadLimit(MaxFrame)
	app.SetReadLimit(MaxFrame)
	type result struct {
		code   websocket.StatusCode
		reason string
	}
	done := make(chan result, 2)
	pump := func(from, to *websocket.Conn, touch func()) {
		for {
			typ, data, err := from.Read(ctx)
			if err != nil {
				code, reason := closeFrom(err)
				done <- result{code, reason}
				return
			}
			if typ != websocket.MessageText && !binaryOK {
				done <- result{websocket.StatusUnsupportedData, "lodge relays text frames only"}
				return
			}
			if touch != nil {
				touch()
			}
			if err := to.Write(ctx, typ, data); err != nil {
				done <- result{websocket.StatusGoingAway, "the other side went away"}
				return
			}
		}
	}
	go pump(browser, app, touch)
	go pump(app, browser, nil)
	first := <-done
	closeBoth(browser, app, first.code, first.reason)
	return first.code, first.reason
}

// closeFrom is the code to pass on when a side's Read fails.
func closeFrom(err error) (websocket.StatusCode, string) {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		switch ce.Code {
		case websocket.StatusNoStatusRcvd:
			return websocket.StatusNormalClosure, ""
		case websocket.StatusAbnormalClosure, websocket.StatusTLSHandshake:
			return websocket.StatusGoingAway, "the other side went away"
		}
		return ce.Code, ce.Reason
	}
	if websocket.CloseStatus(err) == websocket.StatusMessageTooBig {
		return websocket.StatusMessageTooBig, "frame larger than 16 MiB"
	}
	if errors.Is(err, context.Canceled) {
		return websocket.StatusGoingAway, "lodge is closing the session"
	}
	return websocket.StatusGoingAway, "the other side went away"
}

// closeBoth closes both sides with code, concurrently, each waiting for
// its close handshake.
func closeBoth(a, b *websocket.Conn, code websocket.StatusCode, reason string) {
	if len(reason) > 120 {
		reason = reason[:120]
	}
	done := make(chan struct{}, 2)
	for _, c := range []*websocket.Conn{a, b} {
		go func() {
			defer func() { done <- struct{}{} }()
			if c != nil {
				_ = c.Close(code, reason)
			}
		}()
	}
	<-done
	<-done
}
