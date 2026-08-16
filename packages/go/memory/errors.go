package memory

import (
	"errors"
	"fmt"
)

// Sentinel errors. Deliberately few: a caller distinguishes errors only to
// decide what to DO, and there are not many different things to do with a
// memory operation that failed.
var (
	// ErrNotFound indicates no record exists for the key. Distinct from
	// ErrExpired and ErrRedacted, because "never existed", "existed and aged
	// out" and "existed and was destroyed on purpose" are three different
	// facts and a caller frequently needs to tell them apart.
	ErrNotFound = errors.New("memory: not found")

	// ErrExpired indicates the record existed and has passed its TTL.
	ErrExpired = errors.New("memory: expired")

	// ErrRedacted indicates the record exists but its payload was destroyed.
	ErrRedacted = errors.New("memory: redacted")

	// ErrArchived indicates the record is in cold storage and must be restored
	// before it can be read.
	ErrArchived = errors.New("memory: archived")

	// ErrVersionConflict indicates an optimistic-lock failure: the record
	// changed between read and write. The caller re-reads and retries.
	ErrVersionConflict = errors.New("memory: version conflict")

	// ErrConsentRequired indicates a Personal or Sensitive record was
	// submitted with no consent basis. It is refused at write; an unlawful
	// memory is never created and then detected.
	ErrConsentRequired = errors.New("memory: consent reference required")

	// ErrLegalHold indicates an erasure was refused because the record is
	// retained under a legal obligation. Not a failure — a lawful outcome the
	// caller must report to the subject.
	ErrLegalHold = errors.New("memory: retained under legal hold")

	// ErrBudgetExceeded indicates a store or context build exceeded its size
	// or token budget.
	ErrBudgetExceeded = errors.New("memory: budget exceeded")

	// ErrInvalidTransition indicates an undeclared lifecycle transition.
	// Always a programming error.
	ErrInvalidTransition = errors.New("memory: invalid lifecycle transition")

	// ErrClosed indicates the runtime is shutting down.
	ErrClosed = errors.New("memory: closed")

	// ErrInvariant indicates an engine invariant would be breached. Always a
	// programming error; never retried.
	ErrInvariant = errors.New("memory: invariant violation")
)

// InvariantError names the invariant that was breached.
type InvariantError struct {
	// Invariant is the identifier, for example "INV-MEM-2".
	Invariant string
	// Detail explains what was attempted.
	Detail string
}

// Error implements error.
func (e *InvariantError) Error() string {
	return fmt.Sprintf("memory: invariant %s violated: %s", e.Invariant, e.Detail)
}

// Is allows errors.Is(err, ErrInvariant) to match.
func (e *InvariantError) Is(target error) bool { return target == ErrInvariant }

// invariant constructs an InvariantError. Unexported: only this package may
// assert an invariant of the memory engine.
func invariant(id, format string, args ...any) error {
	return &InvariantError{Invariant: id, Detail: fmt.Sprintf(format, args...)}
}

// ConflictError describes an optimistic-lock failure with enough detail for the
// caller to decide between retry, merge and abandon.
type ConflictError struct {
	// Key identifies the contested record.
	Key Key
	// Expected is the version the writer believed current.
	Expected Version
	// Actual is the version found.
	Actual Version
}

// Error implements error.
func (e *ConflictError) Error() string {
	return fmt.Sprintf("memory: version conflict on %s: expected v%d, found v%d",
		e.Key, e.Expected, e.Actual)
}

// Is allows errors.Is(err, ErrVersionConflict) to match.
func (e *ConflictError) Is(target error) bool { return target == ErrVersionConflict }

// Stale reports how many versions behind the writer was. A writer one version
// behind may reasonably retry; one that is twenty behind is working from a very
// old read and should probably abandon.
func (e *ConflictError) Stale() uint64 {
	if e.Actual <= e.Expected {
		return 0
	}
	return uint64(e.Actual - e.Expected)
}

// ConfigError reports one or more configuration problems, aggregated rather
// than reported one per restart cycle.
type ConfigError struct {
	Problems []string
}

// Error implements error.
func (e *ConfigError) Error() string {
	if len(e.Problems) == 1 {
		return "memory: configuration invalid: " + e.Problems[0]
	}
	s := fmt.Sprintf("memory: configuration invalid (%d problems):", len(e.Problems))
	for _, p := range e.Problems {
		s += "\n  - " + p
	}
	return s
}
