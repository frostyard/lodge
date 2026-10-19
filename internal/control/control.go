// Package control serves lodge's control socket, /run/lodge/control.sock
// (docs/specs/lodge-http.md, The control socket and lodgectl): JSON-RPC 2.0,
// one message per line, to uid 0 and lodge's own uid only. It is the only
// place that allows a login or a node, issues a token, or reloads the apps.
package control

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/frostyard/lodge/internal/allow"
	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/inference"
	"github.com/frostyard/lodge/internal/peer"
	"github.com/frostyard/lodge/internal/sessions"
	"github.com/frostyard/lodge/internal/tokens"
)

// Error codes: JSON-RPC's, and frostnotes' notFound and conflict.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
	CodeNotFound       = 1001
	CodeConflict       = 1002
)

// MaxMessage bounds one request line.
const MaxMessage = 1 << 20

// Error is a JSON-RPC error.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

func errorf(code int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Node is one of lodge's tailnet nodes, for lodge.status.
type Node struct {
	Name    string `json:"name"`
	DNSName string `json:"dnsName,omitzero"`
	// State is the node's backend state: NeedsLogin, Starting, Running…
	State string `json:"state"`
	// LoginURL is where to log the node in, while it needs a login.
	LoginURL string `json:"loginURL,omitzero"`
	Up       bool   `json:"up"`
}

// Deps are what the commands act on.
type Deps struct {
	Allow    *allow.Lists
	Tokens   *tokens.Store
	Sessions *sessions.Registry
	Apps     *apps.Registry
	// EndToken closes a revoked token's streams, by its row (the gateway's).
	EndToken func(id int64) int
	// Peers lists the lodge node's peers; nil without a tailnet.
	Peers func(ctx context.Context) ([]peer.Peer, error)
	// Nodes reports lodge's tailnet nodes.
	Nodes func(ctx context.Context) []Node
	// Reload rereads the manifests.
	Reload func() error
	// MCPURL is https://LODGE/mcp/<id>, for issueToken's hint.
	MCPURL func(id string) string
	// Inference is the inference layer; nil without one.
	Inference Inference
	Log       *slog.Logger
}

// Inference is what the control socket asks of the inference layer.
type Inference interface {
	Status() ([]inference.ProviderStatus, string)
	Usage(ctx context.Context, since time.Time) ([]inference.Sum, error)
	SetKey(ctx context.Context, provider, key string) error
}

// Server serves the control socket.
type Server struct {
	d   Deps
	uid int // lodge's own
}

// New returns a server over d.
func New(d Deps) *Server {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Server{d: d, uid: os.Getuid()}
}

// Listen creates the socket at path, owned by lodge with mode 0600,
// replacing a stale one.
func Listen(path string) (*net.UnixListener, error) {
	if len(path) > 107 {
		return nil, fmt.Errorf("control socket %s: longer than a Unix socket's 107 bytes; use a shorter --root", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("control socket: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("control socket: %w", err)
	}
	old := syscall.Umask(0o177)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	syscall.Umask(old)
	if err != nil {
		return nil, fmt.Errorf("control socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("control socket: %w", err)
	}
	return ln, nil
}

// admit reports whether a peer uid may use the socket: root or lodge.
func (s *Server) admit(uid int) bool { return uid == 0 || uid == s.uid }

// peerUID is the connecting process's uid (SO_PEERCRED).
func peerUID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	var cred *unix.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return -1, err
	}
	if serr != nil {
		return -1, serr
	}
	return int(cred.Uid), nil
}

// Serve accepts connections until ctx ends.
func (s *Server) Serve(ctx context.Context, ln *net.UnixListener) error {
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, err := ln.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("control socket: %w", err)
		}
		uid, err := peerUID(c)
		if err != nil || !s.admit(uid) {
			s.d.Log.Warn("control socket refused a peer", "uid", uid, "err", err)
			c.Close()
			continue
		}
		wg.Go(func() { s.serveConn(ctx, c) })
	}
}

type request struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      jsontext.Value `json:"id,omitzero"`
	Method  string         `json:"method"`
	Params  jsontext.Value `json:"params,omitzero"`
}

type response struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      jsontext.Value `json:"id"`
	Result  any            `json:"result,omitzero"`
	Error   *Error         `json:"error,omitzero"`
}

func (s *Server) serveConn(ctx context.Context, c net.Conn) {
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64<<10), MaxMessage)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		resp := s.handle(ctx, line)
		b, err := json.Marshal(resp)
		if err != nil {
			b, _ = json.Marshal(response{JSONRPC: "2.0", ID: resp.ID, Error: errorf(CodeInternal, "lodge cannot encode its answer")})
		}
		if _, err := c.Write(append(b, '\n')); err != nil {
			return
		}
	}
}

func (s *Server) handle(ctx context.Context, line []byte) response {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return response{JSONRPC: "2.0", ID: jsontext.Value("null"), Error: errorf(CodeParseError, "the request is not JSON")}
	}
	id := req.ID
	if len(id) == 0 {
		id = jsontext.Value("null")
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		return response{JSONRPC: "2.0", ID: id, Error: errorf(CodeInvalidRequest, "not a JSON-RPC 2.0 request")}
	}
	result, err := s.call(ctx, req.Method, req.Params)
	if err != nil {
		var e *Error
		if !errors.As(err, &e) {
			s.d.Log.Error("control", "method", req.Method, "err", err)
			e = errorf(CodeInternal, "%s failed: %v", req.Method, err)
		}
		return response{JSONRPC: "2.0", ID: id, Error: e}
	}
	if result == nil {
		result = struct{}{}
	}
	return response{JSONRPC: "2.0", ID: id, Result: result}
}

// params decodes p into v, refusing unknown members.
func params(p jsontext.Value, v any) error {
	if len(p) == 0 {
		p = jsontext.Value("{}")
	}
	if err := json.Unmarshal(p, v, json.RejectUnknownMembers(true)); err != nil {
		return errorf(CodeInvalidParams, "invalid params: %v", err)
	}
	return nil
}
