package toolruntime

import (
	"sort"
	"sync"
	"time"
)

// AuditKind classifies an audit entry.
//
// A small closed enum, because these become metric labels and audit-store
// partition keys, and both punish free text.
type AuditKind string

// The audit kinds.
const (
	AuditExecutionStarted   AuditKind = "execution_started"
	AuditExecutionCompleted AuditKind = "execution_completed"
	AuditExecutionFailed    AuditKind = "execution_failed"
	AuditPermissionDenied   AuditKind = "permission_denied"
	AuditOverrideInstalled  AuditKind = "override_installed"
	AuditOverrideUsed       AuditKind = "override_used"
	AuditCompensated        AuditKind = "compensated"
	AuditCompensationFailed AuditKind = "compensation_failed"
	AuditReplayed           AuditKind = "replayed"
	AuditRegistered         AuditKind = "tool_registered"
	AuditUnregistered       AuditKind = "tool_unregistered"
)

// AuditEntry is one record in the execution audit trail.
//
// IT CARRIES FINGERPRINTS, NOT ARGUMENTS.
//
// The argument to a tool call is frequently personal data — a phone number, an
// address, what somebody said they wanted. An audit trail is written to durable
// storage, replicated, retained for years and read by people during incidents.
// Putting arguments in it creates a second copy of personal data with a
// different retention schedule and a different access-control model from the
// system of record, which is the exact shape of finding that shows up in an
// audit as "unmanaged personal data store".
//
// The fingerprints answer the questions an audit actually asks: was this the
// same call as last time, did the same input produce a different output, did
// the arguments change between the attempt that failed and the one that
// succeeded. None of those needs the values.
type AuditEntry struct {
	// At is the entry instant on the runtime clock.
	At time.Time
	// Kind classifies the entry.
	Kind AuditKind
	// Execution, Step and Plan locate the entry in an execution.
	Execution ExecutionID
	Step      StepID
	Plan      PlanID
	// Correlation ties every entry from one conversation turn together, across
	// processes. The single most useful field during an incident.
	Correlation CorrelationID
	// Session identifies the conversation.
	Session SessionID
	// Actor is on whose behalf the action ran.
	Actor ActorID
	// Descriptor is the tool and pinned version.
	Descriptor Descriptor
	// InputPrint and OutputPrint fingerprint the arguments and result.
	InputPrint  Fingerprint
	OutputPrint Fingerprint
	// Attempt is 1-based, zero for entries not tied to an attempt.
	Attempt int
	// Phase names where in the execution this happened.
	Phase Phase
	// Duration is how long the action took, zero where meaningless.
	Duration time.Duration
	// Reason is a short machine-readable code.
	Reason string
	// Details carries structured extras. Values must not be caller content;
	// nothing in this package puts caller content here.
	Details map[string]string
}

// Auditor receives audit entries.
//
// Synchronous by contract and expected to be fast. A tool runtime that cannot
// write an audit entry has a decision to make, and this package makes it
// explicitly: see [Config.RequireAuditor].
type Auditor interface {
	// Record writes one entry. An error is counted; it does not fail the
	// execution, because failing a completed action because its record could
	// not be written leaves the world changed AND unrecorded, which is strictly
	// worse than changed and unrecorded-with-a-metric.
	Record(AuditEntry) error
}

// NoopAuditor discards entries.
//
// Permitted only when Config.RequireAuditor is false, which itself is permitted
// only for tools with no mutating effect. A runtime that takes actions in the
// world with no audit trail cannot answer "who did that", and that is an
// obligation rather than a preference.
type NoopAuditor struct{}

// Record discards the entry.
func (NoopAuditor) Record(AuditEntry) error { return nil }

// RecordingAuditor captures entries for assertion.
//
// Exported because every service embedding this runtime needs to test its own
// reaction to audit entries, and an auditor only this package can build pushes
// each of them into writing their own.
type RecordingAuditor struct {
	mu      sync.Mutex
	entries []AuditEntry
	err     error
	max     int
}

// NewRecordingAuditor builds a recording auditor holding up to max entries.
func NewRecordingAuditor(max int) *RecordingAuditor {
	if max <= 0 {
		max = 4096
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

// Trace is the ordered audit record of one correlation.
//
// Assembled on demand rather than maintained, because the maintained version
// would need eviction, and an execution trace that evicts is a trace that is
// missing exactly the entry somebody is looking for.
type Trace struct {
	// Correlation identifies the turn.
	Correlation CorrelationID
	// Entries are ordered by time, then by execution, then by kind — a total
	// order, so two assemblies of the same trace are identical.
	Entries []AuditEntry
}

// TraceFor assembles the trace for one correlation from a recording auditor.
func TraceFor(a *RecordingAuditor, c CorrelationID) Trace {
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
		if out[i].Execution != out[j].Execution {
			return out[i].Execution < out[j].Execution
		}
		return out[i].Kind < out[j].Kind
	})
	return Trace{Correlation: c, Entries: out}
}

// Summary renders the trace as one line per entry, for diagnostics.
func (t Trace) Summary() []string {
	out := make([]string, 0, len(t.Entries))
	for _, e := range t.Entries {
		line := string(e.Kind)
		if e.Descriptor.Tool != "" {
			line += " " + e.Descriptor.String()
		}
		if e.Step != "" {
			line += " step=" + string(e.Step)
		}
		if e.Reason != "" {
			line += " reason=" + e.Reason
		}
		out = append(out, line)
	}
	return out
}
