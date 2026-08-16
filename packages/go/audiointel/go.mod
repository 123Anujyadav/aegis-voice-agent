// =============================================================================
// packages/go/audiointel — the Real-Time Audio Intelligence Engine.
//
// THREE FIRST-PARTY DEPENDENCIES, NO THIRD-PARTY ONES.
//
// runtime (Phase 10A) for the injected clock and the FSM, metrics (Phase 10.5)
// for instruments, media (Phase 11B) for the audio frame. All three are
// dependency-free, so the transitive closure of this module is the Go standard
// library — the property every module in this plane has held since Phase 10A.
//
// THERE IS NO VAD LIBRARY HERE, AND THAT IS THE POINT.
//
// No WebRTC VAD, Silero, Pion, LiveKit, Agora, Deepgram, Google, AssemblyAI or
// ElevenLabs voice-activity code — wrapped, vendored, ported or otherwise. No
// DSP library, no FFT package, no model, no inference runtime. The detection
// algorithms in this module are written out in arithmetic that a reader can
// check against the documentation, because a voice agent that cannot explain
// why it decided somebody stopped talking cannot be debugged when it is wrong.
//
// IT NOTABLY DOES NOT REQUIRE packages/go/speech, AND THAT IS DELIBERATE.
//
// Phase 11D provides audio intelligence signals TO Phase 11C. The data flows
// media -> audiointel -> speech, exactly as the phase brief specifies. But the
// IMPORT direction in this repository runs the other way — the higher layer
// imports the lower, which is why packages/go/speech imports packages/go/media
// — and packages/go/speech is frozen, so it cannot be made to import this.
//
// The resolution is a port. This module declares SpeechController and calls it;
// packages/go/audiobridge implements that port over *speech.SpeechSession and
// is the only place in the repository where audio intelligence and speech
// orchestration meet. That keeps `go list -deps` on THIS module provably free
// of speech, conversation, governance, memory, toolruntime and every provider
// SDK — which is what makes the dependency rule checkable rather than
// aspirational.
//
// WHAT THE RUNTIME DEPENDENCY BUYS. Clock (so every hangover, every noise
// adaptation interval and every barge-in measurement is testable without
// sleeping) and FSM (so the voice-activity state machine is declared and
// validated rather than assembled from booleans). Reimplementing either would
// produce a second, subtly different version of a solved problem.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/audiointel

go 1.25.0

require (
	github.com/callscreen/callscreen-platform/packages/go/media v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0
)

// The module paths above are not fetchable remotes: this monorepo is private
// and unpublished. The relative replaces also keep this module buildable
// standalone with GOWORK=off, which CI relies on to prove the go.mod is
// self-sufficient rather than leaning on the workspace.
replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../runtime

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics

replace github.com/callscreen/callscreen-platform/packages/go/media => ../media
