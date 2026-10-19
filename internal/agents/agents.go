// Package agents keeps the agents on lodge's host and their socket,
// /run/lodge/agent.sock (docs/specs/lodge-http.md, The agent socket;
// ADR-0014). The agents member of /etc/lodge/config.json names their system
// users. The socket admits a connection only when its peer's uid
// (SO_PEERCRED) is one of theirs, and the gateway serves /mcp/<id> on it with
// the connection's user in place of WhoIs.
package agents

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// MaxAgents bounds the agents member.
const MaxAgents = 16

// RefusalLogEvery is how often a refused uid is logged at most.
const RefusalLogEvery = time.Minute

var userRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// LoadConfig reads the agents member of lodge's configuration at path: up
// to MaxAgents distinct system user names, never root or lodge. A missing
// file or member is no agents.
func LoadConfig(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var file struct {
		Agents jsontext.Value `json:"agents"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(file.Agents) == 0 {
		return nil, nil
	}
	var names []string
	if err := json.Unmarshal(file.Agents, &names); err != nil {
		return nil, fmt.Errorf("%s: agents: %w", path, err)
	}
	if err := Validate(names); err != nil {
		return nil, fmt.Errorf("%s: agents: %w", path, err)
	}
	return names, nil
}

// Validate checks the agents' names.
func Validate(names []string) error {
	if len(names) > MaxAgents {
		return fmt.Errorf("at most %d agents, not %d", MaxAgents, len(names))
	}
	seen := map[string]bool{}
	for _, n := range names {
		switch {
		case !userRE.MatchString(n):
			return fmt.Errorf("agent %q is not a system user's name", n)
		case n == "root" || n == "lodge":
			return fmt.Errorf("%s cannot be an agent", n)
		case seen[n]:
			return fmt.Errorf("agent %s is listed twice", n)
		}
		seen[n] = true
	}
	return nil
}

// Status is the agent socket's state, for lodgectl status.
type Status struct {
	Path string `json:"path"`
	// Agents are the listed users that exist, whom the socket admits.
	Agents []string `json:"agents"`
	// Skipped are listed users that do not exist.
	Skipped []string `json:"skipped,omitzero"`
	// Refused is why the agents member was refused, when it was.
	Refused string `json:"refused,omitzero"`
	// Listening is whether the socket is up.
	Listening bool `json:"listening"`
}

// Options configure the agents.
type Options struct {
	// Path is the socket's path, for Status.
	Path string
	// DevUID, when not negative, is the developer's uid under --root,
	// admitted as the developer's user whether listed or not.
	DevUID int
	// Lookup resolves a user's name to its uid [os/user].
	Lookup func(name string) (int, error)
	// PerUID and Total cap the socket's connections [64, 1024].
	PerUID, Total int
	Log           *slog.Logger
	Now           func() time.Time
}

// Agents are the agents lodge's configuration names, and their socket's
// connections.
type Agents struct {
	o Options

	mu        sync.Mutex
	byUID     map[int]string
	listed    map[string]bool
	skipped   []string
	refused   string
	devName   string
	listening bool
	conns     map[*conn]struct{}
	perUID    map[int]int
	logged    map[int]time.Time
}

// New returns agents with none listed.
func New(o Options) *Agents {
	if o.Lookup == nil {
		o.Lookup = lookupUID
	}
	if o.PerUID == 0 {
		o.PerUID = 64
	}
	if o.Total == 0 {
		o.Total = 1024
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	a := &Agents{o: o, byUID: map[int]string{}, listed: map[string]bool{}, conns: map[*conn]struct{}{},
		perUID: map[int]int{}, logged: map[int]time.Time{}}
	if o.DevUID >= 0 {
		if u, err := user.LookupId(strconv.Itoa(o.DevUID)); err == nil {
			a.devName = u.Username
		} else {
			a.devName = strconv.Itoa(o.DevUID)
		}
	}
	return a
}

func lookupUID(name string) (int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return -1, err
	}
	return strconv.Atoi(u.Uid)
}

// Load takes the result of LoadConfig. On an error it keeps the agents it
// had and records why. Otherwise it resolves each name, skipping one with no
// system user, and closes the connections of every uid it no longer admits.
func (a *Agents) Load(names []string, err error) {
	if err != nil {
		a.mu.Lock()
		a.refused = err.Error()
		a.mu.Unlock()
		a.o.Log.Error("the agents are refused; keeping the previous list", "err", err)
		return
	}
	byUID := map[int]string{}
	listed := map[string]bool{}
	var skipped []string
	for _, n := range names {
		uid, err := a.o.Lookup(n)
		if err != nil {
			skipped = append(skipped, n)
			a.o.Log.Warn("an agent has no system user; skipped", "agent", n, "err", err)
			continue
		}
		byUID[uid] = n
		listed[n] = true
	}
	a.mu.Lock()
	a.byUID, a.listed, a.skipped, a.refused = byUID, listed, skipped, ""
	var drop []*conn
	for c := range a.conns {
		if name, ok := a.admitLocked(c.uid); !ok || name != c.user {
			drop = append(drop, c)
		}
	}
	a.mu.Unlock()
	for _, c := range drop {
		_ = c.Close()
	}
	if len(drop) > 0 {
		a.o.Log.Info("closed the connections of agents no longer listed", "closed", len(drop))
	}
}

func (a *Agents) admitLocked(uid int) (string, bool) {
	if name, ok := a.byUID[uid]; ok {
		return name, true
	}
	if a.o.DevUID >= 0 && uid == a.o.DevUID {
		return a.devName, true
	}
	return "", false
}

// Admit returns the agent's user name for a peer uid, when the socket
// admits it.
func (a *Agents) Admit(uid int) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.admitLocked(uid)
}

// Listed reports whether a token may be issued for the user: a listed
// agent that exists, or the developer under --root.
func (a *Agents) Listed(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.listed[name] || (a.o.DevUID >= 0 && name == a.devName)
}

// Status reports the socket and its agents.
func (a *Agents) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := Status{Path: a.o.Path, Agents: []string{}, Skipped: slices.Clone(a.skipped), Refused: a.refused, Listening: a.listening}
	for n := range a.listed {
		st.Agents = append(st.Agents, n)
	}
	slices.Sort(st.Agents)
	return st
}

// Listen makes the socket at path, mode 0660, in group (when group is not
// empty), replacing a stale one.
func Listen(path, group string) (*net.UnixListener, error) {
	if len(path) > 107 {
		return nil, fmt.Errorf("agent socket %s: longer than a Unix socket's 107 bytes; use a shorter --root", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("agent socket: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("agent socket: %w", err)
	}
	old := syscall.Umask(0o177)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	syscall.Umask(old)
	if err != nil {
		return nil, fmt.Errorf("agent socket: %w", err)
	}
	if group != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			ln.Close()
			return nil, fmt.Errorf("agent socket: group %s: %w", group, err)
		}
		gid, _ := strconv.Atoi(g.Gid)
		if err := os.Chown(path, -1, gid); err != nil {
			ln.Close()
			return nil, fmt.Errorf("agent socket: %w (lodge must be in group %s)", err, group)
		}
	}
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return nil, fmt.Errorf("agent socket: %w", err)
	}
	return ln, nil
}

// Listener wraps the socket's listener. It closes a connection, before
// anything is read from it, unless its peer's uid is an agent's and the
// connection caps allow it, and it gives each one its agent's user.
func (a *Agents) Listener(ln net.Listener) net.Listener {
	a.mu.Lock()
	a.listening = true
	a.mu.Unlock()
	return &listener{Listener: ln, a: a}
}

type listener struct {
	net.Listener
	a *Agents
}

func (l *listener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		uid := peerUID(c)
		if ac, ok := l.a.accept(c, uid); ok {
			return ac, nil
		}
		_ = c.Close()
	}
}

func (l *listener) Close() error {
	l.a.mu.Lock()
	l.a.listening = false
	l.a.mu.Unlock()
	return l.Listener.Close()
}

func (a *Agents) accept(c net.Conn, uid int) (*conn, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	name, ok := a.admitLocked(uid)
	if !ok {
		now := a.o.Now()
		if last, seen := a.logged[uid]; !seen || now.Sub(last) >= RefusalLogEvery {
			a.logged[uid] = now
			a.o.Log.Warn("the agent socket refused a peer that is not a listed agent", "uid", uid)
		}
		return nil, false
	}
	if len(a.conns) >= a.o.Total || a.perUID[uid] >= a.o.PerUID {
		return nil, false
	}
	ac := &conn{Conn: c, a: a, uid: uid, user: name}
	a.conns[ac] = struct{}{}
	a.perUID[uid]++
	return ac, true
}

func (a *Agents) release(c *conn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.conns[c]; !ok {
		return
	}
	delete(a.conns, c)
	if a.perUID[c.uid]--; a.perUID[c.uid] <= 0 {
		delete(a.perUID, c.uid)
	}
}

// conn is an admitted connection, with its agent's uid and user.
type conn struct {
	net.Conn
	a    *Agents
	uid  int
	user string
	once sync.Once
}

func (c *conn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.a.release(c) })
	return err
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

type userKey struct{}

// ConnContext records an admitted connection's agent in its context, for
// an http.Server's ConnContext.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	if ac, ok := c.(*conn); ok {
		return context.WithValue(ctx, userKey{}, ac.user)
	}
	return ctx
}

// User is the agent's user name a request's connection was admitted as.
func User(ctx context.Context) (string, bool) {
	u, ok := ctx.Value(userKey{}).(string)
	return u, ok && u != ""
}
