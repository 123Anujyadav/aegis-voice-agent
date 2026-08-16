package governance

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Sentinel errors.
//
// Chosen by asking, for each one, "what would a caller DO differently on seeing
// this?" A governance engine that returns one generic error teaches callers to
// treat every refusal identically, and the first thing they do identically is
// retry.
var (
	// ErrDenied indicates the action is refused outright. Never retried; the
	// answer will not change by asking again.
	ErrDenied = errors.New("governance: denied")

	// ErrNotPermitted indicates the action may not proceed YET — an obligation
	// is outstanding. Distinct from ErrDenied because a caller can act on it:
	// obtain consent, ask for confirmation, wait for approval.
	ErrNotPermitted = errors.New("governance: not permitted without satisfying obligations")

	// ErrNoPolicy indicates no policy matched and the engine defaulted. Always
	// accompanied by a denial; surfaced separately because it is an OPERATOR
	// problem, not a caller problem — somebody has not written a rule.
	ErrNoPolicy = errors.New("governance: no policy matched; default applied")

	// ErrPolicyConflict indicates two policies at identical precedence
	// disagreed in a way the resolver could not break. A configuration error,
	// not a runtime one.
	ErrPolicyConflict = errors.New("governance: unresolvable policy conflict")

	// ErrConsentNotFound indicates no consent record exists for the basis.
	ErrConsentNotFound = errors.New("governance: consent not found")

	// ErrConsentExpired indicates a consent record exists but has lapsed.
	// Distinct from not-found: "never given" and "given and lapsed" call for
	// different conversations with the subject.
	ErrConsentExpired = errors.New("governance: consent expired")

	// ErrConsentRevoked indicates the subject withdrew consent. Distinct
	// again, and the distinction is a legal one.
	ErrConsentRevoked = errors.New("governance: consent revoked")

	// ErrConsentSuperseded indicates a newer version of the consent terms
	// exists and the record is against an older one.
	ErrConsentSuperseded = errors.New("governance: consent version superseded")

	// ErrNotRegistered indicates no policy is registered under the identifier.
	ErrNotRegistered = errors.New("governance: policy not registered")

	// ErrImmutableScope indicates an attempt to override a scope that cannot
	// be overridden. Compliance is not negotiable at 3 a.m.
	ErrImmutableScope = errors.New("governance: scope cannot be overridden")

	// ErrEscalationNotFound indicates no pending escalation under the
	// identifier.
	ErrEscalationNotFound = errors.New("governance: escalation not found")

	// ErrAlreadyResolved indicates an escalation was already decided. Two
	// humans resolving one escalation differently is a race with a real-world
	// consequence, so the second one loses and is told.
	ErrAlreadyResolved = errors.New("governance: escalation already resolved")

	// ErrClosed indicates the engine is shutting down or stopped.
	ErrClosed = errors.New("governance: closed")

	// ErrInvariant indicates an engine invariant would be breached. Always a
	// programming error; never retried, never recovered from.
	ErrInvariant = errors.New("governance: invariant violation")
)

// InvariantError names the invariant that was breached.
type InvariantError struct {
	// Invariant is the identifier, for example "INV-GOV-2".
	Invariant string
	// Detail explains the breach.
	Detail string
}

func (e *InvariantError) Error() string {
	return fmt.Sprintf("governance: invariant %s violated: %s", e.Invariant, e.Detail)
}

// Is reports that every InvariantError matches ErrInvariant.
func (e *InvariantError) Is(target error) bool { return target == ErrInvariant }

func invariant(id, format string, args ...any) error {
	return &InvariantError{Invariant: id, Detail: fmt.Sprintf(format, args...)}
}

// ConfigError collects every problem with a configuration at once.
//
// One problem at a time turns policy authoring into a guessing game: fix the
// reported error, reload, discover the next one. Reporting all of them costs a
// slice and saves a deployment cycle per mistake — and policy files are exactly
// the artefact where a person makes six mistakes at once.
type ConfigError struct {
	// Problems is the complete list, sorted for stable output.
	Problems []string
}

func (e *ConfigError) Error() string {
	p := append([]string(nil), e.Problems...)
	sort.Strings(p)
	return "governance: configuration invalid: " + strings.Join(p, "; ")
}

// ConflictError describes two policies that could not be ordered.
//
// It names both, because "there is a conflict" is not something an operator can
// fix and "policy A and policy B both claim priority 100 in the business scope
// and disagree" is.
type ConflictError struct {
	// A and B are the conflicting policies.
	A, B PolicyID
	// Scope is where they sit.
	Scope Scope
	// Priority is the priority they share.
	Priority int
	// Outcomes are what each decided.
	OutcomeA, OutcomeB Outcome
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf(
		"governance: %s and %s both sit at priority %d in the %s scope and "+
			"disagree (%s vs %s); give one of them a distinct priority",
		e.A, e.B, e.Priority, e.Scope, e.OutcomeA, e.OutcomeB)
}

// Is reports that every ConflictError matches ErrPolicyConflict.
func (e *ConflictError) Is(target error) bool { return target == ErrPolicyConflict }
