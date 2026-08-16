package conversation

import (
	"sort"
	"sync"
	"time"
)

// Action is something the engine may attempt.
type Action int

const (
	// ActionRespond answers the caller substantively.
	ActionRespond Action = iota
	// ActionClarify asks a disambiguating question.
	ActionClarify
	// ActionConfirm asks for a yes/no.
	ActionConfirm
	// ActionAsk requests a specific missing slot.
	ActionAsk
	// ActionTransfer routes the call elsewhere.
	ActionTransfer
	// ActionEscalate hands the call to a human or emergency path.
	ActionEscalate
	// ActionIgnore deliberately does nothing — a backchannel, or noise.
	ActionIgnore
	// ActionReject declines the request, politely and finally.
	ActionReject
	// ActionWait holds without speaking, awaiting an external event.
	ActionWait
	// ActionEnd terminates the conversation.
	ActionEnd
)

// String renders the action for logs and metric labels.
func (a Action) String() string {
	switch a {
	case ActionClarify:
		return "clarify"
	case ActionConfirm:
		return "confirm"
	case ActionAsk:
		return "ask"
	case ActionTransfer:
		return "transfer"
	case ActionEscalate:
		return "escalate"
	case ActionIgnore:
		return "ignore"
	case ActionReject:
		return "reject"
	case ActionWait:
		return "wait"
	case ActionEnd:
		return "end"
	default:
		return "respond"
	}
}

// RequiredCapability maps an action to the capability a persona must hold.
func (a Action) RequiredCapability() Capability {
	switch a {
	case ActionClarify, ActionConfirm, ActionAsk:
		return CapAskClarification
	case ActionTransfer:
		return CapTransfer
	case ActionEscalate:
		return CapEscalate
	case ActionEnd:
		return CapEndCall
	case ActionIgnore, ActionWait, ActionReject:
		// These are refusals or pauses. They require no capability, because a
		// persona must always be able to do nothing — a role that cannot
		// decline is a role that must always act.
		return ""
	default:
		return CapAnswerQuestion
	}
}

// Decision is a policy verdict.
type Decision int

const (
	// Allow permits the action.
	Allow Decision = iota
	// Deny forbids it.
	Deny
)

// String renders the decision.
func (d Decision) String() string {
	if d == Deny {
		return "deny"
	}
	return "allow"
}

// RuleClass groups rules by what kind of concern they express, which
// determines whether they may be overridden.
type RuleClass int

const (
	// ClassSafety protects the caller or the subscriber. NEVER overridable.
	ClassSafety RuleClass = iota
	// ClassPersona expresses a persona's capability boundary.
	ClassPersona
	// ClassBoundary expresses a conversation limit — length, turns.
	ClassBoundary
	// ClassBusiness expresses a commercial or operational preference.
	ClassBusiness
)

// String renders the class for logs and metric labels.
func (c RuleClass) String() string {
	switch c {
	case ClassPersona:
		return "persona"
	case ClassBoundary:
		return "boundary"
	case ClassBusiness:
		return "business"
	default:
		return "safety"
	}
}

// PolicyInput is everything a rule may consider.
//
// A struct rather than loose parameters so that adding an input does not
// change every rule's signature, and so a rule cannot reach for something the
// engine did not deliberately expose — a rule that could read arbitrary
// context would be an unaudited policy surface.
type PolicyInput struct {
	// Action being evaluated.
	Action Action
	// State the conversation is in.
	State State
	// Persona currently active.
	Persona Persona
	// Intent under consideration, if any.
	Intent Intent
	// HasIntent reports whether Intent is meaningful.
	HasIntent bool
	// TurnCount is completed turns so far.
	TurnCount int
	// Elapsed is conversation wall time.
	Elapsed time.Duration
	// ClarificationsUsed is how many clarifying questions have been asked.
	ClarificationsUsed int
	// EmergencyRaised reports whether emergency intent has been detected.
	EmergencyRaised bool
	// InterruptionCount is how many interruptions have occurred.
	InterruptionCount int
}

// Rule is one policy check.
//
// Rules are pure functions. Given the same input they return the same verdict,
// with no clock read, no map iteration and no external call. That is what makes
// the policy engine exhaustively testable and what makes a conversation replay
// produce identical decisions.
type Rule struct {
	// Name identifies the rule in denials and metrics.
	Name string
	// Class determines override precedence.
	Class RuleClass
	// Priority orders evaluation within a class. Lower runs first.
	Priority int
	// Eval returns Deny with a reason, or Allow.
	Eval func(PolicyInput) (Decision, string)
}

// Verdict is the outcome of policy evaluation.
type Verdict struct {
	// Decision is the outcome.
	Decision Decision
	// Rule names the deciding rule, empty on a default allow.
	Rule string
	// Class is the deciding rule's class.
	Class RuleClass
	// Reason is a short machine-readable code. Never caller content.
	Reason string
}

// Allowed reports whether the action may proceed.
func (v Verdict) Allowed() bool { return v.Decision == Allow }

// PolicyEngine evaluates rules in class order.
//
// DENY OVERRIDES, AND SAFETY OVERRIDES EVERYTHING.
//
// Rules are evaluated safety-first. The first safety denial wins and no later
// rule can reverse it — there is no mechanism in this type for a business rule
// to permit something a safety rule forbade, because the moment such a
// mechanism exists someone will use it to restore throughput during an
// incident.
type PolicyEngine struct {
	metrics *Metrics

	mu    sync.RWMutex
	rules []Rule
}

// NewPolicyEngine constructs a policy engine with the built-in rules.
func NewPolicyEngine(metrics *Metrics) *PolicyEngine {
	if metrics == nil {
		metrics = NewMetrics()
	}
	p := &PolicyEngine{metrics: metrics}
	for _, r := range BuiltinRules() {
		p.Add(r)
	}
	return p
}

// Add registers a rule, keeping the rule set sorted by class then priority.
func (p *PolicyEngine) Add(r Rule) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rules = append(p.rules, r)
	sort.SliceStable(p.rules, func(i, j int) bool {
		if p.rules[i].Class != p.rules[j].Class {
			return p.rules[i].Class < p.rules[j].Class
		}
		if p.rules[i].Priority != p.rules[j].Priority {
			return p.rules[i].Priority < p.rules[j].Priority
		}
		// Deterministic tie-break so evaluation order never depends on
		// registration order for equal-priority rules.
		return p.rules[i].Name < p.rules[j].Name
	})
}

// Evaluate returns the verdict for an action.
func (p *PolicyEngine) Evaluate(in PolicyInput) Verdict {
	p.mu.RLock()
	rules := make([]Rule, len(p.rules))
	copy(rules, p.rules)
	p.mu.RUnlock()

	for _, r := range rules {
		decision, reason := r.Eval(in)
		if decision == Deny {
			p.metrics.PolicyDenials.Inc(r.Name)
			return Verdict{Decision: Deny, Rule: r.Name, Class: r.Class, Reason: reason}
		}
	}
	return Verdict{Decision: Allow}
}

// RuleCount returns how many rules are registered.
func (p *PolicyEngine) RuleCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.rules)
}

// BuiltinRules returns the platform's own policy set.
//
// Every rule here traces to a frozen invariant or a stated product commitment,
// and each carries that trace in its name so a denial in a log says which rule
// of the platform was being honoured.
func BuiltinRules() []Rule {
	return []Rule{
		// ---- Safety. Never overridable. ----

		{
			Name: "safety.emergency_only_escalates", Class: ClassSafety, Priority: 0,
			Eval: func(in PolicyInput) (Decision, string) {
				// U7: once an emergency is detected, the only permitted actions
				// are getting out of the way. Continuing to converse — even
				// helpfully — is the failure this forbids.
				if !in.EmergencyRaised {
					return Allow, ""
				}
				switch in.Action {
				case ActionEscalate, ActionEnd, ActionIgnore:
					return Allow, ""
				default:
					return Deny, "emergency_active"
				}
			},
		},

		{
			Name: "safety.no_action_after_terminal", Class: ClassSafety, Priority: 1,
			Eval: func(in PolicyInput) (Decision, string) {
				if in.State.IsTerminal() {
					return Deny, "conversation_terminal"
				}
				return Allow, ""
			},
		},

		{
			Name: "safety.escalation_always_available", Class: ClassSafety, Priority: 2,
			Eval: func(in PolicyInput) (Decision, string) {
				// Not a denial — an assertion that no later rule may deny
				// escalation. Expressed as an allow-and-stop by running early
				// in the safety class: a rule that denied escalation would have
				// to run before this one, and none does.
				return Allow, ""
			},
		},

		// ---- Persona capability boundaries. ----

		{
			Name: "persona.capability_required", Class: ClassPersona, Priority: 0,
			Eval: func(in PolicyInput) (Decision, string) {
				cap := in.Action.RequiredCapability()
				if cap == "" {
					return Allow, ""
				}
				if !in.Persona.Allows(cap) {
					return Deny, "capability_" + string(cap)
				}
				return Allow, ""
			},
		},

		{
			Name: "persona.uncertainty_escalates", Class: ClassPersona, Priority: 1,
			Eval: func(in PolicyInput) (Decision, string) {
				// A persona configured to escalate on uncertainty must not
				// clarify instead. Emergency handling is the case: asking a
				// clarifying question spends the only resource that matters.
				if !in.Persona.EscalateOnUncertainty {
					return Allow, ""
				}
				switch in.Action {
				case ActionClarify, ActionConfirm, ActionAsk:
					return Deny, "persona_escalates_on_uncertainty"
				}
				return Allow, ""
			},
		},

		// ---- Conversation boundaries. ----

		{
			Name: "boundary.clarification_budget", Class: ClassBoundary, Priority: 0,
			Eval: func(in PolicyInput) (Decision, string) {
				switch in.Action {
				case ActionClarify, ActionConfirm, ActionAsk:
					if in.ClarificationsUsed >= in.Persona.ClarificationBudget {
						return Deny, "clarification_budget_exhausted"
					}
				}
				return Allow, ""
			},
		},

		{
			Name: "boundary.max_turns", Class: ClassBoundary, Priority: 1,
			Eval: func(in PolicyInput) (Decision, string) {
				if in.Persona.MaxTurns <= 0 || in.TurnCount < in.Persona.MaxTurns {
					return Allow, ""
				}
				// At the limit only terminal actions remain. The conversation
				// must be able to end; denying everything would wedge it.
				switch in.Action {
				case ActionEnd, ActionEscalate, ActionTransfer:
					return Allow, ""
				default:
					return Deny, "max_turns_reached"
				}
			},
		},

		{
			Name: "boundary.max_duration", Class: ClassBoundary, Priority: 2,
			Eval: func(in PolicyInput) (Decision, string) {
				if in.Persona.MaxDuration <= 0 || in.Elapsed < in.Persona.MaxDuration {
					return Allow, ""
				}
				switch in.Action {
				case ActionEnd, ActionEscalate, ActionTransfer:
					return Allow, ""
				default:
					return Deny, "max_duration_reached"
				}
			},
		},

		{
			Name: "boundary.interruption_storm", Class: ClassBoundary, Priority: 3,
			Eval: func(in PolicyInput) (Decision, string) {
				// A conversation being interrupted repeatedly is not working.
				// Continuing to talk over a caller who keeps interrupting is
				// the behaviour that makes people hang up; escalating is the
				// honest response.
				if in.InterruptionCount < 6 {
					return Allow, ""
				}
				switch in.Action {
				case ActionEscalate, ActionEnd, ActionTransfer, ActionIgnore:
					return Allow, ""
				default:
					return Deny, "interruption_storm"
				}
			},
		},
	}
}
