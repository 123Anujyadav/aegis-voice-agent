package runtime

import (
	metrics "github.com/callscreen/callscreen-platform/packages/go/metrics"
)

// Metrics is the runtime's instrument set.
//
// It is a struct of concrete instruments owned by a Kernel, not a global
// registry. Two kernels in one process keep separate metrics, which is what
// makes the test suite parallel-safe and what would make multi-tenancy possible
// later. A package-level registry — the usual shape — would make both
// impossible and is the reason so many Go services cannot test their own
// instrumentation.
//
// The implementation is deliberately small and stdlib-only. It is not a
// Prometheus client; it is a set of counters a Prometheus client can read.
// [Metrics.Snapshot] renders everything in one pass, and an exporter in a
// sibling module turns that into whatever wire format is wanted. That keeps the
// OpenTelemetry dependency out of the kernel entirely.
// Since Phase 10.5 the instruments themselves live in packages/go/metrics and
// this type is a binding over them. That package exists because THIS one kept
// its constructors unexported: five downstream phases each forked the plumbing
// rather than extend it, and the shared registry exports what they needed.
type Metrics struct {
	// Scheduler
	SchedulerShed      *Counter
	SchedulerOvershoot *Counter
	SchedulerInFlight  *Gauge
	SchedulerQueueWait *Histogram
	SchedulerDuration  *Histogram

	// Streaming
	StreamCompleted           *Counter
	StreamAborted             *Counter
	StreamFailed              *Counter
	StreamStalled             *Counter
	StreamChunks              *Counter
	StreamDuration            *Histogram
	StreamTimeToFirstToken    *Histogram
	StreamAbortLatency        *Histogram
	StreamAbortBudgetExceeded *Counter
	SinkDetached              *Counter

	// Sessions
	SessionsActive  *Gauge
	SessionsCreated *Counter
	SessionsExpired *Counter
	SessionLifetime *Histogram

	// Context
	ContextTokens   *Histogram
	ContextEvicted  *Counter
	ContextOverflow *Counter

	// Providers
	ProviderRequests  *Counter
	ProviderFailures  *Counter
	ProviderLatency   *Histogram
	ProviderRetries   *Counter
	BreakerState      *Gauge
	BreakerTransition *Counter

	// Models
	ModelTokens     *Counter
	TierEscalations *Counter
	TierDowngrades  *Counter

	// reg owns every instrument above. Shared implementation:
	// packages/go/metrics.
	reg *metrics.Registry
}

// NewMetrics constructs an instrument set.
func NewMetrics() *Metrics {
	m := &Metrics{reg: metrics.NewRegistry()}

	// Latency buckets in seconds, chosen around the frozen budget: p50 900 ms,
	// p95 1500 ms, p99 ceiling 2500 ms (ADR-0011). Generic exponential buckets
	// would put only two boundaries in the range we actually care about, which
	// makes the histogram useless for the one question we ask of it.
	lat := []float64{0.005, 0.01, 0.02, 0.05, 0.1, 0.2, 0.35, 0.5, 0.75,
		0.9, 1.1, 1.5, 2.0, 2.5, 4.0, 8.0}

	// Abort buckets are much finer: the whole budget is 20 ms, so a bucket at
	// 100 ms tells us nothing. Boundaries straddle 20 ms so a breach is
	// visible as a bucket crossing rather than inferred from a percentile.
	abort := []float64{0.0005, 0.001, 0.002, 0.005, 0.01, 0.015, 0.02, 0.03,
		0.05, 0.1, 0.25}

	tokens := []float64{16, 64, 256, 1024, 4096, 16384, 65536, 262144}

	m.SchedulerShed = m.counter("runtime_scheduler_shed_total", "class", "reason")
	m.SchedulerOvershoot = m.counter("runtime_scheduler_overshoot_total", "class")
	m.SchedulerInFlight = m.gauge("runtime_scheduler_in_flight")
	m.SchedulerQueueWait = m.histogram("runtime_scheduler_queue_wait_seconds", lat, "class")
	m.SchedulerDuration = m.histogram("runtime_scheduler_task_seconds", lat, "class")

	m.StreamCompleted = m.counter("runtime_stream_completed_total")
	m.StreamAborted = m.counter("runtime_stream_aborted_total")
	m.StreamFailed = m.counter("runtime_stream_failed_total")
	m.StreamStalled = m.counter("runtime_stream_stalled_total")
	m.StreamChunks = m.counter("runtime_stream_chunks_total")
	m.StreamDuration = m.histogram("runtime_stream_duration_seconds", lat)
	m.StreamTimeToFirstToken = m.histogram("runtime_stream_ttft_seconds", lat)
	m.StreamAbortLatency = m.histogram("runtime_stream_abort_latency_seconds", abort)
	m.StreamAbortBudgetExceeded = m.counter("runtime_stream_abort_budget_exceeded_total")
	m.SinkDetached = m.counter("runtime_sink_detached_total")

	m.SessionsActive = m.gauge("runtime_sessions_active")
	m.SessionsCreated = m.counter("runtime_sessions_created_total")
	m.SessionsExpired = m.counter("runtime_sessions_expired_total", "reason")
	m.SessionLifetime = m.histogram("runtime_session_lifetime_seconds",
		[]float64{1, 5, 10, 20, 30, 60, 120, 300, 900})

	m.ContextTokens = m.histogram("runtime_context_tokens", tokens)
	m.ContextEvicted = m.counter("runtime_context_evicted_total", "policy")
	m.ContextOverflow = m.counter("runtime_context_overflow_total")

	m.ProviderRequests = m.counter("runtime_provider_requests_total", "provider", "model")
	m.ProviderFailures = m.counter("runtime_provider_failures_total", "provider", "kind")
	m.ProviderLatency = m.histogram("runtime_provider_latency_seconds", lat, "provider", "model")
	m.ProviderRetries = m.counter("runtime_provider_retries_total", "provider")
	m.BreakerState = m.gauge("runtime_breaker_state")
	m.BreakerTransition = m.counter("runtime_breaker_transition_total", "provider", "to")

	m.ModelTokens = m.counter("runtime_model_tokens_total", "model", "kind")
	m.TierEscalations = m.counter("runtime_tier_escalations_total", "from", "to")
	m.TierDowngrades = m.counter("runtime_tier_downgrades_total", "from", "to")

	return m
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
// *runtime.Counter is holding exactly a *metrics.Counter.
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
