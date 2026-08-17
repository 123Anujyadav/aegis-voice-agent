package voiceintel_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/intent"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/callscreen/callscreen-platform/packages/go/voice"
	"github.com/callscreen/callscreen-platform/packages/go/voiceintel"
)

// T6 INTEGRATION TESTS.
//
// These drive the REAL deterministic classifier through the REAL frozen
// conversation engine, entered through the REAL voice.Planner interface, using
// the exact event voice constructs at pipeline.go:942:
//
//	conversation.Event{
//	    Kind:      conversation.EventUtterance,
//	    Utterance: conversation.Utterance{Text: final.Text},
//	    Party:     conversation.PartyCaller,
//	}
//
// No fake classifier is used except where a test's whole point is to prove the
// real one is what makes the difference.
//
// WHAT THESE TESTS DO NOT DO, stated plainly: they do not stand up a full
// voice.Pipeline. That needs a Registry, Intel, Governor, Generator, Output and
// FSM, and voice's doubles for those live in its _test.go files and are not
// importable — voice has no harness.go. So the audio path ABOVE the planner
// seam is not exercised here. What is exercised is every layer from the seam
// down, which is where T6's subject lives.

// utteranceEvent is byte-for-byte the event voice sends.
func utteranceEvent(text string) conversation.Event {
	return conversation.Event{
		Kind:      conversation.EventUtterance,
		Utterance: conversation.Utterance{Text: text, ASRConfidence: 0.95},
		Party:     conversation.PartyCaller,
	}
}

// fixedClock keeps conversations deterministic.
func fixedClock() *rt.FakeClock {
	return rt.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
}

// newPlanner builds a bridge with the REAL classifier and starts one session.
func newPlanner(t *testing.T, id string, opts ...voiceintel.Option) voice.Planner {
	t.Helper()
	opts = append([]voiceintel.Option{voiceintel.WithClock(fixedClock())}, opts...)
	b, err := voiceintel.New(opts...)
	if err != nil {
		t.Fatalf("voiceintel.New: %v", err)
	}
	p, err := b.Planner(conversation.ConversationID(id), "")
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}
	openFloor(t, p)
	return p
}

// openFloor drives the opening turn so the caller holds the floor.
//
// Discovered while building T6, and it is a property of the FROZEN turn
// manager rather than of this bridge: immediately after EventStart the agent
// owns the floor for its greeting, so a caller utterance arriving then is
// QUEUED, not classified — Handle returns Plan{Action: ignore, Reason:
// "floor_queued"} (engine.go:544) and never reaches the intent engine.
//
// EventGreetingComplete ends that opening turn. A real deployment sends it when
// the greeting audio finishes; a test that omits it is not testing
// classification at all, which is exactly the trap the first run of this suite
// fell into — every plan came back with an empty Intent.
func openFloor(t *testing.T, p voice.Planner) {
	t.Helper()
	if _, err := p.Handle(conversation.Event{Kind: conversation.EventStart}); err != nil {
		t.Fatalf("EventStart: %v", err)
	}
	if _, err := p.Handle(conversation.Event{Kind: conversation.EventGreetingComplete}); err != nil {
		t.Fatalf("EventGreetingComplete: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 1. The real classifier produces a non-fallback intent
// ---------------------------------------------------------------------------

// TestRealClassifierProducesNonFallbackIntent is T6's headline.
//
// The utterance is one the T3 classifier is explicitly built to recognise:
// "call me back" is a three-token cue for request_callback (weight 4, saturating
// at 1.0), and the digit run fills the required callback_number slot so the
// frozen Complete() is satisfied.
//
// Before this bridge existed, this same utterance resolved to the FALLBACK
// intent, because no production code ever called WithClassifier
// (intent.go:277). That is the difference this test measures.
func TestRealClassifierProducesNonFallbackIntent(t *testing.T) {
	t.Parallel()

	p := newPlanner(t, "conv-nonfallback")

	plan, err := p.Handle(utteranceEvent("please call me back on 9876543210"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if plan.Intent == conversation.IntentFallback {
		t.Fatal("intent is the FALLBACK — the classifier is not reaching the engine")
	}
	if plan.Intent == conversation.IntentUnknown || plan.Intent == "" {
		t.Fatalf("intent = %q, want a recognised intent", plan.Intent)
	}
	// Hand-declared, not read from intent.Vocabulary().
	if plan.Intent != "request_callback" {
		t.Errorf("intent = %q, want request_callback", plan.Intent)
	}
	t.Logf("voice.Planner → conversation → real classifier → intent %q, action %v",
		plan.Intent, plan.Action)
}

// TestSeveralKnownUtterancesEachResolveToTheirOwnIntent — one lucky phrase
// could be a coincidence; distinct phrases resolving to distinct intents cannot.
func TestSeveralKnownUtterancesEachResolveToTheirOwnIntent(t *testing.T) {
	t.Parallel()

	cases := []struct{ text, want string }{
		{"please call me back on 9876543210", "request_callback"},
		{"transfer me to rajesh", "request_transfer"},
		{"say that again", "repeat"},
		{"goodbye", "end_call"},
	}
	for _, c := range cases {
		p := newPlanner(t, "conv-"+c.want)
		plan, err := p.Handle(utteranceEvent(c.text))
		if err != nil {
			t.Fatalf("Handle(%q): %v", c.text, err)
		}
		if plan.Intent == conversation.IntentFallback {
			t.Errorf("%q fell back; the classifier is not being consulted", c.text)
			continue
		}
		if string(plan.Intent) != c.want {
			t.Errorf("%q → intent %q, want %q", c.text, plan.Intent, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 2. Unknown input stays bounded
// ---------------------------------------------------------------------------

// TestUnknownUtteranceRemainsBounded — an unrecognised utterance must not
// invent an intent. The classifier returns no candidates and the FROZEN engine
// applies its own fallback, which is the correct and unchanged behaviour.
func TestUnknownUtteranceRemainsBounded(t *testing.T) {
	t.Parallel()

	// Hand-declared closed set; nothing outside it may appear.
	allowed := map[string]bool{
		"affirm": true, "deny": true, "greeting": true, "caller_identity": true,
		"call_purpose": true, "leave_message": true, "request_callback": true,
		"request_transfer": true, "repeat": true, "hold": true, "end_call": true,
		"fallback": true, "unknown": true, "": true,
	}

	for _, text := range []string{
		"xyzzy plugh frobnicate",
		"the quick brown fox",
		"$(rm -rf /) ghp_aBcDeFgHiJkLmNoPqRs",
		"'; DROP TABLE conversations;--",
		strings.Repeat("qwertyuiop ", 500),
	} {
		p := newPlanner(t, "conv-unknown-"+fmt.Sprint(len(text)))
		plan, err := p.Handle(utteranceEvent(text))
		if err != nil {
			t.Fatalf("Handle(%q): %v", text, err)
		}
		if !allowed[string(plan.Intent)] {
			t.Errorf("Handle(%q) produced invented intent %q", text, plan.Intent)
		}
		if plan.Confidence < 0 || plan.Confidence > 1 {
			t.Errorf("Handle(%q) confidence %v outside [0,1]", text, plan.Confidence)
		}
		// The caller's words must not reach a field that becomes a log line or
		// a metric label. Plan.Reason is documented "Never caller content".
		if strings.Contains(plan.Reason, "qwerty") || strings.Contains(plan.Reason, "DROP") {
			t.Errorf("caller text leaked into Plan.Reason: %q", plan.Reason)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. The injection is actually load-bearing
// ---------------------------------------------------------------------------

// TestClassifierInjectionIsActuallyUsed proves the wiring matters by comparing
// against the state of the world BEFORE this bridge: an engine with no
// classifier. If both produced the same plan, the bridge would be decorative.
//
// This is the in-test equivalent of mutation M1/M2, kept permanently so a
// future refactor that drops WithClassifier fails here rather than silently
// reverting every utterance to fallback.
func TestClassifierInjectionIsActuallyUsed(t *testing.T) {
	t.Parallel()

	const text = "please call me back on 9876543210"

	// With the real classifier.
	withReal, err := voiceintel.New(voiceintel.WithClock(fixedClock()))
	if err != nil {
		t.Fatal(err)
	}
	pReal, err := withReal.Planner("conv-real", "")
	if err != nil {
		t.Fatal(err)
	}
	openFloor(t, pReal)
	planReal, err := pReal.Handle(utteranceEvent(text))
	if err != nil {
		t.Fatal(err)
	}

	// Without one — conversation.NewEngine directly, exactly as
	// voice/e2e_test.go:328 does today.
	engNone, err := conversation.NewEngine(conversation.DefaultConfig(),
		conversation.WithClock(fixedClock()))
	if err != nil {
		t.Fatal(err)
	}
	convNone, err := engNone.Begin("conv-none", "")
	if err != nil {
		t.Fatal(err)
	}
	openFloor(t, convNone)
	planNone, err := convNone.Handle(utteranceEvent(text))
	if err != nil {
		t.Fatal(err)
	}

	if planNone.Intent != conversation.IntentFallback {
		t.Fatalf("baseline assumption broken: an engine with NO classifier produced "+
			"%q, not the fallback; this test can no longer measure the difference",
			planNone.Intent)
	}
	if planReal.Intent == planNone.Intent {
		t.Fatalf("injecting the real classifier changed nothing: both produced %q",
			planReal.Intent)
	}
	t.Logf("no classifier → %q; real classifier → %q", planNone.Intent, planReal.Intent)
}

// TestSubstitutedClassifierChangesTheOutcome — the seam accepts a different
// implementation and the result follows it. This is what makes ADR-0016's
// "a model-backed classifier drops in behind the same interface" a checked
// claim rather than an aspiration.
func TestSubstitutedClassifierChangesTheOutcome(t *testing.T) {
	t.Parallel()

	const text = "please call me back on 9876543210"

	b, err := voiceintel.New(
		voiceintel.WithClock(fixedClock()),
		voiceintel.WithClassifier(constantClassifier{name: conversation.IntentAffirm}),
	)
	if err != nil {
		t.Fatal(err)
	}
	p, err := b.Planner("conv-substituted", "")
	if err != nil {
		t.Fatal(err)
	}
	openFloor(t, p)
	plan, err := p.Handle(utteranceEvent(text))
	if err != nil {
		t.Fatal(err)
	}

	if plan.Intent != conversation.IntentAffirm {
		t.Errorf("intent = %q, want %q — the substituted classifier is not being "+
			"consulted, which means the real one may not be either",
			plan.Intent, conversation.IntentAffirm)
	}
	if plan.Intent == "request_callback" {
		t.Error("the REAL classifier answered despite a substitution; the seam is " +
			"not honouring WithClassifier")
	}
}

// constantClassifier always proposes one intent. Used only to prove the seam is
// live — never to stand in for the real classifier in a behavioural assertion.
type constantClassifier struct{ name conversation.IntentName }

func (c constantClassifier) Classify(
	conversation.Utterance, conversation.Expectation,
) ([]conversation.Candidate, []conversation.Slot, error) {
	return []conversation.Candidate{{Name: c.name, Confidence: 1.0}}, nil, nil
}

// ---------------------------------------------------------------------------
// 4. The voice.Planner seam itself
// ---------------------------------------------------------------------------

// TestVoicePlannerUsesRealClassifier exercises the value strictly through the
// voice.Planner interface — the same static type voice's pipeline holds — so
// nothing here depends on it being a *conversation.Conversation.
func TestVoicePlannerUsesRealClassifier(t *testing.T) {
	t.Parallel()

	var p voice.Planner = newPlanner(t, "conv-seam")

	plan, err := p.Handle(utteranceEvent("transfer me to rajesh"))
	if err != nil {
		t.Fatalf("Handle through voice.Planner: %v", err)
	}
	if plan.Intent != "request_transfer" {
		t.Errorf("intent through the voice.Planner seam = %q, want request_transfer",
			plan.Intent)
	}
}

// TestBridgePlannerSatisfiesVoicePlannerStatically — compile-time, at the seam.
func TestBridgePlannerSatisfiesVoicePlannerStatically(t *testing.T) {
	t.Parallel()

	b, err := voiceintel.New(voiceintel.WithClock(fixedClock()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.Planner("conv-static", "")
	if err != nil {
		t.Fatal(err)
	}
	// Assigning to the interface type voice declares is the assertion.
	var _ voice.Planner = got
	if got == nil {
		t.Fatal("Planner returned nil")
	}
}

// ---------------------------------------------------------------------------
// 5. Concurrent session isolation
// ---------------------------------------------------------------------------

// TestConcurrentBridgeIsolation — one Bridge, one shared classifier, many
// sessions. Each must resolve its own utterance, and no session may see
// another's.
//
// This is not a race-detector claim; see the T6 report. It is a behavioural
// statement about isolation.
func TestConcurrentBridgeIsolation(t *testing.T) {
	t.Parallel()

	b, err := voiceintel.New(voiceintel.WithClock(fixedClock()))
	if err != nil {
		t.Fatal(err)
	}

	type session struct{ text, want string }
	// Deliberately no "goodbye" here. end_call drives the conversation to a
	// TERMINAL state, after which EventSpeechComplete is correctly refused
	// ("conversation: terminal") — so a repeat loop over it would be testing
	// the frozen lifecycle rather than session isolation. end_call is covered
	// once, in TestSeveralKnownUtterancesEachResolveToTheirOwnIntent.
	sessions := []session{
		{"please call me back on 9876543210", "request_callback"},
		{"transfer me to rajesh", "request_transfer"},
		{"say that again", "repeat"},
	}

	const perSession = 8
	var wg sync.WaitGroup
	errs := make(chan string, len(sessions)*perSession*4)

	for i := 0; i < len(sessions)*4; i++ {
		s := sessions[i%len(sessions)]
		id := conversation.ConversationID(fmt.Sprintf("conv-%d", i))
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, err := b.Planner(id, "")
			if err != nil {
				errs <- fmt.Sprintf("Planner(%s): %v", id, err)
				return
			}
			if _, err := p.Handle(conversation.Event{Kind: conversation.EventStart}); err != nil {
				errs <- fmt.Sprintf("start(%s): %v", id, err)
				return
			}
			if _, err := p.Handle(conversation.Event{Kind: conversation.EventGreetingComplete}); err != nil {
				errs <- fmt.Sprintf("greeting(%s): %v", id, err)
				return
			}
			for k := 0; k < perSession; k++ {
				plan, err := p.Handle(utteranceEvent(s.text))
				if err != nil {
					errs <- fmt.Sprintf("handle(%s): %v", id, err)
					return
				}
				if string(plan.Intent) != s.want {
					errs <- fmt.Sprintf("session %s said %q but resolved to %q, want %q "+
						"— another session's classification leaked in",
						id, s.text, plan.Intent, s.want)
					return
				}
				// Give the floor back. The agent takes it to answer, so without
				// this the NEXT utterance is queued rather than classified —
				// the same frozen turn-taking rule openFloor documents. A real
				// deployment sends this when its response audio finishes.
				if _, err := p.Handle(conversation.Event{
					Kind: conversation.EventSpeechComplete,
				}); err != nil {
					errs <- fmt.Sprintf("speech-complete(%s): %v", id, err)
					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent isolation failed: %s", e)
	}
}

// ---------------------------------------------------------------------------
// 6. Frozen conversation semantics are untouched
// ---------------------------------------------------------------------------

// TestExistingConversationSemanticsRemainIntact — the bridge supplies
// candidates; every DECISION stays with the frozen engine.
func TestExistingConversationSemanticsRemainIntact(t *testing.T) {
	t.Parallel()

	t.Run("noise below MinASRConfidence never reaches the classifier", func(t *testing.T) {
		p := newPlanner(t, "conv-noise")
		e := utteranceEvent("please call me back on 9876543210")
		// Frozen MinASRConfidence is 0.40; the engine rejects as noise at
		// intent.go:309, BEFORE any classifier is consulted.
		e.Utterance.ASRConfidence = 0.05

		plan, err := p.Handle(e)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Intent == "request_callback" {
			t.Error("a sub-threshold utterance was classified; the frozen noise gate " +
				"has been bypassed")
		}
	})

	t.Run("a required unfilled slot still yields clarification", func(t *testing.T) {
		p := newPlanner(t, "conv-incomplete")
		// No number, so the frozen Complete() is false and the engine — not this
		// package — decides to clarify.
		plan, err := p.Handle(utteranceEvent("please call me back"))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Intent != "request_callback" {
			t.Fatalf("intent = %q, want request_callback", plan.Intent)
		}
		if plan.Action == conversation.ActionRespond {
			t.Error("an intent missing its required slot was answered outright; " +
				"the frozen completion check is not being applied")
		}
	})

	t.Run("fallback behaviour is unchanged for unrecognised input", func(t *testing.T) {
		p := newPlanner(t, "conv-fallback")
		plan, err := p.Handle(utteranceEvent("xyzzy plugh"))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Intent != conversation.IntentFallback {
			t.Errorf("intent = %q, want the frozen fallback %q",
				plan.Intent, conversation.IntentFallback)
		}
	})
}

// TestSlotValuesDoNotCrossTheClassifierPort records T4's architectural finding
// as an executable check rather than a comment.
//
// conversation.Slot has no value field, so a spoken number can fill the slot
// while its digits never appear in anything the planner returns.
func TestSlotValuesDoNotCrossTheClassifierPort(t *testing.T) {
	t.Parallel()

	const number = "9876543210"
	p := newPlanner(t, "conv-slotvalue")

	plan, err := p.Handle(utteranceEvent("please call me back on " + number))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Intent != "request_callback" {
		t.Fatalf("intent = %q, want request_callback", plan.Intent)
	}
	rendered := fmt.Sprintf("%+v", plan)
	if strings.Contains(rendered, number) {
		t.Errorf("the spoken number reached the plan: %s", rendered)
	}
}

// TestDeterministicAcrossRepeatedSessions — same utterance, fresh session, same
// plan. Anything derived from map order or entropy would show here.
func TestDeterministicAcrossRepeatedSessions(t *testing.T) {
	t.Parallel()

	const text = "please call me back on 9876543210"
	var first string
	for i := 0; i < 50; i++ {
		p := newPlanner(t, fmt.Sprintf("conv-det-%d", i))
		plan, err := p.Handle(utteranceEvent(text))
		if err != nil {
			t.Fatal(err)
		}
		got := fmt.Sprintf("%s|%v|%v|%.17g", plan.Intent, plan.Action,
			plan.Expectation, plan.Confidence)
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("iteration %d differed\n  first: %s\n  got:   %s", i, first, got)
		}
	}
}

// intentPackageIsTheOneUnderTest guards against this file quietly drifting onto
// a different classifier.
var _ conversation.IntentClassifier = (*intent.Classifier)(nil)
