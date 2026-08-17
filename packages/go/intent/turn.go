package intent

import (
	"github.com/callscreen/callscreen-platform/packages/go/conversation"
)

// Turn / interruption classification.
//
// WHAT THIS IS NOT. It is not a state machine, not a second interruption
// engine, and not a second confidence model. Three of the eight categories are
// already DECIDED by frozen components, not merely representable by them:
//
//	acknowledgement  TurnManager.NoteOverlap → FloorBackchannel, by overlap
//	                 duration (≤600ms), turn.go:414
//	interruption     TurnManager / InterruptionEngine floor arbitration
//	clarification    IntentEngine.Resolve → IntentVerdict, intent.go:305
//
// Writing a lexical backchannel detector here would put a Phase 13 opinion in
// competition with a frozen one that classifies by duration — two answers to
// "was that an interruption", which is exactly the parallel interruption engine
// the architecture forbids. So those decisions arrive as INPUTS.
//
// What is left for this file is a mapping, plus exactly one thing no frozen
// component provides: recognising that a caller has withdrawn a request.
//
// See docs/phase13/TURN_SEMANTICS.md.

// TurnSignal is the classification result.
//
// Every field is a FROZEN type. There is deliberately no field of this
// package's own invention and no new enum — the frozen vocabulary already
// distinguishes all eight categories, and IntentState alone covers four of them
// (Active, Proposed/Superseded, Abandoned, Fulfilled).
//
// Notably absent: conversation.State and conversation.Trigger. This type cannot
// express a lifecycle transition, which is what makes it structurally incapable
// of driving the FSM.
type TurnSignal struct {
	// Event is the frozen event vocabulary, echoed. Carries the silence case.
	Event conversation.EventKind

	// Floor is the frozen floor decision. Carries the acknowledgement case as
	// FloorBackchannel — as decided by TurnManager, not by this package.
	Floor conversation.FloorDecision

	// Interruption is the frozen interruption vocabulary. InterruptionUser for
	// a caller barge-in.
	Interruption conversation.InterruptionKind

	// Clarify is the frozen clarification vocabulary, set when the turn needs
	// clarification and ClarifyNone otherwise.
	Clarify conversation.ClarificationKind

	// Lifecycle is the frozen intent lifecycle. It carries continuation
	// (IntentActive), new request (IntentProposed / IntentSuperseded),
	// cancellation (IntentAbandoned) and completion (IntentFulfilled).
	//
	// This is a REPORT of the lifecycle the turn implies. Nothing here applies
	// it; the existing engine remains the only writer of intent state.
	Lifecycle conversation.IntentState

	// Intent is the intent the turn concerns, or IntentUnknown.
	Intent conversation.IntentName

	// Expectation is the answer shape in force, echoed unchanged.
	Expectation conversation.Expectation
}

// TurnInput is everything ClassifyTurn considers.
//
// A struct of frozen values, and ClassifyTurn is a pure function of it: given
// identical input it returns an identical TurnSignal — no clock, no map
// iteration, no randomness, no receiver. That is what makes replay
// deterministic, and it is the same discipline conversation.PlanInput follows.
type TurnInput struct {
	// Event is the incoming event kind.
	Event conversation.EventKind

	// Utterance is the caller contribution, when Event is EventUtterance.
	Utterance conversation.Utterance

	// Expectation is the answer shape established by the previous turn.
	Expectation conversation.Expectation

	// Floor is the decision the frozen TurnManager already made about this
	// overlap. Supplied, never recomputed.
	Floor conversation.FloorDecision

	// Interruption is the kind the frozen interruption path already
	// determined. Supplied, never recomputed.
	Interruption conversation.InterruptionKind

	// Verdict is the frozen IntentEngine's verdict for this utterance.
	// Supplied, never recomputed — this is why T8 introduces no thresholds.
	Verdict conversation.IntentVerdict

	// Resolved is the frozen IntentEngine's resolved intent.
	Resolved conversation.Intent

	// Active is the intent currently being pursued, or empty if none. Used
	// only to distinguish a new request that supersedes one from a new request
	// that starts one.
	Active conversation.IntentName

	// Lifecycle is the current intent lifecycle state. Returned unchanged for
	// every category that does not imply a lifecycle change, so "unchanged" is
	// explicit rather than an accidental zero value.
	Lifecycle conversation.IntentState

	// Config is the SAME frozen intent configuration the engine used. Only
	// AmbiguityMargin is read, and only to choose between two frozen
	// ClarificationKind values. No threshold is defined by this package.
	Config conversation.IntentConfig
}

// cancellationCues returns the closed set of phrases that withdraw a request.
//
// A function, not a package-level var, for the same reason DefaultRules is one:
// a package-level slice is mutable state that any caller could rewrite, and
// TestPackage_HasNoPackageLevelMutableState rejects it. Returning a fresh
// literal makes the vocabulary unmodifiable from outside.
//
// Bounded and deterministic, in the same shape as the intent lexicon: multi-word
// cues only, because single words are ambiguous in a way that matters here.
// "cancel" alone is a legitimate request in a booking context; "cancel that"
// withdraws the current one. Getting this wrong discards work the caller wanted.
//
// This is the ONLY thing T8 adds to the vocabulary, and it adds no identifier:
// it maps onto the frozen conversation.IntentAbandoned.
func cancellationCues() [][]string {
	return [][]string{
		{"never", "mind"},
		{"forget", "it"},
		{"forget", "that"},
		{"cancel", "that"},
		{"ignore", "that"},
		{"scratch", "that"},
		{"disregard", "that"},
		{"no", "never", "mind"},
	}
}

// hasCancellationCue reports whether the tokens contain a cancellation phrase.
//
// Reuses tokenize from the lexicon, so the same bounded token limit applies and
// an unbounded caller string cannot drive unbounded work.
func hasCancellationCue(tokens []string) bool {
	for _, cue := range cancellationCues() {
		for i := range tokens {
			if matchAt(tokens, i, cue) {
				return true
			}
		}
	}
	return false
}

// ClassifyTurn maps an event and the frozen deciders' outputs onto the frozen
// turn vocabulary.
//
// The order of checks is the policy, and it mirrors the order frozen
// IntentEngine.Resolve documents at intent.go:305 — noise first, then the
// constrained expectation, then the general case. Diverging from that order is
// how a confirmation gets misread as a new request.
func ClassifyTurn(in TurnInput) TurnSignal {
	// Start from the input, so every field that a category does not change is
	// carried through unchanged rather than defaulting to a zero value.
	out := TurnSignal{
		Event:        in.Event,
		Floor:        in.Floor,
		Interruption: in.Interruption,
		Clarify:      conversation.ClarifyNone,
		Lifecycle:    in.Lifecycle,
		Intent:       in.Resolved.Name,
		Expectation:  in.Expectation,
	}

	switch in.Event {
	case conversation.EventSilence:
		// 7 — silence. A silence window is not a contribution: it changes no
		// intent lifecycle and moves no floor. Preserved as its own category
		// rather than collapsed into noise, which is a different thing.
		out.Intent = in.Active
		return out

	case conversation.EventHangup:
		// 8 — completion, in its most final form.
		out.Lifecycle = conversation.IntentFulfilled
		return out

	case conversation.EventInterrupt:
		// 4 — interruption, explicitly signalled.
		if out.Interruption == conversation.InterruptionNone {
			out.Interruption = conversation.InterruptionUser
		}
		return out

	case conversation.EventOverlap:
		// 4 or 5 — the FROZEN floor decision settles which. Overlap below
		// BackchannelMaxDuration is a backchannel and the floor does not move;
		// this package does not get a second opinion.
		if in.Floor == conversation.FloorBackchannel {
			// 5 — acknowledgement. Intent lifecycle untouched: an "mm-hm" is
			// not a contribution to what is being discussed.
			out.Intent = in.Active
			return out
		}
		if out.Interruption == conversation.InterruptionNone {
			out.Interruption = conversation.InterruptionUser
		}
		return out

	case conversation.EventUtterance:
		return classifyUtterance(in, out)

	default:
		// EventStart, EventGreetingComplete, EventSpeechComplete,
		// EventToolComplete, EventTimeout, EventFault. None of these is a
		// caller contribution, so none changes the intent lifecycle.
		return out
	}
}

// classifyUtterance handles EventUtterance, following the frozen precedence.
func classifyUtterance(in TurnInput, out TurnSignal) TurnSignal {
	// 1 — noise first. An unintelligible utterance is not a low-confidence
	// intent; treating it as one produces a clarification about a topic the
	// caller never raised. This is the frozen engine's reasoning, and its
	// verdict is what is being read here.
	if in.Verdict == conversation.IntentNoise {
		out.Clarify = conversation.ClarifyNoise
		out.Intent = conversation.IntentUnknown
		return out
	}

	// 2 — a constrained expectation short-circuits general classification.
	// When a yes/no is expected, "yes" means yes, and it is a continuation of
	// the question already asked, never a new request.
	if in.Expectation == conversation.ExpectYesNo {
		switch in.Resolved.Name {
		case conversation.IntentAffirm, conversation.IntentDeny:
			out.Lifecycle = conversation.IntentActive
			return out
		}
	}

	tokens := tokenize(in.Utterance.Text)

	// 6 — cancellation. Checked before the verdict is consulted because a
	// withdrawal is meaningful whatever the classifier made of the rest of the
	// sentence: "never mind, I'll call back later" must not be routed as a
	// callback request.
	//
	// Deliberately after the yes/no short-circuit: under ExpectYesNo a "no" is
	// IntentDeny answering the question, not a cancellation of it.
	if hasCancellationCue(tokens) {
		out.Lifecycle = conversation.IntentAbandoned
		return out
	}

	// 8 — completion signalled by the caller.
	if in.Resolved.Name == IntentEndCall {
		out.Lifecycle = conversation.IntentFulfilled
		return out
	}

	switch in.Verdict {
	case conversation.IntentReject:
		// Unknown / below the frozen reject threshold. Preserved as its own
		// outcome: not a clarification, because the frozen engine already
		// decided this one is to be discarded rather than asked about.
		out.Intent = conversation.IntentUnknown
		return out

	case conversation.IntentClarify:
		// 3 — clarification. Which frozen kind applies is chosen from frozen
		// predicates only.
		out.Clarify = clarificationKind(in)
		return out
	}

	// IntentAccept from here on.

	// 1 — continuation. A pending expectation means the caller is answering the
	// question just asked, which continues the intent being pursued.
	if in.Expectation != conversation.ExpectNothing {
		out.Lifecycle = conversation.IntentActive
		return out
	}

	// 2 — new request. Superseded when it displaces a different active intent,
	// proposed when nothing was in flight. Both are frozen values.
	if in.Active != "" && in.Active != in.Resolved.Name {
		out.Lifecycle = conversation.IntentSuperseded
		return out
	}
	if in.Active == in.Resolved.Name && in.Active != "" {
		// The same intent restated is a continuation, not a new request.
		out.Lifecycle = conversation.IntentActive
		return out
	}
	out.Lifecycle = conversation.IntentProposed
	return out
}

// clarificationKind selects among the frozen ClarificationKind values.
//
// Uses only frozen inputs: Intent.Complete() and Intent.Margin() are frozen
// methods, and AmbiguityMargin is a frozen config field carrying the same value
// the engine itself used. This package defines no threshold of its own — see
// ADR-0016 on why a second confidence model is not permitted.
func clarificationKind(in TurnInput) conversation.ClarificationKind {
	// Missing information first: the intent is understood, so asking about the
	// gap is more useful than asking which intent was meant.
	if !in.Resolved.Complete() {
		return conversation.ClarifyMissingSlot
	}
	// A close runner-up is ambiguity, not low confidence. This is the case a
	// bare threshold misses, and the frozen engine calls it out for that reason.
	if len(in.Resolved.Alternatives) > 1 &&
		in.Resolved.Margin() < in.Config.AmbiguityMargin {
		return conversation.ClarifyAmbiguous
	}
	return conversation.ClarifyLowConfidence
}
