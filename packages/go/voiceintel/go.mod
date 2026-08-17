// =============================================================================
// packages/go/voiceintel — the composition root that wires intent into voice.
//
// WHY A SEPARATE MODULE
//
// Three facts force it:
//
//  1. packages/go/intent must not widen its dependencies. Its declared set is
//     conversation (plus runtime and metrics transitively), and a Go module's
//     requires apply to its tests too — so an integration test that imports
//     voice cannot live there without widening intent's graph.
//  2. voice and conversation are FROZEN. Neither may gain a wiring helper.
//  3. There is no existing production composition root for the AI plane. Phase
//     12 T8 established that no service imports any AI-plane module;
//     conversation.NewEngine appears exactly once in the repository, in
//     voice/e2e_test.go, and without WithClassifier.
//
// So the wiring has to live somewhere new, and this is the smallest somewhere:
// a leaf module that imports the three packages it joins and is imported by
// nothing.
//
// WHAT IT IS NOT. It adds no planner, no intent engine, no FSM, no context
// engine, no governance path and no tool path. It calls
// conversation.WithClassifier — an injection point that already existed and was
// never used in production — and hands the resulting *conversation.Conversation
// to voice through voice.Planner, an interface voice already declares
// conversation satisfies (pipeline.go:123).
//
// DEPENDENCIES: voice, conversation, intent, and what those pull in. No HTTP,
// no provider SDK, no model runtime, no database, no third-party module.
//
// See docs/adr/0016-intent-classification.md.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/voiceintel

go 1.25.0

require (
	github.com/callscreen/callscreen-platform/packages/go/conversation v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/intent v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/voice v0.0.0
)

require (
	github.com/callscreen/callscreen-platform/packages/go/audiobridge v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/audiointel v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/governance v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/media v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/speech v0.0.0 // indirect
)

replace github.com/callscreen/callscreen-platform/packages/go/audiobridge => ../audiobridge

replace github.com/callscreen/callscreen-platform/packages/go/audiointel => ../audiointel

replace github.com/callscreen/callscreen-platform/packages/go/conversation => ../conversation

replace github.com/callscreen/callscreen-platform/packages/go/governance => ../governance

replace github.com/callscreen/callscreen-platform/packages/go/intent => ../intent

replace github.com/callscreen/callscreen-platform/packages/go/media => ../media

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics

replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../runtime

replace github.com/callscreen/callscreen-platform/packages/go/speech => ../speech

replace github.com/callscreen/callscreen-platform/packages/go/voice => ../voice
