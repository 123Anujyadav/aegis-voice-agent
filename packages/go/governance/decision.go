package governance

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Outcome is what the engine decided.
//
// TEN OUTCOMES, NOT TWO. A governance engine that can only say yes or no says
// no to things it should have asked about, and callers learn to route around
// it. Each of the eight middle outcomes exists because a real situation has no
// correct binary answer:
//
//   - the caller may proceed once a person confirms;
//   - the caller may proceed once consent is on file;
//   - a human should look at this before anything happens;
//   - a supervisor specifically, not any human;
//   - this is fine but not now;
//   - this is fine but not on the request path.
//
// Collapsing those into "deny" produces a system that refuses correct actions,
// and collapsing them into "allow" produces one that takes them unsupervised.
type Outcome uint8

// The ten outcomes.
const (
	// OutcomeDeny refuses the action. The zero value ON PURPOSE: a Decision
	// that was never populated denies, so a bug that drops a decision fails
	// closed rather than open.
	OutcomeDeny Outcome = iota

	// OutcomeAllow permits the action.
	OutcomeAllow

	// OutcomeEscalate routes to a review queue. Not a denial and not an
	// allowance — the action waits.
	OutcomeEscalate

	// OutcomeRequireConfirmation needs the end user to confirm.
	OutcomeRequireConfirmation

	// OutcomeRequireConsent needs a consent basis on file.
	OutcomeRequireConsent

	// OutcomeRequireHuman needs any authorised human to act.
	OutcomeRequireHuman

	// OutcomeRequireSupervisor needs a supervisor specifically. Distinct from
	// RequireHuman because "someone looked at it" and "someone accountable
	// looked at it" are different controls.
	OutcomeRequireSupervisor

	// OutcomeRetryLater refuses for now, with a stated retry-after.
	OutcomeRetryLater

	// OutcomeQueue accepts the action for asynchronous execution.
	OutcomeQueue

	// OutcomeDefer accepts it but not on the request path — the caller should
	// complete the turn and do this afterwards.
	OutcomeDefer
)

// String renders the outcome. Used as a metric label.
func (o Outcome) String() string {
	switch o {
	case OutcomeAllow:
		return "allow"
	case OutcomeEscalate:
		return "escalate"
	case OutcomeRequireConfirmation:
		return "require_confirmation"
	case OutcomeRequireConsent:
		return "require_consent"
	case OutcomeRequireHuman:
		return "require_human"
	case OutcomeRequireSupervisor:
		return "require_supervisor"
	case OutcomeRetryLater:
		return "retry_later"
	case OutcomeQueue:
		return "queue"
	case OutcomeDefer:
		return "defer"
	default:
		return "deny"
	}
}

// Permits reports whether the action may proceed immediately with no further
// condition.
//
// Only Allow. Everything else — including Queue and Defer, which do permit the
// action eventually — returns false, because a caller checking "may I do this
// now" must get a straight answer and the ones that need work are not it.
func (o Outcome) Permits() bool { return o == OutcomeAllow }

// Terminal reports whether the outcome ends the matter with no further action
// possible on this request.
func (o Outcome) Terminal() bool { return o == OutcomeDeny || o == OutcomeAllow }

// NeedsHuman reports whether a person must act before the action can proceed.
func (o Outcome) NeedsHuman() bool {
	switch o {
	case OutcomeEscalate, OutcomeRequireConfirmation, OutcomeRequireHuman, OutcomeRequireSupervisor:
		return true
	default:
		return false
	}
}

// severity orders outcomes for conflict resolution.
//
// HIGHER WINS. The ordering is the engine's most consequential constant, and
// every position in it is a deliberate answer to "if two policies disagree,
// which is safer to obey?"
//
//	deny                 5   refusing is always safe
//	require_supervisor   4   an accountable human, before anything happens
//	require_human        3
//	require_consent      2   a legal precondition
//	require_confirmation 2   a user precondition — same rank; see below
//	escalate             2
//	retry_later          1   delay is safer than proceeding
//	queue                1
//	defer                1
//	allow                0   proceeding is never the safe side of a disagreement
//
// Consent, confirmation and escalation share a rank because none dominates the
// others: they are three different preconditions, and a request carrying two of
// them must satisfy BOTH. That is why [Decision.Obligations] is a set rather
// than a single field — merging keeps every obligation, not the strongest one.
func (o Outcome) severity() int {
	switch o {
	case OutcomeDeny:
		return 5
	case OutcomeRequireSupervisor:
		return 4
	case OutcomeRequireHuman:
		return 3
	case OutcomeRequireConsent, OutcomeRequireConfirmation, OutcomeEscalate:
		return 2
	case OutcomeRetryLater, OutcomeQueue, OutcomeDefer:
		return 1
	default:
		return 0
	}
}

// ObligationKind classifies something that must be satisfied.
type ObligationKind string

// The obligation kinds.
const (
	// ObligationConsent names a consent basis that must be on file.
	ObligationConsent ObligationKind = "consent"
	// ObligationConfirmation requires the end user to confirm.
	ObligationConfirmation ObligationKind = "confirmation"
	// ObligationApproval requires a named human role to approve.
	ObligationApproval ObligationKind = "approval"
	// ObligationMask requires an attribute to be masked before proceeding.
	ObligationMask ObligationKind = "mask"
	// ObligationRedact requires an attribute to be removed.
	ObligationRedact ObligationKind = "redact"
	// ObligationAudit requires an audit entry with a named reason.
	ObligationAudit ObligationKind = "audit"
	// ObligationRetryAfter states when the caller may try again.
	ObligationRetryAfter ObligationKind = "retry_after"
	// ObligationNotify requires a party to be told.
	ObligationNotify ObligationKind = "notify"
)

// Obligation is a machine-readable precondition.
//
// SPECIFIC ON PURPOSE. "Denied: needs approval" leaves a caller guessing whose
// approval, for what, and until when. An obligation names the kind, the target
// and a deadline, so a caller can satisfy it rather than escalate to a human
// who then has to work out what was wanted.
type Obligation struct {
	// Kind classifies it.
	Kind ObligationKind
	// Target names what it applies to: a consent basis name, a role, an
	// attribute name.
	Target string
	// Reason is a short machine-readable code. Never free text — it becomes a
	// metric label and an event field.
	Reason string
	// Deadline bounds it, zero when unbounded.
	Deadline time.Time
	// Policy names the policy that imposed it, so a caller arguing with an
	// obligation knows which rule to argue with.
	Policy PolicyID
}

// String renders the obligation.
func (o Obligation) String() string {
	s := string(o.Kind)
	if o.Target != "" {
		s += "(" + o.Target + ")"
	}
	if o.Reason != "" {
		s += " " + o.Reason
	}
	return s
}

// key is the deduplication key when merging obligations from several policies.
func (o Obligation) key() string { return string(o.Kind) + "\x1f" + o.Target }

// TraceEntry records one policy's contribution to a decision.
//
// EVERY policy consulted appears, including the ones that did not match. A
// trace that only shows the winner answers "what happened" but not "why did the
// other rule not fire", which is the question an operator actually has when a
// policy they wrote appears to have done nothing.
type TraceEntry struct {
	// Policy identifies the policy.
	Policy PolicyID
	// Version is the policy version consulted.
	Version int
	// Scope is where the policy sat in the precedence order.
	Scope Scope
	// Priority is the policy's priority within its scope.
	Priority int
	// Matched reports whether any rule in the policy matched the request.
	Matched bool
	// Rule names the rule that matched, empty when none did.
	Rule string
	// Outcome is what this policy alone would have decided. Only meaningful
	// when Matched.
	Outcome Outcome
	// Reason is the policy's stated reason.
	Reason string
	// Skipped names why a policy was not evaluated at all — out of scope,
	// expired, disabled. Empty when it was evaluated.
	Skipped string
	// Decisive reports that this policy's outcome is the one that won.
	Decisive bool
}

// Decision is the engine's answer.
//
// It carries WHY, WHICH POLICY, WHAT MUST HAPPEN NEXT, and enough metadata to
// recompute it later. A decision a caller cannot explain to a person is a
// decision the platform cannot defend.
type Decision struct {
	// ID identifies this decision.
	ID DecisionID

	// Outcome is the answer. The zero value is Deny.
	Outcome Outcome

	// Reason is a short machine-readable code, never free text.
	Reason string

	// Explanation is one human-readable sentence, assembled from the deciding
	// policy. Safe to show an operator; NEVER safe to show an end caller
	// without review, because it names internal policies.
	Explanation string

	// Obligations are the preconditions. A caller satisfying all of them and
	// asking again should get Allow.
	Obligations []Obligation

	// DecidedBy names the policy that won.
	DecidedBy PolicyID

	// Scope is the winning policy's scope.
	Scope Scope

	// Trace records every policy consulted, in evaluation order.
	Trace []TraceEntry

	// PolicyVersion is the registry snapshot version this was decided against.
	// THE REPLAY ANCHOR: a decision recomputed against a different snapshot is
	// not the same decision, and this is how that is detectable.
	PolicyVersion uint64

	// RequestPrint fingerprints everything that determined the outcome.
	RequestPrint Fingerprint

	// Risk is the aggregated risk the decision saw.
	Risk RiskAssessment

	// Emergency names the emergency activation that influenced this, if any.
	Emergency string

	// Correlation and Session tie it to a turn and a conversation.
	Correlation CorrelationID
	Session     SessionID

	// Actor and Subject are who asked and about whom.
	Actor   ActorID
	Subject SubjectID

	// ActionLabel is the bounded metric form of the action.
	ActionLabel string

	// DecidedAt is the decision instant on the engine's clock.
	DecidedAt time.Time

	// Duration is how long evaluation took.
	Duration time.Duration

	// RetryAfter is set on OutcomeRetryLater.
	RetryAfter time.Duration
}

// Permits reports whether the action may proceed immediately.
func (d Decision) Permits() bool { return d.Outcome.Permits() }

// Err returns a non-nil error when the action may not proceed.
//
// Provided so a caller can use the engine in an error-checking idiom, but the
// Decision itself is the useful object: the error deliberately carries only the
// outcome and reason, because an error string is not a place to put obligations
// a caller is supposed to act on.
func (d Decision) Err() error {
	if d.Permits() {
		return nil
	}
	if d.Outcome == OutcomeDeny {
		return fmt.Errorf("%w: %s (%s)", ErrDenied, d.Reason, d.DecidedBy)
	}
	return fmt.Errorf("%w: %s requires %s (%s)", ErrNotPermitted,
		d.Reason, d.Outcome, d.DecidedBy)
}

// ObligationsOf returns the obligations of one kind.
func (d Decision) ObligationsOf(k ObligationKind) []Obligation {
	var out []Obligation
	for _, o := range d.Obligations {
		if o.Kind == k {
			out = append(out, o)
		}
	}
	return out
}

// Explain renders the decision and its trace, for operators and incident
// reviews.
//
// A decision that can only be understood by reading Go structs is a decision
// that will be argued about rather than understood.
func (d Decision) Explain() string {
	var b strings.Builder
	fmt.Fprintf(&b, "decision %s: %s (%s) by %s [%s] snapshot=%d\n",
		d.ID, d.Outcome, d.Reason, d.DecidedBy, d.Scope, d.PolicyVersion)
	if d.Explanation != "" {
		fmt.Fprintf(&b, "  %s\n", d.Explanation)
	}
	if d.Emergency != "" {
		fmt.Fprintf(&b, "  emergency: %s\n", d.Emergency)
	}
	for _, o := range d.Obligations {
		fmt.Fprintf(&b, "  obligation: %s [%s]\n", o, o.Policy)
	}
	for _, t := range d.Trace {
		switch {
		case t.Skipped != "":
			fmt.Fprintf(&b, "  - %-28s %-12s skipped: %s\n", t.Policy, t.Scope, t.Skipped)
		case !t.Matched:
			fmt.Fprintf(&b, "  - %-28s %-12s no rule matched\n", t.Policy, t.Scope)
		default:
			marker := " "
			if t.Decisive {
				marker = "*"
			}
			fmt.Fprintf(&b, "  %s %-28s %-12s rule=%s → %s (%s)\n",
				marker, t.Policy, t.Scope, t.Rule, t.Outcome, t.Reason)
		}
	}
	return b.String()
}

// mergeObligations combines obligation sets, deduplicating by kind and target
// and keeping the earliest deadline.
//
// KEEPS EVERY OBLIGATION, never the strongest. Two policies requiring different
// consent bases mean the caller needs both; collapsing them to one would let an
// action proceed having satisfied half of what was asked.
func mergeObligations(sets ...[]Obligation) []Obligation {
	index := make(map[string]Obligation)
	for _, set := range sets {
		for _, o := range set {
			existing, seen := index[o.key()]
			if !seen {
				index[o.key()] = o
				continue
			}
			// The earliest deadline wins: the tighter constraint is the one
			// that must actually be met.
			if !o.Deadline.IsZero() &&
				(existing.Deadline.IsZero() || o.Deadline.Before(existing.Deadline)) {
				existing.Deadline = o.Deadline
				index[o.key()] = existing
			}
		}
	}

	out := make([]Obligation, 0, len(index))
	for _, o := range index {
		out = append(out, o)
	}
	// Sorted so two evaluations of the same request produce byte-identical
	// obligation lists, which is what makes a decision fingerprint comparable.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Target != out[j].Target {
			return out[i].Target < out[j].Target
		}
		return out[i].Policy < out[j].Policy
	})
	return out
}
