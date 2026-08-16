package audiointel

import (
	"crypto/rand"
	"encoding/base32"
	"time"
)

// SessionID identifies one audio intelligence session.
//
// Minted here. One session analyses one direction of one call's audio.
type SessionID string

// CallID identifies the call this session serves.
//
// OPAQUE, AND SUPPLIED BY THE CALLER. Phase 11A owns call identity; this
// package does not know what a call is and never mints one. It is carried on
// events so an operator can correlate an audio signal with a call record, and
// it is validated as a label so a malformed value cannot reach a metric.
type CallID string

// TurnID identifies the speech turn this signal belongs to.
//
// OPAQUE, AND SUPPLIED BY THE CALLER. Phase 11C owns turn identity. Carried
// where available — a signal detected between turns legitimately has none.
type TurnID string

// Language is the language tag Phase 11C associated with this audio.
//
// # Carried, never interpreted
//
// §22 of the phase brief requires that where language metadata is available
// from Phase 11C it is preserved. This engine has no use for it: every
// algorithm here counts milliseconds and measures decibels, and none of them
// behaves differently for Hindi than for English.
//
// It is carried so that an evaluation correlating audio signals with language
// can do so, and so that a deployment tuning EndpointPolicy for a
// syllable-timed language can tell which sessions its change affected. Nothing
// in the detection path reads it, and TestScenarios_LanguageMetadataIsCarriedNotInterpreted
// checks that two sessions differing only in this tag reach identical
// conclusions.
//
// Opaque and validated as a label, because it reaches an event and a metric
// would be one careless change away.
type Language string

// The language tags this platform uses, matching Phase 11C's vocabulary.
const (
	// LangUnspecified is the zero value: no language metadata was supplied.
	LangUnspecified Language = ""

	// LangEnglishIN is Indian English.
	LangEnglishIN Language = "en-in"

	// LangHindi is Hindi.
	LangHindi Language = "hi-in"

	// LangHinglish is code-mixed Hindi and English, which is what a great deal
	// of this platform's traffic actually is.
	LangHinglish Language = "hi-en"
)

// Valid reports whether the tag is well-formed. Empty is valid — a session may
// legitimately not know.
func (l Language) Valid() bool { return l == "" || validLabel(string(l)) }

// String implements fmt.Stringer.
func (l Language) String() string { return string(l) }

// maxIDLen bounds an identifier. Long enough for a prefixed sortable ID, short
// enough that a malformed value cannot bloat a label set.
const maxIDLen = 64

// idAlphabet is lowercase Crockford base32, without padding.
//
// Digits first, so byte order matches base32 value order and the timestamp
// prefix actually sorts. Phase 11B shipped an alphabet that put digits after
// letters by index but before them in ASCII, which silently broke sortability;
// this is the corrected form and TestIDs_AlphabetIsAsciiSortable checks the
// property directly rather than sampling identifiers.
var idAlphabet = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").
	WithPadding(base32.NoPadding)

// newID mints a prefixed, sortable, unguessable identifier.
//
// Six bytes of millisecond timestamp then ten of crypto/rand. The timestamp
// prefix makes identifiers roughly ordered by creation, which is what makes a
// log grep useful; the random suffix makes them unguessable, which matters
// because a session identifier reaches logs and operator tooling.
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
		panic("audiointel: crypto/rand failed: " + err.Error())
	}
	return prefix + "-" + idAlphabet.EncodeToString(buf[:])
}

// NewSessionID returns a fresh session identifier.
func NewSessionID() SessionID { return SessionID(newID("ai")) }

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
//
// An EMPTY call identifier is valid: a session may legitimately analyse audio
// that is not yet associated with a call record. A NON-EMPTY malformed one is
// not, because it would reach an event.
func (c CallID) Valid() bool { return c == "" || validLabel(string(c)) }

// Valid reports whether the identifier is well-formed. Empty is valid — a
// signal detected between turns has no turn.
func (t TurnID) Valid() bool { return t == "" || validLabel(string(t)) }

// String implements fmt.Stringer.
func (s SessionID) String() string { return string(s) }

// String implements fmt.Stringer.
func (c CallID) String() string { return string(c) }

// String implements fmt.Stringer.
func (t TurnID) String() string { return string(t) }

// maxReasonLen bounds a reason code.
const maxReasonLen = 48

// boundedReason clamps a reason to something safe as a metric label.
//
// Reasons reach labels and topics. An unbounded one taken from a caller would
// let that caller choose our metric cardinality; a long one would bloat every
// event on a high-volume topic.
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
