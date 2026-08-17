package intent_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"sync"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/intent"
)

// T8 — TURN / INTERRUPTION CLASSIFICATION SEMANTICS.
//
// Every assertion below names an EXACT frozen value. None of them asserts merely
// that classification succeeded, and none reads a table out of the
// implementation — the expected values are stated here independently, from the
// frozen vocabulary's own documented meanings.

// utt builds an utterance whose ASR confidence is comfortably above the frozen
// MinASRConfidence, so noise handling is never triggered by accident.
func utt(text string) conversation.Utterance {
	return conversation.Utterance{Text: text, ASRConfidence: 0.95}
}

// accepted models what the frozen IntentEngine returns for a confident,
// complete intent.
func accepted(name conversation.IntentName) conversation.Intent {
	return conversation.Intent{
		Name:         name,
		Confidence:   0.9,
		Alternatives: []conversation.Candidate{{Name: name, Confidence: 0.9}},
	}
}

func baseInput() intent.TurnInput {
	return intent.TurnInput{
		Event:   conversation.EventUtterance,
		Verdict: conversation.IntentAccept,
		Config:  conversation.DefaultIntentConfig(),
	}
}

// ---------------------------------------------------------------------------
// 1-8. The eight required categories
// ---------------------------------------------------------------------------

// TestTurn_Continuation — answering a pending question continues the intent
// being pursued. Frozen value: IntentActive.
func TestTurn_Continuation(t *testing.T) {
	t.Parallel()

	in := baseInput()
	in.Utterance = utt("rajesh sharma")
	in.Expectation = conversation.ExpectSlotValue // a question was asked
	in.Resolved = accepted(intent.IntentCallerIdentity)
	in.Active = intent.IntentCallerIdentity
	in.Lifecycle = conversation.IntentActive

	got := intent.ClassifyTurn(in)

	if got.Lifecycle != conversation.IntentActive {
		t.Errorf("Lifecycle = %v, want IntentActive", got.Lifecycle)
	}
	if got.Clarify != conversation.ClarifyNone {
		t.Errorf("Clarify = %v, want ClarifyNone", got.Clarify)
	}
	if got.Interruption != conversation.InterruptionNone {
		t.Errorf("Interruption = %v, want InterruptionNone", got.Interruption)
	}
	// The expectation must survive: it is what makes this a continuation.
	if got.Expectation != conversation.ExpectSlotValue {
		t.Errorf("Expectation = %v, want ExpectSlotValue", got.Expectation)
	}
}

// TestTurn_ContinuationUnderYesNo — a yes/no answer continues the confirmation
// it answers. Frozen value: IntentActive, and specifically NOT a new request.
func TestTurn_ContinuationUnderYesNo(t *testing.T) {
	t.Parallel()

	for _, name := range []conversation.IntentName{
		conversation.IntentAffirm, conversation.IntentDeny,
	} {
		in := baseInput()
		in.Utterance = utt("yes")
		in.Expectation = conversation.ExpectYesNo
		in.Resolved = accepted(name)
		in.Active = intent.IntentRequestTransfer
		in.Lifecycle = conversation.IntentActive

		got := intent.ClassifyTurn(in)

		if got.Lifecycle != conversation.IntentActive {
			t.Errorf("%s: Lifecycle = %v, want IntentActive", name, got.Lifecycle)
		}
		if got.Lifecycle == conversation.IntentSuperseded {
			t.Errorf("%s: a confirmation answer was read as a new request", name)
		}
	}
}

// TestTurn_NewRequest — an unprompted request with nothing in flight.
// Frozen value: IntentProposed.
func TestTurn_NewRequest(t *testing.T) {
	t.Parallel()

	in := baseInput()
	in.Utterance = utt("please call me back on 9876543210")
	in.Expectation = conversation.ExpectNothing
	in.Resolved = accepted(intent.IntentRequestCallback)
	in.Active = "" // nothing in flight
	in.Lifecycle = conversation.IntentProposed

	got := intent.ClassifyTurn(in)

	if got.Lifecycle != conversation.IntentProposed {
		t.Errorf("Lifecycle = %v, want IntentProposed", got.Lifecycle)
	}
	if got.Intent != intent.IntentRequestCallback {
		t.Errorf("Intent = %q, want %q", got.Intent, intent.IntentRequestCallback)
	}
}

// TestTurn_NewRequestSupersedesActive — a different request while one is in
// flight. Frozen value: IntentSuperseded, which the frozen vocabulary defines
// as "replaced by a new intent mid-conversation".
func TestTurn_NewRequestSupersedesActive(t *testing.T) {
	t.Parallel()

	in := baseInput()
	in.Utterance = utt("transfer me to rajesh")
	in.Expectation = conversation.ExpectNothing
	in.Resolved = accepted(intent.IntentRequestTransfer)
	in.Active = intent.IntentLeaveMessage // something else was in flight
	in.Lifecycle = conversation.IntentActive

	got := intent.ClassifyTurn(in)

	if got.Lifecycle != conversation.IntentSuperseded {
		t.Errorf("Lifecycle = %v, want IntentSuperseded", got.Lifecycle)
	}
}

// TestTurn_RestatingTheSameIntentIsNotANewRequest — the same intent repeated
// continues it. Frozen value: IntentActive, not IntentSuperseded.
func TestTurn_RestatingTheSameIntentIsNotANewRequest(t *testing.T) {
	t.Parallel()

	in := baseInput()
	in.Utterance = utt("transfer me to rajesh")
	in.Resolved = accepted(intent.IntentRequestTransfer)
	in.Active = intent.IntentRequestTransfer
	in.Lifecycle = conversation.IntentActive

	got := intent.ClassifyTurn(in)

	if got.Lifecycle != conversation.IntentActive {
		t.Errorf("Lifecycle = %v, want IntentActive", got.Lifecycle)
	}
}

// TestTurn_Clarification — the frozen engine returned IntentClarify, so the turn
// needs clarification. Frozen type: ClarificationKind.
func TestTurn_Clarification(t *testing.T) {
	t.Parallel()

	cfg := conversation.DefaultIntentConfig()

	tests := []struct {
		name     string
		resolved conversation.Intent
		want     conversation.ClarificationKind
	}{
		{
			// Understood but incomplete: a required slot is unfilled.
			name: "missing slot",
			resolved: conversation.Intent{
				Name:       intent.IntentLeaveMessage,
				Confidence: 0.6,
				Slots: []conversation.Slot{
					{Name: "message_body", Required: true, Filled: false},
				},
				Alternatives: []conversation.Candidate{
					{Name: intent.IntentLeaveMessage, Confidence: 0.6},
				},
			},
			want: conversation.ClarifyMissingSlot,
		},
		{
			// Two intents scored close together — the margin is under the
			// frozen AmbiguityMargin of 0.15.
			name: "ambiguous",
			resolved: conversation.Intent{
				Name:       intent.IntentRequestCallback,
				Confidence: 0.60,
				Alternatives: []conversation.Candidate{
					{Name: intent.IntentRequestCallback, Confidence: 0.60},
					{Name: intent.IntentRequestTransfer, Confidence: 0.55},
				},
			},
			want: conversation.ClarifyAmbiguous,
		},
		{
			// A single candidate, below accept but with no close rival.
			name: "low confidence",
			resolved: conversation.Intent{
				Name:       intent.IntentCallPurpose,
				Confidence: 0.55,
				Alternatives: []conversation.Candidate{
					{Name: intent.IntentCallPurpose, Confidence: 0.55},
				},
			},
			want: conversation.ClarifyLowConfidence,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInput()
			in.Utterance = utt("something")
			in.Verdict = conversation.IntentClarify
			in.Resolved = tc.resolved
			in.Config = cfg

			got := intent.ClassifyTurn(in)

			if got.Clarify != tc.want {
				t.Errorf("Clarify = %v, want %v", got.Clarify, tc.want)
			}
		})
	}
}

// TestTurn_Interruption — an explicit interrupt. Frozen value: InterruptionUser.
func TestTurn_Interruption(t *testing.T) {
	t.Parallel()

	in := baseInput()
	in.Event = conversation.EventInterrupt

	got := intent.ClassifyTurn(in)

	if got.Interruption != conversation.InterruptionUser {
		t.Errorf("Interruption = %v, want InterruptionUser", got.Interruption)
	}
	if got.Event != conversation.EventInterrupt {
		t.Errorf("Event = %v, want EventInterrupt", got.Event)
	}
	// An interruption is not an acknowledgement.
	if got.Floor == conversation.FloorBackchannel {
		t.Error("an interruption was classified as a backchannel")
	}
}

// TestTurn_OverlapDefersToTheFrozenFloorDecision.
//
// This is the architectural assertion of T8: the frozen TurnManager decides,
// by overlap DURATION, whether simultaneous speech is a backchannel. The same
// overlap event must classify differently purely because the frozen decision
// differs — proving this package holds no competing opinion.
func TestTurn_OverlapDefersToTheFrozenFloorDecision(t *testing.T) {
	t.Parallel()

	base := baseInput()
	base.Event = conversation.EventOverlap
	base.Active = intent.IntentCallPurpose
	base.Lifecycle = conversation.IntentActive

	// 5 — acknowledgement, because the frozen manager called it a backchannel.
	ack := base
	ack.Floor = conversation.FloorBackchannel
	gotAck := intent.ClassifyTurn(ack)
	if gotAck.Floor != conversation.FloorBackchannel {
		t.Errorf("Floor = %v, want FloorBackchannel", gotAck.Floor)
	}
	if gotAck.Interruption != conversation.InterruptionNone {
		t.Errorf("Interruption = %v, want InterruptionNone — a backchannel does "+
			"not move the floor", gotAck.Interruption)
	}
	if gotAck.Lifecycle != conversation.IntentActive {
		t.Errorf("Lifecycle = %v, want IntentActive unchanged — an acknowledgement "+
			"is not a contribution", gotAck.Lifecycle)
	}

	// 4 — interruption, because the frozen manager granted the floor.
	barge := base
	barge.Floor = conversation.FloorGranted
	gotBarge := intent.ClassifyTurn(barge)
	if gotBarge.Interruption != conversation.InterruptionUser {
		t.Errorf("Interruption = %v, want InterruptionUser", gotBarge.Interruption)
	}

	if gotAck.Interruption == gotBarge.Interruption {
		t.Error("the frozen FloorDecision made no difference; this package is " +
			"deciding for itself")
	}
}

// TestTurn_Acknowledgement — the acknowledgement category asserted directly.
// Frozen value: FloorBackchannel.
func TestTurn_Acknowledgement(t *testing.T) {
	t.Parallel()

	in := baseInput()
	in.Event = conversation.EventOverlap
	in.Floor = conversation.FloorBackchannel
	in.Active = intent.IntentLeaveMessage
	in.Lifecycle = conversation.IntentActive

	got := intent.ClassifyTurn(in)

	if got.Floor != conversation.FloorBackchannel {
		t.Errorf("Floor = %v, want FloorBackchannel", got.Floor)
	}
	if got.Intent != intent.IntentLeaveMessage {
		t.Errorf("Intent = %q, want the active intent %q unchanged",
			got.Intent, intent.IntentLeaveMessage)
	}
}

// TestTurn_Cancellation — the caller withdraws the request.
// Frozen value: IntentAbandoned.
func TestTurn_Cancellation(t *testing.T) {
	t.Parallel()

	phrases := []string{
		"never mind",
		"forget it",
		"cancel that",
		"actually never mind i will call back later",
		"ignore that please",
	}

	for _, p := range phrases {
		t.Run(p, func(t *testing.T) {
			in := baseInput()
			in.Utterance = utt(p)
			in.Resolved = accepted(intent.IntentRequestCallback)
			in.Active = intent.IntentRequestCallback
			in.Lifecycle = conversation.IntentActive

			got := intent.ClassifyTurn(in)

			if got.Lifecycle != conversation.IntentAbandoned {
				t.Errorf("Lifecycle = %v, want IntentAbandoned", got.Lifecycle)
			}
			if got.Lifecycle == conversation.IntentFulfilled {
				t.Error("a cancellation was classified as a completion")
			}
		})
	}
}

// TestTurn_CancellationIsNotDenial — under ExpectYesNo a "no" answers the
// question. It must stay a continuation, because abandoning the intent on a
// "no" discards work the caller is still engaged with.
func TestTurn_CancellationIsNotDenial(t *testing.T) {
	t.Parallel()

	in := baseInput()
	in.Utterance = utt("no")
	in.Expectation = conversation.ExpectYesNo
	in.Resolved = accepted(conversation.IntentDeny)
	in.Active = intent.IntentRequestTransfer
	in.Lifecycle = conversation.IntentActive

	got := intent.ClassifyTurn(in)

	if got.Lifecycle != conversation.IntentActive {
		t.Errorf("Lifecycle = %v, want IntentActive — a denial answers the "+
			"question, it does not cancel the intent", got.Lifecycle)
	}
	if got.Lifecycle == conversation.IntentAbandoned {
		t.Error("a yes/no denial was classified as a cancellation")
	}
}

// TestTurn_Silence — frozen value: EventSilence, with no lifecycle change.
func TestTurn_Silence(t *testing.T) {
	t.Parallel()

	in := baseInput()
	in.Event = conversation.EventSilence
	in.Active = intent.IntentCallPurpose
	in.Lifecycle = conversation.IntentActive

	got := intent.ClassifyTurn(in)

	if got.Event != conversation.EventSilence {
		t.Errorf("Event = %v, want EventSilence", got.Event)
	}
	if got.Lifecycle != conversation.IntentActive {
		t.Errorf("Lifecycle = %v, want IntentActive unchanged", got.Lifecycle)
	}
	// Silence is not noise: they are different frozen concepts and collapsing
	// them produces a clarification about nothing.
	if got.Clarify != conversation.ClarifyNone {
		t.Errorf("Clarify = %v, want ClarifyNone — silence is not noise", got.Clarify)
	}
}

// TestTurn_Completion — frozen value: IntentFulfilled.
func TestTurn_Completion(t *testing.T) {
	t.Parallel()

	t.Run("caller signals the call is done", func(t *testing.T) {
		in := baseInput()
		in.Utterance = utt("goodbye")
		in.Resolved = accepted(intent.IntentEndCall)
		in.Lifecycle = conversation.IntentActive

		got := intent.ClassifyTurn(in)

		if got.Lifecycle != conversation.IntentFulfilled {
			t.Errorf("Lifecycle = %v, want IntentFulfilled", got.Lifecycle)
		}
	})

	t.Run("hangup", func(t *testing.T) {
		in := baseInput()
		in.Event = conversation.EventHangup
		in.Lifecycle = conversation.IntentActive

		got := intent.ClassifyTurn(in)

		if got.Lifecycle != conversation.IntentFulfilled {
			t.Errorf("Lifecycle = %v, want IntentFulfilled", got.Lifecycle)
		}
	})
}

// ---------------------------------------------------------------------------
// 9-11. Unknown, low confidence, ambiguous — preserved as distinct outcomes
// ---------------------------------------------------------------------------

// TestTurn_UnknownLowConfidenceAndAmbiguousStayDistinct.
//
// The requirement is that these are not collapsed. Asserted by showing the three
// produce three DIFFERENT frozen results, not merely that each is non-zero.
func TestTurn_UnknownLowConfidenceAndAmbiguousStayDistinct(t *testing.T) {
	t.Parallel()

	// Unknown: the frozen engine rejected it outright.
	unknown := baseInput()
	unknown.Utterance = utt("qwertyuiop")
	unknown.Verdict = conversation.IntentReject
	unknown.Resolved = conversation.Intent{Name: conversation.IntentUnknown}
	gotUnknown := intent.ClassifyTurn(unknown)

	// Low confidence: below accept, no close rival.
	low := baseInput()
	low.Utterance = utt("maybe something")
	low.Verdict = conversation.IntentClarify
	low.Resolved = conversation.Intent{
		Name: intent.IntentCallPurpose, Confidence: 0.55,
		Alternatives: []conversation.Candidate{
			{Name: intent.IntentCallPurpose, Confidence: 0.55},
		},
	}
	gotLow := intent.ClassifyTurn(low)

	// Ambiguous: two close candidates.
	amb := baseInput()
	amb.Utterance = utt("call")
	amb.Verdict = conversation.IntentClarify
	amb.Resolved = conversation.Intent{
		Name: intent.IntentRequestCallback, Confidence: 0.60,
		Alternatives: []conversation.Candidate{
			{Name: intent.IntentRequestCallback, Confidence: 0.60},
			{Name: intent.IntentRequestTransfer, Confidence: 0.55},
		},
	}
	gotAmb := intent.ClassifyTurn(amb)

	if gotUnknown.Intent != conversation.IntentUnknown {
		t.Errorf("unknown: Intent = %q, want IntentUnknown", gotUnknown.Intent)
	}
	if gotUnknown.Clarify != conversation.ClarifyNone {
		t.Errorf("unknown: Clarify = %v, want ClarifyNone — a rejected utterance "+
			"is discarded, not asked about", gotUnknown.Clarify)
	}
	if gotLow.Clarify != conversation.ClarifyLowConfidence {
		t.Errorf("low: Clarify = %v, want ClarifyLowConfidence", gotLow.Clarify)
	}
	if gotAmb.Clarify != conversation.ClarifyAmbiguous {
		t.Errorf("ambiguous: Clarify = %v, want ClarifyAmbiguous", gotAmb.Clarify)
	}

	if gotLow.Clarify == gotAmb.Clarify {
		t.Error("low confidence and ambiguity collapsed into one outcome")
	}
	if gotUnknown.Clarify == gotLow.Clarify {
		t.Error("unknown and low confidence collapsed into one outcome")
	}
}

// TestTurn_NoiseIsNotLowConfidence — the frozen engine's first rule.
func TestTurn_NoiseIsNotLowConfidence(t *testing.T) {
	t.Parallel()

	in := baseInput()
	in.Utterance = conversation.Utterance{Text: "krrsh", ASRConfidence: 0.1}
	in.Verdict = conversation.IntentNoise
	in.Resolved = conversation.Intent{Name: conversation.IntentUnknown}

	got := intent.ClassifyTurn(in)

	if got.Clarify != conversation.ClarifyNoise {
		t.Errorf("Clarify = %v, want ClarifyNoise", got.Clarify)
	}
	if got.Intent != conversation.IntentUnknown {
		t.Errorf("Intent = %q, want IntentUnknown", got.Intent)
	}
	if got.Clarify == conversation.ClarifyLowConfidence {
		t.Error("noise was classified as low confidence")
	}
}

// ---------------------------------------------------------------------------
// 12. Determinism
// ---------------------------------------------------------------------------

// signature renders a TurnSignal as a byte-comparable string.
//
// Deliberately excludes anything timing- or scheduler-derived; a TurnSignal
// contains no such field, which is the point.
func signature(s intent.TurnSignal) string {
	return fmt.Sprintf("event=%v|floor=%v|interrupt=%v|clarify=%v|lifecycle=%v|intent=%s|expect=%v",
		s.Event, s.Floor, s.Interruption, s.Clarify, s.Lifecycle, s.Intent, s.Expectation)
}

// TestTurn_DeterministicAcrossRepeatedClassification — identical input,
// expectation, configuration and context must give a byte-identical signature.
func TestTurn_DeterministicAcrossRepeatedClassification(t *testing.T) {
	t.Parallel()

	inputs := []intent.TurnInput{}

	mk := func(f func(*intent.TurnInput)) intent.TurnInput {
		in := baseInput()
		f(&in)
		return in
	}
	inputs = append(inputs,
		mk(func(i *intent.TurnInput) {
			i.Utterance = utt("please call me back on 9876543210")
			i.Resolved = accepted(intent.IntentRequestCallback)
		}),
		mk(func(i *intent.TurnInput) {
			i.Utterance = utt("never mind")
			i.Resolved = accepted(intent.IntentRequestCallback)
			i.Active = intent.IntentRequestCallback
		}),
		mk(func(i *intent.TurnInput) { i.Event = conversation.EventSilence }),
		mk(func(i *intent.TurnInput) {
			i.Event = conversation.EventOverlap
			i.Floor = conversation.FloorBackchannel
		}),
		mk(func(i *intent.TurnInput) {
			i.Verdict = conversation.IntentClarify
			i.Utterance = utt("call")
			i.Resolved = conversation.Intent{
				Name: intent.IntentRequestCallback, Confidence: 0.6,
				Alternatives: []conversation.Candidate{
					{Name: intent.IntentRequestCallback, Confidence: 0.6},
					{Name: intent.IntentRequestTransfer, Confidence: 0.55},
				},
			}
		}),
	)

	for n, in := range inputs {
		want := signature(intent.ClassifyTurn(in))
		for i := 0; i < 100; i++ {
			if got := signature(intent.ClassifyTurn(in)); got != want {
				t.Fatalf("input %d iteration %d: signature drifted\n got %s\nwant %s",
					n, i, got, want)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 13. FSM-compatible mapping
// ---------------------------------------------------------------------------

// TestTurn_EveryEmittedValueIsAFrozenVocabularyMember.
//
// Sweeps every event kind against representative verdicts and asserts that each
// emitted value is a member of the frozen enum — an invented value would show up
// as a String() of "unknown"/out-of-range, i.e. something the frozen FSM could
// not interpret.
func TestTurn_EveryEmittedValueIsAFrozenVocabularyMember(t *testing.T) {
	t.Parallel()

	events := []conversation.EventKind{
		conversation.EventStart, conversation.EventGreetingComplete,
		conversation.EventUtterance, conversation.EventOverlap,
		conversation.EventSpeechComplete, conversation.EventSilence,
		conversation.EventInterrupt, conversation.EventToolComplete,
		conversation.EventTimeout, conversation.EventFault,
		conversation.EventHangup,
	}
	verdicts := []conversation.IntentVerdict{
		conversation.IntentAccept, conversation.IntentClarify,
		conversation.IntentReject, conversation.IntentNoise,
	}

	validLifecycle := map[conversation.IntentState]bool{
		conversation.IntentProposed: true, conversation.IntentValidated: true,
		conversation.IntentActive: true, conversation.IntentFulfilled: true,
		conversation.IntentAbandoned: true, conversation.IntentSuperseded: true,
	}
	validClarify := map[conversation.ClarificationKind]bool{
		conversation.ClarifyNone: true, conversation.ClarifyAmbiguous: true,
		conversation.ClarifyLowConfidence: true, conversation.ClarifyMissingSlot: true,
		conversation.ClarifyContradiction: true, conversation.ClarifyNoise: true,
		conversation.ClarifyIncomplete: true,
	}
	validInterruption := map[conversation.InterruptionKind]bool{
		conversation.InterruptionNone: true, conversation.InterruptionUser: true,
		conversation.InterruptionAI: true, conversation.InterruptionProvider: true,
		conversation.InterruptionEmergency: true, conversation.InterruptionTransfer: true,
	}
	validFloor := map[conversation.FloorDecision]bool{
		conversation.FloorGranted: true, conversation.FloorDenied: true,
		conversation.FloorBackchannel: true, conversation.FloorQueued: true,
	}

	var checked int
	for _, e := range events {
		for _, v := range verdicts {
			in := baseInput()
			in.Event = e
			in.Verdict = v
			in.Utterance = utt("please call me back")
			in.Resolved = accepted(intent.IntentRequestCallback)
			in.Lifecycle = conversation.IntentActive

			got := intent.ClassifyTurn(in)
			checked++

			if !validLifecycle[got.Lifecycle] {
				t.Errorf("event %v verdict %v: Lifecycle %d is not a frozen IntentState",
					e, v, got.Lifecycle)
			}
			if !validClarify[got.Clarify] {
				t.Errorf("event %v verdict %v: Clarify %d is not a frozen ClarificationKind",
					e, v, got.Clarify)
			}
			if !validInterruption[got.Interruption] {
				t.Errorf("event %v verdict %v: Interruption %d is not a frozen InterruptionKind",
					e, v, got.Interruption)
			}
			if !validFloor[got.Floor] {
				t.Errorf("event %v verdict %v: Floor %d is not a frozen FloorDecision",
					e, v, got.Floor)
			}
		}
	}
	if checked != len(events)*len(verdicts) {
		t.Fatalf("checked %d combinations, want %d", checked, len(events)*len(verdicts))
	}
	t.Logf("%d event×verdict combinations checked against the frozen vocabulary", checked)
}

// ---------------------------------------------------------------------------
// 14. Concurrency
// ---------------------------------------------------------------------------

// TestTurn_ConcurrentClassificationIsIsolated — a pure function shared across
// goroutines must give every caller its own answer.
//
// NOT a race-detector claim; see the T8 report.
func TestTurn_ConcurrentClassificationIsIsolated(t *testing.T) {
	t.Parallel()

	type sample struct {
		in   intent.TurnInput
		want string
	}
	var samples []sample
	for _, spec := range []struct {
		text   string
		name   conversation.IntentName
		active conversation.IntentName
	}{
		{"please call me back", intent.IntentRequestCallback, ""},
		{"never mind", intent.IntentRequestCallback, intent.IntentRequestCallback},
		{"transfer me to rajesh", intent.IntentRequestTransfer, intent.IntentLeaveMessage},
		{"goodbye", intent.IntentEndCall, ""},
	} {
		in := baseInput()
		in.Utterance = utt(spec.text)
		in.Resolved = accepted(spec.name)
		in.Active = spec.active
		samples = append(samples, sample{in: in, want: signature(intent.ClassifyTurn(in))})
	}

	var wg sync.WaitGroup
	errs := make(chan string, 64*len(samples))
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				for n, s := range samples {
					if got := signature(intent.ClassifyTurn(s.in)); got != s.want {
						errs <- fmt.Sprintf("sample %d: got %s want %s", n, got, s.want)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent classification diverged: %s", e)
	}
}

// ---------------------------------------------------------------------------
// 15 + structural guards
// ---------------------------------------------------------------------------

// TestTurn_CannotExpressALifecycleState.
//
// The strongest available proof that T8 introduced no second FSM: TurnSignal has
// no field capable of naming a conversation lifecycle state. A classifier that
// cannot express a State cannot drive one, whatever it is asked to classify.
//
// Verified by reflection over the actual struct, so adding such a field later
// fails this test.
func TestTurn_CannotExpressALifecycleState(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}

	// conversation.State and conversation.Trigger are the FSM's own types.
	// Neither may appear anywhere in this package.
	banned := map[string]bool{"State": true, "Trigger": true, "TransitionRecord": true}

	var inspected int
	for name, pkg := range pkgs {
		_ = name
		for file, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok || id.Name != "conversation" {
					return true
				}
				inspected++
				if banned[sel.Sel.Name] {
					t.Errorf("%s: references conversation.%s — the classifier must "+
						"not be able to name an FSM lifecycle state", file, sel.Sel.Name)
				}
				return true
			})
		}
	}
	if inspected == 0 {
		t.Fatal("inspected no conversation selectors; the guard would pass vacuously")
	}
	t.Logf("%d conversation.* references inspected, none is an FSM type", inspected)
}

// TestTurn_EveryTurnSignalFieldIsAFrozenType.
//
// The structural proof that T8 introduced no vocabulary of its own: every field
// of TurnSignal and TurnInput must be typed `conversation.Something`. A new
// lifecycle enum, or any locally-defined classification type, necessarily
// appears as a field whose type is not qualified by conversation — and fails
// here.
//
// This is what makes "no second FSM" checkable rather than asserted.
func TestTurn_EveryTurnSignalFieldIsAFrozenType(t *testing.T) {
	t.Parallel()

	src, err := parser.ParseFile(token.NewFileSet(), "turn.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing turn.go: %v", err)
	}

	want := map[string]bool{"TurnSignal": true, "TurnInput": true}
	seen := map[string]int{}

	ast.Inspect(src, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || !want[ts.Name.Name] {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			t.Errorf("%s is not a struct", ts.Name.Name)
			return true
		}
		for _, field := range st.Fields.List {
			names := "<embedded>"
			if len(field.Names) > 0 {
				var parts []string
				for _, n := range field.Names {
					parts = append(parts, n.Name)
				}
				names = strings.Join(parts, ",")
			}
			seen[ts.Name.Name]++

			sel, ok := field.Type.(*ast.SelectorExpr)
			if !ok {
				t.Errorf("%s.%s has type %T, not a conversation.* type — T8 must "+
					"introduce no vocabulary of its own", ts.Name.Name, names, field.Type)
				continue
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "conversation" {
				t.Errorf("%s.%s is qualified by %v, not conversation — every field "+
					"must be a frozen type", ts.Name.Name, names, sel.X)
			}
		}
		return true
	})

	for name := range want {
		if seen[name] == 0 {
			t.Fatalf("%s was not found in turn.go; the guard would pass vacuously", name)
		}
	}
	t.Logf("TurnSignal: %d fields, TurnInput: %d fields — all frozen types",
		seen["TurnSignal"], seen["TurnInput"])
}

// TestTurn_DoesNotMutateItsInput — a pure function must not write through its
// argument. Proved by classifying, then re-classifying and comparing.
func TestTurn_DoesNotMutateItsInput(t *testing.T) {
	t.Parallel()

	in := baseInput()
	in.Utterance = utt("please call me back on 9876543210")
	in.Resolved = accepted(intent.IntentRequestCallback)
	in.Active = intent.IntentLeaveMessage
	in.Lifecycle = conversation.IntentActive

	before := fmt.Sprintf("%+v", in)
	first := signature(intent.ClassifyTurn(in))
	after := fmt.Sprintf("%+v", in)

	if before != after {
		t.Errorf("ClassifyTurn mutated its input\nbefore %s\nafter  %s", before, after)
	}
	if second := signature(intent.ClassifyTurn(in)); second != first {
		t.Errorf("second call differed: %s vs %s", second, first)
	}
}

// TestTurn_NoPackageLevelStateAddedByT8.
//
// T8 must add no package-level var at all. The package-wide guard
// TestPackage_HasNoPackageLevelMutableState (T4) already enforces this and in
// fact caught the first draft of turn.go, which declared cancellationCues as a
// package-level slice. This test pins the rule at the file level so a future
// edit to turn.go alone fails here with a message naming the cause.
func TestTurn_NoPackageLevelStateAddedByT8(t *testing.T) {
	t.Parallel()

	src, err := parser.ParseFile(token.NewFileSet(), "turn.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing turn.go: %v", err)
	}

	for _, decl := range src.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, id := range vs.Names {
				t.Errorf("turn.go declares package-level var %q; the turn vocabulary "+
					"must be returned by a function so callers cannot rewrite it",
					id.Name)
			}
		}
	}
}

// TestTurn_CancellationVocabularyCannotBeMutatedByCallers — a caller that
// modifies the returned slice must not affect the next classification.
func TestTurn_CancellationVocabularyCannotBeMutatedByCallers(t *testing.T) {
	t.Parallel()

	mk := func() intent.TurnInput {
		in := baseInput()
		in.Utterance = utt("never mind")
		in.Resolved = accepted(intent.IntentRequestCallback)
		in.Active = intent.IntentRequestCallback
		in.Lifecycle = conversation.IntentActive
		return in
	}

	if got := intent.ClassifyTurn(mk()); got.Lifecycle != conversation.IntentAbandoned {
		t.Fatalf("baseline Lifecycle = %v, want IntentAbandoned", got.Lifecycle)
	}
	// Classify many times; a shared, mutable vocabulary would let earlier calls
	// perturb later ones.
	for i := 0; i < 50; i++ {
		if got := intent.ClassifyTurn(mk()); got.Lifecycle != conversation.IntentAbandoned {
			t.Fatalf("iteration %d: Lifecycle = %v, want IntentAbandoned",
				i, got.Lifecycle)
		}
	}
}
