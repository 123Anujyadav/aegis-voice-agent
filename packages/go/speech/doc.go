// Package speech implements the Aegis AI speech pipeline and STT/TTS
// orchestration.
//
// # What this is
//
// The layer between media transport and language. A [SpeechRuntime] owns
// [SpeechSession] instances; each session runs an [STTOrchestrator] that turns
// inbound audio frames into an ordered transcript, and a [TTSOrchestrator] that
// turns response text into outbound audio frames.
//
//	media.Frame in → STT → partial → final → [TranscriptSink]
//	                                              ↓
//	[ResponseSource] → chunk → TTS → media.Frame out
//
// # What this is not
//
// There is no speech recognition and no speech synthesis in this package. There
// is no model, no inference runtime, no acoustic code, no resampler and no
// voice activity detection. There is no SIP, no RTP, no WebRTC and no carrier.
// There is no LLM.
//
// This package ORCHESTRATES recognition and synthesis performed elsewhere. It
// decides which provider to use, what order results arrive in, what a final
// transcript means, when to stop talking, and what happens when a provider
// fails. It never decides what a word is.
//
// # Provider agnosticism is the whole point
//
// [STTProvider] and [TTSProvider] name no vendor. No type in this package holds
// a vendor request, a vendor response, a raw JSON blob, a map[string]any or an
// API key. A provider is replaceable without changing conversation logic, the
// telephony runtime, the media runtime, governance, memory or evaluation —
// and the way that is kept true is that none of those appear here either.
//
// No Google Speech, Deepgram, OpenAI, Anthropic, ElevenLabs, Cartesia, Sarvam,
// Whisper or Piper SDK is imported. Not "not yet" — provider integrations live
// behind adapters in a later phase, and this package's job is to make that
// replacement uninteresting.
//
// # It does not import packages/go/conversation
//
// A speech session is created FOR a conversation, but the speech layer does not
// need to know what a conversation is. It needs somewhere to send a final
// transcript and somewhere to get response text, which are two interfaces.
//
// Coupling them would make the speech core untestable without a conversation
// engine and would put dialogue vocabulary into an audio pipeline. Note in
// particular that conversation already owns TurnManager (the dialogue floor)
// and InterruptionEngine (interruption semantics and resume policy). This
// package's [SpeechTurnManager] is a different layer — the audio lifecycle of
// one utterance — and a service composes the two.
//
// # Frame payloads are borrowed
//
// [media.Frame] payloads are sub-slices of a ring buffer that is overwritten as
// it wraps. Every component here that retains a frame past the call that
// received it clones first. The STT input adapter is the boundary where
// retention begins, and it clones on entry. This is the sharpest edge inherited
// from Phase 11B.
//
// # No implicit transitions
//
// A speech turn is in exactly one of nine states, and every legal move is
// declared in [turnTransitions]. A transition not declared there is refused; a
// malformed table is refused at construction by runtime.NewFSM. This mirrors
// Phases 11A and 11B for the same reason: a switch statement encodes
// transitions in the places that perform them, so "can a cancelled turn speak"
// is answered by reading every call site.
//
// # Determinism
//
// Every clock is injected. Provider timeouts, circuit cooldowns, latency
// measurements and barge-in timing all measure against runtime.Clock, so a test
// advances a FakeClock and observes a provider timeout in microseconds without
// sleeping.
//
// # The latency budget is frozen, not invented here
//
// ADR-0011 allocates the end-to-end budget and this package is measured against
// it, never against a target of its own choosing:
//
//   - STT final after end-of-speech: 120 ms p50 / 250 ms p95 (ADR-0005)
//   - Sentence segmentation, first speakable clause: 15 ms p50 / 40 ms p95
//   - TTS time-to-first-byte: 90 ms p50 / 180 ms p95 (ADR-0007)
//   - Barge-in: 20 ms, one frame interval (ADR-0004 §12)
//
// Only segmentation and barge-in are hops this package owns end to end. The
// other two are dominated by a provider network call this package does not
// make, so what is measurable here is orchestration overhead — reported as
// that, and never presented as the hop total. See docs/speech/PERFORMANCE.md.
package speech
