package speech

import (
	"crypto/rand"
	"encoding/hex"
)

// SessionID identifies one speech session.
type SessionID string

// TurnID identifies one speech turn within a session.
type TurnID string

// SegmentID identifies one transcript segment within a turn.
type SegmentID string

// ProviderID names a provider.
//
// AUTHORED, NEVER DERIVED FROM PROVIDER OUTPUT. It becomes a metric label and a
// topic segment, so a value taken from a vendor response would let that vendor
// choose our metric cardinality and our topic names.
type ProviderID string

// maxIDLen bounds an identifier. Long enough for a prefixed random ID, short
// enough that a malformed value cannot bloat a label set.
const maxIDLen = 64

// newID returns a prefixed random identifier.
//
// crypto/rand rather than math/rand: these identifiers appear in events that
// cross a service boundary, and a predictable session identifier lets an
// attacker who can reach the API guess one that is live.
func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any supported platform; if it does, the
		// process cannot safely mint identifiers and continuing is worse.
		panic("speech: crypto/rand failed: " + err.Error())
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}

// NewSessionID returns a fresh session identifier.
func NewSessionID() SessionID { return SessionID(newID("ss")) }

// NewTurnID returns a fresh turn identifier.
func NewTurnID() TurnID { return TurnID(newID("st")) }

// NewSegmentID returns a fresh segment identifier.
func NewSegmentID() SegmentID { return SegmentID(newID("sg")) }

// validLabel reports whether s is safe as a Prometheus label value and as a
// Kafka topic segment.
//
// Lowercase alphanumerics, hyphen and underscore. Hyphens are legal here but
// prohibited in topic SEGMENTS by packages/go/eventbus, which is why event
// topics are built from authored constants rather than from these identifiers.
func validLabel(s string) bool {
	if s == "" || len(s) > maxIDLen {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-', r == '_':
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

// Valid reports whether the identifier is well-formed.
func (s SegmentID) Valid() bool { return validLabel(string(s)) }

// Valid reports whether the identifier is well-formed.
func (p ProviderID) Valid() bool { return validLabel(string(p)) }

// String implements fmt.Stringer.
func (s SessionID) String() string { return string(s) }

// String implements fmt.Stringer.
func (t TurnID) String() string { return string(t) }

// String implements fmt.Stringer.
func (s SegmentID) String() string { return string(s) }

// String implements fmt.Stringer.
func (p ProviderID) String() string { return string(p) }
