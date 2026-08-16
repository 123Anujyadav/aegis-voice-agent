package evaluation

import (
	metrics "github.com/callscreen/callscreen-platform/packages/go/metrics"
)

// Metrics is the evaluation platform's own instrument set.
//
// Runtime-scoped, not global: owned by an [EvaluationRuntime], two runtimes in
// one process share nothing, and the test suite is parallel-safe as a result.
//
// It uses the shared instruments in packages/go/metrics. Until Phase 10.5 it
// carried a private ~490-line copy of counter/gauge/histogram plumbing, because
// runtime.Metrics kept its constructors unexported and Phase 10A was frozen —
// the "A1" finding, first handover item for four consecutive phase audits. The
// shared package exports its constructors, so this file is now the thin binding
// those audits predicted it would collapse to.
//
// Note the distinction this type must not blur: these are metrics ABOUT the
// evaluation platform — how many scenarios ran, how long evaluation took. The
// metrics about the SUBJECTS live in [Observation.Metrics] and [Scorecard],
// because mixing them would make "latency" ambiguous in exactly the report where
// it matters most.
type Metrics struct {
	// Runs
	Runs         *Counter   // suite
	ScenariosRun *Counter   // subject, kind
	Verdicts     *Counter   // verdict, subject
	ScenarioTime *Histogram // subject
	RunTime      *Histogram
	StepTime     *Histogram // subject

	// Goldens
	GoldensRecorded *Counter // subject
	GoldensApproved *Counter // subject
	PendingGoldens  *Gauge

	// Determinism and replay
	DeterminismChecks   *Counter // subject, outcome
	DeterminismDiverged *Counter // subject
	Replays             *Counter // subject, outcome

	// Regression
	Regressions  *Counter // subject, kind
	Improvements *Counter // subject

	// Failure injection
	Injections *Counter // kind, subject
	Skipped    *Counter // subject, reason

	// Platform
	Registered    *Gauge
	InFlight      *Gauge
	StorageWrites *Counter // kind
	StorageErrors *Counter // kind

	// reg owns every instrument above. Shared implementation:
	// packages/go/metrics.
	reg *metrics.Registry
}

// NewMetrics constructs an instrument set.
func NewMetrics() *Metrics {
	m := &Metrics{reg: metrics.NewRegistry()}

	// Scenarios span microseconds (a memory read) to seconds (a conversation
	// with clock advances), so the buckets are deliberately wide. A narrow set
	// tuned to one subsystem would put another's every observation in the top
	// bucket.
	wide := []float64{1e-6, 1e-5, 1e-4, 1e-3, 0.01, 0.05, 0.1, 0.5, 1, 5, 30}
	fine := []float64{1e-7, 1e-6, 1e-5, 5e-5, 1e-4, 5e-4, 1e-3, 0.01, 0.1}

	m.Runs = m.counter("evaluation_runs_total", "suite")
	m.ScenariosRun = m.counter("evaluation_scenarios_total", "subject", "kind")
	m.Verdicts = m.counter("evaluation_verdicts_total", "verdict", "subject")
	m.ScenarioTime = m.histogram("evaluation_scenario_seconds", wide, "subject")
	m.RunTime = m.histogram("evaluation_run_seconds", wide)
	m.StepTime = m.histogram("evaluation_step_seconds", fine, "subject")

	m.GoldensRecorded = m.counter("evaluation_goldens_recorded_total", "subject")
	m.GoldensApproved = m.counter("evaluation_goldens_approved_total", "subject")
	m.PendingGoldens = m.gauge("evaluation_goldens_pending")

	m.DeterminismChecks = m.counter("evaluation_determinism_checks_total", "subject", "outcome")
	m.DeterminismDiverged = m.counter("evaluation_determinism_divergences_total", "subject")
	m.Replays = m.counter("evaluation_replays_total", "subject", "outcome")

	m.Regressions = m.counter("evaluation_regressions_total", "subject", "kind")
	m.Improvements = m.counter("evaluation_improvements_total", "subject")

	m.Injections = m.counter("evaluation_injections_total", "kind", "subject")
	m.Skipped = m.counter("evaluation_skipped_total", "subject", "reason")

	m.Registered = m.gauge("evaluation_scenarios_registered")
	m.InFlight = m.gauge("evaluation_scenarios_in_flight")
	m.StorageWrites = m.counter("evaluation_storage_writes_total", "kind")
	m.StorageErrors = m.counter("evaluation_storage_errors_total", "kind")

	return m
}

// PassRate returns passes over scenarios with a verdict, or zero when none.
//
// SKIPS AND NO-BASELINE RESULTS ARE EXCLUDED FROM THE DENOMINATOR. Counting
// them would let a suite improve its pass rate by adding scenarios nothing can
// run — which is the metric gaming an evaluation platform must be hardest
// against, because it is the one nobody notices.
func (m *Metrics) PassRate() float64 {
	pass := m.Verdicts.PrefixSum(VerdictPass.String())
	drift := m.Verdicts.PrefixSum(VerdictDrift.String())
	fail := m.Verdicts.PrefixSum(VerdictFail.String())
	total := pass + drift + fail
	if total == 0 {
		return 0
	}
	return float64(pass) / float64(total)
}

// DriftRate returns drifts over scenarios with a verdict.
func (m *Metrics) DriftRate() float64 {
	pass := m.Verdicts.PrefixSum(VerdictPass.String())
	drift := m.Verdicts.PrefixSum(VerdictDrift.String())
	fail := m.Verdicts.PrefixSum(VerdictFail.String())
	total := pass + drift + fail
	if total == 0 {
		return 0
	}
	return float64(drift) / float64(total)
}

// FailRate returns failures over scenarios with a verdict.
func (m *Metrics) FailRate() float64 {
	pass := m.Verdicts.PrefixSum(VerdictPass.String())
	drift := m.Verdicts.PrefixSum(VerdictDrift.String())
	fail := m.Verdicts.PrefixSum(VerdictFail.String())
	total := pass + drift + fail
	if total == 0 {
		return 0
	}
	return float64(fail) / float64(total)
}

// CoverageRate returns the share of scenarios that actually ran.
//
// The metric that catches a suite quietly skipping half its work because an
// adapter lost a capability.
func (m *Metrics) CoverageRate() float64 {
	total := m.Verdicts.Total()
	if total == 0 {
		return 0
	}
	skipped := m.Verdicts.PrefixSum(VerdictSkipped.String())
	return float64(total-skipped) / float64(total)
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
// *evaluation.Counter is holding exactly a *metrics.Counter.
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
