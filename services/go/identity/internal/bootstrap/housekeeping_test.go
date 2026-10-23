package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/pgstore"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeHousekeeper returns scripted results; block makes an iteration wait for
// its context.
type fakeHousekeeper struct {
	calls   atomic.Int32
	fail    func(n int32) error
	block   bool
	sawDone atomic.Bool
}

func (f *fakeHousekeeper) RunOnce(ctx context.Context) (pgstore.HousekeepingResult, error) {
	n := f.calls.Add(1)
	if f.block {
		<-ctx.Done()
		f.sawDone.Store(true)
		return pgstore.HousekeepingResult{}, ctx.Err()
	}
	if f.fail != nil {
		if err := f.fail(n); err != nil {
			return pgstore.HousekeepingResult{}, err
		}
	}
	return pgstore.HousekeepingResult{Challenges: 1}, nil
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func startRunner(t *testing.T, r *HousekeepingRunner) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = r.Shutdown(context.Background())
	})
	return cancel, done
}

// It runs at once and then on every tick.
func TestHousekeepingRunner_RunsImmediatelyAndRepeatedly(t *testing.T) {
	f := &fakeHousekeeper{}
	r := NewHousekeepingRunner(f, 5*time.Millisecond, slog.New(slog.NewTextHandler(&syncBuffer{}, nil)))
	startRunner(t, r)
	waitFor(t, "three iterations", func() bool { return f.calls.Load() >= 3 })
}

// A failure is retried on the next tick, never stops the loop, is logged by
// category only and rate-limited, and readiness reflects consecutive failures.
func TestHousekeepingRunner_FailuresAreRetriedAndReported(t *testing.T) {
	var logs syncBuffer
	const secretish = "+14155552671 password=hunter2 refresh=0f1e2d"
	var allowSuccess atomic.Bool
	f := &fakeHousekeeper{fail: func(int32) error {
		if allowSuccess.Load() {
			return nil
		}
		return errors.New(secretish)
	}}
	r := NewHousekeepingRunner(f, 2*time.Millisecond, slog.New(slog.NewTextHandler(&logs, nil)))
	if err := r.Check(context.Background()); err != nil {
		t.Fatalf("healthy before any iteration: %v", err)
	}
	startRunner(t, r)

	waitFor(t, "12 failed iterations", func() bool { return f.calls.Load() >= 12 })
	if err := r.Check(context.Background()); err == nil || err.Error() != "identity housekeeping failing" {
		t.Errorf("after repeated failures: Check = %v, want the fixed message", err)
	}
	allowSuccess.Store(true)
	waitFor(t, "recovery", func() bool { return r.Check(context.Background()) == nil })
	waitFor(t, "the recovery log", func() bool { return strings.Contains(logs.String(), "recovered") })

	out := logs.String()
	for _, s := range []string{"+14155552671", "hunter2", "0f1e2d", secretish} {
		if strings.Contains(out, s) {
			t.Errorf("the log carries %q", s)
		}
	}
	if !strings.Contains(out, "cause=other") {
		t.Errorf("the failure category is missing: %s", out)
	}
	// First failure, then every 10th: for >= 12 failures, 2 or 3 lines — not one per failure.
	if n := strings.Count(out, "iteration failed"); n < 2 || n > int(f.calls.Load())/housekeepingLogEvery+1 {
		t.Errorf("%d failure log lines for %d iterations: not rate-limited", n, f.calls.Load())
	}
}

// Shutdown cancels the iteration in progress and waits for the loop; the
// cancellation is not counted as a failure.
func TestHousekeepingRunner_ShutdownCancelsTheIterationInProgress(t *testing.T) {
	f := &fakeHousekeeper{block: true}
	r := NewHousekeepingRunner(f, time.Hour, slog.New(slog.NewTextHandler(&syncBuffer{}, nil)))
	_, done := startRunner(t, r)
	waitFor(t, "the iteration to start", func() bool { return f.calls.Load() == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !f.sawDone.Load() {
		t.Error("the in-progress iteration was not cancelled")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if err := r.Check(context.Background()); err != nil {
		t.Errorf("shutdown counted as a failure: %v", err)
	}
}

func TestHousekeepingRunner_StopsWhenItsContextEnds(t *testing.T) {
	f := &fakeHousekeeper{}
	r := NewHousekeepingRunner(f, time.Hour, slog.New(slog.NewTextHandler(&syncBuffer{}, nil)))
	cancel, done := startRunner(t, r)
	waitFor(t, "the first iteration", func() bool { return f.calls.Load() == 1 })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestHousekeepingRunner_ShutdownBeforeRunIsANoop(t *testing.T) {
	r := NewHousekeepingRunner(&fakeHousekeeper{}, time.Hour, slog.New(slog.NewTextHandler(&syncBuffer{}, nil)))
	if err := r.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown = %v", err)
	}
}

// An iteration that ignores cancellation cannot hold shutdown past its
// deadline.
type stuckHousekeeper struct{ release chan struct{} }

func (s stuckHousekeeper) RunOnce(context.Context) (pgstore.HousekeepingResult, error) {
	<-s.release
	return pgstore.HousekeepingResult{}, nil
}

func TestHousekeepingRunner_ShutdownHonoursItsDeadline(t *testing.T) {
	s := stuckHousekeeper{release: make(chan struct{})}
	r := NewHousekeepingRunner(s, time.Hour, slog.New(slog.NewTextHandler(&syncBuffer{}, nil)))
	ctx, cancelRun := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = r.Run(ctx); close(done) }()
	time.Sleep(20 * time.Millisecond)
	sctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := r.Shutdown(sctx); err == nil {
		t.Error("Shutdown reported success while the iteration was still running")
	}
	close(s.release) // let the goroutine finish: no leak past the test
	cancelRun()
	<-done
}

func TestFailureCategory_IsFixedText(t *testing.T) {
	for err, want := range map[error]string{
		fmt.Errorf("x: %w", pgstore.ErrConnect):                            "connection",
		fmt.Errorf("x: %w", context.DeadlineExceeded):                      "timeout",
		fmt.Errorf("x: %w", &pgconn.PgError{Code: "23503", Message: "+1"}): "sqlstate 23503",
		errors.New("+14155552671"):                                         "other",
	} {
		if got := failureCategory(err); got != want {
			t.Errorf("failureCategory(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestHousekeepingConfig_Validate(t *testing.T) {
	ok := pgstore.HousekeepingConfig{BatchSize: 500, Margin: time.Hour, RateWindow: 15 * time.Minute, IterationTimeout: 30 * time.Second}
	if err := ok.Validate(); err != nil {
		t.Fatalf("defaults refused: %v", err)
	}
	for name, mutate := range map[string]func(*pgstore.HousekeepingConfig){
		"zero batch":   func(c *pgstore.HousekeepingConfig) { c.BatchSize = 0 },
		"huge batch":   func(c *pgstore.HousekeepingConfig) { c.BatchSize = 10_001 },
		"tiny margin":  func(c *pgstore.HousekeepingConfig) { c.Margin = 4 * time.Minute },
		"no window":    func(c *pgstore.HousekeepingConfig) { c.RateWindow = 0 },
		"no timeout":   func(c *pgstore.HousekeepingConfig) { c.IterationTimeout = 0 },
		"long timeout": func(c *pgstore.HousekeepingConfig) { c.IterationTimeout = 6 * time.Minute },
	} {
		c := ok
		mutate(&c)
		if c.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := pgstore.NewHousekeeper(nil, ok, nil); err == nil {
		t.Error("a nil pool was accepted")
	}
}
