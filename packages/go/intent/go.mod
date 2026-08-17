// =============================================================================
// packages/go/intent — an implementation of conversation.IntentClassifier.
//
// WHY THIS MODULE EXISTS
//
// packages/go/conversation owns the intent engine, the confidence thresholds,
// the clarification policy, the bounded context and the turn semantics. What it
// deliberately does NOT own is classification itself. Its own words, at
// intent.go:101:
//
//	THIS INTERFACE HAS NO IMPLEMENTATION IN THIS MODULE, DELIBERATELY.
//
// and at harness.go:94, of its ScriptedClassifier test double:
//
//	It is the ONLY implementation of [IntentClassifier] in this module
//
// So the machinery is complete and, in production, unreachable: with no
// classifier configured, conversation resolves every utterance to the fallback
// intent (intent.go:277). This module supplies the missing piece behind the
// EXISTING port. It adds no engine, no second intent model and no second
// context store. See docs/adr/0016-intent-classification.md.
//
// DEPENDENCY BOUNDARY. This module is a leaf. It requires packages/go/
// conversation for the port and its types, and nothing else first-party beyond
// what conversation itself pulls in (runtime, metrics).
//
// It must NEVER import governance, toolruntime, memory, speech, media,
// audiobridge, any voice provider, any server or CLI package, or any
// third-party module. The classifier proposes meaning; it decides nothing and
// executes nothing, and that is enforced structurally by this import set rather
// than by convention. Governance remains the single decision boundary.
//
// NO MODEL, NO NETWORK, NO CREDENTIAL. The default classifier is deterministic
// and offline. A model-backed classifier is a later, separately-decided change
// that implements this same interface.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/intent

go 1.25.0

require github.com/callscreen/callscreen-platform/packages/go/conversation v0.0.0

require (
	github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0 // indirect
)

replace github.com/callscreen/callscreen-platform/packages/go/conversation => ../conversation

replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../runtime

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics
