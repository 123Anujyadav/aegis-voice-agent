package audiointel

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Sentinel errors. Callers match with errors.Is; none carries audio.
var (
	// ErrInvalidTransition is returned when a detector move is not declared.
	ErrInvalidTransition = errors.New("audiointel: invalid state transition")

	// ErrSessionNotFound is returned for an unknown session identifier.
	ErrSessionNotFound = errors.New("audiointel: session not found")

	// ErrSessionExists is returned when an identifier is already registered.
	ErrSessionExists = errors.New("audiointel: session already registered")

	// ErrSessionClosed is returned by an operation on a closed session.
	ErrSessionClosed = errors.New("audiointel: session is closed")

	// ErrRuntimeStopped is returned by a runtime that is stopped.
	ErrRuntimeStopped = errors.New("audiointel: runtime is stopped")

	// ErrAtCapacity is returned when the runtime will admit no more sessions.
	ErrAtCapacity = errors.New("audiointel: runtime is at capacity")

	// ErrUnsupportedFormat is returned for audio this engine cannot analyse.
	//
	// Stereo and non-PCM codecs. See [validateAnalysisFormat] for why each is
	// refused rather than guessed at.
	ErrUnsupportedFormat = errors.New("audiointel: audio format cannot be analysed")

	// ErrFormatMismatch is returned when a frame's format differs from the
	// session's.
	ErrFormatMismatch = errors.New("audiointel: frame format does not match the session")

	// ErrInvalidFrame is returned for a frame that cannot be measured.
	ErrInvalidFrame = errors.New("audiointel: frame is not measurable")

	// ErrNoSpeechController is returned when a barge-in fires with no port
	// wired.
	//
	// A DETECTED BARGE-IN WITH NOWHERE TO SEND IT IS A CONFIGURATION FAULT, not
	// a silent no-op. A deployment that detects interruptions and cannot act on
	// them is worse than one that does not detect them, because it looks
	// healthy on a dashboard while talking over every caller.
	ErrNoSpeechController = errors.New("audiointel: no speech controller is wired")
)

// ConfigError reports one or more configuration problems.
//
// EVERY problem, not the first. A configuration with four mistakes should
// require one fix-and-restart cycle, not four. The same shape Phases 11A, 11B
// and 11C use, so a service validating several subsystems at boot handles one
// error type.
type ConfigError struct{ Problems []string }

func (e *ConfigError) Error() string {
	if len(e.Problems) == 1 {
		return "audiointel: " + e.Problems[0]
	}
	sorted := append([]string(nil), e.Problems...)
	sort.Strings(sorted)
	return fmt.Sprintf("audiointel: %d configuration problems:\n  - %s",
		len(sorted), strings.Join(sorted, "\n  - "))
}

// invariant builds an error naming a violated invariant.
//
// The identifier is stable and greppable: INV-AI-n. An invariant failure is a
// bug in this package, not a caller mistake, and the identifier is what turns a
// production log line into a specific line of code.
func invariant(id, format string, args ...any) error {
	return fmt.Errorf("audiointel: %s violated: %s", id, fmt.Sprintf(format, args...))
}

// TransitionError describes a refused detector state change.
//
// Carries states and identifiers, never audio.
type TransitionError struct {
	Session  SessionID
	Detector string
	From     string
	To       string
	Reason   string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("audiointel: session %s %s cannot move %s -> %s (%s)",
		e.Session, e.Detector, e.From, e.To, e.Reason)
}

// Unwrap lets errors.Is match ErrInvalidTransition.
func (e *TransitionError) Unwrap() error { return ErrInvalidTransition }
