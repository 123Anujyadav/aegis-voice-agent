package telephony

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// idAlphabet is lowercase Crockford base32, without padding.
//
// Three properties, and the ordering of the alphabet delivers the third:
//
//  1. Lowercase, because these identifiers appear in log lines, metric labels
//     and Kafka keys where mixed case invites a comparison bug that only shows
//     up under a case-sensitive index.
//  2. Crockford's letter set — i, l, o and u omitted — so an identifier read
//     aloud to a support agent cannot be transcribed as a different one.
//  3. DIGITS FIRST, so byte order matches base32 value order and the timestamp
//     prefix actually sorts.
//
// Property 3 was absent from the first version, which used
// "abcdefghijklmnopqrstuvwxyz234567". That alphabet places digits after letters
// by INDEX but before them in ASCII, so two identifiers a millisecond apart
// could compare in either direction and the sortability claim below was simply
// false. Found by running the suite at -count=3 -shuffle=on; a single ordered
// pair passes by luck about half the time. See ENGINEERING_AUDIT F3.
//
// packages/go/runtime got this right with uppercase Crockford. This is the same
// alphabet, lowercased.
var idAlphabet = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").
	WithPadding(base32.NoPadding)

// newID mints a prefixed, sortable, unguessable identifier.
//
// Layout: 6 bytes of millisecond timestamp, then 10 bytes from crypto/rand.
//
// The timestamp prefix makes identifiers roughly sortable by creation, which is
// what makes them usable as a database primary key without a secondary index on
// created_at — a UUIDv4 primary key on a B-tree index scatters writes across
// every page.
//
// The random suffix is from crypto/rand, not math/rand. A call identifier
// appears in URLs, webhooks and support tickets; a guessable one lets somebody
// enumerate calls that are not theirs. The cost of crypto/rand here is a few
// hundred nanoseconds on a path that runs once per call.
func newID(prefix string) string {
	var buf [16]byte

	ms := uint64(time.Now().UnixMilli())
	for i := 5; i >= 0; i-- {
		buf[i] = byte(ms)
		ms >>= 8
	}

	// crypto/rand.Read is documented never to return a short read, and a
	// failure is not a condition a call-setup path can meaningfully handle:
	// falling back to math/rand would silently make identifiers guessable,
	// which is worse than failing loudly.
	if _, err := rand.Read(buf[6:]); err != nil {
		panic("telephony: crypto/rand failed: " + err.Error())
	}

	return prefix + "_" + idAlphabet.EncodeToString(buf[:])
}

// CallID identifies one call for its entire life.
//
// Minted when the call enters the runtime and stable across recovery: a session
// restored after a crash keeps its identifier, which is what lets a carrier
// callback and a support ticket refer to the same thing.
type CallID string

// NewCallID mints a call identifier.
func NewCallID() CallID { return CallID(newID("call")) }

// String implements fmt.Stringer.
func (c CallID) String() string { return string(c) }

// Valid reports whether the identifier is well formed.
func (c CallID) Valid() bool { return strings.HasPrefix(string(c), "call_") && len(c) > 5 }

// SessionID identifies one runtime session for a call.
//
// DISTINCT FROM [CallID], and the distinction is load-bearing. A call has one
// CallID for its whole life. It may have several sessions: a resumed call after
// a runtime restart is a new session against the same call. Collapsing the two
// would make "how many times did we recover this call" unanswerable, which is
// exactly the question a recovery incident asks.
type SessionID string

// NewSessionID mints a session identifier.
func NewSessionID() SessionID { return SessionID(newID("sess")) }

// String implements fmt.Stringer.
func (s SessionID) String() string { return string(s) }

// CorrelationID ties every operation arising from one call together, across
// subsystem boundaries.
//
// DUPLICATION, KNOWINGLY INCURRED. Phase 10.5's observability audit recorded
// (finding O2) that CorrelationID is already declared independently in
// packages/go/toolruntime and packages/go/governance, that neither is defined
// in packages/go/runtime, and that the two are unrelated Go types requiring a
// string conversion to bridge. This is the third declaration.
//
// It is not avoidable here. Telephony sits ABOVE conversation, governance and
// tool runtime in the architecture, so importing either of the existing
// definitions would invert the dependency direction and couple the call
// lifecycle to the tool executor. The correct home is packages/go/runtime,
// which every module already imports — and that module is frozen.
//
// Recorded rather than repeated silently. See ENGINEERING_AUDIT §A1 and the
// Phase 10.5 recommendation, which this phase strengthens rather than
// discovers.
//
// RESOLVED in Phase 12 (ADR-0014). The recommendation above was acted on:
// [runtime.CorrelationID] now exists and this is an ALIAS of it, not a separate
// type. Every signature and field below keeps working unchanged, and a value
// from any subsystem is now assignable here without a string conversion —
// which is what makes an end-to-end trace assemblable. The commentary above is
// preserved because it is the reason the change was made.
type CorrelationID = runtime.CorrelationID

// NewCorrelationID mints a correlation identifier.
//
// Delegates so that one function mints every correlation identity on the
// platform. Two minting functions is two conventions, which is the problem this
// alias exists to end.
func NewCorrelationID() CorrelationID { return runtime.NewCorrelationID() }

// String is provided by [runtime.CorrelationID]; a method cannot be
// declared on an alias to another package's type, and re-declaring one
// here would be a second implementation of the same thing.

// LegID identifies one leg of a call.
//
// A transfer produces a second leg against the same [CallID]. Modelled now
// rather than retrofitted, because adding a leg concept to a live call model
// means migrating every stored session.
type LegID string

// NewLegID mints a leg identifier.
func NewLegID() LegID { return LegID(newID("leg")) }

// String implements fmt.Stringer.
func (l LegID) String() string { return string(l) }

// ProviderID identifies a registered provider, for example "carrier-primary".
//
// Authored, not generated. It appears in configuration and in metric labels, so
// it must be stable across restarts and readable in a dashboard — a generated
// identifier would produce a new time series on every deploy.
type ProviderID string

// String implements fmt.Stringer.
func (p ProviderID) String() string { return string(p) }

// Valid reports whether the identifier is usable as a metric label.
//
// Lowercase alphanumerics, hyphen and underscore only. Enforced because a
// provider identifier reaches a Prometheus label and a Kafka topic segment, and
// a character that is legal in one and not the other produces a failure at the
// far end of the pipeline from the configuration that caused it.
func (p ProviderID) Valid() bool {
	if p == "" || len(p) > 64 {
		return false
	}
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
