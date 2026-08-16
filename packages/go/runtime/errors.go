package runtime

import (
	"errors"
	"fmt"
)

// Sentinel errors. Callers test with errors.Is; none of these carry detail on
// their own, because a sentinel that carries state cannot be compared.
//
// These are deliberately few. A runtime that returns forty distinct error
// values pushes classification work onto every caller; one that returns three
// pushes it nowhere. The set below is the smallest that lets a caller decide
// what to DO, which is the only reason to distinguish errors at all.
var (
	// ErrShed indicates the request was refused at admission because the
	// runtime is over capacity. It is not a failure: the caller's correct
	// response is to degrade gracefully, and in this platform that means the
	// call rings through unscreened (ADR-0002 §6).
	ErrShed = errors.New("runtime: shed at admission")

	// ErrNotFound indicates a referenced session, model, provider or prompt
	// does not exist.
	ErrNotFound = errors.New("runtime: not found")

	// ErrClosed indicates the kernel, a session, or a stream has been closed
	// and cannot accept further work.
	ErrClosed = errors.New("runtime: closed")

	// ErrAborted indicates work was cancelled by an explicit abort — a barge-in
	// or a takeover — rather than by failure or timeout. It is distinct from
	// context.Canceled because an abort is a product event worth counting,
	// whereas a cancelled context is frequently just a client disconnecting.
	ErrAborted = errors.New("runtime: aborted")

	// ErrProviderUnavailable indicates every candidate provider for a request
	// is failing or circuit-broken.
	ErrProviderUnavailable = errors.New("runtime: provider unavailable")

	// ErrBudgetExceeded indicates the request could not be assembled within its
	// token budget, or exhausted its latency budget before completion.
	ErrBudgetExceeded = errors.New("runtime: budget exceeded")

	// ErrInvalidTransition indicates a state machine was asked for a transition
	// that is not legal from its current state.
	ErrInvalidTransition = errors.New("runtime: invalid state transition")

	// ErrInvariantViolation indicates a request would breach a frozen platform
	// invariant. It is always a programming error, never a runtime condition,
	// and it is never retried.
	ErrInvariantViolation = errors.New("runtime: invariant violation")
)

// ProviderError wraps a failure originating inside a provider, preserving
// enough structure for the circuit breaker and the retry policy to decide
// without parsing strings.
//
// Whether a failure is retryable is a property of the failure, not of the
// caller's patience. Deciding it here, once, at the boundary where the vendor's
// shape is still visible, is what keeps retry logic out of every call site.
type ProviderError struct {
	// Provider identifies which provider failed.
	Provider ProviderID

	// Model identifies which model was targeted, when known.
	Model ModelID

	// Kind classifies the failure for the breaker and the retry policy.
	Kind ProviderErrorKind

	// StatusCode is the transport status where one exists, for diagnostics
	// only. Nothing branches on it — that is Kind's job.
	StatusCode int

	// Err is the underlying error.
	Err error
}

// ProviderErrorKind classifies provider failures by what the runtime should do
// about them.
type ProviderErrorKind int

const (
	// KindUnknown is an unclassified failure. Treated as retryable-once and
	// counted against the breaker, because an unclassified failure is more
	// likely to be transient than permanent, but assuming so indefinitely
	// turns a hard failure into a retry storm.
	KindUnknown ProviderErrorKind = iota

	// KindTransport is a network-level failure: connection refused, reset,
	// DNS. Retryable, counts against the breaker.
	KindTransport

	// KindTimeout is the provider exceeding its deadline. Retryable only if the
	// remaining latency budget permits, which is usually does not on the hot
	// path. Counts against the breaker.
	KindTimeout

	// KindRateLimited is the provider refusing on quota. Retryable after
	// backoff, and deliberately does NOT count against the breaker: rate
	// limiting means the provider is healthy and we are asking too fast.
	// Opening a breaker on it would convert throttling into an outage.
	KindRateLimited

	// KindOverloaded is the provider reporting temporary capacity exhaustion.
	// Retryable with backoff; counts against the breaker at reduced weight.
	KindOverloaded

	// KindInvalidRequest is a malformed or unacceptable request. NOT retryable
	// and does NOT count against the breaker — the fault is ours, and retrying
	// or opening a circuit would blame the provider for our bug.
	KindInvalidRequest

	// KindAuth is a credential or permission failure. Not retryable; alerts
	// rather than degrades, because it does not resolve on its own.
	KindAuth

	// KindContentFiltered is the provider refusing to produce output on safety
	// grounds. Not retryable and not a fault: it is a legitimate outcome the
	// layer above must handle.
	KindContentFiltered
)

// String renders the kind for logs and metric labels.
func (k ProviderErrorKind) String() string {
	switch k {
	case KindTransport:
		return "transport"
	case KindTimeout:
		return "timeout"
	case KindRateLimited:
		return "rate_limited"
	case KindOverloaded:
		return "overloaded"
	case KindInvalidRequest:
		return "invalid_request"
	case KindAuth:
		return "auth"
	case KindContentFiltered:
		return "content_filtered"
	default:
		return "unknown"
	}
}

// Retryable reports whether a failure of this kind may be retried at all.
// Whether it SHOULD be is a separate question the retry policy answers using
// the remaining latency budget.
func (k ProviderErrorKind) Retryable() bool {
	switch k {
	case KindTransport, KindTimeout, KindRateLimited, KindOverloaded, KindUnknown:
		return true
	default:
		return false
	}
}

// CountsAgainstBreaker reports whether a failure of this kind is evidence that
// the provider itself is unhealthy.
//
// Rate limiting and invalid requests are excluded deliberately. Both are
// failures, and neither says the provider is broken — one says we are asking
// too fast, the other says we asked wrongly. A breaker that opens on either
// removes a healthy provider from rotation and makes the incident worse.
func (k ProviderErrorKind) CountsAgainstBreaker() bool {
	switch k {
	case KindRateLimited, KindInvalidRequest, KindAuth, KindContentFiltered:
		return false
	default:
		return true
	}
}

// Error implements error.
func (e *ProviderError) Error() string {
	if e.Model != "" {
		return fmt.Sprintf("runtime: provider %s (model %s) failed: %s: %v",
			e.Provider, e.Model, e.Kind, e.Err)
	}
	return fmt.Sprintf("runtime: provider %s failed: %s: %v", e.Provider, e.Kind, e.Err)
}

// Unwrap exposes the underlying error to errors.Is and errors.As.
func (e *ProviderError) Unwrap() error { return e.Err }

// Is allows a *ProviderError to satisfy errors.Is(err, ErrProviderUnavailable)
// for the failure kinds that genuinely mean the provider cannot serve.
func (e *ProviderError) Is(target error) bool {
	if target == ErrProviderUnavailable {
		switch e.Kind {
		case KindTransport, KindTimeout, KindOverloaded, KindAuth:
			return true
		}
	}
	return false
}

// InvariantError reports an attempt to breach a frozen platform invariant.
//
// It carries the invariant's identifier so a violation names the rule it broke
// rather than describing a symptom. These are always programming errors: they
// are returned so a test fails loudly, never so a caller can recover.
type InvariantError struct {
	// Invariant is the frozen identifier, for example "I3" or "INV-AI-2".
	Invariant string

	// Detail explains what was attempted.
	Detail string
}

// Error implements error.
func (e *InvariantError) Error() string {
	return fmt.Sprintf("runtime: invariant %s violated: %s", e.Invariant, e.Detail)
}

// Is allows errors.Is(err, ErrInvariantViolation) to match.
func (e *InvariantError) Is(target error) bool { return target == ErrInvariantViolation }

// invariant constructs an InvariantError. Unexported because only this package
// may assert an invariant of the runtime.
func invariant(id, format string, args ...any) error {
	return &InvariantError{Invariant: id, Detail: fmt.Sprintf(format, args...)}
}

// ConfigError reports one or more problems in runtime configuration.
//
// It aggregates rather than failing on the first problem, for the same reason
// platform.ConfigError does: an operator correcting a misconfigured deployment
// needs the complete list, not one problem per restart cycle.
type ConfigError struct {
	Problems []string
}

// Error implements error.
func (e *ConfigError) Error() string {
	if len(e.Problems) == 1 {
		return "runtime: configuration invalid: " + e.Problems[0]
	}
	s := fmt.Sprintf("runtime: configuration invalid (%d problems):", len(e.Problems))
	for _, p := range e.Problems {
		s += "\n  - " + p
	}
	return s
}
