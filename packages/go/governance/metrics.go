package governance

import (
	metrics "github.com/callscreen/callscreen-platform/packages/go/metrics"
)

// Metrics is the governance engine's instrument set.
//
// Engine-scoped, not global: owned by an [Engine], two engines in one process
// share nothing, and the test suite is parallel-safe as a result.
//
// It uses the shared instruments in packages/go/metrics. Until Phase 10.5 it
// carried a private ~490-line copy of counter/gauge/histogram plumbing, because
// runtime.Metrics kept its constructors unexported and Phase 10A was frozen —
// the "A1" finding, first handover item for four consecutive phase audits. The
// shared package exports its constructors, so this file is now the thin binding
// those audits predicted it would collapse to.
type Metrics struct {
	// Decisions
	Decisions   *Counter   // outcome, action
	Denials     *Counter   // reason, scope
	Escalations *Counter   // outcome, scope
	NoPolicy    *Counter   // action
	Conflicts   *Counter   // scope
	EvalLatency *Histogram // action
	TraceLength *Histogram

	// Consent
	ConsentChecks   *Counter // outcome
	ConsentGrants   *Counter // basis
	ConsentRevokes  *Counter // basis
	ConsentExpiries *Counter // basis
	ConsentRecords  *Gauge

	// Risk
	RiskLevels  *Counter // level
	RiskSignals *Counter // source, level
	Thresholds  *Counter // reason

	// Emergency
	EmergencyActivations *Counter // name
	EmergencyUses        *Counter // name
	EmergencyActive      *Gauge

	// Human override
	Escalated       *Counter // reason
	Resolved        *Counter // resolution
	EscalationDepth *Gauge
	EscalationWait  *Histogram

	// Registry
	Policies        *Gauge
	SnapshotVersion *Gauge

	// Events and audit
	EventsPublished *Counter // type
	EventsDropped   *Counter // type
	AuditWritten    *Counter // kind
	AuditFailed     *Counter // kind

	// reg owns every instrument above. Shared implementation:
	// packages/go/metrics.
	reg *metrics.Registry
}

// NewMetrics constructs an instrument set.
func NewMetrics() *Metrics {
	m := &Metrics{reg: metrics.NewRegistry()}

	// Evaluation is a pure in-memory function over a few dozen policies, so
	// the buckets are microsecond-scale. A governance decision that takes a
	// millisecond has become a latency problem in its own right, and these
	// buckets are chosen so that would be visible rather than lost in a top
	// bucket.
	fast := []float64{1e-7, 5e-7, 1e-6, 5e-6, 1e-5, 5e-5, 1e-4, 5e-4, 1e-3, 5e-3, 0.05}

	// Escalations wait for a human, so their scale is seconds to hours.
	slow := []float64{1, 5, 15, 60, 300, 900, 3600, 14400, 86400}

	lengths := []float64{1, 2, 4, 8, 16, 32, 64}

	m.Decisions = m.counter("governance_decisions_total", "outcome", "action")
	m.Denials = m.counter("governance_denials_total", "reason", "scope")
	m.Escalations = m.counter("governance_escalations_total", "outcome", "scope")
	m.NoPolicy = m.counter("governance_no_policy_total", "action")
	m.Conflicts = m.counter("governance_policy_conflicts_total", "scope")
	m.EvalLatency = m.histogram("governance_evaluation_seconds", fast, "action")
	m.TraceLength = m.histogram("governance_trace_entries", lengths)

	m.ConsentChecks = m.counter("governance_consent_checks_total", "outcome")
	m.ConsentGrants = m.counter("governance_consent_grants_total", "basis")
	m.ConsentRevokes = m.counter("governance_consent_revocations_total", "basis")
	m.ConsentExpiries = m.counter("governance_consent_expiries_total", "basis")
	m.ConsentRecords = m.gauge("governance_consent_records")

	m.RiskLevels = m.counter("governance_risk_levels_total", "level")
	m.RiskSignals = m.counter("governance_risk_signals_total", "source", "level")
	m.Thresholds = m.counter("governance_risk_thresholds_total", "reason")

	m.EmergencyActivations = m.counter("governance_emergency_activations_total", "name")
	m.EmergencyUses = m.counter("governance_emergency_uses_total", "name")
	m.EmergencyActive = m.gauge("governance_emergency_active")

	m.Escalated = m.counter("governance_human_escalations_total", "reason")
	m.Resolved = m.counter("governance_human_resolutions_total", "resolution")
	m.EscalationDepth = m.gauge("governance_escalation_queue_depth")
	m.EscalationWait = m.histogram("governance_escalation_wait_seconds", slow)

	m.Policies = m.gauge("governance_policies")
	m.SnapshotVersion = m.gauge("governance_policy_snapshot_version")

	m.EventsPublished = m.counter("governance_events_published_total", "type")
	m.EventsDropped = m.counter("governance_events_dropped_total", "type")
	m.AuditWritten = m.counter("governance_audit_written_total", "kind")
	m.AuditFailed = m.counter("governance_audit_failed_total", "kind")

	return m
}

// AllowRate returns allows / total decisions.
//
// Returns 0 when nothing has been observed. A rate over no observations is
// undefined rather than zero, and a caller distinguishes the two by consulting
// the counters — the same deliberate simplification Phases 10C and 10D make.
func (m *Metrics) AllowRate() float64 {
	total := m.Decisions.Total()
	if total == 0 {
		return 0
	}
	return float64(m.Decisions.PrefixSum(OutcomeAllow.String())) / float64(total)
}

// DenyRate returns denials / total decisions.
func (m *Metrics) DenyRate() float64 {
	total := m.Decisions.Total()
	if total == 0 {
		return 0
	}
	return float64(m.Decisions.PrefixSum(OutcomeDeny.String())) / float64(total)
}

// EscalationRate returns the share of decisions that needed a human.
func (m *Metrics) EscalationRate() float64 {
	total := m.Decisions.Total()
	if total == 0 {
		return 0
	}
	var n uint64
	for _, o := range []Outcome{OutcomeEscalate, OutcomeRequireConfirmation,
		OutcomeRequireHuman, OutcomeRequireSupervisor} {
		n += m.Decisions.PrefixSum(o.String())
	}
	return float64(n) / float64(total)
}

// ConsentRate returns satisfied consent checks / total checks.
func (m *Metrics) ConsentRate() float64 {
	total := m.ConsentChecks.Total()
	if total == 0 {
		return 0
	}
	return float64(m.ConsentChecks.Value("valid")) / float64(total)
}

// RiskDistribution returns the share of decisions at each risk level.
func (m *Metrics) RiskDistribution() map[RiskLevel]float64 {
	total := m.RiskLevels.Total()
	out := make(map[RiskLevel]float64, 4)
	for _, l := range []RiskLevel{RiskLow, RiskMedium, RiskHigh, RiskCritical} {
		if total == 0 {
			out[l] = 0
			continue
		}
		out[l] = float64(m.RiskLevels.Value(l.String())) / float64(total)
	}
	return out
}

// ---------------------------------------------------------------------------
// Instruments
// ---------------------------------------------------------------------------

// The instrument types are ALIASES, not wrappers.
//
// `type Counter = metrics.Counter` rather than `type Counter metrics.Counter`,
// so every existing reference in this package — and in anything importing it —
// keeps compiling and keeps its method set. That is what made this migration
// additive: no call site changed, no signature changed, and a caller holding a
// *governance.Counter is holding exactly a *metrics.Counter.
//
// Before this, each phase carried its own ~490-line copy of these primitives.
// Six copies, and they had drifted where it mattered: three of the six could
// not emit histogram buckets, counts or sums from Snapshot() at all, so a
// cross-subsystem latency dashboard could not be built. See
// docs/hardening/METRICS_MIGRATION_REPORT.md.
type (
	// Counter is a monotonically increasing value, optionally labelled.
	Counter = metrics.Counter
	// Gauge is a value that may move in either direction.
	Gauge = metrics.Gauge
	// Histogram counts observations into caller-supplied buckets.
	Histogram = metrics.Histogram
	// Sample is one instrument series at one instant.
	Sample = metrics.Sample
)

func (m *Metrics) counter(name string, labels ...string) *Counter {
	return m.reg.Counter(name, labels...)
}

func (m *Metrics) gauge(name string) *Gauge {
	return m.reg.Gauge(name)
}

func (m *Metrics) histogram(name string, buckets []float64, labels ...string) *Histogram {
	return m.reg.Histogram(name, buckets, labels...)
}

// Registry exposes the underlying instrument registry.
//
// Added so a service can register its own instruments alongside this
// subsystem's, and export them through one scrape. The absence of this was the
// specific reason six copies of the primitives came to exist: the original
// registry kept its constructors unexported, so extending it was impossible and
// forking it was easy.
func (m *Metrics) Registry() *metrics.Registry { return m.reg }

// Snapshot returns every series from every instrument, in a stable order.
//
// Delegated to the shared registry, which means it now carries full histogram
// data — bounds, cumulative buckets, count and sum — in every subsystem. Three
// of the six previously emitted a single synthetic `name_count` value with none
// of that, and no consumer could recover a percentile or an average from it.
func (m *Metrics) Snapshot() []Sample { return m.reg.Snapshot() }
