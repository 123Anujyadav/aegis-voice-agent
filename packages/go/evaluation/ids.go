package evaluation

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"sync/atomic"
	"time"
)

// Identifier types.
//
// Each is a distinct named type rather than a string. A ScenarioID passed where
// a GoldenID belongs compiles fine and compares against the wrong record, which
// in an evaluation platform means silently comparing a run to somebody else's
// baseline.
type (
	// RunID identifies one evaluation run.
	RunID string

	// SuiteID identifies a registered suite.
	SuiteID string

	// ScenarioID identifies a scenario across versions.
	ScenarioID string

	// GoldenID identifies one approved observation.
	GoldenID string

	// SubjectName names a subsystem under evaluation: "memory", "governance".
	// A string rather than an enum, because the platform must be able to
	// evaluate a subsystem this module has never heard of.
	SubjectName string

	// Fingerprint is a content hash used to compare observations without
	// carrying them.
	Fingerprint string
)

// String renders each identifier.
func (r RunID) String() string       { return string(r) }
func (s SuiteID) String() string     { return string(s) }
func (s ScenarioID) String() string  { return string(s) }
func (g GoldenID) String() string    { return string(g) }
func (s SubjectName) String() string { return string(s) }
func (f Fingerprint) String() string { return string(f) }

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

var idCounter atomic.Uint64

// newID produces a short, sortable, collision-resistant identifier.
//
// The same construction as every phase since 10A. Two identifier schemes in one
// platform is how log correlation quietly stops working halfway through an
// incident.
func newID(prefix string) string {
	var buf [16]byte
	binary.BigEndian.PutUint64(buf[0:8], uint64(time.Now().UnixNano()))
	binary.BigEndian.PutUint32(buf[8:12], uint32(idCounter.Add(1)))
	if _, err := rand.Read(buf[12:16]); err != nil {
		binary.BigEndian.PutUint32(buf[12:16], uint32(idCounter.Load()))
	}
	return prefix + "_" + idEncoding.EncodeToString(buf[:])
}

// NewRunID mints a run identifier.
func NewRunID() RunID { return RunID(newID("run")) }

// NewGoldenID mints a golden identifier.
func NewGoldenID() GoldenID { return GoldenID(newID("gld")) }

// fingerprintOf hashes canonical bytes.
//
// SHA-256 truncated to 16 hex characters. Long enough that a collision inside
// one evaluation corpus is not a practical concern, short enough to compare by
// eye in a report — which is what somebody reading a drift report actually does.
//
// It identifies content without carrying it, which is the property that lets a
// trend history retain years of behaviour fingerprints without retaining years
// of conversation transcripts.
func fingerprintOf(canonical []byte) Fingerprint {
	sum := sha256.Sum256(canonical)
	return Fingerprint(hex.EncodeToString(sum[:8]))
}

// FingerprintString fingerprints a string, for adapters that need comparable
// fingerprints outside this package.
func FingerprintString(s string) Fingerprint { return fingerprintOf([]byte(s)) }
