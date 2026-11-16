package broker

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Listen makes the broker's socket, /run/lodge/nats.sock: mode 0660, and
// group group when that is not empty (docs/specs/lodge-http.md, The
// broker).
func Listen(path, group string) (*net.UnixListener, error) {
	if len(path) > 107 {
		return nil, fmt.Errorf("broker socket %s: longer than a Unix socket's 107 bytes", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("broker socket: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("broker socket: %w", err)
	}
	old := syscall.Umask(0o177)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	syscall.Umask(old)
	if err != nil {
		return nil, fmt.Errorf("broker socket: %w", err)
	}
	if group != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			ln.Close()
			return nil, fmt.Errorf("broker socket: group %s: %w", group, err)
		}
		gid, _ := strconv.Atoi(g.Gid)
		if err := os.Chown(path, -1, gid); err != nil {
			ln.Close()
			return nil, fmt.Errorf("broker socket: %w (lodge must be in group %s)", err, group)
		}
	}
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return nil, fmt.Errorf("broker socket: %w", err)
	}
	return ln, nil
}

// peerUID is a Unix connection's peer uid (SO_PEERCRED), or -1.
func peerUID(c net.Conn) int {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return -1
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return -1
	}
	uid := -1
	_ = raw.Control(func(fd uintptr) {
		if cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil {
			uid = int(cred.Uid)
		}
	})
	return uid
}

// door decides whose connection a CONNECT line belongs to.
type door struct {
	// admit decides the identity from the CONNECT line's options.
	admit func(ctx context.Context, opts map[string]jsontext.Value) (identity, bool)
	// again, when set, checks the identity a second time, once the
	// connection is registered, so that a revoke in between is seen.
	again func(ctx context.Context) bool
}

// ServeApps serves the broker's socket until ctx ends or ln is closed. A
// connection whose peer uid is no enabled app's is closed before anything
// is read from it.
func (b *Broker) ServeApps(ctx context.Context, ln net.Listener) error {
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	defer ln.Close()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("broker socket: %w", err)
		}
		go b.serveApp(ctx, c)
	}
}

// serveApp serves one app's connection.
func (b *Broker) serveApp(ctx context.Context, c net.Conn) {
	uid := b.peer(c)
	id, named := b.o.Apps.AppForUID(uid)
	dev := b.o.DevUID >= 0 && uid == b.o.DevUID
	if !named && !dev {
		b.logRefused(uid)
		_ = c.Close()
		return
	}
	b.serve(ctx, c, door{admit: func(_ context.Context, opts map[string]jsontext.Value) (identity, bool) {
		if named {
			return identity{kind: kindApp, name: id}, true
		}
		// Under --root every app runs as the developer, which names its
		// app in CONNECT's user field.
		name := ""
		if v, ok := opts["user"]; ok {
			if err := json.Unmarshal(v, &name); err != nil {
				return identity{}, false
			}
		}
		if !b.o.Apps.AppEnabled(name) {
			return identity{}, false
		}
		return identity{kind: kindApp, name: name}, true
	}})
}

// ServeClient serves one outside connection on /nats, such as an agent's
// event bridge, named by the token in its CONNECT line. It logs 1 line per
// connection.
func (b *Broker) ServeClient(ctx context.Context, c net.Conn) {
	tokens := b.o.Tokens
	if tokens == nil {
		// lodge issues no event token yet, so every client's CONNECT line
		// is refused once it is read.
		b.log.Warn("the broker refused a client: lodge issues no event token yet")
	}
	var name, token string
	b.serve(ctx, c, door{
		admit: func(ctx context.Context, opts map[string]jsontext.Value) (identity, bool) {
			if tokens == nil {
				return identity{}, false
			}
			if v, ok := opts["auth_token"]; ok {
				if err := json.Unmarshal(v, &token); err != nil || token == "" {
					return identity{}, false
				}
			}
			named, ok := tokens(ctx, token)
			if !ok {
				return identity{}, false
			}
			if _, err := b.account("client_" + named); err != nil {
				b.log.Error("make a client's account", "client", named, "err", err)
				return identity{}, false
			}
			b.mu.Lock()
			if b.clients[named] == nil {
				b.clients[named] = &clientState{subs: map[string]*consumerRef{}}
			}
			b.mu.Unlock()
			name = named
			return identity{kind: kindClient, name: named}, true
		},
		again: func(ctx context.Context) bool {
			if tokens == nil {
				return false
			}
			named, ok := tokens(ctx, token)
			return ok && named == name
		},
	})
}

// serve moves one connection to the server. The server sends INFO first,
// so lodge forwards it before it reads the outside's CONNECT line; then it
// decides whose connection this is, registers it, and writes the line again
// with a one-time secret in place of every credential.
func (b *Broker) serve(ctx context.Context, c net.Conn, d door) {
	outside := &lockedConn{Conn: c}
	in, err := b.srv.InProcessConn()
	if err != nil {
		b.log.Error("open the broker's in-process connection", "err", err)
		_ = outside.Close()
		return
	}
	defer in.Close()
	stop := context.AfterFunc(ctx, func() { _ = outside.Close(); _ = in.Close() })
	defer stop()

	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		_, _ = io.Copy(outside, in)
		_ = outside.Close()
		_ = in.Close()
	}()

	br := bufio.NewReaderSize(outside, MaxConnectLine)
	tc, secret, payload, ok := b.admitConn(ctx, outside, br, d)
	if !ok {
		<-done
		return
	}
	defer tc.Close()
	// A secret the server never used, because the connection ended first,
	// must not outlive it.
	defer b.forget(secret)
	b.log.Info("broker connection", "who", tc.key)
	if _, err := in.Write(payload); err != nil {
		b.log.Debug("write a connection's CONNECT line to the broker", "err", err)
		_ = in.Close()
		<-done
		return
	}
	go func() {
		defer func() { done <- struct{}{} }()
		_, _ = io.Copy(in, br)
		_ = in.Close()
		_ = outside.Close()
	}()
	<-done
	<-done
}

// admitConn reads one connection's CONNECT line, decides whose it is,
// registers the connection, and answers its one-time secret and the line to
// send on.
func (b *Broker) admitConn(ctx context.Context, outside *lockedConn, br *bufio.Reader, d door) (*conn, string, []byte, bool) {
	line, err := readConnect(br)
	if err != nil {
		b.log.Warn("the broker refused a connection: its first line is not a CONNECT line", "err", err)
		b.refuse(outside, br)
		return nil, "", nil, false
	}
	var opts map[string]jsontext.Value
	err = json.Unmarshal(line, &opts)
	// A CONNECT line whose options are no JSON object is refused: null
	// decodes into a nil map, and lodge writes the secret into that map.
	if err == nil && opts == nil {
		err = errNotObject
	}
	if err != nil {
		b.log.Warn("the broker refused a connection: its CONNECT line is not a JSON object", "err", err)
		b.refuse(outside, br)
		return nil, "", nil, false
	}
	idn, ok := d.admit(ctx, opts)
	if !ok {
		b.log.Warn("the broker refused a connection: lodge cannot name it")
		b.refuse(outside, br)
		return nil, "", nil, false
	}
	tc, ok := b.track(idn, outside)
	if !ok {
		b.log.Warn("the broker refused a connection: it holds too many already", "who", idn.key())
		b.refuse(tc, br)
		return nil, "", nil, false
	}
	if d.again != nil && !d.again(ctx) {
		b.log.Warn("the broker refused a connection: it was revoked while it connected", "who", idn.key())
		b.refuse(tc, br)
		return nil, "", nil, false
	}
	secret, err := b.newSecret(idn)
	if err != nil {
		b.log.Error("make a connection's secret", "err", err)
		b.refuse(tc, br)
		return nil, "", nil, false
	}
	for _, k := range []string{"auth_token", "user", "pass", "jwt", "nkey", "sig"} {
		delete(opts, k)
	}
	opts["auth_token"] = jsontext.Value(`"` + secret + `"`)
	body, err := json.Marshal(opts)
	if err != nil {
		b.log.Error("rewrite a connection's CONNECT line", "err", err)
		b.refuse(tc, br)
		return nil, "", nil, false
	}
	payload := append(append([]byte("CONNECT "), body...), '\r', '\n')
	return tc, secret, payload, true
}

// lockedConn serializes the writes of the proxy with those of a refusal, so
// that a refusal cannot land inside lodge's INFO line.
type lockedConn struct {
	net.Conn
	mu sync.Mutex
}

func (c *lockedConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.Write(p)
}

// errNoConnect is a first line that is not a CONNECT line.
var errNoConnect = errors.New("a connection's first line is not a CONNECT line")

// errNotObject is a CONNECT line whose options are not a JSON object.
var errNotObject = errors.New("a CONNECT line's options are not a JSON object")

// readConnect reads a connection's first line, and answers with its JSON
// object.
func readConnect(br *bufio.Reader) ([]byte, error) {
	line, isPrefix, err := br.ReadLine()
	if err != nil {
		return nil, err
	}
	if isPrefix {
		return nil, fmt.Errorf("a CONNECT line is longer than %d bytes", MaxConnectLine)
	}
	const prefix = "CONNECT "
	if len(line) < len(prefix) || !strings.EqualFold(string(line[:len(prefix)]), prefix) {
		return nil, errNoConnect
	}
	return line[len(prefix):], nil
}

// refusedLine is what a refused connection is told.
const refusedLine = "-ERR 'Authorization Violation'\r\n"

// refuseDrain bounds how long a refusal takes in what the peer has already
// sent. A pipe does not buffer, so a peer in the middle of its own write can
// read the refusal only after lodge has taken that write.
const refuseDrain = time.Second

// refuse answers a connection with an authorization violation, and closes
// it.
func (b *Broker) refuse(c net.Conn, br *bufio.Reader) {
	_ = c.SetReadDeadline(time.Now().Add(refuseDrain))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(io.Discard, br)
	}()
	_, _ = c.Write([]byte(refusedLine))
	_ = c.Close()
	<-done
}
