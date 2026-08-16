package audiointel

import (
	metrics "github.com/callscreen/callscreen-platform/packages/go/metrics"
)

// The instrument types are ALIASES of the shared implementations, so a caller
// holding an *audiointel.Counter is holding exactly a *metrics.Counter and one
// exporter reads this subsystem alongside every other.
//
// Phase 10.5 spent a phase undoing four private copies of these primitives.
// This package does not add a fifth.
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

// AudioIntelligenceMetrics is the audio intelligence engine's instrument set.
//
// Runtime-scoped, not global: owned by an [AudioIntelligenceRuntime], two
// runtimes in one process share nothing, and the test suite is parallel-safe as
// a result.
//
// # Every label here is a bounded enum, and that is not a style preference
//
// A metric label whose cardinality tracks call volume is the classic way to
// take down a metrics backend from application code. Worse, on this subsystem
// the tempting labels are the dangerous ones: a phone number would be perfect
// for debugging one caller's bad audio and would put subscriber PII into a
// system with no erasure path, and transcript content would do the same for
// what they said.
//
// So: no session identifier, no call identifier, no turn identifier, no phone
// number, no transcript. Per-session detail lives in [SessionStats], which is
// pulled on demand rather than pushed on every frame.
type AudioIntelligenceMetrics struct {
	// Sessions.
	SessionsOpened *Counter // direction
	SessionsClosed *Counter // reason
	SessionsActive *Gauge

	// Frame path — the hot path. Bounded labels only.
	FramesAnalysed *Counter // direction
	FramesRefused  *Counter // reason

	// Voice activity.
	VADDecisions     *Counter   // state
	VADTransitions   *Counter   // from, to
	SpeechStarts     *Counter   // direction
	SpeechEnds       *Counter   // direction
	FalseTriggers    *Counter   // reason
	SpeechDuration   *Histogram // seconds
	SilenceDuration  *Histogram // class, seconds
	SpeechConfidence *Histogram

	// Endpointing.
	EndpointCandidates *Counter   // direction
	EndpointConfirmed  *Counter   // direction
	EndpointSuppressed *Counter   // reason
	EndpointLatency    *Histogram // seconds — measured against ADR-0011 hop 1

	// Interruption.
	BargeIns        *Counter   // outcome
	BargeInLatency  *Histogram // seconds — measured against ADR-0004 §12
	Overlaps        *Counter   // state
	OverlapDuration *Histogram

	// Signal conditions.
	NoiseChanges   *Counter // direction
	NoiseFloorDBFS *Gauge
	QualityChanges *Counter // from, to
	Degradations   *Counter // reason
	Recoveries     *Counter // direction

	// Transport continuity.
	FrameGaps        *Counter // kind
	ContinuityEvents *Counter // kind

	// Latency of the engine's own work.
	FrameAnalysisLatency *Histogram // stage — seconds
	DecisionLatency      *Histogram // seconds

	// AnalysisBacklog is frames awaiting analysis.
	//
	// STRUCTURALLY ZERO, AND REPORTED ANYWAY. [Session.Analyze] runs inline on
	// the caller's goroutine and returns a decision; there is no queue between
	// detection and the speech controller, because ADR-0004 §247 says any queue
	// there is added interruption latency.
	//
	// The gauge exists for two reasons. A dashboard that expects a backlog
	// series should see a zero rather than a gap, since a missing series and a
	// healthy one look identical in most alerting configurations. And if a
	// future change ever does introduce a queue on this path, this is where it
	// must be reported — an obvious place beats a new metric nobody adds.
	AnalysisBacklog *Gauge

	reg *metrics.Registry
}

// NewAudioIntelligenceMetrics constructs an instrument set.
func NewAudioIntelligenceMetrics() *AudioIntelligenceMetrics {
	m := &AudioIntelligenceMetrics{reg: metrics.NewRegistry()}

	// Bucket sets chosen from what THIS domain actually does. Phase 10.5
	// established that bucket boundaries are a DOMAIN decision and must not be
	// standardised across subsystems; a shared set would put every observation
	// here in one bucket.

	// Per-frame engine work: microseconds. The floor sits below what a Windows
	// clock can resolve (~520 µs, per Phase 10F) so the overflow bucket is where
	// the truth lives there, and it is visible rather than silently zero.
	stageSecs := []float64{1e-7, 5e-7, 1e-6, 5e-6, 1e-5, 5e-5, 1e-4, 1e-3, 0.01}

	// Endpoint latency. THE 0.25 AND 0.35 EDGES ARE NOT ROUND NUMBERS — they
	// are the ADR-0011 §5.2 hop 1 p50 and p95 budgets. Putting bucket
	// boundaries exactly on the budget is what makes "are we inside hop 1"
	// answerable from a histogram rather than requiring an interpolation nobody
	// trusts.
	endpointSecs := []float64{0.05, 0.1, 0.15, 0.2, 0.25, 0.3, 0.35, 0.5, 1, 2}

	// Barge-in latency. THE 0.02 EDGE IS ADR-0004 §12's one-frame-interval
	// budget, for the same reason.
	bargeSecs := []float64{1e-6, 1e-5, 1e-4, 1e-3, 0.005, 0.01, 0.02, 0.05, 0.1}

	// Speech and silence runs: a syllable to a walked-away caller.
	spanSecs := []float64{0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 30}

	// Confidence, in [0,1]. Its distribution is how a deployment tells a
	// detector that is sure from one that is guessing and happens to be right.
	confidence := []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0}

	m.SessionsOpened = m.reg.Counter("audiointel_sessions_opened_total", "direction")
	m.SessionsClosed = m.reg.Counter("audiointel_sessions_closed_total", "reason")
	m.SessionsActive = m.reg.Gauge("audiointel_sessions_active")

	m.FramesAnalysed = m.reg.Counter("audiointel_frames_analysed_total", "direction")
	m.FramesRefused = m.reg.Counter("audiointel_frames_refused_total", "reason")

	m.VADDecisions = m.reg.Counter("audiointel_vad_decisions_total", "state")
	m.VADTransitions = m.reg.Counter("audiointel_vad_transitions_total", "from", "to")
	m.SpeechStarts = m.reg.Counter("audiointel_speech_starts_total", "direction")
	m.SpeechEnds = m.reg.Counter("audiointel_speech_ends_total", "direction")
	m.FalseTriggers = m.reg.Counter("audiointel_false_triggers_total", "reason")
	m.SpeechDuration = m.reg.Histogram("audiointel_speech_seconds", spanSecs)
	m.SilenceDuration = m.reg.Histogram("audiointel_silence_seconds", spanSecs, "class")
	m.SpeechConfidence = m.reg.Histogram("audiointel_speech_confidence", confidence)

	m.EndpointCandidates = m.reg.Counter("audiointel_endpoint_candidates_total", "direction")
	m.EndpointConfirmed = m.reg.Counter("audiointel_endpoint_confirmed_total", "direction")
	m.EndpointSuppressed = m.reg.Counter("audiointel_endpoint_suppressed_total", "reason")
	m.EndpointLatency = m.reg.Histogram("audiointel_endpoint_seconds", endpointSecs)

	m.BargeIns = m.reg.Counter("audiointel_barge_ins_total", "outcome")
	m.BargeInLatency = m.reg.Histogram("audiointel_barge_in_seconds", bargeSecs)
	m.Overlaps = m.reg.Counter("audiointel_overlaps_total", "state")
	m.OverlapDuration = m.reg.Histogram("audiointel_overlap_seconds", spanSecs)

	m.NoiseChanges = m.reg.Counter("audiointel_noise_changes_total", "direction")
	m.NoiseFloorDBFS = m.reg.Gauge("audiointel_noise_floor_dbfs")
	m.QualityChanges = m.reg.Counter("audiointel_quality_changes_total", "from", "to")
	m.Degradations = m.reg.Counter("audiointel_degradations_total", "reason")
	m.Recoveries = m.reg.Counter("audiointel_recoveries_total", "direction")

	m.FrameGaps = m.reg.Counter("audiointel_frame_gaps_total", "kind")
	m.ContinuityEvents = m.reg.Counter("audiointel_continuity_events_total", "kind")

	m.FrameAnalysisLatency = m.reg.Histogram("audiointel_frame_analysis_seconds",
		stageSecs, "stage")
	m.DecisionLatency = m.reg.Histogram("audiointel_decision_seconds", stageSecs)

	m.AnalysisBacklog = m.reg.Gauge("audiointel_analysis_backlog")

	return m
}

// Registry exposes the underlying instrument registry, so a service can export
// this subsystem alongside its own through one scrape.
func (m *AudioIntelligenceMetrics) Registry() *metrics.Registry { return m.reg }

// Snapshot returns every series, in a stable order.
func (m *AudioIntelligenceMetrics) Snapshot() []Sample { return m.reg.Snapshot() }

// FalseTriggerRate returns aborted onsets over all onset attempts.
//
// THE NUMBER THAT SAYS WHETHER THE THRESHOLDS ARE RIGHT. ADR-0011 §7 records
// that endpointing is tuned by measuring false-endpoint rate, not by minimising
// latency; this is the voice-activity equivalent, and a deployment retuning
// [VADConfig] watches it move.
//
// Returns zero when nothing has been attempted, rather than a division by zero
// that a dashboard renders as an outage.
func (m *AudioIntelligenceMetrics) FalseTriggerRate() float64 {
	aborted := m.FalseTriggers.Total()
	confirmed := m.SpeechStarts.Total()
	attempts := aborted + confirmed
	if attempts == 0 {
		return 0
	}
	return float64(aborted) / float64(attempts)
}

// EndpointConfirmRate returns confirmed endpoints over candidates.
//
// Well below 1 means the gates in [EndpointPolicy] are suppressing most
// candidates, which is either correct or a misconfiguration and is worth
// knowing either way.
func (m *AudioIntelligenceMetrics) EndpointConfirmRate() float64 {
	candidates := m.EndpointCandidates.Total()
	if candidates == 0 {
		return 0
	}
	return float64(m.EndpointConfirmed.Total()) / float64(candidates)
}

// BargeInDeliveryRate returns barge-ins that reached the speech controller over
// all detections.
//
// Below 1 by design: debounced and stale detections are deliberately not
// delivered. A rate near zero means the debounce or staleness window is
// suppressing real interruptions, which sounds to a caller like an agent that
// will not stop talking.
func (m *AudioIntelligenceMetrics) BargeInDeliveryRate() float64 {
	total := m.BargeIns.Total()
	if total == 0 {
		return 0
	}
	return float64(m.BargeIns.PrefixSum(string(BargeInDelivered))) / float64(total)
}
