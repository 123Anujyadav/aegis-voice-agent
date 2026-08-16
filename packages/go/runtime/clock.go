package runtime

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Clock abstracts the passage of time.
//
// Every timeout, deadline, TTL, backoff and rate window in this package goes
// through a Clock. Nothing calls time.Now directly.
//
// This is not testing convenience for its own sake. A runtime whose behaviour
// depends on wall-clock time can only be tested by sleeping, and a test suite
// that sleeps is slow, flaky, and — worst — silently stops asserting anything
// when the sleep is a little too short on a loaded CI machine. Session
// expiry, breaker cooldown and backoff schedules are exactly the behaviours
// most worth testing and least amenable to real time.
//
// Implementations must be safe for concurrent use.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time

	// Since returns the time elapsed since t.
	Since(t time.Time) time.Duration

	// NewTimer returns a timer that fires once after d.
	NewTimer(d time.Duration) Timer

	// NewTicker returns a ticker that fires every d.
	NewTicker(d time.Duration) Ticker

	// Sleep blocks for d or until ctx is done, whichever is first. It returns
	// ctx.Err() if the context ended first, nil otherwise.
	Sleep(ctx context.Context, d time.Duration) error
}

// Timer is a one-shot timer.
type Timer interface {
	// C returns the channel on which the time is delivered.
	C() <-chan time.Time

	// Stop prevents the timer firing. It reports whether the call stopped the
	// timer before it fired.
	Stop() bool

	// Reset changes the timer to fire after d. It must only be called on a
	// stopped or expired timer whose channel has been drained.
	Reset(d time.Duration) bool
}

// Ticker fires repeatedly.
type Ticker interface {
	// C returns the channel on which ticks are delivered.
	C() <-chan time.Time

	// Stop halts the ticker. It does not close the channel.
	Stop()
}

// ---------------------------------------------------------------------------
// Real clock
// ---------------------------------------------------------------------------

// SystemClock is the production Clock, backed by the time package.
//
// It is a zero-size struct so passing it costs nothing and it can be shared
// freely without synchronisation.
type SystemClock struct{}

// Now returns the current instant.
func (SystemClock) Now() time.Time { return time.Now() }

// Since returns the time elapsed since t.
func (SystemClock) Since(t time.Time) time.Duration { return time.Since(t) }

// NewTimer returns a timer backed by time.Timer.
func (SystemClock) NewTimer(d time.Duration) Timer { return &systemTimer{t: time.NewTimer(d)} }

// NewTicker returns a ticker backed by time.Ticker.
func (SystemClock) NewTicker(d time.Duration) Ticker { return &systemTicker{t: time.NewTicker(d)} }

// Sleep blocks for d or until ctx is done.
func (SystemClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type systemTimer struct{ t *time.Timer }

func (s *systemTimer) C() <-chan time.Time        { return s.t.C }
func (s *systemTimer) Stop() bool                 { return s.t.Stop() }
func (s *systemTimer) Reset(d time.Duration) bool { return s.t.Reset(d) }

type systemTicker struct{ t *time.Ticker }

func (s *systemTicker) C() <-chan time.Time { return s.t.C }
func (s *systemTicker) Stop()               { s.t.Stop() }

// ---------------------------------------------------------------------------
// Fake clock
// ---------------------------------------------------------------------------

// FakeClock is a Clock whose time advances only when told to.
//
// It is exported rather than test-only because integration tests in other
// modules need it — a service testing its own session expiry should not have to
// reimplement a controllable clock.
//
// Advancing is synchronous: when Advance returns, every timer that should have
// fired has fired and every waiter that should have woken has been signalled.
// That property is what removes the sleeps from the test suite; without it a
// test would still need to yield and hope.
type FakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*fakeWaiter
	nextID  uint64
}

// fakeWaiter is a pending timer, ticker tick, or sleep.
type fakeWaiter struct {
	id       uint64
	deadline time.Time
	ch       chan time.Time
	period   time.Duration // non-zero for tickers
	stopped  bool
}

// NewFakeClock returns a FakeClock set to start.
//
// If start is the zero Time it is set to a fixed, arbitrary, non-zero instant.
// A zero-valued clock is a common source of confusing test failures, because
// code that treats a zero time.Time as "unset" then sees every timestamp as
// unset.
func NewFakeClock(start time.Time) *FakeClock {
	if start.IsZero() {
		start = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return &FakeClock{now: start}
}

// Now returns the fake current instant.
func (f *FakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Since returns the fake elapsed time since t.
func (f *FakeClock) Since(t time.Time) time.Duration {
	return f.Now().Sub(t)
}

// NewTimer returns a timer that fires when the fake clock passes d.
func (f *FakeClock) NewTimer(d time.Duration) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := f.addWaiterLocked(d, 0)
	return &fakeTimer{clock: f, waiter: w}
}

// NewTicker returns a ticker that fires every d of fake time.
func (f *FakeClock) NewTicker(d time.Duration) Ticker {
	if d <= 0 {
		panic("runtime: FakeClock.NewTicker requires a positive period")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w := f.addWaiterLocked(d, d)
	return &fakeTicker{clock: f, waiter: w}
}

// Sleep blocks until the fake clock advances by d, or ctx ends.
func (f *FakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := f.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// addWaiterLocked registers a waiter. Caller must hold f.mu.
func (f *FakeClock) addWaiterLocked(d, period time.Duration) *fakeWaiter {
	f.nextID++
	w := &fakeWaiter{
		id:       f.nextID,
		deadline: f.now.Add(d),
		// Buffered so a fire never blocks Advance. A test that does not read
		// its timer channel must not deadlock the clock.
		ch:     make(chan time.Time, 1),
		period: period,
	}
	f.waiters = append(f.waiters, w)
	return w
}

// Advance moves the clock forward by d, firing every waiter whose deadline is
// reached, in deadline order.
//
// Firing in order matters: two timers set 1 ms apart must fire in that order
// even when Advance jumps past both, or a test asserting sequencing passes or
// fails depending on map iteration order.
func (f *FakeClock) Advance(d time.Duration) {
	if d < 0 {
		panic("runtime: FakeClock.Advance cannot move backwards")
	}
	f.mu.Lock()
	target := f.now.Add(d)

	for {
		// Find the earliest unfired waiter at or before the target.
		var next *fakeWaiter
		for _, w := range f.waiters {
			if w.stopped {
				continue
			}
			if w.deadline.After(target) {
				continue
			}
			if next == nil || w.deadline.Before(next.deadline) ||
				(w.deadline.Equal(next.deadline) && w.id < next.id) {
				next = w
			}
		}
		if next == nil {
			break
		}

		// Move time to that waiter's deadline before firing, so any handler
		// observing Now() sees a consistent instant.
		f.now = next.deadline

		select {
		case next.ch <- f.now:
		default:
			// Channel already holds an unread tick. Dropping is correct and
			// matches time.Ticker's own behaviour under a slow consumer.
		}

		if next.period > 0 {
			next.deadline = next.deadline.Add(next.period)
		} else {
			next.stopped = true
		}
	}

	f.now = target
	f.compactLocked()
	f.mu.Unlock()
}

// BlockUntil waits until n waiters are registered.
//
// It exists for the one genuinely racy test pattern: a goroutine under test
// registers a timer, and the test must not Advance before it has. Polling with
// a real sleep would reintroduce exactly the flakiness FakeClock removes, so
// this spins on the clock's own lock instead — bounded, because a test that
// never reaches n will fail on the test framework's own timeout.
func (f *FakeClock) BlockUntil(n int) {
	for {
		f.mu.Lock()
		count := 0
		for _, w := range f.waiters {
			if !w.stopped {
				count++
			}
		}
		f.mu.Unlock()
		if count >= n {
			return
		}
		// Yield to the scheduler rather than sleeping. This is the one place
		// in the package that busy-waits, and it is bounded by the test
		// timeout rather than by a duration we would have to guess.
		runtimeGosched()
	}
}

// compactLocked drops stopped one-shot waiters. Caller must hold f.mu.
//
// Without this a long-running test that creates a timer per iteration grows the
// waiter slice without bound, and Advance becomes quadratic.
func (f *FakeClock) compactLocked() {
	if len(f.waiters) == 0 {
		return
	}
	live := f.waiters[:0]
	for _, w := range f.waiters {
		if !w.stopped {
			live = append(live, w)
		}
	}
	// Keep deadline order so Advance's scan is cheap on the common path.
	sort.SliceStable(live, func(i, j int) bool { return live[i].deadline.Before(live[j].deadline) })
	f.waiters = live
}

type fakeTimer struct {
	clock  *FakeClock
	waiter *fakeWaiter
}

func (t *fakeTimer) C() <-chan time.Time { return t.waiter.ch }

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	was := !t.waiter.stopped
	t.waiter.stopped = true
	return was
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	was := !t.waiter.stopped
	t.waiter.stopped = false
	t.waiter.deadline = t.clock.now.Add(d)
	return was
}

type fakeTicker struct {
	clock  *FakeClock
	waiter *fakeWaiter
}

func (t *fakeTicker) C() <-chan time.Time { return t.waiter.ch }

func (t *fakeTicker) Stop() {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	t.waiter.stopped = true
}
