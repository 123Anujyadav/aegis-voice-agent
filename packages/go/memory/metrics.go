package memory

import (
	metrics "github.com/callscreen/callscreen-platform/packages/go/metrics"
)

// Metrics is the memory engine's instrument set.
//
// Engine-scoped, not global: it is owned by a [Runtime], two runtimes in one
// process share nothing, and the test suite is parallel-safe as a result. That
// is the same property Phases 10A and 10B have.
//
// It uses the shared instruments in packages/go/metrics. Until Phase 10.5 it
// carried a private ~490-line copy of counter/gauge/histogram plumbing, because
// runtime.Metrics kept its constructors unexported and Phase 10A was frozen —
// the "A1" finding, first handover item for four consecutive phase audits. The
// shared package exports its constructors, so this file is now the thin binding
// those audits predicted it would collapse to.
type Metrics struct {
	// Operations
	Stores    *Counter // by kind, tier
	Retrieves *Counter // by kind, outcome
	Updates   *Counter // by kind
	Deletes   *Counter // by kind, reason
	Conflicts *Counter // by kind

	// Hit rate — the headline quality signal for a memory layer
	Hits   *Counter // by lookup
	Misses *Counter // by lookup, reason

	// Latency
	RetrieveLatency *Histogram // by lookup
	StoreLatency    *Histogram
	SweepLatency    *Histogram // by phase

	// Lifecycle
	Promotions  *Counter // from, to
	Demotions   *Counter // from, to
	Expirations *Counter // by kind, tier
	Archivals   *Counter // by kind
	Restores    *Counter // by kind
	Redactions  *Counter // by kind, reason
	Erasures    *Counter // by outcome

	// Size
	Records   *Gauge     // by tier
	Bytes     *Gauge     // by tier
	RecordAge *Histogram // by tier

	// Compression
	Compressions     *Counter // by policy
	CompressionRatio *Histogram
	Merges           *Counter
	Splits           *Counter
	Pruned           *Counter // by reason

	// Policy and consent
	ConsentRefusals *Counter // by kind
	LegalHolds      *Counter
	AuditedReads    *Counter // by sensitivity

	// Index
	IndexPostings *Gauge
	IndexScans    *Counter // by index

	// Events
	EventsPublished *Counter // by type
	EventsDropped   *Counter // by reason

	// reg owns every instrument above. Shared implementation:
	// packages/go/metrics.
	reg *metrics.Registry
}

// NewMetrics constructs an instrument set.
func NewMetrics() *Metrics {
	m := &Metrics{reg: metrics.NewRegistry()}

	// Retrieval buckets are microsecond-scale. A memory read that takes a
	// millisecond has already failed its purpose — the conversation engine
	// budgets 15 ms for its entire context stage (Phase 10B), and memory is
	// one part of that.
	fast := []float64{1e-6, 5e-6, 1e-5, 2.5e-5, 5e-5, 1e-4, 2.5e-4, 5e-4,
		1e-3, 5e-3, 0.025, 0.1}

	// Sweeps run on a timer and may take milliseconds without harm.
	slow := []float64{1e-4, 5e-4, 1e-3, 5e-3, 0.01, 0.05, 0.1, 0.5, 1, 5}

	ages := []float64{1, 10, 60, 300, 1800, 3600, 86400, 604800, 2592000}
	ratio := []float64{0.05, 0.1, 0.2, 0.3, 0.5, 0.7, 0.9, 1.0}

	m.Stores = m.counter("memory_stores_total", "kind", "tier")
	m.Retrieves = m.counter("memory_retrieves_total", "kind", "outcome")
	m.Updates = m.counter("memory_updates_total", "kind")
	m.Deletes = m.counter("memory_deletes_total", "kind", "reason")
	m.Conflicts = m.counter("memory_version_conflicts_total", "kind")

	m.Hits = m.counter("memory_hits_total", "lookup")
	m.Misses = m.counter("memory_misses_total", "lookup", "reason")

	m.RetrieveLatency = m.histogram("memory_retrieve_seconds", fast, "lookup")
	m.StoreLatency = m.histogram("memory_store_seconds", fast)
	m.SweepLatency = m.histogram("memory_sweep_seconds", slow, "phase")

	m.Promotions = m.counter("memory_promotions_total", "from", "to")
	m.Demotions = m.counter("memory_demotions_total", "from", "to")
	m.Expirations = m.counter("memory_expirations_total", "kind", "tier")
	m.Archivals = m.counter("memory_archivals_total", "kind")
	m.Restores = m.counter("memory_restores_total", "kind")
	m.Redactions = m.counter("memory_redactions_total", "kind", "reason")
	m.Erasures = m.counter("memory_erasures_total", "outcome")

	m.Records = m.gauge("memory_records")
	m.Bytes = m.gauge("memory_bytes")
	m.RecordAge = m.histogram("memory_record_age_seconds", ages, "tier")

	m.Compressions = m.counter("memory_compressions_total", "policy")
	m.CompressionRatio = m.histogram("memory_compression_ratio", ratio)
	m.Merges = m.counter("memory_merges_total")
	m.Splits = m.counter("memory_splits_total")
	m.Pruned = m.counter("memory_pruned_total", "reason")

	m.ConsentRefusals = m.counter("memory_consent_refusals_total", "kind")
	m.LegalHolds = m.counter("memory_legal_holds_total")
	m.AuditedReads = m.counter("memory_audited_reads_total", "sensitivity")

	m.IndexPostings = m.gauge("memory_index_postings")
	m.IndexScans = m.counter("memory_index_scans_total", "index")

	m.EventsPublished = m.counter("memory_events_published_total", "type")
	m.EventsDropped = m.counter("memory_events_dropped_total", "reason")

	return m
}

// HitRate returns hits / (hits + misses) for a lookup, or for every lookup when
// name is empty. Returns 0 when nothing has been observed, which is
// distinguishable from a genuine zero only by consulting the counters — a
// deliberate simplification, since a rate over no observations is undefined
// rather than zero.
func (m *Metrics) HitRate(lookup string) float64 {
	var hits, misses uint64
	if lookup == "" {
		hits, misses = m.Hits.Total(), m.Misses.Total()
	} else {
		hits = m.Hits.Value(lookup)
		misses = m.missesFor(lookup)
	}
	total := hits + misses
	if total == 0 {
		return 0
	}
	return float64(hits) / float64(total)
}

// missesFor sums misses across every reason for one lookup.
//
// This was a hand-rolled loop over the counter's unexported map, carrying its
// own copy of the label separator. It is exactly [metrics.Counter.PrefixSum] —
// which three other subsystems had independently written too, which is why the
// shared package exports it rather than leaving each caller to reach inside.
func (m *Metrics) missesFor(lookup string) uint64 {
	return m.Misses.PrefixSum(lookup)
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
// *memory.Counter is holding exactly a *metrics.Counter.
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
