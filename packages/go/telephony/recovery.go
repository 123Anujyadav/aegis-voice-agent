package telephony

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// SessionStore is the durable session port.
//
// NOT BOUND TO A DATABASE. Nothing here mentions Redis, SQL, a connection or a
// driver. A Redis adapter (hot path, short TTL) and an Aurora adapter (audit
// trail, long retention) both satisfy this, and the runtime depends only on the
// interface — which is what lets the whole recovery path be tested with no
// infrastructure.
//
// Implementations must be safe for concurrent use. Save is called from the
// snapshot ticker and from graceful shutdown, potentially at once.
type SessionStore interface {
	// Save persists one snapshot, replacing any earlier one for the same call.
	Save(ctx context.Context, snap Snapshot) error

	// SaveBatch persists many. Backends with pipelining should use it; the
	// interface does not promise atomicity, because a backend that cannot
	// provide it should not be excluded and should not pretend.
	SaveBatch(ctx context.Context, snaps []Snapshot) error

	// Load returns one snapshot.
	Load(ctx context.Context, id CallID) (Snapshot, error)

	// LoadAll returns every stored snapshot. The recovery sweep's entry point.
	LoadAll(ctx context.Context) ([]Snapshot, error)

	// Delete removes a snapshot. Called when a call concludes normally, so the
	// store holds only calls that might need recovering.
	Delete(ctx context.Context, id CallID) error

	// Close releases resources. Safe to call more than once.
	Close() error
}

// MemorySessionStore is an in-process [SessionStore].
//
// The reference implementation and what the tests run against. It exists to
// keep the runtime runnable with no infrastructure — the property every phase
// since 10A has preserved — and to pin the behaviour a Redis or Aurora
// implementation must reproduce.
//
// It is NOT the production store: a snapshot store that dies with the process
// cannot survive the crash it exists to survive.
type MemorySessionStore struct {
	mu    sync.RWMutex
	snaps map[CallID]Snapshot
	// failNext lets a test inject a store failure without a second type.
	failNext error
	closed   bool
}

// NewMemorySessionStore builds an empty store.
func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{snaps: make(map[CallID]Snapshot)}
}

// FailNext makes the next operation return err. For failure injection.
func (s *MemorySessionStore) FailNext(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext = err
}

func (s *MemorySessionStore) takeFailure() error {
	if s.closed {
		return fmt.Errorf("telephony: session store is closed")
	}
	if s.failNext != nil {
		err := s.failNext
		s.failNext = nil
		return err
	}
	return nil
}

// Save persists a snapshot.
func (s *MemorySessionStore) Save(ctx context.Context, snap Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.takeFailure(); err != nil {
		return err
	}
	s.snaps[snap.Call] = snap
	return nil
}

// SaveBatch persists many snapshots.
func (s *MemorySessionStore) SaveBatch(ctx context.Context, snaps []Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.takeFailure(); err != nil {
		return err
	}
	for _, snap := range snaps {
		s.snaps[snap.Call] = snap
	}
	return nil
}

// Load returns one snapshot.
func (s *MemorySessionStore) Load(ctx context.Context, id CallID) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap, ok := s.snaps[id]
	if !ok {
		return Snapshot{}, fmt.Errorf("%w: %s", ErrSnapshotNotFound, id)
	}
	return snap, nil
}

// LoadAll returns every snapshot, oldest first.
func (s *MemorySessionStore) LoadAll(ctx context.Context) ([]Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.failNext != nil {
		err := s.failNext
		return nil, err
	}

	out := make([]Snapshot, 0, len(s.snaps))
	for _, snap := range s.snaps {
		out = append(out, snap)
	}
	sortSnapshots(out)
	return out, nil
}

// Delete removes a snapshot.
func (s *MemorySessionStore) Delete(ctx context.Context, id CallID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.snaps, id)
	return nil
}

// Close marks the store closed. Idempotent.
func (s *MemorySessionStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// Len returns how many snapshots are held.
func (s *MemorySessionStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.snaps)
}

// IDs returns the stored call identifiers, sorted.
func (s *MemorySessionStore) IDs() []CallID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]CallID, 0, len(s.snaps))
	for id := range s.snaps {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ---------------------------------------------------------------------------
// Recovery
// ---------------------------------------------------------------------------

// RecoveryOutcome is what happened to one recovered session.
type RecoveryOutcome string

// The recovery outcomes.
const (
	// RecoveryResumed means the call was verified live and resumed.
	RecoveryResumed RecoveryOutcome = "resumed"
	// RecoveryConcluded means the call was no longer live and was ended
	// cleanly. The COMMON case after a crash of any duration.
	RecoveryConcluded RecoveryOutcome = "concluded"
	// RecoveryAbandoned means the snapshot could not be used at all.
	RecoveryAbandoned RecoveryOutcome = "abandoned"
)

// RecoveryReport describes one recovery sweep.
type RecoveryReport struct {
	// Attempted is how many snapshots were considered.
	Attempted int
	// Resumed, Concluded and Abandoned tally the outcomes.
	Resumed   int
	Concluded int
	Abandoned int
	// Calls maps each call to its outcome.
	Calls map[CallID]RecoveryOutcome
	// Took is the wall time.
	Took time.Duration
}

// Summary renders the report.
func (r RecoveryReport) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "recovery: %d attempted, %d resumed, %d concluded, %d abandoned in %s",
		r.Attempted, r.Resumed, r.Concluded, r.Abandoned, r.Took.Round(time.Millisecond))
	return b.String()
}

// LivenessCheck reports whether a call is still up at the provider.
//
// # This is the part a real deployment must supply
//
// The runtime cannot know whether a call survived a crash. Only the carrier
// knows, and asking it is a provider-specific operation that [Provider]
// deliberately does not expose — a "is this call still up" method would be the
// first place carrier-specific semantics leaked into this module.
//
// A deployment supplies this. Returning false for everything is a legitimate
// and conservative choice: it concludes every recovered call, which loses
// in-progress calls but never resurrects a dead one. Resurrecting a dead call
// is the worse error, because the runtime then holds a session, a capacity slot
// and a metric for a call nobody is on.
type LivenessCheck func(ctx context.Context, snap Snapshot) bool

// AssumeDead is the default [LivenessCheck].
//
// Conservative by design, and named so that accepting the default is a visible
// choice rather than an omission. See [LivenessCheck].
func AssumeDead(context.Context, Snapshot) bool { return false }

// recoverSessions rebuilds sessions from a store.
//
// Deterministic: snapshots are processed oldest first, so two recoveries of the
// same store produce the same event sequence. A recovery whose order depended
// on map iteration could not be tested and could not be replayed during an
// incident.
func (r *TelephonyRuntime) recoverSessions(ctx context.Context) (RecoveryReport, error) {
	started := time.Now()
	report := RecoveryReport{Calls: make(map[CallID]RecoveryOutcome)}

	if r.store == nil {
		return report, nil
	}

	snaps, err := r.store.LoadAll(ctx)
	if err != nil {
		return report, fmt.Errorf("telephony: recovery could not read the session store: %w", err)
	}
	sortSnapshots(snaps)
	report.Attempted = len(snaps)

	for _, snap := range snaps {
		outcome := r.recoverOne(ctx, snap)
		report.Calls[snap.Call] = outcome

		switch outcome {
		case RecoveryResumed:
			report.Resumed++
		case RecoveryConcluded:
			report.Concluded++
		default:
			report.Abandoned++
		}
		r.metrics.RecoveryAttempts.Inc(string(outcome))
	}

	report.Took = time.Since(started)
	r.metrics.RecoveryDuration.ObserveDuration(report.Took)
	return report, nil
}

// recoverOne rebuilds a single session and decides its fate.
func (r *TelephonyRuntime) recoverOne(ctx context.Context, snap Snapshot) RecoveryOutcome {
	sess, err := Restore(snap, r.clock)
	if err != nil {
		// An unrecoverable snapshot is deleted rather than retried forever. A
		// snapshot at an unreadable schema will never become readable, and
		// leaving it makes every subsequent recovery slower and noisier.
		_ = r.store.Delete(ctx, snap.Call)
		r.metrics.SnapshotsRestored.Inc("abandoned")
		r.logger.WarnContext(ctx, "abandoning unrecoverable session",
			"call_id", string(snap.Call), "state", string(snap.State),
			"schema", snap.SchemaVersion, "error", err.Error())
		return RecoveryAbandoned
	}

	if err := r.registry.Register(sess); err != nil {
		r.metrics.SnapshotsRestored.Inc("abandoned")
		return RecoveryAbandoned
	}
	r.metrics.SnapshotsRestored.Inc("ok")

	r.lifecycle.publish(ctx, Event{
		Type: EventRecoveryStarted,
		Call: sess.ID(), Session: sess.SessionID(), Correlation: sess.Correlation(),
		To: StateRecovery, Reason: "process_restart",
		Direction: snap.Context.Direction, Channel: snap.Context.Channel,
		Provider: snap.Context.Provider, At: r.clock.Now(),
	})

	// The scheduler slot must be reserved for a resumed call, or the runtime
	// under-counts its own load until those calls end.
	r.scheduler.Admit(snap.Context.Provider)

	if r.liveness(ctx, snap) {
		if err := r.lifecycle.transition(ctx, sess, StateConnected, "resumed_after_restart"); err != nil {
			_ = r.lifecycle.end(ctx, sess, StateFailed, "resume_failed")
			return RecoveryAbandoned
		}
		_ = r.store.Delete(ctx, snap.Call)
		r.lifecycle.publish(ctx, Event{
			Type: EventRecoveryResumed,
			Call: sess.ID(), Session: sess.SessionID(), Correlation: sess.Correlation(),
			From: StateRecovery, To: StateConnected, Reason: "resumed_after_restart",
			Direction: snap.Context.Direction, Provider: snap.Context.Provider,
			At: r.clock.Now(),
		})
		return RecoveryResumed
	}

	// Not live. End it cleanly so downstream consumers see a terminal event
	// rather than a call that simply stopped producing them.
	_ = r.lifecycle.end(ctx, sess, StateEnded, "concluded_after_restart")
	_ = r.store.Delete(ctx, snap.Call)
	r.lifecycle.publish(ctx, Event{
		Type: EventRecoveryAbandoned,
		Call: sess.ID(), Session: sess.SessionID(), Correlation: sess.Correlation(),
		From: StateRecovery, To: StateEnded, Reason: "not_live_after_restart",
		Direction: snap.Context.Direction, Provider: snap.Context.Provider,
		At: r.clock.Now(),
	})
	return RecoveryConcluded
}

// snapshotAll persists every live session.
//
// Called by the snapshot ticker and by graceful shutdown. Terminal sessions are
// skipped: a snapshot of a call that has ended is a snapshot that recovery will
// immediately discard, and writing it wastes a round trip per call at exactly
// the moment the process is trying to exit.
func (r *TelephonyRuntime) snapshotAll(ctx context.Context) (int, error) {
	if r.store == nil {
		return 0, nil
	}

	snaps := make([]Snapshot, 0, r.registry.Len())
	r.registry.Each(func(s *CallSession) bool {
		if s.Terminal() {
			return true
		}
		snaps = append(snaps, s.Snapshot())
		return true
	})
	if len(snaps) == 0 {
		return 0, nil
	}
	sortSnapshots(snaps)

	if err := r.store.SaveBatch(ctx, snaps); err != nil {
		r.metrics.SnapshotsWritten.Inc("error")
		return 0, err
	}
	for range snaps {
		r.metrics.SnapshotsWritten.Inc("ok")
	}
	return len(snaps), nil
}

// ensureClock returns a usable clock.
func ensureClock(c rt.Clock) rt.Clock {
	if c == nil {
		return rt.SystemClock{}
	}
	return c
}
