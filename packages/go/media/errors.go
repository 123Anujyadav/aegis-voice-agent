package media

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Sentinel errors. Callers match with errors.Is; none carries audio.
var (
	// ErrInvalidTransition is returned when a stream move is not declared.
	ErrInvalidTransition = errors.New("media: invalid state transition")

	// ErrStreamNotFound is returned for an unknown stream identifier.
	ErrStreamNotFound = errors.New("media: stream not found")

	// ErrStreamExists is returned when an identifier is already registered.
	ErrStreamExists = errors.New("media: stream already registered")

	// ErrBufferFull is returned when a write cannot be accepted.
	ErrBufferFull = errors.New("media: buffer full")

	// ErrBufferEmpty is returned when a read finds nothing.
	//
	// UNDERFLOW IS NOT A FAILURE. A consumer reading faster than a producer
	// writes is the normal state of a healthy real-time stream — the buffer is
	// supposed to be nearly empty. This error says "nothing right now", and a
	// caller treating it as an outage will be wrong constantly.
	ErrBufferEmpty = errors.New("media: buffer empty")

	// ErrStreamClosed is returned by an operation on a closed stream.
	ErrStreamClosed = errors.New("media: stream is closed")

	// ErrStreamPaused is returned by a write to a paused stream.
	ErrStreamPaused = errors.New("media: stream is paused")

	// ErrFormatMismatch is returned when a frame's format differs from the
	// stream's.
	ErrFormatMismatch = errors.New("media: frame format does not match the stream")

	// ErrFrameTooLarge is returned when a frame exceeds the buffer's capacity
	// for a single frame.
	ErrFrameTooLarge = errors.New("media: frame exceeds the maximum frame size")

	// ErrRuntimeStopped is returned by a runtime that is draining or stopped.
	ErrRuntimeStopped = errors.New("media: runtime is stopped")

	// ErrNotRecoverable is returned when a stream snapshot cannot be resumed.
	ErrNotRecoverable = errors.New("media: stream is not recoverable")

	// ErrSnapshotNotFound is returned by a StreamStore for an unknown stream.
	ErrSnapshotNotFound = errors.New("media: snapshot not found")
)

// ConfigError reports one or more configuration problems.
//
// EVERY problem, not the first. A configuration with four mistakes should
// require one fix-and-restart cycle, not four.
type ConfigError struct{ Problems []string }

func (e *ConfigError) Error() string {
	if len(e.Problems) == 1 {
		return "media: " + e.Problems[0]
	}
	sorted := append([]string(nil), e.Problems...)
	sort.Strings(sorted)
	return fmt.Sprintf("media: %d configuration problems:\n  - %s",
		len(sorted), strings.Join(sorted, "\n  - "))
}

// invariant builds an error naming a violated invariant.
func invariant(id, format string, args ...any) error {
	return fmt.Errorf("media: %s violated: %s", id, fmt.Sprintf(format, args...))
}

// TransitionError describes a refused stream state change.
//
// Carries states and identifiers, never audio.
type TransitionError struct {
	Stream StreamID
	From   StreamState
	To     StreamState
	Reason string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("media: stream %s cannot move %s -> %s (%s)",
		e.Stream, e.From, e.To, e.Reason)
}

// Unwrap lets errors.Is match ErrInvalidTransition.
func (e *TransitionError) Unwrap() error { return ErrInvalidTransition }

// ---------------------------------------------------------------------------
// Identifiers
// ---------------------------------------------------------------------------

// idAlphabet is lowercase Crockford base32, without padding.
//
// Digits first, so byte order matches base32 value order and the timestamp
// prefix actually sorts. Phase 11A shipped an alphabet that put digits after
// letters by index but before them in ASCII, which silently broke sortability
// and took -count=3 -shuffle=on to find. This is the corrected form, and
// TestIDs_AlphabetIsAsciiSortable checks the property directly rather than
// sampling identifiers.
var idAlphabet = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").
	WithPadding(base32.NoPadding)

// newID mints a prefixed, sortable, unguessable identifier.
//
// Six bytes of millisecond timestamp then ten of crypto/rand. The timestamp
// prefix makes identifiers roughly ordered by creation; the random suffix makes
// them unguessable, which matters because a stream identifier reaches logs and
// operator tooling.
func newID(prefix string) string {
	var buf [16]byte

	ms := uint64(time.Now().UnixMilli())
	for i := 5; i >= 0; i-- {
		buf[i] = byte(ms)
		ms >>= 8
	}

	// A crypto/rand failure is not something a stream-setup path can handle,
	// and falling back to math/rand would silently make identifiers guessable.
	if _, err := rand.Read(buf[6:]); err != nil {
		panic("media: crypto/rand failed: " + err.Error())
	}

	return prefix + "_" + idAlphabet.EncodeToString(buf[:])
}

// StreamID identifies one media stream for its whole life.
type StreamID string

// NewStreamID mints a stream identifier.
func NewStreamID() StreamID { return StreamID(newID("strm")) }

// String implements fmt.Stringer.
func (s StreamID) String() string { return string(s) }

// Valid reports whether the identifier is well formed.
func (s StreamID) Valid() bool { return strings.HasPrefix(string(s), "strm_") && len(s) > 5 }

// SessionID identifies one runtime session for a stream.
//
// Distinct from [StreamID] for the same reason Phase 11A separates call from
// session: a stream resumed after a restart is a new session against the same
// stream, and collapsing the two makes "how many times did we recover this"
// unanswerable.
type SessionID string

// NewSessionID mints a session identifier.
func NewSessionID() SessionID { return SessionID(newID("msess")) }

// String implements fmt.Stringer.
func (s SessionID) String() string { return string(s) }

// CorrelationID ties a media stream to the call and conversation it belongs to.
//
// The FOURTH declaration of this type in the platform. Phase 10.5's
// observability audit recorded (O2) that toolruntime and governance each
// declare one, that neither is in packages/go/runtime, and that they are
// unrelated Go types; Phase 11A added a third and recorded the same finding.
//
// This module cannot import any of them: media sits beside telephony, not below
// it, and importing telephony to borrow an identifier type would couple a
// buffer to a call lifecycle. The correct home remains packages/go/runtime,
// which is frozen.
//
// Recorded rather than repeated silently, and the recommendation is now four
// phases old. See ENGINEERING_AUDIT §A1.
type CorrelationID string

// NewCorrelationID mints a correlation identifier.
func NewCorrelationID() CorrelationID { return CorrelationID(newID("corr")) }

// String implements fmt.Stringer.
func (c CorrelationID) String() string { return string(c) }

// SourceID identifies where a stream's frames come from.
//
// Authored, not generated: it appears in configuration and metric labels, so it
// must be stable across restarts and readable in a dashboard.
type SourceID string

// String implements fmt.Stringer.
func (s SourceID) String() string { return string(s) }

// Valid reports whether the identifier is usable as a metric label.
//
// Lowercase alphanumerics, hyphen and underscore. Enforced because this reaches
// a Prometheus label and a Kafka topic segment, and a character legal in one and
// not the other fails at the far end of the pipeline from the configuration
// that caused it.
func (s SourceID) Valid() bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// reasonCodeMax bounds a transition reason.
const reasonCodeMax = 64

// checkReasonCode refuses free text on a path that reaches an event stream.
//
// The control Phase 10E added and Phase 11A carried forward. Here the risk is
// different but real: the obvious thing for an adapter author to put in a
// failure reason is whatever the media source reported, and that can contain a
// path, a URL or a session token.
func checkReasonCode(reason string) error {
	if reason == "" {
		return invariant("INV-MED-3", "a transition reason is required")
	}
	if len(reason) > reasonCodeMax {
		return invariant("INV-MED-3",
			"reason code is %d characters, cap is %d — a reason is a code, not a message",
			len(reason), reasonCodeMax)
	}
	for _, r := range reason {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '.':
		default:
			return invariant("INV-MED-3",
				"reason code %q must be lowercase alphanumerics, underscore or dot; "+
					"free text on this path reaches a durable event stream", truncate(reason))
		}
	}
	return nil
}

func truncate(s string) string {
	const max = 32
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
