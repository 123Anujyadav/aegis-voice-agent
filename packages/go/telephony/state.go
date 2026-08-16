package telephony

import (
	"fmt"
	"sort"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// CallState is one of the fifteen states a call may occupy.
//
// A string rather than an integer, deliberately. These values reach metric
// labels, event payloads, log lines and persisted snapshots. An integer enum
// renumbers itself the moment somebody inserts a constant in the middle, and
// every stored snapshot then decodes to the wrong state — silently, and in a
// system whose whole job is knowing what state a call is in.
type CallState string

// The fifteen call states.
//
// Ordered as a call travels, not alphabetically, so the declaration reads as
// the lifecycle it describes.
const (
	// StateIdle is a session that exists but has not begun. The initial state
	// for every new call.
	StateIdle CallState = "idle"

	// StateIncoming is an inbound call the provider has offered and the runtime
	// has accepted responsibility for, before alerting begins.
	StateIncoming CallState = "incoming"

	// StateRinging is alerting: inbound, the callee is being alerted; outbound,
	// the remote party is.
	StateRinging CallState = "ringing"

	// StateScreening is the state that gives this platform its name. The call
	// is being evaluated before a human is disturbed. Entered from Ringing and
	// exits to Accepted, Rejected or Escalated.
	StateScreening CallState = "screening"

	// StateAccepted means the call will be connected. Distinct from Connected:
	// the decision has been made, the media path is not yet up. Collapsing the
	// two would make "we accepted but failed to connect" unrepresentable, and
	// that gap is precisely where carrier faults live.
	StateAccepted CallState = "accepted"

	// StateRejected means the call will not be connected. NOT TERMINAL — a
	// rejected call still requires teardown, and modelling rejection as the end
	// would leave the teardown unmodelled. Exits to Ended.
	StateRejected CallState = "rejected"

	// StateConnected means the media path is up. This runtime does not carry
	// media; it records that a provider reported the path established.
	StateConnected CallState = "connected"

	// StateMuted is connected with outbound audio suppressed.
	StateMuted CallState = "muted"

	// StateHold is connected with the far end parked.
	StateHold CallState = "hold"

	// StateTransferred means the call has been handed to another destination.
	StateTransferred CallState = "transferred"

	// StateEscalated means a human has been brought in. Reversible: an
	// escalation that resolves returns to Connected, which is why this is not
	// terminal.
	StateEscalated CallState = "escalated"

	// StateTimeout means a lifecycle deadline expired. NOT TERMINAL: a timed-out
	// call may be recoverable, and always requires teardown.
	StateTimeout CallState = "timeout"

	// StateRecovery is a session reconstructed from a snapshot after a restart,
	// deciding whether it can resume. Only ever an INITIAL state — nothing
	// transitions into it, because a live session that needs recovery is a
	// session this process already lost.
	StateRecovery CallState = "recovery"

	// StateEnded is normal termination. Terminal.
	StateEnded CallState = "ended"

	// StateFailed is abnormal termination. Terminal.
	StateFailed CallState = "failed"
)

// AllStates returns every state in lifecycle order.
func AllStates() []CallState {
	return []CallState{
		StateIdle, StateIncoming, StateRinging, StateScreening,
		StateAccepted, StateRejected, StateConnected, StateMuted,
		StateHold, StateTransferred, StateEscalated, StateTimeout,
		StateRecovery, StateEnded, StateFailed,
	}
}

// String implements fmt.Stringer.
func (s CallState) String() string { return string(s) }

// Valid reports whether the state is one of the declared fifteen.
func (s CallState) Valid() bool {
	for _, known := range AllStates() {
		if s == known {
			return true
		}
	}
	return false
}

// Terminal reports whether nothing may follow this state.
//
// Only two states are terminal. Rejected and Timeout deliberately are not: both
// are outcomes that still require teardown, and a model in which "rejected" is
// the end of the story has no place to put the teardown that actually follows.
func (s CallState) Terminal() bool {
	return s == StateEnded || s == StateFailed
}

// Active reports whether the call is occupying provider resources.
//
// The predicate a capacity check uses. Idle and the terminal states are not
// active; everything between is, including Rejected and Timeout — because a
// rejected call still holds a channel until teardown completes, and a scheduler
// that assumed otherwise would over-admit.
func (s CallState) Active() bool {
	switch s {
	case StateIdle, StateEnded, StateFailed:
		return false
	default:
		return true
	}
}

// Connected reports whether media is established, in any of its variants.
func (s CallState) Connected() bool {
	switch s {
	case StateConnected, StateMuted, StateHold:
		return true
	default:
		return false
	}
}

// transitionSpec is THE declaration of every legal move.
//
// This table is the single source of truth for the call model. Nothing else in
// this package decides whether a transition is allowed, and no code path
// assigns a state directly — [runtime.FSM] refuses anything absent here.
//
// Read it as the lifecycle:
//
//	idle ──────▶ incoming ─▶ ringing ─▶ screening ─┬▶ accepted ─▶ connected
//	  └────────▶ ringing (outbound dial)           ├▶ rejected ──▶ ended
//	                                               └▶ escalated
//	connected ⇄ muted ⇄ hold ─▶ transferred ─▶ ended
//	  └──▶ escalated ─▶ connected | transferred | ended
//	anything live ─▶ timeout ─▶ ended | (recovery is initial-only)
//	anything live ─▶ failed (terminal)
func transitionSpec() map[CallState][]CallState {
	return map[CallState][]CallState{
		// A new session goes to Incoming for an inbound call, or straight to
		// Ringing for an outbound one — we dial, and the remote alerts.
		StateIdle: {StateIncoming, StateRinging, StateFailed, StateTimeout},

		// An offered call may be rejected before it ever alerts, which is what
		// a blocklist hit looks like.
		StateIncoming: {StateRinging, StateScreening, StateRejected, StateFailed, StateTimeout},

		// Ringing may go straight to Accepted when screening is disabled for
		// this callee, which is a supported configuration and not a bypass.
		StateRinging: {StateScreening, StateAccepted, StateRejected, StateFailed, StateTimeout},

		// The three screening outcomes, plus the two failure paths.
		StateScreening: {StateAccepted, StateRejected, StateEscalated, StateFailed, StateTimeout},

		// Accepted → Connected is where a carrier fault shows up as Failed.
		StateAccepted: {StateConnected, StateFailed, StateTimeout},

		// Rejection still requires teardown.
		StateRejected: {StateEnded, StateFailed},

		StateConnected: {StateMuted, StateHold, StateTransferred, StateEscalated,
			StateEnded, StateFailed, StateTimeout},

		// A muted call cannot be transferred without first unmuting. Transfer
		// with suppressed audio hands the far end a silent leg, and the
		// operator cannot tell that from a broken one.
		StateMuted: {StateConnected, StateHold, StateEnded, StateFailed, StateTimeout},

		StateHold: {StateConnected, StateMuted, StateTransferred, StateEnded,
			StateFailed, StateTimeout},

		// Once handed off, this runtime's involvement ends one way or another.
		StateTransferred: {StateEnded, StateFailed},

		// Escalation is reversible: a human who resolves it hands the call back.
		StateEscalated: {StateConnected, StateTransferred, StateEnded, StateFailed},

		// A timeout is an outcome, not an end. It tears down, or a recovery
		// pass concludes it.
		StateTimeout: {StateEnded, StateFailed},

		// Recovery is initial-only — nothing transitions INTO it — and exits to
		// a resumed call or a conclusion.
		StateRecovery: {StateConnected, StateEnded, StateFailed},

		StateEnded:  nil,
		StateFailed: nil,
	}
}

// newCallFSM builds the state machine for one call.
//
// initial is StateIdle for a new call and StateRecovery for one restored from a
// snapshot. Any other initial state is refused: those are the only two ways a
// call legitimately begins, and allowing an arbitrary start would let a caller
// fabricate a connected call that never rang.
func newCallFSM(initial CallState, clock rt.Clock) (*rt.FSM[CallState], error) {
	if initial != StateIdle && initial != StateRecovery {
		return nil, fmt.Errorf("%w: a call may begin at %s or %s, not %s",
			ErrInvalidTransition, StateIdle, StateRecovery, initial)
	}

	spec := rt.FSMSpec[CallState]{
		Initial:     initial,
		Transitions: transitionSpec(),
		Terminal:    []CallState{StateEnded, StateFailed},
	}
	return rt.NewFSM(spec, clock)
}

// CanTransition reports whether from → to is declared.
//
// Exported so a caller can ask before attempting, which matters on the
// provider-facing path: a carrier callback arriving late for a call that has
// already ended should be discarded quietly rather than logged as an error.
func CanTransition(from, to CallState) bool {
	for _, allowed := range transitionSpec()[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// TransitionsFrom returns the states reachable from one state, sorted.
func TransitionsFrom(s CallState) []CallState {
	out := append([]CallState(nil), transitionSpec()[s]...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
