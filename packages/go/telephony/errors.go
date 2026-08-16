package telephony

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Sentinel errors. Callers match with errors.Is; none carries call content.
var (
	// ErrInvalidTransition is returned when a state move is not declared in the
	// transition table.
	ErrInvalidTransition = errors.New("telephony: invalid state transition")

	// ErrCallNotFound is returned when a call identifier is unknown to the
	// registry.
	ErrCallNotFound = errors.New("telephony: call not found")

	// ErrCallExists is returned when a call identifier is already registered.
	ErrCallExists = errors.New("telephony: call already registered")

	// ErrCapacityExceeded is returned when the scheduler refuses admission.
	//
	// A refusal, not a queue. See [CallScheduler] for why an overloaded
	// telephony runtime must shed rather than buffer.
	ErrCapacityExceeded = errors.New("telephony: capacity exceeded")

	// ErrTerminal is returned when an operation is attempted on a call that has
	// already ended or failed.
	ErrTerminal = errors.New("telephony: call has terminated")

	// ErrCapabilityUnsupported is returned when an operation requires a
	// provider capability that was not declared.
	ErrCapabilityUnsupported = errors.New("telephony: provider does not support this capability")

	// ErrProviderNotRegistered is returned when a call names an unknown
	// provider.
	ErrProviderNotRegistered = errors.New("telephony: provider not registered")

	// ErrRuntimeStopped is returned by a runtime that is draining or stopped.
	ErrRuntimeStopped = errors.New("telephony: runtime is stopped")

	// ErrNotRecoverable is returned when a snapshot cannot be resumed.
	ErrNotRecoverable = errors.New("telephony: session is not recoverable")

	// ErrSnapshotNotFound is returned by a [SessionStore] for an unknown
	// session.
	ErrSnapshotNotFound = errors.New("telephony: snapshot not found")
)

// ConfigError reports one or more configuration problems.
//
// EVERY problem, not the first. A configuration with four mistakes should
// require one fix-and-restart cycle, not four — the alternative teaches
// operators that a failed boot means "change something and try again".
type ConfigError struct{ Problems []string }

func (e *ConfigError) Error() string {
	if len(e.Problems) == 1 {
		return "telephony: " + e.Problems[0]
	}
	sorted := append([]string(nil), e.Problems...)
	sort.Strings(sorted)
	return fmt.Sprintf("telephony: %d configuration problems:\n  - %s",
		len(sorted), strings.Join(sorted, "\n  - "))
}

// invariant builds an error naming a violated invariant.
//
// Invariants are numbered and the number appears in the message, so a support
// report quoting an error can be traced to the documented rule without anybody
// guessing which check produced it.
func invariant(id, format string, args ...any) error {
	return fmt.Errorf("telephony: %s violated: %s", id, fmt.Sprintf(format, args...))
}

// TransitionError describes a refused state change.
//
// Carries the states and the call identifier, never the caller's number or
// anything said. A telephony error reaches log aggregation, and a log line
// naming a phone number is a privacy incident that outlives the call by however
// long the retention is.
type TransitionError struct {
	Call CallID
	From CallState
	To   CallState
	// Reason is a bounded code, not free text.
	Reason string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("telephony: call %s cannot move %s -> %s (%s)",
		e.Call, e.From, e.To, e.Reason)
}

// Unwrap lets errors.Is match ErrInvalidTransition.
func (e *TransitionError) Unwrap() error { return ErrInvalidTransition }
