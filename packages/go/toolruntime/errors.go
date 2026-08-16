package toolruntime

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Sentinel errors.
//
// The set is chosen by asking, for each one, "what would a caller DO
// differently on seeing this?" An error a caller cannot act on differently
// does not deserve its own sentinel, and an error a caller must act on
// differently must have one — the alternative is string matching, which is how
// a retry loop ends up retrying a permission denial forever.
var (
	// ErrNotRegistered indicates no tool is registered under the identifier.
	ErrNotRegistered = errors.New("toolruntime: tool not registered")

	// ErrNoCapability indicates no registered tool provides the requested
	// capability. Distinct from ErrNotRegistered: "nothing can do this" and
	// "this specific thing does not exist" lead to different operator actions.
	ErrNoCapability = errors.New("toolruntime: no tool provides the capability")

	// ErrNoHealthyProvider indicates tools exist for the capability but none is
	// currently usable. The distinction from ErrNoCapability matters
	// enormously: one is a deployment gap, the other is an outage.
	ErrNoHealthyProvider = errors.New("toolruntime: no healthy tool for the capability")

	// ErrVersionUnsatisfiable indicates a version constraint matched nothing.
	ErrVersionUnsatisfiable = errors.New("toolruntime: version constraint unsatisfiable")

	// ErrPermissionDenied indicates the actor may not invoke the tool. Never
	// retried, and deliberately not wrapped in a generic failure: a denial that
	// looks like an outage produces a retry storm against a policy decision.
	ErrPermissionDenied = errors.New("toolruntime: permission denied")

	// ErrConsentRequired indicates the tool needs a consent basis the caller
	// did not supply. Actionable: the caller can ask for consent.
	ErrConsentRequired = errors.New("toolruntime: consent required")

	// ErrInvalidInput indicates arguments failed contract validation. A
	// programming or planning error; never retried.
	ErrInvalidInput = errors.New("toolruntime: input failed contract validation")

	// ErrInvalidOutput indicates a tool returned something its own contract
	// forbids. The execution FAILS rather than passing the value on — a
	// runtime that forwards output it has judged invalid has no contract.
	ErrInvalidOutput = errors.New("toolruntime: output failed contract validation")

	// ErrTimeout indicates an execution exceeded its deadline.
	ErrTimeout = errors.New("toolruntime: execution timed out")

	// ErrCancelled indicates an execution was cancelled by a caller or by
	// supervisor shutdown. Distinct from ErrTimeout because a cancellation is
	// somebody's decision and a timeout is nobody's.
	ErrCancelled = errors.New("toolruntime: execution cancelled")

	// ErrCircuitOpen indicates the tool's breaker is open and the call was not
	// attempted. Fails fast, and says so, rather than presenting as a timeout.
	ErrCircuitOpen = errors.New("toolruntime: circuit open")

	// ErrQueueFull indicates admission was refused because the queue is at
	// capacity. Load shedding, not failure: the caller may degrade or retry.
	ErrQueueFull = errors.New("toolruntime: execution queue full")

	// ErrBudgetExceeded indicates a sandbox budget — wall clock, output size,
	// or step count — was exhausted.
	ErrBudgetExceeded = errors.New("toolruntime: budget exceeded")

	// ErrDuplicate indicates an execution key was already used. The prior
	// outcome is returned; this sentinel accompanies it so a caller can tell a
	// replay from a fresh execution.
	ErrDuplicate = errors.New("toolruntime: duplicate execution key")

	// ErrCompensationFailed indicates a rollback action itself failed. The
	// worst outcome the runtime can produce: the world is now in a state
	// nobody chose. Always surfaced, never swallowed.
	ErrCompensationFailed = errors.New("toolruntime: compensation failed")

	// ErrClosed indicates the runtime is shutting down or stopped.
	ErrClosed = errors.New("toolruntime: closed")

	// ErrInvariant indicates a runtime invariant would be breached. Always a
	// programming error; never retried, never recovered from.
	ErrInvariant = errors.New("toolruntime: invariant violation")
)

// InvariantError names the invariant that was breached.
//
// Named rather than numbered-in-prose, so a production log line points at a
// specific rule in the documentation instead of at a general feeling that
// something went wrong.
type InvariantError struct {
	// Invariant is the identifier, for example "INV-TOOL-3".
	Invariant string
	// Detail explains the breach.
	Detail string
}

func (e *InvariantError) Error() string {
	return fmt.Sprintf("toolruntime: invariant %s violated: %s", e.Invariant, e.Detail)
}

// Is reports that every InvariantError matches ErrInvariant.
func (e *InvariantError) Is(target error) bool { return target == ErrInvariant }

func invariant(id, format string, args ...any) error {
	return &InvariantError{Invariant: id, Detail: fmt.Sprintf(format, args...)}
}

// ConfigError collects every problem with a configuration at once.
//
// One problem at a time turns configuring a runtime into a guessing game: fix
// the reported error, restart, discover the next one. Reporting all of them
// costs a slice and saves a deployment cycle per mistake.
type ConfigError struct {
	// Problems is the complete list, sorted for stable output.
	Problems []string
}

func (e *ConfigError) Error() string {
	p := append([]string(nil), e.Problems...)
	sort.Strings(p)
	return "toolruntime: configuration invalid: " + strings.Join(p, "; ")
}

// ExecutionError carries everything a caller needs to decide what to do next.
//
// A bare error tells a caller that something failed. This tells it WHICH tool
// failed, on which step, on which attempt, whether trying again could possibly
// help, and whether the world was left changed. Those are four different
// decisions and they need four different facts.
type ExecutionError struct {
	// Tool identifies the tool that failed.
	Tool ToolID
	// Version is the pinned version that was executing.
	Version Version
	// Step names the plan step, empty for a single-tool execution.
	Step StepID
	// Attempt is the 1-based attempt number that produced this error.
	Attempt int
	// Phase names where in the execution the failure occurred.
	Phase Phase
	// Retryable reports whether another attempt could plausibly succeed. Set by
	// the classifier, not by the tool: a tool that marks its own errors
	// retryable will eventually mark a permission denial retryable.
	Retryable bool
	// Compensated reports whether rollback ran and succeeded. A caller telling
	// a person "that didn't work" needs to know whether it half-worked.
	Compensated bool
	// Err is the underlying cause.
	Err error
}

func (e *ExecutionError) Error() string {
	var b strings.Builder
	b.WriteString("toolruntime: ")
	b.WriteString(string(e.Tool))
	if e.Version != "" {
		b.WriteString("@")
		b.WriteString(string(e.Version))
	}
	if e.Step != "" {
		b.WriteString(" step ")
		b.WriteString(string(e.Step))
	}
	fmt.Fprintf(&b, " failed in %s on attempt %d", e.Phase, e.Attempt)
	if e.Compensated {
		b.WriteString(" (compensated)")
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap exposes the cause to errors.Is and errors.As.
func (e *ExecutionError) Unwrap() error { return e.Err }

// Phase names where in an execution something happened.
//
// It is a small enum rather than free text because it becomes a metric label,
// and a metric label built from free text is a cardinality incident waiting for
// its first unusual error message.
type Phase string

// The phases an execution passes through.
const (
	PhasePlan        Phase = "plan"
	PhaseAdmit       Phase = "admit"
	PhasePermission  Phase = "permission"
	PhaseIdempotency Phase = "idempotency"
	PhaseValidateIn  Phase = "validate_input"
	PhaseInvoke      Phase = "invoke"
	PhaseValidateOut Phase = "validate_output"
	PhaseCompensate  Phase = "compensate"
	PhaseComplete    Phase = "complete"
)

// String renders the phase.
func (p Phase) String() string { return string(p) }
