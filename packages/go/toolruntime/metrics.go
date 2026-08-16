package toolruntime

import (
	metrics "github.com/callscreen/callscreen-platform/packages/go/metrics"
)

// Metrics is the tool runtime's instrument set.
//
// Runtime-scoped, not global: it is owned by a [ToolRuntime], two runtimes in
// one process share nothing, and the test suite is parallel-safe as a result.
//
// It uses the shared instruments in packages/go/metrics. Until Phase 10.5 it
// carried a private ~490-line copy of counter/gauge/histogram plumbing, because
// runtime.Metrics kept its constructors unexported and Phase 10A was frozen —
// the "A1" finding, first handover item for four consecutive phase audits. The
// shared package exports its constructors, so this file is now the thin binding
// those audits predicted it would collapse to.
type Metrics struct {
	// Executions
	Started   *Counter // tool, capability
	Completed *Counter // tool, capability
	Failed    *Counter // tool, phase, reason
	Cancelled *Counter // tool, reason
	TimedOut  *Counter // tool
	Retried   *Counter // tool, attempt
	Abandoned *Counter // tool — timed out and the goroutine never returned

	// Planning
	PlansBuilt   *Counter   // shape
	PlanSteps    *Histogram // shape
	PlanFailures *Counter   // reason

	// Discovery
	Resolutions      *Counter // capability, outcome
	FallbacksUsed    *Counter // capability
	NoHealthyTool    *Counter // capability
	RegistryVersions *Gauge

	// Permission
	PermissionAllowed *Counter // tool, reason
	PermissionDenied  *Counter // tool, reason

	// Idempotency
	LedgerHits   *Counter // tool
	LedgerMisses *Counter // tool
	LedgerSize   *Gauge

	// Compensation
	Compensations       *Counter // tool, outcome
	CompensationFailure *Counter // tool

	// Latency
	ExecutionLatency *Histogram // tool
	InvokeLatency    *Histogram // tool
	QueueWait        *Histogram // class
	PlanLatency      *Histogram

	// Queue and capacity
	QueueDepth    *Gauge // class
	QueueRejected *Counter
	InFlight      *Gauge
	SandboxSlots  *Gauge
	BudgetRefused *Counter // tool, limit

	// Streaming
	ChunksEmitted *Counter // tool, kind
	ChunksDropped *Counter // tool

	// Events
	EventsPublished *Counter // type
	EventsDropped   *Counter // type

	// Audit
	AuditWritten *Counter // kind
	AuditFailed  *Counter // kind

	// reg owns every instrument above. Shared implementation:
	// packages/go/metrics.
	reg *metrics.Registry
}

// NewMetrics constructs an instrument set.
func NewMetrics() *Metrics {
	m := &Metrics{reg: metrics.NewRegistry()}

	// Execution buckets span milliseconds to tens of seconds. A tool call is
	// network work, not a map lookup: the memory engine's microsecond buckets
	// would put every observation in the top bucket and tell nobody anything.
	exec := []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

	// Planning and queueing are in-process and should be fast.
	fast := []float64{1e-6, 1e-5, 5e-5, 1e-4, 5e-4, 0.001, 0.005, 0.025, 0.1, 0.5}

	steps := []float64{1, 2, 3, 5, 8, 13, 21, 34}

	m.Started = m.counter("tool_executions_started_total", "tool", "capability")
	m.Completed = m.counter("tool_executions_completed_total", "tool", "capability")
	m.Failed = m.counter("tool_executions_failed_total", "tool", "phase", "reason")
	m.Cancelled = m.counter("tool_executions_cancelled_total", "tool", "reason")
	m.TimedOut = m.counter("tool_executions_timed_out_total", "tool")
	m.Retried = m.counter("tool_executions_retried_total", "tool", "attempt")
	m.Abandoned = m.counter("tool_executions_abandoned_total", "tool")

	m.PlansBuilt = m.counter("tool_plans_built_total", "shape")
	m.PlanSteps = m.histogram("tool_plan_steps", steps, "shape")
	m.PlanFailures = m.counter("tool_plan_failures_total", "reason")

	m.Resolutions = m.counter("tool_resolutions_total", "capability", "outcome")
	m.FallbacksUsed = m.counter("tool_fallbacks_used_total", "capability")
	m.NoHealthyTool = m.counter("tool_no_healthy_total", "capability")
	m.RegistryVersions = m.gauge("tool_registry_entries")

	m.PermissionAllowed = m.counter("tool_permission_allowed_total", "tool", "reason")
	m.PermissionDenied = m.counter("tool_permission_denied_total", "tool", "reason")

	m.LedgerHits = m.counter("tool_idempotency_hits_total", "tool")
	m.LedgerMisses = m.counter("tool_idempotency_misses_total", "tool")
	m.LedgerSize = m.gauge("tool_idempotency_entries")

	m.Compensations = m.counter("tool_compensations_total", "tool", "outcome")
	m.CompensationFailure = m.counter("tool_compensation_failures_total", "tool")

	m.ExecutionLatency = m.histogram("tool_execution_seconds", exec, "tool")
	m.InvokeLatency = m.histogram("tool_invoke_seconds", exec, "tool")
	m.QueueWait = m.histogram("tool_queue_wait_seconds", fast, "class")
	m.PlanLatency = m.histogram("tool_plan_seconds", fast)

	m.QueueDepth = m.gauge("tool_queue_depth")
	m.QueueRejected = m.counter("tool_queue_rejected_total")
	m.InFlight = m.gauge("tool_executions_in_flight")
	m.SandboxSlots = m.gauge("tool_sandbox_slots_in_use")
	m.BudgetRefused = m.counter("tool_budget_refused_total", "tool", "limit")

	m.ChunksEmitted = m.counter("tool_stream_chunks_total", "tool", "kind")
	m.ChunksDropped = m.counter("tool_stream_chunks_dropped_total", "tool")

	m.EventsPublished = m.counter("tool_events_published_total", "type")
	m.EventsDropped = m.counter("tool_events_dropped_total", "type")

	m.AuditWritten = m.counter("tool_audit_written_total", "kind")
	m.AuditFailed = m.counter("tool_audit_failed_total", "kind")

	return m
}

// SuccessRate returns completed / (completed + failed) for a tool, or across
// every tool when the name is empty.
//
// Returns 0 when nothing has been observed. A rate over no observations is
// undefined rather than zero, and the caller distinguishes the two by asking
// the counters — a deliberate simplification, taken so that a dashboard panel
// does not need a second query to know whether to render.
func (m *Metrics) SuccessRate(tool string) float64 {
	var ok, bad uint64
	if tool == "" {
		ok, bad = m.Completed.Total(), m.Failed.Total()
	} else {
		ok = m.Completed.PrefixSum(tool)
		bad = m.Failed.PrefixSum(tool)
	}
	total := ok + bad
	if total == 0 {
		return 0
	}
	return float64(ok) / float64(total)
}

// RetryRate returns retries per started execution.
func (m *Metrics) RetryRate(tool string) float64 {
	var retries, started uint64
	if tool == "" {
		retries, started = m.Retried.Total(), m.Started.Total()
	} else {
		retries = m.Retried.PrefixSum(tool)
		started = m.Started.PrefixSum(tool)
	}
	if started == 0 {
		return 0
	}
	return float64(retries) / float64(started)
}

// TimeoutRate returns timeouts per started execution.
func (m *Metrics) TimeoutRate(tool string) float64 {
	var timeouts, started uint64
	if tool == "" {
		timeouts, started = m.TimedOut.Total(), m.Started.Total()
	} else {
		timeouts = m.TimedOut.PrefixSum(tool)
		started = m.Started.PrefixSum(tool)
	}
	if started == 0 {
		return 0
	}
	return float64(timeouts) / float64(started)
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
// *toolruntime.Counter is holding exactly a *metrics.Counter.
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
