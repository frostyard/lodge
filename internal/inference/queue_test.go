package inference

import (
	"context"
	"errors"
	"testing"
	"time"
)

// try acquires in the background and reports on the channel when granted.
func try(t *testing.T, q *Queue, p Priority) (granted <-chan func(), cancel context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	ch := make(chan func(), 1)
	go func() {
		release, err := q.Acquire(ctx, p)
		if err == nil {
			ch <- release
		}
	}()
	return ch, cancel
}

func got(t *testing.T, ch <-chan func()) func() {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("not granted")
		return nil
	}
}

// watched returns a queue of slots whose waiters the test can follow.
func watched(slots int) *Queue {
	q := NewQueue(slots)
	q.changed = make(chan struct{}, 2*MaxWaiting)
	return q
}

// waitFor follows q's changes until n requests wait.
func waitFor(t *testing.T, q *Queue, n int) {
	t.Helper()
	for q.Load().Waiting != n {
		select {
		case <-q.changed:
		case <-time.After(5 * time.Second):
			t.Fatalf("%d waiting, want %d", q.Load().Waiting, n)
		}
	}
}

// waits reports whether ch holds no grant once n requests wait.
func waits(t *testing.T, q *Queue, n int, ch <-chan func()) bool {
	t.Helper()
	waitFor(t, q, n)
	select {
	case <-ch:
		return false
	default:
		return true
	}
}

func TestBackgroundLeavesOneSlot(t *testing.T) {
	q := watched(4)
	var held []func()
	for range 3 {
		r, err := q.Acquire(t.Context(), Background)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, r)
	}
	// At slots − 1 background requests, a fourth waits.
	bg, cancelBG := try(t, q, Background)
	defer cancelBG()
	if !waits(t, q, 1, bg) {
		t.Fatal("background took the last slot")
	}
	// An interactive request takes the last slot at once.
	r, err := q.Acquire(t.Context(), Interactive)
	if err != nil {
		t.Fatal(err)
	}
	if l := q.Load(); l.Running != 4 || l.Waiting != 1 {
		t.Errorf("load = %+v", l)
	}
	// A background slot freed goes to the waiting background request.
	held[0]()
	got(t, bg)()
	r()
	for _, h := range held[1:] {
		h()
	}
	if l := q.Load(); l.Running != 0 || l.Waiting != 0 {
		t.Errorf("after all releases: %+v", l)
	}
}

func TestInteractiveFirstWhenASlotFrees(t *testing.T) {
	q := watched(2)
	a, _ := q.Acquire(t.Context(), Interactive)
	b, _ := q.Acquire(t.Context(), Interactive)
	bg, cancelBG := try(t, q, Background)
	defer cancelBG()
	waits(t, q, 1, bg)
	i1, c1 := try(t, q, Interactive)
	defer c1()
	waits(t, q, 2, i1)
	i2, c2 := try(t, q, Interactive)
	defer c2()
	waits(t, q, 3, i2)
	// Waiting: background, interactive 1, interactive 2. Slots go to the
	// interactive requests, oldest first, though background came first.
	a()
	r1 := got(t, i1)
	b()
	r2 := got(t, i2)
	select {
	case <-bg:
		t.Fatal("background went before an interactive request")
	default:
	}
	r1()
	got(t, bg)()
	r2()
}

func TestOneSlotStillRunsBackground(t *testing.T) {
	q := watched(1)
	r, err := q.Acquire(t.Context(), Background)
	if err != nil {
		t.Fatalf("background with one slot: %v", err)
	}
	i, cancel := try(t, q, Interactive)
	defer cancel()
	waits(t, q, 1, i)
	r()
	got(t, i)()
}

func TestCancelLeavesTheQueue(t *testing.T) {
	q := watched(1)
	r, _ := q.Acquire(t.Context(), Interactive)
	_, cancel := try(t, q, Interactive)
	waits(t, q, 1, nil)
	cancel()
	waitFor(t, q, 0)
	r()
	if l := q.Load(); l.Running != 0 {
		t.Errorf("a cancelled request holds a slot: %+v", l)
	}
}

func TestQueueLimit(t *testing.T) {
	q := watched(1)
	r, _ := q.Acquire(t.Context(), Interactive)
	defer r()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	for range MaxWaiting {
		go q.Acquire(ctx, Background)
	}
	waits(t, q, MaxWaiting, nil)
	if _, err := q.Acquire(t.Context(), Interactive); !errors.Is(err, ErrQueueFull) {
		t.Errorf("past %d waiting: %v, want ErrQueueFull", MaxWaiting, err)
	}
}

// A queue that grows grants the slots it frees to its waiters.
func TestResizeGrowGrantsWaiters(t *testing.T) {
	q := watched(1)
	r, err := q.Acquire(t.Context(), Interactive)
	if err != nil {
		t.Fatal(err)
	}
	a, ca := try(t, q, Interactive)
	defer ca()
	if !waits(t, q, 1, a) {
		t.Fatal("granted past one slot")
	}
	b, cb := try(t, q, Interactive)
	defer cb()
	if !waits(t, q, 2, b) {
		t.Fatal("granted past one slot")
	}
	q.Resize(3)
	ra, rb := got(t, a), got(t, b)
	if l := q.Load(); l != (Load{Running: 3}) {
		t.Fatalf("after a grow: %+v", l)
	}
	ra()
	rb()
	r()
	if l := q.Load(); l != (Load{}) {
		t.Errorf("after all releases: %+v", l)
	}
}

// A queue that shrinks lets the requests in flight finish, and starts no
// other until fewer than its slots run.
func TestResizeShrinkLetsRunningFinish(t *testing.T) {
	q := watched(3)
	var held []func()
	for range 3 {
		r, err := q.Acquire(t.Context(), Interactive)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, r)
	}
	// A queue of 2 still gives background its share of 1.
	q.Resize(2)
	if q.bgMax != 1 {
		t.Errorf("after Resize(2), bgMax = %d, want 1", q.bgMax)
	}
	// A shrink to 1 frees nothing: 3 requests keep their slots.
	q.Resize(1)
	if l := q.Load(); l != (Load{Running: 3}) {
		t.Fatalf("after a shrink: %+v", l)
	}
	w, cancel := try(t, q, Interactive)
	defer cancel()
	waits(t, q, 1, w)
	held[0]()
	if l := q.Load(); l != (Load{Running: 2, Waiting: 1}) {
		t.Fatalf("after one release: %+v", l)
	}
	if !waits(t, q, 1, w) {
		t.Fatal("a request started while 2 ran, with one slot")
	}
	held[1]()
	waits(t, q, 1, w)
	if l := q.Load(); l != (Load{Running: 1, Waiting: 1}) {
		t.Fatalf("after two releases: %+v", l)
	}
	if !waits(t, q, 1, w) {
		t.Fatal("a request started while 1 ran, with one slot")
	}
	held[2]()
	wr := got(t, w)
	if l := q.Load(); l != (Load{Running: 1}) {
		t.Errorf("after the shrink settled: %+v", l)
	}
	wr()
}

func TestParsePriority(t *testing.T) {
	for h, want := range map[string]Priority{"interactive": Interactive, "background": Background, "": Background, "Interactive": Background, "urgent": Background} {
		if got := ParsePriority(h); got != want {
			t.Errorf("ParsePriority(%q) = %v", h, got)
		}
	}
}
