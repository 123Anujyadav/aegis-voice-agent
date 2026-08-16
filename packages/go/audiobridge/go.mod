// =============================================================================
// packages/go/audiobridge — the Phase 11D ↔ Phase 11C adapter.
//
// THE ONLY PLACE IN THE REPOSITORY WHERE AUDIO INTELLIGENCE AND SPEECH
// ORCHESTRATION MEET, AND THAT IS THE ENTIRE POINT OF THE MODULE.
//
// Phase 11D's brief requires two things that cannot both be satisfied inside
// one module. §8 says barge-in must cancel synthesis "through the existing
// Phase 11C contract" and must not touch a TTS provider directly. §26 and §29
// say packages/go/audiointel must depend only on the Go standard library and
// first-party modules, and must not sit above packages/go/speech.
//
// The Phase 11C contract is speech.SpeechSession.Interrupt and
// speech.SpeechSession.EndOfSpeech. Naming those inside audiointel means
// importing packages/go/speech — and packages/go/speech is FROZEN, so it cannot
// be changed to depend on audiointel instead.
//
// So audiointel declares a port, audiointel.SpeechController, and this module
// implements it over a real *speech.SpeechSession. `go list -deps` on
// audiointel is therefore provably free of speech, conversation, governance,
// memory, toolruntime and every provider SDK, and the integration is real
// compiled code rather than a signature that happens to match.
//
// THIS MODULE IS DELIBERATELY TINY. It contains one adapter and its tests.
// Anything else belongs on one side of the boundary or the other.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/audiobridge

go 1.25.0

require (
	github.com/callscreen/callscreen-platform/packages/go/audiointel v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/media v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/speech v0.0.0
)

require (
	github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0 // indirect
)

// The module paths above are not fetchable remotes: this monorepo is private
// and unpublished. The relative replaces also keep this module buildable
// standalone with GOWORK=off.
replace github.com/callscreen/callscreen-platform/packages/go/audiointel => ../audiointel

replace github.com/callscreen/callscreen-platform/packages/go/speech => ../speech

replace github.com/callscreen/callscreen-platform/packages/go/media => ../media

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics

replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../runtime
