package governance

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Evaluation harness for docs/governance/GOVERNANCE_EVALUATION.md.
//
// The other suites answer "is this built correctly". These answer a different
// question: does the engine decide the right way, refuse explainably, and leave
// a record anybody could audit? Every figure in the evaluation report comes from
// here, so a change that weakens fail-closed behaviour or leaves a decision
// unexplained shows up as a changed number rather than as a document nobody
// reprinted.
//
// They assert as well as measure. A report produced by tests that cannot fail is
// a press release.

// evalHarness builds an engine carrying the baseline plus a small realistic
// tenant policy set.
func evalHarness(t *testing.T) *Harness {
	t.Helper()
	h, err := NewHarness(WithBaseline())
	if err != nil {
		t.Fatal(err)
	}
	h.Register(
		ConsentPolicy("org.recording", ScopeOrganization, 400,
			Match{Kinds: []ActionKind{ActionMemory}}, "call_recording"),
		OutcomePolicy("biz.after-hours", ScopeBusiness, 300,
			Match{Kinds: []ActionKind{ActionNotification}}, OutcomeDefer, "after_hours"),
		DenyPolicy("legal.no-export", ScopeCompliance, 900,
			Match{Kinds: []ActionKind{ActionExternal}}),
	)
	return h
}

// ---------------------------------------------------------------------------
// E1 · Fail-closed coverage
// ---------------------------------------------------------------------------

// TestEvaluation_UngovernedActionsAreDenied measures the single most important
// property: what the engine does when nobody wrote a rule.
//
// The number must be 100%. A governance engine whose gaps default to "allow"
// has, at exactly the moment somebody forgot a policy, stopped being one.
func TestEvaluation_UngovernedActionsAreDenied(t *testing.T) {
	h := newHarness(t) // deliberately empty registry

	probes := []struct {
		name   string
		action Action
	}{
		{"memory read", ReadAction("preferences")},
		{"memory write", WriteAction("transcript")},
		{"tool invoke", ToolAction("calendar.check", ReversibleFully)},
		{"notification", NotifyAction("sms")},
		{"external", ExternalAction("partner-api", ClassInternal)},
	}

	denied := 0
	for _, p := range probes {
		d := h.Ask(p.action, "actor", "subject")
		ok := d.Outcome == OutcomeDeny
		if ok {
			denied++
		} else {
			t.Errorf("%s: an ungoverned action was %s", p.name, d.Outcome)
		}
		t.Logf("%-14s outcome=%-8s denied=%v reason=%s", p.name, d.Outcome, ok, d.Reason)
	}

	t.Logf("ungoverned actions denied: %d of %d (%.0f%%)",
		denied, len(probes), 100*float64(denied)/float64(len(probes)))
	if denied != len(probes) {
		t.Errorf("%d ungoverned actions were permitted", len(probes)-denied)
	}
}

// ---------------------------------------------------------------------------
// E2 · Explainability
// ---------------------------------------------------------------------------

// TestEvaluation_EveryDecisionIsExplainable measures whether the platform can
// defend what it did.
//
// The bar: every decision names a deciding policy, carries a bounded reason
// code, and produces a trace that accounts for every policy consulted.
func TestEvaluation_EveryDecisionIsExplainable(t *testing.T) {
	h := evalHarness(t)
	h.Grant("caller-1", "call_recording", time.Hour)

	probes := []struct {
		name    string
		action  Action
		subject SubjectID
	}{
		{"allowed read", ReadAction("preferences"), "caller-1"},
		{"consented write", WriteAction("transcript"), "caller-1"},
		{"unconsented write", WriteAction("transcript"), "caller-2"},
		{"irreversible notify", NotifyAction("sms"), "caller-1"},
		{"denied export", ExternalAction("partner", ClassSensitive), "caller-1"},
		{"secret refused", func() Action {
			a := WriteAction("credentials")
			a.Classification = ClassSecret
			return a
		}(), "caller-1"},
	}

	explained, traced := 0, 0
	for _, p := range probes {
		d := h.Ask(p.action, "assistant", p.subject)

		hasPolicy := d.DecidedBy != ""
		hasReason := d.Reason != "" && checkReasonCode(d.Reason) == ""
		if hasPolicy && hasReason {
			explained++
		} else {
			t.Errorf("%s: policy=%q reason=%q", p.name, d.DecidedBy, d.Reason)
		}

		accounted := true
		for _, e := range d.Trace {
			if e.Skipped == "" && !e.Matched {
				continue // evaluated, no rule matched — accounted for
			}
			if e.Skipped == "" && e.Reason == "" {
				accounted = false
			}
		}
		if accounted {
			traced++
		}

		t.Logf("%-20s → %-20s by %-22s reason=%-24s trace=%d",
			p.name, d.Outcome, d.DecidedBy, d.Reason, len(d.Trace))
	}

	t.Logf("decisions naming a policy and a bounded reason: %d of %d", explained, len(probes))
	t.Logf("traces accounting for every policy consulted:   %d of %d", traced, len(probes))

	if explained != len(probes) || traced != len(probes) {
		t.Errorf("%d decisions were not fully explainable", len(probes)-explained)
	}
}

// ---------------------------------------------------------------------------
// E3 · Precedence correctness
// ---------------------------------------------------------------------------

// TestEvaluation_PrecedenceMatrix walks every ordered pair of scopes with
// opposed outcomes and checks the winner is the one the precedence order
// promises.
//
// This is the engine's most consequential constant expressed as a matrix, which
// is the only way to be sure the constant and the code agree.
func TestEvaluation_PrecedenceMatrix(t *testing.T) {
	scopes := AllScopes()
	correct, total := 0, 0

	for i, high := range scopes {
		for j, low := range scopes {
			if i >= j {
				continue
			}
			total++

			h := newHarness(t)
			// The higher scope allows, the lower denies. Without an override
			// the safer outcome must win regardless of scope — that is the
			// safety floor.
			h.Register(
				withWindow(AllowPolicy("high", high, 100, Match{}), h),
				withWindow(DenyPolicy("low", low, 900, Match{}), h),
			)
			d := h.Ask(ReadAction("x"), "a", "s")
			if d.Outcome == OutcomeDeny {
				correct++
			} else {
				t.Errorf("%s allow vs %s deny resolved to %s; the safer outcome must win "+
					"without an explicit override", high, low, d.Outcome)
			}
		}
	}

	t.Logf("scope pairs where the safer outcome won: %d of %d", correct, total)

	// And the override exception, which is the only way a milder outcome wins.
	overridable, blocked := 0, 0
	for _, low := range scopes[2:] { // everything below emergency
		h := newHarness(t)
		h.Register(
			withWindow(DenyPolicy("low", low, 900, Match{}), h),
			EmergencyAllowPolicy("emergency", Match{}),
		)
		if d := h.Ask(ReadAction("x"), "a", "s"); d.Outcome == OutcomeAllow {
			overridable++
		} else {
			t.Errorf("an emergency override could not relax the %s scope", low)
		}
	}

	h := newHarness(t)
	h.Register(
		DenyPolicy("legal", ScopeCompliance, 900, Match{}),
		EmergencyAllowPolicy("emergency", Match{}),
	)
	if d := h.Ask(ReadAction("x"), "a", "s"); d.Outcome == OutcomeDeny {
		blocked = 1
	} else {
		t.Error("an emergency override relaxed a compliance policy")
	}

	t.Logf("scopes an emergency may relax:            %d of %d", overridable, len(scopes[2:]))
	t.Logf("compliance held against an override:      %d of 1", blocked)
}

// ---------------------------------------------------------------------------
// E4 · Consent lifecycle
// ---------------------------------------------------------------------------

// TestEvaluation_ConsentLifecycleIsCompleteAndDistinguishable measures whether
// the engine can tell a regulator what happened and tell a caller what to do
// next.
func TestEvaluation_ConsentLifecycleIsCompleteAndDistinguishable(t *testing.T) {
	h := evalHarness(t)

	states := []struct {
		name    string
		subject SubjectID
		setup   func(SubjectID)
		reason  string
	}{
		{"never asked", "s-none", func(SubjectID) {}, "not_found"},
		{"granted", "s-ok", func(s SubjectID) { h.Grant(s, "call_recording", time.Hour) }, ""},
		{"expired", "s-old", func(s SubjectID) {
			h.Grant(s, "call_recording", time.Minute)
			h.Clock.Advance(2 * time.Minute)
		}, "expired"},
		{"revoked", "s-no", func(s SubjectID) {
			h.Grant(s, "call_recording", time.Hour)
			if _, err := h.Engine.Consent().Revoke(s, "call_recording", "withdrawn"); err != nil {
				t.Fatal(err)
			}
		}, "revoked"},
		{"superseded", "s-terms", func(s SubjectID) {
			h.Grant(s, "call_recording", time.Hour)
			if err := h.Engine.Consent().SetTermsVersion("call_recording", 2); err != nil {
				t.Fatal(err)
			}
		}, "superseded"},
	}

	distinct := 0
	for _, st := range states {
		st.setup(st.subject)
		d := h.Ask(WriteAction("transcript"), "assistant", st.subject)

		// Read the obligation for THIS basis specifically. The baseline also
		// demands a data_processing consent for personal writes, so a decision
		// legitimately carries several — which is the point of obligations
		// being a set rather than a single field.
		got := ""
		for _, o := range d.ObligationsOf(ObligationConsent) {
			if o.Target == "call_recording" {
				got = o.Reason
			}
		}

		ok := got == st.reason
		if ok {
			distinct++
		} else {
			t.Errorf("%s: expected obligation reason %q, got %q (outcome %s)",
				st.name, st.reason, got, d.Outcome)
		}
		t.Logf("%-12s outcome=%-18s obligation_reason=%-12s distinguishable=%v",
			st.name, d.Outcome, got, ok)
	}

	t.Logf("consent states distinguishable to a caller: %d of %d", distinct, len(states))

	// And the history is complete enough to answer a DPDP access request.
	history := h.Engine.Consent().History("s-terms", "call_recording")
	t.Logf("history depth for a superseded record: %d records", len(history))
	for _, rec := range history {
		if rec.Method == "" || rec.TermsVersion == 0 {
			t.Error("a history record cannot answer how consent was obtained")
		}
	}
}

// ---------------------------------------------------------------------------
// E5 · Determinism
// ---------------------------------------------------------------------------

// TestEvaluation_IdenticalRequestsProduceIdenticalDecisions measures whether the
// engine is replayable.
//
// A governance engine that decides differently on identical inputs cannot be
// audited, and a platform that acts on somebody's behalf must be able to answer
// "why did it do that" with something better than a log line.
func TestEvaluation_IdenticalRequestsProduceIdenticalDecisions(t *testing.T) {
	const runs = 50

	fingerprint := func() (string, string, string) {
		h := evalHarness(t)
		h.Grant("caller-1", "call_recording", time.Hour)

		req := Request{Action: WriteAction("transcript"), Actor: "assistant",
			Subject: "caller-1", Org: "org-1", Business: "biz-1",
			Roles: []string{"assistant", "receptionist"}}

		d := h.Engine.Decide(req)

		var trace string
		for _, e := range d.Trace {
			trace += string(e.Policy) + ":" + e.Outcome.String() + ":" + e.Skipped + ";"
		}
		var obligations string
		for _, o := range d.Obligations {
			obligations += string(o.Kind) + "/" + o.Target + ";"
		}
		return d.Outcome.String() + "|" + string(d.DecidedBy) + "|" + d.Reason, trace, obligations
	}

	outcome0, trace0, obl0 := fingerprint()
	outcomeDiv, traceDiv, oblDiv := 0, 0, 0
	for i := 1; i < runs; i++ {
		o, tr, ob := fingerprint()
		if o != outcome0 {
			outcomeDiv++
		}
		if tr != trace0 {
			traceDiv++
		}
		if ob != obl0 {
			oblDiv++
		}
	}

	t.Logf("runs=%d outcome divergences=%d trace divergences=%d obligation divergences=%d",
		runs, outcomeDiv, traceDiv, oblDiv)

	if outcomeDiv+traceDiv+oblDiv != 0 {
		t.Errorf("determinism broken: outcome=%d trace=%d obligations=%d",
			outcomeDiv, traceDiv, oblDiv)
	}
}

// ---------------------------------------------------------------------------
// E6 · Risk monotonicity
// ---------------------------------------------------------------------------

// TestEvaluation_RiskIsMonotonicAndOnlyRaises measures the two properties that
// stop a detector becoming an attack surface.
func TestEvaluation_RiskIsMonotonicAndOnlyRaises(t *testing.T) {
	agg := DefaultAggregator()

	// Monotonicity: adding signals never lowers the aggregate.
	signals := []Signal{{Source: "fraud", Kind: "velocity", Level: RiskCritical, Weight: 1}}
	level := agg.Aggregate(signals...).Level
	drops := 0
	for i := 0; i < 50; i++ {
		signals = append(signals, Signal{
			Source: fmt.Sprintf("calm-%d", i), Kind: "nothing", Level: RiskLow, Weight: 1})
		next := agg.Aggregate(signals...).Level
		if next < level {
			drops++
		}
		level = next
	}
	t.Logf("signals added=50 aggregate drops=%d final=%s", drops, level)
	if drops != 0 {
		t.Errorf("the aggregate fell %d times; a reassuring signal must not cancel an "+
			"alarming one", drops)
	}

	// Only-raises: risk never loosens a policy outcome.
	loosened := 0
	for _, riskLevel := range []RiskLevel{RiskLow, RiskMedium, RiskHigh, RiskCritical} {
		h := newHarness(t)
		h.Register(DenyPolicy("deny", ScopeGlobal, 100, Match{}))
		d := h.Engine.Decide(Request{
			Action: ReadAction("x"), Actor: "a", Subject: "s",
			Risk: RiskAssessment{Signals: SignalSet(riskLevel, "fraud")},
		})
		if d.Outcome != OutcomeDeny {
			loosened++
		}
		t.Logf("policy=deny risk=%-8s → %s", riskLevel, d.Outcome)
	}
	if loosened != 0 {
		t.Errorf("risk loosened a denial %d times", loosened)
	}
}

// ---------------------------------------------------------------------------
// E7 · Audit completeness
// ---------------------------------------------------------------------------

// TestEvaluation_EveryDecisionLeavesARecord measures whether the audit trail can
// account for what the platform did.
func TestEvaluation_EveryDecisionLeavesARecord(t *testing.T) {
	h := evalHarness(t)
	h.Grant("caller-1", "call_recording", time.Hour)

	actions := []Action{
		ReadAction("preferences"),
		WriteAction("transcript"),
		ToolAction("calendar.check", ReversibleFully),
		NotifyAction("sms"),
		ExternalAction("partner", ClassInternal),
	}
	for _, a := range actions {
		h.Ask(a, "assistant", "caller-1")
	}

	decisions := h.Engine.Decisions()
	audited := 0
	for _, kind := range []AuditKind{AuditDecision, AuditDenied, AuditEscalated} {
		audited += len(h.Audit.OfKind(kind))
	}

	t.Logf("decisions=%d audit entries=%d events=%d",
		decisions, audited, h.Events.Count(EventDecided))

	if uint64(audited) != decisions {
		t.Errorf("%d decisions produced %d audit entries", decisions, audited)
	}
	if h.Events.Count(EventDecided) != int(decisions) {
		t.Errorf("%d decisions produced %d decision events", decisions,
			h.Events.Count(EventDecided))
	}

	// And no audit entry or event carries content.
	const marker = "preferences"
	leaks := 0
	for _, e := range h.Audit.Entries() {
		if strings.Contains(fmt.Sprintf("%+v", e), marker) {
			leaks++
		}
	}
	for _, e := range h.Events.Events() {
		if strings.Contains(fmt.Sprintf("%+v", e), marker) {
			leaks++
		}
	}
	t.Logf("audit entries or events carrying an action resource: %d", leaks)
	if leaks != 0 {
		t.Errorf("%d records carry the action resource", leaks)
	}
}

// ---------------------------------------------------------------------------
// E8 · Overhead against the frozen budget
// ---------------------------------------------------------------------------

// TestEvaluation_GovernanceOverheadAgainstTheTurnBudget measures what governance
// costs a conversational turn.
//
// Unlike the other phases, this one is on the critical path of EVERY action, so
// the overhead is multiplied by however many decisions a turn makes. The measure
// is deliberately pessimistic: ten decisions per turn against a realistic policy
// set.
func TestEvaluation_GovernanceOverheadAgainstTheTurnBudget(t *testing.T) {
	h := evalHarness(t)
	h.Grant("caller-1", "call_recording", time.Hour)

	const iterations = 500
	const decisionsPerTurn = 10

	req := Request{Action: ReadAction("preferences"), Actor: "assistant",
		Subject: "caller-1", Org: "org-1", Business: "biz-1"}

	start := time.Now()
	for i := 0; i < iterations; i++ {
		_ = h.Engine.Decide(req)
	}
	elapsed := time.Since(start)
	per := elapsed / iterations
	perTurn := per * decisionsPerTurn

	const budget = 900 * time.Millisecond
	share := 100 * float64(perTurn) / float64(budget)

	t.Logf("policies=%d decisions=%d per-decision=%s per-turn(%d decisions)=%s "+
		"budget=%s share=%.3f%%",
		h.Engine.Policies().Len(), iterations, per, decisionsPerTurn, perTurn, budget, share)

	// One percent of the turn budget. Anything approaching it would mean
	// governance had become the thing worth optimising, which would be a
	// finding rather than a passing test.
	if share > 1.0 {
		t.Errorf("governance overhead is %.3f%% of the turn budget, over the 1%% ceiling", share)
	}
}

// TestEvaluation_ScalingWithPolicyCount measures how the cost grows, because the
// engine visits every policy in order to produce a complete trace.
func TestEvaluation_ScalingWithPolicyCount(t *testing.T) {
	sizes := []int{10, 50, 100, 200}
	const iterations = 200

	req := Request{Action: ReadAction("preferences"), Actor: "assistant", Subject: "s"}

	for _, n := range sizes {
		h := newHarness(t)
		if err := h.Engine.Policies().RegisterAll(realisticPolicies(n)...); err != nil {
			t.Fatal(err)
		}

		start := time.Now()
		for i := 0; i < iterations; i++ {
			_ = h.Engine.Decide(req)
		}
		per := time.Since(start) / iterations
		t.Logf("policies=%-4d per-decision=%-12s per-policy=%s", n, per, per/time.Duration(n))
	}
}

// withWindow gives a temporary-scope policy the effective window its validation
// requires, so the precedence matrix can build one policy per scope from the
// same generic builders.
func withWindow(p Policy, h *Harness) Policy {
	if p.Scope == ScopeTemporary {
		p.EffectiveFrom = h.Clock.Now().Add(-time.Minute)
		p.EffectiveUntil = h.Clock.Now().Add(time.Hour)
	}
	return p
}
