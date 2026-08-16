package telephony

import (
	metrics "github.com/callscreen/callscreen-platform/packages/go/metrics"
)

// RuntimeMetrics is the telephony runtime's instrument set.
//
// Runtime-scoped, not global: owned by a [TelephonyRuntime], two runtimes in
// one process share nothing, and the test suite is parallel-safe as a result.
//
// It uses the shared instruments in packages/go/metrics. This is the first
// module written after Phase 10.5 consolidated six forked copies of that
// plumbing, and it is the first to pay nothing for the privilege — no private
// Counter, no private Histogram, no seventh `Sample` shape, and a histogram
// that exports bounds, buckets, count and sum like every other subsystem's.
type RuntimeMetrics struct {
	// Volume
	CallsStarted *Counter // direction, provider
	CallsEnded   *Counter // direction, outcome, reason
	CallFailures *Counter // provider, reason
	Timeouts     *Counter // state, provider
	Transfers    *Counter // provider
	Escalations  *Counter // provider

	// State machine
	Transitions        *Counter // from, to
	InvalidTransitions *Counter // from, to

	// Latency
	CallDuration     *Histogram // direction, outcome
	TalkDuration     *Histogram // direction
	LifecycleLatency *Histogram // to
	SetupLatency     *Histogram // direction

	// Live population
	LiveCalls    *Gauge
	CallsByState *Gauge // set per state by the sweeper; see Observe

	// Admission
	Admitted *Counter // provider
	Shed     *Counter // provider, reason

	// Providers
	ProviderErrors *Counter // provider, operation

	// Events
	EventsPublished *Counter // type
	EventsDropped   *Counter // type

	// Recovery
	RecoveryAttempts  *Counter // outcome
	RecoveryDuration  *Histogram
	SnapshotsWritten  *Counter // outcome
	SnapshotsRestored *Counter // outcome

	reg *metrics.Registry
	// byState holds one gauge per state, built once. Fifteen gauges rather than
	// one labelled gauge because packages/go/metrics gauges are unlabelled —
	// and because a per-state gauge name is what a dashboard query wants.
	byState map[CallState]*Gauge
}

// The instrument types are ALIASES of the shared implementations, so a caller
// holding a *telephony.Counter is holding exactly a *metrics.Counter and a
// single exporter reads this subsystem alongside the other six.
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

// NewRuntimeMetrics constructs an instrument set.
func NewRuntimeMetrics() *RuntimeMetrics {
	m := &RuntimeMetrics{reg: metrics.NewRegistry()}

	// Bucket sets chosen from what a telephone call actually does, which is a
	// different scale from every other subsystem in the platform. Governance
	// decides in hundreds of nanoseconds; a call lasts minutes. Phase 10.5
	// established that bucket boundaries are a domain decision and must NOT be
	// standardised across subsystems — a shared set would put every call in the
	// top bucket.

	// Whole calls: seconds to an hour. A call under a second is a failure, and
	// the tail matters more than the head, so the buckets widen sharply.
	callSecs := []float64{1, 5, 10, 20, 30, 60, 120, 300, 600, 1800, 3600}

	// Talk time: same scale, but zero-heavy because rejected and unanswered
	// calls never talk.
	talkSecs := []float64{1, 5, 15, 30, 60, 120, 300, 600, 1800, 3600}

	// A single transition is in-process bookkeeping: microseconds. The floor is
	// deliberately below what a Windows clock can resolve (~520 µs, per Phase
	// 10F) so the histogram does not lie about a platform it may run on — the
	// overflow bucket is where the truth lives there, and it is visible.
	stepSecs := []float64{1e-6, 1e-5, 5e-5, 1e-4, 5e-4, 1e-3, 5e-3, 0.05, 0.5}

	// Call setup: the number a carrier SLA is written against.
	setupSecs := []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30}

	m.CallsStarted = m.reg.Counter("telephony_calls_started_total", "direction", "provider")
	m.CallsEnded = m.reg.Counter("telephony_calls_ended_total", "direction", "outcome", "reason")
	m.CallFailures = m.reg.Counter("telephony_call_failures_total", "provider", "reason")
	m.Timeouts = m.reg.Counter("telephony_timeouts_total", "state", "provider")
	m.Transfers = m.reg.Counter("telephony_transfers_total", "provider")
	m.Escalations = m.reg.Counter("telephony_escalations_total", "provider")

	m.Transitions = m.reg.Counter("telephony_transitions_total", "from", "to")
	m.InvalidTransitions = m.reg.Counter("telephony_invalid_transitions_total", "from", "to")

	m.CallDuration = m.reg.Histogram("telephony_call_seconds", callSecs, "direction", "outcome")
	m.TalkDuration = m.reg.Histogram("telephony_talk_seconds", talkSecs, "direction")
	m.LifecycleLatency = m.reg.Histogram("telephony_transition_seconds", stepSecs, "to")
	m.SetupLatency = m.reg.Histogram("telephony_setup_seconds", setupSecs, "direction")

	m.LiveCalls = m.reg.Gauge("telephony_calls_live")
	m.CallsByState = m.reg.Gauge("telephony_calls_by_state")

	m.Admitted = m.reg.Counter("telephony_admitted_total", "provider")
	m.Shed = m.reg.Counter("telephony_shed_total", "provider", "reason")

	m.ProviderErrors = m.reg.Counter("telephony_provider_errors_total", "provider", "operation")

	m.EventsPublished = m.reg.Counter("telephony_events_published_total", "type")
	m.EventsDropped = m.reg.Counter("telephony_events_dropped_total", "type")

	m.RecoveryAttempts = m.reg.Counter("telephony_recovery_attempts_total", "outcome")
	m.RecoveryDuration = m.reg.Histogram("telephony_recovery_seconds", setupSecs)
	m.SnapshotsWritten = m.reg.Counter("telephony_snapshots_written_total", "outcome")
	m.SnapshotsRestored = m.reg.Counter("telephony_snapshots_restored_total", "outcome")

	m.byState = make(map[CallState]*Gauge, len(AllStates()))
	for _, s := range AllStates() {
		m.byState[s] = m.reg.Gauge("telephony_calls_state_" + string(s))
	}

	return m
}

// Registry exposes the underlying instrument registry, so a service can export
// this subsystem alongside its own through one scrape.
func (m *RuntimeMetrics) Registry() *metrics.Registry { return m.reg }

// Snapshot returns every series, in a stable order.
func (m *RuntimeMetrics) Snapshot() []Sample { return m.reg.Snapshot() }

// ObserveStates sets the per-state gauges from a registry census.
//
// Takes the counts rather than the registry, so the caller decides when to pay
// for the walk. The sweeper calls this once per interval; nothing calls it on
// the call-setup path, which is the mistake Phase 10F made with its pending
// gauge and paid 45× for.
func (m *RuntimeMetrics) ObserveStates(counts map[CallState]int) {
	for _, s := range AllStates() {
		if g, ok := m.byState[s]; ok {
			// Every state is set, including the ones at zero. A gauge that stops
			// being reported when its population empties leaves a dashboard
			// showing the last non-zero value forever.
			g.Set(float64(counts[s]))
		}
	}
}

// FailureRate returns failed calls over all concluded calls, or zero when none.
//
// The denominator is every call that concluded, including the ones that failed:
// a rate over successes alone climbs above 1 as failures mount, which is the
// kind of number that gets a dashboard ignored.
func (m *RuntimeMetrics) FailureRate() float64 {
	concluded := m.CallsEnded.Total()
	if concluded == 0 {
		return 0
	}
	return float64(m.CallFailures.Total()) / float64(concluded)
}

// ShedRate returns shed calls over all admission decisions, or zero when none.
//
// The number that says whether capacity is sized correctly. A non-zero shed
// rate is not automatically wrong — shedding is the designed response to
// overload — but a rate that is non-zero at ordinary volume means the ceiling
// is too low.
func (m *RuntimeMetrics) ShedRate() float64 {
	admitted, shed := m.Admitted.Total(), m.Shed.Total()
	total := admitted + shed
	if total == 0 {
		return 0
	}
	return float64(shed) / float64(total)
}

// RecoveryRate returns resumed recoveries over all attempts, or zero when none.
func (m *RuntimeMetrics) RecoveryRate() float64 {
	total := m.RecoveryAttempts.Total()
	if total == 0 {
		return 0
	}
	return float64(m.RecoveryAttempts.PrefixSum("resumed")) / float64(total)
}
