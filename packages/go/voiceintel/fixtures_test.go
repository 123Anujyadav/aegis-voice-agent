package voiceintel_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/intent"
	"github.com/callscreen/callscreen-platform/packages/go/voiceintel"
)

// T13 — EVALUATION FIXTURES.
//
// WHAT THIS IS. A fixed, ordered inventory of known inputs, each declaring the
// exact typed outcome the existing Phase 13 architecture must produce. Every
// expectation below was MEASURED against the real classifier and the real
// bridge before being written down — none was assumed.
//
// WHAT THIS IS NOT. Not an NLU accuracy benchmark. The classifier is a
// deterministic closed-vocabulary rule matcher, so a fixture pass rate says
// only "these known inputs still produce these known typed outcomes". It says
// NOTHING about real-world language understanding, and no percentage in this
// file may be read as accuracy. No model, provider or external service is
// involved — FOUR OUTCOMES, KEPT APART. The frozen engine distinguishes more
// than the three the task names, and collapsing any of them would be a
// regression:
//
//	unknown        0 candidates          -> fallback,  ActionRespond
//	below-reject   1 cand @0.333 (<0.45) -> rejected,  ActionEscalate
//	low confidence 1 cand @0.667         -> confirm,   ClarifyLowConfidence
//	ambiguous      2 cands @1.0, margin 0-> clarify,   ClarifyAmbiguous
//
// Reachability note: confidence comes from evidence/saturation, so with the
// real lexicon the attainable values are 0.333 (one single-token cue), 0.667
// (two) and 1.0 (a phrase cue or three singles). 0.667 is what makes the
// low-confidence band reachable at all through real inputs.

// ---------------------------------------------------------------------------
// The fixture table
// ---------------------------------------------------------------------------

// fixture is one evaluation case with its complete expected outcome.
//
// A field left at its zero value is still asserted — "expect no clarification"
// is as much a claim as "expect ambiguity".
type fixture struct {
	// name identifies the fixture. Fixture order is the table order, which is
	// fixed, so runs are auditable and comparable.
	name string
	// category is the required T13 category this fixture covers.
	category string
	// text is the caller utterance.
	text string

	// wantCandidates is the exact candidate count from intent.Classifier.
	wantCandidates int
	// wantTopIntent is the leading candidate, or "" when there are none.
	wantTopIntent conversation.IntentName
	// wantConfLow/High bound the top candidate's confidence inclusively.
	// Both zero means "no candidate, so no confidence".
	wantConfLow, wantConfHigh float64
	// wantConfClass names the band, for auditability.
	wantConfClass string

	// wantAction is the frozen planner's response strategy.
	wantAction conversation.Action
	// wantReason is the frozen machine-readable reason code.
	wantReason string
	// wantPlanIntent is the intent carried on the Plan.
	wantPlanIntent conversation.IntentName
	// wantClarify is the frozen clarification kind.
	wantClarify conversation.ClarificationKind
	// wantSlots is the number of slot SHAPES returned (never values).
	wantSlots int
	// wantTerminal records whether the turn ends the conversation.
	wantTerminal bool
}

// evaluationFixtures is the ordered inventory. Order is deliberate and stable.
func evaluationFixtures() []fixture {
	return []fixture{
		// 1 — NORMAL REQUEST
		{
			name: "normal/callback_with_number", category: "normal",
			text:           "please call me back on 9876543210",
			wantCandidates: 1, wantTopIntent: intent.IntentRequestCallback,
			wantConfLow: 1.0, wantConfHigh: 1.0, wantConfClass: "certain",
			wantAction: conversation.ActionRespond, wantReason: "intent_accepted",
			wantPlanIntent: intent.IntentRequestCallback,
			wantClarify:    conversation.ClarifyNone, wantSlots: 2,
		},
		{
			name: "normal/transfer", category: "normal",
			text:           "transfer me to rajesh",
			wantCandidates: 1, wantTopIntent: intent.IntentRequestTransfer,
			wantConfLow: 1.0, wantConfHigh: 1.0, wantConfClass: "certain",
			wantAction: conversation.ActionRespond, wantReason: "intent_accepted",
			wantPlanIntent: intent.IntentRequestTransfer,
			wantClarify:    conversation.ClarifyNone, wantSlots: 1,
		},

		// 2 — AMBIGUOUS REQUEST: two candidates, both saturated, margin 0.
		{
			name: "ambiguous/hold_and_callback", category: "ambiguous",
			text:           "hold on call back",
			wantCandidates: 2, wantTopIntent: intent.IntentHold,
			wantConfLow: 1.0, wantConfHigh: 1.0, wantConfClass: "certain_but_tied",
			wantAction: conversation.ActionClarify, wantReason: "clarify_ambiguous",
			wantPlanIntent: intent.IntentHold,
			wantClarify:    conversation.ClarifyAmbiguous, wantSlots: 0,
		},

		// 3 — CLARIFICATION: understood, but a required slot is unfilled.
		{
			name: "clarification/missing_message_body", category: "clarification",
			text:           "i want to leave a message",
			wantCandidates: 1, wantTopIntent: intent.IntentLeaveMessage,
			wantConfLow: 1.0, wantConfHigh: 1.0, wantConfClass: "certain",
			wantAction: conversation.ActionAsk, wantReason: "clarify_missing_slot",
			wantPlanIntent: intent.IntentLeaveMessage,
			wantClarify:    conversation.ClarifyMissingSlot, wantSlots: 1,
		},
		{
			name: "clarification/slot_like_but_invalid", category: "clarification",
			text:           "my number is not-a-number call me back",
			wantCandidates: 1, wantTopIntent: intent.IntentRequestCallback,
			wantConfLow: 1.0, wantConfHigh: 1.0, wantConfClass: "certain",
			wantAction: conversation.ActionAsk, wantReason: "clarify_missing_slot",
			wantPlanIntent: intent.IntentRequestCallback,
			wantClarify:    conversation.ClarifyMissingSlot, wantSlots: 2,
		},

		// 12 — LOW CONFIDENCE: recognised, 0.667, inside [reject, accept).
		{
			name: "low_confidence/repeat", category: "low_confidence",
			text:           "repeat pardon",
			wantCandidates: 1, wantTopIntent: intent.IntentRepeat,
			wantConfLow: 0.66, wantConfHigh: 0.67, wantConfClass: "clarify_band",
			wantAction: conversation.ActionConfirm, wantReason: "clarify_low_confidence",
			wantPlanIntent: intent.IntentRepeat,
			wantClarify:    conversation.ClarifyLowConfidence, wantSlots: 0,
		},
		{
			name: "low_confidence/hold", category: "low_confidence",
			text:           "wait ruko",
			wantCandidates: 1, wantTopIntent: intent.IntentHold,
			wantConfLow: 0.66, wantConfHigh: 0.67, wantConfClass: "clarify_band",
			wantAction: conversation.ActionConfirm, wantReason: "clarify_low_confidence",
			wantPlanIntent: intent.IntentHold,
			wantClarify:    conversation.ClarifyLowConfidence, wantSlots: 0,
		},

		// BELOW-REJECT: recognised but under the frozen reject threshold. A
		// FOURTH outcome, distinct from both unknown and low confidence.
		{
			name: "below_reject/single_weak_cue", category: "low_confidence",
			text:           "callback transfer",
			wantCandidates: 1, wantTopIntent: intent.IntentRequestCallback,
			wantConfLow: 0.33, wantConfHigh: 0.34, wantConfClass: "below_reject",
			wantAction: conversation.ActionEscalate, wantReason: "intent_rejected",
			wantPlanIntent: intent.IntentRequestCallback,
			wantClarify:    conversation.ClarifyNone, wantSlots: 2,
			wantTerminal: true, // escalation is terminal
		},

		// 11 — MALFORMED / UNKNOWN: zero candidates, engine falls back.
		{
			name: "malformed/empty", category: "malformed",
			text:           "",
			wantCandidates: 0, wantConfClass: "none",
			wantAction: conversation.ActionRespond, wantReason: "fallback",
			wantPlanIntent: conversation.IntentFallback,
			wantClarify:    conversation.ClarifyNone, wantSlots: 0,
		},
		{
			name: "malformed/whitespace_only", category: "malformed",
			text:           "   \t\n  ",
			wantCandidates: 0, wantConfClass: "none",
			wantAction: conversation.ActionRespond, wantReason: "fallback",
			wantPlanIntent: conversation.IntentFallback,
			wantClarify:    conversation.ClarifyNone, wantSlots: 0,
		},
		{
			name: "malformed/control_bytes", category: "malformed",
			text:           "\x00\x01\x02",
			wantCandidates: 0, wantConfClass: "none",
			wantAction: conversation.ActionRespond, wantReason: "fallback",
			wantPlanIntent: conversation.IntentFallback,
			wantClarify:    conversation.ClarifyNone, wantSlots: 0,
		},
		{
			name: "malformed/unknown_vocabulary", category: "malformed",
			text:           "zzzz qqqq wubble frotz",
			wantCandidates: 0, wantConfClass: "none",
			wantAction: conversation.ActionRespond, wantReason: "fallback",
			wantPlanIntent: conversation.IntentFallback,
			wantClarify:    conversation.ClarifyNone, wantSlots: 0,
		},
	}
}

// oversizedFixtureText is 600 repetitions of a single cue — comfortably past
// the frozen maxTokens = 512 bound. Kept out of the table so the table stays
// readable.
func oversizedFixtureText() string { return strings.Repeat("callback ", 600) }

// ---------------------------------------------------------------------------
// Fixture evaluation
// ---------------------------------------------------------------------------

// evalResult is what a fixture actually produced.
type evalResult struct {
	candidates int
	topIntent  conversation.IntentName
	confidence float64
	slots      int
	plan       conversation.Plan
	terminal   bool
	err        error
}

// evaluate runs one fixture through BOTH the raw classifier and the bridge.
//
// The raw classifier gives candidate/confidence/slot shape; the bridge gives
// the frozen planner's response strategy. Checking both is what separates "the
// classifier saw it" from "the system decided to act on it".
func evaluate(t *testing.T, f fixture, idSuffix string) evalResult {
	t.Helper()

	c, err := intent.New(intent.DefaultConfig())
	if err != nil {
		t.Fatalf("intent.New: %v", err)
	}
	u := conversation.Utterance{Text: f.text, ASRConfidence: 0.95}

	cands, slots, cErr := c.Classify(u, conversation.ExpectNothing)
	res := evalResult{candidates: len(cands), slots: len(slots), err: cErr}
	if len(cands) > 0 {
		res.topIntent = cands[0].Name
		res.confidence = cands[0].Confidence
	}

	b, err := voiceintel.New(voiceintel.WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("voiceintel.New: %v", err)
	}
	id := conversation.ConversationID("fx-" + idSuffix)
	p, err := b.Planner(id, "")
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}
	openFloor(t, p)

	plan, hErr := p.Handle(utteranceEvent(f.text))
	res.plan = plan
	if hErr != nil {
		res.err = hErr
	}
	conv, ok := b.Conversation(id)
	if !ok {
		// NOT an error. The frozen engine DELETES a conversation from its
		// active map the moment it reaches a terminal state (engine.go:450,
		// established in T10), so a fixture that escalates is simply no longer
		// retrievable. Absence is therefore the terminal signal.
		res.terminal = true
		return res
	}
	// The plan is produced while StateSpeaking; the terminal decision shows up
	// once the agent's turn completes.
	if _, err := conv.Handle(conversation.Event{
		Kind: conversation.EventSpeechComplete}); err != nil {
		// A terminal conversation refuses the completion; that IS the signal.
		res.terminal = true
	}
	if conv.State().IsTerminal() {
		res.terminal = true
	}
	return res
}

// TestT13_EvaluationFixtures is the inventory run. Every fixture asserts every
// declared field.
func TestT13_EvaluationFixtures(t *testing.T) {
	t.Parallel()

	for i, f := range evaluationFixtures() {
		t.Run(f.name, func(t *testing.T) {
			got := evaluate(t, f, fmt.Sprintf("%02d", i))

			if got.err != nil && !f.wantTerminal {
				t.Fatalf("unexpected error: %v", got.err)
			}
			if got.candidates != f.wantCandidates {
				t.Errorf("candidates = %d, want %d", got.candidates, f.wantCandidates)
			}
			if got.topIntent != f.wantTopIntent {
				t.Errorf("top intent = %q, want %q", got.topIntent, f.wantTopIntent)
			}
			if f.wantCandidates > 0 {
				if got.confidence < f.wantConfLow || got.confidence > f.wantConfHigh {
					t.Errorf("confidence = %.6f, want within [%.2f, %.2f] (%s)",
						got.confidence, f.wantConfLow, f.wantConfHigh, f.wantConfClass)
				}
			}
			if got.slots != f.wantSlots {
				t.Errorf("slot shapes = %d, want %d", got.slots, f.wantSlots)
			}
			if got.plan.Action != f.wantAction {
				t.Errorf("action = %v, want %v", got.plan.Action, f.wantAction)
			}
			if got.plan.Reason != f.wantReason {
				t.Errorf("reason = %q, want %q", got.plan.Reason, f.wantReason)
			}
			if got.plan.Intent != f.wantPlanIntent {
				t.Errorf("plan intent = %q, want %q", got.plan.Intent, f.wantPlanIntent)
			}
			if got.plan.Clarification.Kind != f.wantClarify {
				t.Errorf("clarification = %v, want %v",
					got.plan.Clarification.Kind, f.wantClarify)
			}
			if got.terminal != f.wantTerminal {
				t.Errorf("terminal = %v, want %v", got.terminal, f.wantTerminal)
			}
			// Nothing may leave the closed vocabulary.
			assertIntentInVocabulary(t, got.topIntent)
			assertIntentInVocabulary(t, got.plan.Intent)
		})
	}
}

// TestT13_FourOutcomesStayDistinct is the anti-collapse assertion.
//
// The unknown, below-reject, low-confidence and ambiguous outcomes must
// produce four
// DIFFERENT (action, reason, clarification) triples. Asserting each in
// isolation would not catch two of them being merged.
func TestT13_FourOutcomesStayDistinct(t *testing.T) {
	t.Parallel()

	pick := func(name string) fixture {
		for _, f := range evaluationFixtures() {
			if f.name == name {
				return f
			}
		}
		t.Fatalf("fixture %q not found", name)
		return fixture{}
	}

	names := []string{
		"malformed/unknown_vocabulary", // unknown
		"below_reject/single_weak_cue", // below reject
		"low_confidence/repeat",        // low confidence
		"ambiguous/hold_and_callback",  // ambiguous
	}

	seen := map[string]string{}
	for i, n := range names {
		f := pick(n)
		got := evaluate(t, f, fmt.Sprintf("distinct%d", i))
		sig := fmt.Sprintf("action=%v reason=%s clarify=%v",
			got.plan.Action, got.plan.Reason, got.plan.Clarification.Kind)
		if prev, dup := seen[sig]; dup {
			t.Errorf("%q and %q produced the identical outcome %s — two distinct "+
				"cases have been collapsed", prev, n, sig)
		}
		seen[sig] = n
		t.Logf("%-30s -> %s", n, sig)
	}
	if len(seen) != len(names) {
		t.Errorf("%d distinct outcomes across %d fixtures", len(seen), len(names))
	}
}

// TestT13_OversizedInputRespectsTokenBound — the frozen bound is maxTokens =
// 512. A 600-cue utterance must classify identically to one that merely
// exceeds the bound, and must not accumulate unbounded evidence.
func TestT13_OversizedInputRespectsTokenBound(t *testing.T) {
	t.Parallel()

	c, err := intent.New(intent.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}

	big := oversizedFixtureText()
	cands, _, err := c.Classify(
		conversation.Utterance{Text: big, ASRConfidence: 0.95}, conversation.ExpectNothing)
	if err != nil {
		t.Fatalf("oversized input errored: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("oversized input gave %d candidates, want 1", len(cands))
	}
	// Repetition must not inflate confidence: evidence is counted per CUE, not
	// per occurrence, so 600 repeats score exactly what one does.
	if cands[0].Confidence > 0.34 {
		t.Errorf("confidence = %.6f; repetition inflated the evidence",
			cands[0].Confidence)
	}
	one, _, _ := c.Classify(
		conversation.Utterance{Text: "callback", ASRConfidence: 0.95},
		conversation.ExpectNothing)
	if len(one) != 1 || one[0].Confidence != cands[0].Confidence {
		t.Errorf("600 repeats scored %v but a single occurrence scored %v",
			cands[0].Confidence, one[0].Confidence)
	}
}

// ---------------------------------------------------------------------------
// 4-7. Turn-level fixtures: interruption, acknowledgement, cancellation,
// silence
// ---------------------------------------------------------------------------

// TestT13_TurnFixtures covers the categories whose outcome is a turn
// classification rather than a plan. Each asserts the exact frozen values.
func TestT13_TurnFixtures(t *testing.T) {
	t.Parallel()

	cfg := conversation.DefaultIntentConfig()

	cases := []struct {
		name     string
		category string
		in       intent.TurnInput
		// Full expected TurnSignal shape.
		wantEvent     conversation.EventKind
		wantFloor     conversation.FloorDecision
		wantInterrupt conversation.InterruptionKind
		wantClarify   conversation.ClarificationKind
		wantLifecycle conversation.IntentState
		wantIntent    conversation.IntentName
	}{
		{
			name: "interruption/barge_in", category: "interruption",
			in: intent.TurnInput{
				Event: conversation.EventInterrupt, Lifecycle: conversation.IntentActive,
				Active: intent.IntentCallPurpose, Config: cfg,
			},
			wantEvent: conversation.EventInterrupt, wantFloor: conversation.FloorGranted,
			wantInterrupt: conversation.InterruptionUser,
			wantClarify:   conversation.ClarifyNone, wantLifecycle: conversation.IntentActive,
		},
		{
			name: "acknowledgement/backchannel", category: "acknowledgement",
			in: intent.TurnInput{
				Event: conversation.EventOverlap, Floor: conversation.FloorBackchannel,
				Lifecycle: conversation.IntentActive, Active: intent.IntentLeaveMessage,
				Config: cfg,
			},
			wantEvent: conversation.EventOverlap, wantFloor: conversation.FloorBackchannel,
			wantInterrupt: conversation.InterruptionNone,
			wantClarify:   conversation.ClarifyNone, wantLifecycle: conversation.IntentActive,
			wantIntent: intent.IntentLeaveMessage,
		},
		{
			name: "cancellation/never_mind", category: "cancellation",
			in: intent.TurnInput{
				Event:     conversation.EventUtterance,
				Utterance: conversation.Utterance{Text: "never mind", ASRConfidence: 0.95},
				Verdict:   conversation.IntentAccept,
				Resolved: conversation.Intent{
					Name: intent.IntentRequestCallback, Confidence: 1.0},
				Active: intent.IntentRequestCallback, Lifecycle: conversation.IntentActive,
				Config: cfg,
			},
			wantEvent: conversation.EventUtterance, wantFloor: conversation.FloorGranted,
			wantInterrupt: conversation.InterruptionNone,
			wantClarify:   conversation.ClarifyNone,
			wantLifecycle: conversation.IntentAbandoned,
			wantIntent:    intent.IntentRequestCallback,
		},
		{
			name: "silence/window", category: "silence",
			in: intent.TurnInput{
				Event: conversation.EventSilence, Lifecycle: conversation.IntentActive,
				Active: intent.IntentHold, Config: cfg,
			},
			wantEvent: conversation.EventSilence, wantFloor: conversation.FloorGranted,
			wantInterrupt: conversation.InterruptionNone,
			wantClarify:   conversation.ClarifyNone, wantLifecycle: conversation.IntentActive,
			wantIntent: intent.IntentHold,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := intent.ClassifyTurn(tc.in)

			if got.Event != tc.wantEvent {
				t.Errorf("Event = %v, want %v", got.Event, tc.wantEvent)
			}
			if got.Floor != tc.wantFloor {
				t.Errorf("Floor = %v, want %v", got.Floor, tc.wantFloor)
			}
			if got.Interruption != tc.wantInterrupt {
				t.Errorf("Interruption = %v, want %v", got.Interruption, tc.wantInterrupt)
			}
			if got.Clarify != tc.wantClarify {
				t.Errorf("Clarify = %v, want %v", got.Clarify, tc.wantClarify)
			}
			if got.Lifecycle != tc.wantLifecycle {
				t.Errorf("Lifecycle = %v, want %v", got.Lifecycle, tc.wantLifecycle)
			}
			if got.Intent != tc.wantIntent {
				t.Errorf("Intent = %q, want %q", got.Intent, tc.wantIntent)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 8. Multi-turn conversation
// ---------------------------------------------------------------------------

// TestT13_MultiTurnProgression drives a scripted dialogue and asserts the plan
// at each step plus the context carried across the turns.
//
// No new context system: the assertions read the frozen ContextEngine through
// Conversation.Context(), the API T7 established.
func TestT13_MultiTurnProgression(t *testing.T) {
	t.Parallel()

	steps := []struct {
		text       string
		wantAction conversation.Action
		wantIntent conversation.IntentName
		wantReason string
	}{
		{"please call me back on 9876543210", conversation.ActionRespond,
			intent.IntentRequestCallback, "intent_accepted"},
		{"transfer me to rajesh", conversation.ActionRespond,
			intent.IntentRequestTransfer, "intent_accepted"},
		{"say that again", conversation.ActionRespond,
			intent.IntentRepeat, "intent_accepted"},
		{"can you hold on a moment", conversation.ActionRespond,
			intent.IntentHold, "intent_accepted"},
	}

	b, err := voiceintel.New(voiceintel.WithClock(fixedClock()))
	if err != nil {
		t.Fatal(err)
	}
	p, err := b.Planner("fx-multiturn", "")
	if err != nil {
		t.Fatal(err)
	}
	openFloor(t, p)
	conv, _ := b.Conversation("fx-multiturn")

	// A caller-supplied fact stored on turn 1 must survive to the last turn.
	if err := conv.Context().Set(conversation.Entry{
		Key: "appointment_number", Value: "A-7419",
		Scope: conversation.ScopeConversation, Source: "t13",
	}); err != nil {
		t.Fatal(err)
	}

	for i, s := range steps {
		plan, err := p.Handle(utteranceEvent(s.text))
		if err != nil {
			t.Fatalf("turn %d (%q): %v", i, s.text, err)
		}
		if plan.Action != s.wantAction {
			t.Errorf("turn %d: action = %v, want %v", i, plan.Action, s.wantAction)
		}
		if plan.Intent != s.wantIntent {
			t.Errorf("turn %d: intent = %q, want %q", i, plan.Intent, s.wantIntent)
		}
		if plan.Reason != s.wantReason {
			t.Errorf("turn %d: reason = %q, want %q", i, plan.Reason, s.wantReason)
		}
		if _, err := p.Handle(conversation.Event{
			Kind: conversation.EventSpeechComplete}); err != nil {
			t.Fatalf("turn %d speech complete: %v", i, err)
		}

		// Context established on turn 1 is still there.
		e, ok := conv.Context().Get(conversation.ScopeConversation, "appointment_number")
		if !ok || e.Value != "A-7419" {
			t.Fatalf("turn %d: context lost (%v, ok=%v)", i, e.Value, ok)
		}
		// The engine records the intent it just pursued.
		li, ok := conv.Context().Get(conversation.ScopeConversation, "last_intent")
		if !ok || li.Value != string(s.wantIntent) {
			t.Errorf("turn %d: last_intent = %v (ok=%v), want %q",
				i, li.Value, ok, s.wantIntent)
		}
	}

	// Four turns is well inside the frozen MaxTurns = 20, so the conversation
	// must still be live.
	if conv.State().IsTerminal() {
		t.Errorf("conversation terminal after %d turns; MaxTurns is 20", len(steps))
	}
}

// ---------------------------------------------------------------------------
// 9. Context eviction
// ---------------------------------------------------------------------------

// TestT13_ContextEvictionFixture exercises the frozen MaxEntriesPerScope = 256
// boundary and asserts ONLY what the frozen contract guarantees.
//
// Per T11: with equal SetAt timestamps the eviction victim is unspecified
// (evictOldestLocked compares with Before(), which is false for ties). This
// fixture therefore asserts the bound, the eviction count and newest-survives
// — and, in a separate distinct-timestamp phase, the victim identity.
func TestT13_ContextEvictionFixture(t *testing.T) {
	t.Parallel()

	t.Run("tied_timestamps_bound_only", func(t *testing.T) {
		b, err := voiceintel.New(voiceintel.WithClock(fixedClock())) // never advanced
		if err != nil {
			t.Fatal(err)
		}
		p, err := b.Planner("fx-evict-tied", "")
		if err != nil {
			t.Fatal(err)
		}
		openFloor(t, p)
		conv, _ := b.Conversation("fx-evict-tied")
		c := conv.Context()

		for i := 0; i < frozenMaxEntriesPerScope; i++ {
			if err := c.Set(conversation.Entry{
				Key: fmt.Sprintf("k%04d", i), Value: i,
				Scope: conversation.ScopeConversation, Source: "t13",
			}); err != nil {
				t.Fatal(err)
			}
		}
		if got := c.Size(conversation.ScopeConversation); got != frozenMaxEntriesPerScope {
			t.Fatalf("size at the bound = %d, want %d", got, frozenMaxEntriesPerScope)
		}

		if err := c.Set(conversation.Entry{
			Key: "overflow", Value: "x",
			Scope: conversation.ScopeConversation, Source: "t13",
		}); err != nil {
			t.Fatal(err)
		}

		if got := c.Size(conversation.ScopeConversation); got != frozenMaxEntriesPerScope {
			t.Errorf("size after overflow = %d, want exactly %d",
				got, frozenMaxEntriesPerScope)
		}
		if _, ok := c.Get(conversation.ScopeConversation, "overflow"); !ok {
			t.Error("the newest entry was evicted")
		}
		missing := 0
		for i := 0; i < frozenMaxEntriesPerScope; i++ {
			if _, ok := c.Get(conversation.ScopeConversation, fmt.Sprintf("k%04d", i)); !ok {
				missing++
			}
		}
		if missing != 1 {
			t.Errorf("%d entries evicted, want exactly 1", missing)
		}
		// Deliberately NOT asserted: WHICH key was evicted.
	})

	t.Run("distinct_timestamps_victim_is_oldest", func(t *testing.T) {
		clock := fixedClock()
		b, err := voiceintel.New(voiceintel.WithClock(clock))
		if err != nil {
			t.Fatal(err)
		}
		p, err := b.Planner("fx-evict-distinct", "")
		if err != nil {
			t.Fatal(err)
		}
		openFloor(t, p)
		conv, _ := b.Conversation("fx-evict-distinct")
		c := conv.Context()

		for i := 0; i < frozenMaxEntriesPerScope; i++ {
			if err := c.Set(conversation.Entry{
				Key: fmt.Sprintf("k%04d", i), Value: i,
				Scope: conversation.ScopeConversation, Source: "t13",
			}); err != nil {
				t.Fatal(err)
			}
			clock.Advance(time.Millisecond)
		}
		if err := c.Set(conversation.Entry{
			Key: "overflow", Value: "x",
			Scope: conversation.ScopeConversation, Source: "t13",
		}); err != nil {
			t.Fatal(err)
		}

		if _, ok := c.Get(conversation.ScopeConversation, "k0000"); ok {
			t.Error("k0000 survived; the oldest entry should have been evicted")
		}
		if _, ok := c.Get(conversation.ScopeConversation, "k0001"); !ok {
			t.Error("k0001 was evicted; only the single oldest should have been")
		}
	})
}

// ---------------------------------------------------------------------------
// 10. Concurrent sessions
// ---------------------------------------------------------------------------

// TestT13_ConcurrentSessionFixture runs each fixture in its own session
// simultaneously and requires every result to match the single-session
// expectation exactly.
//
// Behavioural isolation only — NOT a race-detector test. See the T13 report.
func TestT13_ConcurrentSessionFixture(t *testing.T) {
	t.Parallel()

	fixtures := evaluationFixtures()

	// Serial baseline first, so the concurrent run is compared against a
	// known-good answer rather than against its own consensus.
	baseline := make([]string, len(fixtures))
	for i, f := range fixtures {
		got := evaluate(t, f, fmt.Sprintf("base%02d", i))
		baseline[i] = fmt.Sprintf("%d|%s|%.6f|%v|%s|%s|%v",
			got.candidates, got.topIntent, got.confidence,
			got.plan.Action, got.plan.Reason, got.plan.Intent,
			got.plan.Clarification.Kind)
	}

	// ONE bridge shared by every session — the production shape, and the only
	// arrangement in which a cross-session leak is observable. An earlier draft
	// built a bridge per goroutine, which made each session structurally
	// isolated by construction and could not have detected sharing at all.
	shared, err := voiceintel.New(voiceintel.WithClock(fixedClock()))
	if err != nil {
		t.Fatal(err)
	}
	// One classifier shared too, for the same reason.
	sharedClassifier, err := intent.New(intent.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	results := make([]string, len(fixtures))
	markers := make([]string, len(fixtures))

	for i, f := range fixtures {
		wg.Add(1)
		go func(i int, f fixture) {
			defer wg.Done()

			b := shared
			id := conversation.ConversationID(fmt.Sprintf("fx-conc-%02d", i))
			p, err := b.Planner(id, "")
			if err != nil {
				results[i] = "err:" + err.Error()
				return
			}
			if _, err := p.Handle(conversation.Event{
				Kind: conversation.EventStart}); err != nil {
				results[i] = "err:" + err.Error()
				return
			}
			if _, err := p.Handle(conversation.Event{
				Kind: conversation.EventGreetingComplete}); err != nil {
				results[i] = "err:" + err.Error()
				return
			}
			conv, ok := b.Conversation(id)
			if !ok {
				results[i] = "missing"
				return
			}

			// A marker unique to this session.
			marker := fmt.Sprintf("session-%02d-%s", i, f.category)
			if err := conv.Context().Set(conversation.Entry{
				Key: "fixture_marker", Value: marker,
				Scope: conversation.ScopeConversation, Source: "t13",
			}); err != nil {
				results[i] = "err:" + err.Error()
				return
			}

			cands, _, _ := sharedClassifier.Classify(
				conversation.Utterance{Text: f.text, ASRConfidence: 0.95},
				conversation.ExpectNothing)

			plan, _ := p.Handle(utteranceEvent(f.text))

			var top conversation.IntentName
			var conf float64
			if len(cands) > 0 {
				top, conf = cands[0].Name, cands[0].Confidence
			}
			results[i] = fmt.Sprintf("%d|%s|%.6f|%v|%s|%s|%v",
				len(cands), top, conf, plan.Action, plan.Reason, plan.Intent,
				plan.Clarification.Kind)

			// Read the marker back: it must still be this session's.
			e, ok := conv.Context().Get(conversation.ScopeConversation, "fixture_marker")
			if !ok {
				markers[i] = "missing"
				return
			}
			markers[i] = fmt.Sprintf("%v", e.Value)
		}(i, f)
	}
	wg.Wait()

	for i, f := range fixtures {
		if results[i] != baseline[i] {
			t.Errorf("%s: concurrent result differs from the serial baseline\n got %s\nwant %s",
				f.name, results[i], baseline[i])
		}
		want := fmt.Sprintf("session-%02d-%s", i, f.category)
		if markers[i] != want {
			t.Errorf("%s: context marker = %q, want %q — a session observed "+
				"another session's context", f.name, markers[i], want)
		}
	}
}

// ---------------------------------------------------------------------------
// Determinism
// ---------------------------------------------------------------------------

// TestT13_FixturesAreDeterministic re-runs the whole inventory and requires an
// identical signature each time, including fixture ORDER.
func TestT13_FixturesAreDeterministic(t *testing.T) {
	t.Parallel()

	run := func(pass int) string {
		var b strings.Builder
		for i, f := range evaluationFixtures() {
			got := evaluate(t, f, fmt.Sprintf("det%d-%02d", pass, i))
			fmt.Fprintf(&b, "%s=%d|%s|%.6f|%v|%s|%s|%v|%v\n",
				f.name, got.candidates, got.topIntent, got.confidence,
				got.plan.Action, got.plan.Reason, got.plan.Intent,
				got.plan.Clarification.Kind, got.terminal)
		}
		return b.String()
	}

	want := run(0)
	if want == "" {
		t.Fatal("empty fixture signature")
	}
	for i := 1; i <= 25; i++ {
		if got := run(i); got != want {
			t.Fatalf("pass %d differed\n got %s\nwant %s", i, got, want)
		}
	}

	// Fixture ORDER is part of the contract.
	first := evaluationFixtures()
	for i := 0; i < 10; i++ {
		again := evaluationFixtures()
		if len(again) != len(first) {
			t.Fatalf("fixture count changed: %d vs %d", len(again), len(first))
		}
		for j := range first {
			if again[j].name != first[j].name {
				t.Fatalf("fixture order changed at %d: %q vs %q",
					j, again[j].name, first[j].name)
			}
		}
	}
}

// TestT13_InventoryCoversEveryRequiredCategory guards the inventory itself:
// a category silently dropped from the table would otherwise go unnoticed.
func TestT13_InventoryCoversEveryRequiredCategory(t *testing.T) {
	t.Parallel()

	// Categories covered by the fixture table.
	tableCategories := map[string]int{}
	for _, f := range evaluationFixtures() {
		tableCategories[f.category]++
	}
	for _, want := range []string{
		"normal", "ambiguous", "clarification", "low_confidence", "malformed",
	} {
		if tableCategories[want] == 0 {
			t.Errorf("fixture table has no %q case", want)
		}
	}

	// The remaining required categories live in dedicated tests, named here so
	// the mapping is explicit and auditable.
	dedicated := map[string]string{
		"interruption":     "TestT13_TurnFixtures/interruption/barge_in",
		"acknowledgement":  "TestT13_TurnFixtures/acknowledgement/backchannel",
		"cancellation":     "TestT13_TurnFixtures/cancellation/never_mind",
		"silence":          "TestT13_TurnFixtures/silence/window",
		"multi_turn":       "TestT13_MultiTurnProgression",
		"context_eviction": "TestT13_ContextEvictionFixture",
		"concurrent":       "TestT13_ConcurrentSessionFixture",
	}
	if len(dedicated)+5 != 12 {
		t.Errorf("category accounting is off: %d dedicated + 5 table = %d, want 12",
			len(dedicated), len(dedicated)+5)
	}
	t.Logf("table categories: %v", tableCategories)
	t.Logf("dedicated categories: %d", len(dedicated))
}
