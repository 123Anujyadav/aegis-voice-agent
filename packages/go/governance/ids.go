package governance

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"sync/atomic"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Identifier types.
//
// Each is a distinct named type rather than a string, because every one of them
// is a string and mixing them up compiles fine. A SubjectID passed where an
// ActorID belongs would silently evaluate the wrong person's policies, which is
// the single worst class of bug this engine could have.
type (
	// DecisionID identifies one decision. Minted per call, carried into the
	// audit record and the event, and quotable in an incident.
	DecisionID string

	// PolicyID identifies a policy across versions.
	PolicyID string

	// ConsentID identifies one consent record.
	ConsentID string

	// CorrelationID ties every decision arising from one conversation turn
	// together, across processes and subsystems.
	//
	// An ALIAS of [runtime.CorrelationID] since Phase 12 (ADR-0014), so a
	// decision's correlation is literally the same type as the call's and the
	// media stream's rather than a fourth unrelated string type. Every field
	// and signature using it is unchanged.
	CorrelationID = runtime.CorrelationID

	// SessionID identifies the conversation. Carried for audit; never used to
	// route or to select policies, because policy selection that depends on a
	// session identifier is policy selection nobody can reproduce.
	SessionID string

	// ActorID is who or what is asking to act.
	ActorID string

	// SubjectID is the person the action is about. Frequently different from
	// the actor: a receptionist acting about a caller.
	SubjectID string

	// OrgID identifies an organisation.
	OrgID string

	// BusinessID identifies a business within an organisation.
	BusinessID string

	// Fingerprint is a content hash used in decisions, events and audit
	// records in place of the content itself.
	Fingerprint string
)

// String renders each identifier.
func (d DecisionID) String() string  { return string(d) }
func (p PolicyID) String() string    { return string(p) }
func (c ConsentID) String() string   { return string(c) }
func (a ActorID) String() string     { return string(a) }
func (s SubjectID) String() string   { return string(s) }
func (f Fingerprint) String() string { return string(f) }

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

var idCounter atomic.Uint64

// newID produces a short, sortable, collision-resistant identifier.
//
// The same construction as Phases 10A and 10D. Deliberately not a UUID: having
// two identifier schemes in one platform is how log correlation quietly stops
// working halfway through an incident.
func newID(prefix string) string {
	var buf [16]byte
	binary.BigEndian.PutUint64(buf[0:8], uint64(time.Now().UnixNano()))
	binary.BigEndian.PutUint32(buf[8:12], uint32(idCounter.Add(1)))
	if _, err := rand.Read(buf[12:16]); err != nil {
		binary.BigEndian.PutUint32(buf[12:16], uint32(idCounter.Load()))
	}
	return prefix + "_" + idEncoding.EncodeToString(buf[:])
}

// NewDecisionID mints a decision identifier.
func NewDecisionID() DecisionID { return DecisionID(newID("dec")) }

// NewConsentID mints a consent identifier.
func NewConsentID() ConsentID { return ConsentID(newID("con")) }

// NewCorrelationID mints a correlation identifier.
func NewCorrelationID() CorrelationID { return runtime.NewCorrelationID() }

// fingerprintOf hashes canonical bytes into a short, stable fingerprint.
//
// SHA-256 truncated to 16 hex characters. Long enough that a collision within
// one audit corpus is not a practical concern, short enough to read in a log
// line and compare by eye during an incident — which is what an operator
// actually does with these.
//
// It is a fingerprint, NOT a commitment: it identifies content without
// revealing it, and it is not a defence against an adversary who chooses the
// content. Nothing here treats a matching fingerprint as proof of anything.
// See SECURITY_REVIEW.
func fingerprintOf(canonical []byte) Fingerprint {
	sum := sha256.Sum256(canonical)
	return Fingerprint(hex.EncodeToString(sum[:8]))
}

// FingerprintString fingerprints a string, for callers that need to produce
// comparable fingerprints outside this package.
func FingerprintString(s string) Fingerprint { return fingerprintOf([]byte(s)) }
