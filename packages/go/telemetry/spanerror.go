package telemetry

import (
	"context"
	"errors"
	"fmt"

	"github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// SafeError is implemented by errors whose text has been vetted to carry no
// PERSONAL or SENSITIVE data: no phone number, no transcript or prompt
// content, no credential. Only such text is ever recorded on a span as-is.
type SafeError interface {
	error
	// SafeErrorText returns the vetted, fixed-vocabulary description.
	SafeErrorText() string
}

// knownErrors maps sentinel errors to the fixed category recorded for them.
// The sentinels are identifiers, not messages: a wrapping error's own text,
// which may quote a request or a provider response, is never recorded.
var knownErrors = []struct {
	err  error
	text string
}{
	{context.Canceled, "context canceled"},
	{context.DeadlineExceeded, "context deadline exceeded"},
	{runtime.ErrShed, "runtime: shed at admission"},
	{runtime.ErrNotFound, "runtime: not found"},
	{runtime.ErrClosed, "runtime: closed"},
	{runtime.ErrAborted, "runtime: aborted"},
	{runtime.ErrProviderUnavailable, "runtime: provider unavailable"},
	{runtime.ErrBudgetExceeded, "runtime: budget exceeded"},
	{runtime.ErrInvalidTransition, "runtime: invalid state transition"},
	{runtime.ErrInvariantViolation, "runtime: invariant violation"},
}

// spanErrorText returns what a span records for err (ADR-0026 rule 4).
//
// Error text is allowlisted, not passed through: err.Error() can embed content
// that no attribute allowlist sees, such as a provider response that quotes a
// prompt, and the platform logger redacts by key, not by value. So, in order:
//  1. the vetted text of the first SafeError in err's chain;
//  2. the fixed category of the first known sentinel err wraps;
//  3. otherwise only err's Go type, which names a kind of failure and carries
//     no data.
func spanErrorText(err error) string {
	var safe SafeError
	if errors.As(err, &safe) {
		return safe.SafeErrorText()
	}
	for _, k := range knownErrors {
		if errors.Is(err, k.err) {
			return k.text
		}
	}
	return fmt.Sprintf("unclassified error (%T)", err)
}
