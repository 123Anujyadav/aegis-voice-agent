package conversation

import (
	"time"
)

// Plan is the engine's decision about what to do next.
//
// It carries no words. Deciding to ask a clarifying question is planning;
// choosing the sentence is prompt work, which the Phase 10B brief excludes. The
// layer above renders a Plan into an actual utterance.
type Plan struct {
	// Action is what to do.
	Action Action

	// Reason is a short machine-readable code explaining why. Never caller
	// content — it appears in logs and metric labels.
	Reason string

	// Clarification is populated when Action is Clarify, Confirm or Ask.
	Clarification Request

	// Expectation is the answer shape this plan establishes, used to set the
	// next constrained awaiting state.
	Expectation Expectation

	// NextState is the state to enter after the action is executed.
	NextState State

	// Intent is the intent being pursued, if any.
	Intent IntentName

	// Confidence is the planner's confidence in this decision, 0..1. Distinct
	// from intent confidence: a low-confidence intent can produce a
	// high-confidence decision to clarify.
	Confidence float64

	// Deadline is when the action must have completed.
	Deadline time.Time

	// Escalation names why escalation was chosen, when it was.
	Escalation string
}

// PlanInput is everything the planner considers.
//
// A struct, and the planner is a pure function of it. Given identical input the
// planner returns an identical Plan — no clock read, no map iteration, no
// randomness. That is what makes [TestPlanner_DecisionTable] able to enumerate
// the decision space, and what makes a conversation replay deterministically.
type PlanInput struct {
	// State the conversation is currently in.
	State State
	// Persona active.
	Persona Persona
	// Intent resolved from the latest utterance.
	Intent Intent
	// Verdict from the intent engine.
	Verdict IntentVerdict
	// Clarification assessed for this turn.
	Clarification Request
	// ClarificationAllowed reports whether the budget permits it.
	ClarificationAllowed bool
	// Expectation established by the previous agent turn.
	Expectation Expectation
	// EmergencyRaised reports whether emergency intent has been detected.
	EmergencyRaised bool
	// TurnCount is completed turns.
	TurnCount int
	// Elapsed is conversation wall time.
	Elapsed time.Duration
	// InterruptionCount is how many interruptions have occurred.
	InterruptionCount int
	// Deadline for the resulting action.
	Deadline time.Time
	// PolicyDenied carries a prior policy verdict when the planner is
	// re-planning after a denial, so it does not propose the same thing twice.
	PolicyDenied map[Action]bool
}

// Planner decides what to do next.
//
// It is stateless. Every fact it needs arrives in [PlanInput], which is what
// makes it a pure function and therefore exhaustively testable. A planner that
// held state would be a planner whose decisions depended on history nobody
// passed it.
type Planner struct {
	metrics *Metrics
}

// NewPlanner constructs a planner.
func NewPlanner(metrics *Metrics) *Planner {
	if metrics == nil {
		metrics = NewMetrics()
	}
	return &Planner{metrics: metrics}
}

// Plan decides the next action.
//
// The ladder below is the whole policy, in priority order. Each rung is a
// complete decision — the function returns rather than accumulating — so the
// reason for any outcome is exactly one condition, which is what makes a
// production decision explicable after the fact.
func (p *Planner) Plan(in PlanInput) Plan {
	plan := p.decide(in)
	plan.Deadline = in.Deadline
	plan.Intent = in.Intent.Name
	p.metrics.PlansProduced.Inc(plan.Action.String())
	return plan
}

func (p *Planner) decide(in PlanInput) Plan {
	denied := func(a Action) bool { return in.PolicyDenied != nil && in.PolicyDenied[a] }

	// 1 — Emergency outranks everything, including the conversation's own
	// state. U7: the product's job is to stop being in the way.
	if in.EmergencyRaised {
		return Plan{
			Action: ActionEscalate, Reason: "emergency_detected",
			NextState: StateEscalated, Confidence: 1.0, Escalation: "emergency",
		}
	}

	// 2 — A persona that escalates on uncertainty does so before anything
	// else that would prolong the conversation.
	if in.Persona.EscalateOnUncertainty && in.Verdict != IntentAccept {
		return Plan{
			Action: ActionEscalate, Reason: "persona_escalates_on_uncertainty",
			NextState: StateEscalated, Confidence: 0.9, Escalation: "uncertainty",
		}
	}

	// 3 — Hard boundaries. At the limit the only honest move is to stop.
	if in.Persona.MaxTurns > 0 && in.TurnCount >= in.Persona.MaxTurns {
		return p.boundaryExit(in, "max_turns_reached")
	}
	if in.Persona.MaxDuration > 0 && in.Elapsed >= in.Persona.MaxDuration {
		return p.boundaryExit(in, "max_duration_reached")
	}
	if in.InterruptionCount >= 6 {
		return p.boundaryExit(in, "interruption_storm")
	}

	// 4 — Noise. Not a low-confidence intent; the caller may not have spoken
	// to us at all. Ignoring a single noise event is usually right; the
	// clarification engine decides when to stop ignoring.
	if in.Verdict == IntentNoise {
		if in.Clarification.Kind == ClarifyNoise && in.ClarificationAllowed && !denied(ActionClarify) {
			return Plan{
				Action: ActionClarify, Reason: "noise_repeat",
				Clarification: in.Clarification, Expectation: ExpectNothing,
				NextState: StateSpeaking, Confidence: 0.6,
			}
		}
		if !in.ClarificationAllowed {
			return Plan{
				Action: ActionEscalate, Reason: "noise_unrecoverable",
				NextState: StateEscalated, Confidence: 0.7, Escalation: "noise",
			}
		}
		return Plan{Action: ActionIgnore, Reason: "noise", NextState: StateListening, Confidence: 0.5}
	}

	// 5 — Clarification, when one is warranted and affordable.
	//
	// Two clarification kinds are INDEPENDENT OF CLASSIFICATION CONFIDENCE, and
	// missing that was a real bug caught by test. A contradiction is exactly the
	// case where the utterance classified perfectly and conflicts with what we
	// already know — gating it on low confidence means a confident contradiction
	// is silently acted on, which is the worst possible handling. Truncation is
	// the same: an utterance cut off mid-sentence can classify confidently on
	// the fragment and still not mean what the fragment says.
	alwaysClarify := in.Clarification.Kind == ClarifyContradiction ||
		in.Clarification.Kind == ClarifyIncomplete

	if in.Clarification.Kind != ClarifyNone && (alwaysClarify || in.Verdict != IntentAccept) {
		if !in.ClarificationAllowed {
			// Budget spent. Escalating is the honest move; asking again is the
			// failure mode this engine exists to prevent.
			return Plan{
				Action: ActionEscalate, Reason: "clarification_exhausted",
				NextState: StateEscalated, Confidence: 0.8, Escalation: "clarification",
			}
		}
		action := ActionClarify
		switch in.Clarification.Kind {
		case ClarifyMissingSlot:
			action = ActionAsk
		case ClarifyLowConfidence, ClarifyContradiction:
			action = ActionConfirm
		}
		if denied(action) {
			return p.boundaryExit(in, "clarification_denied")
		}
		return Plan{
			Action: action, Reason: "clarify_" + in.Clarification.Kind.String(),
			Clarification: in.Clarification,
			Expectation:   in.Clarification.Kind.Expectation(),
			NextState:     StateSpeaking, Confidence: 0.75,
		}
	}

	// 6 — A rejected intent. The caller said something we understood well
	// enough to know we will not act on it.
	if in.Verdict == IntentReject {
		if in.Persona.Allows(CapEscalate) && !denied(ActionEscalate) {
			return Plan{
				Action: ActionEscalate, Reason: "intent_rejected",
				NextState: StateEscalated, Confidence: 0.6, Escalation: "rejected",
			}
		}
		return Plan{Action: ActionReject, Reason: "intent_rejected",
			NextState: StateListening, Confidence: 0.6}
	}

	// 7 — A confirmed denial. The caller said no, and "no" is an instruction.
	if in.Intent.Name == IntentDeny && in.Expectation == ExpectYesNo {
		return Plan{
			Action: ActionRespond, Reason: "confirmation_denied",
			Expectation: ExpectNothing, NextState: StateSpeaking, Confidence: 0.9,
		}
	}

	// 8 — An accepted, complete intent. Act on it.
	if in.Verdict == IntentAccept {
		if in.Intent.Name == IntentFallback {
			// The fallback intent means "we did not recognise this but must do
			// something". Responding is right; claiming to have understood is
			// not, and the reason code records which happened.
			return Plan{
				Action: ActionRespond, Reason: "fallback",
				NextState: StateSpeaking, Confidence: 0.4,
			}
		}
		if denied(ActionRespond) {
			return p.boundaryExit(in, "respond_denied")
		}
		return Plan{
			Action: ActionRespond, Reason: "intent_accepted",
			Expectation: ExpectNothing, NextState: StateSpeaking,
			Confidence: in.Intent.Confidence,
		}
	}

	// 9 — Nothing above matched. Waiting is the conservative default: it
	// neither speaks over the caller nor ends a call that may still be alive.
	return Plan{Action: ActionWait, Reason: "no_decision", NextState: StateWaiting, Confidence: 0.3}
}

// boundaryExit chooses how to leave when a limit is reached.
//
// Escalation is preferred over ending: a conversation that hit a boundary
// usually had an unmet need, and hanging up on it is worse than handing it to
// someone. Ending is the fallback when the persona cannot escalate.
func (p *Planner) boundaryExit(in PlanInput, reason string) Plan {
	if in.Persona.Allows(CapEscalate) {
		return Plan{
			Action: ActionEscalate, Reason: reason,
			NextState: StateEscalated, Confidence: 0.8, Escalation: reason,
		}
	}
	return Plan{Action: ActionEnd, Reason: reason, NextState: StateEnded, Confidence: 0.8}
}

// stateForExpectation returns the awaiting state a given expectation implies,
// which is how the planner's Expectation becomes the next conversation state
// once the agent has finished speaking.
func stateForExpectation(e Expectation) State {
	switch e {
	case ExpectDisambiguation:
		return StateClarification
	case ExpectYesNo:
		return StateConfirmation
	case ExpectSlotValue:
		return StateQuestion
	default:
		return StateListening
	}
}
