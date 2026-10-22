package governance

import (
	"errors"
	"testing"
	"time"
)

// TestAudit_FailuresOutsideTheDecisionPathAreCounted pins the Auditor
// contract — "An error is counted; it does not fail the decision" — for the
// registry, emergency and escalation paths. Each used to discard the error
// uncounted, so an audit-store outage during a policy change, an emergency
// override or an escalation left no trace at all.
func TestAudit_FailuresOutsideTheDecisionPathAreCounted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Audit.FailWith(errors.New("audit store down"))

	// Every operation still succeeds: the failure is counted, not raised.
	h.Register(AllowPolicy("p", ScopeGlobal, 100, Match{}))
	if err := h.Engine.Policies().Register(AllowPolicy("q", ScopeGlobal, 90, Match{})); err != nil {
		t.Fatalf("Register failed on an audit outage: %v", err)
	}
	if err := h.Engine.Policies().SetEnabled("p", false); err != nil {
		t.Fatalf("SetEnabled failed on an audit outage: %v", err)
	}
	if err := h.Engine.Policies().Unregister("p"); err != nil {
		t.Fatalf("Unregister failed on an audit outage: %v", err)
	}

	em := TestEmergency("inc", h.Clock.Now().Add(time.Minute),
		EmergencyAllowPolicy("emp", Match{}))
	if err := h.Engine.Emergency().Activate(em); err != nil {
		t.Fatalf("Activate failed on an audit outage: %v", err)
	}
	if d := h.Ask(ReadAction("x"), "a", "s"); d.Outcome != OutcomeAllow {
		t.Fatalf("decision under the emergency = %s, want allow", d.Outcome)
	}
	if err := h.Engine.Emergency().Deactivate("inc", "resolved"); err != nil {
		t.Fatalf("Deactivate failed on an audit outage: %v", err)
	}

	d := Decision{ID: NewDecisionID(), Outcome: OutcomeRequireHuman, Reason: "test"}
	if _, err := h.Engine.Human().Raise(d, EscalationApproval, time.Minute); err != nil {
		t.Fatalf("Raise failed on an audit outage: %v", err)
	}
	if _, err := h.Engine.Human().Resolve(d.ID, ResolutionApproved, "reviewer", ""); err != nil {
		t.Fatalf("Resolve failed on an audit outage: %v", err)
	}

	for _, kind := range []AuditKind{
		AuditPolicyRegistered, AuditPolicyToggled, AuditPolicyUnregistered,
		AuditEmergencyActivated, AuditEmergencyUsed, AuditEmergencyExpired,
		AuditEscalated, AuditEscalationResolved,
	} {
		if h.Metrics.AuditFailed.Value(string(kind)) == 0 {
			t.Errorf("an audit failure for %s was not counted", kind)
		}
		if got := h.Metrics.AuditWritten.Value(string(kind)); got != 0 {
			t.Errorf("%s counted %d writes while every write failed", kind, got)
		}
	}
}

// TestAudit_WritesOutsideTheDecisionPathAreCounted is the healthy-store
// counterpart: a successful write is counted as written, not failed.
func TestAudit_WritesOutsideTheDecisionPathAreCounted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.Register(AllowPolicy("p", ScopeGlobal, 100, Match{}))
	if err := h.Engine.Policies().SetEnabled("p", false); err != nil {
		t.Fatal(err)
	}

	for _, kind := range []AuditKind{AuditPolicyRegistered, AuditPolicyToggled} {
		if h.Metrics.AuditWritten.Value(string(kind)) == 0 {
			t.Errorf("a successful %s write was not counted", kind)
		}
		if got := h.Metrics.AuditFailed.Value(string(kind)); got != 0 {
			t.Errorf("%s counted %d failures with a healthy store", kind, got)
		}
	}
}
