package audiointel

import (
	"fmt"
	"time"
)

// EndpointDecision is the endpointer's verdict for one frame.
type EndpointDecision struct {
	// Candidate is set on the frame where the turn MIGHT be ending — the
	// moment the caller's audio went quiet.
	Candidate bool

	// Confirmed is set on the frame where the turn is declared ended.
	Confirmed bool

	// Suppressed is set when confirmation was withheld by a gate that would
	// otherwise have fired.
	Suppressed bool

	// Reason is the bounded code for the confirmation or the suppression.
	Reason string

	// SilenceHeld is how much silence had accumulated, in media time.
	//
	// Measured from the FIRST sub-threshold frame. This is the quantity
	// ADR-0011 §5.2 hop 1 budgets at 250 ms p50 / 350 ms p95, so it is measured
	// the way the budget defines it and not from the moment the hangover
	// elapsed.
	SilenceHeld time.Duration

	// SpeechDuration is how long the turn's speech ran, excluding the hangover.
	SpeechDuration time.Duration

	// TurnDuration is how long the whole turn ran, onset to confirmation.
	TurnDuration time.Duration
}

// String renders the decision.
func (d EndpointDecision) String() string {
	switch {
	case d.Confirmed:
		return fmt.Sprintf("endpoint confirmed after %s silence (%s speech, %s)",
			d.SilenceHeld.Round(time.Millisecond),
			d.SpeechDuration.Round(time.Millisecond), d.Reason)
	case d.Suppressed:
		return fmt.Sprintf("endpoint suppressed (%s)", d.Reason)
	case d.Candidate:
		return "endpoint candidate"
	default:
		return "no endpoint"
	}
}

// EndpointGates is the conversation state the endpointer consults.
//
// Supplied by the session from what its caller told it. This engine does not
// know what a conversation is and does not ask — it takes two booleans.
type EndpointGates struct {
	// AgentSpeaking reports whether the agent currently holds the floor.
	AgentSpeaking bool

	// BargeInActive reports whether an interruption is unresolved.
	BargeInActive bool
}

// EndpointDetector decides when a caller's turn has ended.
//
// # Endpointing is a policy decision, and it is separate from acoustic offset
//
// The voice activity detector answers "is the caller making a sound". The
// endpointer answers "should we treat the turn as over", and those are
// different questions with different costs. Ending a turn early cuts a caller
// off mid-thought; ending it late leaves dead air. ADR-0011 §7 records that the
// window is tuned by measuring FALSE-ENDPOINT RATE, not by minimising latency,
// and this detector is built to make that measurable.
//
// # There is no English pause model here
//
// Every threshold is configuration. The 250 ms default comes from ADR-0011
// §5.2 hop 1, which set it for this platform's traffic, and a deployment
// serving Hindi or Hinglish callers tunes [EndpointPolicy] rather than changing
// code. Nothing in this file encodes an assumption about any language's
// phonology or rhythm — it counts milliseconds of silence.
//
// Not safe for concurrent use. One detector per session.
type EndpointDetector struct {
	policy EndpointPolicy

	// turnOpen tracks whether a speech run is in progress or awaiting an
	// endpoint.
	turnOpen  bool
	turnStart time.Duration

	// speechDuration is the run length reported by the last offset, or the
	// running length while speech continues.
	speechDuration time.Duration

	// candidateEmitted stops a candidate being reported on every frame of the
	// same pause. Cleared when speech resumes, so a LATER pause in the same
	// turn produces a fresh candidate.
	candidateEmitted bool

	// confirmed latches so one turn produces one endpoint.
	confirmed bool
}

// NewEndpointDetector builds an endpointer for one session.
func NewEndpointDetector(cfg Config) (*EndpointDetector, error) {
	if problems := cfg.Endpoint.validate(cfg.FrameInterval); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	return &EndpointDetector{policy: cfg.Endpoint}, nil
}

// Observe folds one frame's voice activity decision in.
func (e *EndpointDetector) Observe(v SignalView, d VADDecision, gates EndpointGates) EndpointDecision {
	var out EndpointDecision

	// A new speech run opens a turn.
	if d.OnsetConfirmed {
		e.turnOpen = true
		e.turnStart = d.SpeechStart
		e.speechDuration = 0
		e.candidateEmitted = false
		e.confirmed = false
	}

	if !e.turnOpen {
		return out
	}

	// Track the run length. While speech is in progress this grows; when the
	// run ends the offset reports the authoritative figure, which excludes the
	// hangover.
	if d.SpeechDuration > e.speechDuration {
		e.speechDuration = d.SpeechDuration
	}
	if d.OffsetConfirmed {
		e.speechDuration = d.RunDuration
	}

	out.SilenceHeld = d.SilenceDuration
	out.SpeechDuration = e.speechDuration
	out.TurnDuration = v.Frame.End() - e.turnStart

	// Speech resumed: any open candidate is withdrawn, because the pause it
	// described did not turn out to be the end of anything.
	if d.State.Active() && d.State == VADSpeech {
		e.candidateEmitted = false
	}

	// THE FORCED ENDPOINT. A caller on a noisy line can hold the voice activity
	// detector in speech indefinitely, and without this the conversation never
	// advances — the symptom being an agent that has apparently stopped
	// listening. Checked before the gates, because a turn that will not end is
	// exactly the case the gates would keep suppressing.
	if !e.confirmed && out.TurnDuration >= e.policy.MaxTurnDuration {
		e.confirmed = true
		e.turnOpen = false
		out.Confirmed = true
		out.Reason = ReasonMaxTurn
		return out
	}

	if e.confirmed {
		return out
	}

	// The caller's audio went quiet: the turn might be ending. Reported as
	// early as possible, because a consumer may want to start preparing a
	// response before the endpoint is certain.
	if d.SilenceDuration > 0 && !e.candidateEmitted {
		e.candidateEmitted = true
		out.Candidate = true
	}

	if d.SilenceDuration < e.policy.SilenceWindow {
		return out
	}

	// The window has elapsed. Everything below decides whether to act on it.
	if reason, ok := e.suppress(v, gates); ok {
		out.Suppressed = true
		out.Reason = reason
		return out
	}

	e.confirmed = true
	e.turnOpen = false
	out.Confirmed = true
	out.Reason = ReasonSilenceWindow
	return out
}

// suppress reports the gate withholding confirmation, if any.
//
// Ordered so the most decisive reason is reported: an agent holding the floor
// makes every other consideration irrelevant, because what the caller is doing
// then is a barge-in and barge-in opens its own turn.
func (e *EndpointDetector) suppress(v SignalView, gates EndpointGates) (string, bool) {
	if e.policy.SuppressWhileAgentSpeaking && gates.AgentSpeaking {
		return ReasonAgentSpeaking, true
	}
	if e.policy.SuppressDuringBargeIn && gates.BargeInActive {
		return ReasonBargeInActive, true
	}

	// A cough is not a turn. Checked against the run length excluding the
	// hangover, so a 40 ms noise followed by 250 ms of silence does not
	// endpoint on the strength of the silence.
	if e.speechDuration < e.policy.MinSpeechDuration {
		return ReasonSpeechTooShort, true
	}

	// OFF BY DEFAULT. It defers an endpoint on a caller who is audibly winding
	// up to say more, and it also defers one on a rising noise floor — which is
	// why a deployment turns it on only after measuring a benefit.
	if e.policy.RequireFallingEnergy && v.Window.Trend > e.policy.EnergyTrendTolerance {
		return ReasonEnergyRising, true
	}

	return "", false
}

// TurnOpen reports whether a turn is in progress or awaiting an endpoint.
func (e *EndpointDetector) TurnOpen() bool { return e.turnOpen }

// Reset returns the detector to its initial state.
func (e *EndpointDetector) Reset() {
	e.turnOpen = false
	e.turnStart = 0
	e.speechDuration = 0
	e.candidateEmitted = false
	e.confirmed = false
}
