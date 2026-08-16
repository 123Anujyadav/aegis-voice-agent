package audiointel

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// SessionContext is the immutable description of an audio intelligence session.
//
// Immutable once the session exists. Mutable state lives in [Session]; this is
// what the session IS. The split is what lets a snapshot be taken without
// locking the parts that never move.
type SessionContext struct {
	// Call ties this session to the call it serves. Opaque, and may be empty:
	// a session may legitimately analyse audio before a call record exists.
	Call CallID

	// Direction is which way the analysed audio flows.
	Direction Direction

	// Language is the tag Phase 11C associated with this audio, where one is
	// known. Carried onto events and never interpreted — see [Language].
	Language Language

	// Format is the audio analysed. Mono PCM only.
	Format media.AudioFormat
}

// Validate checks the context, reporting every problem.
func (c SessionContext) Validate() error {
	var problems []string

	if !c.Call.Valid() {
		problems = append(problems, fmt.Sprintf(
			"session: Call %q must be lowercase alphanumerics, hyphen or underscore, "+
				"or empty — it reaches an event", c.Call))
	}
	if !c.Direction.Valid() {
		problems = append(problems, fmt.Sprintf(
			"session: Direction %q must be inbound or outbound", c.Direction))
	}
	if !c.Language.Valid() {
		problems = append(problems, fmt.Sprintf(
			"session: Language %q must be lowercase alphanumerics, hyphen or "+
				"underscore, or empty — it reaches an event", c.Language))
	}
	if err := validateAnalysisFormat(c.Format); err != nil {
		problems = append(problems, err.Error())
	}

	if len(problems) > 0 {
		return &ConfigError{Problems: problems}
	}
	return nil
}

// SessionStats is a consistent view of one session.
//
// Pulled on demand rather than pushed on every frame, which is what keeps the
// metric label set bounded: per-session detail lives here, and nothing carrying
// a session identifier reaches the metrics backend.
type SessionStats struct {
	ID        SessionID
	Call      CallID
	Direction Direction

	// Frames is how many frames were analysed, and Refused how many could not
	// be.
	Frames  uint64
	Refused uint64

	// VADState, Quality and Overlap are the current classifications.
	VADState VADState
	Quality  QualityClass
	Overlap  OverlapState

	// NoiseFloorDBFS is the current adaptive background estimate.
	NoiseFloorDBFS float64

	// SpeechRuns, Endpoints and BargeIns are lifetime counts.
	SpeechRuns int
	Endpoints  int
	BargeIns   int

	// SpeechTime is the total media time classified as speech.
	SpeechTime time.Duration

	// Age is how long the session has existed.
	Age time.Duration

	// Closed reports whether the session has ended.
	Closed bool
}

// String renders the stats.
func (s SessionStats) String() string {
	return fmt.Sprintf(
		"session %s %s frames=%d vad=%s quality=%s runs=%d endpoints=%d barge_ins=%d",
		s.ID, s.Direction, s.Frames, s.VADState, s.Quality,
		s.SpeechRuns, s.Endpoints, s.BargeIns)
}

// Session is one direction of one call's audio intelligence.
//
// # It owns an analyser and nothing else owns its state
//
// Two sessions share no detector, no window, no floor estimate and no lock.
// Cross-session contamination is therefore structurally impossible rather than
// merely unlikely: there is no path from one session's state to another's, and
// TestSession_IsIsolated drives many in parallel and checks their conclusions
// independently.
//
// # The lock exists for lifecycle, not for throughput
//
// Analysis is synchronous and single-threaded per session by design — see the
// package documentation. The mutex is held across Analyze so that Close and
// Stats, which a supervising goroutine legitimately calls, cannot observe a
// half-updated analyser. It is uncontended in the intended usage and costs
// about twenty nanoseconds against a frame budget measured in microseconds.
type Session struct {
	id        SessionID
	ctx       SessionContext
	clock     rt.Clock
	metrics   *AudioIntelligenceMetrics
	events    EventPublisher
	direction string

	// onClose deregisters the session from its runtime.
	//
	// WITHOUT THIS, CLOSING A SESSION LEAKS ITS REGISTRY ENTRY. A caller that
	// closes sessions directly — the obvious thing to do — would accumulate
	// entries until the runtime refused new sessions for capacity, on a process
	// that was in fact idle. Phase 11C shipped exactly that bug.
	onClose func(SessionID)

	mu       sync.Mutex
	analyzer *AudioAnalyzer
	closed   bool

	createdAt time.Time
	seq       int

	frames     uint64
	refused    uint64
	speechRuns int
	endpoints  int
	bargeIns   int
	speechTime time.Duration

	// lastNoiseClass and lastQuality track what was last published, so a change
	// event fires once rather than on every frame.
	lastNoiseClass NoiseClass
}

// ID returns the session identifier.
func (s *Session) ID() SessionID { return s.id }

// Context returns the immutable session description.
func (s *Session) Context() SessionContext { return s.ctx }

// Closed reports whether the session has ended.
func (s *Session) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Analyze runs one frame through the engine and publishes what it concluded.
//
// # This is the hot path, and it runs inline
//
// No goroutine, no channel, no queue. ADR-0004 §247 requires that there be
// nothing between the detector and the speech controller, and ADR-0004 §12
// budgets the whole barge-in hop at one frame interval.
//
// The frame's payload is BORROWED — see [media.Frame] — and is read in place
// within this call. Nothing retains it.
func (s *Session) Analyze(
	ctx context.Context,
	f media.Frame,
	state ConversationState,
	controller SpeechController,
	envelope OutboundEnvelope,
) (Analysis, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		s.refused++
		s.metrics.FramesRefused.Add(1, ReasonSessionClosed)
		return Analysis{}, ErrSessionClosed
	}

	started := s.clock.Now()

	out, err := s.analyzer.Analyze(ctx, f, state, controller, envelope)
	if err != nil {
		s.refused++
		s.metrics.FramesRefused.Add(1, refusalReason(err))
		return Analysis{}, err
	}

	s.frames++
	s.metrics.FramesAnalysed.Add(1, s.direction)
	s.metrics.FrameAnalysisLatency.Observe(s.clock.Since(started).Seconds(), "full_chain")

	s.record(ctx, out, state)
	return out, nil
}

// refusalReason maps an analysis error to a bounded metric label.
func refusalReason(err error) string {
	switch {
	case errors.Is(err, ErrFormatMismatch):
		return ReasonFormatMismatch
	case errors.Is(err, ErrInvalidFrame):
		return ReasonEmptyFrame
	default:
		return "unspecified"
	}
}

// record folds one analysis into the metrics and publishes its events.
//
// # Every event here is bounded, and speech_detected is the one that needed
// thought
//
// §16 requires a speech_detected event. Emitting one per frame while speech is
// active would produce fifty events a second per session — the highest-volume
// topic in the platform, describing a fact a consumer can already read from
// speech_started. It is emitted ONCE per run, at the point the run exceeds
// VADConfig.MinSpeech, which is the moment the engine stops being able to
// dismiss it as a transient. That is a fact worth one event.
func (s *Session) record(ctx context.Context, a Analysis, state ConversationState) {
	m := s.metrics
	dir := s.direction

	m.VADDecisions.Add(1, string(a.VAD.State))
	if a.VAD.Changed {
		m.VADTransitions.Add(1, string(a.VAD.Previous), string(a.VAD.State))
	}
	if a.VAD.State.Active() {
		s.speechTime += a.Signal.Frame.Duration
		m.SpeechConfidence.Observe(a.VAD.Confidence)
	}

	if a.VAD.OnsetConfirmed {
		s.speechRuns++
		m.SpeechStarts.Add(1, dir)
		s.publish(ctx, EventSpeechStarted, a, state, ReasonOnsetConfirmed,
			Classification(a.VAD.State), a.VAD.Confidence)
	}

	// Emitted once per run, at the moment the run outlasts MinSpeech and can no
	// longer be dismissed as a transient.
	if a.VAD.State == VADSpeech && s.crossedMinSpeech(a) {
		s.publish(ctx, EventSpeechDetected, a, state, ReasonOnsetConfirmed,
			Classification(a.VAD.State), a.VAD.Confidence)
	}

	if a.VAD.OffsetConfirmed {
		m.SpeechEnds.Add(1, dir)
		m.SpeechDuration.Observe(a.VAD.RunDuration.Seconds())
		s.publish(ctx, EventSpeechStopped, a, state, ReasonHangoverElapsed,
			Classification(a.VAD.State), a.VAD.Confidence)
	}
	if r := s.analyzer.vad.LastFalseTrigger(); r != "" {
		m.FalseTriggers.Add(1, r)
	}

	if a.Silence.Started {
		s.publish(ctx, EventSilenceStarted, a, state, "",
			Classification(a.Silence.Class), 0)
	}
	if a.Silence.Extended {
		m.SilenceDuration.Observe(a.Silence.Duration.Seconds(), string(a.Silence.Class))
		s.publish(ctx, EventSilenceExtended, a, state, "",
			Classification(a.Silence.Class), 0)
	}

	if a.Endpoint.Candidate {
		m.EndpointCandidates.Add(1, dir)
		s.publish(ctx, EventEndpointCandidate, a, state, "", "", 0)
	}
	if a.Endpoint.Suppressed {
		m.EndpointSuppressed.Add(1, a.Endpoint.Reason)
	}
	if a.Endpoint.Confirmed {
		s.endpoints++
		m.EndpointConfirmed.Add(1, dir)
		m.EndpointLatency.Observe(a.Endpoint.SilenceHeld.Seconds())
		s.publish(ctx, EventEndpointConfirmed, a, state, a.Endpoint.Reason, "", 0)
	}

	if a.BargeIn.Detected {
		m.BargeIns.Add(1, string(a.BargeIn.Outcome))
		if a.BargeIn.Outcome == BargeInDelivered {
			s.bargeIns++
			m.BargeInLatency.Observe(a.BargeIn.Latency.Seconds())
		}
		s.publish(ctx, EventBargeInDetected, a, state, string(a.BargeIn.Outcome),
			Classification(a.BargeIn.Outcome), a.VAD.Confidence)
	}

	if a.Overlap.Changed {
		m.Overlaps.Add(1, string(a.Overlap.State))
		switch a.Overlap.State {
		case OverlapConfirmed:
			s.publish(ctx, EventOverlapDetected, a, state, "",
				Classification(a.Overlap.State), a.Overlap.Confidence)
		case OverlapResolved:
			m.OverlapDuration.Observe(a.Overlap.Duration.Seconds())
			s.publish(ctx, EventOverlapResolved, a, state, "",
				Classification(a.Overlap.State), a.Overlap.Confidence)
		}
	}

	if c := a.Signal.Noise.Class; c != s.lastNoiseClass {
		s.lastNoiseClass = c
		m.NoiseChanges.Add(1, dir)
		m.NoiseFloorDBFS.Set(a.Signal.Noise.FloorDBFS)
		s.publish(ctx, EventNoiseChanged, a, state, "",
			Classification(c), a.Signal.Noise.Confidence)
	}

	if a.Quality.Changed {
		m.QualityChanges.Add(1, string(a.Quality.Previous), string(a.Quality.Class))
		s.publish(ctx, EventQualityChanged, a, state, a.Quality.Reason,
			Classification(a.Quality.Class), 0)

		switch {
		case a.Quality.Degraded:
			m.Degradations.Add(1, a.Quality.Reason)
			s.publish(ctx, EventAudioDegraded, a, state, a.Quality.Reason,
				Classification(a.Quality.Class), 0)
		case a.Quality.Recovered:
			m.Recoveries.Add(1, dir)
			s.publish(ctx, EventAudioRecovered, a, state, ReasonQualityRose,
				Classification(a.Quality.Class), 0)
		}
	}

	if f := a.Continuity.Fault; f != FaultNone {
		m.FrameGaps.Add(1, string(f))
	}
	if a.Continuity.GapOpened {
		m.ContinuityEvents.Add(1, string(a.Continuity.Fault))
		s.publish(ctx, EventFrameGapDetected, a, state, "",
			Classification(a.Continuity.Fault), 0)
	}
	if a.Continuity.Restored {
		m.ContinuityEvents.Add(1, string(FaultNone))
		s.publish(ctx, EventFrameContinuityRestored, a, state, "", "", 0)
	}
}

// crossedMinSpeech reports whether this frame is the one on which the open run
// first outlasted MinSpeech.
func (s *Session) crossedMinSpeech(a Analysis) bool {
	min := s.analyzer.cfg.VAD.MinSpeech
	if min <= 0 {
		return a.VAD.OnsetConfirmed
	}
	frame := a.Signal.Frame.Duration
	return a.VAD.SpeechDuration >= min && a.VAD.SpeechDuration-frame < min
}

// publish emits one event, never blocking the caller on a broker.
func (s *Session) publish(
	ctx context.Context, t AudioEventType, a Analysis,
	state ConversationState, reason string, class Classification, confidence float64,
) {
	if s.events == nil {
		return
	}

	s.seq++

	e := AudioEvent{
		Type:           t,
		Session:        s.id,
		Call:           s.ctx.Call,
		Turn:           state.Turn,
		Language:       s.ctx.Language,
		Classification: class,
		Confidence:     confidence,
		Reason:         boundedReason(reason),
		MediaTime:      a.Signal.Frame.Timestamp,
		Sequence:       s.seq,
		At:             s.clock.Now(),
		Detail: EventDetail{
			LevelDBFS:      a.Signal.Frame.LevelDBFS(),
			NoiseFloorDBFS: a.Signal.Noise.FloorDBFS,
			SNRDB:          a.Signal.ExcessDB,
			FrameCount:     a.Signal.Window.Frames,
		},
	}

	switch t {
	case EventSpeechStopped:
		e.Detail.DurationMillis = a.VAD.RunDuration.Milliseconds()
	case EventSilenceStarted, EventSilenceExtended:
		e.Detail.DurationMillis = a.Silence.Duration.Milliseconds()
	case EventEndpointConfirmed, EventEndpointCandidate:
		e.Detail.DurationMillis = a.Endpoint.SilenceHeld.Milliseconds()
	case EventBargeInDetected:
		e.Detail.LatencyMicros = a.BargeIn.Latency.Microseconds()
	case EventOverlapResolved:
		e.Detail.DurationMillis = a.Overlap.Duration.Milliseconds()
	}

	// A publisher error MUST NOT fail the analysis that produced it: a broker
	// outage cannot be allowed to stop the engine noticing somebody is talking.
	_ = s.events.Publish(ctx, e)
}

// ObserveDelivery folds in Phase 11B's verdict on a frame that never arrived.
//
// Optional. A deployment that does not thread the pipeline result through gets
// arrival-side continuity only, which is most of it — but not buffer
// starvation, which by definition produces no frame to observe.
func (s *Session) ObserveDelivery(r media.PipelineResult) ContinuityFault {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return FaultNone
	}
	fault := s.analyzer.ObserveDelivery(r)
	if fault != FaultNone {
		s.metrics.FrameGaps.Add(1, string(fault))
	}
	return fault
}

// NoteAgentFinished tells the engine the agent stopped speaking.
//
// What turns the following pause into a thinking pause rather than merely a
// pause. POSITIONAL, not semantic — see [SilenceClassifier].
func (s *Session) NoteAgentFinished() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.analyzer.NoteAgentFinished()
	}
}

// Analyzer returns the detector chain, for a caller that needs to inspect a
// detector directly.
//
// NOT SAFE TO USE CONCURRENTLY WITH Analyze. Intended for tests and for a
// consumer reading state between frames on the same goroutine.
func (s *Session) Analyzer() *AudioAnalyzer { return s.analyzer }

// Stats returns a consistent snapshot.
func (s *Session) Stats() SessionStats {
	s.mu.Lock()
	defer s.mu.Unlock()

	return SessionStats{
		ID:             s.id,
		Call:           s.ctx.Call,
		Direction:      s.ctx.Direction,
		Frames:         s.frames,
		Refused:        s.refused,
		VADState:       s.analyzer.VADState(),
		Quality:        s.analyzer.QualityClass(),
		Overlap:        s.analyzer.OverlapState(),
		NoiseFloorDBFS: dbfs(s.analyzer.NoiseFloor()),
		SpeechRuns:     s.speechRuns,
		Endpoints:      s.endpoints,
		BargeIns:       s.bargeIns,
		SpeechTime:     s.speechTime,
		Age:            s.clock.Since(s.createdAt),
		Closed:         s.closed,
	}
}

// Reset returns every detector to its initial state, for a session recovering
// mid-call.
//
// The lifetime counters are deliberately NOT reset: a recovered session is the
// same session, and zeroing its history would make an incident look like it
// happened to a fresh call.
func (s *Session) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.analyzer.Reset()
		s.lastNoiseClass = ""
	}
}

// Close ends the session and releases its registry entry.
//
// Idempotent. There is no goroutine to stop — this engine starts none — so
// closing is bookkeeping plus deregistration.
func (s *Session) Close(ctx context.Context, reason string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	s.metrics.SessionsActive.Add(-1)
	s.metrics.SessionsClosed.Add(1, boundedReason(reason))

	if s.onClose != nil {
		s.onClose(s.id)
	}
	_ = ctx
	return nil
}
