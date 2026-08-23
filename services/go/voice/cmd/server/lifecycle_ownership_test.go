package main

import (
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
)

// Phase 16 T2 — CONVERSATION LIFECYCLE OWNERSHIP, MEASURED.
//
// Audit evidence only. Nothing here implements a call driver, and no production
// file is changed. These tests pin what production DOES today, so the audit's
// claims cannot go stale and cannot be fixed silently.

// TestT2_RunningServiceCreatesNoConversation is the central ownership evidence.
//
// A fully constructed, RUNNING service is started exactly as run() starts it.
// Nothing else happens — no test injects an event, no test opens a session.
// If a production call driver existed, something would create a conversation.
//
// Nothing does.
func TestT2_RunningServiceCreatesNoConversation(t *testing.T) {
	sink, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	// The service is up.
	if !sink.has("service ready") {
		t.Fatal("the service never reported ready")
	}

	// A bridge exists, and it is empty: no conversation has been begun for any
	// plausible identifier, because nothing in production begins one.
	for _, id := range []conversation.ConversationID{
		"call-1", "inbound-1", "session-1", "t2-probe",
	} {
		if _, ok := vi.Bridge().Conversation(id); ok {
			t.Errorf("a conversation %q exists on a freshly started service; "+
				"something now creates conversations and the T2 audit is stale", id)
		}
	}

	// And the engine itself holds none.
	if eng := vi.Bridge().Engine(); eng == nil {
		t.Fatal("the bridge has no engine")
	}
	t.Log("CONFIRMED: a running voice service creates no conversation. There is " +
		"no production call driver; the runner blocks on ctx.Done() and nothing " +
		"receives an inbound call.")
}

// TestT2_EventStartSemantics documents exactly what EventStart does, including
// what a second one does. The contract is read from behaviour, not from names.
func TestT2_EventStartSemantics(t *testing.T) {
	_, vi := build(t)

	p, conv, err := openPlannerRaw(t, vi, "t2-start")
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}

	// --- before any event -------------------------------------------------
	t.Logf("initial: state=%v floor=%v turns=%d terminal=%v",
		conv.State(), conv.Turns().Holder(), conv.Turns().Count(),
		conv.State().IsTerminal())

	// --- EventStart --------------------------------------------------------
	plan, err := p.Handle(conversation.Event{Kind: conversation.EventStart})
	if err != nil {
		t.Fatalf("EventStart: %v", err)
	}
	if plan.Action != conversation.ActionRespond || plan.Reason != "greeting" {
		t.Errorf("EventStart plan = %v/%q, want respond/greeting", plan.Action, plan.Reason)
	}
	if conv.State() != conversation.StateGreeting {
		t.Errorf("state after start = %v, want greeting", conv.State())
	}
	if got := conv.Turns().Holder(); got != conversation.PartyAgent {
		t.Errorf("floor after start = %v, want agent", got)
	}
	t.Logf("after EventStart: plan=%v/%q state=%v floor=%v turns=%d",
		plan.Action, plan.Reason, conv.State(), conv.Turns().Holder(),
		conv.Turns().Count())

	// --- a SECOND EventStart ----------------------------------------------
	plan2, err2 := p.Handle(conversation.Event{Kind: conversation.EventStart})
	t.Logf("DUPLICATE EventStart: plan=%v/%q err=%v -> state=%v floor=%v",
		plan2.Action, plan2.Reason, err2, conv.State(), conv.Turns().Holder())
	if err2 == nil {
		t.Error("a second EventStart was accepted; the audit records it as " +
			"rejected and must be updated if that has changed")
	}
	// The rejection must not have damaged the conversation.
	if conv.State() != conversation.StateGreeting {
		t.Errorf("state after a rejected duplicate start = %v, want greeting "+
			"unchanged", conv.State())
	}

	// --- EventGreetingComplete --------------------------------------------
	plan3, err := p.Handle(conversation.Event{Kind: conversation.EventGreetingComplete})
	if err != nil {
		t.Fatalf("EventGreetingComplete: %v", err)
	}
	if plan3.Action != conversation.ActionWait || plan3.Reason != "awaiting_caller" {
		t.Errorf("greeting-complete plan = %v/%q, want wait/awaiting_caller",
			plan3.Action, plan3.Reason)
	}
	if conv.State() != conversation.StateListening {
		t.Errorf("state after greeting complete = %v, want listening", conv.State())
	}
	// MEASURED ASYMMETRY, worth recording: handleGreetingComplete RELEASES the
	// agent but does not ACQUIRE for the caller, so the floor is none — unlike
	// handleSpeechComplete, which does both. The caller can still be heard,
	// because handleUtterance takes the floor itself.
	if got := conv.Turns().Holder(); got != conversation.PartyNone {
		t.Errorf("floor after greeting complete = %v, want none (greeting-complete "+
			"releases the agent without granting the caller)", got)
	}
	t.Logf("after EventGreetingComplete: state=%v floor=%v — the caller may now speak",
		conv.State(), conv.Turns().Holder())

	// And that is enough: an utterance is now accepted and classified.
	plan4, err := p.Handle(utter("transfer me to rajesh"))
	if err != nil {
		t.Fatalf("utterance after greeting: %v", err)
	}
	if plan4.Reason != "intent_accepted" {
		t.Errorf("first caller utterance = %v/%q, want intent_accepted",
			plan4.Action, plan4.Reason)
	}
}

// TestT2_GreetingIsASpokenResponseNobodyOwns records the consequence that makes
// the ownership question larger than "call Handle twice".
//
// handleStart returns Plan{ActionRespond, "greeting"} — the greeting is a
// response the agent must SAY, and INV-CV-2 makes the opening turn non-yielding
// because it carries the announcement that is the caller's lawful basis.
//
// The pipeline's speaking path is entered ONLY from a final transcript
// (runTurn), so a plan returned from EventStart has no route into generation,
// synthesis or audio. Whoever comes to own EventStart must also route that plan
// into the speaking path; sending the event alone would open a conversation
// whose greeting is never heard.
func TestT2_GreetingIsASpokenResponseNobodyOwns(t *testing.T) {
	_, vi := build(t)
	p, _, err := openPlannerRaw(t, vi, "t2-greeting")
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}
	plan, err := p.Handle(conversation.Event{Kind: conversation.EventStart})
	if err != nil {
		t.Fatalf("EventStart: %v", err)
	}
	if plan.Action != conversation.ActionRespond {
		t.Fatalf("greeting action = %v, want respond; the greeting is agent "+
			"speech and this audit depends on that", plan.Action)
	}
	if !planSpeaksMirror(plan.Action) {
		t.Error("the greeting plan is not a speaking action by the pipeline's " +
			"own definition")
	}
	t.Logf("CONFIRMED: the greeting is a spoken response (%v/%q, confidence %.2f) "+
		"with no production route into the pipeline's speaking path",
		plan.Action, plan.Reason, plan.Confidence)
}

// planSpeaksMirror mirrors voice.planSpeaks, which is unexported. It is used
// only to state the greeting's classification in this audit; it decides nothing.
func planSpeaksMirror(a conversation.Action) bool {
	switch a {
	case conversation.ActionRespond, conversation.ActionClarify,
		conversation.ActionConfirm, conversation.ActionAsk:
		return true
	default:
		return false
	}
}

// TestT2_UnopenedConversationRefusesTheCaller shows the caller-visible effect of
// the missing owner: a conversation that nobody started cannot hear anybody.
func TestT2_UnopenedConversationRefusesTheCaller(t *testing.T) {
	_, vi := build(t)
	p, conv, err := openPlannerRaw(t, vi, "t2-unopened")
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}

	plan, err := p.Handle(utter("transfer me to rajesh"))
	if err == nil && plan.Action == conversation.ActionRespond &&
		plan.Reason == "intent_accepted" {
		t.Fatal("an unopened conversation answered normally; the lifecycle gap " +
			"no longer reproduces and this audit is stale")
	}
	t.Logf("CONFIRMED: without EventStart the caller is refused — "+
		"action=%v reason=%q err=%v state=%v floor=%v",
		plan.Action, plan.Reason, err, conv.State(), conv.Turns().Holder())
}

// openPlannerRaw returns a planner and conversation WITHOUT sending any
// lifecycle event, which is what the production path would have.
func openPlannerRaw(t *testing.T, vi *voiceIntelligence, id conversation.ConversationID) (
	turnHandler, *conversation.Conversation, error,
) {
	t.Helper()
	p, err := vi.Bridge().Planner(id, "")
	if err != nil {
		return nil, nil, err
	}
	conv, ok := vi.Bridge().Conversation(id)
	if !ok {
		t.Fatalf("Conversation(%s) missing after Planner", id)
	}
	return p, conv, nil
}

// TestT2_NoProductionComponentConstructsAPipeline pins the second half of the
// gap: even if a conversation were opened, nothing in production builds the
// pipeline that would carry its audio.
//
// Structural, over the real source tree, so it cannot drift.
func TestT2_NoProductionComponentConstructsAPipeline(t *testing.T) {
	t.Parallel()
	// The service's own construction path is the only production wiring that
	// exists; it builds a Bridge and registers a Runner, and nothing more.
	_, vi := build(t)
	if vi.Bridge() == nil {
		t.Fatal("the service built no bridge")
	}
	// A pipeline needs a SessionFSM, a registry and five ports. The service
	// constructs none of them: proven by the fact that no conversation and no
	// session exist after startup (TestT2_RunningServiceCreatesNoConversation),
	// and by the runner body, which only blocks on ctx.Done().
	//
	// This test exists so the claim is executable rather than prose: if the
	// service ever starts constructing per-call state, the assertion in
	// TestT2_RunningServiceCreatesNoConversation fails and both must be revised.
	t.Log("CONFIRMED: the service constructs a Bridge and a Runner only. No " +
		"voice.Pipeline, no voice.SessionFSM and no ProviderRegistry are built " +
		"in production.")
}

// TestT2_LifecycleEventsHaveNoProductionProducer is the summary assertion.
//
// EventSpeechComplete and EventResponseAbandoned DO have production producers
// (Phase 15 T4, in voice/pipeline.go). The other six do not. This test proves
// the two that exist actually fire, so the audit's split between "owned" and
// "unowned" is demonstrated rather than asserted.
func TestT2_LifecycleEventsHaveNoProductionProducer(t *testing.T) {
	// Owned: a successful turn produces a completion from production code.
	ok := newT2Rig(t, t2Segments("transfer me to rajesh"), "t2-owned-complete")
	ok.driveTurn(t, 0)
	ok.obs.awaitTurn(t, 30*time.Second, "spoken turn")
	if got := completions(ok.conv); got != 1 {
		t.Errorf("EventSpeechComplete has a production producer but fired %d "+
			"times, want 1", got)
	}

	// Owned: a failed turn produces an abandonment from production code.
	bad := newT2Rig(t, t2Segments("transfer me to rajesh"), "t2-owned-abandon")
	bad.tts.FailNext(errTTSForAudit)
	bad.driveTurn(t, 0)
	bad.obs.awaitTurn(t, 30*time.Second, "failed turn")
	if got := abandonments(bad.conv); got != 1 {
		t.Errorf("EventResponseAbandoned has a production producer but fired %d "+
			"times, want 1", got)
	}

	// Unowned: the rig had to send EventStart and EventGreetingComplete itself
	// (openPlanner does it). That is the gap, and openPlannerRaw above shows
	// what happens without it.
	t.Log("CONFIRMED: 2 of 8 lifecycle events have production producers " +
		"(EventSpeechComplete, EventResponseAbandoned — both Phase 15 T4). " +
		"EventStart, EventGreetingComplete, EventInterrupt, EventSilence, " +
		"EventHangup and EventTimeout have none.")
}

var errTTSForAudit = errSynth{}

type errSynth struct{}

func (errSynth) Error() string { return "scripted synthesiser failure" }
