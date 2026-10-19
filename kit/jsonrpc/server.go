package jsonrpc

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime/debug"
	"sync"
	"sync/atomic"
)

// DefaultMaxInFlight bounds a connection's concurrent requests; reading
// pauses until one finishes.
const DefaultMaxInFlight = 32

// ServerOptions configures a Server.
type ServerOptions struct {
	// Hello is the method that readies a connection, such as rpc.hello:
	// until its handler calls Conn.SetReady, every other request answers
	// notReady, and the hello runs inline, so a call pipelined after it
	// sees the connection ready. Empty readies every connection at once.
	Hello string
	// MaxInFlight bounds each connection's concurrent requests:
	// DefaultMaxInFlight when 0.
	MaxInFlight int
	// Logger receives panics, failed requests and dropped replies. Nil
	// discards them.
	Logger *slog.Logger
}

// Server serves newline-delimited JSON-RPC 2.0 on connections, with error
// codes of type C:
//
//   - each line is one message of at most MaxMessageSize;
//   - requests run concurrently, MaxInFlight at a time, behind Hello;
//   - a handler that panics answers internal and is logged, and the
//     connection lives on;
//   - a handler's *Error[C] is sent as is, and any other error answers
//     internal;
//   - a reply over MaxMessageSize, which the client could not read,
//     answers internal instead;
//   - client notifications are ignored, since the protocol defines none.
type Server[C Code] struct {
	opts ServerOptions
	log  *slog.Logger
}

// NewServer returns a Server.
func NewServer[C Code](opts ServerOptions) *Server[C] {
	if opts.MaxInFlight <= 0 {
		opts.MaxInFlight = DefaultMaxInFlight
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server[C]{opts: opts, log: log}
}

// Serve accepts connections on ln until ctx ends, and serves each with
// ServeConn, in the context conn returns for it (ctx when conn is nil),
// which carries what the app's handlers know of the connection, such as
// its caller. It then waits for every connection to end. The listener
// checks who may connect (Listen, lodgeapp.Env.Listen).
func (s *Server[C]) Serve(ctx context.Context, ln net.Listener, d Dispatcher, conn func(context.Context) context.Context) error {
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		nc, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		cctx := ctx
		if conn != nil {
			cctx = conn(ctx)
		}
		wg.Go(func() { s.ServeConn(cctx, nc, d) })
	}
}

// ServeConn serves one connection whose listener has authenticated it,
// until it ends or ctx does, and then closes it: a socket connection from
// Serve, lodge's /api (lodgeapp.Server.API), or a development gateway's.
// ctx carries what the app's handlers know of the connection, and each
// request's context also carries the Conn (ConnFrom).
func (s *Server[C]) ServeConn(ctx context.Context, nc net.Conn, d Dispatcher) {
	c := &Conn{nc: nc, d: d, hello: s.opts.Hello, log: s.log, wire: s.wire,
		sem: make(chan struct{}, s.opts.MaxInFlight)}
	c.serve(ctx)
}

// wire returns err's error object, if it is an *Error[C].
func (s *Server[C]) wire(err error) (*wireError, bool) {
	var e *Error[C]
	if !errors.As(err, &e) {
		return nil, false
	}
	return &wireError{Code: int(e.Code), Message: e.Message, Data: e.Data}, true
}

// The messages the server reads and writes, with codes as plain ints.
type (
	request struct {
		JSONRPC string         `json:"jsonrpc"`
		ID      jsontext.Value `json:"id,omitzero"`
		Method  string         `json:"method,omitzero"`
		Params  jsontext.Value `json:"params,omitzero"`
	}
	reply struct {
		JSONRPC string         `json:"jsonrpc"`
		ID      jsontext.Value `json:"id"`
		Result  jsontext.Value `json:"result,omitzero"`
		Error   *wireError     `json:"error,omitzero"`
	}
	notification struct {
		JSONRPC string         `json:"jsonrpc"`
		Method  string         `json:"method"`
		Params  jsontext.Value `json:"params,omitzero"`
	}
	wireError struct {
		Code    int            `json:"code"`
		Message string         `json:"message"`
		Data    jsontext.Value `json:"data,omitzero"`
	}
)

// Conn is one connection a Server serves. Handlers reach it with ConnFrom,
// to notify the client, to keep state for the connection's lifetime, or to
// ready it after hello.
type Conn struct {
	nc    net.Conn
	d     Dispatcher
	hello string
	log   *slog.Logger
	wire  func(error) (*wireError, bool)
	sem   chan struct{}

	ctx    context.Context
	cancel context.CancelFunc

	wmu    sync.Mutex
	ready  atomic.Bool
	client atomic.Pointer[string]
	wg     sync.WaitGroup // requests and Go

	mu      sync.Mutex
	onClose []func()
	closed  bool
}

type connKey struct{}

// ConnFrom returns the connection a request arrived on, or nil outside
// the server.
func ConnFrom(ctx context.Context) *Conn {
	c, _ := ctx.Value(connKey{}).(*Conn)
	return c
}

func (c *Conn) serve(parent context.Context) {
	c.ctx, c.cancel = context.WithCancel(context.WithValue(parent, connKey{}, c))
	stop := context.AfterFunc(c.ctx, func() { _ = c.nc.Close() })
	defer func() {
		c.cancel()
		stop()
		_ = c.nc.Close()
		c.wg.Wait()
		c.mu.Lock()
		c.closed = true
		fns := c.onClose
		c.onClose = nil
		c.mu.Unlock()
		for i := len(fns) - 1; i >= 0; i-- {
			fns[i]()
		}
	}()

	sc := bufio.NewScanner(c.nc)
	sc.Buffer(make([]byte, 64<<10), MaxMessageSize)
	for sc.Scan() {
		var m request
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			c.reply(nil, nil, &wireError{Code: CodeParseError, Message: err.Error()})
			continue
		}
		if m.JSONRPC != "2.0" || m.Method == "" {
			c.reply(m.ID, nil, &wireError{Code: CodeInvalidRequest, Message: "expected a JSON-RPC 2.0 request"})
			continue
		}
		if len(m.ID) == 0 {
			continue // the protocol defines no client notifications
		}
		if c.hello != "" && !c.ready.Load() {
			if m.Method != c.hello {
				c.reply(m.ID, nil, &wireError{Code: CodeNotReady, Message: "call " + c.hello + " first"})
				continue
			}
			c.dispatch(m) // inline, so a pipelined call after hello sees the connection ready
			continue
		}
		c.sem <- struct{}{}
		c.wg.Go(func() {
			defer func() { <-c.sem }()
			c.dispatch(m)
		})
	}
	if err := sc.Err(); err != nil && c.ctx.Err() == nil {
		c.log.Debug("connection read ended", "err", err)
	}
}

// pending is one request's AfterReply functions.
type pending struct {
	mu  sync.Mutex
	fns []func()
}

type pendingKey struct{}

// AfterReply runs fn once the successful reply to ctx's request is on the
// wire, such as event delivery after events.subscribe answers; it does not
// run when the request fails. Outside a request, fn runs at once.
func AfterReply(ctx context.Context, fn func()) {
	p, ok := ctx.Value(pendingKey{}).(*pending)
	if !ok {
		fn()
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fns = append(p.fns, fn)
}

func (c *Conn) dispatch(m request) {
	p := &pending{}
	ctx := context.WithValue(c.ctx, pendingKey{}, p)
	res, err := c.call(ctx, m)
	var we *wireError
	if pe, ok := err.(*internalError); ok {
		we = &wireError{Code: CodeInternal, Message: pe.Error()}
	} else if err != nil {
		if we, ok = c.wire(err); !ok {
			c.log.Warn("request failed", "method", m.Method, "err", err)
			we = &wireError{Code: CodeInternal, Message: err.Error()}
		}
	}
	if !c.reply(m.ID, res, we) || we != nil {
		return
	}
	p.mu.Lock()
	fns := p.fns
	p.fns = nil
	p.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// call runs one request. A handler that panics answers internal and is
// logged, rather than ending the daemon for every client.
func (c *Conn) call(ctx context.Context, m request) (res any, err error) {
	defer func() {
		if p := recover(); p != nil {
			c.log.Error("request panicked", "method", m.Method, "panic", p, "stack", string(debug.Stack()))
			res, err = nil, &internalError{method: m.Method}
		}
	}()
	return c.d.Dispatch(ctx, m.Method, m.Params)
}

// internalError is a panic's answer: internal, "<method> failed".
type internalError struct{ method string }

func (e *internalError) Error() string { return e.method + " failed" }

// reply sends a result or an error object, and reports whether it did and
// the result was sent.
func (c *Conn) reply(id jsontext.Value, result any, we *wireError) bool {
	if len(id) == 0 {
		id = jsontext.Value("null")
	}
	r := reply{JSONRPC: "2.0", ID: id, Error: we}
	if we == nil {
		raw, err := json.Marshal(result)
		if err != nil {
			c.log.Warn("request failed", "err", err)
			r.Error = &wireError{Code: CodeInternal, Message: err.Error()}
		} else {
			r.Result = raw
		}
	}
	line, err := json.Marshal(r)
	if err != nil {
		c.log.Debug("reply not sent", "err", err)
		return false
	}
	if len(line) >= MaxMessageSize {
		big := len(line)
		r = reply{JSONRPC: "2.0", ID: id, Error: &wireError{Code: CodeInternal,
			Message: fmt.Sprintf("the reply is %d bytes; a message is at most %d", big, MaxMessageSize)}}
		if line, err = json.Marshal(r); err != nil {
			return false
		}
	}
	if err := c.write(line); err != nil {
		c.log.Debug("reply not sent", "err", err)
		return false
	}
	return r.Error == nil
}

func (c *Conn) write(line []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.nc.Write(append(line, '\n'))
	return err
}

// Notify sends a notification, such as an event (EventMethod with an
// EventEnvelope). One over MaxMessageSize is not sent.
func (c *Conn) Notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	line, err := json.Marshal(notification{JSONRPC: "2.0", Method: method, Params: raw})
	if err != nil {
		return err
	}
	if len(line) >= MaxMessageSize {
		return fmt.Errorf("a %s of %d bytes is over the %d-byte message limit", method, len(line), MaxMessageSize)
	}
	return c.write(line)
}

// Context ends when the connection does.
func (c *Conn) Context() context.Context { return c.ctx }

// Done is closed when the connection ends.
func (c *Conn) Done() <-chan struct{} { return c.ctx.Done() }

// SetReady readies the connection after a hello that named the client,
// such as "notesctl".
func (c *Conn) SetReady(client string) {
	c.client.Store(&client)
	c.ready.Store(true)
}

// Ready reports whether the connection is ready.
func (c *Conn) Ready() bool { return c.hello == "" || c.ready.Load() }

// Client is the name the client gave its hello: "" until then.
func (c *Conn) Client() string {
	if name := c.client.Load(); name != nil {
		return *name
	}
	return ""
}

// Go runs fn for the connection, with its context; the connection waits
// for fn before it finishes closing.
func (c *Conn) Go(fn func(ctx context.Context)) { c.wg.Go(func() { fn(c.ctx) }) }

// OnClose runs fn when the connection has ended and its requests and Go
// functions have returned, the last registered first. On a closed
// connection, fn runs at once.
func (c *Conn) OnClose(fn func()) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		fn()
		return
	}
	c.onClose = append(c.onClose, fn)
	c.mu.Unlock()
}

// Close ends the connection, such as a subscriber that lags.
func (c *Conn) Close() error {
	c.cancel()
	return nil
}
