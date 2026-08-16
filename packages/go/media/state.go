package media

import (
	"fmt"
	"sort"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// StreamState is one of the nine states a stream may occupy.
//
// A string rather than an integer. These values reach metric labels, event
// payloads, log lines and persisted snapshots; an integer enum renumbers itself
// the moment somebody inserts a constant in the middle, and every stored
// snapshot then decodes to the wrong state.
type StreamState string

// The nine stream states, in lifecycle order.
const (
	// StateIdle is a created stream that has not started. The initial state.
	StateIdle StreamState = "idle"

	// StateOpening is acquiring resources — buffer allocation, source
	// attachment. Distinct from Active because a stream that failed to open
	// never carried a frame, and collapsing the two makes "did we ever receive
	// audio" unanswerable.
	StateOpening StreamState = "opening"

	// StateActive is carrying frames. The only state in which a write is
	// accepted.
	StateActive StreamState = "active"

	// StatePaused has stopped accepting frames but retains its buffer.
	//
	// The buffer is DELIBERATELY retained: a pause is expected to be brief, and
	// discarding buffered audio on pause would make every pause audible as a
	// gap on resume.
	StatePaused StreamState = "paused"

	// StateRecovering is reattaching a source after a fault or a restart.
	StateRecovering StreamState = "recovering"

	// StateClosing is draining. Writes are refused; reads continue until the
	// buffer empties, which is what makes Drain different from Stop.
	StateClosing StreamState = "closing"

	// StateClosed is a normally terminated stream. Terminal.
	StateClosed StreamState = "closed"

	// StateFailed is an abnormally terminated stream. Terminal.
	StateFailed StreamState = "failed"

	// StateTimeout is a stream whose source stopped producing.
	//
	// NOT TERMINAL. A timed-out stream may recover — a source that stalled for
	// two seconds may come back — and it still holds a buffer that must be
	// released. Making it terminal would leave the release unmodelled.
	StateTimeout StreamState = "timeout"
)

// AllStates returns every state in lifecycle order.
func AllStates() []StreamState {
	return []StreamState{
		StateIdle, StateOpening, StateActive, StatePaused, StateRecovering,
		StateClosing, StateClosed, StateFailed, StateTimeout,
	}
}

// String implements fmt.Stringer.
func (s StreamState) String() string { return string(s) }

// Valid reports whether the state is one of the declared nine.
func (s StreamState) Valid() bool {
	for _, known := range AllStates() {
		if s == known {
			return true
		}
	}
	return false
}

// Terminal reports whether nothing may follow.
//
// Only Closed and Failed. Timeout deliberately is not — see its comment.
func (s StreamState) Terminal() bool { return s == StateClosed || s == StateFailed }

// AcceptsFrames reports whether a write may be accepted.
//
// ONLY Active. Not Opening — resources are not ready. Not Paused — that is what
// a pause means. Not Closing — that is what draining means. Not Recovering —
// the source is being reattached and its sequence numbers are not yet trusted.
func (s StreamState) AcceptsFrames() bool { return s == StateActive }

// DeliversFrames reports whether a read may return buffered frames.
//
// Active, Paused and Closing. Paused and Closing both still deliver: a paused
// stream's consumer may drain what was already buffered, and a closing stream
// delivering nothing would discard exactly the audio the drain exists to save.
func (s StreamState) DeliversFrames() bool {
	switch s {
	case StateActive, StatePaused, StateClosing:
		return true
	default:
		return false
	}
}

// HoldsResources reports whether the stream still occupies a buffer.
//
// The predicate a capacity check uses. Everything except Idle and the terminal
// states — including Timeout, because a timed-out stream's buffer is not freed
// until it concludes.
func (s StreamState) HoldsResources() bool {
	switch s {
	case StateIdle, StateClosed, StateFailed:
		return false
	default:
		return true
	}
}

// transitionSpec is THE declaration of every legal move.
//
// The single source of truth for the stream model. Nothing else decides whether
// a transition is allowed, and no code path assigns a state directly —
// [runtime.FSM] refuses anything absent here.
//
//	idle → opening → active ⇄ paused
//	                   ↓  ⇅ recovering
//	                 closing → closed
//	any live state → timeout → recovering | closing | failed
//	any live state → failed (terminal)
func transitionSpec() map[StreamState][]StreamState {
	return map[StreamState][]StreamState{
		// A stream may be abandoned before it opens, which is what a call that
		// ends during setup produces.
		StateIdle: {StateOpening, StateClosing, StateFailed},

		// Opening may time out: a source that never attaches is the common
		// carrier-side fault.
		StateOpening: {StateActive, StateClosing, StateFailed, StateTimeout},

		StateActive: {StatePaused, StateRecovering, StateClosing, StateFailed, StateTimeout},

		// A paused stream may be resumed, closed, or found to have died. It
		// cannot go straight to Recovering: recovery reattaches a source, and a
		// paused stream's source is still attached — going through Active makes
		// the reattachment explicit.
		StatePaused: {StateActive, StateClosing, StateFailed, StateTimeout},

		// Recovery either succeeds back to Active or gives up.
		StateRecovering: {StateActive, StateClosing, StateFailed, StateTimeout},

		// Draining ends one way or the other. It cannot return to Active: a
		// drain is a commitment, and a stream that resumed mid-drain would
		// deliver frames a consumer had already been told were the last.
		StateClosing: {StateClosed, StateFailed},

		// A timed-out stream may come back, drain, or be declared failed.
		StateTimeout: {StateRecovering, StateClosing, StateFailed},

		StateClosed: nil,
		StateFailed: nil,
	}
}

// newStreamFSM builds the state machine for one stream.
//
// initial is StateIdle for a new stream and StateRecovering for one restored
// from a snapshot. Any other is refused: those are the only two ways a stream
// legitimately begins, and allowing an arbitrary start would let a caller
// fabricate an active stream that never opened.
func newStreamFSM(initial StreamState, clock rt.Clock) (*rt.FSM[StreamState], error) {
	if initial != StateIdle && initial != StateRecovering {
		return nil, fmt.Errorf("%w: a stream may begin at %s or %s, not %s",
			ErrInvalidTransition, StateIdle, StateRecovering, initial)
	}

	spec := rt.FSMSpec[StreamState]{
		Initial:     initial,
		Transitions: transitionSpec(),
		Terminal:    []StreamState{StateClosed, StateFailed},
	}
	return rt.NewFSM(spec, clock)
}

// CanTransition reports whether from → to is declared.
//
// Exported so a caller can ask before attempting. That matters on the
// source-facing path: a late signal for a stream that has already closed should
// be discarded quietly rather than logged as an error.
func CanTransition(from, to StreamState) bool {
	for _, allowed := range transitionSpec()[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// TransitionsFrom returns the states reachable from one state, sorted.
func TransitionsFrom(s StreamState) []StreamState {
	out := append([]StreamState(nil), transitionSpec()[s]...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
