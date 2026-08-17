package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/intent"
)

// Phase 14 T4 — end-to-end intelligence path.
//
// "End-to-end" here means the INTELLIGENCE path, not audio. No pipeline, no
// provider, no model, no network: T1 established that every shipped provider
// shells out to an external binary, so the audio-bearing stages stay out of
// scope and this file must not be read as proving them.
//
// T2 proved buildService returns wired components; T3 proved platform.Service
// registers and drives the runner. Neither showed the path DISCRIMINATES — a
// single hard-coded intent would have satisfied both. T4 adds that, plus the
// turn/plan/response side.
//
// ONE HONEST DISTINCTION, verified against current source:
// intent.ClassifyTurn has NO production caller. The conversation engine owns
// turn-taking itself (TurnManager, Expectation, the FSM). So this file proves
// two separate things and does not conflate them:
//
//   - the FROZEN turn semantics ARE reached by the service path
//     (floor, expectation, state, turn count)
//   - intent.ClassifyTurn COMPOSES over the service's real outputs
//     (demonstrated explicitly, never claimed to be invoked by the service)

// e2eCase is one deterministic utterance and the exact contract it must produce
// through the service wiring.
type e2eCase struct {
	name       string
	utterance  string
	wantIntent conversation.IntentName
	wantAction conversation.Action
	wantReason string
	// wantTerminal records whether this turn ends the conversation.
	wantTerminal bool
}

// e2eCases uses only names declared in the current lexicon. Verified against
// packages/go/intent/lexicon.go: greeting, caller_identity, call_purpose,
// leave_message, request_callback, request_transfer, repeat, hold, end_call,
// plus the frozen reserved unknown/fallback/affirm/deny.
//
// "information_request" and "emergency" do NOT exist in this vocabulary and are
// deliberately not used.
func e2eCases() []e2eCase {
	return []e2eCase{
		{
			name: "callback_with_number", utterance: "please call me back on 9876543210",
			wantIntent: intent.IntentRequestCallback,
			wantAction: conversation.ActionRespond, wantReason: "intent_accepted",
		},
		{
			name: "transfer", utterance: "transfer me to rajesh",
			wantIntent: intent.IntentRequestTransfer,
			wantAction: conversation.ActionRespond, wantReason: "intent_accepted",
		},
		{
			name: "repeat", utterance: "say that again",
			wantIntent: intent.IntentRepeat,
			wantAction: conversation.ActionRespond, wantReason: "intent_accepted",
		},
		{
			name: "hold", utterance: "can you hold on a moment",
			wantIntent: intent.IntentHold,
			wantAction: conversation.ActionRespond, wantReason: "intent_accepted",
		},
		{
			name: "caller_identity", utterance: "this is rajesh sharma calling",
			wantIntent: intent.IntentCallerIdentity,
			wantAction: conversation.ActionRespond, wantReason: "intent_accepted",
		},
		{
			// A different response STRATEGY: understood, but a required slot is
			// unfilled, so the planner asks rather than answers.
			name: "leave_message_missing_slot", utterance: "i want to leave a message",
			wantIntent: intent.IntentLeaveMessage,
			wantAction: conversation.ActionAsk, wantReason: "clarify_missing_slot",
		},
	}
}

// openSession starts a conversation on the service's bridge and opens the floor.
//
// The frozen turn-taking floor matters: the agent holds it through the greeting,
// so an utterance before EventGreetingComplete is queued rather than planned.
func openSession(t *testing.T, vi *voiceIntelligence, id string) interface {
	Handle(conversation.Event) (conversation.Plan, error)
} {
	t.Helper()
	p, err := vi.Bridge().Planner(conversation.ConversationID(id), "")
	if err != nil {
		t.Fatalf("Planner(%s): %v", id, err)
	}
	for _, e := range []conversation.Event{
		{Kind: conversation.EventStart},
		{Kind: conversation.EventGreetingComplete},
	} {
		if _, err := p.Handle(e); err != nil {
			t.Fatalf("%s %v: %v", id, e.Kind, err)
		}
	}
	return p
}

func utter(text string) conversation.Event {
	return conversation.Event{
		Kind:      conversation.EventUtterance,
		Utterance: conversation.Utterance{Text: text, ASRConfidence: 0.95},
		Party:     conversation.PartyCaller,
	}
}

// ---------------------------------------------------------------------------
// Intent discrimination through a RUNNING service
// ---------------------------------------------------------------------------

// TestT4_RunningServiceDiscriminatesBetweenIntents drives six deterministic
// utterances through a live service and requires six distinct, exact outcomes.
//
// This is the evidence T2 and T3 could not give: a hard-coded or single-intent
// path passes both of those and fails this.
func TestT4_RunningServiceDiscriminatesBetweenIntents(t *testing.T) {
	sink, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	seen := map[conversation.IntentName]bool{}

	for i, tc := range e2eCases() {
		p := openSession(t, vi, fmt.Sprintf("t4-disc-%02d", i))

		plan, err := p.Handle(utter(tc.utterance))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		if plan.Intent != tc.wantIntent {
			t.Errorf("%s: intent = %q, want %q", tc.name, plan.Intent, tc.wantIntent)
		}
		if plan.Action != tc.wantAction {
			t.Errorf("%s: action = %v, want %v", tc.name, plan.Action, tc.wantAction)
		}
		if plan.Reason != tc.wantReason {
			t.Errorf("%s: reason = %q, want %q", tc.name, plan.Reason, tc.wantReason)
		}
		if plan.Intent == conversation.IntentFallback {
			t.Fatalf("%s: fell back; the running service is not reaching the "+
				"classifier. log:\n%s", tc.name, sink.dump())
		}
		seen[plan.Intent] = true
	}

	// Discrimination, stated as a property rather than implied by the table.
	if len(seen) != len(e2eCases()) {
		t.Errorf("%d distinct intents across %d cases; the path is not "+
			"discriminating", len(seen), len(e2eCases()))
	}
}

// TestT4_TheSameTableCollapsesWithoutTheClassifier is what makes the table
// above meaningful.
//
// The identical utterances are driven through a pre-Phase-13 engine — no
// WithClassifier — and every one must collapse to the fallback intent. If they
// did not, the table would be passing for reasons unrelated to the wiring.
func TestT4_TheSameTableCollapsesWithoutTheClassifier(t *testing.T) {
	t.Parallel()

	bare, err := conversation.NewEngine(conversation.DefaultConfig())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	for i, tc := range e2eCases() {
		conv, err := bare.Begin(conversation.ConversationID(fmt.Sprintf("t4-bare-%02d", i)), "")
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		for _, e := range []conversation.Event{
			{Kind: conversation.EventStart},
			{Kind: conversation.EventGreetingComplete},
		} {
			if _, err := conv.Handle(e); err != nil {
				t.Fatalf("%v: %v", e.Kind, err)
			}
		}
		plan, err := conv.Handle(utter(tc.utterance))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if plan.Intent != conversation.IntentFallback {
			t.Errorf("%s: an engine with NO classifier produced %q, want fallback; "+
				"the discrimination table would prove nothing", tc.name, plan.Intent)
		}
	}
}

// ---------------------------------------------------------------------------
// Frozen turn semantics, reached through the service
// ---------------------------------------------------------------------------

// TestT4_FrozenTurnSemanticsAreReached proves the service path drives the
// FROZEN turn machinery — floor ownership, expectation, FSM state, turn count —
// rather than only producing an intent.
//
// No new enum, no second state machine: every value asserted here is a frozen
// conversation type.
func TestT4_FrozenTurnSemanticsAreReached(t *testing.T) {
	t.Parallel()

	_, vi := build(t)
	id := conversation.ConversationID("t4-turnsem")
	p := openSession(t, vi, string(id))

	conv, ok := vi.Bridge().Conversation(id)
	if !ok {
		t.Fatal("conversation missing")
	}

	// MEASURED frozen handoff cycle. The greeting turn is complete and
	// recorded, and the floor is released to NOBODY -- the caller acquires it
	// by speaking, rather than being handed it in advance.
	if got := conv.Turns().Holder(); got != conversation.PartyNone {
		t.Errorf("floor holder after greeting = %v, want PartyNone", got)
	}
	if got := conv.State(); got != conversation.StateListening {
		t.Errorf("state = %v, want StateListening", got)
	}
	if got := conv.Turns().Count(); got != 1 {
		t.Errorf("turn count after greeting = %d, want 1 (the greeting turn)", got)
	}

	// A complete request: the planner answers and expects nothing back.
	plan, err := p.Handle(utter("please call me back on 9876543210"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Expectation != conversation.ExpectNothing {
		t.Errorf("expectation = %v, want ExpectNothing for a complete request",
			plan.Expectation)
	}
	if plan.NextState != conversation.StateSpeaking {
		t.Errorf("next state = %v, want StateSpeaking", plan.NextState)
	}
	if got := conv.State(); got != conversation.StateSpeaking {
		t.Errorf("state = %v, want StateSpeaking while the agent answers", got)
	}
	// The AGENT now holds the floor: it took it to deliver the answer.
	if got := conv.Turns().Holder(); got != conversation.PartyAgent {
		t.Errorf("floor holder while answering = %v, want PartyAgent", got)
	}

	// Only when the agent finishes speaking does the turn close and the floor
	// pass to the caller. Asserting the whole handoff, not a single snapshot.
	if _, err := p.Handle(conversation.Event{
		Kind: conversation.EventSpeechComplete}); err != nil {
		t.Fatalf("speech complete: %v", err)
	}
	if got := conv.Turns().Holder(); got != conversation.PartyCaller {
		t.Errorf("floor holder after the answer = %v, want PartyCaller", got)
	}
	if got := conv.Turns().Count(); got != 2 {
		t.Errorf("turn count after a full exchange = %d, want 2", got)
	}
	if got := conv.State(); got != conversation.StateListening {
		t.Errorf("state after the answer = %v, want StateListening", got)
	}

	// An incomplete request establishes a constrained expectation instead.
	p2 := openSession(t, vi, "t4-turnsem-slot")
	askPlan, err := p2.Handle(utter("i want to leave a message"))
	if err != nil {
		t.Fatal(err)
	}
	if askPlan.Action != conversation.ActionAsk {
		t.Fatalf("action = %v, want ActionAsk", askPlan.Action)
	}
	if askPlan.Expectation == conversation.ExpectNothing {
		t.Error("an unfilled required slot established no expectation; the " +
			"frozen turn machinery was not reached")
	}
	if askPlan.Clarification.Kind != conversation.ClarifyMissingSlot {
		t.Errorf("clarification kind = %v, want ClarifyMissingSlot",
			askPlan.Clarification.Kind)
	}
}

// TestT4_TurnClassificationComposesOverServiceOutputs.
//
// VERIFIED AGAINST SOURCE: intent.ClassifyTurn has no production caller. The
// conversation engine owns turn-taking itself. This test therefore demonstrates
// COMPOSITION — feeding the service's real Plan-derived values into
// ClassifyTurn — and deliberately does not claim the service invokes it.
//
// Everything asserted is a frozen conversation type; no new lifecycle enum.
func TestT4_TurnClassificationComposesOverServiceOutputs(t *testing.T) {
	t.Parallel()

	_, vi := build(t)
	id := conversation.ConversationID("t4-compose")
	p := openSession(t, vi, string(id))

	plan, err := p.Handle(utter("transfer me to rajesh"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Intent != intent.IntentRequestTransfer {
		t.Fatalf("setup: intent = %q", plan.Intent)
	}

	cfg := conversation.DefaultIntentConfig()

	// New request: the service accepted an intent with nothing already active.
	newReq := intent.ClassifyTurn(intent.TurnInput{
		Event:       conversation.EventUtterance,
		Utterance:   conversation.Utterance{Text: "transfer me to rajesh", ASRConfidence: 0.95},
		Expectation: plan.Expectation,
		Verdict:     conversation.IntentAccept,
		Resolved:    conversation.Intent{Name: plan.Intent, Confidence: plan.Confidence},
		Config:      cfg,
	})
	if newReq.Lifecycle != conversation.IntentProposed {
		t.Errorf("new request lifecycle = %v, want IntentProposed", newReq.Lifecycle)
	}

	// Cancellation of the intent the service just resolved.
	cancelSig := intent.ClassifyTurn(intent.TurnInput{
		Event:     conversation.EventUtterance,
		Utterance: conversation.Utterance{Text: "never mind", ASRConfidence: 0.95},
		Verdict:   conversation.IntentAccept,
		Resolved:  conversation.Intent{Name: plan.Intent, Confidence: plan.Confidence},
		Active:    plan.Intent,
		Lifecycle: conversation.IntentActive,
		Config:    cfg,
	})
	if cancelSig.Lifecycle != conversation.IntentAbandoned {
		t.Errorf("cancellation lifecycle = %v, want IntentAbandoned", cancelSig.Lifecycle)
	}

	// Interruption and silence, over the same live session's active intent.
	interrupt := intent.ClassifyTurn(intent.TurnInput{
		Event: conversation.EventInterrupt, Lifecycle: conversation.IntentActive,
		Active: plan.Intent, Config: cfg,
	})
	if interrupt.Interruption != conversation.InterruptionUser {
		t.Errorf("interruption = %v, want InterruptionUser", interrupt.Interruption)
	}
	silence := intent.ClassifyTurn(intent.TurnInput{
		Event: conversation.EventSilence, Lifecycle: conversation.IntentActive,
		Active: plan.Intent, Config: cfg,
	})
	if silence.Lifecycle != conversation.IntentActive {
		t.Errorf("silence lifecycle = %v, want IntentActive unchanged", silence.Lifecycle)
	}
	if silence.Event != conversation.EventSilence {
		t.Errorf("silence event = %v", silence.Event)
	}

	// Acknowledgement is decided by the FROZEN floor decision, not by wording.
	ack := intent.ClassifyTurn(intent.TurnInput{
		Event: conversation.EventOverlap, Floor: conversation.FloorBackchannel,
		Lifecycle: conversation.IntentActive, Active: plan.Intent, Config: cfg,
	})
	if ack.Floor != conversation.FloorBackchannel {
		t.Errorf("acknowledgement floor = %v, want FloorBackchannel", ack.Floor)
	}
	if ack.Interruption != conversation.InterruptionNone {
		t.Errorf("a backchannel moved the floor: %v", ack.Interruption)
	}
}

// ---------------------------------------------------------------------------
// Multi-turn
// ---------------------------------------------------------------------------

// TestT4_MultiTurnThroughTheServiceUsesSessionContext runs a deterministic
// three-turn dialogue on one service session and verifies the FROZEN context
// engine carries state across turns.
//
// No new context system, no persistence, no session registry: the assertions
// read Conversation.Context(), the sanctioned API.
func TestT4_MultiTurnThroughTheServiceUsesSessionContext(t *testing.T) {
	t.Parallel()

	_, vi := build(t)
	id := conversation.ConversationID("t4-multiturn")
	p := openSession(t, vi, string(id))
	conv, _ := vi.Bridge().Conversation(id)

	// A caller-supplied fact stored on turn 1 must survive every later turn.
	if err := conv.Context().Set(conversation.Entry{
		Key: "appointment_number", Value: "A-7419",
		Scope: conversation.ScopeConversation, Source: "t4",
	}); err != nil {
		t.Fatal(err)
	}

	turns := []struct {
		utterance string
		want      conversation.IntentName
	}{
		{"please call me back on 9876543210", intent.IntentRequestCallback},
		{"transfer me to rajesh", intent.IntentRequestTransfer},
		{"say that again", intent.IntentRepeat},
	}

	for i, turn := range turns {
		plan, err := p.Handle(utter(turn.utterance))
		if err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
		if plan.Intent != turn.want {
			t.Errorf("turn %d: intent = %q, want %q", i, plan.Intent, turn.want)
		}
		if _, err := p.Handle(conversation.Event{
			Kind: conversation.EventSpeechComplete}); err != nil {
			t.Fatalf("turn %d speech complete: %v", i, err)
		}

		// Context set on turn 1 is still present.
		e, ok := conv.Context().Get(conversation.ScopeConversation, "appointment_number")
		if !ok || e.Value != "A-7419" {
			t.Fatalf("turn %d: session context lost (%v, ok=%v)", i, e.Value, ok)
		}
		// The engine records the intent it just pursued — evidence the turn
		// actually reached the intent stage.
		li, ok := conv.Context().Get(conversation.ScopeConversation, "last_intent")
		if !ok || li.Value != string(turn.want) {
			t.Errorf("turn %d: last_intent = %v (ok=%v), want %q",
				i, li.Value, ok, turn.want)
		}
	}

	if conv.State().IsTerminal() {
		t.Error("conversation terminal after three turns")
	}
}

// ---------------------------------------------------------------------------
// Session isolation across the service
// ---------------------------------------------------------------------------

// TestT4_TwoServiceSessionsProduceIndependentPlans exercises Step 7 through the
// service: one shared classifier and config, two sessions, different requests.
func TestT4_TwoServiceSessionsProduceIndependentPlans(t *testing.T) {
	t.Parallel()

	_, vi := build(t)

	pa := openSession(t, vi, "t4-iso-a")
	pb := openSession(t, vi, "t4-iso-b")

	convA, _ := vi.Bridge().Conversation("t4-iso-a")
	convB, _ := vi.Bridge().Conversation("t4-iso-b")

	if err := convA.Context().Set(conversation.Entry{
		Key: "marker", Value: "only-A",
		Scope: conversation.ScopeConversation, Source: "t4",
	}); err != nil {
		t.Fatal(err)
	}

	planA, err := pa.Handle(utter("please call me back on 9876543210"))
	if err != nil {
		t.Fatal(err)
	}
	planB, err := pb.Handle(utter("transfer me to rajesh"))
	if err != nil {
		t.Fatal(err)
	}

	if planA.Intent != intent.IntentRequestCallback {
		t.Errorf("A intent = %q", planA.Intent)
	}
	if planB.Intent != intent.IntentRequestTransfer {
		t.Errorf("B intent = %q", planB.Intent)
	}
	if planA.Intent == planB.Intent {
		t.Error("both sessions produced the same intent")
	}
	if _, seen := convB.Context().Get(conversation.ScopeConversation, "marker"); seen {
		t.Error("session B observed session A's context through the service")
	}
	if convA.Context() == convB.Context() {
		t.Error("both sessions share one context engine")
	}
}

// ---------------------------------------------------------------------------
// Unknown / low confidence / ambiguity, through the service
// ---------------------------------------------------------------------------

// TestT4_UnknownLowConfidenceAndAmbiguityStayDistinct proves the service path
// preserves the frozen distinctions rather than flattening everything it does
// not confidently understand into one answer.
//
// No threshold is changed and no fallback policy is invented; each expectation
// was measured against this wiring.
func TestT4_UnknownLowConfidenceAndAmbiguityStayDistinct(t *testing.T) {
	t.Parallel()

	_, vi := build(t)

	cases := []struct {
		name       string
		utterance  string
		wantIntent conversation.IntentName
		wantAction conversation.Action
		wantReason string
		wantClar   conversation.ClarificationKind
	}{
		{
			name: "unknown", utterance: "zzzz qqqq wubble frotz",
			wantIntent: conversation.IntentFallback,
			wantAction: conversation.ActionRespond, wantReason: "fallback",
			wantClar: conversation.ClarifyNone,
		},
		{
			name: "low_confidence", utterance: "repeat pardon",
			wantIntent: intent.IntentRepeat,
			wantAction: conversation.ActionConfirm, wantReason: "clarify_low_confidence",
			wantClar: conversation.ClarifyLowConfidence,
		},
		{
			name: "ambiguous", utterance: "hold on call back",
			wantIntent: intent.IntentHold,
			wantAction: conversation.ActionClarify, wantReason: "clarify_ambiguous",
			wantClar: conversation.ClarifyAmbiguous,
		},
	}

	signatures := map[string]string{}
	for i, tc := range cases {
		p := openSession(t, vi, fmt.Sprintf("t4-neg-%d", i))
		plan, err := p.Handle(utter(tc.utterance))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if plan.Intent != tc.wantIntent {
			t.Errorf("%s: intent = %q, want %q", tc.name, plan.Intent, tc.wantIntent)
		}
		if plan.Action != tc.wantAction {
			t.Errorf("%s: action = %v, want %v", tc.name, plan.Action, tc.wantAction)
		}
		if plan.Reason != tc.wantReason {
			t.Errorf("%s: reason = %q, want %q", tc.name, plan.Reason, tc.wantReason)
		}
		if plan.Clarification.Kind != tc.wantClar {
			t.Errorf("%s: clarification = %v, want %v",
				tc.name, plan.Clarification.Kind, tc.wantClar)
		}
		sig := fmt.Sprintf("%v|%s|%v", plan.Action, plan.Reason, plan.Clarification.Kind)
		if prev, dup := signatures[sig]; dup {
			t.Errorf("%s and %s produced the identical outcome %s; two distinct "+
				"cases collapsed", prev, tc.name, sig)
		}
		signatures[sig] = tc.name
	}
}

// ---------------------------------------------------------------------------
// Failure behaviour
// ---------------------------------------------------------------------------

// TestT4_IntelligenceFailureStaysSessionScoped drives a session to a terminal
// escalation and verifies the blast radius is one session.
//
// "callback transfer" scores below the frozen reject threshold, which the
// planner escalates — a real, typed, terminal outcome rather than a contrived
// error.
func TestT4_IntelligenceFailureStaysSessionScoped(t *testing.T) {
	sink, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	// A healthy neighbour, established first.
	healthy := openSession(t, vi, "t4-fail-healthy")
	before, err := healthy.Handle(utter("transfer me to rajesh"))
	if err != nil {
		t.Fatal(err)
	}
	if before.Intent != intent.IntentRequestTransfer {
		t.Fatalf("neighbour setup: intent = %q", before.Intent)
	}
	if _, err := healthy.Handle(conversation.Event{
		Kind: conversation.EventSpeechComplete}); err != nil {
		t.Fatal(err)
	}

	// The failing session.
	bad := openSession(t, vi, "t4-fail-bad")
	badPlan, err := bad.Handle(utter("callback transfer"))
	if err != nil {
		t.Fatalf("below-reject utterance returned an error rather than a plan: %v", err)
	}
	if badPlan.Action != conversation.ActionEscalate {
		t.Errorf("action = %v, want ActionEscalate", badPlan.Action)
	}
	if badPlan.Reason != "intent_rejected" {
		t.Errorf("reason = %q, want intent_rejected", badPlan.Reason)
	}

	// Typed and bounded: further work on that session is refused, not panicked.
	if _, err := bad.Handle(utter("are you there")); err == nil {
		t.Error("a terminal session accepted further work")
	}

	// The neighbour is untouched.
	after, err := healthy.Handle(utter("please call me back on 9876543210"))
	if err != nil {
		t.Fatalf("neighbour broke after a sibling escalated: %v", err)
	}
	if after.Intent != intent.IntentRequestCallback {
		t.Errorf("neighbour intent = %q after sibling failure", after.Intent)
	}

	// And a brand-new session still works.
	fresh := openSession(t, vi, "t4-fail-fresh")
	freshPlan, err := fresh.Handle(utter("say that again"))
	if err != nil {
		t.Fatalf("new session after failure: %v", err)
	}
	if freshPlan.Intent != intent.IntentRepeat {
		t.Errorf("new session intent = %q", freshPlan.Intent)
	}

	// The service itself never failed.
	select {
	case err := <-done:
		t.Fatalf("the service exited because one session escalated (err=%v); "+
			"log:\n%s", err, sink.dump())
	default:
	}
	if sink.has("runner failed, shutting down service") {
		t.Errorf("a session-scoped failure became a service failure; log:\n%s",
			sink.dump())
	}
}

// ---------------------------------------------------------------------------
// Determinism
// ---------------------------------------------------------------------------

// TestT4_SemanticSignatureIsStable replays every case on a fresh service each
// time and requires an identical semantic signature.
//
// The signature carries intent, confidence class, plan action, reason,
// expectation and clarification. It deliberately excludes timestamps,
// Plan.Deadline, scheduler ordering, log ordering and session ids.
func TestT4_SemanticSignatureIsStable(t *testing.T) {
	t.Parallel()

	confClass := func(c float64) string {
		cfg := conversation.DefaultIntentConfig()
		switch {
		case c <= 0:
			return "zero"
		case c < cfg.RejectThreshold:
			return "below_reject"
		case c < cfg.AcceptThreshold:
			return "clarify_band"
		case c < 1:
			return "accept"
		default:
			return "certain"
		}
	}

	run := func(pass int) string {
		_, vi := build(t)
		var b strings.Builder
		for i, tc := range e2eCases() {
			p := openSession(t, vi, fmt.Sprintf("t4-det-%d-%02d", pass, i))
			plan, err := p.Handle(utter(tc.utterance))
			if err != nil {
				fmt.Fprintf(&b, "%s=err:%v\n", tc.name, err)
				continue
			}
			fmt.Fprintf(&b, "%s=%s|%s|%v|%s|%v|%v\n",
				tc.name, plan.Intent, confClass(plan.Confidence), plan.Action,
				plan.Reason, plan.Expectation, plan.Clarification.Kind)
		}
		return b.String()
	}

	want := run(0)
	if want == "" {
		t.Fatal("empty signature")
	}
	for i := 1; i <= 20; i++ {
		if got := run(i); got != want {
			t.Fatalf("pass %d drifted\n got %s\nwant %s", i, got, want)
		}
	}
}
