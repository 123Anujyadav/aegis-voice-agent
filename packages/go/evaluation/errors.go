package evaluation

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Sentinel errors.
//
// Chosen by asking what a caller would DO differently. An evaluation platform
// that returns one generic error teaches operators to treat every red result
// identically, and the first thing they do identically is re-run it.
var (
	// ErrNotRegistered indicates no scenario or suite under the identifier.
	ErrNotRegistered = errors.New("evaluation: not registered")

	// ErrNoSubject indicates the scenario names a subject that is not
	// registered. An OPERATOR problem — somebody has not wired an adapter —
	// distinct from the subject being unable to do the thing.
	ErrNoSubject = errors.New("evaluation: subject not registered")

	// ErrNoGolden indicates no approved baseline exists for a scenario.
	// Not a failure: it is the state every new scenario starts in, and the
	// verdict is [VerdictNoBaseline] rather than a red result nobody can act on.
	ErrNoGolden = errors.New("evaluation: no approved golden")

	// ErrGoldenVersionMismatch indicates the golden was recorded from a
	// different scenario version. Comparing across it would produce drift
	// nobody can explain, so it is refused.
	ErrGoldenVersionMismatch = errors.New("evaluation: golden was recorded from a different scenario version")

	// ErrNotApproved indicates a candidate observation was used where an
	// approved golden is required.
	ErrNotApproved = errors.New("evaluation: golden candidate has not been approved")

	// ErrAlreadyApproved indicates an attempt to approve a golden twice.
	ErrAlreadyApproved = errors.New("evaluation: golden already approved")

	// ErrScenarioTimeout indicates a scenario exceeded its budget.
	ErrScenarioTimeout = errors.New("evaluation: scenario timed out")

	// ErrRunClosed indicates the runtime is stopped.
	ErrRunClosed = errors.New("evaluation: runtime closed")

	// ErrNoBaselineRun indicates a regression comparison with nothing to
	// compare against.
	ErrNoBaselineRun = errors.New("evaluation: no baseline run")

	// ErrInvariant indicates a platform invariant would be breached. Always a
	// programming error; never retried.
	ErrInvariant = errors.New("evaluation: invariant violation")
)

// InvariantError names the invariant that was breached.
type InvariantError struct {
	// Invariant is the identifier, for example "INV-EVAL-3".
	Invariant string
	// Detail explains the breach.
	Detail string
}

func (e *InvariantError) Error() string {
	return fmt.Sprintf("evaluation: invariant %s violated: %s", e.Invariant, e.Detail)
}

// Is reports that every InvariantError matches ErrInvariant.
func (e *InvariantError) Is(target error) bool { return target == ErrInvariant }

func invariant(id, format string, args ...any) error {
	return &InvariantError{Invariant: id, Detail: fmt.Sprintf(format, args...)}
}

// ConfigError collects every problem with a configuration at once.
//
// Scenarios and suites are exactly the artefact where somebody makes six
// mistakes in one sitting. Reporting them one at a time turns authoring into a
// guessing game.
type ConfigError struct {
	// Problems is the complete list, sorted for stable output.
	Problems []string
}

func (e *ConfigError) Error() string {
	p := append([]string(nil), e.Problems...)
	sort.Strings(p)
	return "evaluation: configuration invalid: " + strings.Join(p, "; ")
}
