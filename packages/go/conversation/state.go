// Package conversation is the Conversation Intelligence Engine: a real-time
// conversation operating system for enterprise voice calls.
//
// It sits on top of the Phase 10A runtime (packages/go/runtime), which moves
// tokens. This package decides what a turn MEANS — who holds the floor, what
// was intended, what is permitted, and what to do next.
//
// # What this is not
//
// Not a chatbot and not an LLM wrapper. There is no prompt text in this
// package, no model is called from it, and no vendor is imported. It is the
// control plane of a conversation: a deterministic state machine, a turn
// arbiter, a policy evaluator and a planner.
//
// # The boundary with the runtime
//
//	runtime   : sessions, streaming, cancellation, providers, token budget
//	            — knows nothing about conversation
//	conversation: floor control, intent lifecycle, policy, planning, personas
//	            — knows nothing about tokens, vendors or transport
//
// The engine never calls a provider. It emits a [Plan]; an orchestration layer
// above turns that plan into a runtime generation. That inversion is what keeps
// this package free of prompts, models and telephony.
//
// # Explicitly not implemented
//
// Per the Phase 10B brief: LLM prompts, memory reasoning, tool execution,
// telephony intelligence, fraud intelligence, business workflows. Where the
// engine must refer to such a thing it does so through an interface it does not
// implement — [IntentClassifier] is the clearest example, and it has no
// implementation anywhere in this module by design.
//
// # Determinism
//
// Given the same event sequence and the same injected clock, the engine
// produces the same state trace, the same plans and the same metrics. Every
// decision function is pure or explicitly clock-driven; nothing consults wall
// time, a random source, or map iteration order. That property is what makes
// [Simulator] able to replay a conversation and assert on it.
package conversation

import (
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// State is a conversation state.
//
// Seventeen states, every transition declared. There are no implicit changes:
// [Conversation.transition] is the only writer, it goes through a
// runtime.FSM, and an undeclared transition returns an error rather than
// being logged and allowed.
type State int

const (
	// StateIdle is the state at construction, before anything has happened.
	StateIdle State = iota

	// StateGreeting is the opening turn. The agent holds the floor and the
	// caller cannot take it — see [TurnManager] and INV-CV-2.
	StateGreeting

	// StateListening is the unconstrained awaiting state: the caller holds the
	// floor and may say anything.
	StateListening

	// StateThinking means a decision is genuinely in flight — planning,
	// classification, or a model request. It is never entered speculatively.
	StateThinking

	// StateSpeaking means the agent holds the floor and output is streaming.
	StateSpeaking

	// StateClarification is a constrained awaiting state: the agent asked a
	// clarifying question and expects a disambiguating answer.
	StateClarification

	// StateConfirmation is a constrained awaiting state expecting a yes/no.
	StateConfirmation

	// StateQuestion is a constrained awaiting state expecting a specific slot
	// value.
	StateQuestion

	// StateToolExecution means the conversation is blocked on an external
	// action. This engine does not execute tools; it models the wait.
	StateToolExecution

	// StateWaiting is a deliberate pause — hold, or waiting on the caller —
	// distinct from Listening because no answer is expected imminently.
	StateWaiting

	// StateInterrupted means the floor is contested and arbitration is running.
	// It is deliberately a real state rather than a flag, so that "who is
	// speaking" is never ambiguous.
	StateInterrupted

	// StateTransferred is terminal: the call went to another party.
	StateTransferred

	// StateEscalated is terminal: the call went to a human or an emergency
	// path. Irreversible.
	StateEscalated

	// StateEnded is terminal and normal.
	StateEnded

	// StateTimeout is terminal: no activity within the configured window.
	StateTimeout

	// StateError is a recoverable fault. It is not terminal — that is the
	// difference between an error and a failure.
	StateError

	// StateRecovery is an active attempt to return to a usable state.
	StateRecovery
)

// String renders the state for logs, metrics and diagrams.
func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateGreeting:
		return "greeting"
	case StateListening:
		return "listening"
	case StateThinking:
		return "thinking"
	case StateSpeaking:
		return "speaking"
	case StateClarification:
		return "clarification"
	case StateConfirmation:
		return "confirmation"
	case StateQuestion:
		return "question"
	case StateToolExecution:
		return "tool_execution"
	case StateWaiting:
		return "waiting"
	case StateInterrupted:
		return "interrupted"
	case StateTransferred:
		return "transferred"
	case StateEscalated:
		return "escalated"
	case StateEnded:
		return "ended"
	case StateTimeout:
		return "timeout"
	case StateError:
		return "error"
	case StateRecovery:
		return "recovery"
	default:
		return "invalid"
	}
}

// IsTerminal reports whether no transition may follow.
func (s State) IsTerminal() bool {
	switch s {
	case StateTransferred, StateEscalated, StateEnded, StateTimeout:
		return true
	}
	return false
}

// IsAwaiting reports whether the caller holds the floor and the engine is
// waiting for them.
//
// The distinction between Listening and the three constrained awaiting states
// is the most load-bearing idea in this state machine. Listening accepts
// anything. Clarification, Confirmation and Question each carry an EXPECTATION,
// and the same utterance is interpreted differently depending on which is
// active — "yes" after a confirmation is an answer, "yes" while listening is an
// utterance to classify. Collapsing them into one state loses that, and it is
// why voice systems that model only "listening" mishandle every yes/no.
func (s State) IsAwaiting() bool {
	switch s {
	case StateListening, StateClarification, StateConfirmation, StateQuestion:
		return true
	}
	return false
}

// IsConstrained reports whether the engine is awaiting a specific answer shape.
func (s State) IsConstrained() bool {
	switch s {
	case StateClarification, StateConfirmation, StateQuestion:
		return true
	}
	return false
}

// AgentHoldsFloor reports whether the agent is producing output in this state.
func (s State) AgentHoldsFloor() bool {
	return s == StateGreeting || s == StateSpeaking
}

// AllStates returns every state, in declaration order. Used by the diagram
// generator and by exhaustive tests.
func AllStates() []State {
	return []State{
		StateIdle, StateGreeting, StateListening, StateThinking, StateSpeaking,
		StateClarification, StateConfirmation, StateQuestion, StateToolExecution,
		StateWaiting, StateInterrupted, StateTransferred, StateEscalated,
		StateEnded, StateTimeout, StateError, StateRecovery,
	}
}

// transitionTable declares every legal transition.
//
// This table IS the state machine. It is a single literal rather than a set of
// scattered guard clauses so that the whole machine can be read, diffed and
// diagrammed in one place — and so that adding a state without declaring its
// edges fails a test rather than producing a silently unreachable state.
//
// Three properties are asserted by TestStateMachine_TableIsWellFormed:
//   - every state except Idle is reachable from Idle
//   - every non-terminal state can reach a terminal state
//   - no terminal state has outgoing edges
func transitionTable() map[State][]State {
	return map[State][]State{
		// Nothing has happened yet. A conversation may end before it starts —
		// the caller hung up during setup — so Ended is reachable directly.
		StateIdle: {StateGreeting, StateEnded, StateError},

		// The greeting is non-yielding (INV-CV-2). A barge-in during it is
		// recorded and applied afterwards, so Interrupted is NOT reachable here.
		StateGreeting: {StateListening, StateEnded, StateError, StateEscalated},

		// The unconstrained awaiting state.
		StateListening: {
			StateThinking, StateInterrupted, StateWaiting, StateTimeout,
			StateEnded, StateError, StateEscalated, StateTransferred,
		},

		// Thinking always resolves into an action. It cannot loop back to
		// Listening: that would mean deciding to do nothing while the caller
		// waits in silence, which is the "dead air" failure.
		StateThinking: {
			StateSpeaking, StateToolExecution, StateWaiting, StateTransferred,
			StateEscalated, StateEnded, StateError, StateInterrupted,
		},

		// After speaking, the planner's action determines which awaiting state
		// we enter. This is where a constrained expectation is established.
		StateSpeaking: {
			StateListening, StateClarification, StateConfirmation, StateQuestion,
			StateWaiting, StateInterrupted, StateEnded, StateError,
			StateTransferred, StateEscalated,
		},

		// Constrained awaiting states. All three resolve into Thinking when an
		// answer arrives, because interpreting a constrained answer is itself a
		// decision.
		StateClarification: {
			StateThinking, StateInterrupted, StateTimeout, StateEnded,
			StateError, StateEscalated, StateTransferred,
		},
		StateConfirmation: {
			StateThinking, StateInterrupted, StateTimeout, StateEnded,
			StateError, StateEscalated, StateTransferred,
		},
		StateQuestion: {
			StateThinking, StateInterrupted, StateTimeout, StateEnded,
			StateError, StateEscalated, StateTransferred,
		},

		// Blocked on an external action this engine does not perform.
		StateToolExecution: {
			StateThinking, StateSpeaking, StateError, StateTimeout,
			StateEnded, StateEscalated,
		},

		// A deliberate pause.
		StateWaiting: {
			StateListening, StateThinking, StateTimeout, StateEnded,
			StateError, StateEscalated, StateTransferred,
		},

		// Arbitration. Resolves to whoever won the floor.
		StateInterrupted: {
			StateListening, StateSpeaking, StateThinking, StateEnded,
			StateError, StateEscalated, StateTransferred,
		},

		// Error is recoverable. Escalation is always available from it, because
		// an unrecoverable error on a live call must reach a human.
		StateError: {StateRecovery, StateEnded, StateEscalated},

		// Recovery either restores the conversation or gives up honestly.
		StateRecovery: {
			StateListening, StateGreeting, StateEnded, StateEscalated, StateError,
		},

		// Terminal states declare no outgoing edges.
		StateTransferred: {},
		StateEscalated:   {},
		StateEnded:       {},
		StateTimeout:     {},
	}
}

// terminalStates lists the states from which nothing may follow.
func terminalStates() []State {
	return []State{StateTransferred, StateEscalated, StateEnded, StateTimeout}
}

// newStateMachine builds the conversation FSM on the frozen runtime FSM.
//
// Reusing runtime.FSM rather than writing another one is deliberate: it already
// refuses undeclared transitions, refuses exit from terminal states, runs hooks
// outside its lock, and is race-tested. A second state machine in this package
// would be a second thing to get wrong.
func newStateMachine(clock rt.Clock) (*rt.FSM[State], error) {
	return rt.NewFSM(rt.FSMSpec[State]{
		Initial:     StateIdle,
		Transitions: transitionTable(),
		Terminal:    terminalStates(),
	}, clock)
}

// Trigger names why a transition happened.
//
// Every transition carries one. A state machine that records where it went but
// not why produces a trace nobody can debug — and on a voice call, "why did it
// stop listening" is the only question anyone ever asks.
type Trigger string

const (
	// TriggerStart begins the conversation.
	TriggerStart Trigger = "start"
	// TriggerGreetingComplete ends the opening turn.
	TriggerGreetingComplete Trigger = "greeting_complete"
	// TriggerUtterance is a completed caller utterance.
	TriggerUtterance Trigger = "utterance"
	// TriggerPlanned is the planner producing an action.
	TriggerPlanned Trigger = "planned"
	// TriggerSpeechComplete is the agent finishing its output.
	TriggerSpeechComplete Trigger = "speech_complete"
	// TriggerInterruption is any interruption kind.
	TriggerInterruption Trigger = "interruption"
	// TriggerArbitrated is the outcome of floor arbitration.
	TriggerArbitrated Trigger = "arbitrated"
	// TriggerToolStarted blocks on an external action.
	TriggerToolStarted Trigger = "tool_started"
	// TriggerToolComplete resumes from an external action.
	TriggerToolComplete Trigger = "tool_complete"
	// TriggerSilence is a detected silence window.
	TriggerSilence Trigger = "silence"
	// TriggerTimeout is a budget or inactivity expiry.
	TriggerTimeout Trigger = "timeout"
	// TriggerFault is an internal or provider error.
	TriggerFault Trigger = "fault"
	// TriggerRecovered is a successful recovery.
	TriggerRecovered Trigger = "recovered"
	// TriggerHangup is the caller ending the call.
	TriggerHangup Trigger = "hangup"
	// TriggerTransfer routes the call elsewhere.
	TriggerTransfer Trigger = "transfer"
	// TriggerEscalate hands the call to a human or emergency path.
	TriggerEscalate Trigger = "escalate"
)

// TransitionRecord is one entry in a conversation's state trace.
type TransitionRecord struct {
	// From is the state departed.
	From State
	// To is the state entered.
	To State
	// Trigger names the cause.
	Trigger Trigger
	// At is the instant on the engine's clock.
	At int64 // unix nanos, for cheap comparison and stable serialisation
	// Note carries a short machine-readable detail. Never caller content.
	Note string
}
