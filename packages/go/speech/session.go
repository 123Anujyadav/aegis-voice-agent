package speech

import (
	"context"
	"fmt"
	"sync"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// SessionContext is the immutable description of a speech session.
type SessionContext struct {
	// Correlation ties this session to the call and conversation it serves.
	//
	// AN OPAQUE REFERENCE. Not a call object, not a conversation handle — this
	// package does not know what either of those is.
	Correlation string

	// Language is the expected language. Providers may report otherwise.
	Language Language

	// Format is the audio carried in both directions.
	Format media.AudioFormat

	// Voice and Prosody configure synthesis.
	Voice   VoiceID
	Prosody Prosody
}

// Validate checks the context.
func (c SessionContext) Validate() error {
	var problems []string
	if len(c.Correlation) > 128 {
		problems = append(problems, "session: Correlation exceeds 128 characters")
	}
	if err := c.Format.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	if err := c.Prosody.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	if len(problems) > 0 {
		return &ConfigError{Problems: problems}
	}
	return nil
}

// InterruptResult describes one barge-in.
type InterruptResult struct {
	// PreviousTurn is the turn that was interrupted.
	PreviousTurn TurnID

	// NewTurn is the turn opened for the caller's incoming speech.
	NewTurn TurnID

	// ChunksDropped is how many text chunks never reached the synthesiser.
	ChunksDropped int

	// At stamps the moment the interruption was accepted.
	At time.Time

	// Latency is how long the whole interruption took.
	//
	// ADR-0011 budgets 20 ms — one frame interval — from the interruption
	// signal to silence. This is the measurement of that.
	Latency time.Duration
}

// String renders the result.
func (r InterruptResult) String() string {
	return fmt.Sprintf("interrupt %s->%s dropped=%d in %s",
		r.PreviousTurn, r.NewTurn, r.ChunksDropped, r.Latency)
}

// SpeechSession is one call's speech pipeline.
//
// # It owns a turn manager, an assembler, and two orchestrators
//
// Everything a session owns is scoped to it. Two sessions share no state, which
// is what makes cross-session contamination structurally impossible rather than
// merely unlikely: the assembler refuses foreign segments, and no channel,
// queue or provider stream is reachable from another session.
type SpeechSession struct {
	id      SessionID
	ctx     SessionContext
	clock   rt.Clock
	metrics *SpeechMetrics
	events  EventPublisher

	turns     *SpeechTurnManager
	assembler *TranscriptAssembler
	stt       *STTOrchestrator
	tts       *TTSOrchestrator

	cancel context.CancelFunc
	done   chan struct{}
	// onClose deregisters the session from its runtime.
	//
	// WITHOUT THIS, CLOSING A SESSION LEAKS ITS REGISTRY ENTRY. A caller that
	// closes sessions directly — the obvious thing to do — would accumulate
	// entries until the runtime refused new sessions for capacity, on a process
	// that was in fact idle.
	onClose func(SessionID)

	mu         sync.RWMutex
	closed     bool
	createdAt  time.Time
	interrupts int
	seq        int
}

// ID returns the session identifier.
func (s *SpeechSession) ID() SessionID { return s.id }

// Context returns the session context.
func (s *SpeechSession) Context() SessionContext { return s.ctx }

// Turns returns the turn manager.
func (s *SpeechSession) Turns() *SpeechTurnManager { return s.turns }

// Transcript returns the assembler.
func (s *SpeechSession) Transcript() *TranscriptAssembler { return s.assembler }

// Frames yields outbound synthesised audio.
func (s *SpeechSession) Frames() <-chan media.Frame { return s.tts.Frames() }

// Segments yields inbound transcript segments.
func (s *SpeechSession) Segments() <-chan TranscriptSegment { return s.stt.Segments() }

// Closed reports whether the session has ended.
func (s *SpeechSession) Closed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}

// Interrupts returns how many barge-ins this session has handled.
func (s *SpeechSession) Interrupts() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.interrupts
}

// Listen opens a turn and starts recognition.
func (s *SpeechSession) Listen(ctx context.Context) (*SpeechTurn, error) {
	if s.Closed() {
		return nil, ErrSpeechSessionClosed
	}
	turn, err := s.turns.Begin(RoleCaller)
	if err != nil {
		return nil, err
	}
	if err := s.stt.Start(ctx, turn.ID, s.ctx.Language); err != nil {
		_ = s.turns.Transition(turn.ID, TurnFailed, "stt_open_failed")
		return nil, err
	}
	s.publish(EventSpeechStarted, turn.ID, "", nil)
	return turn, nil
}

// PushAudio submits one inbound frame. This is the AudioInputAdapter seam.
func (s *SpeechSession) PushAudio(f media.Frame) error {
	if s.Closed() {
		return ErrSpeechSessionClosed
	}
	return s.stt.Push(f)
}

// EndOfSpeech signals the endpoint. See [STTOrchestrator.EndOfSpeech] — this is
// the VAD boundary, not a VAD.
func (s *SpeechSession) EndOfSpeech() error {
	if s.Closed() {
		return ErrSpeechSessionClosed
	}
	return s.stt.EndOfSpeech()
}

// Respond synthesises text for the live turn.
func (s *SpeechSession) Respond(ctx context.Context, text string) error {
	if s.Closed() {
		return ErrSpeechSessionClosed
	}
	turn, ok := s.turns.Active()
	if !ok {
		return fmt.Errorf("%w: no live turn to respond to", ErrSpeechSessionClosed)
	}
	if turn.State() == TurnFinal {
		if err := s.turns.Transition(turn.ID, TurnResponding, "response_ready"); err != nil {
			return err
		}
	}
	s.publish(EventTTSStarted, turn.ID, "", nil)
	err := s.tts.Speak(ctx, turn.ID, text, s.ctx.Language)
	if err != nil {
		s.publish(EventTTSCancelled, turn.ID, "speak_failed", nil)
		return err
	}
	s.publish(EventTTSCompleted, turn.ID, "", nil)
	return nil
}

// Interrupt is the barge-in contract.
//
// # The seven requirements, in the order they are satisfied
//
//  1. The interruption signal is accepted from the media/speech boundary.
//  2. Active synthesis is cancelled.
//  3. No further outbound audio escapes for the cancelled generation.
//  4. Inbound audio already queued is preserved — the input path is NOT
//     touched, because the caller's new speech is already arriving into it and
//     flushing it would discard the very words that caused the interruption.
//  5. A new turn is created.
//  6. Already-arrived inbound audio survives, for the same reason as 4.
//  7. The latency is measured and reported.
//
// # Ordering is the contract
//
// The timestamp is taken FIRST so the measurement includes everything. The TTS
// generation is bumped SECOND, before any blocking work, so that in-flight
// audio is stale from the earliest possible instant — that is what bounds the
// window in which the agent could still be heard talking over the caller.
func (s *SpeechSession) Interrupt(reason string) (InterruptResult, error) {
	if s.Closed() {
		return InterruptResult{}, ErrSpeechSessionClosed
	}

	at := s.clock.Now()

	previous, ok := s.turns.Active()
	if !ok {
		return InterruptResult{}, fmt.Errorf(
			"%w: nothing is live to interrupt", ErrSpeechSessionClosed)
	}

	// An interruption is only meaningful once the agent has something to be
	// interrupted FROM. A caller talking while we are still listening is not
	// barging in — they are just talking, and their audio already belongs to
	// the live turn. Refusing here is deliberate: silently treating it as a
	// barge-in would cancel a turn that was recognising speech perfectly well
	// and throw away the transcript in progress.
	if state := previous.State(); state != TurnResponding && state != TurnSpeaking {
		return InterruptResult{}, fmt.Errorf(
			"%w: turn %s is %s; there is no agent speech to interrupt",
			ErrInternalFailure, previous.ID, state)
	}

	dropped, err := s.tts.Cancel(reason)
	if err != nil {
		return InterruptResult{}, err
	}

	if err := s.turns.Transition(previous.ID, TurnInterrupted, reason); err != nil {
		return InterruptResult{}, err
	}

	next, err := s.turns.Begin(RoleCaller)
	if err != nil {
		return InterruptResult{}, err
	}

	latency := s.clock.Since(at)

	s.mu.Lock()
	s.interrupts++
	s.mu.Unlock()

	s.metrics.Interruptions.Add(1)
	s.metrics.InterruptLatency.Observe(latency.Seconds())
	s.publish(EventSpeechInterrupted, previous.ID, reason, nil)

	return InterruptResult{
		PreviousTurn: previous.ID, NewTurn: next.ID,
		ChunksDropped: dropped, At: at, Latency: latency,
	}, nil
}

// Close ends the session and releases everything it owns.
//
// # No goroutine outlives this
//
// The session context is cancelled, both orchestrators are closed, and both
// wait for their pump goroutines. A session that leaked one goroutine per call
// would exhaust a long-running process in hours.
func (s *SpeechSession) Close(ctx context.Context, reason string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	if turn, ok := s.turns.Active(); ok {
		_ = s.turns.Transition(turn.ID, TurnCancelled, reason)
	}

	if s.cancel != nil {
		s.cancel()
	}
	_ = s.stt.Close()
	_ = s.tts.Close()

	s.assembler.Reset()
	s.metrics.SessionsActive.Add(-1)
	s.metrics.SessionsClosed.Add(1, boundedReason(reason))
	s.publish(EventSpeechSessionClosed, "", reason, nil)

	if s.onClose != nil {
		s.onClose(s.id)
	}

	close(s.done)
	return nil
}

// Done is closed when the session ends.
func (s *SpeechSession) Done() <-chan struct{} { return s.done }

// publish emits an event, never blocking the caller on a broker.
func (s *SpeechSession) publish(t SpeechEventType, turn TurnID, reason string, seg *TranscriptSegment) {
	if s.events == nil {
		return
	}

	s.mu.Lock()
	s.seq++
	seq := s.seq
	s.mu.Unlock()

	e := SpeechEvent{
		Type: t, Session: s.id, Turn: turn,
		Language: s.ctx.Language, Reason: boundedReason(reason),
		Sequence: seq, At: s.clock.Now(),
	}
	if seg != nil {
		e.Segment = seg.Segment
		e.CharCount = len([]rune(seg.Text))
		e.Confidence = seg.Confidence
		e.Provider = seg.Meta.Provider
	}

	// A publisher error MUST NOT fail the operation that produced it: a broker
	// outage cannot be allowed to stop calls from being answered.
	_ = s.events.Publish(context.Background(), e)
}
