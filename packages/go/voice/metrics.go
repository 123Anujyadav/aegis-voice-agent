package voice

import (
	metrics "github.com/callscreen/callscreen-platform/packages/go/metrics"
)

// The instrument types are ALIASES of the shared implementations, so one
// exporter reads this subsystem alongside every other. Phase 10.5 spent a phase
// removing private copies of these; this package does not add another.
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

// VoiceMetrics is the voice runtime's instrument set.
//
// Runtime-scoped, not global: two runtimes in one process share nothing, and
// the test suite is parallel-safe as a result.
//
// # Every label is a bounded enum, and the dangerous ones are the tempting ones
//
// A phone number would be genuinely useful for debugging one caller's bad
// audio, and would put subscriber PII into a system with no erasure path. A
// transcript would do the same for what they said. A model's raw output would
// let a provider choose our metric cardinality.
//
// So: no phone number, no transcript, no response text, no session identifier,
// no credential. Per-session detail lives in [SessionStats], pulled on demand.
type VoiceMetrics struct {
	// Sessions and turns.
	SessionsOpened *Counter // reason
	SessionsClosed *Counter // reason
	SessionsActive *Gauge
	TurnsStarted   *Counter
	TurnsCompleted *Counter // outcome

	// State machine.
	StateTransitions   *Counter // from, to
	InvalidTransitions *Counter // from, to

	// Latency. THE NUMBERS THIS PHASE EXISTS TO MAKE VISIBLE.
	//
	// Each is a separate instrument rather than one labelled histogram, because
	// they have genuinely different scales — a first partial is tens of
	// milliseconds and a local model's first token can be seconds — and one
	// bucket set would put most observations in one bucket.
	STTFirstPartial *Histogram // provider
	STTFinal        *Histogram // provider
	ModelFirstToken *Histogram // provider
	TTSFirstAudio   *Histogram // provider
	TurnLatency     *Histogram
	BargeInCancel   *Histogram

	// Providers.
	ProviderCalls    *Counter // provider, kind, outcome
	ProviderFailures *Counter // provider, kind, reason
	ProviderSwitches *Counter // kind
	ProviderRestarts *Counter // provider
	ProcessFailures  *Counter // provider, reason
	ProviderUp       *Gauge

	// Flow control.
	QueueDepth         *Gauge   // the deepest queue currently held
	Backpressure       *Counter // stage
	DroppedChunks      *Counter // stage, reason
	StaleChunksBlocked *Counter // the barge-in correctness counter

	// Governance.
	GovernanceDecisions *Counter // outcome

	reg *metrics.Registry
}

// NewVoiceMetrics constructs an instrument set.
func NewVoiceMetrics() *VoiceMetrics {
	m := &VoiceMetrics{reg: metrics.NewRegistry()}

	// Bucket sets chosen from what THIS domain does. Phase 10.5 established
	// that boundaries are a domain decision and must not be standardised across
	// subsystems.

	// Recognition and synthesis on a local CPU: tens of milliseconds to a
	// minute. The top buckets exist because a batch recogniser genuinely lands
	// there, and hiding that in an overflow bucket would hide the main finding.
	providerSecs := []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60}

	// A local model's first token. THE 0.25 AND 0.55 EDGES ARE ADR-0011 hop 6's
	// p50 and p95 — present so a reader can see how far a local model sits from
	// the production reference, NOT because this phase is held to them.
	firstTokenSecs := []float64{0.05, 0.1, 0.25, 0.55, 1, 2, 5, 10, 30, 60, 120}

	// A whole turn.
	turnSecs := []float64{0.5, 1, 2, 5, 10, 20, 30, 60, 120}

	// Barge-in cancellation. THE 0.02 EDGE IS ADR-0004 §12's frozen budget,
	// which this phase IS held to.
	cancelSecs := []float64{1e-5, 1e-4, 1e-3, 0.005, 0.01, 0.02, 0.05, 0.1, 0.5}

	m.SessionsOpened = m.reg.Counter("voice_sessions_opened_total", "reason")
	m.SessionsClosed = m.reg.Counter("voice_sessions_closed_total", "reason")
	m.SessionsActive = m.reg.Gauge("voice_sessions_active")
	m.TurnsStarted = m.reg.Counter("voice_turns_started_total")
	m.TurnsCompleted = m.reg.Counter("voice_turns_completed_total", "outcome")

	m.StateTransitions = m.reg.Counter("voice_state_transitions_total", "from", "to")
	m.InvalidTransitions = m.reg.Counter("voice_invalid_transitions_total", "from", "to")

	m.STTFirstPartial = m.reg.Histogram("voice_stt_first_partial_seconds",
		providerSecs, "provider")
	m.STTFinal = m.reg.Histogram("voice_stt_final_seconds", providerSecs, "provider")
	m.ModelFirstToken = m.reg.Histogram("voice_model_first_token_seconds",
		firstTokenSecs, "provider")
	m.TTSFirstAudio = m.reg.Histogram("voice_tts_first_audio_seconds",
		providerSecs, "provider")
	m.TurnLatency = m.reg.Histogram("voice_turn_seconds", turnSecs)
	m.BargeInCancel = m.reg.Histogram("voice_barge_in_cancel_seconds", cancelSecs)

	m.ProviderCalls = m.reg.Counter("voice_provider_calls_total",
		"provider", "kind", "outcome")
	m.ProviderFailures = m.reg.Counter("voice_provider_failures_total",
		"provider", "kind", "reason")
	m.ProviderSwitches = m.reg.Counter("voice_provider_switches_total", "kind")
	m.ProviderRestarts = m.reg.Counter("voice_provider_restarts_total", "provider")
	m.ProcessFailures = m.reg.Counter("voice_process_failures_total",
		"provider", "reason")
	m.ProviderUp = m.reg.Gauge("voice_providers_up")

	m.QueueDepth = m.reg.Gauge("voice_queue_depth")
	m.Backpressure = m.reg.Counter("voice_backpressure_total", "stage")
	m.DroppedChunks = m.reg.Counter("voice_dropped_chunks_total", "stage", "reason")
	m.StaleChunksBlocked = m.reg.Counter("voice_stale_chunks_blocked_total")

	m.GovernanceDecisions = m.reg.Counter("voice_governance_decisions_total", "outcome")

	return m
}

// Registry exposes the instrument registry so a service can export this
// subsystem alongside its own through one scrape.
func (m *VoiceMetrics) Registry() *metrics.Registry { return m.reg }

// Snapshot returns every series, in a stable order.
func (m *VoiceMetrics) Snapshot() []Sample { return m.reg.Snapshot() }

// TurnSuccessRate returns completed turns over all turns that finished.
//
// Returns zero when nothing has finished, rather than a division by zero that a
// dashboard renders as a total outage.
func (m *VoiceMetrics) TurnSuccessRate() float64 {
	total := m.TurnsCompleted.Total()
	if total == 0 {
		return 0
	}
	return float64(m.TurnsCompleted.PrefixSum(string(OutcomeCompleted))) / float64(total)
}

// ProviderFailureRate returns failed provider calls over all provider calls.
func (m *VoiceMetrics) ProviderFailureRate() float64 {
	total := m.ProviderCalls.Total()
	if total == 0 {
		return 0
	}
	return float64(m.ProviderFailures.Total()) / float64(total)
}
