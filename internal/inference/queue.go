package inference

import (
	"container/list"
	"context"
	"errors"
	"sync"
)

// MaxWaiting bounds the requests waiting for one provider.
const MaxWaiting = 256

// ErrQueueFull means MaxWaiting requests already wait.
var ErrQueueFull = errors.New("the provider's queue is full")

// Priority is a request's class.
type Priority int

// The classes (docs/specs/inference.md, The queue).
const (
	Background Priority = iota
	Interactive
)

func (p Priority) String() string {
	if p == Interactive {
		return "interactive"
	}
	return "background"
}

// ParsePriority reads Lodge-Priority: interactive, or background for
// anything else.
func ParsePriority(h string) Priority {
	if h == "interactive" {
		return Interactive
	}
	return Background
}

// Queue hands out a provider's slots: interactive first, and background
// to at most slots − 1 at once (at least 1).
type Queue struct {
	slots, bgMax int

	mu        sync.Mutex
	running   int
	bgRunning int
	waiting   [2]*list.List // by Priority; of *waiter
	// changed, when set (by tests), hears of every waiter queued or gone.
	changed chan struct{}
}

// notify tells a test's changed, read under the lock, since a test sets it
// while requests are queued. Callers hold no lock.
func (q *Queue) notify() {
	q.mu.Lock()
	changed := q.changed
	q.mu.Unlock()
	if changed != nil {
		changed <- struct{}{}
	}
}

type waiter struct {
	p     Priority
	ready chan struct{}
}

// NewQueue returns a queue of slots.
func NewQueue(slots int) *Queue {
	q := &Queue{slots: slots, bgMax: max(slots-1, 1)}
	q.waiting[Background] = list.New()
	q.waiting[Interactive] = list.New()
	return q
}

// Acquire waits for a slot for a request of class p, or for ctx to end.
// The returned release gives the slot back; call it once.
func (q *Queue) Acquire(ctx context.Context, p Priority) (release func(), err error) {
	q.mu.Lock()
	if q.admits(p) && q.waiting[Interactive].Len() == 0 && (p == Interactive || q.waiting[Background].Len() == 0) {
		q.take(p)
		q.mu.Unlock()
		return q.releaser(p), nil
	}
	if q.waiting[Background].Len()+q.waiting[Interactive].Len() >= MaxWaiting {
		q.mu.Unlock()
		return nil, ErrQueueFull
	}
	w := &waiter{p: p, ready: make(chan struct{})}
	e := q.waiting[p].PushBack(w)
	q.mu.Unlock()
	q.notify()

	select {
	case <-w.ready:
		return q.releaser(p), nil
	case <-ctx.Done():
		q.mu.Lock()
		select {
		case <-w.ready:
			// Granted while giving up: hand the slot on.
			q.mu.Unlock()
			q.releaser(p)()
		default:
			q.waiting[p].Remove(e)
			q.mu.Unlock()
			q.notify()
		}
		return nil, ctx.Err()
	}
}

// admits reports whether a request of class p could run now; q.mu held.
func (q *Queue) admits(p Priority) bool {
	return q.running < q.slots && (p == Interactive || q.bgRunning < q.bgMax)
}

func (q *Queue) take(p Priority) {
	q.running++
	if p == Background {
		q.bgRunning++
	}
}

func (q *Queue) releaser(p Priority) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			q.mu.Lock()
			defer q.mu.Unlock()
			q.running--
			if p == Background {
				q.bgRunning--
			}
			q.grant()
		})
	}
}

// grant gives free slots to waiters: the oldest interactive first, then
// background within its share; q.mu held.
func (q *Queue) grant() {
	for {
		var e *list.Element
		switch {
		case q.waiting[Interactive].Len() > 0 && q.admits(Interactive):
			e = q.waiting[Interactive].Front()
		case q.waiting[Interactive].Len() == 0 && q.waiting[Background].Len() > 0 && q.admits(Background):
			e = q.waiting[Background].Front()
		default:
			return
		}
		w := e.Value.(*waiter)
		q.waiting[w.p].Remove(e)
		q.take(w.p)
		close(w.ready)
	}
}

// Load is a queue's state for lodgectl status.
type Load struct {
	Running int `json:"running"`
	Waiting int `json:"waiting"`
}

// Load reports how many requests run and wait.
func (q *Queue) Load() Load {
	q.mu.Lock()
	defer q.mu.Unlock()
	return Load{Running: q.running, Waiting: q.waiting[Background].Len() + q.waiting[Interactive].Len()}
}
