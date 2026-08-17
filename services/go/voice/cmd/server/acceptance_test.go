package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/intent"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/callscreen/callscreen-platform/packages/go/voiceintel"
)

// Phase 14 T11 — ACCEPTANCE / EVALUATION FIXTURES.
//
// THIS IS CONTRACT EVALUATION, NOT ACCURACY EVALUATION. No accuracy, precision,
// recall, F1, model-quality or speech-recognition figure is produced or implied.
// There is no model and no provider in this path. Every expectation below is a
// clause of the frozen behavioural contract, checked against a running service.
//
// WHAT MAKES T11 DIFFERENT FROM T9, and it is the whole point:
//
//	T9 compared the service against ITSELF (equality across runs) and against a
//	golden file GENERATED FROM A RUN. Both are captures. If the implementation
//	were uniformly wrong, T9's golden would have recorded the wrong values as
//	correct — and T9 proved that hazard is real: two mutations passed its entire
//	equality suite before the golden existed.
//
//	Every expectation in this file is DECLARED BY HAND from the frozen contract
//	and from behaviour measured directly, never captured from a run. A uniformly
//	wrong implementation cannot make this suite agree with it, which is what
//	TestT11_SuiteIsNonVacuous demonstrates by construction.
//
// TWO BOUNDARIES, reported rather than engineered around (see the matrix in the
// T11 report):
//
//  1. INTENT-LEVEL CANCELLATION IS NOT REACHABLE through the service seam.
//     intent.ClassifyTurn owns the cancellation vocabulary ("never mind",
//     "forget it", "cancel that") and the Bridge never calls it — the Phase 13
//     T9 finding, still true. MEASURED: all three cues resolve to
//     IntentFallback through the service. Service-level cancellation (context
//     cancellation at a turn boundary) IS reachable and is the fixture used.
//  2. THERE IS NO "LOW CONFIDENCE" CONFIDENCE BAND ON A PLAN. Plan.Confidence
//     is the planner's confidence in its DECISION, explicitly distinct from
//     intent confidence (planner.go:33-36) — measured at 0.750 for the
//     low-confidence fixture, which is the accept band. The low-confidence
//     signal the contract actually carries is ClarificationKind ==
//     ClarifyLowConfidence with reason "clarify_low_confidence" and
//     ActionConfirm. Those are asserted; no band is invented.

// ---------------------------------------------------------------------------
// Step 15 — fixture schema
// ---------------------------------------------------------------------------

// step is one event delivered to a session.
type step struct {
	op   t9Op // reuses the T9 operation vocabulary
	text string
}

// acceptance is one fixture. Expectations describe the plan produced by the
// FINAL step plus the conversation state it leaves behind.
//
// Optional fields are absent rather than invented: wantCands nil, wantClarSlot
// "" and wantCtx nil each mean "the contract says nothing here, so nothing is
// asserted". Fields that are always observable (intent, action, reason,
// expectation, clarification kind, next state, terminal, floor holder) are
// declared on every fixture.
//
// Deliberately absent from the schema: timestamps, session IDs, log text,
// goroutine identity, durations, machine-specific values.
type acceptance struct {
	name     string
	category string
	steps    []step

	wantIntent    conversation.IntentName
	wantAction    conversation.Action
	wantReason    string
	wantExpect    conversation.Expectation
	wantClar      conversation.ClarificationKind
	wantClarSlot  string
	wantCands     []conversation.IntentName
	wantNext      conversation.State
	wantTerminal  bool
	wantHolder    conversation.Party
	wantErr       error
	wantIntrCount int
}

// acceptanceFixtures declares the required categories. Every expected value was
// either read from the frozen contract or measured directly against the service
// before being written down here.
func acceptanceFixtures() []acceptance {
	return []acceptance{
		{
			name: "normal_transfer", category: "NORMAL",
			steps:      []step{{opUtter, "transfer me to rajesh"}},
			wantIntent: intent.IntentRequestTransfer,
			wantAction: conversation.ActionRespond, wantReason: "intent_accepted",
			wantExpect: conversation.ExpectNothing, wantClar: conversation.ClarifyNone,
			wantNext: conversation.StateSpeaking, wantHolder: conversation.PartyAgent,
		},
		{
			name: "normal_callback", category: "NORMAL",
			steps:      []step{{opUtter, "please call me back on 9876543210"}},
			wantIntent: intent.IntentRequestCallback,
			wantAction: conversation.ActionRespond, wantReason: "intent_accepted",
			wantExpect: conversation.ExpectNothing, wantClar: conversation.ClarifyNone,
			wantNext: conversation.StateSpeaking, wantHolder: conversation.PartyAgent,
		},
		{
			name: "ambiguous_hold_or_callback", category: "AMBIGUOUS",
			steps:      []step{{opUtter, "hold on call back"}},
			wantIntent: intent.IntentHold,
			wantAction: conversation.ActionClarify, wantReason: "clarify_ambiguous",
			wantExpect: conversation.ExpectDisambiguation,
			wantClar:   conversation.ClarifyAmbiguous,
			// Candidate SET is asserted; see assertCandidates for why order is
			// not, and why asserting it here would be asserting the frozen
			// engine's re-sort rather than the classifier's behaviour.
			wantCands: []conversation.IntentName{
				intent.IntentHold, intent.IntentRequestCallback,
			},
			wantNext: conversation.StateSpeaking, wantHolder: conversation.PartyAgent,
		},
		{
			name: "clarification_missing_slot", category: "CLARIFICATION",
			steps:      []step{{opUtter, "i want to leave a message"}},
			wantIntent: intent.IntentLeaveMessage,
			wantAction: conversation.ActionAsk, wantReason: "clarify_missing_slot",
			wantExpect: conversation.ExpectSlotValue,
			wantClar:   conversation.ClarifyMissingSlot, wantClarSlot: "message_body",
			wantNext: conversation.StateSpeaking, wantHolder: conversation.PartyAgent,
		},
		{
			name: "interruption_barge_in", category: "INTERRUPTION",
			steps: []step{
				{opUtter, "transfer me to rajesh"},
				{opInterrupt, ""},
			},
			wantIntent: "",
			wantAction: conversation.ActionIgnore, wantReason: "interrupted_user",
			wantExpect: conversation.ExpectNothing, wantClar: conversation.ClarifyNone,
			wantNext: conversation.StateListening,
			// MEASURED: the frozen floor is force-yielded to the caller.
			wantHolder: conversation.PartyCaller, wantIntrCount: 1,
		},
		{
			name: "acknowledgement_backchannel", category: "ACKNOWLEDGEMENT",
			steps: []step{
				{opUtter, "transfer me to rajesh"},
				{opOverlap, ""},
			},
			wantIntent: "",
			wantAction: conversation.ActionIgnore, wantReason: "backchannel",
			wantExpect: conversation.ExpectNothing, wantClar: conversation.ClarifyNone,
			// The agent KEEPS the floor: that is what distinguishes an
			// acknowledgement from the barge-in fixture above.
			wantNext: conversation.StateSpeaking, wantHolder: conversation.PartyAgent,
		},
		{
			name: "acknowledgement_affirm_in_confirmation", category: "ACKNOWLEDGEMENT",
			steps: []step{
				{opUtter, "repeat pardon"}, // establishes ExpectYesNo
				{opComplete, ""},
				{opUtter, "yes"},
			},
			wantIntent: conversation.IntentAffirm,
			wantAction: conversation.ActionRespond, wantReason: "intent_accepted",
			wantExpect: conversation.ExpectNothing, wantClar: conversation.ClarifyNone,
			wantNext: conversation.StateSpeaking, wantHolder: conversation.PartyAgent,
		},
		{
			name: "silence_from_listening", category: "SILENCE",
			steps:      []step{{opSilence, ""}},
			wantIntent: "",
			wantAction: conversation.ActionWait, wantReason: "silence",
			wantExpect: conversation.ExpectNothing, wantClar: conversation.ClarifyNone,
			wantNext: conversation.StateWaiting, wantHolder: conversation.PartyNone,
		},
		{
			name: "malformed_control_bytes", category: "MALFORMED",
			steps:      []step{{opUtter, "\x00\x01\x02"}},
			wantIntent: conversation.IntentFallback,
			wantAction: conversation.ActionRespond, wantReason: "fallback",
			wantExpect: conversation.ExpectNothing, wantClar: conversation.ClarifyNone,
			wantNext: conversation.StateSpeaking, wantHolder: conversation.PartyAgent,
		},
		{
			name: "malformed_whitespace_only", category: "MALFORMED",
			steps:      []step{{opUtter, "   \t\n  "}},
			wantIntent: conversation.IntentFallback,
			wantAction: conversation.ActionRespond, wantReason: "fallback",
			wantExpect: conversation.ExpectNothing, wantClar: conversation.ClarifyNone,
			wantNext: conversation.StateSpeaking, wantHolder: conversation.PartyAgent,
		},
		{
			name: "malformed_punctuation_only", category: "MALFORMED",
			steps:      []step{{opUtter, "!!!@@@###"}},
			wantIntent: conversation.IntentFallback,
			wantAction: conversation.ActionRespond, wantReason: "fallback",
			wantExpect: conversation.ExpectNothing, wantClar: conversation.ClarifyNone,
			wantNext: conversation.StateSpeaking, wantHolder: conversation.PartyAgent,
		},
		{
			name: "low_confidence_confirm", category: "LOW_CONFIDENCE",
			steps:      []step{{opUtter, "repeat pardon"}},
			wantIntent: intent.IntentRepeat,
			wantAction: conversation.ActionConfirm, wantReason: "clarify_low_confidence",
			wantExpect: conversation.ExpectYesNo,
			wantClar:   conversation.ClarifyLowConfidence,
			wantNext:   conversation.StateSpeaking, wantHolder: conversation.PartyAgent,
		},
		{
			name: "terminal_session_refuses_work", category: "MALFORMED",
			steps: []step{
				{opUtter, "transfer me to rajesh"},
				{opEnd, ""},
				{opUtter, "say that again"},
			},
			wantIntent: "",
			wantAction: conversation.ActionRespond, // zero Plan on a refusal
			wantReason: "",
			wantExpect: conversation.ExpectNothing, wantClar: conversation.ClarifyNone,
			wantNext: conversation.StateIdle, wantTerminal: true,
			wantHolder: conversation.PartyAgent,
			wantErr:    conversation.ErrTerminal,
		},
		{
			name: "multi_turn_callback_transfer_repeat", category: "MULTI_TURN",
			steps: []step{
				{opUtter, "please call me back on 9876543210"},
				{opComplete, ""},
				{opUtter, "transfer me to rajesh"},
				{opComplete, ""},
				{opUtter, "say that again"},
			},
			wantIntent: intent.IntentRepeat,
			wantAction: conversation.ActionRespond, wantReason: "intent_accepted",
			wantExpect: conversation.ExpectNothing, wantClar: conversation.ClarifyNone,
			wantNext: conversation.StateSpeaking, wantHolder: conversation.PartyAgent,
		},
	}
}

// opOverlap extends the T9 operation vocabulary with the frozen EventOverlap,
// which T9 had no fixture for.
const opOverlap t9Op = 99

// runAcceptance executes a fixture and returns the final plan, error and
// conversation.
func runAcceptance(vi *voiceIntelligence, id conversation.ConversationID, f acceptance) (
	conversation.Plan, error, *conversation.Conversation, error,
) {
	p, conv, err := openPlanner(vi, id)
	if err != nil {
		return conversation.Plan{}, nil, nil, err
	}
	var (
		plan conversation.Plan
		hErr error
	)
	for _, s := range f.steps {
		switch s.op {
		case opUtter:
			plan, hErr = p.Handle(utter(s.text))
		case opComplete:
			plan, hErr = p.Handle(conversation.Event{Kind: conversation.EventSpeechComplete})
		case opInterrupt:
			plan, hErr = p.Handle(conversation.Event{
				Kind:         conversation.EventInterrupt,
				Interruption: conversation.InterruptionUser,
				Party:        conversation.PartyCaller,
				Reason:       "barge_in",
			})
		case opOverlap:
			plan, hErr = p.Handle(conversation.Event{
				Kind: conversation.EventOverlap, Party: conversation.PartyCaller})
		case opSilence:
			plan, hErr = p.Handle(conversation.Event{Kind: conversation.EventSilence})
		case opEnd:
			plan, hErr = conversation.Plan{}, conv.End("t11_terminated")
		}
	}
	return plan, hErr, conv, nil
}

// checkAcceptance returns every clause of the contract the result violates.
//
// Returning the full list rather than failing on the first mismatch is what
// makes the non-vacuity test able to count violations.
func checkAcceptance(f acceptance, plan conversation.Plan, err error,
	conv *conversation.Conversation,
) []string {
	var bad []string
	add := func(format string, a ...any) { bad = append(bad, fmt.Sprintf(format, a...)) }

	if f.wantErr != nil {
		if !errors.Is(err, f.wantErr) {
			add("error = %v, want %v", err, f.wantErr)
		}
	} else if err != nil {
		add("unexpected error %v", err)
	}
	if plan.Intent != f.wantIntent {
		add("intent = %q, want %q", plan.Intent, f.wantIntent)
	}
	if plan.Action != f.wantAction {
		add("action = %v, want %v", plan.Action, f.wantAction)
	}
	if plan.Reason != f.wantReason {
		add("reason = %q, want %q", plan.Reason, f.wantReason)
	}
	if plan.Expectation != f.wantExpect {
		add("expectation = %v, want %v", plan.Expectation, f.wantExpect)
	}
	if plan.Clarification.Kind != f.wantClar {
		add("clarification = %v, want %v", plan.Clarification.Kind, f.wantClar)
	}
	if f.wantClarSlot != "" && plan.Clarification.Slot != f.wantClarSlot {
		add("clarification slot = %q, want %q", plan.Clarification.Slot, f.wantClarSlot)
	}
	if plan.NextState != f.wantNext {
		add("next state = %v, want %v", plan.NextState, f.wantNext)
	}
	if conv != nil {
		if got := conv.State().IsTerminal(); got != f.wantTerminal {
			add("terminal = %v, want %v", got, f.wantTerminal)
		}
		if got := conv.Turns().Holder(); got != f.wantHolder {
			add("floor holder = %v, want %v", got, f.wantHolder)
		}
		if got := len(conv.Interruptions().History()); got != f.wantIntrCount {
			add("interruptions recorded = %d, want %d", got, f.wantIntrCount)
		}
	}
	if f.wantCands != nil {
		bad = append(bad, checkCandidates(f.wantCands, plan.Clarification.Candidates)...)
	}
	return bad
}

// checkCandidates compares the candidate SET, not its order.
//
// Order is deliberately not asserted. T9 established that the frozen
// IntentEngine.Resolve re-sorts every candidate slice it receives
// (conversation/intent.go:347) by (confidence desc, name asc), so an ordering
// assertion here would be testing the frozen engine's normalisation rather than
// any behaviour of the path under evaluation. Membership IS the contract:
// ambiguity is detectable only because both candidates survive.
func checkCandidates(want, got []conversation.IntentName) []string {
	seen := make(map[conversation.IntentName]bool, len(got))
	for _, g := range got {
		seen[g] = true
	}
	var bad []string
	for _, w := range want {
		if !seen[w] {
			bad = append(bad, fmt.Sprintf("candidate %q missing from %v", w, got))
		}
	}
	if len(got) != len(want) {
		bad = append(bad, fmt.Sprintf("candidate count = %d %v, want %d %v",
			len(got), got, len(want), want))
	}
	return bad
}

// ---------------------------------------------------------------------------
// The acceptance matrix
// ---------------------------------------------------------------------------

// TestT11_AcceptanceMatrix runs every fixture against a RUNNING service and
// checks it against hand-declared contract expectations.
func TestT11_AcceptanceMatrix(t *testing.T) {
	_, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	fixtures := acceptanceFixtures()
	for i, f := range fixtures {
		t.Run(f.category+"/"+f.name, func(t *testing.T) {
			var (
				plan conversation.Plan
				hErr error
				conv *conversation.Conversation
			)
			mustFinish(t, 30*time.Second, f.name, func() error {
				var setupErr error
				plan, hErr, conv, setupErr = runAcceptance(vi,
					conversation.ConversationID(fmt.Sprintf("t11-%02d", i)), f)
				return setupErr
			})
			for _, bad := range checkAcceptance(f, plan, hErr, conv) {
				t.Errorf("%s: %s", f.name, bad)
			}
		})
	}
}

// TestT11_EveryRequiredCategoryIsCovered fails if a required category silently
// disappears from the table.
//
// CANCELLATION, EVICTION and CONCURRENT are covered by dedicated tests below
// rather than by the single-plan schema, because their contract is about state
// across sessions and cannot be expressed as one plan.
func TestT11_EveryRequiredCategoryIsCovered(t *testing.T) {
	t.Parallel()
	present := map[string]int{}
	for _, f := range acceptanceFixtures() {
		present[f.category]++
	}
	for _, c := range []string{
		"NORMAL", "AMBIGUOUS", "CLARIFICATION", "INTERRUPTION", "ACKNOWLEDGEMENT",
		"SILENCE", "MULTI_TURN", "MALFORMED", "LOW_CONFIDENCE",
	} {
		if present[c] == 0 {
			t.Errorf("required category %s has no fixture", c)
		}
	}
	for _, c := range []struct{ name, test string }{
		{"CANCELLATION", "TestT11_CancellationAtSupportedBoundary"},
		{"EVICTION", "TestT11_EvictionUnderControlledClock"},
		{"CONCURRENT", "TestT11_ConcurrentSessionsStayIndependent"},
	} {
		t.Logf("category %s covered by %s", c.name, c.test)
	}
}

// ---------------------------------------------------------------------------
// Step 17 — non-vacuity
// ---------------------------------------------------------------------------

// constantClassifier returns the same candidate for every utterance. It is a
// uniformly wrong but perfectly DETERMINISTIC implementation.
type constantClassifier struct{}

func (constantClassifier) Classify(conversation.Utterance, conversation.Expectation) (
	[]conversation.Candidate, []conversation.Slot, error,
) {
	return []conversation.Candidate{{Name: intent.IntentHold, Confidence: 1.0}}, nil, nil
}

// TestT11_SuiteIsNonVacuous is the guard that makes the rest of this file
// meaningful.
//
// It swaps in a classifier that is deterministic and always wrong, via the
// EXISTING voiceintel.WithClassifier seam — the seam whose documented purpose is
// exactly this — and requires the acceptance suite to REJECT it.
//
// T9 discovered that self-versus-self determinism tests stay green against a
// consistently wrong implementation. A suite of hand-declared expectations must
// not have that weakness, and this proves it does not: a uniformly wrong service
// is caught on the large majority of fixtures, and no fixture that depends on
// classification can pass.
func TestT11_SuiteIsNonVacuous(t *testing.T) {
	bridge, err := voiceintel.New(voiceintel.WithClassifier(constantClassifier{}))
	if err != nil {
		t.Fatalf("bridge: %v", err)
	}
	vi := &voiceIntelligence{bridge: bridge, log: discardLogger()}

	fixtures := acceptanceFixtures()
	var rejected, accepted []string
	for i, f := range fixtures {
		plan, hErr, conv, setupErr := runAcceptance(vi,
			conversation.ConversationID(fmt.Sprintf("t11-nv-%02d", i)), f)
		if setupErr != nil {
			t.Fatalf("%s: setup: %v", f.name, setupErr)
		}
		if len(checkAcceptance(f, plan, hErr, conv)) > 0 {
			rejected = append(rejected, f.name)
		} else {
			accepted = append(accepted, f.name)
		}
	}

	if len(rejected) == 0 {
		t.Fatal("a uniformly wrong classifier satisfied EVERY fixture — this " +
			"suite proves nothing")
	}
	// Every fixture whose outcome depends on classification must be rejected.
	// The ones that legitimately survive are those the classifier cannot
	// influence: a barge-in, a silence event and a terminal refusal are decided
	// by the frozen FSM before classification is ever consulted.
	classifierIndependent := map[string]bool{
		"interruption_barge_in":         true,
		"acknowledgement_backchannel":   true,
		"silence_from_listening":        true,
		"terminal_session_refuses_work": true,
	}
	for _, name := range accepted {
		if !classifierIndependent[name] {
			t.Errorf("fixture %q passed against a uniformly wrong classifier — it "+
				"is not actually asserting classification behaviour", name)
		}
	}
	t.Logf("uniformly wrong classifier rejected by %d/%d fixtures; %d survived and "+
		"all are classifier-independent by construction: %v",
		len(rejected), len(fixtures), len(accepted), accepted)
}

// TestT11_NoCallerContentLeaksIntoMachineFields checks the security clause of
// the frozen contract against the fixture inputs.
//
// The fixtures deliberately carry realistic personal data — a phone number and a
// person's name — because a fixture suite made only of harmless tokens could not
// detect a leak. Plan.Reason is documented as "Never caller content — it appears
// in logs and metric labels" (planner.go:16-18), and conversation.Slot carries no
// value at all. This asserts both hold for every fixture: no caller-supplied word
// reaches Reason, Escalation, the clarification slot NAME, or any context entry
// the ENGINE wrote.
//
// Context entries written by the test itself are excluded — they are the test's
// own data, not a leak.
func TestT11_NoCallerContentLeaksIntoMachineFields(t *testing.T) {
	_, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	// Distinctive caller-supplied tokens that must never appear in machine fields.
	sensitive := []string{"9876543210", "rajesh", "sharma"}

	for i, f := range acceptanceFixtures() {
		plan, _, conv, setupErr := runAcceptance(vi,
			conversation.ConversationID(fmt.Sprintf("t11-leak-%02d", i)), f)
		if setupErr != nil {
			t.Fatalf("%s: %v", f.name, setupErr)
		}
		machine := []struct{ field, value string }{
			{"Reason", plan.Reason},
			{"Escalation", plan.Escalation},
			{"Clarification.Slot", plan.Clarification.Slot},
		}
		for _, e := range conv.Context().Export(conversation.SensitiveValue) {
			machine = append(machine,
				struct{ field, value string }{"context[" + e.Key + "]",
					fmt.Sprintf("%v", e.Value)})
		}
		for _, m := range machine {
			for _, s := range sensitive {
				if strings.Contains(strings.ToLower(m.value), s) {
					t.Errorf("%s: caller content %q leaked into %s = %q — this field "+
						"reaches logs and metric labels", f.name, s, m.field, m.value)
				}
			}
		}
		// The frozen Slot type carries no value; confirm the shape has not
		// grown one that a fixture could smuggle content through.
		if plan.Clarification.Slot != "" && plan.Clarification.Slot != "message_body" {
			t.Errorf("%s: unexpected clarification slot %q", f.name, plan.Clarification.Slot)
		}
	}
}

// ---------------------------------------------------------------------------
// CANCELLATION
// ---------------------------------------------------------------------------

// TestT11_CancellationAtSupportedBoundary covers the CANCELLATION category.
//
// SCOPE, stated precisely: this is SERVICE-level cancellation observed at a turn
// boundary. Handle is synchronous and takes no context, so no mid-turn
// cancellation is claimed. Intent-level cancellation ("never mind") is NOT
// reachable through this seam and is asserted as such below, rather than being
// quietly omitted.
func TestT11_CancellationAtSupportedBoundary(t *testing.T) {
	_, vi, cancel, done := runningService(t)

	id := conversation.ConversationID("t11-cancel")
	p, conv, err := openPlanner(vi, id)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	plan, err := p.Handle(utter("please call me back on 9876543210"))
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if plan.Intent != intent.IntentRequestCallback {
		t.Fatalf("intent = %q, want %q", plan.Intent, intent.IntentRequestCallback)
	}
	before := conv.State()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("service did not shut down")
	}

	// No panic, no lost state, and the session is still exactly where it was.
	if got := conv.State(); got != before {
		t.Errorf("state = %v after cancellation, want %v — cancellation rewrote "+
			"semantic state", got, before)
	}
	if _, ok := vi.Bridge().Conversation(id); !ok {
		t.Error("session vanished on cancellation")
	}

	// The boundary itself, asserted rather than described: the cancellation
	// vocabulary is not reachable through the service classifier.
	for _, cue := range []string{"never mind", "forget it", "cancel that"} {
		p2, _, err := openPlanner(vi, conversation.ConversationID("t11-cue-"+cue))
		if err != nil {
			t.Fatalf("open %q: %v", cue, err)
		}
		got, err := p2.Handle(utter(cue))
		if err != nil {
			t.Fatalf("%q: %v", cue, err)
		}
		if got.Intent != conversation.IntentFallback {
			t.Errorf("cue %q resolved to %q — intent-level cancellation appears to "+
				"have become reachable through the service seam. That is a contract "+
				"change: re-evaluate the T11 boundary rather than editing this test.",
				cue, got.Intent)
		}
	}
}

// ---------------------------------------------------------------------------
// EVICTION
// ---------------------------------------------------------------------------

// TestT11_EvictionUnderControlledClock covers the EVICTION category.
//
// The tied-timestamp distinction established by T7/T9 is honoured exactly:
// with the clock ADVANCED between writes the victim is deterministic and is
// asserted; with timestamps TIED only the guaranteed invariants are asserted
// and victim identity is not.
func TestT11_EvictionUnderControlledClock(t *testing.T) {
	bound := maxCtxEntries()

	// --- distinct timestamps: victim identity IS guaranteed ---
	clock := rt.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	bridge, err := voiceintel.New(voiceintel.WithClock(clock))
	if err != nil {
		t.Fatalf("bridge: %v", err)
	}
	conv, err := bridge.Engine().Begin("t11-evict", "")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	ctx := conv.Context()

	for i := 0; i < bound; i++ {
		clock.Advance(time.Millisecond)
		if err := ctx.Set(conversation.Entry{
			Key: fmt.Sprintf("k%05d", i), Value: i,
			Scope: conversation.ScopeConversation, Source: "t11",
		}); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	if got := ctx.Size(conversation.ScopeConversation); got != bound {
		t.Fatalf("size %d after filling to the bound, want %d", got, bound)
	}
	// One more write with a strictly newer timestamp: the OLDEST entry, k00000,
	// is the determined victim.
	clock.Advance(time.Millisecond)
	if err := ctx.Set(conversation.Entry{
		Key: "overflow", Value: "new",
		Scope: conversation.ScopeConversation, Source: "t11",
	}); err != nil {
		t.Fatalf("overflow: %v", err)
	}
	if got := ctx.Size(conversation.ScopeConversation); got != bound {
		t.Errorf("size %d after overflow, want the bound %d", got, bound)
	}
	if _, ok := ctx.Get(conversation.ScopeConversation, "k00000"); ok {
		t.Error("the oldest entry survived eviction despite distinct timestamps")
	}
	if _, ok := ctx.Get(conversation.ScopeConversation, "overflow"); !ok {
		t.Error("the newest entry was evicted")
	}
	if _, ok := ctx.Get(conversation.ScopeConversation, "k00001"); !ok {
		t.Error("the second-oldest entry was evicted; exactly one victim is expected")
	}

	// --- tied timestamps: invariants ONLY ---
	tiedClock := rt.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	tiedBridge, err := voiceintel.New(voiceintel.WithClock(tiedClock))
	if err != nil {
		t.Fatalf("tied bridge: %v", err)
	}
	tiedConv, err := tiedBridge.Engine().Begin("t11-evict-tied", "")
	if err != nil {
		t.Fatalf("tied begin: %v", err)
	}
	tied := tiedConv.Context()
	total := bound + 16
	for i := 0; i < total; i++ { // clock never advances: every SetAt ties
		if err := tied.Set(conversation.Entry{
			Key: fmt.Sprintf("t%05d", i), Value: i,
			Scope: conversation.ScopeConversation, Source: "t11",
		}); err != nil {
			t.Fatalf("tied %d: %v", i, err)
		}
	}
	if got := tied.Size(conversation.ScopeConversation); got != bound {
		t.Errorf("tied size = %d, want the bound %d", got, bound)
	}
	if _, ok := tied.Get(conversation.ScopeConversation,
		fmt.Sprintf("t%05d", total-1)); !ok {
		t.Error("the newest write was evicted under tied timestamps")
	}
	// NOT ASSERTED: which tied entry was evicted. The frozen contract does not
	// decide it (T7/T9), so neither does this fixture.
}

// ---------------------------------------------------------------------------
// CONCURRENT SESSIONS
// ---------------------------------------------------------------------------

// TestT11_ConcurrentSessionsStayIndependent covers the CONCURRENT category at
// ACCEPTANCE level only. T8 owns concurrency correctness in depth; this asserts
// the single invariant an acceptance matrix needs: simultaneous sessions produce
// independent, individually correct outcomes, and one session's termination does
// not corrupt another.
func TestT11_ConcurrentSessionsStayIndependent(t *testing.T) {
	_, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	// One session per fixture that is a plain single-utterance case.
	var chosen []acceptance
	for _, f := range acceptanceFixtures() {
		if len(f.steps) == 1 && f.steps[0].op == opUtter && f.wantErr == nil {
			chosen = append(chosen, f)
		}
	}
	if len(chosen) < 6 {
		t.Fatalf("only %d single-utterance fixtures; too few to be meaningful", len(chosen))
	}

	const copies = 3 // 3 x ~8 fixtures = 24+ simultaneous sessions
	type job struct {
		f  acceptance
		id conversation.ConversationID
	}
	var jobs []job
	for c := 0; c < copies; c++ {
		for i, f := range chosen {
			jobs = append(jobs, job{f, conversation.ConversationID(
				fmt.Sprintf("t11-con-%d-%02d", c, i))})
		}
	}

	// Sessions are opened on the test goroutine, and each is seeded with a
	// private marker so cross-session context leakage is detectable.
	planners := make([]turnHandler, len(jobs))
	convs := make([]*conversation.Conversation, len(jobs))
	markers := make([]string, len(jobs))
	mustFinish(t, 60*time.Second, "opening sessions", func() error {
		for i, j := range jobs {
			p, conv, err := openPlanner(vi, j.id)
			if err != nil {
				return err
			}
			markers[i] = fmt.Sprintf("marker-%03d", i)
			if err := conv.Context().Set(conversation.Entry{
				Key: "owner", Value: markers[i],
				Scope: conversation.ScopeConversation, Source: "t11",
			}); err != nil {
				return err
			}
			planners[i], convs[i] = p, conv
		}
		return nil
	})

	// One extra session is terminated mid-flight to prove a failure in one
	// session does not corrupt its neighbours.
	victimP, victimConv, err := openPlanner(vi, "t11-con-victim")
	if err != nil {
		t.Fatalf("victim: %v", err)
	}

	gate := newBarrier(len(jobs) + 1)
	problems := make(chan string, len(jobs)*3)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if !gate.wait() {
			return
		}
		if err := victimConv.End("t11_victim"); err != nil {
			gate.abort()
			problems <- fmt.Sprintf("victim End: %v", err)
			return
		}
		if _, err := victimP.Handle(utter("transfer me to rajesh")); !errors.Is(
			err, conversation.ErrTerminal) {
			gate.abort()
			problems <- fmt.Sprintf("victim post-terminal err = %v, want ErrTerminal", err)
		}
	}()

	for i, j := range jobs {
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			if !gate.wait() {
				return
			}
			plan, hErr := planners[i].Handle(utter(j.f.steps[0].text))
			for _, bad := range checkAcceptance(j.f, plan, hErr, convs[i]) {
				gate.abort()
				problems <- fmt.Sprintf("%s (%s): %s", j.f.name, j.id, bad)
				return
			}
			e, ok := convs[i].Context().Get(conversation.ScopeConversation, "owner")
			if !ok || e.Value != markers[i] {
				gate.abort()
				problems <- fmt.Sprintf("%s: owner = %v, want %q — another session's "+
					"context is visible here", j.id, e.Value, markers[i])
			}
		}(i, j)
	}

	awaitAll(t, &wg, gate, 90*time.Second, "concurrent acceptance fixtures")
	close(problems)
	for p := range problems {
		t.Errorf("concurrent session invariant broken: %s", p)
	}
}

// ---------------------------------------------------------------------------
// MULTI-TURN with context
// ---------------------------------------------------------------------------

// TestT11_MultiTurnWithContextRepeatsAcrossFreshServices covers the MULTI_TURN
// category's context requirement: a bounded fact stored early is retrievable
// later, the last-intent context the frozen engine maintains tracks each turn,
// and the whole sequence reproduces on fresh services.
func TestT11_MultiTurnWithContextRepeatsAcrossFreshServices(t *testing.T) {
	type turn struct {
		text       string
		wantIntent conversation.IntentName
	}
	sequence := []turn{
		{"please call me back on 9876543210", intent.IntentRequestCallback},
		{"transfer me to rajesh", intent.IntentRequestTransfer},
		{"say that again", intent.IntentRepeat},
	}
	const fact = "appointment A-7419"

	for pass := 0; pass < 10; pass++ {
		_, vi, cancel, done := runningService(t)

		id := conversation.ConversationID(fmt.Sprintf("t11-mt-%d", pass))
		p, conv, err := openPlanner(vi, id)
		if err != nil {
			t.Fatalf("pass %d: open: %v", pass, err)
		}
		if err := conv.Context().Set(conversation.Entry{
			Key: "appointment", Value: fact,
			Scope: conversation.ScopeConversation, Source: "t11",
		}); err != nil {
			t.Fatalf("pass %d: store: %v", pass, err)
		}

		for n, tn := range sequence {
			plan, err := p.Handle(utter(tn.text))
			if err != nil {
				t.Fatalf("pass %d turn %d: %v", pass, n, err)
			}
			if plan.Intent != tn.wantIntent {
				t.Fatalf("pass %d turn %d: intent = %q, want %q",
					pass, n, plan.Intent, tn.wantIntent)
			}
			if plan.Reason != "intent_accepted" {
				t.Errorf("pass %d turn %d: reason = %q, want intent_accepted",
					pass, n, plan.Reason)
			}
			// The frozen engine records the intent it just resolved.
			last, ok := conv.Context().Get(conversation.ScopeConversation, "last_intent")
			if !ok || last.Value != string(tn.wantIntent) {
				t.Errorf("pass %d turn %d: last_intent = %v, want %q",
					pass, n, last.Value, tn.wantIntent)
			}
			// The fact stored before turn 1 survives every later turn.
			got, ok := conv.Context().Get(conversation.ScopeConversation, "appointment")
			if !ok || got.Value != fact {
				t.Fatalf("pass %d turn %d: appointment = %v (present=%v), want %q",
					pass, n, got.Value, ok, fact)
			}
			if n+1 < len(sequence) {
				if _, err := p.Handle(conversation.Event{
					Kind: conversation.EventSpeechComplete}); err != nil {
					t.Fatalf("pass %d turn %d: speech-complete: %v", pass, n, err)
				}
			}
		}
		if conv.State().IsTerminal() {
			t.Errorf("pass %d: session terminal after a healthy multi-turn sequence", pass)
		}
		cancel()
		<-done
	}
}
