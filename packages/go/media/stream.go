package media

import (
	"fmt"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// StreamDirection is which way audio flows.
type StreamDirection string

// The directions.
const (
	// DirectionInbound carries audio from the caller toward the platform.
	DirectionInbound StreamDirection = "inbound"
	// DirectionOutbound carries synthesised audio toward the caller.
	DirectionOutbound StreamDirection = "outbound"
)

// Valid reports whether the direction is declared.
func (d StreamDirection) Valid() bool {
	return d == DirectionInbound || d == DirectionOutbound
}

// String implements fmt.Stringer.
func (d StreamDirection) String() string { return string(d) }

// StreamContext is the immutable description of a stream.
//
// Immutable once the stream exists. Mutable per-stream state lives in [Stream];
// this is what the stream IS. The split is what lets a snapshot be taken
// without locking the parts that never move, and makes "did the format change
// mid-stream" a question with an obvious answer: no, it cannot.
type StreamContext struct {
	// Correlation ties this stream to the call and conversation it serves.
	Correlation CorrelationID

	// Source identifies where frames come from.
	Source SourceID

	// Direction is which way audio flows.
	Direction StreamDirection

	// Format is the audio carried. Immutable — nothing here renegotiates.
	Format AudioFormat

	// Tags classify the stream for routing and policy. Coarse by construction.
	Tags []string
}

const (
	maxTags   = 16
	maxTagLen = 48
)

// Validate checks the context, reporting every problem.
func (c StreamContext) Validate() error {
	var problems []string

	if !c.Source.Valid() {
		problems = append(problems, fmt.Sprintf(
			"context: Source %q must be lowercase alphanumerics, hyphen or underscore — "+
				"it becomes a metric label and a topic segment", c.Source))
	}
	if !c.Direction.Valid() {
		problems = append(problems, fmt.Sprintf(
			"context: Direction %q must be inbound or outbound", c.Direction))
	}
	if err := c.Format.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	if len(c.Tags) > maxTags {
		problems = append(problems, fmt.Sprintf(
			"context: %d tags exceeds the %d cap", len(c.Tags), maxTags))
	}
	for _, t := range c.Tags {
		if t == "" {
			problems = append(problems, "context: empty tag")
			continue
		}
		if len(t) > maxTagLen {
			problems = append(problems, fmt.Sprintf(
				"context: tag %q exceeds %d characters", truncate(t), maxTagLen))
		}
	}

	if len(problems) > 0 {
		return &ConfigError{Problems: problems}
	}
	return nil
}

// Clone returns a deep copy.
func (c StreamContext) Clone() StreamContext {
	out := c
	out.Tags = append([]string(nil), c.Tags...)
	return out
}

// String renders the context.
func (c StreamContext) String() string {
	return fmt.Sprintf("%s from %s %s", c.Direction, c.Source, c.Format)
}

// TransitionRecord is one entry in a stream's history.
type TransitionRecord struct {
	From   StreamState
	To     StreamState
	Reason string
	At     time.Time
	// Seq orders records within one clock tick, so a frozen FakeClock still
	// produces a sequenceable history.
	Seq int
}

// String renders the record.
func (t TransitionRecord) String() string {
	return fmt.Sprintf("#%d %s->%s (%s)", t.Seq, t.From, t.To, t.Reason)
}

// maxHistory bounds a stream's retained transitions.
//
// A stream that pauses and resumes repeatedly would otherwise grow without
// limit, and the stream lives in memory for the whole call.
const maxHistory = 64

// Stream is one media stream.
//
// # Ownership
//
// Owned by exactly one [MediaRegistry] and carries its own lock. Two streams
// share nothing, so a thousand concurrent streams contend only when they land
// on the same registry shard.
//
// # Lock ordering
//
// [runtime.FSM] has its own lock. Transition takes the FSM lock then the stream
// lock, never the reverse — the natural way to write the reverse is to hold the
// stream lock while asking the FSM whether a move is legal, and that deadlocks
// against any hook that reads the stream.
type Stream struct {
	// Immutable identity, safe to read without the lock.
	id        StreamID
	sessionID SessionID
	ctx       StreamContext
	createdAt time.Time

	fsm      *rt.FSM[StreamState]
	clock    rt.Clock
	pipeline *Pipeline
	media    *MediaClock

	mu        sync.RWMutex
	history   []TransitionRecord
	seq       int
	updatedAt time.Time
	// lastFrameAt is when the source last offered a frame.
	//
	// SEPARATE FROM updatedAt ON PURPOSE. updatedAt moves on a state TRANSITION,
	// and a healthy stream carrying audio for an hour transitions exactly never.
	// Stall detection asks "is the source still producing", which is a question
	// about frames, so measuring it against updatedAt declares every long,
	// perfectly healthy stream stalled the moment it exceeds the stall timeout.
	lastFrameAt time.Time
	// startedAt and endedAt bound the active period.
	startedAt time.Time
	endedAt   time.Time
	endReason string
	// resumeCount records recoveries, the number an incident asks for.
	resumeCount int
}

// newStream builds a stream. Callers go through [MediaRegistry.Create].
func newStream(id StreamID, sess SessionID, sc StreamContext, cfg PipelineConfig,
	initial StreamState, clock rt.Clock) (*Stream, error) {
	if err := sc.Validate(); err != nil {
		return nil, err
	}
	if sc.Format != cfg.Format {
		return nil, invariant("INV-MED-4",
			"stream format %s does not match pipeline format %s", sc.Format, cfg.Format)
	}

	fsm, err := newStreamFSM(initial, clock)
	if err != nil {
		return nil, err
	}
	pipe, err := NewPipeline(cfg, clock)
	if err != nil {
		return nil, err
	}

	now := clock.Now()
	return &Stream{
		id: id, sessionID: sess, ctx: sc.Clone(),
		createdAt: now, updatedAt: now, lastFrameAt: now,
		fsm: fsm, clock: clock, pipeline: pipe,
		media: NewMediaClock(clock),
	}, nil
}

// ID returns the stream identifier. Immutable, no lock.
func (s *Stream) ID() StreamID { return s.id }

// SessionID returns this session's identifier. Immutable, no lock.
func (s *Stream) SessionID() SessionID { return s.sessionID }

// Context returns a copy of the stream context.
func (s *Stream) Context() StreamContext { return s.ctx.Clone() }

// CreatedAt returns when the stream was created. Immutable, no lock.
func (s *Stream) CreatedAt() time.Time { return s.createdAt }

// State returns the current state.
func (s *Stream) State() StreamState { return s.fsm.State() }

// Is reports whether the stream is in the given state.
func (s *Stream) Is(state StreamState) bool { return s.fsm.Is(state) }

// Terminal reports whether the stream has concluded.
func (s *Stream) Terminal() bool { return s.fsm.IsTerminal() }

// Pipeline returns the frame pipeline.
func (s *Stream) Pipeline() *Pipeline { return s.pipeline }

// MediaClock returns the stream's media clock.
func (s *Stream) MediaClock() *MediaClock { return s.media }

// CanTransition reports whether a move is currently legal.
func (s *Stream) CanTransition(to StreamState) bool { return s.fsm.CanGo(to) }

// Transition moves the stream, recording the reason.
//
// THE ONLY WAY A STREAM CHANGES STATE. There is no setter, and the FSM refuses
// anything the transition table does not declare.
func (s *Stream) Transition(to StreamState, reason string) error {
	if err := checkReasonCode(reason); err != nil {
		return err
	}

	from, err := s.fsm.To(to)
	if err != nil {
		return &TransitionError{Stream: s.id, From: from, To: to, Reason: reason}
	}

	now := s.clock.Now()

	s.mu.Lock()
	s.seq++
	s.appendHistoryLocked(TransitionRecord{
		From: from, To: to, Reason: reason, At: now, Seq: s.seq})
	s.updatedAt = now

	if to == StateActive && s.startedAt.IsZero() {
		s.startedAt = now
	}
	if to.Terminal() {
		s.endedAt = now
		s.endReason = reason
	}
	s.mu.Unlock()

	// Media-clock bookkeeping follows the state, outside the stream lock so a
	// clock operation cannot deadlock against a concurrent reader.
	switch to {
	case StateActive:
		if !s.media.Started() {
			s.media.Start(0)
		} else {
			s.media.Resume()
		}
	case StatePaused:
		s.media.Pause()
	}

	return nil
}

// appendHistoryLocked adds a record, preserving the first and dropping the
// oldest of the rest. The caller holds the write lock.
func (s *Stream) appendHistoryLocked(rec TransitionRecord) {
	if len(s.history) < maxHistory {
		s.history = append(s.history, rec)
		return
	}
	// Keep index 0 — how the stream began — and slide the rest.
	copy(s.history[1:], s.history[2:])
	s.history[len(s.history)-1] = rec
}

// History returns a copy of the transition history.
//
// COPIES. Use [Stream.HistoryLen] when only the count is needed — Phase 11A
// shipped a hot path that obtained a length this way and paid 12.8 KB per
// event for it.
func (s *Stream) History() []TransitionRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]TransitionRecord(nil), s.history...)
}

// HistoryLen returns how many transitions are retained. O(1), no allocation.
func (s *Stream) HistoryLen() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.history)
}

// LastActivity returns the most recent sign of life: a state change or an
// accepted frame, whichever is later.
//
// The predicate stall detection must use. See [Stream.lastFrameAt].
func (s *Stream) LastActivity() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.lastFrameAt.After(s.updatedAt) {
		return s.lastFrameAt
	}
	return s.updatedAt
}

// UpdatedAt returns when the stream last changed.
func (s *Stream) UpdatedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updatedAt
}

// EndReason returns the code the stream concluded with.
func (s *Stream) EndReason() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.endReason
}

// ResumeCount returns how many times this stream has been recovered.
func (s *Stream) ResumeCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.resumeCount
}

// Duration returns how long the stream has existed.
func (s *Stream) Duration() time.Duration {
	s.mu.RLock()
	end := s.endedAt
	s.mu.RUnlock()
	if end.IsZero() {
		return s.clock.Now().Sub(s.createdAt)
	}
	return end.Sub(s.createdAt)
}

// Write offers a frame to the stream.
//
// Refuses unless the stream is Active. That is the state machine doing its job:
// a paused stream that accepted frames would silently buffer audio the caller
// believed was discarded, and a closing stream that accepted frames would never
// finish draining.
func (s *Stream) Write(f Frame) (PipelineResult, error) {
	state := s.fsm.State()
	if !state.AcceptsFrames() {
		switch state {
		case StatePaused:
			return PipelineResult{Reason: DropNotAccepting}, ErrStreamPaused
		case StateClosed, StateFailed, StateClosing:
			return PipelineResult{Reason: DropNotAccepting}, ErrStreamClosed
		default:
			return PipelineResult{Reason: DropNotAccepting},
				fmt.Errorf("%w: stream is %s", ErrStreamClosed, state)
		}
	}

	res := s.pipeline.Push(f)

	// Recorded whether or not the frame was ACCEPTED. The question stall
	// detection asks is "is the source still there", and a frame refused for
	// backpressure answers it just as well as one that was buffered. Counting
	// only accepted frames means a stalled CONSUMER — which refuses everything —
	// eventually looks like a dead PRODUCER, and the runtime times out a stream
	// whose source was healthy the entire time.
	s.mu.Lock()
	s.lastFrameAt = s.clock.Now()
	s.mu.Unlock()

	if res.Accepted {
		s.media.Observe(f.Duration())
	}
	return res, nil
}

// Read returns the next delivered frame.
//
// THE RETURNED FRAME'S PAYLOAD IS BORROWED from the output ring — see [Frame].
// A caller that retains it must clone.
//
// # Read-through
//
// When the output ring is empty this pumps once and retries. The runtime's pump
// loop is the steady-state driver, but a consumer must never starve merely
// because the pump was late, and a test must be able to read without first
// advancing a clock. Pumping here is bounded — one pass, one retry — so a Read
// on a genuinely empty stream still returns promptly rather than spinning.
func (s *Stream) Read() (Frame, error) {
	if !s.fsm.State().DeliversFrames() {
		return Frame{}, ErrStreamClosed
	}
	f, err := s.pipeline.Buffer().Read()
	if err == nil {
		return f, nil
	}
	if s.pipeline.Pump() > 0 {
		return s.pipeline.Buffer().Read()
	}
	return Frame{}, err
}

// Pump moves due frames from the jitter buffer to the output ring.
func (s *Stream) Pump() int {
	if !s.fsm.State().DeliversFrames() {
		return 0
	}
	return s.pipeline.Pump()
}

// Flush discards buffered audio and returns how many frames went.
func (s *Stream) Flush() int { return s.pipeline.Flush() }

// StreamStats is a consistent view of a stream.
type StreamStats struct {
	ID          StreamID
	Session     SessionID
	State       StreamState
	Direction   StreamDirection
	Source      SourceID
	Format      AudioFormat
	Duration    time.Duration
	ResumeCount int
	Pipeline    PipelineStats
	Clock       ClockStats
}

// String renders the stats.
func (s StreamStats) String() string {
	return fmt.Sprintf("stream %s %s %s dur=%s | %s | %s",
		s.ID, s.State, s.Direction, s.Duration.Round(time.Millisecond),
		s.Pipeline, s.Clock)
}

// Stats returns a consistent snapshot.
func (s *Stream) Stats() StreamStats {
	s.mu.RLock()
	resumes := s.resumeCount
	s.mu.RUnlock()

	return StreamStats{
		ID: s.id, Session: s.sessionID, State: s.fsm.State(),
		Direction: s.ctx.Direction, Source: s.ctx.Source, Format: s.ctx.Format,
		Duration: s.Duration(), ResumeCount: resumes,
		Pipeline: s.pipeline.Stats(), Clock: s.media.Stats(),
	}
}

// ---------------------------------------------------------------------------
// Snapshot and restore
// ---------------------------------------------------------------------------

// StreamSnapshot is a stream captured for durable storage.
//
// # It carries buffered audio, and that is a deliberate exception
//
// Every other snapshot in this platform is content-free — Phase 11A's call
// snapshot carries identifiers and states and nothing a person said. This one
// carries PCM, because the entire point of buffer recovery is not losing the
// audio that was in flight.
//
// That makes a stream snapshot far more sensitive than a call snapshot, and the
// controls are correspondingly different: [SnapshotConfig.IncludeAudio] defaults
// to FALSE, so a deployment must opt in; the audio is bounded; and
// [SECURITY_REVIEW] treats a snapshot store holding these as a recording system
// with the retention obligations that implies.
type StreamSnapshot struct {
	// SchemaVersion is stamped so a rolling deploy can read what the previous
	// version wrote.
	SchemaVersion int

	Stream      StreamID
	Session     SessionID
	Correlation CorrelationID

	State   StreamState
	Context StreamContext

	CreatedAt time.Time
	UpdatedAt time.Time
	EndReason string

	History     []TransitionRecord
	ResumeCount int

	// MediaPosition is the media timeline position, so a resumed stream
	// continues rather than restarting at zero.
	MediaPosition time.Duration

	// Buffered is the in-flight audio, empty unless audio capture was enabled.
	Buffered []Frame
	// BufferedDropped counts frames omitted because the audio bound was hit,
	// so a reader can tell a partial capture from a complete one.
	BufferedDropped int
}

// SnapshotSchemaVersion is the version this build writes.
const SnapshotSchemaVersion = 1

// SnapshotConfig controls what a snapshot captures.
type SnapshotConfig struct {
	// IncludeAudio captures buffered frames.
	//
	// FALSE BY DEFAULT. Audio in a durable store is a recording, with every
	// obligation that implies. A deployment that needs gapless recovery opts in
	// knowingly.
	IncludeAudio bool

	// MaxAudioFrames bounds captured frames when IncludeAudio is set.
	MaxAudioFrames int
}

// DefaultSnapshotConfig returns the conservative default.
func DefaultSnapshotConfig() SnapshotConfig {
	return SnapshotConfig{IncludeAudio: false, MaxAudioFrames: 25}
}

// Snapshot captures the stream.
func (s *Stream) Snapshot(cfg SnapshotConfig) StreamSnapshot {
	state := s.fsm.State()

	s.mu.RLock()
	snap := StreamSnapshot{
		SchemaVersion: SnapshotSchemaVersion,
		Stream:        s.id,
		Session:       s.sessionID,
		Correlation:   s.ctx.Correlation,
		State:         state,
		Context:       s.ctx.Clone(),
		CreatedAt:     s.createdAt,
		UpdatedAt:     s.updatedAt,
		EndReason:     s.endReason,
		History:       append([]TransitionRecord(nil), s.history...),
		ResumeCount:   s.resumeCount,
	}
	s.mu.RUnlock()

	snap.MediaPosition = s.media.MediaTime()

	if cfg.IncludeAudio {
		// BOTH buffering stages, in playout order: the output ring holds what is
		// already due, the jitter buffer what is still being held for reordering.
		// Capturing only the ring — as this once did — routinely captured nothing,
		// because a frame reaches the ring only when pumped, so a snapshot of a
		// live stream saw an empty buffer and recovery restored silence.
		//
		// Ring frames always precede held frames: a frame reaches the ring only
		// by being released from the jitter buffer, and release is strictly in
		// playout order.
		//
		// MEDIA-PCM-1: this is PCM. It is captured only on explicit opt-in, it is
		// bounded, and no store in this phase persists it beyond process memory.
		ring := s.pipeline.Buffer().Snapshot().Frames
		holding := s.pipeline.Jitter().Peek()

		all := make([]Frame, 0, len(ring)+len(holding))
		all = append(all, ring...)
		all = append(all, holding...)

		max := cfg.MaxAudioFrames
		if max <= 0 || max > len(all) {
			max = len(all)
		}
		// The NEWEST frames are kept when the bound binds. Oldest audio is
		// closest to having been played already; the newest is what a resumed
		// consumer still needs.
		start := len(all) - max
		snap.Buffered = all[start:]
		snap.BufferedDropped = start
	}

	return snap
}

// Recoverable reports whether this snapshot can be resumed.
func (s StreamSnapshot) Recoverable() bool {
	if s.SchemaVersion != SnapshotSchemaVersion {
		return false
	}
	if !s.State.Valid() || s.State.Terminal() {
		return false
	}
	return s.Stream.Valid()
}

// RestoreStream reconstructs a stream from a snapshot.
//
// # The restored stream starts in StateRecovering, not its snapshotted state
//
// The same rule Phase 11A applies to calls, for the same reason. The snapshot
// says Active; the process that believed it is gone, and no source is attached.
// A stream restored directly into Active would accept frames from a producer
// that does not exist and report a healthy stream carrying nothing.
//
// StateRecovering says what is true: this stream existed and its source must be
// reattached before it can carry audio.
func RestoreStream(snap StreamSnapshot, cfg PipelineConfig, clock rt.Clock) (*Stream, error) {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	if !snap.Recoverable() {
		return nil, fmt.Errorf("%w: stream %s in state %s at schema v%d",
			ErrNotRecoverable, snap.Stream, snap.State, snap.SchemaVersion)
	}
	if snap.Context.Format != cfg.Format {
		return nil, fmt.Errorf("%w: snapshot is %s, pipeline is %s",
			ErrFormatMismatch, snap.Context.Format, cfg.Format)
	}

	s, err := newStream(snap.Stream, NewSessionID(), snap.Context, cfg,
		StateRecovering, clock)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.createdAt = snap.CreatedAt
	s.history = append([]TransitionRecord(nil), snap.History...)
	if n := len(s.history); n > 0 {
		s.seq = s.history[n-1].Seq
	}
	s.updatedAt = clock.Now()
	s.resumeCount = snap.ResumeCount + 1
	s.mu.Unlock()

	// The media clock resumes at the snapshotted position, so a recovered
	// stream's timeline continues rather than restarting at zero — which would
	// make every timestamp after recovery collide with one from before it.
	s.media.Start(snap.MediaPosition)

	for _, f := range snap.Buffered {
		if err := s.pipeline.Buffer().Write(f); err != nil {
			break
		}
	}

	return s, nil
}
