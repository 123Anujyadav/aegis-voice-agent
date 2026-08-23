package main

import (
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
)

// Phase 16 T5 — GREETING COMPLETION CONTRACT, MEASURED.
//
// T5 is a design audit; these tests pin the frozen behaviours the design rests
// on, so a future implementation cannot be built on a premise that has since
// drifted. No production code is changed and no greeting is implemented.

// TestT5_BothCompletionEventsAreLegalFromGreeting is the pivotal measurement.
//
// Both EventSpeechComplete and EventGreetingComplete are ACCEPTED from
// StateGreeting — greeting -> listening is a declared transition
// (state.go:232) — but they differ observably, and the difference is what makes
// the choice a real decision rather than a preference.
func TestT5_BothCompletionEventsAreLegalFromGreeting(t *testing.T) {
	_, vi := build(t)

	// --- EventSpeechComplete: acquires the caller floor -------------------
	pa, ca, err := openPlannerRaw(t, vi, "t5-speech")
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}
	if _, err := pa.Handle(conversation.Event{Kind: conversation.EventStart}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := pa.Handle(conversation.Event{
		Kind: conversation.EventSpeechComplete}); err != nil {
		t.Fatalf("EventSpeechComplete from greeting was rejected: %v", err)
	}
	if got := ca.State(); got != conversation.StateListening {
		t.Errorf("speech-complete: state = %v, want listening", got)
	}
	if got := ca.Turns().Holder(); got != conversation.PartyCaller {
		t.Errorf("speech-complete: floor = %v, want caller", got)
	}

	// --- EventGreetingComplete: leaves the floor unheld -------------------
	pb, cb, err := openPlannerRaw(t, vi, "t5-greeting")
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}
	if _, err := pb.Handle(conversation.Event{Kind: conversation.EventStart}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := pb.Handle(conversation.Event{
		Kind: conversation.EventGreetingComplete}); err != nil {
		t.Fatalf("EventGreetingComplete: %v", err)
	}
	if got := cb.State(); got != conversation.StateListening {
		t.Errorf("greeting-complete: state = %v, want listening", got)
	}
	if got := cb.Turns().Holder(); got != conversation.PartyNone {
		t.Errorf("greeting-complete: floor = %v, want none — it releases the "+
			"agent without granting the caller", got)
	}

	// Both leave a usable conversation: the first caller utterance classifies.
	for name, p := range map[string]turnHandler{"speech": pa, "greeting": pb} {
		plan, err := p.Handle(utter("transfer me to rajesh"))
		if err != nil {
			t.Fatalf("%s: first utterance: %v", name, err)
		}
		if plan.Reason != "intent_accepted" {
			t.Errorf("%s: first utterance reason = %q, want intent_accepted",
				name, plan.Reason)
		}
	}
}

// TestT5_GreetingCompleteWouldCorruptAnOrdinaryTurn is why the completion event
// must be chosen per-plan rather than adopted globally.
//
// EventGreetingComplete is ACCEPTED on an ordinary speaking turn. Using it
// everywhere would therefore "work", while recording every turn in the trace as
// a greeting completion and leaving the floor unheld instead of with the caller.
func TestT5_GreetingCompleteWouldCorruptAnOrdinaryTurn(t *testing.T) {
	_, vi := build(t)
	p, conv, err := openPlannerRaw(t, vi, "t5-ordinary")
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}
	if _, err := p.Handle(conversation.Event{Kind: conversation.EventStart}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := p.Handle(conversation.Event{
		Kind: conversation.EventGreetingComplete}); err != nil {
		t.Fatalf("greeting complete: %v", err)
	}
	if _, err := p.Handle(utter("transfer me to rajesh")); err != nil {
		t.Fatalf("turn: %v", err)
	}

	// An ordinary turn, finished with the GREETING event.
	if _, err := p.Handle(conversation.Event{
		Kind: conversation.EventGreetingComplete}); err != nil {
		t.Fatalf("greeting-complete on an ordinary turn was rejected: %v", err)
	}
	if got := conv.Turns().Holder(); got != conversation.PartyNone {
		t.Errorf("floor = %v, want none — this is the corruption: an ordinary "+
			"turn should end with the caller holding the floor", got)
	}
	greetings := 0
	for _, tr := range conv.Trace() {
		if tr.Trigger == conversation.TriggerGreetingComplete {
			greetings++
		}
	}
	if greetings != 2 {
		t.Errorf("%d greeting-complete triggers recorded, want 2 — the trace "+
			"cannot distinguish the greeting from an ordinary turn", greetings)
	}
}

// TestT5_PlanNextStateDiscriminatesTheGreeting establishes the typed
// discriminator the design depends on.
//
// The greeting plan carries NextState=StateGreeting; an ordinary response
// carries NextState=StateSpeaking. That is an existing, typed field of the
// frozen Plan — no string comparison on Reason, and nothing invented.
func TestT5_PlanNextStateDiscriminatesTheGreeting(t *testing.T) {
	_, vi := build(t)

	p, _, err := openPlannerRaw(t, vi, "t5-discriminator")
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}
	greeting, err := p.Handle(conversation.Event{Kind: conversation.EventStart})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if greeting.NextState != conversation.StateGreeting {
		t.Errorf("greeting plan NextState = %v, want greeting", greeting.NextState)
	}

	if _, err := p.Handle(conversation.Event{
		Kind: conversation.EventGreetingComplete}); err != nil {
		t.Fatalf("greeting complete: %v", err)
	}
	ordinary, err := p.Handle(utter("transfer me to rajesh"))
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if ordinary.NextState != conversation.StateSpeaking {
		t.Errorf("ordinary plan NextState = %v, want speaking", ordinary.NextState)
	}
	if greeting.NextState == ordinary.NextState {
		t.Error("the greeting and an ordinary response are indistinguishable by " +
			"NextState; the proposed discriminator does not hold")
	}
}

// TestT5_ApprovedFailureAndInterruptionSemanticsApplyToGreeting shows the two
// already-approved mechanisms need no extension for the greeting phase.
func TestT5_ApprovedFailureAndInterruptionSemanticsApplyToGreeting(t *testing.T) {
	_, vi := build(t)

	// EventResponseAbandoned during the greeting.
	pa, ca, err := openPlannerRaw(t, vi, "t5-greet-abandon")
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}
	if _, err := pa.Handle(conversation.Event{Kind: conversation.EventStart}); err != nil {
		t.Fatalf("start: %v", err)
	}
	plan, err := pa.Handle(conversation.Event{
		Kind: conversation.EventResponseAbandoned, Reason: "greeting_failed"})
	if err != nil {
		t.Fatalf("abandon during greeting: %v", err)
	}
	if plan.Reason != "response_abandoned" {
		t.Errorf("abandon plan reason = %q, want response_abandoned", plan.Reason)
	}
	if ca.State() != conversation.StateListening ||
		ca.Turns().Holder() != conversation.PartyCaller {
		t.Errorf("after greeting abandonment: state=%v floor=%v, want listening/caller",
			ca.State(), ca.Turns().Holder())
	}
	if next, err := pa.Handle(utter("transfer me to rajesh")); err != nil ||
		next.Reason != "intent_accepted" {
		t.Errorf("caller not heard after a failed greeting: %v / %q", err, next.Reason)
	}

	// EventInterrupt during the greeting.
	pb, cb, err := openPlannerRaw(t, vi, "t5-greet-interrupt")
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}
	if _, err := pb.Handle(conversation.Event{Kind: conversation.EventStart}); err != nil {
		t.Fatalf("start: %v", err)
	}
	plan, err = pb.Handle(conversation.Event{
		Kind:         conversation.EventInterrupt,
		Interruption: conversation.InterruptionUser,
		Party:        conversation.PartyCaller, Reason: "barge_in",
	})
	if err != nil {
		t.Fatalf("interrupt during greeting: %v", err)
	}
	if plan.Reason != "interrupted_user" {
		t.Errorf("interrupt plan reason = %q, want interrupted_user", plan.Reason)
	}
	if got := len(cb.Interruptions().History()); got != 1 {
		t.Errorf("%d interruption records, want 1", got)
	}
	if cb.Turns().Holder() != conversation.PartyCaller {
		t.Errorf("floor = %v after greeting barge-in, want caller",
			cb.Turns().Holder())
	}
	if next, err := pb.Handle(utter("transfer me to rajesh")); err != nil ||
		next.Reason != "intent_accepted" {
		t.Errorf("caller not heard after interrupting the greeting: %v / %q",
			err, next.Reason)
	}
}
