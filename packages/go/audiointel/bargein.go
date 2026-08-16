package audiointel

import (
	"context"
	"fmt"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// BargeInDecision is what happened to one potential interruption.
type BargeInDecision struct {
	// Detected reports that the conditions for an interruption were met.
	//
	// TRUE EVEN WHEN THE OUTCOME IS NOT Delivered. A detection that was
	// debounced or discarded as stale still happened, and a consumer counting
	// only deliveries cannot tell a quiet call from one where every
	// interruption was suppressed.
	Detected bool

	// Outcome is what was done about it. Every detection produces exactly one.
	Outcome BargeInOutcome

	// Reason is the bounded code sent to the speech controller.
	Reason string

	// Latency is measured from the DETECTION to the controller returning.
	//
	// ADR-0004 §12 and ADR-0011 §5.1 budget this hop at one frame interval —
	// 20 ms — and the budget runs from the detection signal, NOT from acoustic
	// onset. The frames VADConfig.MinOnsetFrames spends confirming the onset
	// are upstream of it, and PERFORMANCE.md reports both figures separately so
	// neither is mistaken for the other.
	Latency time.Duration

	// OnsetLatency is the other half: from where speech actually began, in
	// media time, to the detection. Carried so the two are never conflated.
	OnsetLatency time.Duration

	// At is when the detection was stamped, on the injected clock.
	At time.Time

	// Err is the speech controller's error, if it refused.
	Err error
}

// String renders the decision.
func (d BargeInDecision) String() string {
	if !d.Detected {
		return "no barge-in"
	}
	return fmt.Sprintf("barge-in %s in %s (onset %s earlier)",
		d.Outcome, d.Latency.Round(time.Microsecond),
		d.OnsetLatency.Round(time.Millisecond))
}

// BargeInDetector turns a speech onset during agent speech into a cancellation.
//
// # It never touches a TTS provider
//
// Cancellation goes through [SpeechController], which packages/go/audiobridge
// implements over Phase 11C's speech.SpeechSession.Interrupt. §8 requires
// exactly that, and §29 forbids this module from importing anything that would
// let it do otherwise.
//
// # Inbound audio is never touched, and that is satisfied by not doing anything
//
// The caller's new speech is already arriving — it is the reason the
// interruption fired. Flushing or pausing the input would discard the very
// words that caused the barge-in. This detector has no path to the input at
// all, which is the strongest form of that guarantee. Phase 11C's requirements
// 4 and 6 are satisfied the same way, for the same reason.
//
// # Three ways a detection is deliberately not delivered
//
// DEBOUNCED. A caller who interrupts is usually still talking a moment later
// and the voice activity detector may legitimately re-confirm. Without a
// debounce, one interruption becomes several, each cancelling the turn the
// previous one opened.
//
// STALE. A detection older than MaxAge is discarded. Cancelling speech the
// agent finished half a second ago cuts off whatever it started next, and to
// the caller that is the agent interrupting itself — worse than the missed
// interruption.
//
// NOT SPEAKING. Mirrors Phase 11C, whose Interrupt refuses unless the turn is
// responding or speaking: a caller talking while we are listening is not
// interrupting, they are just talking, and their audio already belongs to the
// live turn. Firing anyway would cancel a turn that was recognising speech
// perfectly well.
//
// Every one of those is COUNTED. A detection that vanished without a counter is
// a barge-in nobody can explain the absence of, and "the agent talked over me"
// is the hardest complaint to investigate after the fact.
//
// Not safe for concurrent use. One detector per session.
type BargeInDetector struct {
	policy BargeInPolicy
	clock  rt.Clock

	// lastDelivered is when an interruption last reached the controller, for
	// the debounce.
	lastDelivered time.Time
	haveDelivered bool

	// pending tracks an onset awaiting ConfirmFrames of extra evidence.
	pending       bool
	pendingSince  time.Duration
	pendingFrames int

	// active reports an interruption that has fired and not yet been resolved
	// by the caller's speech ending.
	active bool
}

// NewBargeInDetector builds an interruption detector for one session.
func NewBargeInDetector(cfg Config, clock rt.Clock) (*BargeInDetector, error) {
	if problems := cfg.BargeIn.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if clock == nil {
		clock = rt.SystemClock{}
	}
	return &BargeInDetector{policy: cfg.BargeIn, clock: clock}, nil
}

// Active reports whether an interruption is unresolved.
func (b *BargeInDetector) Active() bool { return b.active }

// Observe folds one frame in and, if an interruption is warranted, delivers it.
//
// # The ordering here is the contract, and it mirrors Phase 11C's
//
// The timestamp is taken FIRST so the measurement includes everything that
// follows. Phase 11C's Interrupt does the same thing for the same reason, and
// the two measurements meet at the port.
func (b *BargeInDetector) Observe(
	ctx context.Context,
	v SignalView,
	d VADDecision,
	agentSpeaking bool,
	controller SpeechController,
) BargeInDecision {
	// An interruption resolves when the caller's speech run ends.
	if d.OffsetConfirmed {
		b.active = false
	}

	if !b.armed(d, agentSpeaking) {
		return BargeInDecision{}
	}

	// FIRST. Everything after this is inside the measurement.
	at := b.clock.Now()

	out := BargeInDecision{
		Detected:     true,
		Reason:       ReasonCallerSpoke,
		At:           at,
		OnsetLatency: v.Frame.End() - d.SpeechStart,
	}

	switch {
	case !b.policy.Enabled:
		out.Outcome = BargeInDisabled
		return out

	case b.policy.RequireAgentSpeaking && !agentSpeaking:
		out.Outcome = BargeInNotSpeaking
		return out

	case b.haveDelivered && at.Sub(b.lastDelivered) < b.policy.MinInterval:
		out.Outcome = BargeInDebounced
		return out
	}

	// STALENESS, measured on the frame's arrival rather than on media time: the
	// question is how long ago this audio reached the process, which is a wall
	// clock question. A frame with no arrival stamp is treated as fresh —
	// refusing it would make an unstamped test fixture look like a stale call.
	if !v.Frame.Arrival.IsZero() && at.Sub(v.Frame.Arrival) > b.policy.MaxAge {
		out.Outcome = BargeInStale
		return out
	}

	if controller == nil {
		// A CONFIGURATION FAULT, COUNTED RATHER THAN SWALLOWED. A deployment
		// that detects interruptions it cannot act on looks healthy on every
		// dashboard while talking over every caller.
		out.Outcome = BargeInNoController
		out.Err = ErrNoSpeechController
		return out
	}

	if err := controller.Interrupt(ctx, out.Reason); err != nil {
		// Phase 11C legitimately refuses when the turn has already moved on.
		// Not retried: a refusal means the thing to interrupt is gone.
		out.Outcome = BargeInRefused
		out.Err = err
		out.Latency = b.clock.Since(at)
		return out
	}

	b.lastDelivered = at
	b.haveDelivered = true
	b.active = true

	out.Outcome = BargeInDelivered
	out.Latency = b.clock.Since(at)
	return out
}

// armed reports whether this frame completes a detection.
//
// With the default ConfirmFrames of zero, a confirmed onset is a detection and
// nothing is held back — each extra frame here costs one frame interval against
// a 20 ms budget.
func (b *BargeInDetector) armed(d VADDecision, agentSpeaking bool) bool {
	if b.policy.ConfirmFrames <= 0 {
		return d.OnsetConfirmed
	}

	if d.OnsetConfirmed {
		b.pending = true
		b.pendingFrames = 0
		b.pendingSince = d.SpeechStart
		return false
	}

	if !b.pending {
		return false
	}

	// The extra evidence must be continuous speech. A run that ended during the
	// confirmation window was not an interruption.
	if !d.State.Active() {
		b.pending = false
		return false
	}
	_ = agentSpeaking

	b.pendingFrames++
	if b.pendingFrames >= b.policy.ConfirmFrames {
		b.pending = false
		return true
	}
	return false
}

// Reset returns the detector to its initial state.
//
// The debounce timestamp is deliberately NOT cleared: a session recovering
// mid-call should not immediately deliver a second interruption for the one it
// already delivered before the recovery.
func (b *BargeInDetector) Reset() {
	b.pending = false
	b.pendingFrames = 0
	b.pendingSince = 0
	b.active = false
}
