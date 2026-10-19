// Package sessions holds the live browser sessions: one per /app/<id>/api
// WebSocket (docs/specs/lodge-http.md, Sessions). It bounds them per node
// and app and in all, closes those with no browser frame for 90 s, and
// closes those whose peer no longer passes. Sessions live in memory only.
package sessions

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/frostyard/lodge/internal/peer"
)

// The limits (docs/specs/lodge-http.md, Sessions).
const (
	Idle         = 90 * time.Second
	RecheckEvery = 60 * time.Second
	PerNodeApp   = 8
	Max          = 64
)

// WebSocket close codes lodge uses.
const (
	CodeGoingAway       = 1001
	CodePolicyViolation = 1008
)

var (
	// ErrFull means lodge holds Max sessions already.
	ErrFull = errors.New("lodge holds too many sessions")
	// ErrNotFound means no live session has the ID.
	ErrNotFound = errors.New("no such session")
)

// Session is one live session.
type Session struct {
	ID         string
	App        string
	Peer       peer.Peer
	RemoteAddr string
	Started    time.Time
	// Recheck asks WhoIs again and re-runs the identity checks; a non-nil
	// error closes the session (1008) with the error's text.
	Recheck func(context.Context) error

	seen      atomic.Int64 // unix nanoseconds of the browser's last frame
	close     func(code int, reason string)
	closeOnce sync.Once
}

// New returns a session that close ends. Its seenAt starts at started.
func New(app string, p peer.Peer, remoteAddr string, started time.Time, close func(code int, reason string)) *Session {
	var b [8]byte
	_, _ = rand.Read(b[:])
	s := &Session{ID: hex.EncodeToString(b[:]), App: app, Peer: p, RemoteAddr: remoteAddr, Started: started, close: close}
	s.seen.Store(started.UnixNano())
	return s
}

// Touch records a frame from the browser.
func (s *Session) Touch(now time.Time) { s.seen.Store(now.UnixNano()) }

// SeenAt is when the browser last sent a frame.
func (s *Session) SeenAt() time.Time { return time.Unix(0, s.seen.Load()) }

// Close ends the session once, with a WebSocket close code and reason.
func (s *Session) Close(code int, reason string) {
	s.closeOnce.Do(func() {
		if s.close != nil {
			s.close(code, reason)
		}
	})
}

// Info is a session as lodgectl shows it.
type Info struct {
	ID       string    `json:"id"`
	App      string    `json:"app"`
	Login    string    `json:"login"`
	NodeName string    `json:"nodeName"`
	Started  time.Time `json:"started"`
	SeenAt   time.Time `json:"seenAt"`
}

// Registry is the live sessions.
type Registry struct {
	mu      sync.Mutex
	byID    map[string]*Session
	pending int
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{byID: map[string]*Session{}} }

// Reservation holds a place for a session being opened, so the total
// bound is decided before the upgrade.
type Reservation struct {
	r    *Registry
	done bool
}

// Reserve holds a place, or returns ErrFull.
func (r *Registry) Reserve() (*Reservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.byID)+r.pending >= Max {
		return nil, ErrFull
	}
	r.pending++
	return &Reservation{r: r}, nil
}

// Cancel gives the place back. It does nothing after Add.
func (v *Reservation) Cancel() {
	v.r.mu.Lock()
	defer v.r.mu.Unlock()
	if !v.done {
		v.done = true
		v.r.pending--
	}
}

// Add turns the place into s. Past the node's bound for the app, it first
// closes that node's session of the app with the oldest seenAt.
func (v *Reservation) Add(s *Session) {
	r := v.r
	r.mu.Lock()
	if !v.done {
		v.done = true
		r.pending--
	}
	var same []*Session
	for _, o := range r.byID {
		if o.App == s.App && o.Peer.NodeID == s.Peer.NodeID {
			same = append(same, o)
		}
	}
	var evict []*Session
	if over := len(same) - PerNodeApp + 1; over > 0 {
		slices.SortFunc(same, func(a, b *Session) int {
			return cmp.Or(a.SeenAt().Compare(b.SeenAt()), cmp.Compare(a.ID, b.ID))
		})
		evict = same[:over]
		for _, o := range evict {
			delete(r.byID, o.ID)
		}
	}
	r.byID[s.ID] = s
	r.mu.Unlock()
	for _, o := range evict {
		o.Close(CodePolicyViolation, "this node opened too many sessions of this app")
	}
}

// Remove forgets session id once it has ended.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byID, id)
}

// closeWhere removes and closes every session match selects.
func (r *Registry) closeWhere(match func(*Session) bool, code int, reason string) int {
	r.mu.Lock()
	var hit []*Session
	for id, s := range r.byID {
		if match(s) {
			hit = append(hit, s)
			delete(r.byID, id)
		}
	}
	r.mu.Unlock()
	for _, s := range hit {
		s.Close(code, reason)
	}
	return len(hit)
}

// Sweep closes (1001) every session with no browser frame for Idle.
func (r *Registry) Sweep(now time.Time) int {
	return r.closeWhere(func(s *Session) bool { return now.Sub(s.SeenAt()) >= Idle }, CodeGoingAway, "no frame from the browser for 90 s")
}

// Recheck runs every session's Recheck and closes (1008) those that fail.
func (r *Registry) Recheck(ctx context.Context) int {
	r.mu.Lock()
	all := make([]*Session, 0, len(r.byID))
	for _, s := range r.byID {
		all = append(all, s)
	}
	r.mu.Unlock()
	n := 0
	for _, s := range all {
		if s.Recheck == nil {
			continue
		}
		if err := s.Recheck(ctx); err != nil {
			r.Remove(s.ID)
			s.Close(CodePolicyViolation, err.Error())
			n++
		}
	}
	return n
}

// EndLogin closes every session of login.
func (r *Registry) EndLogin(login string) int {
	return r.closeWhere(func(s *Session) bool { return s.Peer.Login == login }, CodePolicyViolation, "this login is no longer allowed")
}

// EndNode closes every session of the node.
func (r *Registry) EndNode(nodeID string) int {
	return r.closeWhere(func(s *Session) bool { return s.Peer.NodeID == nodeID }, CodePolicyViolation, "this node is no longer allowed")
}

// End closes session id.
func (r *Registry) End(id string) error {
	if r.closeWhere(func(s *Session) bool { return s.ID == id }, CodePolicyViolation, "the session was ended") == 0 {
		return ErrNotFound
	}
	return nil
}

// Count is the number of live sessions.
func (r *Registry) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byID)
}

// List returns the live sessions, oldest first.
func (r *Registry) List() []Info {
	r.mu.Lock()
	out := make([]Info, 0, len(r.byID))
	for _, s := range r.byID {
		out = append(out, Info{ID: s.ID, App: s.App, Login: s.Peer.Login, NodeName: s.Peer.NodeName, Started: s.Started, SeenAt: s.SeenAt()})
	}
	r.mu.Unlock()
	slices.SortFunc(out, func(a, b Info) int { return cmp.Or(a.Started.Compare(b.Started), cmp.Compare(a.ID, b.ID)) })
	return out
}
