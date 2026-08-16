package governance

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Integration, concurrency, stress and failure-injection tests.
//
// These run whole decisions through the real engine — validator, registry,
// evaluator, consent, risk, emergency, escalation, events, audit. The unit
// suite proves each part; this proves they agree with each other.

// ---------------------------------------------------------------------------
// End-to-end decision flows
// ---------------------------------------------------------------------------

func TestIntegration_ConsentGateOpensWhenConsentArrives(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(ConsentPolicy("recording", ScopeGlobal, 100, Match{}, "call_recording"))

	action := WriteAction("transcript")

	d := h.Ask(action, "assistant", "caller-1")
	if d.Outcome != OutcomeRequireConsent {
		t.Fatalf("expected require_consent, got %s\n%s", d.Outcome, d.Explain())
	}
	obligations := d.ObligationsOf(ObligationConsent)
	if len(obligations) != 1 || obligations[0].Target != "call_recording" {
		t.Fatalf("the caller was not told which consent to obtain: %v", d.Obligations)
	}
	if obligations[0].Reason != "not_found" {
		t.Errorf("expected the registry's specific reason, got %q", obligations[0].Reason)
	}

	h.Grant("caller-1", "call_recording", time.Hour)

	d = h.Ask(action, "assistant", "caller-1")
	if d.Outcome != OutcomeAllow {
		t.Fatalf("consent was granted but the action is still %s\n%s", d.Outcome, d.Explain())
	}
	if len(d.ObligationsOf(ObligationConsent)) != 0 {
		t.Error("a satisfied consent obligation was still reported")
	}
}

func TestIntegration_RevocationClosesTheGateImmediately(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(ConsentPolicy("recording", ScopeGlobal, 100, Match{}, "call_recording"))
	h.Grant("caller-1", "call_recording", time.Hour)

	if d := h.Ask(WriteAction("transcript"), "assistant", "caller-1"); d.Outcome != OutcomeAllow {
		t.Fatalf("granted consent did not allow: %s", d.Outcome)
	}

	if _, err := h.Engine.Consent().Revoke("caller-1", "call_recording", "withdrawn"); err != nil {
		t.Fatal(err)
	}

	d := h.Ask(WriteAction("transcript"), "assistant", "caller-1")
	if d.Outcome != OutcomeRequireConsent {
		t.Fatalf("a revoked consent still permitted the write: %s\n%s", d.Outcome, d.Explain())
	}
	if got := d.ObligationsOf(ObligationConsent); len(got) != 1 || got[0].Reason != "revoked" {
		t.Errorf("the caller was not told the subject had revoked: %v", got)
	}
}

// TestIntegration_ExpiredConsentIsDistinguishableFromRevoked is what decides
// whether the platform asks the subject again. It is a legal distinction, not a
// cosmetic one.
func TestIntegration_ExpiredConsentIsDistinguishableFromRevoked(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(ConsentPolicy("recording", ScopeGlobal, 100, Match{}, "call_recording"))
	h.Grant("caller-1", "call_recording", time.Minute)

	h.Clock.Advance(2 * time.Minute)

	d := h.Ask(WriteAction("transcript"), "assistant", "caller-1")
	got := d.ObligationsOf(ObligationConsent)
	if len(got) != 1 || got[0].Reason != "expired" {
		t.Fatalf("expected an 'expired' obligation reason, got %v", got)
	}
}

func TestIntegration_EveryOutcomeIsReachableAndCarriesAReason(t *testing.T) {
	t.Parallel()

	outcomes := []Outcome{OutcomeAllow, OutcomeDeny, OutcomeEscalate,
		OutcomeRequireConfirmation, OutcomeRequireHuman, OutcomeRequireSupervisor,
		OutcomeRetryLater, OutcomeQueue, OutcomeDefer}

	for _, want := range outcomes {
		h := newHarness(t)
		h.Register(OutcomePolicy("p", ScopeGlobal, 100, Match{}, want, "test_outcome"))

		d := h.Ask(ReadAction("x"), "actor", "subject")
		if d.Outcome != want {
			t.Errorf("%s: got %s\n%s", want, d.Outcome, d.Explain())
			continue
		}
		if d.Reason == "" || d.DecidedBy == "" {
			t.Errorf("%s: decision carries no reason or deciding policy", want)
		}
		if want == OutcomeRetryLater && d.RetryAfter <= 0 {
			t.Errorf("retry_later carried no retry-after")
		}
	}
}

// ---------------------------------------------------------------------------
// Emergency
// ---------------------------------------------------------------------------

func TestIntegration_EmergencyLifecycleIsFullyAudited(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(DenyPolicy("floor", ScopeGlobal, 100, Match{}))

	em := TestEmergency("inc-9001", h.Clock.Now().Add(time.Hour),
		EmergencyAllowPolicy("inc-9001-allow", Match{}))
	if err := h.Engine.Emergency().Activate(em); err != nil {
		t.Fatal(err)
	}

	d := h.Ask(ReadAction("x"), "actor", "subject")
	if d.Outcome != OutcomeAllow {
		t.Fatalf("emergency did not take effect: %s\n%s", d.Outcome, d.Explain())
	}
	if d.Emergency != "inc-9001" {
		t.Errorf("the decision did not name the incident, got %q", d.Emergency)
	}

	// Declared, used, and ended — three separate audit facts.
	for _, kind := range []AuditKind{AuditEmergencyActivated, AuditEmergencyUsed} {
		if len(h.Audit.OfKind(kind)) == 0 {
			t.Errorf("%s was not audited", kind)
		}
	}
	if err := h.Engine.Emergency().Deactivate("inc-9001", "resolved"); err != nil {
		t.Fatal(err)
	}
	if len(h.Audit.OfKind(AuditEmergencyExpired)) != 1 {
		t.Error("ending an emergency was not audited")
	}
	if d := h.Ask(ReadAction("x"), "actor", "subject"); d.Outcome != OutcomeDeny {
		t.Fatalf("a deactivated emergency still permitted the action: %s", d.Outcome)
	}
}

func TestIntegration_EmergencyCannotReachComplianceAcrossTheWholeEngine(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.Register(
		DenyPolicy("legal-retention", ScopeCompliance, 500, Match{}),
		DenyPolicy("business-rule", ScopeBusiness, 500, Match{}),
	)
	em := TestEmergency("inc-1", h.Clock.Now().Add(time.Hour),
		EmergencyAllowPolicy("inc-1-allow", Match{}))
	if err := h.Engine.Emergency().Activate(em); err != nil {
		t.Fatal(err)
	}

	d := h.Ask(ReadAction("x"), "actor", "subject")
	if d.Outcome != OutcomeDeny || d.Scope != ScopeCompliance {
		t.Fatalf("compliance was overridden: %s by %s [%s]\n%s",
			d.Outcome, d.DecidedBy, d.Scope, d.Explain())
	}
}

// ---------------------------------------------------------------------------
// Human override
// ---------------------------------------------------------------------------

func TestIntegration_DecisionNeedingAHumanRaisesAnEscalation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(OutcomePolicy("supervise", ScopeGlobal, 100, Match{},
		OutcomeRequireSupervisor, "needs_supervisor"))

	d := h.Ask(ReadAction("x"), "actor", "subject")
	if d.Outcome != OutcomeRequireSupervisor {
		t.Fatalf("got %s", d.Outcome)
	}

	pending := h.Engine.Human().Pending()
	if len(pending) != 1 {
		t.Fatalf("expected 1 escalation, got %d; a decision that says a human must "+
			"approve and does not ask one quietly becomes a denial", len(pending))
	}
	if pending[0].Kind != EscalationSupervisor {
		t.Errorf("expected a supervisor escalation, got %s", pending[0].Kind)
	}
	if pending[0].Decision != d.ID {
		t.Error("the escalation does not reference the decision that raised it")
	}

	if _, err := h.Engine.Human().Approve(d.ID, "supervisor-7", "verified with the caller"); err != nil {
		t.Fatal(err)
	}
	esc, _ := h.Engine.Human().Get(d.ID)
	if !esc.Resolution.Permits() || esc.ResolvedBy != "supervisor-7" {
		t.Fatalf("approval not recorded: %+v", esc)
	}
	if len(h.Audit.OfKind(AuditEscalationResolved)) != 1 {
		t.Error("a human resolution was not audited")
	}
}

func TestIntegration_EscalationCarriesNoResourceIdentifier(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(OutcomePolicy("review", ScopeGlobal, 100, Match{},
		OutcomeRequireHuman, "needs_review"))

	const resource = "SENSITIVE-RESOURCE-IDENTIFIER"
	h.Ask(WriteAction(resource), "actor", "subject")

	for _, esc := range h.Engine.Human().Pending() {
		if strings.Contains(fmt.Sprintf("%+v", esc), resource) {
			t.Fatalf("an escalation queue entry carries a resource identifier: %+v", esc)
		}
	}
}

// ---------------------------------------------------------------------------
// Risk
// ---------------------------------------------------------------------------

func TestIntegration_RiskRaisesButNeverLowers(t *testing.T) {
	t.Parallel()

	t.Run("raises an allow", func(t *testing.T) {
		h := newHarness(t)
		h.Register(AllowPolicy("allow", ScopeGlobal, 100, Match{}))

		d := h.Engine.Decide(Request{
			Action: ReadAction("x"), Actor: "a", Subject: "s",
			Risk: RiskAssessment{Signals: SignalSet(RiskCritical, "fraud")},
		})
		if d.Outcome != OutcomeRequireSupervisor {
			t.Fatalf("critical risk did not raise an allow: %s\n%s", d.Outcome, d.Explain())
		}
		if d.DecidedBy != "<risk>" {
			t.Errorf("the risk overlay did not attribute itself, got %s", d.DecidedBy)
		}
	})

	t.Run("cannot lower a deny", func(t *testing.T) {
		h := newHarness(t)
		h.Register(DenyPolicy("deny", ScopeGlobal, 100, Match{}))

		d := h.Engine.Decide(Request{
			Action: ReadAction("x"), Actor: "a", Subject: "s",
			Risk: RiskAssessment{Signals: SignalSet(RiskLow, "fraud")},
		})
		if d.Outcome != OutcomeDeny {
			t.Fatalf("a low-risk signal loosened a denial to %s", d.Outcome)
		}
	})
}

// TestIntegration_CallerCannotAssertALowRiskLevel closes the obvious attack on
// a risk-aware policy: a compromised subsystem declaring itself safe.
func TestIntegration_CallerCannotAssertALowRiskLevel(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(AllowPolicy("allow", ScopeGlobal, 100, Match{}))

	d := h.Engine.Decide(Request{
		Action: ReadAction("x"), Actor: "a", Subject: "s",
		Risk: RiskAssessment{
			Level:   RiskLow, // the caller says low
			Signals: SignalSet(RiskCritical, "fraud"),
		},
	})
	if d.Risk.Level != RiskCritical {
		t.Fatalf("the caller's asserted level survived aggregation: %s", d.Risk.Level)
	}
	if d.Outcome != OutcomeRequireSupervisor {
		t.Fatalf("a caller-asserted low level defeated the threshold: %s", d.Outcome)
	}
}

// ---------------------------------------------------------------------------
// Replay and drift
// ---------------------------------------------------------------------------

func TestIntegration_ReplayDetectsPolicyDrift(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(AllowPolicy("p", ScopeGlobal, 100, Match{}))

	req := Request{Action: ReadAction("x"), Actor: "a", Subject: "s",
		Correlation: NewCorrelationID()}
	original := h.Engine.Decide(req)
	meta := ReplayMetadataOf(original, h.Engine.Policies().Snapshot().Digest)

	// Same policies: no drift.
	_, drift := h.Engine.Coordinator().Replay(meta, req)
	if !drift.Same || drift.PolicyChanged {
		t.Fatalf("replay against unchanged policies reported drift: %s", drift)
	}

	// A new higher-precedence denial: drift, and named.
	h.Register(DenyPolicy("new-floor", ScopeCompliance, 100, Match{}))
	replayed, drift := h.Engine.Coordinator().Replay(meta, req)
	if drift.Same {
		t.Fatal("replay did not notice that today's policies decide differently")
	}
	if !drift.PolicyChanged || drift.NowPolicy != "new-floor" {
		t.Fatalf("drift did not name the change: %+v", drift)
	}
	if replayed.Outcome != OutcomeDeny {
		t.Fatalf("replayed outcome is %s", replayed.Outcome)
	}

	// A replay must have no side effects.
	before := h.Engine.Decisions()
	_, _ = h.Engine.Coordinator().Replay(meta, req)
	if h.Engine.Decisions() != before {
		t.Error("a replay counted as a decision; reviewing a policy change must not " +
			"change the system")
	}
}

// ---------------------------------------------------------------------------
// Determinism
// ---------------------------------------------------------------------------

func TestIntegration_IdenticalRequestsProduceIdenticalDecisions(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(
		DenyPolicy("compliance", ScopeCompliance, 100,
			Match{Kinds: []ActionKind{ActionExternal}}),
		ConsentPolicy("consent", ScopeGlobal, 200, Match{}, "call_recording"),
		AllowPolicy("business", ScopeBusiness, 300, Match{}),
		OutcomePolicy("session", ScopeSession, 50, Match{}, OutcomeDefer, "defer_it"),
	)
	h.Grant("s", "call_recording", time.Hour)

	req := Request{Action: WriteAction("x"), Actor: "a", Subject: "s",
		Roles: []string{"receptionist"}}

	first := h.Engine.Decide(req)
	for i := 0; i < 100; i++ {
		got := h.Engine.Decide(req)
		if got.Outcome != first.Outcome || got.DecidedBy != first.DecidedBy ||
			got.Reason != first.Reason || len(got.Trace) != len(first.Trace) ||
			len(got.Obligations) != len(first.Obligations) {
			t.Fatalf("run %d differed:\nfirst:\n%s\ngot:\n%s", i, first.Explain(), got.Explain())
		}
		for j := range got.Trace {
			if got.Trace[j].Policy != first.Trace[j].Policy ||
				got.Trace[j].Outcome != first.Trace[j].Outcome {
				t.Fatalf("run %d: trace entry %d differed", i, j)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Sweep
// ---------------------------------------------------------------------------

func TestIntegration_SweepExpiresEverythingWithADeadline(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.Grant("s", "call_recording", time.Minute)

	em := TestEmergency("inc", h.Clock.Now().Add(2*time.Minute),
		EmergencyAllowPolicy("emp", Match{}))
	if err := h.Engine.Emergency().Activate(em); err != nil {
		t.Fatal(err)
	}

	d := Decision{ID: NewDecisionID(), Outcome: OutcomeRequireHuman, Reason: "test"}
	if _, err := h.Engine.Human().Raise(d, EscalationApproval, time.Minute); err != nil {
		t.Fatal(err)
	}

	temp := TemporaryPolicy("temp", "team", h.Clock.Now(), time.Minute,
		Rule{Name: "r", Then: OutcomeAllow, Reason: "temporary_allow"})
	h.Register(temp)

	h.Clock.Advance(5 * time.Minute)
	rep := h.Engine.Sweep()

	if rep.ConsentExpired != 1 || rep.EmergenciesEnded != 1 ||
		rep.EscalationsExpired != 1 || rep.PoliciesExpired != 1 {
		t.Fatalf("sweep missed something: %+v", rep)
	}
	if rep.Empty() {
		t.Error("a sweep that expired four things reported itself empty")
	}
}

// ---------------------------------------------------------------------------
// Failure injection
// ---------------------------------------------------------------------------

func TestFailure_AuditFailureDoesNotChangeTheDecision(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(AllowPolicy("p", ScopeGlobal, 100, Match{}))
	h.Audit.FailWith(errors.New("audit store down"))

	d := h.Ask(ReadAction("x"), "a", "s")
	if d.Outcome != OutcomeAllow {
		t.Fatalf("an audit-store outage denied an action the policies permit: %s", d.Outcome)
	}
	if h.Metrics.AuditFailed.Total() == 0 {
		t.Error("an audit failure should be counted so the gap is visible")
	}
}

func TestFailure_ConflictingPoliciesStillProduceASafeDecision(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(
		AllowPolicy("a", ScopeOrganization, 100, Match{}),
		DenyPolicy("b", ScopeOrganization, 100, Match{}),
	)

	d := h.Ask(ReadAction("x"), "actor", "subject")
	if d.Outcome != OutcomeDeny {
		t.Fatalf("an unresolvable conflict resolved to %s; the platform must stay safe "+
			"and the operator must find out", d.Outcome)
	}
	if len(h.Engine.Coordinator().Conflicts()) != 1 {
		t.Error("the conflict was not reported to the operator")
	}
}

// ---------------------------------------------------------------------------
// Concurrency and stress
// ---------------------------------------------------------------------------

func TestStress_ConcurrentDecisions(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(
		AllowPolicy("allow", ScopeGlobal, 100, Match{Kinds: []ActionKind{ActionMemory}}),
		DenyPolicy("deny-external", ScopeGlobal, 100, Match{Kinds: []ActionKind{ActionExternal}}),
	)

	const workers, each = 16, 100
	var wg sync.WaitGroup
	var allows, denies atomic.Int64

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				var d Decision
				if (w+i)%2 == 0 {
					d = h.Ask(ReadAction("x"), "a", "s")
				} else {
					d = h.Ask(ExternalAction("partner", ClassInternal), "a", "s")
				}
				if d.Outcome == OutcomeAllow {
					allows.Add(1)
				} else {
					denies.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()

	total := allows.Load() + denies.Load()
	if total != workers*each {
		t.Fatalf("decisions lost: %d of %d", total, workers*each)
	}
	if h.Engine.Decisions() != uint64(total) {
		t.Fatalf("the engine counted %d decisions, callers made %d",
			h.Engine.Decisions(), total)
	}
	if allows.Load() == 0 || denies.Load() == 0 {
		t.Fatalf("expected a mix of outcomes, got %d allows and %d denies",
			allows.Load(), denies.Load())
	}
}

// TestStress_PolicyChurnDuringDecisions checks INV-GOV-9: a decision is made
// against exactly one snapshot, so reloading policies mid-flight cannot produce
// a decision that half-obeys each rule set.
func TestStress_PolicyChurnDuringDecisions(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(AllowPolicy("stable", ScopeGlobal, 100, Match{}))

	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 2; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			p := AllowPolicy("churn", ScopeGlobal, 50, Match{})
			p.Version = i
			_ = h.Engine.Policies().Register(p)
		}
	}()

	var bad atomic.Int64
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				d := h.Ask(ReadAction("x"), "a", "s")
				// Whatever the churn is doing, the decision must be internally
				// consistent: a decided outcome with a naming policy and a
				// snapshot version.
				if d.DecidedBy == "" || d.PolicyVersion == 0 || d.Reason == "" {
					bad.Add(1)
				}
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	if bad.Load() != 0 {
		t.Fatalf("INV-GOV-9: %d decisions were internally inconsistent during churn", bad.Load())
	}
}

func TestStress_ConcurrentConsentGrantsAndRevocations(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	const subjects = 32
	var wg sync.WaitGroup

	for i := 0; i < subjects; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := SubjectID(fmt.Sprintf("subject-%02d", i))
			h.Grant(s, "call_recording", time.Hour)
			if i%2 == 0 {
				if _, err := h.Engine.Consent().Revoke(s, "call_recording", "test"); err != nil {
					t.Errorf("revoke: %v", err)
				}
			}
		}(i)
	}
	wg.Wait()

	valid, revoked := 0, 0
	for i := 0; i < subjects; i++ {
		s := SubjectID(fmt.Sprintf("subject-%02d", i))
		if h.Engine.Consent().Check(s, "call_recording").Valid {
			valid++
		} else {
			revoked++
		}
	}
	if valid != subjects/2 || revoked != subjects/2 {
		t.Fatalf("expected an even split, got %d valid and %d revoked", valid, revoked)
	}
}

func TestStress_ConcurrentEscalationResolution(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	hr := h.Engine.Human()

	d := Decision{ID: NewDecisionID(), Outcome: OutcomeRequireHuman, Reason: "test"}
	if _, err := hr.Raise(d, EscalationApproval, time.Hour); err != nil {
		t.Fatal(err)
	}

	const racers = 16
	var wg sync.WaitGroup
	var won atomic.Int64

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				_, err = hr.Approve(d.ID, fmt.Sprintf("human-%d", i), "yes")
			} else {
				_, err = hr.Reject(d.ID, fmt.Sprintf("human-%d", i), "no")
			}
			if err == nil {
				won.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if won.Load() != 1 {
		t.Fatalf("%d humans resolved one escalation; two different answers to one "+
			"question is a race with a real-world consequence", won.Load())
	}
}

// ---------------------------------------------------------------------------
// Health and coverage
// ---------------------------------------------------------------------------

func TestIntegration_HealthAnswersTheOperatorQuestions(t *testing.T) {
	t.Parallel()
	h, err := NewHarness(WithBaseline())
	if err != nil {
		t.Fatal(err)
	}

	em := TestEmergency("inc-42", h.Clock.Now().Add(time.Hour),
		EmergencyAllowPolicy("inc-42-allow", Match{Kinds: []ActionKind{ActionTool}}))
	if err := h.Engine.Emergency().Activate(em); err != nil {
		t.Fatal(err)
	}
	h.Grant("s", "call_recording", time.Hour)
	h.Ask(ReadAction("x"), "a", "s")

	rep := h.Engine.Coordinator().Health()

	if rep.Policies != len(BaselinePolicies())+1 {
		t.Errorf("policy count is %d", rep.Policies)
	}
	if rep.PolicyDigest == "" || rep.PolicyVersion == 0 {
		t.Error("health did not report a policy digest or version")
	}
	if len(rep.ActiveEmergencies) != 1 || rep.ActiveEmergencies[0] != "inc-42" {
		t.Errorf("an active emergency must be visible in the health report, got %v",
			rep.ActiveEmergencies)
	}
	if rep.ConsentRecords != 1 {
		t.Errorf("consent record count is %d", rep.ConsentRecords)
	}
	if rep.Decisions == 0 {
		t.Error("the decision counter — the bypass detector — is zero after a decision")
	}
	for kind, n := range rep.Coverage {
		if n == 0 {
			t.Errorf("%s actions have no covering policy and are therefore denied by "+
				"default; the baseline should cover every kind", kind)
		}
	}
}
