package telephony

import (
	"context"
	"fmt"
)

// Carrier-observed transitions.
//
// PHASE 19 P1-5. [CallLifecycle.Accept] and [CallLifecycle.Disconnect] are
// COMMANDS: each tells the carrier to act (Provider.Answer, Provider.Hangup)
// and then records the result. When the carrier reports that it has ALREADY
// answered or ended a call, issuing those commands again would be wrong — a
// second answer, or a hangup sent for a call the carrier just told us is over.
//
// The two methods here record the observation and issue NO provider command.
// They move the call only along edges [transitionSpec] already declares; they
// add no state and no edge.
//
// # What "answered" means here
//
// [StateAccepted]: the call will be connected, the media path is not yet up.
// It is NOT [StateConnected]: Connected means "a provider reported the media
// path established" ([CallLifecycle.Connect]), and a carrier's answer report
// says nothing about media. Moving to Connected on an answer would fabricate a
// media path nobody reported.
//
// # Reason codes
//
// Fixed codes, "remote_answered" and "remote_ended". The carrier's own reason
// for ending a call is not represented by this model, and inventing one would
// put a carrier's vocabulary into the lifecycle.

// Reason codes recorded by the observation methods.
const (
	ReasonRemoteAnswered = "remote_answered"
	ReasonRemoteEnded    = "remote_ended"

	// reasonInboundAnswer is the refusal recorded for an inbound call: the
	// platform answers inbound calls itself, through Accept, so a carrier
	// claiming to have answered one is not an observation this method can
	// record.
	reasonInboundAnswer = "inbound_answer_requires_accept"
)

// answered reports whether a call in s has already been answered.
//
// Escalated is deliberately absent: Screening → Escalated happens before any
// answer, so an escalated call's answer status is not knowable from its state.
func answered(s CallState) bool {
	switch s {
	case StateAccepted, StateConnected, StateMuted, StateHold, StateTransferred:
		return true
	default:
		return false
	}
}

// ObserveRemoteAnswer records that the carrier has answered an OUTBOUND call:
// Ringing or Screening → Accepted. It never calls the provider.
//
// Idempotent: a call that is already answered (Accepted, Connected, Muted,
// Hold, Transferred) is left untouched and nil is returned, including when a
// concurrent observation of the same answer won the race.
//
// Errors:
//   - an unknown or already-ended call: [ErrCallNotFound];
//   - an inbound call not yet accepted, or a state from which Accepted is not
//     declared (Rejected, Timeout, Escalated, Recovery): a [TransitionError]
//     wrapping [ErrInvalidTransition]. The call's state is unchanged.
func (l *CallLifecycle) ObserveRemoteAnswer(ctx context.Context, id CallID) error {
	sess, err := l.registry.Get(id)
	if err != nil {
		return err
	}
	from := sess.State()
	if answered(from) {
		return nil
	}
	if sess.Context().Direction != DirectionOutbound {
		l.metrics.InvalidTransitions.Inc(string(from), string(StateAccepted))
		return &TransitionError{Call: id, From: from, To: StateAccepted, Reason: reasonInboundAnswer}
	}
	if err := l.transition(ctx, sess, StateAccepted, ReasonRemoteAnswered); err != nil {
		if answered(sess.State()) {
			return nil // an identical observation got there first
		}
		return err
	}
	return nil
}

// ObserveRemoteEnd records that the carrier has ended a call: → Ended, from any
// state that declares that edge (Connected, Muted, Hold, Transferred,
// Escalated, Rejected, Timeout, Recovery). It never calls the provider.
//
// It concludes the call exactly as [CallLifecycle.Disconnect] does — metrics,
// terminal event, registry removal — minus the hangup. Like Disconnect, it does
// NOT release the call's capacity slot; [CallCoordinator.ObserveRemoteEnd] is
// the counterpart that does, and is the path a runtime should use.
//
// Errors:
//   - an unknown call, or one already removed after ending:
//     [ErrCallNotFound]. The registry does not distinguish the two;
//   - a call that ended concurrently: an error wrapping [ErrTerminal];
//   - a state with no declared Ended edge (Idle, Incoming, Ringing, Screening,
//     Accepted): a [TransitionError] wrapping [ErrInvalidTransition]. Those
//     calls can only conclude as Failed, and choosing that on the carrier's
//     behalf would invent a reason; the state is left unchanged.
func (l *CallLifecycle) ObserveRemoteEnd(ctx context.Context, id CallID) error {
	sess, err := l.registry.Get(id)
	if err != nil {
		return err
	}
	if err := l.end(ctx, sess, StateEnded, ReasonRemoteEnded); err != nil {
		if sess.Terminal() {
			return fmt.Errorf("%w: %s is already %s: %w", ErrTerminal, id, sess.State(), err)
		}
		return err
	}
	return nil
}

// ObserveRemoteEnd is the coordinator's counterpart to
// [CallLifecycle.ObserveRemoteEnd]: it records the carrier-reported end and
// then releases the call's capacity slot and snapshot, exactly as
// [CallCoordinator.End] does after a commanded hangup. The slot is released
// only by the call that actually ended the session, so concurrent observations
// release it once.
func (c *CallCoordinator) ObserveRemoteEnd(ctx context.Context, id CallID) error {
	sess, err := c.rt.registry.Get(id)
	if err != nil {
		return err
	}
	provider := sess.Context().Provider

	if err := c.rt.lifecycle.ObserveRemoteEnd(ctx, id); err != nil {
		return err
	}
	c.rt.scheduler.Release(provider)
	if c.rt.store != nil {
		_ = c.rt.store.Delete(ctx, id)
	}
	return nil
}
