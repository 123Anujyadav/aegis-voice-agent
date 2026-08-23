package main

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/voice"
)

// Phase 15 T5 — FINAL LIFECYCLE VERIFICATION.
//
// Regression cover for everything Phase 15 implemented, plus the measured
// evidence for the two gaps T4 discovered and deliberately left open.

// ---------------------------------------------------------------------------
// Part 1 — the whole successful lifecycle
// ---------------------------------------------------------------------------

// TestT5_FullSuccessfulLifecycle drives two complete turns and asserts every
// observable the phase claims.
func TestT5_FullSuccessfulLifecycle(t *testing.T) {
	stt := newT4STT("transfer me to rajesh", "call me back on 9876543210")
	r := newT2RigWith(t, nil, stt, "t5-lifecycle")

	// The conversation begins open: floor with the caller, nothing spoken yet.
	if got := completions(r.conv); got != 0 {
		t.Fatalf("%d completions before any turn", got)
	}

	type want struct {
		intent       string
		completions  int
		turnsAtLeast int
	}
	for i, w := range []want{
		{"request_transfer", 1, 2},
		{"request_callback", 2, 4},
	} {
		r.driveTurn(t, uint64(i*t2TurnFrames))
		r.obs.awaitTurn(t, 30*time.Second, fmt.Sprintf("turn %d", i))

		last, ok := r.conv.Context().Get(conversation.ScopeConversation, "last_intent")
		if !ok {
			t.Fatalf("turn %d: no intent recorded", i)
		}
		if got := fmt.Sprintf("%v", last.Value); got != w.intent {
			t.Errorf("turn %d: intent = %q, want %q", i, got, w.intent)
		}
		if got := r.conv.State(); got != conversation.StateListening {
			t.Errorf("turn %d: state = %v, want listening", i, got)
		}
		if got := r.conv.Turns().Holder(); got != conversation.PartyCaller {
			t.Errorf("turn %d: floor = %v, want caller", i, got)
		}
		if got := completions(r.conv); got != w.completions {
			t.Errorf("turn %d: %d completions, want %d", i, got, w.completions)
		}
		if got := abandonments(r.conv); got != 0 {
			t.Errorf("turn %d: %d abandonments on a successful turn, want 0", i, got)
		}
		if got := r.conv.Turns().Count(); got < w.turnsAtLeast {
			t.Errorf("turn %d: turn count = %d, want >= %d", i, got, w.turnsAtLeast)
		}
		if r.conv.State().IsTerminal() {
			t.Fatalf("turn %d: conversation terminated", i)
		}
	}

	// Context survived both turns and output was produced for each.
	if frames, bytes := r.sink.stats(); frames == 0 || bytes == 0 {
		t.Errorf("no audio delivered across the lifecycle (%d frames, %d bytes)",
			frames, bytes)
	}
	if len(r.conv.Interruptions().History()) != 0 {
		t.Error("interruption history is not empty on a clean lifecycle")
	}
}

// ---------------------------------------------------------------------------
// Part 7 — session reuse
// ---------------------------------------------------------------------------

// TestT5_ReusedConversationIDStartsFresh proves that ending a session and
// reusing its ConversationID yields clean state, with no registry of any kind.
func TestT5_ReusedConversationIDStartsFresh(t *testing.T) {
	_, vi := build(t)
	const id = conversation.ConversationID("t5-reuse")

	first, conv1, err := openPlanner(vi, id)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := first.Handle(utter("transfer me to rajesh")); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if err := conv1.Context().Set(conversation.Entry{
		Key: "secret", Value: "from-first",
		Scope: conversation.ScopeConversation, Source: "t5",
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := first.Handle(conversation.Event{
		Kind:         conversation.EventInterrupt,
		Interruption: conversation.InterruptionUser,
		Party:        conversation.PartyCaller, Reason: "barge_in",
	}); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if err := conv1.End("t5_done"); err != nil {
		t.Fatalf("End: %v", err)
	}
	if !conv1.State().IsTerminal() {
		t.Fatal("the first conversation is not terminal")
	}

	// Same id, fresh conversation.
	_, conv2, err := openPlanner(vi, id)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if conv2 == conv1 {
		t.Fatal("reuse returned the SAME conversation object")
	}
	if conv2.State().IsTerminal() {
		t.Error("the reused session inherited a terminal state")
	}
	if _, found := conv2.Context().Get(conversation.ScopeConversation, "secret"); found {
		t.Error("the reused session inherited the previous conversation's context")
	}
	if got := len(conv2.Interruptions().History()); got != 0 {
		t.Errorf("the reused session inherited %d interruption records", got)
	}
	if got := completions(conv2); got != 0 {
		t.Errorf("the reused session inherited %d completions", got)
	}
	if _, found := conv2.Context().Get(conversation.ScopeConversation, "last_intent"); found {
		t.Error("the reused session inherited the previous intent")
	}
	// And it is usable.
	plan, err := conv2.Handle(utter("say that again"))
	if err != nil {
		t.Fatalf("reused session turn: %v", err)
	}
	if plan.Intent != "repeat" {
		t.Errorf("reused session intent = %q, want repeat", plan.Intent)
	}
}

// ---------------------------------------------------------------------------
// Part 5 — ActionAsk / ActionClarify / ActionConfirm, MEASURED
// ---------------------------------------------------------------------------

// TestT5_NonRespondSpeakingPlansNowSpeak was written in Phase 15 T5 to PIN
// GAP B: ActionAsk/Clarify/Confirm were routed to emptyTurn, so the planner
// decided to ask and the pipeline spoke nothing, stranding the floor.
//
// Phase 16 T1 CLOSED that gap by widening the speaking guard to the four
// actions that genuinely speak. The pin fired on the very run that fixed it —
// which is what it existed to do — so it is now a regression guard that the gap
// stays closed. The behavioural detail lives in clarification_test.go; this
// keeps the Phase 15 entry point honest.
func TestT5_NonRespondSpeakingPlansNowSpeak(t *testing.T) {
	// Same fixture as the original pin: an unfilled required slot -> ActionAsk.
	r := newT2Rig(t, t2Segments("i want to leave a message"), "t5-ask")
	r.driveTurn(t, 0)
	r.obs.awaitTurn(t, 30*time.Second, "ask turn")

	last, ok := r.conv.Context().Get(conversation.ScopeConversation, "last_intent")
	if !ok || fmt.Sprintf("%v", last.Value) != "leave_message" {
		t.Fatalf("last_intent = %v, want leave_message; this fixture no longer "+
			"produces the ask path", last.Value)
	}

	// GAP B CLOSED: the question is spoken and the floor comes back.
	if frames, _ := r.sink.stats(); frames == 0 {
		t.Error("the question was not spoken; GAP B has regressed")
	}
	if got := completions(r.conv); got != 1 {
		t.Errorf("%d speech completions for a spoken question, want 1", got)
	}
	if got := abandonments(r.conv); got != 0 {
		t.Errorf("%d abandonments for a successfully spoken question, want 0 — "+
			"a clarification is not an abandoned response", got)
	}
	if got := r.conv.Turns().Holder(); got != conversation.PartyCaller {
		t.Errorf("floor = %v after the question, want caller", got)
	}

	// And the caller's answer is heard rather than ignored as a backchannel.
	plan, err := r.conv.Handle(conversation.Event{
		Kind: conversation.EventUtterance,
		Utterance: conversation.Utterance{
			Text: "transfer me to rajesh", ASRConfidence: 0.94},
		Party: conversation.PartyCaller,
	})
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if plan.Reason == "backchannel" {
		t.Error("the caller's answer was ignored as a backchannel; GAP B has " +
			"regressed")
	}
}

// ---------------------------------------------------------------------------
// Part 6 — EventStart / EventGreetingComplete ownership, MEASURED
// ---------------------------------------------------------------------------

// TestT5_ConversationMustBeOpenedOutsideThePipeline is the evidence for GAP A.
//
// A conversation that has never received EventStart is in its initial state and
// cannot plan a response. The pipeline sends neither EventStart nor
// EventGreetingComplete — verified by source inspection in T3 and re-asserted
// here behaviourally — so something above it must open the conversation. Today
// nothing in production does; only the test rigs do, through the Bridge.
func TestT5_ConversationMustBeOpenedOutsideThePipeline(t *testing.T) {
	_, vi := build(t)

	// A conversation straight from the Bridge, with NO lifecycle events sent.
	unopened, err := vi.Bridge().Planner("t5-unopened", "")
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}
	plan, err := unopened.Handle(utter("transfer me to rajesh"))
	if err == nil && plan.Action == conversation.ActionRespond &&
		plan.Reason == "intent_accepted" {
		t.Fatal("an unopened conversation planned a normal response; the " +
			"EventStart/EventGreetingComplete gap recorded in the Phase 15 " +
			"report no longer reproduces and the report needs updating")
	}
	t.Logf("GAP A CONFIRMED: without EventStart/EventGreetingComplete an "+
		"utterance yields action=%v reason=%q err=%v", plan.Action, plan.Reason, err)

	// The same utterance on a properly opened conversation succeeds, which is
	// what makes the gap a production-ownership question rather than a defect
	// in the engine.
	opened, _, err := openPlanner(vi, "t5-opened")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, err := opened.Handle(utter("transfer me to rajesh"))
	if err != nil {
		t.Fatalf("opened conversation: %v", err)
	}
	if got.Intent != "request_transfer" || got.Reason != "intent_accepted" {
		t.Errorf("opened conversation: intent=%q reason=%q, want "+
			"request_transfer/intent_accepted", got.Intent, got.Reason)
	}
}

// ---------------------------------------------------------------------------
// Parts 2-4 — consolidated failure / interruption / cancellation regression
// ---------------------------------------------------------------------------

// TestT5_LifecycleInvariantsHoldOnEveryPath is the single-table regression over
// every terminating path Phase 15 touches.
func TestT5_LifecycleInvariantsHoldOnEveryPath(t *testing.T) {
	type expect struct {
		completions  int
		abandonments int
		interrupts   int
	}
	cases := []struct {
		name   string
		setup  func(*t2Rig)
		want   expect
		floor  conversation.Party
		denied bool
	}{
		{"success", func(*t2Rig) {}, expect{1, 0, 0}, conversation.PartyCaller, false},
		{"generation_failure", func(r *t2Rig) {
			r.model.FailNext(1, errors.New("boom"))
		}, expect{0, 1, 0}, conversation.PartyCaller, false},
		{"tts_failure", func(r *t2Rig) {
			r.tts.FailNext(errors.New("boom"))
		}, expect{0, 1, 0}, conversation.PartyCaller, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t5-inv-"+c.name)
			c.setup(r)
			r.driveTurn(t, 0)
			r.obs.awaitTurn(t, 30*time.Second, c.name)

			if got := completions(r.conv); got != c.want.completions {
				t.Errorf("%d completions, want %d", got, c.want.completions)
			}
			if got := abandonments(r.conv); got != c.want.abandonments {
				t.Errorf("%d abandonments, want %d", got, c.want.abandonments)
			}
			if got := len(r.conv.Interruptions().History()); got != c.want.interrupts {
				t.Errorf("%d interruption records, want %d", got, c.want.interrupts)
			}
			// INVARIANT: never both.
			if completions(r.conv) > 0 && abandonments(r.conv) > 0 {
				t.Error("a single turn produced BOTH a completion and an abandonment")
			}
			if got := r.conv.Turns().Holder(); got != c.floor {
				t.Errorf("floor = %v, want %v", got, c.floor)
			}
			if r.conv.State().IsTerminal() {
				t.Error("conversation terminated")
			}
		})
	}

	// Governance denial, with a genuinely empty policy registry.
	t.Run("governance_denial", func(t *testing.T) {
		r := newT2RigDenying(t, t2Segments("transfer me to rajesh"), "t5-inv-denied")
		r.driveTurn(t, 0)
		r.obs.awaitTurn(t, 30*time.Second, "denial")
		if got := completions(r.conv); got != 0 {
			t.Errorf("%d completions on a denial, want 0", got)
		}
		if got := abandonments(r.conv); got != 1 {
			t.Errorf("%d abandonments on a denial, want 1", got)
		}
		if got := r.conv.Turns().Holder(); got != conversation.PartyCaller {
			t.Errorf("floor = %v after denial, want caller", got)
		}
	})

	// Cancellation before any turn can complete.
	t.Run("cancellation", func(t *testing.T) {
		r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t5-inv-cancel")
		for i := uint64(0); i < t2EndAt; i++ {
			if err := r.pipeline.WriteFrame(t2Frame(i)); err != nil &&
				!errors.Is(err, voice.ErrBackpressure) &&
				!errors.Is(err, voice.ErrSessionClosed) {
				t.Fatalf("WriteFrame: %v", err)
			}
		}
		if err := r.pipeline.Cancel(voice.ReasonCallEnded); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		select {
		case <-r.pipeline.Done():
		case <-time.After(30 * time.Second):
			t.Fatal("cancellation did not finish")
		}
		if got := completions(r.conv); got != 0 {
			t.Errorf("%d completions after cancellation, want 0", got)
		}
		if got := abandonments(r.conv); got != 0 {
			t.Errorf("%d abandonments after cancellation, want 0 — nothing was "+
				"abandoned, the session ended", got)
		}
	})
}
