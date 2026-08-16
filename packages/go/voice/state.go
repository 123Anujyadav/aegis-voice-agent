package voice

// SessionState is one of the eleven states a voice session may occupy.
//
// A string rather than an integer. These values reach metric labels, event
// payloads and log lines; an integer enum renumbers itself the moment somebody
// inserts a constant in the middle, and every stored record then decodes to the
// wrong state.
type SessionState string

// The voice session states.
//
// The seven in the steady-state cycle, then four terminal or exceptional ones.
// §4 of the phase brief names all of them and requires that no transition be
// implicit; the declared table lives in [sessionTransitions].
const (
	// StateCreated is a session that exists but has not begun listening. The
	// initial state.
	StateCreated SessionState = "created"

	// StateListening is waiting for the caller to say something. The state a
	// healthy session spends most of its life in.
	StateListening SessionState = "listening"

	// StateSpeakingDetected is voice activity confirmed but recognition not yet
	// open.
	//
	// DISTINCT FROM Transcribing ON PURPOSE. Phase 11D confirms an onset before
	// any recognition stream exists, and collapsing the two would make "did we
	// hear them but fail to open a recogniser" unanswerable — which is exactly
	// the failure a provider outage produces.
	StateSpeakingDetected SessionState = "speaking_detected"

	// StateTranscribing is recognition in progress. Partial transcripts may be
	// arriving.
	StateTranscribing SessionState = "transcribing"

	// StateThinking is the conversation plan and the generation. Covers the
	// governance decision too, because a refusal is a thought that concluded
	// no.
	StateThinking SessionState = "thinking"

	// StateSynthesizing is text going to the synthesiser, before any audio has
	// come back.
	StateSynthesizing SessionState = "synthesizing"

	// StateSpeaking is outbound audio flowing to the caller. The state a
	// barge-in interrupts.
	StateSpeaking SessionState = "speaking"

	// StateInterrupted is the caller having spoken over the agent. Transient:
	// the session returns to listening once the interruption is orchestrated.
	StateInterrupted SessionState = "interrupted"

	// StateCancelled is a session ended by request. Terminal.
	StateCancelled SessionState = "cancelled"

	// StateFailed is a session ended by an unrecoverable fault. Terminal.
	StateFailed SessionState = "failed"

	// StateCompleted is a session that ended normally. Terminal.
	StateCompleted SessionState = "completed"
)

// AllSessionStates returns every state, cycle first then terminal.
func AllSessionStates() []SessionState {
	return []SessionState{
		StateCreated, StateListening, StateSpeakingDetected, StateTranscribing,
		StateThinking, StateSynthesizing, StateSpeaking, StateInterrupted,
		StateCancelled, StateFailed, StateCompleted,
	}
}

// String implements fmt.Stringer.
func (s SessionState) String() string { return string(s) }

// Valid reports whether the state is one of the declared eleven.
func (s SessionState) Valid() bool {
	for _, known := range AllSessionStates() {
		if s == known {
			return true
		}
	}
	return false
}

// Terminal reports whether nothing may follow.
func (s SessionState) Terminal() bool {
	switch s {
	case StateCancelled, StateFailed, StateCompleted:
		return true
	default:
		return false
	}
}

// AcceptsAudio reports whether inbound frames should still be analysed.
//
// # Almost everything, and the exceptions are the interesting part
//
// Inbound audio keeps flowing while the agent speaks — that is how a barge-in
// is heard at all — and while the engine is thinking, because a caller who adds
// something mid-thought has not stopped being worth listening to.
//
// Only a terminal session stops. A session that refused audio while
// synthesising would be deaf during exactly the window barge-in exists for.
func (s SessionState) AcceptsAudio() bool { return !s.Terminal() }

// AgentHoldsFloor reports whether the agent is producing or about to produce
// audio.
//
// What Phase 11D's barge-in detector is told. Synthesizing counts: the first
// frame may not have left yet, but the turn is committed and a caller talking
// now is interrupting.
func (s SessionState) AgentHoldsFloor() bool {
	return s == StateSynthesizing || s == StateSpeaking
}
