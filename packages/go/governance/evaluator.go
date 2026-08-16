package governance

import (
	"time"
)

// Evaluator turns a request and a policy snapshot into a decision.
//
// THE ONE PURE FUNCTION AT THE CENTRE OF THE ENGINE. It performs no I/O, holds
// no state, mutates nothing, and takes the instant as a parameter rather than
// reading a clock. Given the same snapshot, request and instant it returns a
// byte-identical decision forever.
//
// Everything the brief asks for falls out of that. Determinism is free. Replay
// is free — the snapshot version travels in the decision. Explainability is
// free, because a pure function over declared rules can name exactly which rule
// decided. And the entire evaluator test suite runs with no clock, no broker
// and no store.
type Evaluator struct {
	// Default is what happens when no policy matches.
	//
	// There is no field to make this Allow. The type permits it; [Config]
	// validation refuses it. A safety engine whose failure mode is "allow" has
	// stopped being a safety engine at the exact moment it broke.
	Default Outcome

	// DefaultReason is the reason attached to the default outcome.
	DefaultReason string

	// Thresholds turn risk into outcomes.
	Thresholds Thresholds

	// Privacy turns classification into obligations.
	Privacy PrivacyRules

	// PrivacyPolicy names the pseudo-policy credited with privacy obligations,
	// so a caller reading an obligation can tell it came from the privacy
	// rules rather than from a written policy.
	PrivacyPolicy PolicyID
}

// Evaluate decides.
//
// The pipeline is five stages, and the order is load-bearing:
//
//  1. RESOLUTION   which policies apply at all, per scope
//  2. EVALUATION   what each applicable policy alone would decide
//  3. MERGE        within a scope, highest priority wins; ties that disagree
//     are a configuration error, not a coin toss
//  4. CONFLICT     across scopes, most severe wins; an Override in an
//     overriding scope may win with a less severe outcome
//  5. OVERLAY      risk thresholds and privacy obligations, which can only
//     make the outcome stricter
//
// Stage 5 last, and can only raise. A risk signal that could lower an outcome
// would let a detector overrule a written policy, which inverts the whole
// precedence model.
func (e Evaluator) Evaluate(snap *PolicySnapshot, r Request, now time.Time) Decision {
	d := Decision{
		Outcome:       e.Default,
		Reason:        e.DefaultReason,
		DecidedBy:     "<default>",
		PolicyVersion: snap.Version,
		RequestPrint:  r.Fingerprint(),
		Risk:          r.Risk,
		Correlation:   r.Correlation,
		Session:       r.Session,
		Actor:         r.Actor,
		Subject:       r.Subject,
		ActionLabel:   r.Action.Label(),
		DecidedAt:     now,
	}
	if d.Reason == "" {
		d.Reason = "no_policy_matched"
	}

	var (
		trace       []TraceEntry
		obligations [][]Obligation
		winner      *scopeResult
	)

	// ---- stages 1 to 3: per scope, in precedence order ---------------------
	for _, scope := range AllScopes() {
		result, entries := e.evaluateScope(snap, scope, r, now)
		trace = append(trace, entries...)
		if result == nil {
			continue
		}
		obligations = append(obligations, result.obligations)

		// ---- stage 4: conflict resolution across scopes --------------------
		if winner == nil {
			winner = result
			continue
		}

		// Three rules, in this order, and the order is the whole of the
		// emergency containment story.
		switch {
		case winner.override:
			// An override from a higher scope is FINAL for everything below it.
			//
			// The first version let a lower scope re-strengthen an override on
			// the grounds that an override "relaxes a rule rather than
			// disabling every rule beneath it". That reasoning is wrong and it
			// made every emergency override a no-op: the global denial the
			// emergency existed to relax simply won again one scope later. An
			// override that anything below it can undo is not an override.
			// See ENGINEERING_AUDIT F3.

		case result.override && winner.scope.Overridable():
			// An overriding policy wins outright, even with a milder outcome.
			// This is the ONLY way a less severe outcome beats a more severe
			// one, and it is gated on the incumbent scope being overridable —
			// which compliance is not, so an emergency can never displace a
			// legal rule.
			winner = result

		case result.outcome.severity() > winner.outcome.severity():
			// Otherwise the safer outcome wins, whichever scope it came from.
			winner = result
		}
	}

	if winner != nil {
		d.Outcome = winner.outcome
		d.Reason = winner.reason
		d.Explanation = winner.explanation
		d.DecidedBy = winner.policy
		d.Scope = winner.scope
		d.RetryAfter = winner.retryAfter
	}

	// ---- stage 5: overlays, which can only raise ---------------------------
	if forced, reason, fired := e.Thresholds.Apply(r.Action, r.Risk); fired {
		if forced.severity() > d.Outcome.severity() {
			d.Outcome = forced
			d.Reason = reason
			d.Explanation = "risk overlay: " + r.Risk.Explanation
			d.DecidedBy = "<risk>"
			// The scope stays whatever the policy decided, so a trace still
			// shows which policy was in play when risk raised the outcome.
		}
		obligations = append(obligations, []Obligation{{
			Kind: ObligationAudit, Target: "risk", Reason: reason, Policy: "<risk>"}})
	}

	obligations = append(obligations,
		e.Privacy.PrivacyObligations(r.Action, e.PrivacyPolicy))

	d.Obligations = mergeObligations(obligations...)

	// Mark the decisive entry, so the trace can be read without re-deriving
	// which policy won.
	for i := range trace {
		if winner != nil && trace[i].Policy == winner.policy &&
			trace[i].Scope == winner.scope && trace[i].Matched {
			trace[i].Decisive = true
			break
		}
	}
	d.Trace = trace
	return d
}

// scopeResult is one scope's verdict.
type scopeResult struct {
	scope       Scope
	policy      PolicyID
	outcome     Outcome
	reason      string
	explanation string
	obligations []Obligation
	override    bool
	priority    int
	retryAfter  time.Duration
}

// evaluateScope resolves and merges one scope.
//
// Within a scope the highest priority wins. Two policies at the same priority
// that disagree are a CONFIGURATION ERROR: rather than picking one, the engine
// takes the more severe outcome and records the conflict in the trace, so the
// platform stays safe and the operator finds out. Silently picking by
// alphabetical order would be deterministic and would hide a real mistake.
func (e Evaluator) evaluateScope(snap *PolicySnapshot, scope Scope, r Request, now time.Time) (*scopeResult, []TraceEntry) {
	ids := snap.InScope(scope)
	if len(ids) == 0 {
		return nil, nil
	}

	entries := make([]TraceEntry, 0, len(ids))
	var (
		best *scopeResult
		// EVERY matching policy's obligations accumulate, not just the
		// winner's.
		//
		// An obligation is a precondition somebody wrote down. Dropping one
		// because a different policy happened to have a higher priority means
		// an action proceeds having satisfied half of what was asked — and the
		// half it skipped is invisible, because the losing policy is in the
		// trace as matched-but-not-decisive.
		//
		// The concrete case that exposed this: a personal, irreversible
		// notification matches both "irreversible actions require
		// confirmation" and "personal data requires consent". They sit at
		// different priorities in one scope, so the winner-takes-all version
		// silently discarded the confirmation requirement. See
		// ENGINEERING_AUDIT F5.
		//
		// A policy that must REMOVE an obligation does so from a higher scope
		// with Override, where the relaxation is visible and attributed.
		obligations [][]Obligation
	)

	for _, id := range ids {
		p, ok := snap.Get(id)
		if !ok {
			continue
		}

		entry := TraceEntry{Policy: p.ID, Version: p.Version, Scope: p.Scope,
			Priority: p.Priority}

		if active, why := p.Active(now); !active {
			entry.Skipped = why
			entries = append(entries, entry)
			continue
		}
		if applies, why := p.Match.applies(r); !applies {
			entry.Skipped = "match_" + why
			entries = append(entries, entry)
			continue
		}

		outcome, rule, matched := p.Evaluate(r)
		entry.Matched = true
		entry.Rule = rule.Name
		entry.Outcome = outcome
		entry.Reason = rule.Reason
		if !matched {
			entry.Rule = "<default>"
			entry.Reason = p.DefaultReason
		}
		entries = append(entries, entry)

		obligations = append(obligations, rule.Obligations)

		candidate := &scopeResult{
			scope: p.Scope, policy: p.ID, outcome: outcome,
			reason: entry.Reason, explanation: rule.Explanation,
			override: p.Override, priority: p.Priority, retryAfter: rule.RetryAfter,
		}

		switch {
		case best == nil:
			best = candidate
		case candidate.priority > best.priority:
			best = candidate
		case candidate.priority == best.priority && candidate.outcome != best.outcome:
			// Same priority, different answers. Take the safer one and leave
			// the evidence: the trace shows both policies matched with
			// different outcomes at the same priority.
			if candidate.outcome.severity() > best.outcome.severity() {
				best = candidate
			}
		}
	}

	if best != nil {
		best.obligations = mergeObligations(obligations...)
	}
	return best, entries
}

// ConflictsIn reports policies that sit at the same scope and priority and
// could decide differently.
//
// A STATIC CHECK, not a runtime one. It cannot prove two policies will disagree
// — that depends on the request — but it can prove they might, which is the
// thing an operator can act on before a decision goes the wrong way in
// production. Run at boot and after every policy load.
func ConflictsIn(snap *PolicySnapshot) []*ConflictError {
	var out []*ConflictError

	for _, scope := range AllScopes() {
		ids := snap.InScope(scope)
		for i := 0; i < len(ids); i++ {
			a, ok := snap.Get(ids[i])
			if !ok || !a.Enabled {
				continue
			}
			for j := i + 1; j < len(ids); j++ {
				b, ok := snap.Get(ids[j])
				if !ok || !b.Enabled {
					continue
				}
				if a.Priority != b.Priority {
					continue
				}
				if outcomesAgree(a, b) {
					continue
				}
				out = append(out, &ConflictError{
					A: a.ID, B: b.ID, Scope: scope, Priority: a.Priority,
					OutcomeA: dominantOutcome(a), OutcomeB: dominantOutcome(b),
				})
			}
		}
	}
	return out
}

// outcomesAgree reports whether two policies can only ever decide the same way.
//
// Conservative: it compares the set of outcomes each policy can produce. Two
// policies that can both only ever say Deny agree, however differently they
// reach it. Anything else is reported, which over-reports — and over-reporting
// a possible conflict is the right direction to be wrong in.
func outcomesAgree(a, b Policy) bool {
	seen := make(map[Outcome]bool)
	for _, o := range possibleOutcomes(a) {
		seen[o] = true
	}
	for _, o := range possibleOutcomes(b) {
		if !seen[o] {
			return false
		}
	}
	return len(seen) == 1 || equalOutcomeSets(possibleOutcomes(a), possibleOutcomes(b))
}

func possibleOutcomes(p Policy) []Outcome {
	seen := map[Outcome]bool{p.Default: true}
	for _, r := range p.Rules {
		seen[r.Then] = true
	}
	out := make([]Outcome, 0, len(seen))
	for o := range seen {
		out = append(out, o)
	}
	// Sorted so two calls produce the same slice, which the comparison relies
	// on and which makes a reported conflict reproducible.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func equalOutcomeSets(a, b []Outcome) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func dominantOutcome(p Policy) Outcome {
	best := p.Default
	for _, r := range p.Rules {
		if r.Then.severity() > best.severity() {
			best = r.Then
		}
	}
	return best
}
