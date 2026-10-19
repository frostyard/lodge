package jsonrpc_test

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/frostyard/lodge/kit/jsonrpc"
	"github.com/frostyard/lodge/kit/rpctest"
)

// The fake app's api: its code type, its caller kinds and its context.
type code int

const (
	codeNotFound code = 1001
	codeInternal code = jsonrpc.CodeInternal
)

func (c code) String() string {
	switch c {
	case jsonrpc.CodeMethodNotFound:
		return "methodNotFound"
	case codeInternal:
		return "internal"
	case codeNotFound:
		return "notFound"
	}
	return fmt.Sprintf("code(%d)", int(c))
}

type kind string

const (
	local   kind = "local"
	session kind = "session"
)

type (
	kindKey     struct{}
	readOnlyKey struct{}
)

func callerOf(ctx context.Context) kind {
	k, _ := ctx.Value(kindKey{}).(kind)
	return k
}

type (
	errT    = jsonrpc.Error[code]
	routerT = jsonrpc.Router[kind, code]
)

var errNotFound = &errT{Code: codeNotFound}

type echoParams struct {
	Text string `json:"text"`
}

// app is the fake app's router.
func app() *routerT {
	r := jsonrpc.NewRouter[kind, code](jsonrpc.RouterOptions[kind]{
		Caller: callerOf,
		Admit: func(ctx context.Context, _ string, rt jsonrpc.Route[kind]) bool {
			return !rt.Writes || ctx.Value(readOnlyKey{}) == nil
		},
	})
	both := []kind{local, session}
	handle := func(method string, callers []kind, writes bool, h jsonrpc.HandlerFunc) {
		r.Handle(method, jsonrpc.Route[kind]{Callers: callers, Writes: writes, Handler: h})
	}
	handle("rpc.hello", both, false, func(ctx context.Context, _ jsontext.Value) (any, error) {
		jsonrpc.ConnFrom(ctx).SetReady("test")
		return map[string]string{"caller": string(callerOf(ctx))}, nil
	})
	handle("echo", both, false, func(ctx context.Context, raw jsontext.Value) (any, error) {
		var p echoParams
		if err := jsonrpc.DecodeParams[code](raw, &p, []string{"text"}); err != nil {
			return nil, err
		}
		return p.Text, nil
	})
	handle("local.only", []kind{local}, false, func(context.Context, jsontext.Value) (any, error) { return "ok", nil })
	handle("write", both, true, func(context.Context, jsontext.Value) (any, error) { return "written", nil })
	handle("panic", both, false, func(context.Context, jsontext.Value) (any, error) { panic("a bug") })
	handle("huge", both, false, func(context.Context, jsontext.Value) (any, error) {
		return strings.Repeat("x", jsonrpc.MaxMessageSize), nil
	})
	handle("fail", both, false, func(context.Context, jsontext.Value) (any, error) { return nil, errors.New("disk full") })
	handle("missing", both, false, func(context.Context, jsontext.Value) (any, error) {
		return nil, jsonrpc.Errorf(codeNotFound, "no such note")
	})
	handle("subscribe", both, false, func(ctx context.Context, raw jsontext.Value) (any, error) {
		c := jsonrpc.ConnFrom(ctx)
		var p struct {
			N    int  `json:"n"`
			Fail bool `json:"fail"`
		}
		if err := jsonrpc.DecodeParams[code](raw, &p, nil); err != nil {
			return nil, err
		}
		jsonrpc.AfterReply(ctx, func() {
			c.Go(func(context.Context) {
				for i := range p.N {
					_ = c.Notify(jsonrpc.EventMethod, jsonrpc.EventEnvelope{Seq: int64(i + 1), Event: "tick", Data: jsontext.Value("{}")})
				}
			})
		})
		if p.Fail {
			return nil, jsonrpc.Errorf(codeNotFound, "nothing to subscribe to")
		}
		return "subscribed", nil
	})
	return r
}

// serve runs the fake app on a private socket, as caller local.
func serve(t *testing.T, opts jsonrpc.ServerOptions) (*rpctest.Run, *jsonrpc.Server[code], *routerT) {
	t.Helper()
	srv := jsonrpc.NewServer[code](opts)
	r := app()
	run := rpctest.Serve(t, func(ctx context.Context, ln net.Listener) error {
		return srv.Serve(ctx, ln, r, func(ctx context.Context) context.Context {
			return context.WithValue(ctx, kindKey{}, local)
		})
	})
	return run, srv, r
}

// raw speaks the wire format directly.
type raw struct {
	t  *testing.T
	nc net.Conn
	sc *bufio.Scanner
}

func dialRaw(t *testing.T, nc net.Conn) *raw {
	sc := bufio.NewScanner(nc)
	sc.Buffer(make([]byte, 64<<10), 2*jsonrpc.MaxMessageSize)
	return &raw{t: t, nc: nc, sc: sc}
}

func (r *raw) send(lines ...string) {
	r.t.Helper()
	if _, err := r.nc.Write([]byte(strings.Join(lines, "\n") + "\n")); err != nil {
		r.t.Fatal(err)
	}
}

func (r *raw) recv() string {
	r.t.Helper()
	_ = r.nc.SetReadDeadline(time.Now().Add(10 * time.Second))
	if !r.sc.Scan() {
		r.t.Fatalf("no reply: %v", r.sc.Err())
	}
	return r.sc.Text()
}

func TestWireRules(t *testing.T) {
	run, _, _ := serve(t, jsonrpc.ServerOptions{Hello: "rpc.hello"})
	r := dialRaw(t, run.Dial(t))
	r.send(`{"jsonrpc":"2.0","id":1,"method":"echo","params":{"text":"early"}}`)
	if got := r.recv(); got != `{"jsonrpc":"2.0","id":1,"error":{"code":1004,"message":"call rpc.hello first"}}` {
		t.Fatalf("a call before hello = %s", got)
	}
	// hello and a pipelined call: the call sees the connection ready.
	r.send(`{"jsonrpc":"2.0","id":"h","method":"rpc.hello"}`, `{"jsonrpc":"2.0","id":2,"method":"echo","params":{"text":"hi"}}`)
	if got := r.recv(); got != `{"jsonrpc":"2.0","id":"h","result":{"caller":"local"}}` {
		t.Fatalf("hello = %s", got)
	}
	if got := r.recv(); got != `{"jsonrpc":"2.0","id":2,"result":"hi"}` {
		t.Fatalf("echo = %s", got)
	}
	for _, tc := range []struct{ req, want string }{
		{`not json`, `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,`},
		{`{"jsonrpc":"1.0","id":4,"method":"x"}`, `{"jsonrpc":"2.0","id":4,"error":{"code":-32600,`},
		{`{"jsonrpc":"2.0","id":5}`, `"code":-32600`},
		{`{"jsonrpc":"2.0","id":6,"method":"no.such"}`, `{"code":-32601,"message":"method \"no.such\" does not exist"}`},
		{`{"jsonrpc":"2.0","id":7,"method":"echo","params":{}}`, `missing required param \"text\"`},
		{`{"jsonrpc":"2.0","id":8,"method":"echo","params":{"text":"a","extra":1}}`, `"code":-32602`},
		{`{"jsonrpc":"2.0","id":9,"method":"echo","params":["a"]}`, `params must be an object`},
		{`{"jsonrpc":"2.0","id":10,"method":"echo","params":{"text":null}}`, `missing required param \"text\"`},
	} {
		r.send(tc.req)
		if got := r.recv(); !strings.Contains(got, tc.want) {
			t.Errorf("%s -> %s, want %s", tc.req, got, tc.want)
		}
	}
	// A client notification is ignored: the next reply is the next call's.
	r.send(`{"jsonrpc":"2.0","method":"echo","params":{"text":"note"}}`, `{"jsonrpc":"2.0","id":11,"method":"echo","params":{"text":"after"}}`)
	if got := r.recv(); got != `{"jsonrpc":"2.0","id":11,"result":"after"}` {
		t.Errorf("after a notification = %s", got)
	}
}

func TestNoHelloReadiesAtOnce(t *testing.T) {
	run, _, _ := serve(t, jsonrpc.ServerOptions{})
	r := dialRaw(t, run.Dial(t))
	r.send(`{"jsonrpc":"2.0","id":1,"method":"echo","params":{"text":"now"}}`)
	if got := r.recv(); got != `{"jsonrpc":"2.0","id":1,"result":"now"}` {
		t.Errorf("echo without a hello = %s", got)
	}
}

// client connects a Client over a pipe served as k, and completes hello.
func client(t *testing.T, run *rpctest.Run, srv *jsonrpc.Server[code], r *routerT, ctx func(context.Context) context.Context) *jsonrpc.Client[code] {
	t.Helper()
	nc := run.Pipe(t, func(c context.Context, nc net.Conn) { srv.ServeConn(ctx(c), nc, r) })
	cl := jsonrpc.NewClient[code](nc)
	t.Cleanup(func() { _ = cl.Close() })
	if err := cl.Call(t.Context(), "rpc.hello", nil, nil); err != nil {
		t.Fatal(err)
	}
	return cl
}

func as(k kind) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context { return context.WithValue(ctx, kindKey{}, k) }
}

func TestCallerKindsAndAdmit(t *testing.T) {
	run, srv, r := serve(t, jsonrpc.ServerOptions{Hello: "rpc.hello"})
	var got string
	if err := client(t, run, srv, r, as(local)).Call(t.Context(), "local.only", nil, &got); err != nil || got != "ok" {
		t.Errorf("local.only as local = %q, %v", got, err)
	}
	s := client(t, run, srv, r, as(session))
	if err := s.Call(t.Context(), "local.only", nil, nil); !errors.Is(err, &errT{Code: jsonrpc.CodeMethodNotFound}) {
		t.Errorf("local.only as a session = %v, want methodNotFound", err)
	}
	if err := s.Call(t.Context(), "write", nil, nil); err != nil {
		t.Errorf("write as a session = %v", err)
	}
	ro := client(t, run, srv, r, func(ctx context.Context) context.Context {
		return context.WithValue(as(session)(ctx), readOnlyKey{}, true)
	})
	if err := ro.Call(t.Context(), "write", nil, nil); !errors.Is(err, &errT{Code: jsonrpc.CodeMethodNotFound}) {
		t.Errorf("write under a read scope = %v, want methodNotFound", err)
	}
	if _, err := r.Dispatch(t.Context(), "echo", nil); !errors.Is(err, &errT{Code: jsonrpc.CodeMethodNotFound}) {
		t.Errorf("Dispatch without a caller = %v, want methodNotFound", err)
	}
	if rt, ok := r.Route("write"); !ok || !rt.Writes || len(rt.Callers) != 2 {
		t.Errorf("Route(write) = %+v, %v", rt, ok)
	}
}

func TestErrorsReachTheClient(t *testing.T) {
	run, srv, r := serve(t, jsonrpc.ServerOptions{Hello: "rpc.hello"})
	c := client(t, run, srv, r, as(local))
	err := c.Call(t.Context(), "missing", nil, nil)
	var e *errT
	if !errors.As(err, &e) || e.Code != codeNotFound || !errors.Is(err, errNotFound) || err.Error() != "notFound: no such note" {
		t.Errorf("a coded error = %v", err)
	}
	if err := c.Call(t.Context(), "fail", nil, nil); !errors.As(err, &e) || e.Code != codeInternal || e.Message != "disk full" {
		t.Errorf("a plain error = %v, want internal with its message", err)
	}
}

func TestAPanicAnswersInternalAndTheConnectionLives(t *testing.T) {
	run, srv, r := serve(t, jsonrpc.ServerOptions{Hello: "rpc.hello"})
	c := client(t, run, srv, r, as(local))
	err := c.Call(t.Context(), "panic", nil, nil)
	if !errors.Is(err, &errT{Code: codeInternal}) || err.Error() != "internal: panic failed" {
		t.Fatalf("panic = %v, want internal", err)
	}
	var got string
	if err := c.Call(t.Context(), "echo", echoParams{"still"}, &got); err != nil || got != "still" {
		t.Errorf("after a panic: %q, %v", got, err)
	}
}

func TestAReplyTooLargeAnswersInternal(t *testing.T) {
	run, srv, r := serve(t, jsonrpc.ServerOptions{Hello: "rpc.hello"})
	c := client(t, run, srv, r, as(local))
	err := c.Call(t.Context(), "huge", nil, nil)
	var e *errT
	if !errors.As(err, &e) || e.Code != codeInternal || !strings.Contains(e.Message, "a message is at most") {
		t.Fatalf("huge = %v, want internal", err)
	}
	var got string
	if err := c.Call(t.Context(), "echo", echoParams{"small"}, &got); err != nil || got != "small" {
		t.Errorf("after a reply too large: %q, %v", got, err)
	}
}

func TestEventsFollowTheReply(t *testing.T) {
	var runs atomic.Int32
	closed := make(chan int32, 2)
	r := jsonrpc.NewRouter[kind, code](jsonrpc.RouterOptions[kind]{Caller: func(context.Context) kind { return local }})
	r.Handle("subscribe", jsonrpc.Route[kind]{Callers: []kind{local}, Handler: func(ctx context.Context, raw jsontext.Value) (any, error) {
		var p struct {
			Fail bool `json:"fail"`
		}
		if err := jsonrpc.DecodeParams[code](raw, &p, nil); err != nil {
			return nil, err
		}
		c := jsonrpc.ConnFrom(ctx)
		c.OnClose(func() { closed <- runs.Load() })
		jsonrpc.AfterReply(ctx, func() {
			runs.Add(1)
			_ = c.Notify(jsonrpc.EventMethod, jsonrpc.EventEnvelope{Seq: 1, Event: "tick", Data: jsontext.Value("{}")})
		})
		if p.Fail {
			return nil, jsonrpc.Errorf(codeNotFound, "nothing to subscribe to")
		}
		return "subscribed", nil
	}})
	srv := jsonrpc.NewServer[code](jsonrpc.ServerOptions{})
	run := rpctest.Serve(t, func(ctx context.Context, ln net.Listener) error { return srv.Serve(ctx, ln, r, nil) })
	nc := run.Dial(t)
	rc := dialRaw(t, nc)
	rc.send(`{"jsonrpc":"2.0","id":1,"method":"subscribe","params":{"fail":true}}`,
		`{"jsonrpc":"2.0","id":2,"method":"subscribe"}`)
	at := map[string]int{}
	for i := range 3 {
		at[rc.recv()] = i
	}
	_, ok1 := at[`{"jsonrpc":"2.0","id":1,"error":{"code":1001,"message":"nothing to subscribe to"}}`]
	replied, ok2 := at[`{"jsonrpc":"2.0","id":2,"result":"subscribed"}`]
	event, ok3 := at[`{"jsonrpc":"2.0","method":"event","params":{"seq":1,"event":"tick","data":{}}}`]
	if !ok1 || !ok2 || !ok3 || event < replied {
		t.Fatalf("messages %v: want both replies, and the event after its reply", at)
	}
	_ = nc.Close()
	if n := <-closed; n != 1 {
		t.Errorf("AfterReply ran %d times, want once: not for the failed call", n)
	}
}

func TestClientNotifications(t *testing.T) {
	run, srv, r := serve(t, jsonrpc.ServerOptions{Hello: "rpc.hello"})
	c := client(t, run, srv, r, as(local))
	if err := c.Call(t.Context(), "subscribe", map[string]int{"n": 3}, nil); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		ev := <-c.Notifications()
		if ev.Seq != int64(i+1) || ev.Event != "tick" {
			t.Errorf("event %d = %+v", i, ev)
		}
	}
}

func TestTheClientFailsWhenNotificationsAreNotRead(t *testing.T) {
	run, srv, r := serve(t, jsonrpc.ServerOptions{Hello: "rpc.hello"})
	c := client(t, run, srv, r, as(local))
	if err := c.Call(t.Context(), "subscribe", map[string]int{"n": jsonrpc.EventBuffer + 1}, nil); err != nil {
		t.Fatal(err)
	}
	<-c.Done()
	if !errors.Is(c.Err(), jsonrpc.ErrEventsOverflow) {
		t.Errorf("Err = %v, want overflow", c.Err())
	}
	if err := c.Call(t.Context(), "echo", echoParams{"x"}, nil); !errors.Is(err, jsonrpc.ErrEventsOverflow) {
		t.Errorf("a call after the overflow = %v", err)
	}
}

func TestConnLifetime(t *testing.T) {
	var mu sync.Mutex
	var order []string
	note := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, s)
	}
	closed := make(chan struct{})
	r := jsonrpc.NewRouter[kind, code](jsonrpc.RouterOptions[kind]{Caller: func(context.Context) kind { return local }})
	r.Handle("start", jsonrpc.Route[kind]{Callers: []kind{local}, Handler: func(ctx context.Context, _ jsontext.Value) (any, error) {
		c := jsonrpc.ConnFrom(ctx)
		c.OnClose(func() { note("first registered") })
		c.OnClose(func() { note("last registered"); close(closed) })
		c.Go(func(ctx context.Context) {
			<-ctx.Done()
			note("go returned")
		})
		if c.Client() != "" || !c.Ready() {
			return nil, errors.New("a connection without hello is ready, and has no client")
		}
		return nil, nil
	}})
	r.Handle("stop", jsonrpc.Route[kind]{Callers: []kind{local}, Handler: func(ctx context.Context, _ jsontext.Value) (any, error) {
		return nil, jsonrpc.ConnFrom(ctx).Close()
	}})
	srv := jsonrpc.NewServer[code](jsonrpc.ServerOptions{})
	run := rpctest.Serve(t, func(ctx context.Context, ln net.Listener) error { return srv.Serve(ctx, ln, r, nil) })
	c := jsonrpc.NewClient[code](run.Dial(t))
	if err := c.Call(t.Context(), "start", nil, nil); err != nil {
		t.Fatal(err)
	}
	_ = c.Call(t.Context(), "stop", nil, nil)
	<-closed
	<-c.Done()
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(order, ", ") != "go returned, last registered, first registered" {
		t.Errorf("order: %v", order)
	}
}

func TestAfterReplyOutsideARequestRunsAtOnce(t *testing.T) {
	ran := false
	jsonrpc.AfterReply(t.Context(), func() { ran = true })
	if !ran {
		t.Error("AfterReply outside a request did not run")
	}
	if jsonrpc.ConnFrom(t.Context()) != nil {
		t.Error("ConnFrom outside a server is not nil")
	}
}

func TestNotifyRefusesAnOversizedNotification(t *testing.T) {
	gotErr := make(chan error, 1)
	r := jsonrpc.NewRouter[kind, code](jsonrpc.RouterOptions[kind]{Caller: func(context.Context) kind { return local }})
	r.Handle("big", jsonrpc.Route[kind]{Callers: []kind{local}, Handler: func(ctx context.Context, _ jsontext.Value) (any, error) {
		gotErr <- jsonrpc.ConnFrom(ctx).Notify("event", strings.Repeat("y", jsonrpc.MaxMessageSize))
		return "done", nil
	}})
	srv := jsonrpc.NewServer[code](jsonrpc.ServerOptions{})
	run := rpctest.Serve(t, func(ctx context.Context, ln net.Listener) error { return srv.Serve(ctx, ln, r, nil) })
	rc := dialRaw(t, run.Dial(t))
	rc.send(`{"jsonrpc":"2.0","id":1,"method":"big"}`)
	if got := rc.recv(); got != `{"jsonrpc":"2.0","id":1,"result":"done"}` {
		t.Errorf("the reply after an oversized notification = %s", got)
	}
	if err := <-gotErr; err == nil || !strings.Contains(err.Error(), "message limit") {
		t.Errorf("Notify = %v, want refused", err)
	}
}
