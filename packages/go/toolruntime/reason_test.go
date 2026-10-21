package toolruntime

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// reasonPrecedence is shortReason's contract: each known error's code, in
// the order they are tested. An error matching several (a joined or doubly
// wrapped error) gets the code of the first that matches.
var reasonPrecedence = []struct {
	err  error
	code string
}{
	{ErrTimeout, "timeout"},
	{ErrCancelled, "cancelled"},
	{ErrCircuitOpen, "circuit_open"},
	{ErrPermissionDenied, "permission_denied"},
	{ErrConsentRequired, "consent_required"},
	{ErrInvalidInput, "invalid_input"},
	{ErrInvalidOutput, "invalid_output"},
	{ErrBudgetExceeded, "budget_exceeded"},
	{ErrQueueFull, "queue_full"},
	{ErrDuplicate, "duplicate"},
	{ErrNoHealthyProvider, "no_healthy_tool"},
	{ErrNoCapability, "no_capability"},
	{ErrCompensationFailed, "compensation_failed"},
	{ErrClosed, "closed"},
	{ErrInvariant, "invariant"},
	{context.DeadlineExceeded, "timeout"},
	{context.Canceled, "cancelled"},
}

func TestShortReason_MapsEachKnownErrorAndItsWrapping(t *testing.T) {
	for _, tc := range reasonPrecedence {
		if got := shortReason(tc.err); got != tc.code {
			t.Errorf("shortReason(%v) = %q, want %q", tc.err, got, tc.code)
		}
		if got := shortReason(fmt.Errorf("wrapped: %w", tc.err)); got != tc.code {
			t.Errorf("shortReason(wrapped %v) = %q, want %q", tc.err, got, tc.code)
		}
	}
}

func TestShortReason_NilAndUnknownErrors(t *testing.T) {
	if got := shortReason(nil); got != "" {
		t.Errorf("shortReason(nil) = %q, want empty", got)
	}
	// Unmapped sentinels and foreign errors share one bounded code, so no error
	// text can reach a metric label.
	for _, err := range []error{
		ErrNotRegistered, ErrVersionUnsatisfiable,
		errors.New("a downstream service's own error text"),
	} {
		if got := shortReason(err); got != "tool_error" {
			t.Errorf("shortReason(%v) = %q, want tool_error", err, got)
		}
	}
}

// Every pair of known errors joined together: the earlier one in
// reasonPrecedence wins, whichever order they are joined in.
func TestShortReason_ThePrecedenceOrderDecidesJoinedErrors(t *testing.T) {
	for i, a := range reasonPrecedence {
		for j, b := range reasonPrecedence {
			want := a.code
			if j < i {
				want = b.code
			}
			if got := shortReason(errors.Join(a.err, b.err)); got != want {
				t.Errorf("shortReason(Join(%v, %v)) = %q, want %q", a.err, b.err, got, want)
			}
		}
	}
}
