package toolruntime

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Identifier types.
//
// Each is a distinct named type rather than a string, because every one of them
// is a string and mixing them up compiles fine. A ToolID passed where a
// CapabilityID belongs would resolve to nothing and look like a registry
// problem; the compiler catching it costs nothing.
type (
	// ToolID uniquely names a registered tool implementation.
	ToolID string

	// CapabilityID names something a tool can DO. Intents reference these,
	// never ToolIDs — see the package documentation for why that is the
	// load-bearing decision of this module.
	CapabilityID string

	// IntentID identifies one ToolIntent from the conversation engine.
	IntentID string

	// PlanID identifies one execution plan.
	PlanID string

	// StepID identifies one step within a plan. Stable across replans of the
	// same intent, so a trace can be compared with a previous run.
	StepID string

	// ExecutionID identifies one attempt to run one step.
	ExecutionID string

	// CorrelationID ties every execution arising from one conversation turn
	// together, across processes.
	CorrelationID string

	// SessionID identifies the conversation session. Carried for audit and
	// never used for routing.
	SessionID string

	// ActorID identifies who or what is asking. A subscriber, an operator, a
	// business, or the runtime itself.
	ActorID string

	// IdempotencyKey deduplicates executions. Derived, never supplied by a
	// caller — see idempotency.go.
	IdempotencyKey string

	// Fingerprint is a content hash used in audit records and events in place
	// of the content itself.
	Fingerprint string
)

// String renders each identifier.
func (t ToolID) String() string        { return string(t) }
func (c CapabilityID) String() string  { return string(c) }
func (i IntentID) String() string      { return string(i) }
func (p PlanID) String() string        { return string(p) }
func (s StepID) String() string        { return string(s) }
func (e ExecutionID) String() string   { return string(e) }
func (c CorrelationID) String() string { return string(c) }
func (a ActorID) String() string       { return string(a) }
func (f Fingerprint) String() string   { return string(f) }

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

var idCounter atomic.Uint64

// newID produces a short, sortable, collision-resistant identifier.
//
// Time prefix so identifiers sort roughly by creation, a process-local counter
// so two calls in the same nanosecond differ, and randomness so two processes
// do not collide. Deliberately not a UUID: this is the same construction Phase
// 10A uses, and having two identifier schemes in one platform is how log
// correlation quietly stops working.
func newID(prefix string) string {
	var buf [16]byte
	binary.BigEndian.PutUint64(buf[0:8], uint64(time.Now().UnixNano()))
	binary.BigEndian.PutUint32(buf[8:12], uint32(idCounter.Add(1)))
	if _, err := rand.Read(buf[12:16]); err != nil {
		// crypto/rand failing is not a condition a request path can handle, and
		// the counter alone still yields process-unique identifiers.
		binary.BigEndian.PutUint32(buf[12:16], uint32(idCounter.Load()))
	}
	return prefix + "_" + idEncoding.EncodeToString(buf[:])
}

// NewIntentID mints an intent identifier.
func NewIntentID() IntentID { return IntentID(newID("int")) }

// NewPlanID mints a plan identifier.
func NewPlanID() PlanID { return PlanID(newID("pln")) }

// NewExecutionID mints an execution identifier.
func NewExecutionID() ExecutionID { return ExecutionID(newID("exe")) }

// NewCorrelationID mints a correlation identifier.
func NewCorrelationID() CorrelationID { return CorrelationID(newID("cor")) }

// Version is a semantic version of a tool contract: "MAJOR.MINOR.PATCH".
//
// Stored as a string rather than a struct because it is compared far more often
// than it is decomposed, and because a struct would tempt somebody to add a
// pre-release field and a build-metadata field and then a parser for both.
type Version string

// ParseVersion splits a version, reporting whether it is well-formed.
func ParseVersion(v Version) (major, minor, patch int, ok bool) {
	parts := strings.Split(string(v), ".")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	out := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, 0, 0, false
		}
		out[i] = n
	}
	return out[0], out[1], out[2], true
}

// Valid reports whether the version is well-formed.
func (v Version) Valid() bool {
	_, _, _, ok := ParseVersion(v)
	return ok
}

// Major returns the major component, or -1 when malformed.
func (v Version) Major() int {
	m, _, _, ok := ParseVersion(v)
	if !ok {
		return -1
	}
	return m
}

// Compare orders two versions: -1, 0 or 1.
//
// A malformed version sorts BELOW every well-formed one rather than causing an
// error. Sorting is used to pick the best candidate, and a registry that
// refuses to sort because one entry is malformed cannot serve the other nine.
func (v Version) Compare(other Version) int {
	aMaj, aMin, aPat, aOK := ParseVersion(v)
	bMaj, bMin, bPat, bOK := ParseVersion(other)
	switch {
	case !aOK && !bOK:
		return strings.Compare(string(v), string(other))
	case !aOK:
		return -1
	case !bOK:
		return 1
	}
	for _, pair := range [3][2]int{{aMaj, bMaj}, {aMin, bMin}, {aPat, bPat}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// String renders the version.
func (v Version) String() string { return string(v) }

// VersionConstraint selects among registered versions.
//
// Three forms only: any, an exact version, and "compatible with major N". A
// full range grammar was considered and refused — every operator would need a
// parser, a test suite and a documented precedence table, to express selections
// nobody in this platform has yet needed. The two-line escape hatch is to
// register the tool under a different capability.
type VersionConstraint struct {
	// Exact pins one version. Wins over Major when both are set.
	Exact Version
	// Major requires a major version. -1 means unconstrained.
	Major int
}

// AnyVersion is the unconstrained constraint.
func AnyVersion() VersionConstraint { return VersionConstraint{Major: -1} }

// ExactVersion pins one version.
func ExactVersion(v Version) VersionConstraint {
	return VersionConstraint{Exact: v, Major: -1}
}

// MajorVersion requires a major version.
func MajorVersion(m int) VersionConstraint { return VersionConstraint{Major: m} }

// Satisfies reports whether a version meets the constraint.
func (c VersionConstraint) Satisfies(v Version) bool {
	if c.Exact != "" {
		return c.Exact == v
	}
	if c.Major >= 0 {
		return v.Major() == c.Major
	}
	return true
}

// String renders the constraint for logs and audit records.
func (c VersionConstraint) String() string {
	switch {
	case c.Exact != "":
		return "=" + string(c.Exact)
	case c.Major >= 0:
		return "^" + strconv.Itoa(c.Major)
	default:
		return "*"
	}
}

// fingerprintOf hashes canonical bytes into a short, stable fingerprint.
//
// SHA-256 truncated to 16 hex characters (64 bits). Long enough that a
// collision within one audit corpus is not a practical concern, short enough to
// read in a log line and compare by eye during an incident — which is what an
// operator actually does with these.
//
// It is a fingerprint, NOT a commitment: it identifies content without
// revealing it, and it is not a defence against an adversary who chooses the
// content. Nothing in this runtime treats a matching fingerprint as proof of
// anything; see SECURITY_REVIEW.
func fingerprintOf(canonical []byte) Fingerprint {
	sum := sha256.Sum256(canonical)
	return Fingerprint(hex.EncodeToString(sum[:8]))
}

// FingerprintString fingerprints a string. Exported for adapters that need to
// produce comparable fingerprints outside this package.
func FingerprintString(s string) Fingerprint { return fingerprintOf([]byte(s)) }

// Descriptor is a tool at a pinned version, the pair that actually identifies
// what ran.
//
// A ToolID alone does not: "calendar" ran is not a fact anybody can act on when
// three versions are registered and one of them is broken.
type Descriptor struct {
	Tool    ToolID
	Version Version
}

// String renders "tool@version".
func (d Descriptor) String() string {
	return string(d.Tool) + "@" + string(d.Version)
}

// Valid reports whether both halves are present and well-formed.
func (d Descriptor) Valid() bool { return d.Tool != "" && d.Version.Valid() }

func (d Descriptor) validate() []string {
	var problems []string
	if d.Tool == "" {
		problems = append(problems, "descriptor: tool is required")
	}
	if !d.Version.Valid() {
		problems = append(problems, fmt.Sprintf(
			"descriptor: version %q is not MAJOR.MINOR.PATCH", d.Version))
	}
	return problems
}
