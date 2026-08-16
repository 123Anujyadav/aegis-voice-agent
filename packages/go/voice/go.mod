// =============================================================================
// packages/go/voice — Local Voice AI Provider Integration and the end-to-end
// voice loop.
//
// EIGHT FIRST-PARTY DEPENDENCIES, NO THIRD-PARTY ONES.
//
// This module orchestrates the layers below it into a working voice turn:
// media frames in, audio intelligence signals, recognition, a conversation
// plan, a governance decision, generation, synthesis, audio out. Every one of
// those is somebody else's frozen contract and this module owns none of them.
//
// THERE IS NO CLOUD SDK HERE, AND THERE IS NO API KEY.
//
// No Google, Deepgram, Sarvam, ElevenLabs, Cartesia, OpenAI or Anthropic SDK.
// Phase 11E is a LOCAL-FIRST phase: the first working implementation runs
// against processes on the developer's own machine and requires no paid
// credential of any kind. Cloud adapters are a later phase and will be written
// against exactly the same ports these local ones implement — which is the
// whole point of putting them behind ports.
//
// THE PROVIDER ADAPTERS DEPEND ON LESS THAN THIS MODULE DOES.
//
// providers/whispercpp, providers/whispercli and providers/piper import
// packages/go/speech and the standard library. providers/ollama imports
// packages/go/runtime and the standard library. None of them imports this
// package, conversation, governance or each other.
//
// That is checked per PACKAGE rather than per MODULE, because a module's
// dependency list is the union of every package in it and would hide exactly
// the coupling worth preventing. See TestDependencies_AdaptersImportOnlyTheirPort.
//
// WHY conversation AND governance ARE IMPORTED DIRECTLY.
//
// Phase 11D needed packages/go/audiobridge because audiointel had to sit BELOW
// frozen speech, and importing it would have inverted the layering. Nothing of
// the sort applies here: voice sits ABOVE conversation and governance in the
// data flow, so importing them is the ordinary direction — the same direction
// in which speech imports media. A bridge module would be ceremony without a
// reason.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/voice

go 1.25.0

require (
	github.com/callscreen/callscreen-platform/packages/go/audiobridge v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/audiointel v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/conversation v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/governance v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/media v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/speech v0.0.0
)

// The module paths above are not fetchable remotes: this monorepo is private
// and unpublished. The relative replaces also keep this module buildable
// standalone with GOWORK=off, which CI relies on to prove the go.mod is
// self-sufficient rather than leaning on the workspace.
replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../runtime

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics

replace github.com/callscreen/callscreen-platform/packages/go/media => ../media

replace github.com/callscreen/callscreen-platform/packages/go/speech => ../speech

replace github.com/callscreen/callscreen-platform/packages/go/audiointel => ../audiointel

replace github.com/callscreen/callscreen-platform/packages/go/audiobridge => ../audiobridge

replace github.com/callscreen/callscreen-platform/packages/go/conversation => ../conversation

replace github.com/callscreen/callscreen-platform/packages/go/governance => ../governance
