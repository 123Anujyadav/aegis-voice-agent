package runtime

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	goruntime "runtime"
	"sync/atomic"
	"time"
)

// This package is named `runtime`, which shadows the standard library package
// of the same name. Where the stdlib package is genuinely needed it is imported
// as goruntime. This is the only file that needs it.

// runtimeGosched yields the processor, allowing other goroutines to run.
//
// Used only by FakeClock.BlockUntil, which must spin rather than sleep — see
// the comment there for why.
func runtimeGosched() { goruntime.Gosched() }

// NumCPU reports the number of logical CPUs usable by the process. Exposed so
// Config can derive sensible worker-pool defaults without importing goruntime
// at every call site.
func NumCPU() int { return goruntime.NumCPU() }

// ---------------------------------------------------------------------------
// Identifiers
// ---------------------------------------------------------------------------

// The runtime's identifiers are opaque, non-sequential, and prefixed by type.
//
// Non-sequential because a sequential identifier leaks volume — the number of
// sessions we have run is commercially sensitive and is trivially inferred from
// a monotonic counter appearing in a log or a trace. Prefixed because a
// SessionID passed where a RequestID is expected should be visibly wrong in a
// log line, not silently accepted.
//
// This mirrors the reasoning already frozen for ResourceId in types.proto: the
// type discriminator makes a category error visible on the wire.

// idAlphabet is Crockford-style base32 without padding: unambiguous when read
// aloud to a support agent, and safe in a URL, a filename and a log line.
var idAlphabet = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// idCounter disambiguates identifiers generated within the same nanosecond on
// the same host. It is the one package-level mutable value in the runtime, and
// it is permitted because it is write-only, monotonic, carries no semantics,
// and is read by nothing. It is not state; it is entropy.
var idCounter atomic.Uint64

// newID generates a prefixed opaque identifier.
//
// Layout after the prefix: 6 bytes of millisecond timestamp, 2 bytes of
// counter, 8 bytes of cryptographic randomness. The timestamp prefix makes
// identifiers roughly sortable by creation, which makes a log or an index scan
// dramatically cheaper, without making them guessable — the 8 random bytes
// carry the unguessability.
func newID(prefix string) string {
	var buf [16]byte

	ms := uint64(time.Now().UnixMilli())
	buf[0] = byte(ms >> 40)
	buf[1] = byte(ms >> 32)
	buf[2] = byte(ms >> 24)
	buf[3] = byte(ms >> 16)
	buf[4] = byte(ms >> 8)
	buf[5] = byte(ms)

	c := idCounter.Add(1)
	buf[6] = byte(c >> 8)
	buf[7] = byte(c)

	// crypto/rand.Read is documented never to return a short read, and on
	// every supported platform an error here means the OS entropy source has
	// failed — a condition from which this process cannot safely continue,
	// since every identifier it subsequently mints would be predictable.
	if _, err := rand.Read(buf[8:]); err != nil {
		panic(fmt.Sprintf("runtime: entropy source unavailable: %v", err))
	}

	return prefix + "_" + idAlphabet.EncodeToString(buf[:])
}

// SessionID identifies a runtime session.
type SessionID string

// NewSessionID mints a session identifier.
func NewSessionID() SessionID { return SessionID(newID("ses")) }

// String implements fmt.Stringer.
func (s SessionID) String() string { return string(s) }

// RequestID identifies one generation request within a session.
type RequestID string

// NewRequestID mints a request identifier.
func NewRequestID() RequestID { return RequestID(newID("req")) }

// String implements fmt.Stringer.
func (r RequestID) String() string { return string(r) }

// ProviderID identifies a registered provider, for example "anthropic" or
// "python-host". Unlike SessionID and RequestID these are authored, not
// generated: they appear in configuration and in metric labels, so they must be
// stable and readable.
type ProviderID string

// String implements fmt.Stringer.
func (p ProviderID) String() string { return string(p) }

// ModelID identifies a registered model, for example "claude-haiku-4-5".
// Authored, stable, and used as a metric label.
type ModelID string

// String implements fmt.Stringer.
func (m ModelID) String() string { return string(m) }

// PromptID identifies a registered prompt template. Authored.
type PromptID string

// String implements fmt.Stringer.
func (p PromptID) String() string { return string(p) }

// StreamID identifies one token stream. Generated, because a session may open
// several over its life and correlating them in a trace requires distinguishing
// them.
type StreamID string

// NewStreamID mints a stream identifier.
func NewStreamID() StreamID { return StreamID(newID("str")) }

// String implements fmt.Stringer.
func (s StreamID) String() string { return string(s) }
