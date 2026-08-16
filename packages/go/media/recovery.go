package media

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// StreamStore is the durable snapshot port.
//
// NOT BOUND TO A DATABASE. Nothing here mentions Redis, SQL, a connection or a
// driver. A Redis adapter (hot path, short TTL) and an Aurora adapter (audit
// trail) both satisfy this, and the runtime depends only on the interface —
// which is what lets the whole recovery path be tested with no infrastructure.
//
// # A store holding audio is a recording system
//
// Unlike Phase 11A's call snapshots, a [StreamSnapshot] may carry PCM. An
// implementation that persists snapshots with [SnapshotConfig.IncludeAudio] set
// is storing recordings of conversations, with every retention, encryption and
// consent obligation that implies. The default is off for exactly this reason.
type StreamStore interface {
	// Save persists one snapshot, replacing any earlier one for the stream.
	Save(ctx context.Context, snap StreamSnapshot) error

	// SaveBatch persists many.
	SaveBatch(ctx context.Context, snaps []StreamSnapshot) error

	// Load returns one snapshot.
	Load(ctx context.Context, id StreamID) (StreamSnapshot, error)

	// LoadAll returns every stored snapshot. The recovery sweep's entry point.
	LoadAll(ctx context.Context) ([]StreamSnapshot, error)

	// Delete removes a snapshot.
	Delete(ctx context.Context, id StreamID) error

	// Close releases resources. Safe to call more than once.
	Close() error
}

// MemoryStreamStore is an in-process [StreamStore].
//
// The reference implementation and what the tests run against. It keeps the
// runtime runnable with no infrastructure and pins the behaviour a Redis or
// Aurora implementation must reproduce. It is NOT the production store: a
// snapshot store that dies with the process cannot survive the crash it exists
// to survive.
type MemoryStreamStore struct {
	mu       sync.RWMutex
	snaps    map[StreamID]StreamSnapshot
	failNext error
	closed   bool
}

// NewMemoryStreamStore builds an empty store.
func NewMemoryStreamStore() *MemoryStreamStore {
	return &MemoryStreamStore{snaps: make(map[StreamID]StreamSnapshot)}
}

// FailNext makes the next operation return err. For failure injection.
func (s *MemoryStreamStore) FailNext(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext = err
}

func (s *MemoryStreamStore) takeFailure() error {
	if s.closed {
		return fmt.Errorf("media: stream store is closed")
	}
	if s.failNext != nil {
		err := s.failNext
		s.failNext = nil
		return err
	}
	return nil
}

// Save persists a snapshot.
func (s *MemoryStreamStore) Save(ctx context.Context, snap StreamSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.takeFailure(); err != nil {
		return err
	}
	s.snaps[snap.Stream] = snap
	return nil
}

// SaveBatch persists many snapshots.
func (s *MemoryStreamStore) SaveBatch(ctx context.Context, snaps []StreamSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.takeFailure(); err != nil {
		return err
	}
	for _, snap := range snaps {
		s.snaps[snap.Stream] = snap
	}
	return nil
}

// Load returns one snapshot.
func (s *MemoryStreamStore) Load(ctx context.Context, id StreamID) (StreamSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return StreamSnapshot{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap, ok := s.snaps[id]
	if !ok {
		return StreamSnapshot{}, fmt.Errorf("%w: %s", ErrSnapshotNotFound, id)
	}
	return snap, nil
}

// LoadAll returns every snapshot, oldest first.
func (s *MemoryStreamStore) LoadAll(ctx context.Context) ([]StreamSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.failNext != nil {
		return nil, s.failNext
	}

	out := make([]StreamSnapshot, 0, len(s.snaps))
	for _, snap := range s.snaps {
		out = append(out, snap)
	}
	sortSnapshots(out)
	return out, nil
}

// Delete removes a snapshot.
func (s *MemoryStreamStore) Delete(ctx context.Context, id StreamID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.snaps, id)
	return nil
}

// Close marks the store closed. Idempotent.
func (s *MemoryStreamStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// Len returns how many snapshots are held.
func (s *MemoryStreamStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.snaps)
}

// IDs returns the stored stream identifiers, sorted.
func (s *MemoryStreamStore) IDs() []StreamID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]StreamID, 0, len(s.snaps))
	for id := range s.snaps {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// sortSnapshots orders snapshots oldest first, for a deterministic recovery.
//
// Recovery order must not depend on map iteration: two recoveries of the same
// store must produce the same sequence, or an incident cannot be replayed.
func sortSnapshots(snaps []StreamSnapshot) {
	sort.Slice(snaps, func(i, j int) bool {
		if !snaps[i].CreatedAt.Equal(snaps[j].CreatedAt) {
			return snaps[i].CreatedAt.Before(snaps[j].CreatedAt)
		}
		return snaps[i].Stream < snaps[j].Stream
	})
}

// ---------------------------------------------------------------------------
// Recovery
// ---------------------------------------------------------------------------

// RecoveryOutcome is what happened to one recovered stream.
type RecoveryOutcome string

// The recovery outcomes.
const (
	// RecoveryResumed means the stream was restored and its source reattached.
	RecoveryResumed RecoveryOutcome = "resumed"
	// RecoveryConcluded means the stream was restored and then closed, because
	// no source reattached. The common case after a crash.
	RecoveryConcluded RecoveryOutcome = "concluded"
	// RecoveryAbandoned means the snapshot could not be used at all.
	RecoveryAbandoned RecoveryOutcome = "abandoned"
)

// SourceCheck reports whether a stream's source can be reattached.
//
// # This is the part a real deployment must supply
//
// The engine cannot know whether a media source survived a restart. Only the
// carrier adapter knows, and asking it is source-specific — a method on the
// engine would be the first place carrier semantics leaked in.
//
// Returning false for everything is legitimate and conservative: it concludes
// every recovered stream, losing in-flight audio but never reporting a healthy
// stream that is carrying nothing. That second error is worse, because a stream
// that appears active but receives no frames holds a buffer, a capacity slot
// and a metric while a consumer waits forever.
type SourceCheck func(ctx context.Context, snap StreamSnapshot) bool

// AssumeDetached is the default [SourceCheck].
//
// Conservative by design, and named so accepting the default is a visible
// choice rather than an omission.
func AssumeDetached(context.Context, StreamSnapshot) bool { return false }

// AlwaysAttached is a [SourceCheck] reporting every source reattachable.
//
// For testing the resume path. Named so its use in production would be
// obviously wrong.
func AlwaysAttached(context.Context, StreamSnapshot) bool { return true }

// RecoveryReport describes one recovery sweep.
type RecoveryReport struct {
	Attempted int
	Resumed   int
	Concluded int
	Abandoned int
	// FramesRestored counts buffered frames returned to their streams.
	FramesRestored int
	Streams        map[StreamID]RecoveryOutcome
	Took           time.Duration
}

// Summary renders the report.
func (r RecoveryReport) Summary() string {
	return fmt.Sprintf("recovery: %d attempted, %d resumed, %d concluded, %d abandoned, "+
		"%d frames restored in %s",
		r.Attempted, r.Resumed, r.Concluded, r.Abandoned, r.FramesRestored,
		r.Took.Round(time.Millisecond))
}

// WithSourceCheck injects the source reattachment probe used during recovery.
// Defaults to [AssumeDetached].
func WithSourceCheck(f SourceCheck) Option {
	return func(o *options) { o.sourceCheck = f }
}

// recoverStreams rebuilds streams from the store.
//
// Deterministic: snapshots are processed oldest first, so two recoveries of the
// same store produce the same outcome sequence.
func (r *MediaRuntime) recoverStreams(ctx context.Context) (RecoveryReport, error) {
	started := time.Now()
	report := RecoveryReport{Streams: make(map[StreamID]RecoveryOutcome)}

	if r.store == nil {
		return report, nil
	}

	snaps, err := r.store.LoadAll(ctx)
	if err != nil {
		return report, fmt.Errorf("media: recovery could not read the stream store: %w", err)
	}
	sortSnapshots(snaps)
	report.Attempted = len(snaps)

	check := r.sourceCheck
	if check == nil {
		check = AssumeDetached
	}

	for _, snap := range snaps {
		outcome, frames := r.recoverOne(ctx, snap, check)
		report.Streams[snap.Stream] = outcome
		report.FramesRestored += frames

		switch outcome {
		case RecoveryResumed:
			report.Resumed++
		case RecoveryConcluded:
			report.Concluded++
		default:
			report.Abandoned++
		}
		r.metrics.Recoveries.Inc(string(outcome))
	}

	report.Took = time.Since(started)
	return report, nil
}

// recoverOne rebuilds a single stream and decides its fate.
func (r *MediaRuntime) recoverOne(ctx context.Context, snap StreamSnapshot,
	check SourceCheck) (RecoveryOutcome, int) {
	cfg := r.cfg.Pipeline
	cfg.Format = snap.Context.Format
	cfg.Buffer.Format = snap.Context.Format

	stream, err := RestoreStream(snap, cfg, r.clock)
	if err != nil {
		// An unrecoverable snapshot is deleted rather than retried forever: a
		// snapshot at an unreadable schema will never become readable, and
		// leaving it makes every subsequent recovery slower and noisier.
		_ = r.store.Delete(ctx, snap.Stream)
		r.logger.WarnContext(ctx, "abandoning unrecoverable stream",
			slog.String("stream_id", string(snap.Stream)),
			slog.String("state", string(snap.State)),
			slog.String("error", err.Error()))
		return RecoveryAbandoned, 0
	}

	if err := r.registry.Register(stream); err != nil {
		return RecoveryAbandoned, 0
	}

	// A recovered stream must hold a capacity slot, or the runtime under-counts
	// its own load until those streams close.
	r.scheduler.Admit(snap.Context.Source)

	restored := stream.Pipeline().Buffer().Len()

	if check(ctx, snap) {
		if err := r.transition(stream, StateActive, "resumed_after_restart"); err != nil {
			_ = r.transition(stream, StateFailed, "resume_failed")
			r.registry.Remove(stream.ID())
			r.scheduler.Release(snap.Context.Source)
			return RecoveryAbandoned, 0
		}
		_ = r.store.Delete(ctx, snap.Stream)
		return RecoveryResumed, restored
	}

	// No source. Conclude it cleanly so a consumer sees a closed stream rather
	// than one that simply stopped producing frames.
	_ = r.transition(stream, StateClosing, "no_source_after_restart")
	_ = r.transition(stream, StateClosed, "concluded_after_restart")
	r.registry.Remove(stream.ID())
	r.scheduler.Release(snap.Context.Source)
	_ = r.store.Delete(ctx, snap.Stream)
	return RecoveryConcluded, 0
}

// snapshotAll persists every live stream.
//
// Terminal streams are skipped: a snapshot of a closed stream is one recovery
// will immediately discard, and writing it wastes a round trip per stream at
// exactly the moment the process is trying to exit.
func (r *MediaRuntime) snapshotAll(ctx context.Context) (int, error) {
	if r.store == nil {
		return 0, nil
	}

	snaps := make([]StreamSnapshot, 0, r.registry.Len())
	r.registry.Each(func(s *Stream) bool {
		if s.Terminal() {
			return true
		}
		snaps = append(snaps, s.Snapshot(r.cfg.Snapshot))
		return true
	})
	if len(snaps) == 0 {
		return 0, nil
	}
	sortSnapshots(snaps)

	if err := r.store.SaveBatch(ctx, snaps); err != nil {
		return 0, err
	}
	return len(snaps), nil
}

// SnapshotAll persists every live stream. Exported for a scheduled snapshot job.
func (r *MediaRuntime) SnapshotAll(ctx context.Context) (int, error) {
	return r.snapshotAll(ctx)
}
