package toolruntime

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestAudit_FailuresOutsideTheExecutorAreCounted pins the Auditor contract —
// "An error is counted; it does not fail the execution" — for the registry,
// permission and compensation paths. Each used to discard the error uncounted,
// so an audit-store outage during an override, a denial or a rollback left no
// trace at all.
func TestAudit_FailuresOutsideTheExecutorAreCounted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Audit.FailWith(errors.New("audit store down"))

	// Registry: register and unregister still succeed.
	extra := ReadContract("extra", "1.0.0", "extra")
	h.Register(extra, &FakeTool{})
	if err := h.Runtime.Registry().Unregister(extra.Descriptor); err != nil {
		t.Fatalf("Unregister failed on an audit outage: %v", err)
	}

	// Permissions: a denial, then an override that is installed and used.
	pe := h.Runtime.Permissions()
	c := ReadContract("t", "1.0.0", "cap")
	c.RequiredPermissions = []Permission{"a"}
	if d := pe.Evaluate(c, Grant{Actor: "x"}); d.Allowed {
		t.Fatal("a call without its permission was allowed")
	}
	if err := pe.AddOverride(Override{
		Name: "o", Permissions: []Permission{"a"},
		ExpiresAt: h.Clock.Now().Add(time.Hour), AuthorisedBy: "oncall", Reason: "incident-1",
	}); err != nil {
		t.Fatalf("AddOverride failed on an audit outage: %v", err)
	}
	if d := pe.Evaluate(c, Grant{Actor: "x"}); !d.Allowed {
		t.Fatal("a covering override did not allow the call")
	}

	// Compensation: a mutation followed by a failing step is rolled back.
	undo := &CompensatingFake{}
	h.Register(WriteContract("w", "1.0.0", "write"), undo)
	boom := WriteContract("boom", "1.0.0", "boom")
	boom.Compensable = false
	boom.Retry = RetrySpec{MaxAttempts: 1}
	h.Register(boom, &WriteFake{FakeTool: FakeTool{FailAlways: true}})
	res, _ := h.Runtime.Execute(context.Background(),
		evalIntent(write("s0", "write"), write("boom", "boom", "s0")))
	if res.Compensation == nil || undo.Compensations() != 1 {
		t.Fatalf("the rollback did not run: %+v", res.Compensation)
	}

	for _, kind := range []AuditKind{
		AuditRegistered, AuditUnregistered,
		AuditPermissionDenied, AuditOverrideInstalled, AuditOverrideUsed,
		AuditCompensated,
	} {
		if h.Metrics.AuditFailed.Value(string(kind)) == 0 {
			t.Errorf("an audit failure for %s was not counted", kind)
		}
		if got := h.Metrics.AuditWritten.Value(string(kind)); got != 0 {
			t.Errorf("%s counted %d writes while every write failed", kind, got)
		}
	}
}

// TestAudit_WritesOutsideTheExecutorAreCounted is the healthy-store
// counterpart: a successful write is counted as written, not failed.
func TestAudit_WritesOutsideTheExecutorAreCounted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.Register(ReadContract("t", "1.0.0", "cap"), &FakeTool{})

	if h.Metrics.AuditWritten.Value(string(AuditRegistered)) == 0 {
		t.Error("a successful registration write was not counted")
	}
	if got := h.Metrics.AuditFailed.Value(string(AuditRegistered)); got != 0 {
		t.Errorf("registration counted %d failures with a healthy store", got)
	}
}
