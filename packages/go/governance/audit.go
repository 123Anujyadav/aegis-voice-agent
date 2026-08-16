package governance

import (
	"sort"
	"sync"
	"time"
)

// AuditKind classifies a governance audit entry.
//
// A small closed enum, because these become metric labels and audit-store
// partition keys, and both punish free text.
type AuditKind string

// The audit kinds.
const (
	AuditDecision           AuditKind = "decision"
	AuditDenied             AuditKind = "denied"
	AuditEscalated          AuditKind = "escalated"
	AuditEscalationResolved AuditKind = "escalation_resolved"
	AuditConsentGranted     AuditKind = "consent_granted"
	AuditConsentRevoked     AuditKind = "consent_revoked"
	AuditConsentExpired     AuditKind = "consent_expired"
	AuditConsentChecked     AuditKind = "consent_checked"
	AuditEmergencyActivated AuditKind = "emergency_activated"
	AuditEmergencyExpired   AuditKind = "emergency_expired"
	AuditEmergencyUsed      AuditKind = "emergency_used"
	AuditPolicyRegistered   AuditKind = "policy_registered"
	AuditPolicyUnregistered AuditKind = "policy_unregistered"
	AuditPolicyToggled      AuditKind = "policy_toggled"
	AuditHumanOverride      AuditKind = "human_override"
)

// AuditEntry is one record in the governance audit trail.
//
// IT CARRIES FINGERPRINTS AND CODES, NOT CONTENT.
//
// A governance audit trail is the most durable, most replicated and most widely
// read artefact this platform produces: it is written to permanent storage,
// retained for years, and read by engineers, auditors and regulators. Putting a
// caller's words or a subject's identifiers in it creates a second copy of
// personal data with a different retention schedule and a different access model
// from the system of record — which is the exact shape of finding that shows up
// in an audit as "unmanaged personal data store".
//
// The fingerprints answer what an audit actually asks: was this the same
// question as last time, did the same question get a different answer, which
// policy version was in force.
type AuditEntry struct {
	// At is the entry instant on the engine's clock.
	At time.Time
	// Kind classifies it.
	Kind AuditKind
	// Decision, Correlation and Session locate it.
	Decision    DecisionID
	Correlation CorrelationID
	Session     SessionID
	// Actor and Subject are who asked and about whom.
	Actor   ActorID
	Subject SubjectID
	// Policy names the deciding or affected policy.
	Policy PolicyID
	// Scope is the deciding scope.
	Scope Scope
	// Outcome is what was decided.
	Outcome Outcome
	// Reason is a short machine-readable code.
	Reason string
	// ActionLabel is the bounded metric form of the action — kind and
	// operation only, never the resource.
	ActionLabel string
	// RequestPrint fingerprints everything that determined the outcome, so two
	// entries can be compared without either holding the inputs.
	RequestPrint Fingerprint
	// PolicyVersion is the snapshot the decision was made against. THE REPLAY
	// ANCHOR.
	PolicyVersion uint64
	// Risk is the aggregate the decision saw.
	Risk RiskLevel
	// Duration is how long evaluation took.
	Duration time.Duration
	// Details carries structured extras. Values must never be caller content;
	// nothing in this package puts caller content here.
	Details map[string]string
}

// Auditor receives governance audit entries.
//
// Synchronous by contract and expected to be fast. A governance engine that
// cannot write an audit entry has a decision to make, and this module makes it
// explicitly: see [Config.RequireAuditor].
type Auditor interface {
	// Record writes one entry. An error is counted; it does not fail the
	// decision.
	//
	// That asymmetry is deliberate and is the opposite of the tool runtime's
	// reasoning, so it is worth being explicit. A tool execution that cannot be
	// audited has already changed the world. A GOVERNANCE DECISION has not — but
	// failing it would deny an action the policies permitted, converting an
	// audit-store outage into a platform outage. The engine therefore proceeds
	// and counts, and [Config.RequireAuditor] ensures somebody is listening at
	// boot.
	Record(AuditEntry) error
}

// NoopAuditor discards entries.
//
// Permitted only when Config.RequireAuditor is false. An engine that decides
// whether the platform may act, with no record of what it decided, cannot
// answer "why did it do that" — which is the one question governance exists to
// answer.
type NoopAuditor struct{}

// Record discards the entry.
func (NoopAuditor) Record(AuditEntry) error { return nil }

// RecordingAuditor captures entries for assertion.
//
// Exported because every service embedding this engine needs to test its own
// reaction to governance decisions, and an auditor only this package can build
// pushes each of them into writing their own.
type RecordingAuditor struct {
	mu      sync.Mutex
	entries []AuditEntry
	err     error
	max     int
}

// NewRecordingAuditor builds a recording auditor holding up to max entries.
func NewRecordingAuditor(max int) *RecordingAuditor {
	if max <= 0 {
		max = 8192
	}
	return &RecordingAuditor{max: max}
}

// Record captures an entry.
func (a *RecordingAuditor) Record(e AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	if len(a.entries) < a.max {
		a.entries = append(a.entries, e)
	}
	return nil
}

// FailWith makes every subsequent Record fail, for failure injection.
func (a *RecordingAuditor) FailWith(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.err = err
}

// Entries returns a copy, in the order recorded.
func (a *RecordingAuditor) Entries() []AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]AuditEntry(nil), a.entries...)
}

// OfKind returns entries of one kind.
func (a *RecordingAuditor) OfKind(k AuditKind) []AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []AuditEntry
	for _, e := range a.entries {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

// Len returns the entry count.
func (a *RecordingAuditor) Len() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.entries)
}

// Reset clears the recorded entries.
func (a *RecordingAuditor) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = nil
}

// GovernanceTrace is the ordered record of every governance entry for one
// correlation.
//
// Assembled on demand rather than maintained, because a maintained version
// would need eviction, and a trace that evicts is a trace missing exactly the
// entry somebody is looking for.
type GovernanceTrace struct {
	// Correlation identifies the turn.
	Correlation CorrelationID
	// Entries are ordered by time, then decision, then kind — a total order,
	// so two assemblies of the same trace are identical.
	Entries []AuditEntry
}

// TraceFor assembles the trace for one correlation.
func TraceFor(a *RecordingAuditor, c CorrelationID) GovernanceTrace {
	a.mu.Lock()
	var out []AuditEntry
	for _, e := range a.entries {
		if e.Correlation == c {
			out = append(out, e)
		}
	}
	a.mu.Unlock()

	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		if out[i].Decision != out[j].Decision {
			return out[i].Decision < out[j].Decision
		}
		return out[i].Kind < out[j].Kind
	})
	return GovernanceTrace{Correlation: c, Entries: out}
}

// Summary renders the trace as one line per entry, for diagnostics.
func (t GovernanceTrace) Summary() []string {
	out := make([]string, 0, len(t.Entries))
	for _, e := range t.Entries {
		line := string(e.Kind)
		if e.ActionLabel != "" {
			line += " " + e.ActionLabel
		}
		if e.Outcome != OutcomeDeny || e.Kind == AuditDenied {
			line += " → " + e.Outcome.String()
		}
		if e.Policy != "" {
			line += " by " + string(e.Policy)
		}
		if e.Reason != "" {
			line += " (" + e.Reason + ")"
		}
		out = append(out, line)
	}
	return out
}

// ReplayMetadata is everything needed to recompute a decision.
//
// A decision cannot be replayed from a fingerprint alone — the fingerprint
// identifies the inputs without containing them. This is the honest statement
// of what replay means here: given the ORIGINAL REQUEST and this metadata, the
// engine can prove whether the same rules would decide the same way today, and
// name the drift if they would not.
type ReplayMetadata struct {
	// Decision identifies the original.
	Decision DecisionID
	// PolicyVersion is the snapshot version in force.
	PolicyVersion uint64
	// PolicyDigest fingerprints that snapshot's decision-relevant content.
	PolicyDigest Fingerprint
	// RequestPrint fingerprints the inputs.
	RequestPrint Fingerprint
	// Outcome and Reason are what was decided.
	Outcome Outcome
	// Reason is the short code.
	Reason string
	// DecidedBy names the winning policy.
	DecidedBy PolicyID
	// DecidedAt is when.
	DecidedAt time.Time
}

// ReplayMetadataOf extracts replay metadata from a decision.
func ReplayMetadataOf(d Decision, digest Fingerprint) ReplayMetadata {
	return ReplayMetadata{
		Decision: d.ID, PolicyVersion: d.PolicyVersion, PolicyDigest: digest,
		RequestPrint: d.RequestPrint, Outcome: d.Outcome, Reason: d.Reason,
		DecidedBy: d.DecidedBy, DecidedAt: d.DecidedAt,
	}
}

// Drift describes how a replayed decision differs from the original.
type Drift struct {
	// Same reports that the outcome is unchanged.
	Same bool
	// WasOutcome and NowOutcome are the two outcomes.
	WasOutcome, NowOutcome Outcome
	// WasPolicy and NowPolicy are the two deciding policies.
	WasPolicy, NowPolicy PolicyID
	// PolicyChanged reports that the policy set itself changed.
	PolicyChanged bool
	// WasVersion and NowVersion are the snapshot versions.
	WasVersion, NowVersion uint64
}

// String renders the drift for an operator.
func (d Drift) String() string {
	if d.Same && !d.PolicyChanged {
		return "identical"
	}
	if d.Same {
		return "same outcome under a changed policy set"
	}
	return string(d.WasOutcome.String()) + " → " + d.NowOutcome.String() +
		" (" + string(d.WasPolicy) + " → " + string(d.NowPolicy) + ")"
}
