package app

import (
	"context"
	"errors"
	"testing"
)

// While the F-1 tester allowlist is configured, a deletion code (an SMS like
// a sign-in code) is never requested for a number outside it: the refusal
// comes first, sends nothing and spends neither the deletion budget nor the
// number's allowance. Deletion codes are not wired in production (O13); this
// keeps them from ever bypassing the allowlist.
func TestWebDeletion_TheTesterAllowlistAppliesFirst(t *testing.T) {
	ctx := context.Background()
	w := newWebRig(ctx, t, 1) // a budget of one request this hour
	if err := w.svc.SetSignInDeliveryControls([]string{rlMSISDN}, 5); err != nil {
		t.Fatal(err)
	}
	// More refusals than the number's allowance (3) and the budget (1).
	for i := 0; i < 5; i++ {
		if _, err := w.svc.RequestDeletionCode(ctx, noAccountMSISDN); !errors.Is(err, ErrDeletionCodesUnavailable) {
			t.Fatalf("unlisted number, request %d: err = %v, want ErrDeletionCodesUnavailable", i+1, err)
		}
	}
	if w.sender.calls != 0 {
		t.Fatalf("%d deletion codes sent to an unlisted number", w.sender.calls)
	}
	// The budget is untouched: the listed number's one request goes through.
	w.request(ctx, t, rlMSISDN)
	if w.sender.calls != 1 {
		t.Errorf("the listed number's code was not sent (%d sends)", w.sender.calls)
	}
}

// Without the controls (development, tests) deletion codes behave as before.
func TestWebDeletion_WithoutTheControlsNoAllowlistApplies(t *testing.T) {
	ctx := context.Background()
	w := newWebRig(ctx, t, 5)
	w.request(ctx, t, rlMSISDN)
	if w.sender.calls != 1 {
		t.Errorf("%d sends, want 1", w.sender.calls)
	}
}
