package speech

import (
	metrics "github.com/callscreen/callscreen-platform/packages/go/metrics"
)

// The instrument types are ALIASES of the shared implementations, so a caller
// holding a *speech.Counter is holding exactly a *metrics.Counter and one
// exporter reads this subsystem alongside every other. Phase 10.5 owns the
// implementation; this package owns only which instruments exist.
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

// latencyBuckets are seconds, chosen around the frozen budget.
//
// ADR-0011 allocates 120/250 ms to STT final, 90/180 ms to TTS first byte,
// 15/40 ms to segmentation and 20 ms to barge-in. Buckets straddle each of
// those so a histogram can answer "are we inside budget" directly rather than
// by interpolation.
var latencyBuckets = []float64{
	0.005, 0.010, 0.020, 0.040, 0.060, 0.090, 0.120, 0.180,
	0.250, 0.400, 0.600, 0.900, 1.500, 2.500,
}

// SpeechMetrics is the speech engine's instrument set.
//
// Runtime-scoped, not global: owned by a [SpeechRuntime], two runtimes in one
// process share nothing, and the test suite is parallel-safe as a result.
//
// # Every label is bounded, and that is a privacy control
//
// Labels are enum values (language, reason, stage, queue, outcome) or authored
// identifiers (ProviderID). No transcript text, no caller identifier and no
// free-form provider output ever becomes a label. An unbounded label here would
// be both a cardinality explosion and a disclosure of what somebody said — the
// metric backend is not a system anyone consented to store their words in.
type SpeechMetrics struct {
	// Sessions
	SessionsActive *Gauge
	SessionsOpened *Counter // language
	SessionsClosed *Counter // reason
	STTStreams     *Gauge
	TTSStreams     *Gauge

	// Transcripts
	PartialsReceived   *Counter // language
	FinalsReceived     *Counter // language
	AssemblyRejections *Counter // reason

	// Providers
	ProviderFailures *Counter // provider, outcome
	ProviderSwitches *Counter // from, to
	CircuitOpens     *Counter // provider

	// Control
	Interruptions      *Counter
	Cancellations      *Counter // stage
	BackpressureEvents *Counter // queue
	QueueDepth         *Gauge

	// Latency, in SECONDS, matching every other subsystem in the platform.
	FirstPartialLatency    *Histogram // language
	FinalTranscriptLatency *Histogram // language
	FirstAudioLatency      *Histogram // provider
	InterruptLatency       *Histogram
}

// NewSpeechMetrics builds the instrument set.
func NewSpeechMetrics() *SpeechMetrics {
	return &SpeechMetrics{
		SessionsActive: metrics.NewGauge("speech_sessions_active"),
		SessionsOpened: metrics.NewCounter("speech_sessions_opened_total", "language"),
		SessionsClosed: metrics.NewCounter("speech_sessions_closed_total", "reason"),
		STTStreams:     metrics.NewGauge("speech_stt_streams_active"),
		TTSStreams:     metrics.NewGauge("speech_tts_streams_active"),

		PartialsReceived:   metrics.NewCounter("speech_partials_received_total", "language"),
		FinalsReceived:     metrics.NewCounter("speech_finals_received_total", "language"),
		AssemblyRejections: metrics.NewCounter("speech_assembly_rejections_total", "reason"),

		ProviderFailures: metrics.NewCounter("speech_provider_failures_total", "provider", "outcome"),
		ProviderSwitches: metrics.NewCounter("speech_provider_switches_total", "from", "to"),
		CircuitOpens:     metrics.NewCounter("speech_circuit_opens_total", "provider"),

		Interruptions:      metrics.NewCounter("speech_interruptions_total"),
		Cancellations:      metrics.NewCounter("speech_cancellations_total", "stage"),
		BackpressureEvents: metrics.NewCounter("speech_backpressure_total", "queue"),
		QueueDepth:         metrics.NewGauge("speech_queue_depth"),

		FirstPartialLatency:    metrics.NewHistogram("speech_first_partial_seconds", latencyBuckets, "language"),
		FinalTranscriptLatency: metrics.NewHistogram("speech_final_transcript_seconds", latencyBuckets, "language"),
		FirstAudioLatency:      metrics.NewHistogram("speech_first_audio_seconds", latencyBuckets, "provider"),
		InterruptLatency:       metrics.NewHistogram("speech_interrupt_seconds", latencyBuckets),
	}
}
