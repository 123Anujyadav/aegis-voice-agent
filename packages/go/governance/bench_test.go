package governance

import (
	"fmt"
	"testing"
	"time"
)

// Benchmarks for the governance hot path. Every number in
// docs/governance/PERFORMANCE.md comes from these, on the machine named there.
//
// WHAT THESE MEASURE. Evaluation is a pure in-memory function over a policy
// snapshot: no network, no broker, no store. That is the whole cost. And it is
// on the critical path of EVERY action the platform takes — every conversation
// decision, every tool call, every memory write — so unlike the other phases,
// this module's own cost is not obviously negligible and has to be checked
// rather than assumed.

func benchHarness(b *testing.B, policies ...Policy) *Harness {
	b.Helper()
	h, err := NewHarness()
	if err != nil {
		b.Fatal(err)
	}
	if len(policies) > 0 {
		if err := h.Engine.Policies().RegisterAll(policies...); err != nil {
			b.Fatal(err)
		}
	}
	return h
}

// realisticPolicies builds a policy set of a given size with a realistic
// distribution across scopes, so the numbers are not measured against a
// registry that happens to be one policy deep.
func realisticPolicies(n int) []Policy {
	scopes := []Scope{ScopeCompliance, ScopeGlobal, ScopeOrganization,
		ScopeBusiness, ScopeUser, ScopeSession}
	kinds := []ActionKind{ActionConversation, ActionMemory, ActionTool,
		ActionNotification, ActionExternal}

	out := make([]Policy, 0, n)
	for i := 0; i < n; i++ {
		p := Policy{
			ID: PolicyID(fmt.Sprintf("p%03d", i)), Version: 1,
			Scope: scopes[i%len(scopes)], Priority: 100 + i,
			Title: "bench", Owner: "bench", Enabled: true,
			Match: Match{Kinds: []ActionKind{kinds[i%len(kinds)]}},
			Rules: []Rule{
				{
					Name: "high_risk",
					When: []Condition{
						{Field: FieldRisk, Selector: SelAtLeast, Value: Str("high")},
						{Field: FieldReversibility, Selector: SelEquals, Value: Str("never")},
					},
					Then: OutcomeRequireSupervisor, Reason: "bench_high_risk",
				},
				{
					Name: "personal_write",
					When: []Condition{
						{Field: FieldClassification, Selector: SelAtLeast, Value: Str("personal")},
						{Field: FieldOperation, Selector: SelIn, Values: []string{"write", "update"}},
					},
					Then: OutcomeRequireConsent, Reason: "bench_personal",
					Obligations: []Obligation{{Kind: ObligationConsent, Target: "data_processing"}},
				},
				{Name: "allow_rest", Then: OutcomeAllow, Reason: "bench_allow"},
			},
			Default: OutcomeDeny, DefaultReason: "bench_default",
		}
		out = append(out, p)
	}
	return out
}

func benchRequest() Request {
	return Request{
		Action: ReadAction("preferences"),
		Actor:  "assistant", Subject: "caller-1",
		Org: "org-1", Business: "biz-1", Session: "sess-1",
		Correlation: "corr-1", Roles: []string{"assistant"},
	}
}

// BenchmarkDecideSmall is the headline number: one decision against a small,
// realistic policy set.
func BenchmarkDecideSmall(b *testing.B) {
	h := benchHarness(b, realisticPolicies(12)...)
	req := benchRequest()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Engine.Decide(req)
	}
}

// BenchmarkDecideLarge measures a registry a mature multi-tenant deployment
// might carry.
func BenchmarkDecideLarge(b *testing.B) {
	h := benchHarness(b, realisticPolicies(200)...)
	req := benchRequest()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Engine.Decide(req)
	}
}

// BenchmarkDecideParallel measures the same path under contention. Every action
// in the platform passes through here, so this is the number that decides
// whether the engine is a bottleneck.
func BenchmarkDecideParallel(b *testing.B) {
	h := benchHarness(b, realisticPolicies(50)...)
	req := benchRequest()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = h.Engine.Decide(req)
		}
	})
}

// BenchmarkEvaluatePure isolates the pure evaluator: no validation, no risk
// aggregation, no consent, no metrics, no audit, no events. The difference from
// BenchmarkDecideSmall is what the engine's surrounding machinery costs.
func BenchmarkEvaluatePure(b *testing.B) {
	h := benchHarness(b, realisticPolicies(12)...)
	snap := h.Engine.Policies().Snapshot()
	ev := h.Engine.Evaluator()
	req := benchRequest()
	now := time.Now()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ev.Evaluate(snap, req, now)
	}
}

func BenchmarkDecideWithConsent(b *testing.B) {
	h := benchHarness(b, ConsentPolicy("c", ScopeGlobal, 100, Match{}, "data_processing"))
	h.Grant("caller-1", "data_processing", time.Hour)

	req := benchRequest()
	req.Action = WriteAction("transcript")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Engine.Decide(req)
	}
}

func BenchmarkDecideWithRisk(b *testing.B) {
	h := benchHarness(b, realisticPolicies(12)...)
	req := benchRequest()
	req.Risk = RiskAssessment{Signals: []Signal{
		{Source: "fraud", Kind: "velocity", Level: RiskMedium, Weight: 0.8},
		{Source: "telephony", Kind: "spoofed_cli", Level: RiskHigh, Weight: 1},
		{Source: "identity", Kind: "new_device", Level: RiskMedium, Weight: 0.5},
	}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Engine.Decide(req)
	}
}

func BenchmarkConditionMatch(b *testing.B) {
	req := benchRequest()
	c := Condition{Field: FieldClassification, Selector: SelAtLeast, Value: Str("personal")}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Matches(req)
	}
}

func BenchmarkConditionMatchRoleSet(b *testing.B) {
	req := benchRequest()
	req.Roles = []string{"assistant", "receptionist", "operator", "supervisor"}
	c := Condition{Field: FieldRole, Selector: SelIn, Values: []string{"supervisor", "admin"}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Matches(req)
	}
}

func BenchmarkRequestFingerprint(b *testing.B) {
	req := benchRequest()
	req.Action.Attributes = Attrs{"capability": Str("calendar.check"),
		"channel": Str("sms"), "count": Num(3)}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = req.Fingerprint()
	}
}

func BenchmarkRiskAggregate(b *testing.B) {
	agg := DefaultAggregator()
	signals := []Signal{
		{Source: "fraud", Kind: "velocity", Level: RiskMedium, Weight: 0.8},
		{Source: "telephony", Kind: "spoofed_cli", Level: RiskHigh, Weight: 1},
		{Source: "identity", Kind: "new_device", Level: RiskMedium, Weight: 0.5},
		{Source: "behaviour", Kind: "hesitation", Level: RiskLow, Weight: 0.2},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = agg.Aggregate(signals...)
	}
}

func BenchmarkConsentCheck(b *testing.B) {
	h := benchHarness(b)
	h.Grant("caller-1", "data_processing", time.Hour)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Engine.Consent().Check("caller-1", "data_processing")
	}
}

func BenchmarkConsentGrant(b *testing.B) {
	h := benchHarness(b)
	expires := h.Clock.Now().Add(time.Hour)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := h.Engine.Consent().Grant(ConsentRecord{
			Subject: SubjectID(fmt.Sprintf("s%08d", i)), Basis: "data_processing",
			TermsVersion: 1, Method: "test", ExpiresAt: expires,
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPolicyRegister measures the copy-on-write write path, which is what
// the lock-free read path is paid for with.
func BenchmarkPolicyRegister(b *testing.B) {
	h := benchHarness(b, realisticPolicies(50)...)
	p := AllowPolicy("churn", ScopeGlobal, 100, Match{})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Version = i + 1
		if err := h.Engine.Policies().Register(p); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSnapshotLoad(b *testing.B) {
	h := benchHarness(b, realisticPolicies(200)...)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Engine.Policies().Snapshot()
	}
}

// BenchmarkConflictsIn measures the static conflict check, which runs at boot
// and after every policy load rather than on the request path.
func BenchmarkConflictsIn(b *testing.B) {
	h := benchHarness(b, realisticPolicies(200)...)
	snap := h.Engine.Policies().Snapshot()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ConflictsIn(snap)
	}
}

func BenchmarkEscalationRaiseResolve(b *testing.B) {
	h := benchHarness(b)
	hr := h.Engine.Human()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d := Decision{ID: DecisionID(fmt.Sprintf("d%08d", i)),
			Outcome: OutcomeRequireHuman, Reason: "bench"}
		if _, err := hr.Raise(d, EscalationApproval, time.Hour); err != nil {
			b.Fatal(err)
		}
		if _, err := hr.Approve(d.ID, "bench-human", ""); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEventDispatch(b *testing.B) {
	m := NewMetrics()
	d := NewEventDispatcher(m, NoopPublisher{}, time.Now)
	e := Event{Type: EventDecided, Decision: "d", Outcome: OutcomeAllow,
		Reason: "bench", ActionLabel: "memory:read"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Dispatch(e)
	}
}

func BenchmarkAuditRecord(b *testing.B) {
	a := NewRecordingAuditor(1 << 20)
	entry := AuditEntry{Kind: AuditDecision, Decision: "d", Outcome: OutcomeAllow,
		Reason: "bench", ActionLabel: "memory:read"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := a.Record(entry); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkValidatorCheck(b *testing.B) {
	v := DefaultValidator()
	req := benchRequest()
	req.Action.Attributes = Attrs{"capability": Str("calendar.check")}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := v.Check(req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecisionExplain(b *testing.B) {
	h := benchHarness(b, realisticPolicies(20)...)
	d := h.Engine.Decide(benchRequest())

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = d.Explain()
	}
}
