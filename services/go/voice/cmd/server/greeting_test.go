package main

import (
	"errors"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/voice"
)

// Phase 16 T4 — GREETING LIFECYCLE, MEASURED.
//
// T4 set out to build the smallest production Call Driver. The audit below is
// why it stopped: the greeting cannot be spoken through the existing response
// path, so a driver could open a conversation but never deliver the greeting
// that opening it produces.
//
// Evidence only. No call driver is implemented, and no production file changed.

// TestT4_PipelineStartsInListeningWithNoGreetingPhase pins the structural
// mismatch between the two lifecycles.
//
// conversation has a greeting phase: EventStart -> StateGreeting, agent floor,
// and a greeting Plan that must be spoken. voice.SessionFSM has NO greeting
// state at all, and Pipeline.Start goes straight to StateListening while
// sending no conversation event.
func TestT4_PipelineStartsInListeningWithNoGreetingPhase(t *testing.T) {
	r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t4-nogreeting")

	// The voice session begins listening for the CALLER, immediately.
	if got := r.fsm.State(); got != voice.StateListening {
		t.Errorf("session state after Start = %v, want listening", got)
	}

	// The conversation, meanwhile, was opened by the test rig — not by the
	// pipeline. Nothing in production sends EventStart, so nothing in
	// production would have opened it at all.
	if got := completions(r.conv); got != 0 {
		t.Errorf("%d completions before any turn", got)
	}
	if frames, _ := r.sink.stats(); frames != 0 {
		t.Errorf("%d frames delivered before any caller audio — the pipeline "+
			"spoke something unprompted", frames)
	}
	t.Log("CONFIRMED: Pipeline.Start emits no conversation event and delivers " +
		"no greeting audio; voice.SessionFSM has no greeting state.")
}

// TestT4_GreetingPlanHasNoRouteIntoTheSpeakingPath is the blocking finding.
//
// EventStart returns Plan{ActionRespond, "greeting"} — by the pipeline's own
// planSpeaks predicate (Phase 16 T1) that is a SPEAKING action. But the
// pipeline's speaking path is entered only from runTurn, which is unexported
// and called only from the transcript loop. The Pipeline exposes Start,
// WriteFrame, InvokeTool, Cancel, Disconnect, Fail, Done, Wait, Err and three
// counters — and nothing that speaks a Plan.
//
// So the greeting Plan is returned to whoever sent EventStart and is dropped.
func TestT4_GreetingPlanHasNoRouteIntoTheSpeakingPath(t *testing.T) {
	r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t4-greetingplan")

	// The rig's own conversation was ALREADY opened by openPlanner, which sent
	// EventStart, received the greeting plan and discarded it — precisely the
	// behaviour under examination. A second EventStart is rejected
	// (`listening -> greeting is not declared`), so the plan is observed on a
	// separate, unopened conversation.
	_, vi := build(t)
	raw, _, err := openPlannerRaw(t, vi, "t4-greetingplan-raw")
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}
	greeting, err := raw.Handle(conversation.Event{Kind: conversation.EventStart})
	if err != nil {
		t.Fatalf("EventStart: %v", err)
	}
	if greeting.Action != conversation.ActionRespond || greeting.Reason != "greeting" {
		t.Fatalf("greeting plan = %v/%q, want respond/greeting",
			greeting.Action, greeting.Reason)
	}

	// The plan is in the driver's hand. Nothing can be done with it: no audio
	// is produced, no generation runs, no completion is recorded.
	if r.model.Calls() != 0 {
		t.Errorf("the generator ran %d times for a greeting the pipeline "+
			"cannot see", r.model.Calls())
	}
	if r.tts.Opened() != 0 {
		t.Errorf("TTS opened %d times for an unspeakable greeting", r.tts.Opened())
	}
	if frames, _ := r.sink.stats(); frames != 0 {
		t.Errorf("%d greeting frames delivered", frames)
	}
	if got := completions(r.conv); got != 0 {
		t.Errorf("%d completions for a greeting that was never spoken", got)
	}
	t.Logf("CONFIRMED: greeting plan = %v/%q (confidence %.2f) is returned to "+
		"the caller of EventStart and dropped; no exported Pipeline method "+
		"speaks a Plan", greeting.Action, greeting.Reason, greeting.Confidence)
}

// TestT4_NaiveDriverBreaksTheFirstCallerTurn measures what a driver that sent
// EventStart WITHOUT being able to speak the greeting would actually produce.
//
// This is the concrete reason T4 stopped rather than shipping a partial driver:
// opening the conversation without delivering the greeting leaves it in the
// greeting phase, and the caller's first utterance does not get classified.
func TestT4_NaiveDriverBreaksTheFirstCallerTurn(t *testing.T) {
	_, vi := build(t)

	// A "driver" that opens the conversation but cannot speak the greeting.
	p, conv, err := openPlannerRaw(t, vi, "t4-naive")
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}
	if _, err := p.Handle(conversation.Event{Kind: conversation.EventStart}); err != nil {
		t.Fatalf("EventStart: %v", err)
	}
	if got := conv.State(); got != conversation.StateGreeting {
		t.Fatalf("state after start = %v, want greeting", got)
	}

	// The caller now speaks. Because the greeting was never delivered, nothing
	// ever sent EventGreetingComplete, and the conversation is still greeting.
	plan, err := p.Handle(utter("transfer me to rajesh"))

	if err == nil && plan.Reason == "intent_accepted" {
		t.Fatal("the first caller utterance was classified normally despite the " +
			"greeting never completing; the T4 finding no longer reproduces and " +
			"this test must be updated")
	}
	t.Logf("CONFIRMED: with the greeting undelivered, the first caller utterance "+
		"yields action=%v reason=%q err=%v (state=%v floor=%v) — the caller is "+
		"not heard", plan.Action, plan.Reason, err, conv.State(), conv.Turns().Holder())
}

// TestT4_FrozenContractAlreadySupportsTheFullLifecycle proves the blocker is
// NOT the frozen conversation module.
//
// Driven entirely through the frozen contract, the complete lifecycle works:
// start -> greeting -> greeting complete -> caller utterance -> plan. Nothing
// in conversation needs to change; what is missing is a production component
// able to SPEAK the greeting between steps two and three.
func TestT4_FrozenContractAlreadySupportsTheFullLifecycle(t *testing.T) {
	_, vi := build(t)
	p, conv, err := openPlannerRaw(t, vi, "t4-contract")
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}

	greeting, err := p.Handle(conversation.Event{Kind: conversation.EventStart})
	if err != nil {
		t.Fatalf("EventStart: %v", err)
	}
	if greeting.Action != conversation.ActionRespond {
		t.Errorf("greeting action = %v, want respond", greeting.Action)
	}
	if conv.State() != conversation.StateGreeting ||
		conv.Turns().Holder() != conversation.PartyAgent {
		t.Errorf("after start: state=%v floor=%v, want greeting/agent",
			conv.State(), conv.Turns().Holder())
	}

	// This is the step a real driver could only take AFTER the greeting audio
	// had actually been delivered.
	if _, err := p.Handle(conversation.Event{
		Kind: conversation.EventGreetingComplete}); err != nil {
		t.Fatalf("EventGreetingComplete: %v", err)
	}
	if conv.State() != conversation.StateListening {
		t.Errorf("after greeting complete: state=%v, want listening", conv.State())
	}

	// And the caller is then heard normally.
	plan, err := p.Handle(utter("transfer me to rajesh"))
	if err != nil {
		t.Fatalf("first caller utterance: %v", err)
	}
	if plan.Intent != "request_transfer" || plan.Reason != "intent_accepted" {
		t.Errorf("first utterance = %q/%q, want request_transfer/intent_accepted",
			plan.Intent, plan.Reason)
	}
	t.Log("CONFIRMED: the frozen conversation contract already supports the " +
		"complete lifecycle. No frozen amendment is required; the gap is a " +
		"production component that can speak the greeting.")
}

// TestT4_GreetingGenerationPayloadIsAbsent records the second-order problem,
// so it is not discovered late if the speaking route is ever added.
//
// The pipeline builds its generation request from the caller's transcript
// (Messages: [{RoleUser, final.Text}]). A greeting has no transcript — there is
// no caller utterance yet — so even with a speaking route, the generator would
// be asked to produce a greeting with an EMPTY user message and no indication
// that a greeting is what is wanted.
//
// With the deterministic fake this is invisible; with a real model it is the
// difference between a greeting and silence. Recorded, not fixed: supplying it
// is a generation-semantics change outside T4's scope.
func TestT4_GreetingGenerationPayloadIsAbsent(t *testing.T) {
	r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t4-payload")

	// Drive a normal turn so the generator records a request to inspect.
	r.driveTurn(t, 0)
	r.obs.awaitTurn(t, 30*time.Second, "turn")

	req := r.model.LastRequest()
	if req == nil {
		t.Fatal("the generator recorded no request")
	}
	// The request carries the caller's words and the system instruction, and
	// nothing that identifies which PLAN is being spoken.
	t.Logf("generation request for a normal turn: %d message(s); a greeting "+
		"would arrive with no caller message at all and no plan identity",
		len(req.Messages))
	if len(req.Messages) == 0 {
		t.Error("the generation request carried no messages")
	}
}

// TestT4_CancellationOfAnOpenedConversationIsClean confirms the one lifecycle
// property T4 could verify without a driver: shutdown stays correct.
func TestT4_CancellationOfAnOpenedConversationIsClean(t *testing.T) {
	// The rig's conversation is already open (openPlanner sent EventStart and
	// EventGreetingComplete); a second EventStart would be rejected.
	r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t4-cancel")

	if err := r.pipeline.Cancel(voice.ReasonCallEnded); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case <-r.pipeline.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the pipeline did not finish after Cancel")
	}
	if err := r.pipeline.WriteFrame(t2Frame(99)); err == nil {
		t.Error("a cancelled pipeline accepted another frame")
	}

	// Cancelling a call that never spoke must fabricate nothing.
	if got := completions(r.conv); got != 0 {
		t.Errorf("%d completions after cancelling an unspoken call, want 0", got)
	}
	if got := abandonments(r.conv); got != 0 {
		t.Errorf("%d abandonments after cancellation, want 0 — the call was "+
			"cancelled, not abandoned mid-response", got)
	}
	if got := len(r.conv.Interruptions().History()); got != 0 {
		t.Errorf("%d interruption records after cancellation, want 0", got)
	}
	_ = errors.New
}
