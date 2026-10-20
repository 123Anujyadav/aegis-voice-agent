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

	"github.com/callscreen/callscreen-platform/services/go/telephony-gateway/internal/fence"
)

// The fence's ownership-loss lines, as internal/fence/fence.go writes them
// (its TestEvents_ClassifyTheFencesOwnLosses pins the same wording). A loss
// this file does not recognise is reported, never excused.
const (
	msgFenceLost = "fence: ownership LOST; no longer accepting work"
	msgFenceLate = "fence: confirmation arrived after the validity window; ownership dropped"
	// The network category of redact, as internal/fence/fence.go defines it.
	errFenceNetwork = "network error reaching the database (details withheld)"
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
// window, or a heartbeat that ran into its timeout: exactly the context's
// error or, when the driver was blocked on the socket, exactly the network
// category after the full timeout. Never a missing lock, never a server error
// (an SQLSTATE), never a network error before the timeout.
func (l fenceLoss) failClosedTiming(timeout time.Duration) bool {
	switch {
	case l.msg == msgFenceLate:
		return true
	case l.msg != msgFenceLost || l.lockMissing:
		return false
	}
	return l.err == context.DeadlineExceeded.Error() || (l.err == errFenceNetwork && l.heartbeat >= timeout)
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
					if p.Key == fence.PhaseHeartbeat && p.Value.Kind() == slog.KindDuration {
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

// requireFailClosedTimingOnly fails the test for any of the losses that is not
// a fail-closed timing loss, and logs each one it excuses, so a liveness gap
// under load is reported as one and never mistaken for an overlap.
func requireFailClosedTimingOnly(t *testing.T, who string, losses []fenceLoss, timeout time.Duration) {
	t.Helper()
	for _, l := range losses {
		if l.failClosedTiming(timeout) {
			t.Logf("%s: fail-closed timing loss (liveness under load, not an overlap): %s", who, l)
			continue
		}
		t.Errorf("%s: an ownership loss that is not a fail-closed timing loss: %s", who, l)
	}
}

// The constants and attribute keys above must be the fence's own; if fence.go
// changes one, this fails instead of the losses being misread.
func TestFenceLossMessagesMatchTheFence(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "fence", "fence.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{
		strconv.Quote(msgFenceLost), strconv.Quote(msgFenceLate), strconv.Quote(errFenceNetwork),
		`slog.Bool("lock_missing"`, `slog.String("error"`,
	} {
		if !strings.Contains(string(src), m) {
			t.Errorf("internal/fence/fence.go no longer contains %s", m)
		}
	}
}
