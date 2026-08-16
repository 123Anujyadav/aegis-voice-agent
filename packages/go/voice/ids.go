package voice

import (
	"crypto/rand"
	"encoding/base32"
	"time"
)

// SessionID identifies one voice session — one caller, one conversation.
type SessionID string

// TurnID identifies one turn within a voice session.
type TurnID string

// CallID identifies the call this session serves.
//
// OPAQUE, AND SUPPLIED BY THE CALLER. Phase 11A owns call identity; this
// package does not mint one. Carried so an operator can correlate a voice
// signal with a call record, and validated as a label so a malformed value
// cannot reach a metric.
type CallID string

// ProviderID names a provider.
//
// AUTHORED, NEVER DERIVED FROM PROVIDER OUTPUT. It becomes a metric label, so a
// value taken from a process's stdout would let that process choose our metric
// cardinality.
type ProviderID string

// ModelID names a model within a provider.
//
// CONFIGURATION, NOT A CONSTANT. No model name is hardcoded anywhere in this
// package. ADR-0006 freezes the production model ladder and this phase does not
// touch it; a local development model is whatever the operator has pulled, and
// naming one here would read as an endorsement the architecture has not made.
type ModelID string

// maxIDLen bounds an identifier. Long enough for a prefixed sortable ID, short
// enough that a malformed value cannot bloat a label set.
const maxIDLen = 64

// idAlphabet is lowercase Crockford base32, without padding.
//
// Digits first, so byte order matches base32 value order and the timestamp
// prefix actually sorts.
var idAlphabet = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").
	WithPadding(base32.NoPadding)

// newID mints a prefixed, sortable, unguessable identifier.
//
// Six bytes of millisecond timestamp then ten of crypto/rand. The prefix makes
// identifiers roughly ordered by creation, which is what makes a log grep
// useful; the random suffix makes them unguessable, which matters because a
// session identifier reaches logs and operator tooling.
func newID(prefix string) string {
	var buf [16]byte

	ms := uint64(time.Now().UnixMilli())
	for i := 5; i >= 0; i-- {
		buf[i] = byte(ms)
		ms >>= 8
	}

	// A crypto/rand failure is not something a session-setup path can handle,
	// and falling back to math/rand would silently make identifiers guessable.
	if _, err := rand.Read(buf[6:]); err != nil {
		panic("voice: crypto/rand failed: " + err.Error())
	}
	return prefix + "-" + idAlphabet.EncodeToString(buf[:])
}

// NewSessionID returns a fresh voice session identifier.
func NewSessionID() SessionID { return SessionID(newID("vs")) }

// NewTurnID returns a fresh voice turn identifier.
func NewTurnID() TurnID { return TurnID(newID("vt")) }

// validLabel reports whether s is safe as a Prometheus label value and as a
// Kafka topic segment.
//
// Lowercase alphanumerics, hyphen, underscore, dot and colon. Dot and colon are
// permitted here and nowhere else in the platform because a model identifier
// legitimately contains both — "llama3.2:3b" is one token to the daemon that
// serves it, and mangling it would make the configuration not work.
//
// They are safe in a Prometheus label VALUE. They are not safe in a metric NAME
// or a topic segment, and no identifier from this file is ever used as either.
func validLabel(s string) bool {
	if s == "" || len(s) > maxIDLen {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == ':':
		default:
			return false
		}
	}
	return true
}

// Valid reports whether the identifier is well-formed.
func (s SessionID) Valid() bool { return validLabel(string(s)) }

// Valid reports whether the identifier is well-formed.
func (t TurnID) Valid() bool { return validLabel(string(t)) }

// Valid reports whether the identifier is well-formed. Empty is valid — a
// session may analyse audio before a call record exists.
func (c CallID) Valid() bool { return c == "" || validLabel(string(c)) }

// Valid reports whether the identifier is well-formed.
func (p ProviderID) Valid() bool { return validLabel(string(p)) }

// Valid reports whether the identifier is well-formed. Empty is valid — a
// provider that serves exactly one model needs no identifier for it.
func (m ModelID) Valid() bool { return m == "" || validLabel(string(m)) }

// String implements fmt.Stringer.
func (s SessionID) String() string { return string(s) }

// String implements fmt.Stringer.
func (t TurnID) String() string { return string(t) }

// String implements fmt.Stringer.
func (c CallID) String() string { return string(c) }

// String implements fmt.Stringer.
func (p ProviderID) String() string { return string(p) }

// String implements fmt.Stringer.
func (m ModelID) String() string { return string(m) }

// maxReasonLen bounds a reason code.
const maxReasonLen = 48

// boundedReason clamps a reason to something safe as a metric label.
//
// Reasons reach labels. An unbounded one taken from a provider's stderr would
// let that provider choose our metric cardinality — and stderr is exactly where
// a caller's words could end up if an adapter were careless.
func boundedReason(r string) string {
	if r == "" {
		return "unspecified"
	}
	out := make([]rune, 0, maxReasonLen)
	for _, c := range r {
		if len(out) == maxReasonLen {
			break
		}
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
		case c == ' ', c == '-':
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "unspecified"
	}
	return string(out)
}
