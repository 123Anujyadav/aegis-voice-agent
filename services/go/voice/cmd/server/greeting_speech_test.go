package main

import (
	"context"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/voice"
)

// Phase 16 T6 — THE GREETING, SPOKEN, THROUGH THE REAL SEAM.
//
// Everything here runs against the assembly Phase 15 T2 built: the real
// Pipeline, the real registry and router, the real governance engine with the
// production baseline, the real generation kernel, and the real voiceintel
// bridge as the Planner. The only scripted parts are the frozen modules' own
// exported fakes and the one Intelligence port that has none.
//
// The conversation is handed over exactly where a call driver would hand it
// over: EventStart sent, greeting plan in hand, NOT yet spoken.
//
// REAL INBOUND TRANSPORT IS NOT PRESENT. No driver is implemented here, and
// nothing below claims one.

// greetings counts opening-turn completions recorded by the frozen engine.
//
// Read from the engine's own trace, like completions and abandonments. The
// TRIGGER is the assertion: greeting-complete and speech-complete both leave the
// conversation in listening, so the state cannot tell them apart and the trace
// is the only place the difference is visible.
func greetings(conv *conversation.Conversation) int {
	if conv == nil {
		return 0
	}
	n := 0
	for _, tr := range conv.Trace() {
		if tr.Trigger == conversation.TriggerGreetingComplete {
			n++
		}
	}
	return n
}

// newT6Rig is the T2 assembly with the opening turn still pending.
func newT6Rig(t *testing.T, sessionID string) *t2Rig {
	t.Helper()
	return newT2RigOpts(t, t2Segments("transfer me to rajesh"), sessionID, t2RigOpts{
		baselinePolicies: true,
		pendingGreeting:  true,
		// The deployment's instruction. Deliberately an INSTRUCTION and not a
		// greeting: no test in this repository authors the words the caller
		// hears, and the deterministic provider ignores the content anyway.
		greetingSystem: "open the call with the lawful-basis announcement",
	})
}

// TestT6_TheGreetingPlanIsSpokenThroughTheRealPipeline closes the Phase 16 T4
// blocker.
//
// T4 measured that the greeting plan was returned to whoever sent EventStart and
// dropped: no generation, no synthesis, no audio, and a caller whose first words
// came back "floor_queued" behind an announcement that would never finish. The
// same plan, submitted to the same pipeline, now reaches the caller.
func TestT6_TheGreetingPlanIsSpokenThroughTheRealPipeline(t *testing.T) {
	r := newT6Rig(t, "t6-greeting")

	// The conversation is where a driver would hand it over.
	if got := r.conv.State(); got != conversation.StateGreeting {
		t.Fatalf("state before the greeting is spoken = %v, want greeting", got)
	}
	if r.greeting.Action != conversation.ActionRespond || r.greeting.Reason != "greeting" {
		t.Fatalf("the frozen engine returned %v/%q from EventStart, want respond/greeting",
			r.greeting.Action, r.greeting.Reason)
	}

	if err := r.pipeline.Speak(context.Background(), r.greeting); err != nil {
		t.Fatalf("Speak: %v", err)
	}

	if got := r.model.Calls(); got != 1 {
		t.Errorf("the generator ran %d times for the greeting, want 1", got)
	}
	if got := r.tts.Opened(); got != 1 {
		t.Errorf("synthesis opened %d times for the greeting, want 1", got)
	}
	frames, bytes := r.sink.stats()
	if frames == 0 || bytes == 0 {
		t.Errorf("the greeting delivered %d frames / %d bytes; the caller heard "+
			"nothing", frames, bytes)
	}
	if got := r.stt.Opened(); got != 0 {
		t.Errorf("recognition opened %d times for the greeting; the caller has "+
			"not spoken yet", got)
	}
}

// TestT6_TheGreetingCompletionTraceIsCorrect is the critical assertion.
//
// Not the final state — EventSpeechComplete would produce the same one. Five
// things together, of which the trigger and the floor are the two that separate
// a correct opening turn from one recorded as an ordinary answer.
func TestT6_TheGreetingCompletionTraceIsCorrect(t *testing.T) {
	r := newT6Rig(t, "t6-trace")

	if err := r.pipeline.Speak(context.Background(), r.greeting); err != nil {
		t.Fatalf("Speak: %v", err)
	}

	if got := greetings(r.conv); got != 1 {
		t.Errorf("the trace records %d greeting completions, want exactly 1", got)
	}
	if got := completions(r.conv); got != 0 {
		t.Errorf("the trace records %d SPEECH completions for the opening turn, "+
			"want 0: the greeting is not an answer to anything the caller said", got)
	}
	if got := abandonments(r.conv); got != 0 {
		t.Errorf("the trace records %d abandonments for a greeting that was "+
			"delivered, want 0", got)
	}
	if got := r.conv.State(); got != conversation.StateListening {
		t.Errorf("state after the greeting = %v, want listening", got)
	}

	// The floor, at the EXACT frozen contract result. handleGreetingComplete
	// RELEASES the agent's turn to nobody (engine.go:549) — it does not hand it
	// to the caller, which is what handleSpeechComplete does. Asserting
	// PartyCaller here would pass with the wrong event.
	if got := r.conv.Turns().Holder(); got != conversation.PartyNone {
		t.Errorf("floor after the greeting = %v, want none: the opening turn is "+
			"released rather than handed over", got)
	}

	// And the voice session is ready for the caller.
	if got := r.fsm.State(); got != voice.StateListening {
		t.Errorf("session state after the greeting = %v, want listening", got)
	}
}

// TestT6_TheCallerIsHeardAfterTheGreeting is the whole point of the lifecycle.
//
// T4's naive-driver measurement was that the first caller utterance came back
// action=ignore reason="floor_queued". With the greeting actually spoken, the
// same utterance classifies normally.
func TestT6_TheCallerIsHeardAfterTheGreeting(t *testing.T) {
	r := newT6Rig(t, "t6-firstturn")

	if err := r.pipeline.Speak(context.Background(), r.greeting); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	// The greeting is a completed TURN and left its own token on the observer.
	// Consuming it here is what makes the wait below wait for the CALLER's turn
	// rather than returning immediately on the greeting's.
	r.obs.awaitTurn(t, 30*time.Second, "the greeting")

	r.driveTurn(t, 0)
	r.obs.awaitTurn(t, 30*time.Second, "first caller turn after the greeting")

	// The caller's turn ran as an ordinary turn: recognised, planned, spoken.
	if got := r.stt.Opened(); got != 1 {
		t.Errorf("recognition opened %d times for the caller's turn, want 1", got)
	}
	if got := completions(r.conv); got != 1 {
		t.Errorf("the trace records %d speech completions after one caller turn, "+
			"want 1", got)
	}
	if got := greetings(r.conv); got != 1 {
		t.Errorf("the trace records %d greeting completions after a caller turn, "+
			"want the original 1", got)
	}
	if got := r.conv.Turns().Holder(); got != conversation.PartyCaller {
		t.Errorf("floor after the caller's turn = %v, want caller", got)
	}
}

// TestT6_AGreetingThatCannotBeSpokenReleasesTheOpeningTurn.
//
// The failure that matters most on this platform. INV-CV-2 makes the opening
// turn NON-YIELDING because it carries the lawful-basis announcement, so a
// greeting that is accepted and then produces no audio strands the floor on the
// agent — and the caller cannot take it back by talking, because the turn
// manager queues them behind it.
//
// Governance denial is used because it is a real refusal by the real engine: the
// rig is built without the production baseline, so the policy registry is empty
// and the decision is a genuine deny-by-default.
func TestT6_AGreetingThatCannotBeSpokenReleasesTheOpeningTurn(t *testing.T) {
	r := newT2RigOpts(t, t2Segments("transfer me to rajesh"), "t6-denied", t2RigOpts{
		baselinePolicies: false,
		pendingGreeting:  true,
		greetingSystem:   "open the call with the lawful-basis announcement",
	})

	err := r.pipeline.Speak(context.Background(), r.greeting)
	if err == nil {
		t.Fatal("Speak reported success for a greeting governance refused")
	}

	if frames, _ := r.sink.stats(); frames != 0 {
		t.Errorf("%d frames were delivered for a refused greeting", frames)
	}
	if got := abandonments(r.conv); got != 1 {
		t.Errorf("the trace records %d abandonments, want exactly 1", got)
	}
	if got := greetings(r.conv) + completions(r.conv); got != 0 {
		t.Errorf("the trace records %d completions for a greeting that was never "+
			"spoken, want 0", got)
	}

	// THE FLOOR IS BACK. This is what the abandonment exists for.
	if got := r.conv.State(); got != conversation.StateListening {
		t.Errorf("state after an abandoned greeting = %v, want listening", got)
	}
	if got := r.conv.Turns().Holder(); got != conversation.PartyCaller {
		t.Errorf("floor after an abandoned greeting = %v, want caller: nothing "+
			"was said, so the caller must be able to speak", got)
	}

	// And the caller is genuinely heard, not merely nominally holding the floor.
	// Asked of the CONVERSATION directly: whether the floor came back is a
	// dialogue question, not an audio one.
	plan, herr := r.conv.Handle(utter("transfer me to rajesh"))
	if herr != nil {
		t.Fatalf("the caller's first utterance after an abandoned greeting "+
			"failed: %v", herr)
	}
	if plan.Reason == "floor_queued" {
		t.Errorf("the caller is still queued behind the abandoned greeting "+
			"(action=%v reason=%q)", plan.Action, plan.Reason)
	}
}

// TestT6_AnOrdinaryPlanSubmittedMidConversationCompletesAsSpeech.
//
// The seam is not greeting-only, and the discriminator has to work in both
// directions. EventGreetingComplete is ACCEPTED mid-conversation by the frozen
// engine, so a pipeline that sent it for every submitted plan would record an
// opening turn in the middle of a call and release the floor to nobody.
func TestT6_AnOrdinaryPlanSubmittedMidConversationCompletesAsSpeech(t *testing.T) {
	r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t6-ordinary")
	before := greetings(r.conv)

	// A REAL plan, from the real conversation. Not a literal: producing a plan
	// is what moves the conversation into speaking, and a submitted plan the
	// conversation never decided would be a response to a turn that never
	// happened — which the frozen engine correctly refuses to complete.
	plan, err := r.conv.Handle(utter("transfer me to rajesh"))
	if err != nil {
		t.Fatalf("utterance: %v", err)
	}
	if plan.Action != conversation.ActionRespond {
		t.Fatalf("the caller's utterance produced %v/%q, want a spoken response",
			plan.Action, plan.Reason)
	}
	if plan.NextState != conversation.StateSpeaking {
		t.Fatalf("an ordinary response plan carries NextState=%v, want speaking",
			plan.NextState)
	}

	if err := r.pipeline.Speak(context.Background(), plan); err != nil {
		t.Fatalf("Speak: %v", err)
	}

	if got := completions(r.conv); got != 1 {
		t.Errorf("the trace records %d speech completions, want 1", got)
	}
	if got := greetings(r.conv); got != before {
		t.Errorf("the trace gained %d greeting completions mid-conversation",
			got-before)
	}
	if got := r.conv.Turns().Holder(); got != conversation.PartyCaller {
		t.Errorf("floor after an ordinary submitted response = %v, want caller", got)
	}
}

// TestT6_TheServiceStillDoesNotDriveCalls is the boundary statement, asserted.
//
// T6 added a seam. It did not add a call driver, a transport, or an owner for
// any of the lifecycle steps T2 found ownerless. Every conversation in this file
// is opened by a TEST, and that remains the honest description of the system.
func TestT6_TheServiceStillDoesNotDriveCalls(t *testing.T) {
	svc, vi := build(t)

	if svc == nil || vi == nil {
		t.Fatal("the service did not build")
	}

	// Nothing in the service opens a conversation, mints a pipeline or sends
	// EventStart. If that ever changes, this test should be replaced by tests of
	// the driver — not deleted.
	raw, conv, err := openPlannerRaw(t, vi, "t6-nodriver")
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}
	if got := conv.State(); got != conversation.StateIdle {
		t.Errorf("a freshly opened conversation is in %v; the service started it "+
			"without a driver", got)
	}
	if got := greetings(conv); got != 0 {
		t.Errorf("%d greeting completions on a conversation nobody drove", got)
	}
	_ = raw

	t.Log("REAL INBOUND TRANSPORT IS NOT PRESENT. Pipeline.Speak is a seam a " +
		"driver can use; no driver exists, and no conversation is opened by " +
		"production code.")
}
