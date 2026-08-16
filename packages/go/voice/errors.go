package voice

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Sentinel errors. Callers match with errors.Is; none carries a transcript,
// audio, or a credential.
var (
	// ErrProviderUnavailable is returned when a local provider cannot run at
	// all — a missing executable, a missing model, a daemon that is not
	// listening.
	//
	// DISTINCT FROM A FAILURE, AND THE DISTINCTION IS OPERATIONAL. "Whisper is
	// not installed" is fixed by installing whisper; "whisper crashed on this
	// audio" is fixed by looking at the audio. Collapsing them into one error
	// sends an operator to the wrong place, and on a first run it is nearly
	// always the former.
	ErrProviderUnavailable = errors.New("voice: provider is unavailable")

	// ErrProviderFailed is returned when a provider that WAS available failed
	// during use.
	ErrProviderFailed = errors.New("voice: provider failed")

	// ErrProviderTimeout is returned when a provider exceeded its configured
	// budget.
	ErrProviderTimeout = errors.New("voice: provider timed out")

	// ErrProcessFailed is returned when an external process could not be
	// started, died unexpectedly, or would not stop.
	ErrProcessFailed = errors.New("voice: external process failed")

	// ErrSessionClosed is returned by an operation on a closed voice session.
	ErrSessionClosed = errors.New("voice: session is closed")

	// ErrSessionNotFound is returned for an unknown session identifier.
	ErrSessionNotFound = errors.New("voice: session not found")

	// ErrRuntimeStopped is returned by a runtime that is stopped.
	ErrRuntimeStopped = errors.New("voice: runtime is stopped")

	// ErrAtCapacity is returned when the runtime will admit no more sessions.
	ErrAtCapacity = errors.New("voice: runtime is at capacity")

	// ErrInvalidTransition is returned when a session move is not declared.
	ErrInvalidTransition = errors.New("voice: invalid state transition")

	// ErrGovernanceDenied is returned when the governance engine refused an
	// action.
	//
	// NOT A FAILURE. A refusal is the platform working: something asked to do
	// something it may not do, and was stopped. It is a distinct sentinel so a
	// caller cannot accidentally retry it as though it were a transient fault.
	ErrGovernanceDenied = errors.New("voice: governance denied the action")

	// ErrBackpressure is returned when a bounded queue is full.
	ErrBackpressure = errors.New("voice: backpressure")

	// ErrUnsafePath is returned when a configured executable or model path
	// cannot be used safely.
	ErrUnsafePath = errors.New("voice: unsafe path")
)

// ConfigError reports one or more configuration problems.
//
// EVERY problem, not the first. A configuration with four mistakes should
// require one fix-and-restart cycle, not four. The same shape every phase since
// 10A uses, so a service validating several subsystems at boot handles one
// error type.
type ConfigError struct{ Problems []string }

func (e *ConfigError) Error() string {
	if len(e.Problems) == 1 {
		return "voice: " + e.Problems[0]
	}
	sorted := append([]string(nil), e.Problems...)
	sort.Strings(sorted)
	return fmt.Sprintf("voice: %d configuration problems:\n  - %s",
		len(sorted), strings.Join(sorted, "\n  - "))
}

// UnavailableError explains why a provider cannot run, and what to do about it.
//
// # It carries the remedy, because the remedy is the whole point
//
// The first time anybody runs this phase's end-to-end path, the overwhelmingly
// likely outcome is that a local binary is not installed. An error saying
// "provider unavailable" sends that person to read source code. An error naming
// the executable it looked for, the path it looked at, and the command that
// installs it sends them to a package manager.
//
// Nothing here is derived from caller input, so nothing here can carry
// user content into a log.
type UnavailableError struct {
	// Provider is the authored provider identifier.
	Provider string

	// Component is what was missing: "executable", "model", "daemon".
	Component string

	// Path is where it was looked for. Empty for a daemon.
	Path string

	// Endpoint is what was probed. Empty for an executable.
	Endpoint string

	// Remedy is the operator-facing instruction. Authored, never templated from
	// input.
	Remedy string

	// Cause is the underlying error, if there was one.
	Cause error
}

func (e *UnavailableError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "voice: provider %s is unavailable: %s", e.Provider, e.Component)

	switch {
	case e.Path != "":
		fmt.Fprintf(&b, " not found at %q", e.Path)
	case e.Endpoint != "":
		fmt.Fprintf(&b, " not reachable at %s", e.Endpoint)
	}
	if e.Cause != nil {
		fmt.Fprintf(&b, " (%v)", e.Cause)
	}
	if e.Remedy != "" {
		fmt.Fprintf(&b, "\n  to fix: %s", e.Remedy)
	}
	return b.String()
}

// Unwrap lets errors.Is match ErrProviderUnavailable.
func (e *UnavailableError) Unwrap() error { return ErrProviderUnavailable }

// TransitionError describes a refused session state change.
type TransitionError struct {
	Session SessionID
	From    SessionState
	To      SessionState
	Reason  string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("voice: session %s cannot move %s -> %s (%s)",
		e.Session, e.From, e.To, e.Reason)
}

// Unwrap lets errors.Is match ErrInvalidTransition.
func (e *TransitionError) Unwrap() error { return ErrInvalidTransition }

// invariant builds an error naming a violated invariant.
//
// The identifier is stable and greppable: INV-VOICE-n. An invariant failure is
// a bug in this package, not a caller mistake, and the identifier is what turns
// a production log line into a specific line of code.
func invariant(id, format string, args ...any) error {
	return fmt.Errorf("voice: %s violated: %s", id, fmt.Sprintf(format, args...))
}
