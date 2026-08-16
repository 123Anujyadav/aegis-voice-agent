package conversation

import (
	"errors"
	"fmt"
)

// Sentinel errors. Few, because a caller distinguishes errors only to decide
// what to DO, and there are not many different things to do on a live call.
var (
	// ErrNotAllowed indicates the policy engine denied an action. It is a
	// domain outcome, not a fault: the engine asked whether something was
	// permitted and was told no.
	ErrNotAllowed = errors.New("conversation: action not permitted by policy")

	// ErrInvalidTransition indicates an undeclared state transition was
	// attempted. Always a programming error.
	ErrInvalidTransition = errors.New("conversation: invalid state transition")

	// ErrTerminal indicates the conversation has ended and cannot accept work.
	ErrTerminal = errors.New("conversation: terminal")

	// ErrFloorHeld indicates the floor could not be acquired.
	ErrFloorHeld = errors.New("conversation: floor is held")

	// ErrBudgetExhausted indicates a latency budget was spent before the stage
	// could run.
	ErrBudgetExhausted = errors.New("conversation: latency budget exhausted")

	// ErrClarificationExhausted indicates the clarification budget is spent.
	// The correct response is to escalate, not to ask again — asking a fourth
	// time is how a voice product becomes a punchline.
	ErrClarificationExhausted = errors.New("conversation: clarification budget exhausted")

	// ErrNoIntent indicates classification produced nothing usable and no
	// fallback was configured.
	ErrNoIntent = errors.New("conversation: no intent and no fallback")

	// ErrPersonaSwitchDenied indicates a persona transition is not permitted.
	ErrPersonaSwitchDenied = errors.New("conversation: persona switch denied")

	// ErrInvariant indicates a conversation invariant would be breached.
	// Always a programming error; never retried.
	ErrInvariant = errors.New("conversation: invariant violation")
)

// InvariantError names the invariant that was breached.
type InvariantError struct {
	// Invariant is the identifier, for example "INV-CV-2".
	Invariant string
	// Detail explains what was attempted.
	Detail string
}

// Error implements error.
func (e *InvariantError) Error() string {
	return fmt.Sprintf("conversation: invariant %s violated: %s", e.Invariant, e.Detail)
}

// Is allows errors.Is(err, ErrInvariant) to match.
func (e *InvariantError) Is(target error) bool { return target == ErrInvariant }

// invariant constructs an InvariantError. Unexported: only this package may
// assert an invariant of the conversation engine.
func invariant(id, format string, args ...any) error {
	return &InvariantError{Invariant: id, Detail: fmt.Sprintf(format, args...)}
}

// ConfigError reports one or more configuration problems, aggregated rather
// than reported one per restart.
type ConfigError struct {
	Problems []string
}

// Error implements error.
func (e *ConfigError) Error() string {
	if len(e.Problems) == 1 {
		return "conversation: configuration invalid: " + e.Problems[0]
	}
	s := fmt.Sprintf("conversation: configuration invalid (%d problems):", len(e.Problems))
	for _, p := range e.Problems {
		s += "\n  - " + p
	}
	return s
}
