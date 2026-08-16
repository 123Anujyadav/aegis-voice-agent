package media

import (
	metrics "github.com/callscreen/callscreen-platform/packages/go/metrics"
)

// The instrument types are ALIASES of the shared implementations, so a caller
// holding a *media.Counter is holding exactly a *metrics.Counter and one
// exporter reads this subsystem alongside every other.
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

// MediaMetrics is the media engine's instrument set.
//
// Runtime-scoped, not global: owned by a [MediaRuntime], two runtimes in one
// process share nothing, and the test suite is parallel-safe as a result.
//
// # The instruments are deliberately sparse on the frame path
//
// At fifty thousand frames per second, a counter increment per frame per label
// dimension is real cost — and worse, a label whose cardinality tracks stream
// count would produce fifty thousand time series. Frame-level counters here are
// labelled by DIRECTION and REASON only, both bounded enums. Per-stream detail
// lives in [StreamStats], which is pulled on demand rather than pushed on every
// frame.
type MediaMetrics struct {
	// Streams
	StreamsOpened *Counter // direction, source
	StreamsClosed *Counter // direction, outcome, reason
	StreamsFailed *Counter // source, reason
	Recoveries    *Counter // outcome
	LiveStreams   *Gauge

	// State machine
	Transitions        *Counter // from, to
	InvalidTransitions *Counter // from, to

	// Frames — the hot path. Bounded labels only.
	FramesAccepted  *Counter // direction
	FramesDelivered *Counter // direction
	FramesDropped   *Counter // direction, reason
	FramesGapFilled *Counter // direction
	BytesMoved      *Counter // direction

	// Buffers
	BufferDepth       *Histogram // direction — in seconds of audio
	BufferUtilisation *Gauge
	Overflows         *Counter // direction
	Underflows        *Counter // direction

	// Jitter and timing
	Jitter        *Histogram // direction — seconds
	JitterDelay   *Histogram // direction — seconds
	FrameLateness *Histogram // direction — seconds
	DriftRatio    *Gauge
	Reordered     *Counter // direction

	// Pipeline
	PipelineLatency *Histogram // stage
	PumpBatch       *Histogram

	// Lifecycle
	StreamDuration *Histogram // direction, outcome

	reg *metrics.Registry
	// byState holds one gauge per state, built once.
	byState map[StreamState]*Gauge
}

// NewMediaMetrics constructs an instrument set.
func NewMediaMetrics() *MediaMetrics {
	m := &MediaMetrics{reg: metrics.NewRegistry()}

	// Bucket sets chosen from what audio actually does, which is a different
	// scale again from telephony. Phase 10.5 established that bucket boundaries
	// are a DOMAIN decision and must not be standardised across subsystems; a
	// shared set would put every frame observation in one bucket.

	// Jitter and lateness: sub-millisecond to a quarter second. Beyond 250 ms a
	// conversation is unusable, so the top bucket is "broken" rather than a
	// value worth resolving.
	jitterSecs := []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.02, 0.04,
		0.06, 0.1, 0.15, 0.25}

	// Buffer depth: one frame to five seconds. A buffer deeper than a second is
	// already a latency problem; the upper buckets exist to show how bad.
	depthSecs := []float64{0.02, 0.04, 0.06, 0.1, 0.2, 0.5, 1, 2, 5}

	// Per-stage pipeline work: microseconds. The floor sits below what a
	// Windows clock can resolve (~520 µs, per Phase 10F) so the overflow bucket
	// is where the truth lives there, and it is visible rather than silently
	// reported as zero.
	stageSecs := []float64{1e-7, 5e-7, 1e-6, 5e-6, 1e-5, 5e-5, 1e-4, 1e-3, 0.01}

	// Whole streams: seconds to an hour.
	streamSecs := []float64{1, 5, 15, 30, 60, 180, 600, 1800, 3600}

	// Pump batch size: how many frames one pump released.
	batchSizes := []float64{1, 2, 3, 5, 8, 13, 21, 50}

	m.StreamsOpened = m.reg.Counter("media_streams_opened_total", "direction", "source")
	m.StreamsClosed = m.reg.Counter("media_streams_closed_total", "direction", "outcome", "reason")
	m.StreamsFailed = m.reg.Counter("media_streams_failed_total", "source", "reason")
	m.Recoveries = m.reg.Counter("media_recoveries_total", "outcome")
	m.LiveStreams = m.reg.Gauge("media_streams_live")

	m.Transitions = m.reg.Counter("media_transitions_total", "from", "to")
	m.InvalidTransitions = m.reg.Counter("media_invalid_transitions_total", "from", "to")

	m.FramesAccepted = m.reg.Counter("media_frames_accepted_total", "direction")
	m.FramesDelivered = m.reg.Counter("media_frames_delivered_total", "direction")
	m.FramesDropped = m.reg.Counter("media_frames_dropped_total", "direction", "reason")
	m.FramesGapFilled = m.reg.Counter("media_frames_gap_filled_total", "direction")
	m.BytesMoved = m.reg.Counter("media_bytes_moved_total", "direction")

	m.BufferDepth = m.reg.Histogram("media_buffer_depth_seconds", depthSecs, "direction")
	m.BufferUtilisation = m.reg.Gauge("media_buffer_utilisation")
	m.Overflows = m.reg.Counter("media_buffer_overflows_total", "direction")
	m.Underflows = m.reg.Counter("media_buffer_underflows_total", "direction")

	m.Jitter = m.reg.Histogram("media_jitter_seconds", jitterSecs, "direction")
	m.JitterDelay = m.reg.Histogram("media_jitter_delay_seconds", jitterSecs, "direction")
	m.FrameLateness = m.reg.Histogram("media_frame_lateness_seconds", jitterSecs, "direction")
	m.DriftRatio = m.reg.Gauge("media_drift_ratio")
	m.Reordered = m.reg.Counter("media_frames_reordered_total", "direction")

	m.PipelineLatency = m.reg.Histogram("media_pipeline_seconds", stageSecs, "stage")
	m.PumpBatch = m.reg.Histogram("media_pump_batch_frames", batchSizes)

	m.StreamDuration = m.reg.Histogram("media_stream_seconds", streamSecs, "direction", "outcome")

	m.byState = make(map[StreamState]*Gauge, len(AllStates()))
	for _, s := range AllStates() {
		m.byState[s] = m.reg.Gauge("media_streams_state_" + string(s))
	}

	return m
}

// Registry exposes the underlying instrument registry, so a service can export
// this subsystem alongside its own through one scrape.
func (m *MediaMetrics) Registry() *metrics.Registry { return m.reg }

// Snapshot returns every series, in a stable order.
func (m *MediaMetrics) Snapshot() []Sample { return m.reg.Snapshot() }

// ObserveStates sets the per-state gauges from a registry census.
//
// Takes the counts rather than the registry, so the caller decides when to pay
// for the walk. The sweeper calls this once per interval; nothing calls it on
// the frame path.
func (m *MediaMetrics) ObserveStates(counts map[StreamState]int) {
	for _, s := range AllStates() {
		if g, ok := m.byState[s]; ok {
			// Every state is set, including zeroes. A gauge that stops being
			// reported when its population empties leaves a dashboard showing
			// the last non-zero value forever.
			g.Set(float64(counts[s]))
		}
	}
}

// DropRate returns dropped frames over frames offered, or zero when none.
func (m *MediaMetrics) DropRate() float64 {
	accepted := m.FramesAccepted.Total()
	dropped := m.FramesDropped.Total()
	offered := accepted + dropped
	if offered == 0 {
		return 0
	}
	return float64(dropped) / float64(offered)
}

// DeliveryRate returns delivered frames over accepted, or zero when none.
//
// May exceed 1: gap-filled silence is delivered without ever being accepted,
// which is exactly what the number should show — a delivery rate above 1 means
// the engine is inventing audio to cover losses.
func (m *MediaMetrics) DeliveryRate() float64 {
	accepted := m.FramesAccepted.Total()
	if accepted == 0 {
		return 0
	}
	return float64(m.FramesDelivered.Total()) / float64(accepted)
}

// RecoveryRate returns resumed recoveries over all attempts, or zero when none.
func (m *MediaMetrics) RecoveryRate() float64 {
	total := m.Recoveries.Total()
	if total == 0 {
		return 0
	}
	return float64(m.Recoveries.PrefixSum("resumed")) / float64(total)
}
