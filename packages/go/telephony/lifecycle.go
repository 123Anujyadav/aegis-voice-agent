package telephony

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// CallLifecycle drives a call through its states.
//
// # One place where a transition, its event and its metric happen together
//
// Every state change goes through [CallLifecycle.transition]. That function
// moves the FSM, records the metric, and publishes the event — in that order,
// and never partially. Scattering those three across the call sites that
// perform transitions is how a system ends up with a call that ended and an
// event stream that says it did not.
//
// # A publisher failure never stops a call
//
// The ordering above is deliberate: the FSM moves FIRST, and the publish
// happens after and cannot undo it. If Kafka is down, calls continue to
// connect, transfer and end; the events are lost and counted as lost.
//
// The alternative — failing the transition when the publish fails — sounds
// safer and is catastrophic. It means a broker outage prevents calls from
// ENDING, so every call in progress stays in Connected, the registry fills,
// capacity is exhausted, and an outage in an observability system becomes an
// outage in the phone system. Telemetry must never be load-bearing for the
// thing it observes.
type CallLifecycle struct {
	registry  *CallRegistry
	publisher Publisher
	metrics   *RuntimeMetrics
	clock     rt.Clock
	logger    *slog.Logger
	providers *providerRegistry
	cfg       Config

	// seq numbers events per call, so a consumer can detect a gap.
	seqs *sequencer
}

// sequencer hands out per-call event sequence numbers.
type sequencer struct {
	reg *CallRegistry
}

// next returns the next sequence number for a call.
//
// Derived from the session's transition count rather than a shared counter,
// because a shared counter across calls would be a single contended atomic on
// every transition — and would number events globally, which tells a consumer
// nothing about whether IT missed one.
func (s *sequencer) next(sess *CallSession) int {
	return sess.HistoryLen()
}

// Incoming registers an inbound call and moves it to Incoming.
//
// The entry point a provider adapter calls when a carrier offers a call. It
// does NOT answer: answering is a decision the screening layer makes, and a
// runtime that answered on arrival would defeat the platform's purpose.
func (l *CallLifecycle) Incoming(ctx context.Context, cc CallContext) (*CallSession, error) {
	if cc.Direction != DirectionInbound {
		return nil, invariant("INV-TEL-2",
			"Incoming requires an inbound context, got %s", cc.Direction)
	}
	return l.begin(ctx, cc, StateIncoming, "carrier_offered")
}

// Outgoing registers an outbound call and moves it to Ringing.
//
// Straight to Ringing, with no Incoming state: an outbound call has no arrival,
// and modelling one would put every outbound call through a state that means
// nothing for it.
func (l *CallLifecycle) Outgoing(ctx context.Context, cc CallContext) (*CallSession, error) {
	if cc.Direction != DirectionOutbound {
		return nil, invariant("INV-TEL-2",
			"Outgoing requires an outbound context, got %s", cc.Direction)
	}
	if !cc.Capabilities.Has(CapDial) {
		return nil, fmt.Errorf("%w: provider %s cannot dial", ErrCapabilityUnsupported, cc.Provider)
	}
	return l.begin(ctx, cc, StateRinging, "dialed")
}

func (l *CallLifecycle) begin(ctx context.Context, cc CallContext,
	first CallState, reason string) (*CallSession, error) {
	if _, err := l.providers.get(cc.Provider); err != nil {
		return nil, err
	}

	sess, err := l.registry.Create(cc, l.clock)
	if err != nil {
		return nil, err
	}

	if err := l.transition(ctx, sess, first, reason); err != nil {
		// The session exists but could not begin. Remove it rather than leave
		// an Idle session in the registry consuming a capacity slot forever.
		l.registry.Remove(sess.ID())
		return nil, err
	}

	l.metrics.CallsStarted.Inc(string(cc.Direction), string(cc.Provider))
	return sess, nil
}

// Ring moves a call to Ringing.
func (l *CallLifecycle) Ring(ctx context.Context, id CallID) error {
	return l.move(ctx, id, StateRinging, "alerting")
}

// Screen moves a call into screening.
func (l *CallLifecycle) Screen(ctx context.Context, id CallID) error {
	return l.move(ctx, id, StateScreening, "screening_started")
}

// Accept accepts a call and asks the provider to answer.
//
// # The provider is called BEFORE the state moves
//
// The opposite of the publish ordering, and for the opposite reason. A publish
// is telemetry and must not gate the call; answering is the call. Moving to
// Accepted before the carrier confirmed would produce a session that believes
// it answered a call the carrier never connected — and the next thing that
// session does is wait forever for media that is not coming.
func (l *CallLifecycle) Accept(ctx context.Context, id CallID, reason string) error {
	sess, err := l.registry.Get(id)
	if err != nil {
		return err
	}
	if !sess.Context().Capabilities.Has(CapAnswer) {
		return fmt.Errorf("%w: answer", ErrCapabilityUnsupported)
	}

	prov, err := l.providers.get(sess.Context().Provider)
	if err != nil {
		return err
	}

	pctx, cancel := context.WithTimeout(ctx, l.cfg.ProviderTimeout)
	defer cancel()

	if err := prov.Answer(pctx, id); err != nil {
		// The carrier refused or timed out. The call has failed, and saying so
		// is better than leaving it in Ringing until the lifecycle timeout
		// sweeps it — a failure with a cause beats a timeout without one.
		_ = l.transition(ctx, sess, StateFailed, "provider_answer_failed")
		l.metrics.ProviderErrors.Inc(string(sess.Context().Provider), "answer")
		return fmt.Errorf("telephony: provider answer failed for %s: %w", id, err)
	}

	return l.transition(ctx, sess, StateAccepted, reason)
}

// Reject declines a call and asks the provider to reject it.
func (l *CallLifecycle) Reject(ctx context.Context, id CallID, reason string) error {
	sess, err := l.registry.Get(id)
	if err != nil {
		return err
	}
	if err := checkReasonCode(reason); err != nil {
		return err
	}

	prov, err := l.providers.get(sess.Context().Provider)
	if err != nil {
		return err
	}

	pctx, cancel := context.WithTimeout(ctx, l.cfg.ProviderTimeout)
	defer cancel()

	// A provider that fails to reject is logged and the call is still moved to
	// Rejected. Unlike answering, rejection is a decision the platform has
	// already made: the caller will not be connected whatever the carrier says,
	// and leaving the session in Ringing because a REST call failed would hold
	// a screening decision hostage to the carrier's availability.
	if err := prov.Reject(pctx, id, reason); err != nil {
		l.metrics.ProviderErrors.Inc(string(sess.Context().Provider), "reject")
		l.logger.WarnContext(ctx, "provider reject failed; rejecting locally anyway",
			slog.String("call_id", string(id)), slog.String("error", err.Error()))
	}

	return l.transition(ctx, sess, StateRejected, reason)
}

// Connect moves an accepted call to Connected.
//
// Called by a provider adapter when media is established. This runtime does not
// carry media and does not verify the claim; it records that the provider made
// it.
func (l *CallLifecycle) Connect(ctx context.Context, id CallID) error {
	return l.move(ctx, id, StateConnected, "media_established")
}

// Mute, Unmute, Hold and Resume are the in-call controls.
func (l *CallLifecycle) Mute(ctx context.Context, id CallID) error {
	return l.controlled(ctx, id, StateMuted, CapMute, "muted")
}

// Unmute returns a muted call to Connected.
func (l *CallLifecycle) Unmute(ctx context.Context, id CallID) error {
	return l.move(ctx, id, StateConnected, "unmuted")
}

// Hold parks a call.
func (l *CallLifecycle) Hold(ctx context.Context, id CallID) error {
	return l.controlled(ctx, id, StateHold, CapHold, "held")
}

// Unhold returns a held call to Connected.
func (l *CallLifecycle) Unhold(ctx context.Context, id CallID) error {
	return l.move(ctx, id, StateConnected, "unheld")
}

func (l *CallLifecycle) controlled(ctx context.Context, id CallID, to CallState,
	need Capability, reason string) error {
	sess, err := l.registry.Get(id)
	if err != nil {
		return err
	}
	if !sess.Context().Capabilities.Has(need) {
		return fmt.Errorf("%w: %s", ErrCapabilityUnsupported, need)
	}
	return l.transition(ctx, sess, to, reason)
}

// Transfer hands a call to another destination.
//
// Records a new leg. A transfer from Muted is refused by the transition table
// rather than by a check here — the table says Muted cannot reach Transferred,
// because transferring with suppressed audio hands the far end a silent leg
// that is indistinguishable from a broken one.
func (l *CallLifecycle) Transfer(ctx context.Context, id CallID, reason string) (LegID, error) {
	sess, err := l.registry.Get(id)
	if err != nil {
		return "", err
	}
	if !sess.Context().Capabilities.Has(CapTransfer) {
		return "", fmt.Errorf("%w: transfer", ErrCapabilityUnsupported)
	}

	// THE TRANSITION FIRST, THEN THE LEG.
	//
	// The first version minted and recorded the leg before attempting the
	// transition, so a REFUSED transfer — from Muted, say — still left a leg on
	// the session. The call then looked transferred to anything counting legs,
	// while its state said otherwise, and the two disagreed permanently.
	// See ENGINEERING_AUDIT F2.
	if err := l.transition(ctx, sess, StateTransferred, reason); err != nil {
		return "", err
	}

	leg := NewLegID()
	sess.AddLeg(leg)
	l.metrics.Transfers.Inc(string(sess.Context().Provider))
	return leg, nil
}

// Escalate brings a human in.
func (l *CallLifecycle) Escalate(ctx context.Context, id CallID, reason string) error {
	sess, err := l.registry.Get(id)
	if err != nil {
		return err
	}
	if err := l.transition(ctx, sess, StateEscalated, reason); err != nil {
		return err
	}
	l.metrics.Escalations.Inc(string(sess.Context().Provider))
	return nil
}

// Deescalate returns an escalated call to Connected.
func (l *CallLifecycle) Deescalate(ctx context.Context, id CallID) error {
	return l.move(ctx, id, StateConnected, "escalation_resolved")
}

// Disconnect ends a call normally, asking the provider to hang up.
func (l *CallLifecycle) Disconnect(ctx context.Context, id CallID, reason string) error {
	sess, err := l.registry.Get(id)
	if err != nil {
		return err
	}
	if err := checkReasonCode(reason); err != nil {
		return err
	}

	if prov, perr := l.providers.get(sess.Context().Provider); perr == nil {
		pctx, cancel := context.WithTimeout(ctx, l.cfg.ProviderTimeout)
		// A hangup that the carrier refuses is logged and the call still ends.
		// A session that stayed live because a REST call failed would hold a
		// capacity slot until the lifecycle timeout, and the carrier has almost
		// certainly torn the call down regardless.
		if herr := prov.Hangup(pctx, id, reason); herr != nil {
			l.metrics.ProviderErrors.Inc(string(sess.Context().Provider), "hangup")
			l.logger.WarnContext(ctx, "provider hangup failed; ending locally anyway",
				slog.String("call_id", string(id)), slog.String("error", herr.Error()))
		}
		cancel()
	}

	return l.end(ctx, sess, StateEnded, reason)
}

// Fail ends a call abnormally.
func (l *CallLifecycle) Fail(ctx context.Context, id CallID, reason string) error {
	sess, err := l.registry.Get(id)
	if err != nil {
		return err
	}
	return l.end(ctx, sess, StateFailed, reason)
}

// Timeout moves a call to Timeout.
//
// Not terminal: a timed-out call still needs teardown, and [CallLifecycle.end]
// is what concludes it.
func (l *CallLifecycle) Timeout(ctx context.Context, id CallID, reason string) error {
	sess, err := l.registry.Get(id)
	if err != nil {
		return err
	}
	if err := l.transition(ctx, sess, StateTimeout, reason); err != nil {
		return err
	}
	l.metrics.Timeouts.Inc(string(sess.State()), string(sess.Context().Provider))
	return nil
}

// move is the ordinary transition path.
func (l *CallLifecycle) move(ctx context.Context, id CallID, to CallState, reason string) error {
	sess, err := l.registry.Get(id)
	if err != nil {
		return err
	}
	return l.transition(ctx, sess, to, reason)
}

// end concludes a call and removes it from the registry.
func (l *CallLifecycle) end(ctx context.Context, sess *CallSession,
	to CallState, reason string) error {
	if err := l.transition(ctx, sess, to, reason); err != nil {
		return err
	}

	cc := sess.Context()
	l.metrics.CallDuration.ObserveDuration(sess.Duration(),
		string(cc.Direction), string(to))
	if talk := sess.TalkDuration(); talk > 0 {
		l.metrics.TalkDuration.ObserveDuration(talk, string(cc.Direction))
	}
	if to == StateFailed {
		l.metrics.CallFailures.Inc(string(cc.Provider), reason)
	}
	l.metrics.CallsEnded.Inc(string(cc.Direction), string(to), reason)

	// Removed only after the terminal transition has been recorded and
	// published. Removing first would race a concurrent lookup into
	// ErrCallNotFound for a call that is legitimately still ending.
	l.registry.Remove(sess.ID())
	l.metrics.LiveCalls.Set(float64(l.registry.Len()))
	return nil
}

// transition is THE state-change path.
//
// Order: FSM, then metric, then event. The FSM is authoritative and moves
// first; nothing after it can fail the transition. See the type comment.
func (l *CallLifecycle) transition(ctx context.Context, sess *CallSession,
	to CallState, reason string) error {
	from := sess.State()
	started := l.clock.Now()

	if err := sess.Transition(to, reason); err != nil {
		l.metrics.InvalidTransitions.Inc(string(from), string(to))
		return err
	}

	l.metrics.Transitions.Inc(string(from), string(to))
	l.metrics.LifecycleLatency.ObserveDuration(l.clock.Now().Sub(started), string(to))
	l.metrics.LiveCalls.Set(float64(l.registry.Len()))

	l.emit(ctx, sess, from, to, reason)
	return nil
}

// emit publishes the event for a transition, if the state has one.
func (l *CallLifecycle) emit(ctx context.Context, sess *CallSession,
	from, to CallState, reason string) {
	typ, ok := stateEvent(to)
	if !ok {
		return
	}

	cc := sess.Context()
	e := Event{
		Type: typ,
		Call: sess.ID(), Session: sess.SessionID(), Correlation: sess.Correlation(),
		From: from, To: to, Reason: reason,
		Direction: cc.Direction, Channel: cc.Channel, Provider: cc.Provider,
		Tags:           append([]string(nil), cc.Tags...),
		DurationMillis: sess.Duration().Milliseconds(),
		TalkMillis:     sess.TalkDuration().Milliseconds(),
		Sequence:       l.seqs.next(sess),
		At:             l.clock.Now(),
	}

	l.publish(ctx, e)
}

// publish emits an event and counts a failure without propagating it.
func (l *CallLifecycle) publish(ctx context.Context, e Event) {
	// A publisher deadline separate from the caller's. A caller that passed a
	// cancelled context — a hung-up call tearing down — must still get its
	// terminal event out, and context.WithoutCancel is what separates the
	// publish's lifetime from the request's.
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.cfg.PublishTimeout)
	defer cancel()

	if err := l.publisher.Publish(pctx, e); err != nil {
		l.metrics.EventsDropped.Inc(string(e.Type))
		l.logger.WarnContext(ctx, "telephony event dropped",
			slog.String("call_id", string(e.Call)),
			slog.String("event", string(e.Type)),
			slog.String("error", err.Error()))
		return
	}
	l.metrics.EventsPublished.Inc(string(e.Type))
}

// SweepTimeouts moves calls past their lifecycle deadline to Timeout.
//
// Returns how many were swept. Called by the runtime's ticker and directly by
// tests, which is why it takes no ticker of its own — a sweep that owned its
// own timer could not be tested without waiting for it.
func (l *CallLifecycle) SweepTimeouts(ctx context.Context) int {
	now := l.clock.Now()
	var swept int

	l.registry.Each(func(sess *CallSession) bool {
		deadline := l.deadlineFor(sess.State())
		if deadline <= 0 {
			return true
		}
		if now.Sub(sess.UpdatedAt()) < deadline {
			return true
		}
		// TryTo semantics: a call that reached a terminal state between the
		// snapshot and here is not an error, it is a race this sweep expects.
		if err := l.transition(ctx, sess, StateTimeout, "lifecycle_deadline"); err == nil {
			l.metrics.Timeouts.Inc(string(sess.State()), string(sess.Context().Provider))
			swept++
		}
		return true
	})
	return swept
}

// deadlineFor returns how long a call may sit in a state.
//
// Zero means no deadline. Connected has none deliberately: a long call is a
// good call, and a runtime that hung up on hour-long conversations would be a
// worse product than one that occasionally leaks a session. Ringing and
// Screening have short deadlines because a call stuck there is a caller
// listening to silence.
func (l *CallLifecycle) deadlineFor(s CallState) time.Duration {
	switch s {
	case StateIdle, StateIncoming:
		return l.cfg.SetupTimeout
	case StateRinging:
		return l.cfg.RingTimeout
	case StateScreening:
		return l.cfg.ScreenTimeout
	case StateAccepted:
		return l.cfg.ConnectTimeout
	case StateRejected, StateTimeout, StateTransferred:
		return l.cfg.TeardownTimeout
	case StateEscalated:
		return l.cfg.EscalationTimeout
	case StateRecovery:
		return l.cfg.RecoveryTimeout
	default:
		// Connected, Muted, Hold: no deadline.
		return 0
	}
}

// ReapTerminal removes calls that reached Timeout and never concluded.
//
// The second half of the timeout story. SweepTimeouts moves a stalled call to
// Timeout and publishes it; without this, a call whose teardown never completed
// would sit in Timeout forever holding a capacity slot. Returns how many were
// concluded.
func (l *CallLifecycle) ReapTerminal(ctx context.Context) int {
	now := l.clock.Now()
	var reaped int

	l.registry.Each(func(sess *CallSession) bool {
		if sess.State() != StateTimeout {
			return true
		}
		if now.Sub(sess.UpdatedAt()) < l.cfg.TeardownTimeout {
			return true
		}
		if err := l.end(ctx, sess, StateEnded, "reaped_after_timeout"); err == nil {
			reaped++
		}
		return true
	})
	return reaped
}
