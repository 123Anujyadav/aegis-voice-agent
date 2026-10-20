package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fence's ownership-loss lines, as internal/fence/fence.go writes them
// (its TestEvents_ClassifyTheFencesOwnLosses pins the same wording). A loss
// this file does not recognise is reported, never excused.
const (
	msgFenceLost = "fence: ownership LOST; no longer accepting work"
	msgFenceLate = "fence: confirmation arrived after the validity window; ownership dropped"
)

// fenceLoss is one ownership loss a gateway's fence logged.
type fenceLoss struct {
	at          time.Time
	msg         string
	err         string
	lockMissing bool
	heartbeat   time.Duration // the heartbeat's round trip, when it ran one
}

// failClosedTiming reports whether the fence gave ownership up because a
// confirmation did not complete in time: correct behaviour under load, a
// liveness gap and not an overlap. That is an answer after the validity
// window, or a heartbeat that ran into its timeout (failing as the context's
// error or, when the driver was blocked on the socket, as a network error after
// the full timeout). A missing lock, or an error before the timeout, is not.
func (l fenceLoss) failClosedTiming(timeout time.Duration) bool {
	return l.msg == msgFenceLate ||
		(l.msg == msgFenceLost && !l.lockMissing &&
			(strings.Contains(l.err, context.DeadlineExceeded.Error()) || l.heartbeat >= timeout))
}

func (l fenceLoss) String() string {
	return fmt.Sprintf("%s %q error=%q lock_missing=%v heartbeat=%s", l.at.Format("15:04:05.000"), l.msg, l.err, l.lockMissing, l.heartbeat)
}

// losses is a slog.Handler, handed to WireFence, that keeps the fence's INFO
// and worse records in memory: cheap enough not to change the timing it
// explains. Only the ownership losses are classified; the rest (failed
// attempts, acquisitions) are there to explain a failure.
type losses struct {
	mu   sync.Mutex
	recs []fenceLoss
}

func (*losses) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelInfo }

func (s *losses) Handle(_ context.Context, r slog.Record) error {
	if !strings.HasPrefix(r.Message, "fence: ") {
		return nil
	}
	l := fenceLoss{at: r.Time, msg: r.Message}
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "error":
			l.err = a.Value.String()
		case "lock_missing":
			l.lockMissing = a.Value.Kind() == slog.KindBool && a.Value.Bool()
		case "phase":
			if a.Value.Kind() == slog.KindGroup {
				for _, p := range a.Value.Group() {
					if p.Key == "heartbeat" && p.Value.Kind() == slog.KindDuration {
						l.heartbeat = p.Value.Duration()
					}
				}
			}
		}
		return true
	})
	s.mu.Lock()
	s.recs = append(s.recs, l)
	s.mu.Unlock()
	return nil
}

func (s *losses) WithAttrs([]slog.Attr) slog.Handler { return s }
func (s *losses) WithGroup(string) slog.Handler      { return s }

// all returns the ownership losses.
func (s *losses) all() []fenceLoss {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []fenceLoss
	for _, l := range s.recs {
		if l.msg == msgFenceLost || l.msg == msgFenceLate {
			out = append(out, l)
		}
	}
	return out
}

// records returns every fence record kept, for a failure's diagnosis.
func (s *losses) records() []fenceLoss {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]fenceLoss(nil), s.recs...)
}

// requireFailClosedTimingOnly fails the test if the gateway's fence lost
// ownership other than by a fail-closed timing loss more than caused times
// (the losses the test itself provoked, such as killing the session), and
// logs each timing loss it excuses, so a liveness gap under load is reported
// as one and never mistaken for an overlap.
func requireFailClosedTimingOnly(t *testing.T, who string, s *losses, caused int, timeout time.Duration) {
	t.Helper()
	var other []fenceLoss
	for _, l := range s.all() {
		if l.failClosedTiming(timeout) {
			t.Logf("%s: fail-closed timing loss (liveness under load, not an overlap): %s", who, l)
			continue
		}
		other = append(other, l)
	}
	if len(other) > caused {
		t.Errorf("%s: %d ownership loss(es) that are not fail-closed timing losses, %d caused by the test: %v", who, len(other), caused, other)
	}
}

// The constants above must be the fence's own wording; if fence.go changes it,
// this fails instead of the losses going unrecognised.
func TestFenceLossMessagesMatchTheFence(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "fence", "fence.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{msgFenceLost, msgFenceLate} {
		if !strings.Contains(string(src), strconv.Quote(m)) {
			t.Errorf("internal/fence/fence.go no longer logs %q", m)
		}
	}
}
