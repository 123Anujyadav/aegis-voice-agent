// =============================================================================
// packages/go/speech — the Enterprise Speech Pipeline & STT/TTS Orchestration.
//
// THREE FIRST-PARTY DEPENDENCIES, NO THIRD-PARTY ONES.
//
// runtime (Phase 10A) for the injected clock and the FSM, metrics (Phase 10.5)
// for instruments, media (Phase 11B) for the audio frame. All three are
// dependency-free, so the transitive closure of this module is the Go standard
// library — the property every module in this plane has held since Phase 10A.
//
// THERE IS NO SPEECH SDK HERE, AND THAT IS THE POINT.
//
// No Google Speech, Deepgram, OpenAI, Anthropic, ElevenLabs, Cartesia, Sarvam,
// Whisper or Piper. No model, no inference runtime, no acoustic code of any
// kind. This module ORCHESTRATES recognition and synthesis performed elsewhere,
// behind interfaces that name no vendor.
//
// IT NOTABLY DOES NOT REQUIRE packages/go/conversation EITHER.
//
// A speech session is created FOR a conversation, but the speech layer does not
// need to know what a conversation is — it needs a transcript sink and a
// response source. Coupling the two would make the speech core untestable
// without a conversation engine, and would put dialogue vocabulary into an
// audio pipeline. This is the same reasoning packages/go/media used to avoid
// depending on packages/go/telephony.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/speech

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
