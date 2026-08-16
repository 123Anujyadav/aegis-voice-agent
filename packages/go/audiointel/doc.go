// Package audiointel implements the Aegis AI real-time audio intelligence
// engine.
//
// # What this is
//
// The layer between media transport and speech orchestration. It receives
// [media.Frame] values from Phase 11B, measures bounded acoustic features, and
// emits provider-neutral intelligence signals — voice activity, speech onset
// and offset, endpoints, barge-in, overlap, silence classification, noise and
// audio quality — for Phase 11C to act on.
//
//	media.Frame in → FrameAnalyzer → NoiseAnalyzer → SpeechDetector
//	                                                      ↓
//	         EndpointDetector ← SignalAnalyzer ← VAD state machine
//	                ↓                                     ↓
//	         SpeechController                      BargeInDetector
//	         (Phase 11C port)                      OverlapDetector
//
// # What this is not
//
// There is no speech recognition, no synthesis, no transport, and no language
// understanding. There is no SIP, no RTP, no WebRTC and no carrier. There is no
// STT provider, no TTS provider and no LLM. There is no fraud detection, no
// emergency detection, no call screening, no memory retrieval and no governance
// policy. Nothing here writes audio to disk.
//
// Above all there is no third-party voice activity detector. No WebRTC VAD, no
// Silero, no Pion, no LiveKit, no Agora, no Deepgram, Google, AssemblyAI or
// ElevenLabs voice-activity code — wrapped, vendored, ported or otherwise. The
// algorithms are written out in arithmetic a reader can check against
// docs/audio-intelligence/VAD_ARCHITECTURE.md.
//
// # Every decision is explainable, and that is a hard requirement
//
// A voice agent that cannot say why it decided somebody stopped talking cannot
// be debugged when it is wrong, and it will be wrong — on a noisy line, on a
// speaker who pauses mid-sentence, on a language whose rhythm the thresholds
// were not tuned for. Every [Decision] this package produces carries an
// [Explanation] naming which measured features crossed which configured
// thresholds and by what margin.
//
// No opaque model sits in the hot path. [SpeechLikelihoodModel] declares the
// seam a future learned detector would occupy; nothing implements it in this
// phase and nothing calls it.
//
// # Frame payloads are borrowed, and no PCM leaves Analyze
//
// [media.Frame] payloads are sub-slices of a ring buffer that is overwritten as
// it wraps. This package reads them in place inside [Session.Analyze] and
// retains only scalars. No sample, no payload and no reference to one is stored
// in session state, published in an event, recorded in a metric or written to a
// log. TestAudioEvent_CarriesNoAudio enforces the event half by reflection, so
// a later field addition cannot quietly break it.
//
// This is the sharpest edge inherited from Phase 11B and the reason §24 of the
// phase brief is satisfied by construction rather than by policy.
//
// # Synchronous, caller-driven, no goroutines on the hot path
//
// [Session.Analyze] runs inline on the caller's goroutine and returns a
// decision. There is no channel, no queue and no background worker between
// detection and the [SpeechController] call.
//
// ADR-0004 §12 requires barge-in to interrupt agent audio within one frame
// interval and states plainly that any queue between the detector and the
// output is added interruption latency. A per-session goroutine would insert
// exactly that queue. It would also make deterministic replay depend on
// goroutine scheduling, which is the difference between a test that proves
// something and a test that usually passes.
//
// Phase 11B established the same convention — its runtime pump is off by
// default in tests and the consumer drives the engine at its own cadence.
//
// # No implicit transitions
//
// Voice activity is in exactly one of six states and every legal move is
// declared in [vadTransitions]. The overlap detector has its own four-state
// table. A transition not declared is refused; a malformed table is refused at
// construction by runtime.NewFSM. This mirrors Phases 11A, 11B and 11C for the
// same reason: a switch statement encodes transitions in the places that
// perform them, so "can speech end from CandidateSpeech" is answered by reading
// every call site.
//
// # Determinism
//
// Every clock is injected. Hangover windows, noise adaptation intervals,
// endpoint silence windows and barge-in measurements all measure against
// runtime.Clock, so a test advances a FakeClock and observes a 250 ms endpoint
// in microseconds without sleeping. The same frame sequence produces the same
// decision sequence, and TestDeterminism_ReplayProducesIdenticalDecisions
// checks that directly rather than sampling.
//
// # The latency budget is frozen, not invented here
//
// ADR-0011 allocates the end-to-end budget and this package is measured against
// it, never against a target of its own choosing:
//
//   - Endpoint detection — silence window before a turn is declared ended:
//     250 ms p50 / 350 ms p95 (ADR-0011 §5.2 hop 1, explicitly ours)
//   - Barge-in — detection to outbound silence: 20 ms, one frame interval
//     (ADR-0004 §12, ADR-0011 §5.1)
//
// No frozen target exists for per-frame analysis, VAD decision latency, noise
// adaptation or quality classification. Those are measured and reported as
// measurements. This phase creates no new contractual SLA. See
// docs/audio-intelligence/PERFORMANCE.md.
//
// # It does not import packages/go/speech
//
// Phase 11D provides signals TO Phase 11C, and the data flows
// media → audiointel → speech. But the import direction in this repository runs
// the other way — the higher layer imports the lower — and packages/go/speech
// is frozen, so it cannot be made to import this.
//
// The resolution is a port: this package declares [SpeechController] and calls
// it, and packages/go/audiobridge implements that port over
// *speech.SpeechSession. That is the only place the two meet, and it keeps
// `go list -deps` on this module free of speech, conversation, governance,
// memory, toolruntime and every provider SDK.
package audiointel
