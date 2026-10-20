package fence

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fence's own WARN and ERROR lines, as fence.go writes them. A test that
// classifies a loss by these fails closed if the wording changes: a loss it
// does not recognise is reported, never excused.
const (
	msgLost       = "fence: ownership LOST; no longer accepting work"
	msgLate       = "fence: confirmation arrived after the validity window; ownership dropped"
	msgCannotOpen = "fence: cannot open a database session"
	msgLockFailed = "fence: lock attempt failed"
	msgNotPrimary = "fence: the database is not a writable primary; refusing to take the lock"
	msgConfirmed  = "fence: ownership confirmed"
)

// fenceEvent is one record the fence logged.
type fenceEvent struct {
	at          time.Time
	msg         string
	err         string
	lockMissing bool
	heartbeat   time.Duration // the heartbeat's round trip, when it ran one
}

// loss reports whether the event ended an ownership.
func (e fenceEvent) loss() bool { return e.msg == msgLost || e.msg == msgLate }

// failClosedTiming reports whether the event is a loss the fence took because
// a confirmation did not complete in time: the heartbeat ran into its timeout,
// or its answer came back after the validity window. Under -race on a loaded
// host that is correct behaviour, a liveness gap and not an overlap. A heartbeat
// stopped by its deadline can fail as the context's error or, when the driver
// was blocked reading the socket, as a network error after the full timeout;
// both count. Any other loss (the lock reported missing, an error that came
// back before the timeout) is not.
func (e fenceEvent) failClosedTiming(timeout time.Duration) bool {
	switch e.msg {
	case msgLate:
		return true
	case msgLost:
		return !e.lockMissing &&
			(strings.Contains(e.err, context.DeadlineExceeded.Error()) || e.heartbeat >= timeout)
	}
	return false
}

// confirmed reports whether the event is a successful heartbeat.
func (e fenceEvent) confirmed() bool { return e.msg == msgConfirmed }

// attemptFailed reports whether the event ended a connection attempt before
// it could own. Each attempt dials once, so it asks a credential source once.
func (e fenceEvent) attemptFailed() bool {
	return e.msg == msgCannotOpen || e.msg == msgLockFailed || e.msg == msgNotPrimary
}

func (e fenceEvent) String() string {
	return fmt.Sprintf("%s %q error=%q lock_missing=%v heartbeat=%s", e.at.Format("15:04:05.000"), e.msg, e.err, e.lockMissing, e.heartbeat)
}

// events is a slog.Handler that keeps the fence's records, heartbeats included,
// in memory. It is cheap enough not to change the timing it is there to explain.
type events struct {
	mu   sync.Mutex
	recs []fenceEvent
}

func (*events) Enabled(context.Context, slog.Level) bool { return true }

func (e *events) Handle(_ context.Context, r slog.Record) error {
	ev := fenceEvent{at: r.Time, msg: r.Message}
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "error":
			ev.err = a.Value.String()
		case "lock_missing":
			ev.lockMissing = a.Value.Kind() == slog.KindBool && a.Value.Bool()
		case "phase":
			if a.Value.Kind() == slog.KindGroup {
				for _, p := range a.Value.Group() {
					if p.Key == PhaseHeartbeat && p.Value.Kind() == slog.KindDuration {
						ev.heartbeat = p.Value.Duration()
					}
				}
			}
		}
		return true
	})
	e.mu.Lock()
	e.recs = append(e.recs, ev)
	e.mu.Unlock()
	return nil
}

func (e *events) WithAttrs([]slog.Attr) slog.Handler { return e }
func (e *events) WithGroup(string) slog.Handler      { return e }

func (e *events) all() []fenceEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]fenceEvent(nil), e.recs...)
}

func (e *events) count(match func(fenceEvent) bool) int {
	n := 0
	for _, ev := range e.all() {
		if match(ev) {
			n++
		}
	}
	return n
}

// requireFailClosedTimingOnly fails the test for any ownership loss that is not
// a fail-closed timing loss, and logs each one it excuses, so a liveness gap
// under load is reported as one and never mistaken for an overlap.
func requireFailClosedTimingOnly(t *testing.T, who string, e *events, timeout time.Duration) {
	t.Helper()
	for _, ev := range e.all() {
		switch {
		case !ev.loss():
		case ev.failClosedTiming(timeout):
			t.Logf("%s: fail-closed timing loss (liveness under load, not an overlap): %s", who, ev)
		default:
			t.Errorf("%s: an ownership loss that is not a fail-closed timing loss: %s", who, ev)
		}
	}
}

// The classification is checked against the fence's real log output, so a
// change to its wording or attributes breaks this test rather than silently
// excusing a loss in the real-PostgreSQL suites.
func TestEvents_ClassifyTheFencesOwnLosses(t *testing.T) {
	blockUntilDeadline := func(ctx context.Context) (bool, error) { <-ctx.Done(); return false, ctx.Err() }
	// What pgx returns when the deadline interrupts a blocked socket read.
	socketTimeout := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	for name, tc := range map[string]struct {
		holds  func(context.Context) (bool, error)
		late   bool
		timing bool
	}{
		"the heartbeat reached its timeout": {holds: blockUntilDeadline, timing: true},
		"a socket read stopped by the timeout": {holds: func(ctx context.Context) (bool, error) {
			<-ctx.Done()
			return false, socketTimeout
		}, timing: true},
		"the answer came after the validity window": {late: true, timing: true},
		"the lock was reported missing":             {holds: func(context.Context) (bool, error) { return false, nil }},
		"a network error before the timeout":        {holds: func(context.Context) (bool, error) { return false, socketTimeout }},
		"the server ended the session": {holds: func(context.Context) (bool, error) {
			return false, errors.New("FATAL: terminating connection due to administrator command (SQLSTATE 57P01)")
		}},
	} {
		t.Run(name, func(t *testing.T) {
			c := newClock()
			ev := &events{}
			cf := Config{LockKey: 4242, HeartbeatInterval: 10 * time.Second, HeartbeatTimeout: 20 * time.Millisecond}
			f, err := New(cf, func(context.Context) (Session, error) { return &fakeSession{holds: tc.holds}, nil }, slog.New(ev))
			if err != nil {
				t.Fatal(err)
			}
			f.now, f.sleep = c.now, c.sleep
			go func() { _ = f.Run(context.Background()) }()
			t.Cleanup(func() { _ = f.Shutdown(context.Background()) })
			// The pause before the first heartbeat; the fence has acquired by then.
			select {
			case <-c.sleeping:
			case <-time.After(5 * time.Second):
				t.Fatal("the fence never paused after acquiring")
			}
			if tc.late {
				// The heartbeat is then sent a full validity window after the
				// acquisition's confirmation.
				c.advance(cf.HeartbeatTimeout)
			}
			c.wake <- struct{}{}
			eventually(t, "a loss", func() bool { return ev.count(fenceEvent.loss) > 0 })
			for _, got := range ev.all() {
				if got.loss() && got.failClosedTiming(cf.HeartbeatTimeout) != tc.timing {
					t.Errorf("failClosedTiming() = %v, want %v for %s", !tc.timing, tc.timing, got)
				}
			}
		})
	}
}

// A dial that fails is a failed attempt, never a loss.
func TestEvents_AFailedDialIsAnAttemptNotALoss(t *testing.T) {
	c := newClock()
	ev := &events{}
	f, err := New(cfg, func(context.Context) (Session, error) { return nil, errors.New("test: refused") }, slog.New(ev))
	if err != nil {
		t.Fatal(err)
	}
	f.now, f.sleep = c.now, c.sleep
	go func() { _ = f.Run(context.Background()) }()
	t.Cleanup(func() { _ = f.Shutdown(context.Background()) })
	eventually(t, "a failed attempt", func() bool { return ev.count(fenceEvent.attemptFailed) > 0 })
	if n := ev.count(fenceEvent.loss); n != 0 {
		t.Errorf("losses = %d, want 0", n)
	}
}
