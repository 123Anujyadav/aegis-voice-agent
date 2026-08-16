package audiointel

import (
	"fmt"
	"math"
	"sort"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// overlapTransitions declares every legal overlap move.
//
//	none → possible → confirmed → resolved → none
//	       possible → none
//	                              resolved → possible
func overlapTransitions() map[OverlapState][]OverlapState {
	return map[OverlapState][]OverlapState{
		OverlapNone:      {OverlapPossible},
		OverlapPossible:  {OverlapConfirmed, OverlapNone},
		OverlapConfirmed: {OverlapResolved},

		// Resolved is a distinct state rather than a return to none, so a
		// consumer can tell an overlap that ENDED from one that never happened.
		// It leads back to possible directly because two people talking over
		// each other rarely stop cleanly once.
		OverlapResolved: {OverlapNone, OverlapPossible},
	}
}

// OverlapTransitionsFrom returns the states reachable from one state, sorted.
func OverlapTransitionsFrom(s OverlapState) []OverlapState {
	out := append([]OverlapState(nil), overlapTransitions()[s]...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// CanOverlapTransition reports whether from → to is declared.
func CanOverlapTransition(from, to OverlapState) bool {
	for _, allowed := range overlapTransitions()[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// OverlapDecision is the double-talk detector's verdict for one frame.
type OverlapDecision struct {
	// State is the overlap state after this frame.
	State OverlapState

	// Previous is the state before it.
	Previous OverlapState

	// Changed reports whether the frame moved the machine.
	Changed bool

	// Confidence is how sure the detector is that this is genuine simultaneous
	// speech rather than echo, in [0,1].
	//
	// READ THIS, NOT THE STATE. Without an echo canceller and a sample-aligned
	// outbound reference, echo and double-talk are not separable, and a state
	// of "confirmed" carrying a confidence of 0.51 means the detector is
	// guessing. See the type comment on [OverlapDetector].
	Confidence float64

	// Duration is how long the overlap has lasted, in media time.
	Duration time.Duration

	// EchoCorrelation is how strongly the inbound envelope tracked the agent's
	// own output, in [-1,1], and whether it could be measured at all.
	//
	// Positive values LOWER confidence. They never raise it, and they are never
	// treated as proof of anything.
	EchoCorrelation float64
	EchoMeasured    bool
}

// String renders the decision.
func (d OverlapDecision) String() string {
	s := fmt.Sprintf("overlap %s conf=%.2f dur=%s",
		d.State, d.Confidence, d.Duration.Round(time.Millisecond))
	if d.EchoMeasured {
		s += fmt.Sprintf(" echo_r=%.2f", d.EchoCorrelation)
	}
	return s
}

// OverlapDetector reports when the caller and the agent are talking at once.
//
// # Read this limitation before reading anything else
//
// THIS ENGINE CANNOT SEPARATE ECHO FROM GENUINE DOUBLE-TALK, and it does not
// claim to. Doing so requires an acoustic echo canceller and an outbound
// reference signal aligned to the inbound one at the sample level. Neither
// exists here: Phase 11B moves frames and does not align them across
// directions, and no echo canceller is in scope for this phase or implied by
// it.
//
// What the detector actually reports is: the caller's audio shows speech while
// the agent holds the floor, sustained for MinDuration. On a handset with poor
// isolation, or a speakerphone, that condition is also met by the agent hearing
// itself. When an [OutboundEnvelope] is supplied, the detector measures whether
// the inbound level tracks the outbound one and uses the correlation to LOWER
// its confidence — never to raise it, and never to assert that separation
// occurred, because a caller can perfectly well speak in the same rhythm as the
// agent and the absence of correlation proves nothing either.
//
// Consumers should act on [OverlapDecision.Confidence], not on the state.
// docs/audio-intelligence/OVERLAP_DETECTION.md says the same thing at greater
// length and leads with it.
//
// # What it does exclude
//
// Short acoustic artifacts. A click, a handset bump, a codec transient and a
// door closing all reach PossibleOverlap and none of them reaches Confirmed,
// because confirmation needs MinDuration of sustained caller speech.
//
// Not safe for concurrent use. One detector per session.
type OverlapDetector struct {
	policy OverlapPolicy
	fsm    *rt.FSM[OverlapState]
	clock  rt.Clock

	// startedAt is the media time the current overlap began.
	startedAt time.Duration
	open      bool

	// quietSince is the media time the overlap conditions stopped holding.
	quietSince time.Duration
	quiet      bool

	// inbound and outbound hold recent level pairs for the echo correlation.
	// Fixed size, allocated once.
	inbound  []float64
	outbound []float64
	next     int
	filled   int
}

// echoWindowFrames is how many level pairs the correlation is computed over.
//
// Short on purpose: echo tracks the outbound signal within milliseconds, and a
// long window would average across the caller's own speech and wash the
// correlation out. Not configurable because it is a property of what echo IS
// rather than a policy a deployment would tune.
const echoWindowFrames = 16

// NewOverlapDetector builds a double-talk detector for one session.
func NewOverlapDetector(cfg Config, clock rt.Clock) (*OverlapDetector, error) {
	if problems := cfg.Overlap.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if clock == nil {
		clock = rt.SystemClock{}
	}

	fsm, err := rt.NewFSM(rt.FSMSpec[OverlapState]{
		Initial:     OverlapNone,
		Transitions: overlapTransitions(),
	}, clock)
	if err != nil {
		return nil, err
	}

	return &OverlapDetector{
		policy:   cfg.Overlap,
		fsm:      fsm,
		clock:    clock,
		inbound:  make([]float64, echoWindowFrames),
		outbound: make([]float64, echoWindowFrames),
	}, nil
}

// State returns the current overlap state.
func (o *OverlapDetector) State() OverlapState { return o.fsm.State() }

// Observe folds one frame in.
//
// envelope may be nil, in which case no echo correlation is measured and the
// confidence carries no echo penalty — which makes it an UPPER bound on how
// sure the detector could be, not a higher certainty.
func (o *OverlapDetector) Observe(
	v SignalView,
	d VADDecision,
	agentSpeaking bool,
	envelope OutboundEnvelope,
) OverlapDecision {
	prev := o.fsm.State()
	next := prev

	overlapping := o.policy.Enabled && agentSpeaking && d.State.Active()

	correlation, measured := o.observeEcho(v, agentSpeaking, envelope)

	frameEnd := v.Frame.End()

	// The confidence is computed BEFORE the transition is decided, because
	// MinConfidence gates the promotion to Confirmed. An earlier version
	// promoted first and then downgraded the REPORTED state when the confidence
	// came out low, which left the reported state disagreeing with the state
	// machine's — so Previous and Changed described one thing and State
	// described another. Gating the transition keeps them the same thing.
	var duration time.Duration
	if o.open {
		duration = frameEnd - o.startedAt
	}
	confidence := o.score(d, duration, correlation, measured)

	switch prev {
	case OverlapNone:
		if overlapping {
			next = OverlapPossible
			o.startedAt = v.Frame.Timestamp
			o.open = true
			o.quiet = false
		}

	case OverlapPossible:
		switch {
		case !overlapping:
			// Did not persist. A click, a handset bump, a codec transient.
			next = OverlapNone
			o.open = false

		case frameEnd-o.startedAt >= o.policy.MinDuration &&
			confidence >= o.policy.MinConfidence:
			// BOTH conditions. Sustained for long enough AND the evidence
			// supports it — a "confirmed" carrying a confidence of 0.2 would
			// tell a consumer reading only the state something the detector
			// does not believe.
			next = OverlapConfirmed
		}

	case OverlapConfirmed:
		if !overlapping {
			if !o.quiet {
				o.quiet = true
				o.quietSince = v.Frame.Timestamp
			}
			if frameEnd-o.quietSince >= o.policy.ResolveAfter {
				next = OverlapResolved
			}
		} else {
			o.quiet = false
		}

	case OverlapResolved:
		if overlapping {
			next = OverlapPossible
			o.startedAt = v.Frame.Timestamp
			o.open = true
			o.quiet = false
		} else {
			next = OverlapNone
			o.open = false
		}
	}

	changed := next != prev
	if changed {
		if _, err := o.fsm.To(next); err != nil {
			// Unreachable: every branch above moves along a declared edge. Held
			// rather than panicked, because crashing a live call to report an
			// internal inconsistency is worse than holding the previous state.
			next = prev
			changed = false
		}
	}

	// Recomputed against the state actually entered: None and Resolved carry no
	// overlap confidence, and the promotion above may have just opened or
	// closed the stretch.
	if o.open {
		duration = frameEnd - o.startedAt
	} else {
		duration = 0
	}

	out := OverlapDecision{
		State:           next,
		Previous:        prev,
		Changed:         changed,
		Duration:        duration,
		EchoCorrelation: correlation,
		EchoMeasured:    measured,
	}
	if next != OverlapNone && next != OverlapResolved {
		out.Confidence = o.score(d, duration, correlation, measured)
	}

	return out
}

// observeEcho records the level pair and returns the correlation.
func (o *OverlapDetector) observeEcho(
	v SignalView, agentSpeaking bool, envelope OutboundEnvelope,
) (float64, bool) {
	if envelope == nil || !agentSpeaking {
		return 0, false
	}

	level, known := envelope.LevelAt(v.Frame.Timestamp)
	if !known {
		// Unknown is a perfectly good answer — the outbound path may simply not
		// be instrumented — and it is treated as no evidence rather than as
		// silence, which would fabricate an anti-correlation.
		return 0, false
	}

	o.inbound[o.next] = v.Frame.RMS
	o.outbound[o.next] = level
	o.next = (o.next + 1) % echoWindowFrames
	if o.filled < echoWindowFrames {
		o.filled++
	}

	if o.filled < echoWindowFrames {
		return 0, false
	}
	return pearson(o.inbound, o.outbound), true
}

// pearson returns the correlation coefficient of two equal-length series.
//
// Returns zero when either series is constant: a constant has no variance, the
// coefficient is undefined, and zero — "no evidence of correlation" — is the
// honest answer rather than a division by zero.
func pearson(a, b []float64) float64 {
	n := float64(len(a))

	var sumA, sumB float64
	for i := range a {
		sumA += a[i]
		sumB += b[i]
	}
	meanA, meanB := sumA/n, sumB/n

	var cov, varA, varB float64
	for i := range a {
		da, db := a[i]-meanA, b[i]-meanB
		cov += da * db
		varA += da * da
		varB += db * db
	}

	if varA <= 0 || varB <= 0 {
		return 0
	}
	return cov / math.Sqrt(varA*varB)
}

// score rates how likely it is that this is genuine simultaneous speech.
//
// # The formula, stated so it can be argued with
//
//	base       = the voice activity detector's own confidence in the speech
//	sustained  = clamp01(duration / MinDuration)
//	score      = base × sustained × (1 − EchoCorrelationPenalty × max(0, r))
//
// The sustained factor is what excludes short artifacts without a hard cutoff:
// a 40 ms click scores low rather than being silently discarded, so a consumer
// watching the distribution can see how often it happens.
//
// The base term carries the noise floor's own confidence through, because an
// overlap judgement is built on a speech judgement and cannot be surer than it.
// Early in a call, before the floor has been observed for long, that keeps
// overlap confidence low — which is correct, and it means a deployment tuning
// MinConfidence is also deciding how much of a call's opening to stay quiet
// about.
//
// The echo term only ever REDUCES. A negative correlation — the caller getting
// quieter as the agent gets louder — is not evidence of anything and is clamped
// away, because the honest reading of it is that these two signals are
// unrelated, which is exactly what the base score already assumed.
func (o *OverlapDetector) score(
	d VADDecision, duration time.Duration, correlation float64, measured bool,
) float64 {
	base := d.Confidence

	sustained := 1.0
	if o.policy.MinDuration > 0 && duration < o.policy.MinDuration {
		sustained = float64(duration) / float64(o.policy.MinDuration)
	}

	s := base * clamp01(sustained)

	if measured && correlation > 0 {
		s *= 1 - o.policy.EchoCorrelationPenalty*correlation
	}

	return clamp01(s)
}

// Reset returns the detector to its initial state, keeping its storage.
//
// Rebuilds the state machine for the reason [SpeechDetector.Reset] documents:
// runtime.FSM has no reset, because a machine that could be moved anywhere on
// demand is not a machine whose transition table means anything. A rebuild
// failure leaves the detector untouched rather than half-reset.
func (o *OverlapDetector) Reset() {
	fsm, err := rt.NewFSM(rt.FSMSpec[OverlapState]{
		Initial:     OverlapNone,
		Transitions: overlapTransitions(),
	}, o.clock)
	if err != nil {
		return
	}
	o.fsm = fsm

	o.startedAt, o.open = 0, false
	o.quietSince, o.quiet = 0, false
	o.next, o.filled = 0, 0
	for i := range o.inbound {
		o.inbound[i], o.outbound[i] = 0, 0
	}
}
