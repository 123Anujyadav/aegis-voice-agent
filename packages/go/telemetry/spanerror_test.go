package telemetry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// vettedError is a SafeError: its text is a fixed vocabulary.
type vettedError struct{ reason string }

func (e vettedError) Error() string         { return "fence: " + e.reason + " (with details)" }
func (e vettedError) SafeErrorText() string { return "fence: " + e.reason }

// ADR-0026 rule 4: span error text is allowlisted. Content in an error's own
// message never reaches a span.
func TestSpanErrorText_IsAllowlisted(t *testing.T) {
	t.Parallel()
	const content = "my account number is 1234 and my phone is +919800000000"
	cases := map[string]struct {
		err  error
		want string
	}{
		"plain error":           {errors.New(content), "unclassified error (*errors.errorString)"},
		"wrapped plain error":   {fmt.Errorf("provider said %q: %w", content, errors.New(content)), "unclassified error (*fmt.wrapError)"},
		"context canceled":      {fmt.Errorf("%s: %w", content, context.Canceled), "context canceled"},
		"deadline":              {fmt.Errorf("%s: %w", content, context.DeadlineExceeded), "context deadline exceeded"},
		"runtime shed":          {fmt.Errorf("%s: %w", content, runtime.ErrShed), "runtime: shed at admission"},
		"runtime budget":        {fmt.Errorf("%s: %w", content, runtime.ErrBudgetExceeded), "runtime: budget exceeded"},
		"safe error":            {vettedError{"lock lost"}, "fence: lock lost"},
		"safe error, wrapped":   {fmt.Errorf("%s: %w", content, vettedError{"lock lost"}), "fence: lock lost"},
		"safe beats a sentinel": {fmt.Errorf("%w: %w", vettedError{"lock lost"}, runtime.ErrClosed), "fence: lock lost"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := spanErrorText(tc.err)
			if got != tc.want {
				t.Errorf("spanErrorText = %q, want %q", got, tc.want)
			}
			if strings.Contains(got, "1234") || strings.Contains(got, "+91") || strings.Contains(got, "details") {
				t.Errorf("content leaked into span error text: %q", got)
			}
		})
	}
}

// Every runtime sentinel the kernel can return has a category, so a kernel
// failure is never reduced to a bare type name.
func TestSpanErrorText_CoversEveryRuntimeSentinel(t *testing.T) {
	t.Parallel()
	for _, err := range []error{
		runtime.ErrShed, runtime.ErrNotFound, runtime.ErrClosed, runtime.ErrAborted,
		runtime.ErrProviderUnavailable, runtime.ErrBudgetExceeded,
		runtime.ErrInvalidTransition, runtime.ErrInvariantViolation,
	} {
		if got := spanErrorText(fmt.Errorf("context: %w", err)); got != err.Error() {
			t.Errorf("%v recorded as %q", err, got)
		}
	}
}
