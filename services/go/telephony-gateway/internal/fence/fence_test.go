package fence

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/platform"
)

// Unit tests: a scriptable session and controllable time, so every
// fail-closed path is deterministic. Real PostgreSQL behaviour is in
// fence_postgres_test.go.

var (
	_ platform.Runner  = (*Fence)(nil)
	_ platform.Checker = (*Fence)(nil)
)

var cfg = Config{LockKey: 4242, HeartbeatInterval: 10 * time.Second, HeartbeatTimeout: 2 * time.Second}

// clock is controllable time: now() reads it; sleep() blocks until the test
// steps it, which also advances now by the requested duration.
type clock struct {
	mu       sync.Mutex
	t        time.Time
	sleeping chan time.Duration
	wake     chan struct{}
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), sleeping: make(chan time.Duration, 1), wake: make(chan struct{})}
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *clock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func (c *clock) sleep(ctx context.Context, d time.Duration) error {
	select {
	case c.sleeping <- d:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-c.wake:
		c.advance(d)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// step waits until the fence sleeps, then lets that sleep finish.
func (c *clock) step(t *testing.T) {
	t.Helper()
	select {
	case <-c.sleeping:
	case <-time.After(5 * time.Second):
		t.Fatal("the fence never slept")
	}
	c.wake <- struct{}{}
}

type fakeSession struct {
	primary  func(ctx context.Context) (bool, error)
	tryLock  func(ctx context.Context) (bool, error)
	holds    func(ctx context.Context) (bool, error)
	unlocks  atomic.Int32
	closes   atomic.Int32
	holdsHit atomic.Int32
}

func (s *fakeSession) Primary(ctx context.Context) (bool, error) {
	if s.primary == nil {
		return true, nil
	}
	return s.primary(ctx)
}

func (s *fakeSession) TryLock(ctx context.Context, _ int64) (bool, error) {
	if s.tryLock == nil {
		return true, nil
	}
	return s.tryLock(ctx)
}

func (s *fakeSession) Holds(ctx context.Context, _ int64) (bool, error) {
	s.holdsHit.Add(1)
	if s.holds == nil {
		return true, nil
	}
	return s.holds(ctx)
}
func (s *fakeSession) Unlock(context.Context, int64) error { s.unlocks.Add(1); return nil }
func (s *fakeSession) Close(context.Context) error         { s.closes.Add(1); return nil }

type rig struct {
	f     *Fence
	c     *clock
	dials atomic.Int32
	sess  []*fakeSession
	mu    sync.Mutex
	logs  *bytes.Buffer
	ran   chan error
}

// newRig: each dial returns the next scripted session (the last repeats).
func newRig(t *testing.T, dialErr error, sessions ...*fakeSession) *rig {
	t.Helper()
	r := &rig{c: newClock(), sess: sessions, logs: &bytes.Buffer{}, ran: make(chan error, 1)}
	var logMu sync.Mutex
	log := slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		logMu.Lock()
		defer logMu.Unlock()
		return r.logs.Write(p)
	}), nil))
	dial := func(context.Context) (Session, error) {
		n := int(r.dials.Add(1))
		if dialErr != nil {
			return nil, dialErr
		}
		if n > len(r.sess) {
			n = len(r.sess)
		}
		return r.sess[n-1], nil
	}
	f, err := New(cfg, dial, log)
	if err != nil {
		t.Fatal(err)
	}
	f.now, f.sleep = r.c.now, r.c.sleep
	r.f = f
	return r
}

type writerFunc func([]byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }

func (r *rig) start(t *testing.T) {
	t.Helper()
	go func() { r.ran <- r.f.Run(context.Background()) }()
	t.Cleanup(func() { _ = r.f.Shutdown(context.Background()) })
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestConfig_EverythingIsRequired(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]Config{
		"zero":             {},
		"no key":           {HeartbeatInterval: time.Second, HeartbeatTimeout: time.Millisecond},
		"no interval":      {LockKey: 1, HeartbeatTimeout: time.Millisecond},
		"no timeout":       {LockKey: 1, HeartbeatInterval: time.Second},
		"timeout≥interval": {LockKey: 1, HeartbeatInterval: time.Second, HeartbeatTimeout: time.Second},
	} {
		if err := c.Validate(); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := New(cfg, nil, log); err == nil {
		t.Error("nil dialer accepted")
	}
	if _, err := New(cfg, func(context.Context) (Session, error) { return nil, nil }, nil); err == nil {
		t.Error("nil logger accepted")
	}
	if cfg.Validity() != 12*time.Second {
		t.Errorf("Validity = %s", cfg.Validity())
	}
}

// Ownership only after the acquisition grace; confirmations keep it.
func TestFence_OwnerOnlyAfterTheGraceThenWhileConfirmed(t *testing.T) {
	t.Parallel()
	s := &fakeSession{}
	r := newRig(t, nil, s)
	r.start(t)
	eventually(t, "the lock to be taken", func() bool { r.f.mu.Lock(); defer r.f.mu.Unlock(); return r.f.held })
	if r.f.Owned() || r.f.Check(context.Background()) == nil {
		t.Fatal("owner immediately after acquiring; the grace was skipped")
	}
	r.c.step(t) // +10s, confirmed: still inside the 12s grace
	eventually(t, "a confirmation", func() bool { return s.holdsHit.Load() == 1 })
	if r.f.Owned() {
		t.Fatal("owner at 10s; grace is 12s")
	}
	r.c.step(t) // +20s, confirmed
	eventually(t, "ownership", r.f.Owned)
	if err := r.f.Check(context.Background()); err != nil {
		t.Errorf("Check while owner: %v", err)
	}
}

// A failed confirmation ends ownership at once, closes the session and starts
// over on a new one.
func TestFence_AFailedConfirmationFailsClosed(t *testing.T) {
	t.Parallel()
	var fail atomic.Bool
	s1 := &fakeSession{holds: func(context.Context) (bool, error) {
		if fail.Load() {
			return false, errors.New("connection reset")
		}
		return true, nil
	}}
	s2 := &fakeSession{tryLock: func(context.Context) (bool, error) { return false, nil }}
	r := newRig(t, nil, s1, s2)
	r.start(t)
	r.c.step(t)
	r.c.step(t)
	eventually(t, "ownership", r.f.Owned)

	fail.Store(true)
	r.c.step(t)
	eventually(t, "ownership to end", func() bool { return !r.f.Owned() })
	eventually(t, "the session to close", func() bool { return s1.closes.Load() == 1 })
	if !strings.Contains(r.logText(), "ownership LOST") {
		t.Errorf("loss not logged:\n%s", r.logText())
	}
	r.c.step(t) // retry interval, then a NEW session is dialled
	eventually(t, "a new session", func() bool { return r.dials.Load() >= 2 })
	if r.f.Owned() {
		t.Error("owner on a session that did not get the lock")
	}
}

// The lock reported missing on our own backend (e.g. the server released it).
func TestFence_ALockReportedMissingFailsClosed(t *testing.T) {
	t.Parallel()
	var gone atomic.Bool
	s := &fakeSession{holds: func(context.Context) (bool, error) { return !gone.Load(), nil }}
	r := newRig(t, nil, s)
	r.start(t)
	r.c.step(t)
	r.c.step(t)
	eventually(t, "ownership", r.f.Owned)
	gone.Store(true)
	r.c.step(t)
	eventually(t, "ownership to end", func() bool { return !r.f.Owned() })
}

// A confirmation that never returns (a partitioned or paused connection):
// Owned() goes false by the clock alone, before the heartbeat gives up.
func TestFence_AStuckConfirmationExpiresOwnershipByTheClock(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	var stuck atomic.Bool
	s := &fakeSession{holds: func(ctx context.Context) (bool, error) {
		if stuck.Load() {
			select {
			case <-block:
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		return true, nil
	}}
	r := newRig(t, nil, s)
	r.start(t)
	r.c.step(t)
	r.c.step(t)
	eventually(t, "ownership", r.f.Owned)

	stuck.Store(true)
	r.c.step(t) // last confirmation stays at the previous one; Holds now hangs
	eventually(t, "the hung confirmation", func() bool { return s.holdsHit.Load() == 3 })
	if !r.f.Owned() {
		t.Fatal("ownership ended early")
	}
	r.c.advance(cfg.HeartbeatTimeout + time.Millisecond) // past confirmed + interval + timeout
	if r.f.Owned() {
		t.Error("a process whose confirmation is stuck still claims ownership after the validity window")
	}
	close(block)
}

func TestFence_ALockHeldElsewhereNeverMakesThisProcessOwner(t *testing.T) {
	t.Parallel()
	s := &fakeSession{tryLock: func(context.Context) (bool, error) { return false, nil }}
	r := newRig(t, nil, s)
	r.start(t)
	for range 3 {
		r.c.step(t)
		if r.f.Owned() {
			t.Fatal("owner without the lock")
		}
	}
	if s.closes.Load() < 3 {
		t.Errorf("sessions without the lock were not closed: %d", s.closes.Load())
	}
}

// An unreachable database: never owner, Run keeps going, nothing secret logged.
func TestFence_AnUnreachableDatabaseNeverMakesOwnerOrLeaksTheDSN(t *testing.T) {
	t.Parallel()
	r := newRig(t, errors.New(`dial: password=hunter2 host=db.internal user=gw`))
	r.start(t)
	r.c.step(t)
	r.c.step(t)
	if r.f.Owned() {
		t.Error("owner without a database")
	}
	select {
	case err := <-r.ran:
		t.Fatalf("Run ended on a database failure: %v", err)
	default:
	}
	if strings.Contains(r.logText(), "hunter2") || strings.Contains(r.logText(), "db.internal") {
		t.Errorf("log leaks connection details:\n%s", r.logText())
	}
}

func TestFence_ShutdownDropsOwnershipAndReleasesTheLock(t *testing.T) {
	t.Parallel()
	s := &fakeSession{}
	r := newRig(t, nil, s)
	r.start(t)
	r.c.step(t)
	r.c.step(t)
	eventually(t, "ownership", r.f.Owned)
	if err := r.f.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.f.Owned() {
		t.Error("owner after Shutdown")
	}
	if s.unlocks.Load() != 1 || s.closes.Load() != 1 {
		t.Errorf("unlocks %d, closes %d; want 1 and 1", s.unlocks.Load(), s.closes.Load())
	}
	if err := <-r.ran; err != nil {
		t.Errorf("Run = %v", err)
	}
	f, _ := New(cfg, func(context.Context) (Session, error) { return &fakeSession{}, nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := f.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown before Run = %v", err)
	}
}

// P1-50: advisoryKeyParts (bit operations) equals the former unsigned
// conversion split for every class of key, including negative keys.
func TestAdvisoryKeyParts_MatchesTheUnsignedSplit(t *testing.T) {
	t.Parallel()
	keys := []int64{0, 1, -1, 42, 4242, 0x7fffffff, 0x80000000, 0xffffffff, 1 << 32, -(1 << 32),
		-0x80000000, math.MaxInt64, math.MinInt64, math.MinInt64 + 1, 0x123456789abcdef, -0x123456789abcdef}
	for i := int64(0); i < 64; i++ {
		keys = append(keys, int64(1)<<i, -(int64(1) << i))
	}
	for _, k := range keys {
		hi, lo := advisoryKeyParts(k)
		wantHi, wantLo := int64(uint64(k)>>32), int64(uint64(k)&0xffffffff)
		if hi != wantHi || lo != wantLo {
			t.Errorf("advisoryKeyParts(%d) = %d, %d; want %d, %d", k, hi, lo, wantHi, wantLo)
		}
		if hi < 0 || hi > 0xffffffff || lo < 0 || lo > 0xffffffff {
			t.Errorf("advisoryKeyParts(%d) = %d, %d: outside the oid range", k, hi, lo)
		}
	}
}

// Fail-closed readiness through the real platform health handler: not ready
// until owner; not ready again after a loss.
func TestFence_ReadinessIsFalseUnlessOwner(t *testing.T) {
	t.Parallel()
	var fail atomic.Bool
	s := &fakeSession{holds: func(context.Context) (bool, error) {
		if fail.Load() {
			return false, errors.New("gone")
		}
		return true, nil
	}}
	r := newRig(t, nil, s, &fakeSession{tryLock: func(context.Context) (bool, error) { return false, nil }})
	h := platform.NewHealth(platform.ServiceConfig{Name: "telephony-gateway", Version: "test"})
	h.Register(r.f, true, time.Second)
	h.MarkReady() // the service lifecycle marks ready; the fence must still veto
	ready := func() int {
		rec := httptest.NewRecorder()
		h.ReadinessHandler()(rec, httptest.NewRequest(http.MethodGet, "/readyz", http.NoBody))
		return rec.Code
	}
	if ready() != http.StatusServiceUnavailable {
		t.Fatal("ready before the fence ran")
	}
	r.start(t)
	r.c.step(t)
	if ready() != http.StatusServiceUnavailable {
		t.Fatal("ready during the acquisition grace")
	}
	r.c.step(t)
	eventually(t, "readiness", func() bool { return ready() == http.StatusOK })
	fail.Store(true)
	r.c.step(t)
	eventually(t, "readiness to drop", func() bool { return ready() == http.StatusServiceUnavailable })
}

func (r *rig) logText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.logs.String()
}

func TestRedact_WithholdsConnectionDetails(t *testing.T) {
	t.Parallel()
	if got := redact(errors.New("host=db password=x")).Error(); strings.Contains(got, "password") {
		t.Errorf("redact leaked: %s", got)
	}
	if !errors.Is(redact(context.DeadlineExceeded), context.DeadlineExceeded) {
		t.Error("deadline not preserved")
	}
	if _, err := ParseDSN("postgres://u:secret@[bad"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("ParseDSN = %v", err)
	}
}
