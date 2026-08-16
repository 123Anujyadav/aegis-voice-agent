package governance

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func newHarness(t *testing.T) *Harness {
	t.Helper()
	h, err := NewHarness()
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	return h
}

// ---------------------------------------------------------------------------
// Attributes and fingerprints
// ---------------------------------------------------------------------------

// TestAttrs_FingerprintIgnoresMapOrder is the property replay rests on. If a
// request fingerprint depended on map iteration order, two records of the same
// question would look like two different questions and drift detection would be
// noise.
func TestAttrs_FingerprintIgnoresMapOrder(t *testing.T) {
	t.Parallel()

	a := Attrs{"zulu": Str("z"), "alpha": Num(1), "mid": Flag(true)}
	b := Attrs{"mid": Flag(true), "alpha": Num(1), "zulu": Str("z")}

	if a.Fingerprint() != b.Fingerprint() {
		t.Fatalf("same attributes fingerprinted differently: %s vs %s",
			a.Fingerprint(), b.Fingerprint())
	}
	first := a.Fingerprint()
	for i := 0; i < 200; i++ {
		if got := a.Fingerprint(); got != first {
			t.Fatalf("fingerprint drifted on run %d", i)
		}
	}
}

func TestAttrs_DistinctValuesDoNotCollide(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a, b Attrs
	}{
		{"string vs number", Attrs{"k": Str("1")}, Attrs{"k": Num(1)}},
		{"bool vs string", Attrs{"k": Flag(true)}, Attrs{"k": Str("true")}},
		{"absent vs missing key", Attrs{"k": Absent()}, Attrs{}},
		{"key/value swap", Attrs{"a": Str("b")}, Attrs{"b": Str("a")}},
	}
	for _, tc := range cases {
		if tc.a.Fingerprint() == tc.b.Fingerprint() {
			t.Errorf("%s: distinct attributes share a fingerprint", tc.name)
		}
	}
}

// TestRequest_FingerprintExcludesSessionAndCorrelation is what makes the
// fingerprint usable for replay: two identical questions asked in two different
// turns must fingerprint the same, or drift detection compares nothing.
func TestRequest_FingerprintExcludesSessionAndCorrelation(t *testing.T) {
	t.Parallel()

	base := Request{Action: ReadAction("pref"), Actor: "a", Subject: "s"}
	one := base
	one.Session, one.Correlation = "sess-1", "corr-1"
	two := base
	two.Session, two.Correlation = "sess-2", "corr-2"

	if one.Fingerprint() != two.Fingerprint() {
		t.Fatal("session and correlation leaked into the request fingerprint")
	}

	three := base
	three.Actor = "different"
	if three.Fingerprint() == one.Fingerprint() {
		t.Fatal("a different actor produced the same fingerprint")
	}
}

// ---------------------------------------------------------------------------
// Outcomes
// ---------------------------------------------------------------------------

// TestOutcome_ZeroValueIsDeny is a one-line test for the most consequential
// default in the module: a Decision that was never populated denies, so a bug
// that drops a decision fails closed.
func TestOutcome_ZeroValueIsDeny(t *testing.T) {
	t.Parallel()

	var o Outcome
	if o != OutcomeDeny {
		t.Fatalf("the zero Outcome is %s, not deny; a dropped decision would fail open", o)
	}
	var d Decision
	if d.Permits() {
		t.Fatal("a zero-valued Decision permits the action")
	}
}

func TestOutcome_SeverityOrderIsTotalAndDenyIsHighest(t *testing.T) {
	t.Parallel()

	all := []Outcome{OutcomeDeny, OutcomeAllow, OutcomeEscalate, OutcomeRequireConfirmation,
		OutcomeRequireConsent, OutcomeRequireHuman, OutcomeRequireSupervisor,
		OutcomeRetryLater, OutcomeQueue, OutcomeDefer}

	for _, o := range all {
		if o == OutcomeDeny {
			continue
		}
		if o.severity() >= OutcomeDeny.severity() {
			t.Errorf("%s is at least as severe as deny; refusing must always win", o)
		}
		if o != OutcomeAllow && o.severity() <= OutcomeAllow.severity() {
			t.Errorf("%s is no more severe than allow; proceeding is never the safe side", o)
		}
	}
	if !OutcomeAllow.Permits() {
		t.Error("allow must permit")
	}
	for _, o := range all {
		if o != OutcomeAllow && o.Permits() {
			t.Errorf("%s permits the action immediately; only allow may", o)
		}
	}
}

func TestObligations_MergeKeepsEveryPrecondition(t *testing.T) {
	t.Parallel()

	early := time.Now()
	late := early.Add(time.Hour)

	merged := mergeObligations(
		[]Obligation{{Kind: ObligationConsent, Target: "recording", Deadline: late}},
		[]Obligation{{Kind: ObligationConsent, Target: "sync"}},
		[]Obligation{{Kind: ObligationConsent, Target: "recording", Deadline: early}},
	)

	if len(merged) != 2 {
		t.Fatalf("expected 2 distinct obligations, got %d: %v", len(merged), merged)
	}
	for _, o := range merged {
		if o.Target == "recording" && !o.Deadline.Equal(early) {
			t.Error("merging kept the later deadline; the tighter constraint is the real one")
		}
	}
}

// ---------------------------------------------------------------------------
// Conditions
// ---------------------------------------------------------------------------

func TestCondition_SelectorsBehaveAsDocumented(t *testing.T) {
	t.Parallel()

	req := Request{
		Action: Action{Kind: ActionTool, Operation: "invoke", Resource: "calendar.check",
			Reversibility: ReversibleFully, Classification: ClassPersonal,
			Attributes: Attrs{"count": Num(5), "flagged": Flag(true)}},
		Actor: "actor-1", Subject: "subj-1", Roles: []string{"receptionist", "operator"},
		Risk: RiskAssessment{Level: RiskHigh},
	}

	cases := []struct {
		name string
		cond Condition
		want bool
	}{
		{"kind equals", Condition{Field: FieldKind, Selector: SelEquals, Value: Str("tool")}, true},
		{"kind not equals", Condition{Field: FieldKind, Selector: SelNotEquals, Value: Str("memory")}, true},
		{"resource prefix", Condition{Field: FieldResource, Selector: SelPrefix, Value: Str("calendar.")}, true},
		{"resource prefix miss", Condition{Field: FieldResource, Selector: SelPrefix, Value: Str("crm.")}, false},
		{"operation in", Condition{Field: FieldOperation, Selector: SelIn, Values: []string{"invoke", "read"}}, true},
		{"operation not in", Condition{Field: FieldOperation, Selector: SelNotIn, Values: []string{"read"}}, true},
		{"risk at least high", Condition{Field: FieldRisk, Selector: SelAtLeast, Value: Str("high")}, true},
		{"risk at least critical", Condition{Field: FieldRisk, Selector: SelAtLeast, Value: Str("critical")}, false},
		{"classification at least personal", Condition{Field: FieldClassification, Selector: SelAtLeast, Value: Str("personal")}, true},
		{"attribute greater than", Condition{Field: "count", Selector: SelGreaterThan, Value: Num(3)}, true},
		{"attribute less than", Condition{Field: "count", Selector: SelLessThan, Value: Num(3)}, false},
		{"attribute exists", Condition{Field: "flagged", Selector: SelExists}, true},
		{"attribute absent", Condition{Field: "missing", Selector: SelAbsent}, true},
		{"role in", Condition{Field: FieldRole, Selector: SelIn, Values: []string{"supervisor", "operator"}}, true},
		{"role not in", Condition{Field: FieldRole, Selector: SelNotIn, Values: []string{"supervisor"}}, true},
		{"role equals membership", Condition{Field: FieldRole, Selector: SelEquals, Value: Str("receptionist")}, true},
	}

	for _, tc := range cases {
		if got := tc.cond.Matches(req); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestCondition_AbsentFieldIsFalseForEveryComparison covers the trap that makes
// a deny rule silently never fire: "the field is not X" is not true of a field
// that is not there.
func TestCondition_AbsentFieldIsFalseForEveryComparison(t *testing.T) {
	t.Parallel()

	req := Request{Action: Action{Kind: ActionMemory, Operation: "read"}, Actor: "a"}

	comparisons := []Selector{SelEquals, SelNotEquals, SelGreaterThan, SelLessThan,
		SelAtLeast, SelIn, SelNotIn, SelPrefix}
	for _, sel := range comparisons {
		c := Condition{Field: "nothing_here", Selector: sel, Value: Str("x"), Values: []string{"x"}}
		if c.Matches(req) {
			t.Errorf("%s against an absent field matched; a deny rule written this way "+
				"would never fire", sel)
		}
	}
	if !(Condition{Field: "nothing_here", Selector: SelAbsent}).Matches(req) {
		t.Error("absent selector did not match an absent field")
	}
}

// TestCondition_BuiltinsCannotBeShadowed checks that a caller cannot defeat a
// policy by passing an attribute named after a built-in field.
func TestCondition_BuiltinsCannotBeShadowed(t *testing.T) {
	t.Parallel()

	req := Request{
		Action: Action{Kind: ActionMemory, Operation: "write", Classification: ClassSensitive,
			Attributes: Attrs{"classification": Str("public"), "kind": Str("conversation")}},
		Actor: "a",
	}

	if !(Condition{Field: FieldClassification, Selector: SelAtLeast, Value: Str("sensitive")}).Matches(req) {
		t.Error("an attribute named 'classification' shadowed the real classification")
	}
	if !(Condition{Field: FieldKind, Selector: SelEquals, Value: Str("memory")}).Matches(req) {
		t.Error("an attribute named 'kind' shadowed the real kind")
	}
}

// ---------------------------------------------------------------------------
// Policy validation
// ---------------------------------------------------------------------------

func TestPolicy_ValidationCatchesTheDangerousMistakes(t *testing.T) {
	t.Parallel()

	base := func() Policy {
		return Policy{ID: "p", Version: 1, Scope: ScopeGlobal, Owner: "team",
			Enabled: true, DefaultReason: "default",
			Rules: []Rule{{Name: "r", Then: OutcomeAllow, Reason: "ok"}}}
	}

	cases := []struct {
		name     string
		mutate   func(*Policy)
		contains string
	}{
		{"no id", func(p *Policy) { p.ID = "" }, "id is required"},
		{"no owner", func(p *Policy) { p.Owner = "" }, "owner is required"},
		{"no default reason", func(p *Policy) { p.DefaultReason = "" }, "DefaultReason is required"},
		{"version zero", func(p *Policy) { p.Version = 0 }, "version must be at least 1"},
		{"rule without reason", func(p *Policy) { p.Rules[0].Reason = "" }, "reason is required"},
		{"rule without name", func(p *Policy) { p.Rules[0].Name = "" }, "rule name is required"},
		{"duplicate rule names", func(p *Policy) {
			p.Rules = append(p.Rules, Rule{Name: "r", Then: OutcomeDeny, Reason: "x"})
		}, "duplicate rule name"},
		{"override outside emergency", func(p *Policy) { p.Override = true },
			"Override is permitted only in the emergency and compliance scopes"},
		{"temporary without end", func(p *Policy) { p.Scope = ScopeTemporary },
			"temporary policy requires EffectiveUntil"},
		{"blanket allow with no rules", func(p *Policy) {
			p.Rules = nil
			p.Default = OutcomeAllow
		}, "blanket permission"},
		{"retry without retry-after", func(p *Policy) {
			p.Rules[0].Then = OutcomeRetryLater
		}, "retry_later needs a positive RetryAfter"},
		{"empty value set", func(p *Policy) {
			p.Rules[0].When = []Condition{{Field: FieldKind, Selector: SelIn}}
		}, "non-empty value set"},
	}

	for _, tc := range cases {
		p := base()
		tc.mutate(&p)
		problems := p.validate()
		if len(problems) == 0 {
			t.Errorf("%s: expected a validation problem", tc.name)
			continue
		}
		if !strings.Contains(strings.Join(problems, "; "), tc.contains) {
			t.Errorf("%s: expected %q, got %v", tc.name, tc.contains, problems)
		}
	}
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

func TestRegistry_EmptyRegistryDeniesEverything(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	d := h.Ask(ReadAction("anything"), "actor", "subject")
	if d.Outcome != OutcomeDeny {
		t.Fatalf("an empty registry decided %s; the default must be deny", d.Outcome)
	}
	if d.DecidedBy != "<default>" {
		t.Errorf("expected the default to decide, got %s", d.DecidedBy)
	}
	if d.Reason == "" {
		t.Error("even the default denial must carry a reason")
	}
}

func TestRegistry_VersionMustAdvance(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	p := AllowPolicy("p", ScopeGlobal, 100, Match{})
	if err := h.Engine.Policies().Register(p); err != nil {
		t.Fatal(err)
	}
	err := h.Engine.Policies().Register(p)
	if err == nil || !strings.Contains(err.Error(), "does not advance") {
		t.Fatalf("re-registering at the same version must be refused, got %v", err)
	}

	p.Version = 2
	if err := h.Engine.Policies().Register(p); err != nil {
		t.Fatalf("a version bump should be accepted: %v", err)
	}
}

func TestRegistry_SnapshotIsImmutableAndVersioned(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	reg := h.Engine.Policies()

	h.Register(AllowPolicy("a", ScopeGlobal, 100, Match{}))
	before := reg.Snapshot()

	h.Register(DenyPolicy("b", ScopeGlobal, 200, Match{}))
	after := reg.Snapshot()

	if before.Len() != 1 || after.Len() != 2 {
		t.Fatalf("snapshot lengths: before=%d after=%d", before.Len(), after.Len())
	}
	if before.Version >= after.Version {
		t.Error("snapshot version did not advance")
	}
	if before.Digest == after.Digest {
		t.Error("a changed policy set produced the same digest")
	}
}

func TestRegistry_RegisterAllIsAtomic(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	good := AllowPolicy("good", ScopeGlobal, 100, Match{})
	bad := AllowPolicy("bad", ScopeGlobal, 100, Match{})
	bad.Owner = "" // invalid

	if err := h.Engine.Policies().RegisterAll(good, bad); err == nil {
		t.Fatal("a batch containing an invalid policy was accepted")
	}
	if h.Engine.Policies().Len() != 0 {
		t.Fatalf("a failed batch left %d policies registered; a partial policy load "+
			"means the platform runs under half a rule set", h.Engine.Policies().Len())
	}
}

// ---------------------------------------------------------------------------
// Evaluation and precedence
// ---------------------------------------------------------------------------

// TestEvaluator_DenyWinsAtEqualPrecedence is the fail-closed property stated as
// a test: when two policies disagree, the platform obeys the safer one.
func TestEvaluator_DenyWinsAtEqualPrecedence(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.Register(
		AllowPolicy("allow", ScopeGlobal, 100, Match{}),
		DenyPolicy("deny", ScopeGlobal, 100, Match{}),
	)

	d := h.Ask(ReadAction("x"), "actor", "subject")
	if d.Outcome != OutcomeDeny {
		t.Fatalf("got %s; at equal precedence the safer outcome must win\n%s", d.Outcome, d.Explain())
	}
}

func TestEvaluator_HigherPriorityWinsWithinAScope(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.Register(
		DenyPolicy("low-deny", ScopeGlobal, 100, Match{}),
		AllowPolicy("high-allow", ScopeGlobal, 900, Match{}),
	)

	d := h.Ask(ReadAction("x"), "actor", "subject")
	if d.Outcome != OutcomeAllow || d.DecidedBy != "high-allow" {
		t.Fatalf("higher priority did not win: %s by %s\n%s", d.Outcome, d.DecidedBy, d.Explain())
	}
}

// TestEvaluator_MoreSpecificScopeCannotLoosenWithoutOverride is the safety
// floor: a business cannot permit what the platform forbids simply by being
// more specific.
func TestEvaluator_MoreSpecificScopeCannotLoosenWithoutOverride(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.Register(
		DenyPolicy("platform-floor", ScopeGlobal, 100, Match{}),
		AllowPolicy("business-wants-it", ScopeBusiness, 999, Match{}),
	)

	d := h.Ask(ReadAction("x"), "actor", "subject")
	if d.Outcome != OutcomeDeny {
		t.Fatalf("a business policy loosened the platform floor: %s\n%s", d.Outcome, d.Explain())
	}
}

// TestEvaluator_EmergencyOverrideCanLoosenButNotCompliance is the single most
// important containment property in the module.
func TestEvaluator_EmergencyOverrideCanLoosenButNotCompliance(t *testing.T) {
	t.Parallel()

	t.Run("overrides a global denial", func(t *testing.T) {
		h := newHarness(t)
		h.Register(
			DenyPolicy("global-deny", ScopeGlobal, 100, Match{}),
			EmergencyAllowPolicy("emergency-allow", Match{}),
		)
		d := h.Ask(ReadAction("x"), "actor", "subject")
		if d.Outcome != OutcomeAllow || d.Scope != ScopeEmergency {
			t.Fatalf("emergency override did not take effect: %s by %s\n%s",
				d.Outcome, d.DecidedBy, d.Explain())
		}
	})

	t.Run("cannot override compliance", func(t *testing.T) {
		h := newHarness(t)
		h.Register(
			DenyPolicy("legal", ScopeCompliance, 100, Match{}),
			EmergencyAllowPolicy("emergency-allow", Match{}),
		)
		d := h.Ask(ReadAction("x"), "actor", "subject")
		if d.Outcome != OutcomeDeny || d.Scope != ScopeCompliance {
			t.Fatalf("an emergency overrode a compliance policy: %s by %s [%s]\n%s",
				d.Outcome, d.DecidedBy, d.Scope, d.Explain())
		}
	})
}

// TestEvaluator_TraceNamesEveryPolicyIncludingTheOnesThatDidNotMatch covers the
// question an operator actually has: why did the rule I wrote do nothing?
func TestEvaluator_TraceNamesEveryPolicyConsulted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	disabled := AllowPolicy("disabled", ScopeGlobal, 500, Match{})
	disabled.Enabled = false

	h.Register(
		AllowPolicy("matches", ScopeGlobal, 100, Match{}),
		DenyPolicy("wrong-kind", ScopeGlobal, 200, Match{Kinds: []ActionKind{ActionExternal}}),
		disabled,
	)

	d := h.Ask(ReadAction("x"), "actor", "subject")
	if len(d.Trace) != 3 {
		t.Fatalf("trace has %d entries, want 3 — a trace that omits the policies that "+
			"did not fire cannot answer why they did not\n%s", len(d.Trace), d.Explain())
	}

	byID := map[PolicyID]TraceEntry{}
	for _, e := range d.Trace {
		byID[e.Policy] = e
	}
	if byID["wrong-kind"].Skipped != "match_kind" {
		t.Errorf("expected wrong-kind to be skipped on kind, got %q", byID["wrong-kind"].Skipped)
	}
	if byID["disabled"].Skipped != "disabled" {
		t.Errorf("expected disabled to be skipped, got %q", byID["disabled"].Skipped)
	}
	if !byID["matches"].Decisive {
		t.Error("the winning policy was not marked decisive")
	}
}

func TestEvaluator_IsPureAndTakesNoClock(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(AllowPolicy("p", ScopeGlobal, 100, Match{}))

	snap := h.Engine.Policies().Snapshot()
	req := Request{Action: ReadAction("x"), Actor: "a", Subject: "s"}
	now := time.Now()

	first := h.Engine.Evaluator().Evaluate(snap, req, now)
	for i := 0; i < 100; i++ {
		got := h.Engine.Evaluator().Evaluate(snap, req, now)
		if got.Outcome != first.Outcome || got.DecidedBy != first.DecidedBy ||
			len(got.Trace) != len(first.Trace) {
			t.Fatalf("evaluation is not pure: run %d differed", i)
		}
	}
	// Nothing was published, audited or counted by the pure path.
	if h.Events.Len() != 0 || h.Engine.Decisions() != 0 {
		t.Fatal("the pure evaluator had side effects")
	}
}

func TestConflicts_AreDetectedStatically(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.Register(
		AllowPolicy("a", ScopeBusiness, 100, Match{}),
		DenyPolicy("b", ScopeBusiness, 100, Match{}),
		AllowPolicy("c", ScopeBusiness, 200, Match{}),
	)

	conflicts := h.Engine.Coordinator().Conflicts()
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d: %v", len(conflicts), conflicts)
	}
	if !errors.Is(conflicts[0], ErrPolicyConflict) {
		t.Error("a conflict should match ErrPolicyConflict")
	}
	if !strings.Contains(conflicts[0].Error(), "distinct priority") {
		t.Errorf("a conflict error should say how to fix it: %v", conflicts[0])
	}
}

// ---------------------------------------------------------------------------
// Consent
// ---------------------------------------------------------------------------

// TestConsent_FourNegativeOutcomesAreDistinct is the distinction that decides
// whether the platform asks a subject again, and it is a legal distinction as
// much as a technical one.
func TestConsent_FourNegativeOutcomesAreDistinct(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.Engine.Consent()

	if got := c.Check("nobody", "recording"); got.Valid || !errors.Is(got.Err, ErrConsentNotFound) {
		t.Fatalf("expected not-found, got %+v", got)
	}

	h.Grant("expiring", "recording", time.Minute)
	h.Clock.Advance(2 * time.Minute)
	if got := c.Check("expiring", "recording"); got.Valid || !errors.Is(got.Err, ErrConsentExpired) {
		t.Fatalf("expected expired, got %+v", got)
	}

	h.Grant("revoker", "recording", time.Hour)
	if _, err := c.Revoke("revoker", "recording", "subject asked"); err != nil {
		t.Fatal(err)
	}
	if got := c.Check("revoker", "recording"); got.Valid || !errors.Is(got.Err, ErrConsentRevoked) {
		t.Fatalf("expected revoked, got %+v", got)
	}

	h.Grant("old-terms", "recording", time.Hour)
	if err := c.SetTermsVersion("recording", 2); err != nil {
		t.Fatal(err)
	}
	got := c.Check("old-terms", "recording")
	if got.Valid || !errors.Is(got.Err, ErrConsentSuperseded) {
		t.Fatalf("expected superseded, got %+v", got)
	}
	if got.RequiredTerms != 2 {
		t.Errorf("a superseded check should say which terms are current, got %d", got.RequiredTerms)
	}
}

func TestConsent_RevocationIsImmediate(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.Grant("s", "recording", time.Hour)
	if !h.Engine.Consent().Check("s", "recording").Valid {
		t.Fatal("fresh consent is not valid")
	}
	if _, err := h.Engine.Consent().Revoke("s", "recording", "withdrawn"); err != nil {
		t.Fatal(err)
	}
	if h.Engine.Consent().Check("s", "recording").Valid {
		t.Fatal("consent was still valid after revocation; there is no grace period")
	}
}

func TestConsent_RevokingTwiceIsNotAnError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Grant("s", "recording", time.Hour)

	if _, err := h.Engine.Consent().Revoke("s", "recording", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Engine.Consent().Revoke("s", "recording", "again"); err != nil {
		t.Fatalf("a subject repeating themselves is not a fault condition: %v", err)
	}
}

func TestConsent_HistoryIsAppendOnlyInEffect(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.Grant("s", "recording", time.Hour)
	h.Clock.Advance(time.Minute)
	h.Grant("s", "recording", time.Hour) // renewal

	history := h.Engine.Consent().History("s", "recording")
	if len(history) != 2 {
		t.Fatalf("expected 2 records in history, got %d", len(history))
	}
	if history[0].State != ConsentSuperseded {
		t.Errorf("the earlier record should be superseded, got %s", history[0].State)
	}
	if history[0].ID == history[1].ID {
		t.Error("a renewal reused the consent identifier; the history must distinguish them")
	}
}

func TestConsent_TermsVersionsDoNotGoBackwards(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.Engine.Consent()

	if err := c.SetTermsVersion("recording", 3); err != nil {
		t.Fatal(err)
	}
	if err := c.SetTermsVersion("recording", 2); err == nil {
		t.Fatal("terms went backwards, which would revalidate superseded consent")
	}
}

func TestConsent_RevokeAllCoversEveryBasis(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	for _, basis := range []string{"recording", "sync", "marketing"} {
		h.Grant("s", basis, time.Hour)
	}
	revoked := h.Engine.Coordinator().ForgetSubject("s", "erasure request")
	if len(revoked) != 3 {
		t.Fatalf("expected 3 revocations, got %d; a subject must not have to enumerate "+
			"bases they never knew existed", len(revoked))
	}
	for _, basis := range []string{"recording", "sync", "marketing"} {
		if h.Engine.Consent().Check("s", basis).Valid {
			t.Errorf("%s survived a bulk revocation", basis)
		}
	}
}

// ---------------------------------------------------------------------------
// Risk
// ---------------------------------------------------------------------------

// TestRisk_AggregationIsMonotonic is the property that stops an attacker
// suppressing an alarming signal with cheap reassuring ones.
func TestRisk_AggregationIsMonotonic(t *testing.T) {
	t.Parallel()
	agg := DefaultAggregator()

	signals := []Signal{{Source: "fraud", Kind: "velocity", Level: RiskHigh, Weight: 1}}
	base := agg.Aggregate(signals...)

	for i := 0; i < 20; i++ {
		signals = append(signals, Signal{
			Source: fmt.Sprintf("calm%d", i), Kind: "nothing", Level: RiskLow, Weight: 1})
		got := agg.Aggregate(signals...)
		if got.Level < base.Level {
			t.Fatalf("adding a low signal lowered the aggregate from %s to %s",
				base.Level, got.Level)
		}
	}
}

func TestRisk_CorroborationAcrossSourcesEscalates(t *testing.T) {
	t.Parallel()
	agg := DefaultAggregator()

	// Three signals from ONE source stay put; three from three sources escalate.
	same := agg.Aggregate(
		Signal{Source: "fraud", Kind: "a", Level: RiskMedium, Weight: 1},
		Signal{Source: "fraud", Kind: "b", Level: RiskMedium, Weight: 1},
		Signal{Source: "fraud", Kind: "c", Level: RiskMedium, Weight: 1})
	if same.Level != RiskMedium {
		t.Errorf("one source repeating itself escalated to %s", same.Level)
	}

	distinct := agg.Aggregate(
		Signal{Source: "fraud", Kind: "a", Level: RiskMedium, Weight: 1},
		Signal{Source: "telephony", Kind: "b", Level: RiskMedium, Weight: 1},
		Signal{Source: "identity", Kind: "c", Level: RiskMedium, Weight: 1})
	if distinct.Level != RiskHigh {
		t.Errorf("three independent sources agreeing did not escalate: %s", distinct.Level)
	}
}

func TestRisk_CeilingAndWeightFloorAreReported(t *testing.T) {
	t.Parallel()

	capped := Aggregator{Ceiling: RiskMedium, MinWeight: 0.01}.Aggregate(
		Signal{Source: "fraud", Kind: "x", Level: RiskCritical, Weight: 1})
	if capped.Level != RiskMedium || !capped.Capped {
		t.Fatalf("ceiling not applied or not reported: %+v", capped)
	}
	if !strings.Contains(capped.Explanation, "capped") {
		t.Error("a capped assessment must say so; an operator should not have to infer it")
	}

	floored := DefaultAggregator().Aggregate(
		Signal{Source: "shadow", Kind: "x", Level: RiskCritical, Weight: 0})
	if floored.Level != RiskLow {
		t.Errorf("a zero-weight signal contributed: %s", floored.Level)
	}
	if !strings.Contains(floored.Explanation, "weight floor") {
		t.Errorf("an all-filtered assessment should explain itself, got %q", floored.Explanation)
	}
}

// TestRisk_ThresholdsOnlyRaise is what stops a detector overruling a policy.
func TestRisk_ThresholdsOnlyRaise(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(DenyPolicy("deny", ScopeGlobal, 100, Match{}))

	d := h.Engine.Decide(Request{
		Action: ReadAction("x"), Actor: "a", Subject: "s",
		Risk: RiskAssessment{Signals: SignalSet(RiskLow, "fraud")},
	})
	if d.Outcome != OutcomeDeny {
		t.Fatalf("a low-risk signal loosened a denial to %s", d.Outcome)
	}
}

// ---------------------------------------------------------------------------
// Emergency
// ---------------------------------------------------------------------------

func TestEmergency_ValidationRefusesTheDangerousShapes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	future := h.Clock.Now().Add(time.Hour)

	cases := []struct {
		name     string
		mutate   func(*Emergency)
		contains string
	}{
		{"no expiry", func(e *Emergency) { e.ExpiresAt = time.Time{} }, "ExpiresAt is required"},
		{"anonymous", func(e *Emergency) { e.AuthorisedBy = "" }, "AuthorisedBy is required"},
		{"no reason", func(e *Emergency) { e.Reason = "" }, "reason is required"},
		{"no policies", func(e *Emergency) { e.Policies = nil }, "does nothing"},
		{"targets compliance", func(e *Emergency) {
			e.Scopes = append(e.Scopes, ScopeCompliance)
		}, "which no emergency may relax"},
		{"non-emergency policy", func(e *Emergency) {
			e.Policies = []Policy{AllowPolicy("wrong", ScopeGlobal, 100, Match{})}
		}, "installs emergency-scope policies only"},
	}

	for _, tc := range cases {
		em := TestEmergency("inc", future, EmergencyAllowPolicy("emp", Match{}))
		tc.mutate(&em)
		err := h.Engine.Emergency().Activate(em)
		if err == nil {
			t.Errorf("%s: expected activation to be refused", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.contains) {
			t.Errorf("%s: expected %q, got %v", tc.name, tc.contains, err)
		}
	}
}

func TestEmergency_ExpiresOnItsOwn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.Register(DenyPolicy("floor", ScopeGlobal, 100, Match{}))
	em := TestEmergency("inc-1", h.Clock.Now().Add(time.Hour),
		EmergencyAllowPolicy("emp", Match{}))
	if err := h.Engine.Emergency().Activate(em); err != nil {
		t.Fatal(err)
	}

	if d := h.Ask(ReadAction("x"), "a", "s"); d.Outcome != OutcomeAllow {
		t.Fatalf("emergency did not take effect: %s", d.Outcome)
	}

	h.Clock.Advance(2 * time.Hour)
	if n := h.Engine.Emergency().Sweep(); n != 1 {
		t.Fatalf("expected 1 emergency to expire, got %d", n)
	}
	if d := h.Ask(ReadAction("x"), "a", "s"); d.Outcome != OutcomeDeny {
		t.Fatalf("an expired emergency still permitted the action: %s\n%s", d.Outcome, d.Explain())
	}
}

func TestEmergency_ActivationIsAuditedEvenIfNeverUsed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	em := TestEmergency("inc-2", h.Clock.Now().Add(time.Hour),
		EmergencyAllowPolicy("emp", Match{Kinds: []ActionKind{ActionExternal}}))
	if err := h.Engine.Emergency().Activate(em); err != nil {
		t.Fatal(err)
	}
	if len(h.Audit.OfKind(AuditEmergencyActivated)) != 1 {
		t.Fatal("declaring an emergency was not audited")
	}
	if len(h.Audit.OfKind(AuditEmergencyUsed)) != 0 {
		t.Error("an unused emergency was recorded as used")
	}
}

// ---------------------------------------------------------------------------
// Human override
// ---------------------------------------------------------------------------

func TestHuman_EscalationResolvesOnceAndOnlyOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	hr := h.Engine.Human()

	d := Decision{ID: NewDecisionID(), Outcome: OutcomeRequireSupervisor,
		Reason: "test", ActionLabel: "tool:invoke"}
	if _, err := hr.Raise(d, EscalationSupervisor, time.Minute); err != nil {
		t.Fatal(err)
	}
	// Raising twice returns the same escalation rather than creating a second.
	if _, err := hr.Raise(d, EscalationSupervisor, time.Minute); err != nil {
		t.Fatal(err)
	}
	if hr.Depth() != 1 {
		t.Fatalf("raising twice created %d escalations", hr.Depth())
	}

	if _, err := hr.Approve(d.ID, "supervisor-1", "checked"); err != nil {
		t.Fatal(err)
	}
	if _, err := hr.Approve(d.ID, "supervisor-2", "also checked"); !errors.Is(err, ErrAlreadyResolved) {
		t.Fatalf("a second resolution was accepted: %v", err)
	}
}

func TestHuman_AnonymousResolutionIsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	hr := h.Engine.Human()

	d := Decision{ID: NewDecisionID(), Outcome: OutcomeRequireHuman, Reason: "test"}
	if _, err := hr.Raise(d, EscalationApproval, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := hr.Resolve(d.ID, ResolutionApproved, "", "note"); err == nil {
		t.Fatal("an anonymous approval was accepted")
	}
}

// TestHuman_ExpiryDoesNotPermit is the property that stops an approval gate
// quietly becoming a delay.
func TestHuman_ExpiryDoesNotPermit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	hr := h.Engine.Human()

	d := Decision{ID: NewDecisionID(), Outcome: OutcomeRequireHuman, Reason: "test"}
	if _, err := hr.Raise(d, EscalationApproval, time.Minute); err != nil {
		t.Fatal(err)
	}

	h.Clock.Advance(2 * time.Minute)
	if n := hr.Sweep(); n != 1 {
		t.Fatalf("expected 1 expiry, got %d", n)
	}
	esc, ok := hr.Get(d.ID)
	if !ok {
		t.Fatal("expired escalation vanished")
	}
	if esc.Resolution != ResolutionExpired || esc.Resolution.Permits() {
		t.Fatalf("expiry resolved to %s and permits=%v; silence is not consent",
			esc.Resolution, esc.Resolution.Permits())
	}
}

// ---------------------------------------------------------------------------
// Validator
// ---------------------------------------------------------------------------

func TestValidator_RefusesContentShapedAttributes(t *testing.T) {
	t.Parallel()
	v := DefaultValidator()

	long := strings.Repeat("x", 300)
	err := v.Check(Request{
		Action: Action{Kind: ActionMemory, Operation: "read", Reversibility: ReversibleNone,
			Attributes: Attrs{"utterance": Str(long)}},
		Actor: "a", Subject: "s"})
	if err == nil || !strings.Contains(err.Error(), "never content") {
		t.Fatalf("a 300-character attribute was accepted: %v", err)
	}

	err = v.Check(Request{
		Action: Action{Kind: ActionMemory, Operation: "read", Reversibility: ReversibleNone,
			Attributes: Attrs{"transcript": Str("line one\nline two")}},
		Actor: "a", Subject: "s"})
	if err == nil || !strings.Contains(err.Error(), "line break") {
		t.Fatalf("an attribute containing a line break was accepted: %v", err)
	}
}

func TestValidator_RequiredFactsPerKind(t *testing.T) {
	t.Parallel()
	v := DefaultValidator()

	cases := []struct {
		name     string
		req      Request
		contains string
	}{
		{"memory without subject",
			Request{Action: Action{Kind: ActionMemory, Operation: "read",
				Reversibility: ReversibleNone}, Actor: "a"},
			"require a subject"},
		{"tool without capability",
			Request{Action: Action{Kind: ActionTool, Operation: "invoke",
				Reversibility: ReversibleFully}, Actor: "a"},
			`requires the "capability" attribute`},
		{"unknown operation",
			Request{Action: Action{Kind: ActionMemory, Operation: "obliterate",
				Reversibility: ReversibleFully}, Actor: "a", Subject: "s"},
			"is not one of"},
		{"mutating operation claiming reversibility none",
			Request{Action: Action{Kind: ActionMemory, Operation: "delete",
				Reversibility: ReversibleNone}, Actor: "a", Subject: "s"},
			"if that is true it is a read"},
	}

	for _, tc := range cases {
		err := v.Check(tc.req)
		if err == nil {
			t.Errorf("%s: expected a validation error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.contains) {
			t.Errorf("%s: expected %q, got %v", tc.name, tc.contains, err)
		}
	}
}

func TestEngine_MalformedActionIsDistinguishableFromDenial(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(AllowPolicy("allow-all", ScopeGlobal, 100, Match{}))

	d := h.Engine.Decide(Request{
		Action: Action{Kind: ActionMemory, Operation: "read", Reversibility: ReversibleNone},
		Actor:  "a", // no subject
	})
	if d.Outcome != OutcomeDeny || d.Reason != "malformed_action" {
		t.Fatalf("a malformed request produced %s/%s; a caller with a typo should not "+
			"conclude the platform is over-restrictive", d.Outcome, d.Reason)
	}
	if d.Explanation == "" {
		t.Error("a malformed-action denial must say what was wrong")
	}
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

func TestConfig_RefusesToDefaultToAllow(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Default = OutcomeAllow
	_, err := New(cfg, WithAuditor(NoopAuditor{}))
	if err == nil || !strings.Contains(err.Error(), "Default must be deny") {
		t.Fatalf("an allow-by-default engine was accepted: %v", err)
	}

	cfg = DefaultConfig()
	cfg.FailClosedOnPanic = false
	if _, err := New(cfg, WithAuditor(NoopAuditor{})); err == nil {
		t.Fatal("an engine that fails open on panic was accepted")
	}
}

func TestConfig_RequiresAnAuditorByDefault(t *testing.T) {
	t.Parallel()

	if _, err := New(DefaultConfig()); err == nil ||
		!strings.Contains(err.Error(), "cannot answer why it did that") {
		t.Fatalf("an engine with no audit trail was accepted: %v", err)
	}

	cfg := DefaultConfig()
	cfg.RequireAuditor = false
	if _, err := New(cfg); err != nil {
		t.Fatalf("an explicitly audit-free engine should start: %v", err)
	}
}

func TestEngine_StartsOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	if err := h.Engine.Start(); err != nil {
		t.Fatal(err)
	}
	if err := h.Engine.Start(); !errors.Is(err, ErrInvariant) {
		t.Fatalf("INV-GOV-10: a second Start should be refused, got %v", err)
	}
	if err := h.Engine.Stop(); err != nil {
		t.Fatal(err)
	}
	if d := h.Ask(ReadAction("x"), "a", "s"); d.Reason != "engine_closed" {
		t.Fatalf("a stopped engine decided %s/%s", d.Outcome, d.Reason)
	}
}

func TestEngine_TwoEnginesShareNothing(t *testing.T) {
	t.Parallel()
	a, b := newHarness(t), newHarness(t)

	a.Register(AllowPolicy("only-in-a", ScopeGlobal, 100, Match{}))

	if b.Engine.Policies().Len() != 0 {
		t.Fatal("a policy registered in one engine appeared in another")
	}
	if d := b.Ask(ReadAction("x"), "a", "s"); d.Outcome != OutcomeDeny {
		t.Fatal("the second engine used the first engine's policies")
	}
}

// TestEngine_PanicInEvaluationFailsClosed exercises the recover path directly:
// a nil snapshot dereferences inside the evaluator.
func TestEngine_PanicInEvaluationFailsClosed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	req := Request{Action: ReadAction("x"), Actor: "a", Subject: "s"}
	d := h.Engine.evaluateSafely(nil, req, h.Clock.Now())

	if d.Outcome != OutcomeDeny || d.Reason != "evaluation_panic" {
		t.Fatalf("a panic produced %s/%s; not knowing what the policies say resolves "+
			"to deny", d.Outcome, d.Reason)
	}
	if h.Engine.Panics() != 1 {
		t.Errorf("the panic was not counted, got %d", h.Engine.Panics())
	}
	if strings.Contains(d.Explanation, "nil pointer") {
		t.Error("the panic value leaked into the decision")
	}
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

func TestEvents_TopicNamingMatchesTheFrozenConvention(t *testing.T) {
	t.Parallel()
	for _, et := range AllEventTypes() {
		topic := et.Topic()
		if !strings.HasPrefix(topic, "governance.decision.") || !strings.HasSuffix(topic, ".v1") {
			t.Errorf("topic %q does not match <domain>.<entity>.<event>.v<major>", topic)
		}
		if strings.Contains(topic, "-") {
			t.Errorf("topic %q contains a hyphen, which collides with Prometheus "+
				"metric-name normalisation", topic)
		}
		if topic != strings.ToLower(topic) {
			t.Errorf("topic %q is not lowercase", topic)
		}
	}
}

// TestEvents_CarryNoContent is the mechanical check behind frozen invariant I7.
func TestEvents_CarryNoContent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	const secret = "SUPER-SECRET-RESOURCE-VALUE"
	h.Register(ConsentPolicy("c", ScopeGlobal, 100, Match{}, "SECRET-BASIS-NAME"))

	action := WriteAction(secret)
	action.Attributes = Attrs{"reference": Str("ref-123")}
	h.Ask(action, "actor", "subject")

	if h.Events.Len() == 0 {
		t.Fatal("no events were published at all")
	}
	for _, e := range h.Events.Events() {
		rendered := fmt.Sprintf("%+v", e)
		if strings.Contains(rendered, secret) {
			t.Fatalf("event %s carries the action resource: %s", e.Type, rendered)
		}
		if strings.Contains(rendered, "SECRET-BASIS-NAME") {
			t.Fatalf("event %s carries an obligation target: %s", e.Type, rendered)
		}
	}
}

func TestEvents_SequenceIsMonotonic(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(AllowPolicy("p", ScopeGlobal, 100, Match{}))

	for i := 0; i < 5; i++ {
		h.Ask(ReadAction("x"), "a", "s")
	}
	var last uint64
	for _, e := range h.Events.Events() {
		if e.Sequence <= last {
			t.Fatalf("sequence went backwards: %d after %d", e.Sequence, last)
		}
		last = e.Sequence
	}
}

func TestEvents_FailingPublisherDoesNotFailTheDecision(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(AllowPolicy("p", ScopeGlobal, 100, Match{}))
	h.Events.FailWith(errors.New("broker down"))

	if d := h.Ask(ReadAction("x"), "a", "s"); d.Outcome != OutcomeAllow {
		t.Fatalf("a broken subscriber changed the decision to %s", d.Outcome)
	}
	if h.Metrics.EventsDropped.Total() == 0 {
		t.Error("a dropped event should be counted so the loss is visible")
	}
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

func TestMetrics_RatesAreUndefinedRatherThanWrongWithNoData(t *testing.T) {
	t.Parallel()
	m := NewMetrics()

	if m.AllowRate() != 0 || m.DenyRate() != 0 || m.EscalationRate() != 0 || m.ConsentRate() != 0 {
		t.Fatal("rates over no observations should be zero rather than NaN")
	}
	m.Decisions.Inc(OutcomeAllow.String(), "memory:read")
	m.Decisions.Inc(OutcomeDeny.String(), "memory:write")
	if got := m.AllowRate(); got != 0.5 {
		t.Fatalf("allow rate is %v, want 0.5", got)
	}
}

func TestMetrics_SnapshotIsStablyOrdered(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.Decisions.Inc("deny", "b")
	m.Decisions.Inc("allow", "a")
	m.Policies.Set(3)

	first := fmt.Sprint(m.Snapshot())
	for i := 0; i < 20; i++ {
		if got := fmt.Sprint(m.Snapshot()); got != first {
			t.Fatal("metric snapshot ordering is unstable")
		}
	}
}

// ---------------------------------------------------------------------------
// Baseline
// ---------------------------------------------------------------------------

func TestBaseline_PoliciesAreValidAndConflictFree(t *testing.T) {
	t.Parallel()
	h, err := NewHarness(WithBaseline())
	if err != nil {
		t.Fatalf("the baseline does not load: %v", err)
	}
	if conflicts := h.Engine.Coordinator().Conflicts(); len(conflicts) != 0 {
		t.Fatalf("the baseline conflicts with itself: %v", conflicts)
	}
	if h.Engine.Policies().Len() != len(BaselinePolicies()) {
		t.Fatal("not every baseline policy registered")
	}
}

func TestBaseline_SecretsAreRefusedAndCannotBeOverridden(t *testing.T) {
	t.Parallel()
	h, err := NewHarness(WithBaseline())
	if err != nil {
		t.Fatal(err)
	}

	action := WriteAction("credentials")
	action.Classification = ClassSecret

	if d := h.Ask(action, "a", "s"); d.Outcome != OutcomeDeny || d.Reason != "secret_material" {
		t.Fatalf("secret material was not refused: %s/%s", d.Outcome, d.Reason)
	}

	em := TestEmergency("inc", h.Clock.Now().Add(time.Hour), EmergencyAllowPolicy("emp", Match{}))
	if err := h.Engine.Emergency().Activate(em); err != nil {
		t.Fatal(err)
	}
	d := h.Ask(action, "a", "s")
	if d.Outcome != OutcomeDeny {
		t.Fatalf("an emergency permitted storing secret material: %s\n%s", d.Outcome, d.Explain())
	}
}
