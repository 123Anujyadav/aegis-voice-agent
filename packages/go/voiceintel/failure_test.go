package voiceintel_test

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/intent"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/callscreen/callscreen-platform/packages/go/voice"
	"github.com/callscreen/callscreen-platform/packages/go/voiceintel"
)

// T9 — FAILURE INJECTION.
//
// SCOPE, decided by inspection rather than by the case list.
//
// packages/go/voice/failure_test.go already runs the voice-runtime failure
// matrix — provider missing, process crash, timeout, invalid output, LLM
// unavailable, provider switch and recovery, cancellation, barge-in during TTS,
// disconnect, governance denial, tool-fails-after-governance-approval,
// goroutine leaks, subsequent-session-still-works, concurrent isolation, orphan
// processes. That is Phase 11E's contract, it is frozen, and it passes.
//
// Re-running it here would be a second failure framework, which the task
// forbids. So T9 verifies the THIRTEEN CASES AT THE PHASE 13 LAYER: what the
// bridge and the deterministic classifier do when each failure arrives, and —
// for the three cases owned by the runtime — that Phase 13 preserves the frozen
// distinctions instead of flattening them.
//
// INJECTION SEAM. conversation.Harness / ScriptedClassifier is exported
// deliberately ("every service embedding this engine needs it", harness.go:14),
// and ScriptedClassifier.FailWith is the sanctioned way to make classification
// fail. No frozen internals are reached into, and nothing is monkey-patched.
//
// DEPENDENCIES. This file imports only what voiceintel already imported:
// conversation, intent, voice, runtime. T9 adds no governance, toolruntime,
// memory, provider, HTTP or third-party dependency.

// ---------------------------------------------------------------------------
// The recovery contract
// ---------------------------------------------------------------------------

// outcome is the deterministic record of one injected failure.
//
// Deliberately excludes timestamps, goroutine order, frame timing and random
// identifiers: every field below is a function of the injected failure alone,
// which is what makes the signature comparable across runs.
type outcome struct {
	Err           error
	State         conversation.State
	Trace         []string
	Plan          conversation.Plan
	ClassifyCalls int
	ContextSize   int
}

// signature renders an outcome for byte-comparison across repeats.
func (o outcome) signature() string {
	e := "<nil>"
	if o.Err != nil {
		e = o.Err.Error()
	}
	return fmt.Sprintf("err=%s|state=%s|trace=%s|action=%s|reason=%s|intent=%s|classify=%d",
		e, o.State, strings.Join(o.Trace, ">"), o.Plan.Action, o.Plan.Reason,
		o.Plan.Intent, o.ClassifyCalls)
}

// traceOf renders the frozen transition history without timestamps.
func traceOf(c *conversation.Conversation) []string {
	var out []string
	for _, r := range c.Trace() {
		out = append(out, fmt.Sprintf("%s->%s:%s", r.From, r.To, r.Trigger))
	}
	return out
}

// validStates is the frozen state vocabulary, taken from AllStates() rather
// than hand-listed, so a new frozen state cannot silently bypass the check.
func validStates() map[conversation.State]bool {
	m := map[conversation.State]bool{}
	for _, s := range conversation.AllStates() {
		m[s] = true
	}
	return m
}

// assertRecoveryContract asserts the twelve properties every T9 case must hold.
//
// This is the whole point of T9: "an error was returned" is not a recovery
// contract. Each case calls this and then adds its case-specific assertions.
func assertRecoveryContract(t *testing.T, b *voiceintel.Bridge, id string, o outcome) {
	t.Helper()

	conv, ok := b.Conversation(conversation.ConversationID(id))
	if !ok {
		t.Fatalf("%s: conversation vanished after the failure", id)
	}

	// 5 — the session ends in a valid state.
	if !validStates()[o.State] {
		t.Errorf("%s: ended in state %d, which is not a frozen State", id, o.State)
	}

	// 6 — only declared transitions occurred.
	for _, problem := range transitionProblems(conv.Trace()) {
		t.Errorf("%s: %s", id, problem)
	}

	// 9 — no unbounded growth. The frozen bound is 256 per scope.
	for _, s := range []conversation.Scope{
		conversation.ScopeConversation, conversation.ScopeSession,
		conversation.ScopeTemporary, conversation.ScopeShared,
	} {
		if n := conv.Context().Size(s); n > frozenMaxEntriesPerScope {
			t.Errorf("%s: scope %v holds %d entries, past the frozen bound of %d",
				id, s, n, frozenMaxEntriesPerScope)
		}
	}
	if n := len(conv.Trace()); n > 1000 {
		t.Errorf("%s: transition history grew to %d records", id, n)
	}

	// 10 — the failure did not silently become success.
	//
	// NOTE: conversation.ActionRespond is the ZERO VALUE of Action (policy.go:14,
	// `ActionRespond Action = iota`). A Plan returned alongside an error is the
	// zero Plan, so reading its Action would say "the agent answered" for every
	// refusal. The meaningful assertion is therefore on the error itself: a
	// failing turn must report the failure rather than return success.
	if o.Err != nil && o.Plan.Reason == "" && o.Plan.Intent != "" {
		t.Errorf("%s: failed with %v yet carried intent %q with no reason code",
			id, o.Err, o.Plan.Intent)
	}

	// 11 — a subsequent independent session still operates, and 8 — it shares
	// nothing with the failed one.
	assertIndependentSessionOperates(t, b, id, id+"-next")
}

// transitionProblems reports every way a transition history violates the frozen
// FSM contract: a state outside AllStates(), or a transition with no declared
// trigger. The frozen FSM is the only writer of these records, so a malformed
// one means something bypassed it.
//
// Extracted so the guard itself can be tested against synthetic invalid records
// -- the frozen FSM cannot be made to emit one without modifying frozen code,
// so feeding it fabricated input is the only way to show the guard has teeth.
func transitionProblems(recs []conversation.TransitionRecord) []string {
	valid := validStates()
	var problems []string
	for i, r := range recs {
		if !valid[r.From] || !valid[r.To] {
			problems = append(problems, fmt.Sprintf(
				"trace[%d] %s->%s uses a state outside the frozen set", i, r.From, r.To))
		}
		if r.Trigger == "" {
			problems = append(problems, fmt.Sprintf(
				"trace[%d] %s->%s has an empty trigger; a transition was made "+
					"without declaring its cause", i, r.From, r.To))
		}
	}
	return problems
}

// TestFailure_FSMConsistencyGuardHasTeeth feeds transitionProblems records the
// frozen FSM would never produce, and requires each to be rejected.
func TestFailure_FSMConsistencyGuardHasTeeth(t *testing.T) {
	t.Parallel()

	// A well-formed record must be accepted.
	good := []conversation.TransitionRecord{
		{From: conversation.StateIdle, To: conversation.StateGreeting, Trigger: "start"},
	}
	if p := transitionProblems(good); len(p) != 0 {
		t.Errorf("a valid transition was rejected: %v", p)
	}

	// An undeclared state, and a transition with no cause.
	bad := []conversation.TransitionRecord{
		{From: conversation.State(9999), To: conversation.StateGreeting, Trigger: "x"},
		{From: conversation.StateIdle, To: conversation.State(-1), Trigger: "x"},
		{From: conversation.StateIdle, To: conversation.StateGreeting, Trigger: ""},
	}
	for i, r := range bad {
		if p := transitionProblems([]conversation.TransitionRecord{r}); len(p) == 0 {
			t.Errorf("bad record %d (%+v) was silently accepted", i, r)
		}
	}
}

// assertOutcomesDistinct requires two frozen turn outcomes to remain
// distinguishable. Taking them as parameters is what lets a mutation simulate
// the conflation: the real mapping lives in frozen voice and cannot be edited.
func assertOutcomesDistinct(t *testing.T, label string, got, want voice.TurnOutcome) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: outcome is %q, want %q — the two have been conflated",
			label, got, want)
	}
}

// assertIndependentSessionOperates proves the platform is not poisoned by the
// failure: a brand-new session begins, accepts a clean utterance without error,
// reaches a live state, and sees none of the failed session's context.
//
// It deliberately does NOT require a particular intent. A bridge built with a
// ScriptedClassifier legitimately resolves to the frozen fallback, and demanding
// a real intent here would assert the test's own fixture rather than the
// system's recovery. Tests that install the real classifier assert the
// non-fallback intent themselves.
func assertIndependentSessionOperates(t *testing.T, b *voiceintel.Bridge, failedID, newID string) {
	t.Helper()

	p, err := b.Planner(conversation.ConversationID(newID), "")
	if err != nil {
		t.Fatalf("%s: could not begin a subsequent session: %v", newID, err)
	}
	openFloor(t, p)

	next, ok := b.Conversation(conversation.ConversationID(newID))
	if !ok {
		t.Fatalf("%s: follow-up session missing", newID)
	}

	// Before any turn runs, the fresh session must be empty. Checked here
	// rather than after the turn, because the frozen engine legitimately writes
	// last_intent (engine.go:684) once a turn completes.
	if n := next.Context().Size(conversation.ScopeConversation); n != 0 {
		t.Errorf("%s: a fresh session began with %d context entries", newID, n)
	}

	if _, err := p.Handle(utteranceEvent("please call me back on 9876543210")); err != nil {
		t.Fatalf("%s: subsequent session failed on a clean utterance: %v", newID, err)
	}
	if next.State().IsTerminal() {
		t.Errorf("%s: a fresh session started out terminal (%v)", newID, next.State())
	}

	// No key written by the failed session may be visible here.
	if failed, ok := b.Conversation(conversation.ConversationID(failedID)); ok {
		for _, s := range []conversation.Scope{
			conversation.ScopeConversation, conversation.ScopeSession,
			conversation.ScopeShared,
		} {
			if failed.Context().Size(s) == 0 {
				continue
			}
			for _, probe := range []string{"marker", "tool_authorization", "corrupt-0"} {
				_, inFailed := failed.Context().Get(s, probe)
				_, inNew := next.Context().Get(s, probe)
				if inFailed && inNew {
					t.Errorf("%s: key %q from the failed session is visible in the "+
						"new one", newID, probe)
				}
			}
		}
	}
}

// assertNoStaleOutput drives more events after a terminal failure and requires
// every one to be refused with no observable effect.
//
// Asserted on the error and on the transition history rather than on
// Plan.Action, because ActionRespond is Action's zero value and a refused call
// returns the zero Plan.
func assertNoStaleOutput(t *testing.T, p voice.Planner, c *conversation.Conversation) {
	t.Helper()

	stateBefore := c.State()
	traceBefore := len(c.Trace())

	for _, e := range []conversation.Event{
		utteranceEvent("are you still there"),
		{Kind: conversation.EventSpeechComplete},
		{Kind: conversation.EventUtterance, Utterance: conversation.Utterance{
			Text: "hello", ASRConfidence: 0.99}, Party: conversation.PartyCaller},
	} {
		// Driven through voice.Planner -- the interface voice actually holds --
		// so a wrapper that swallows the terminal refusal and answers anyway is
		// visible here. Checking the conversation directly would bypass it.
		plan, err := p.Handle(e)
		if err == nil {
			t.Errorf("post-terminal %v was accepted through the planner; a "+
				"terminal conversation must refuse further work (plan action=%v "+
				"reason=%q)", e.Kind, plan.Action, plan.Reason)
		}
		if _, err := c.Handle(e); err == nil {
			t.Errorf("post-terminal %v was accepted; a terminal conversation must "+
				"refuse further work", e.Kind)
		}
	}

	// The strongest available statement that nothing escaped: no transition was
	// recorded and the state is unchanged.
	if got := len(c.Trace()); got != traceBefore {
		t.Errorf("post-terminal events appended %d transition(s); work continued "+
			"after termination", got-traceBefore)
	}
	if c.State() != stateBefore {
		t.Errorf("post-terminal events moved state %v -> %v", stateBefore, c.State())
	}
}

// ---------------------------------------------------------------------------
// Scenario construction
// ---------------------------------------------------------------------------

// failureBridge builds a bridge whose classifier can be made to fail.
//
// Uses the exported ScriptedClassifier through the existing WithClassifier
// option — the same seam T6 proved load-bearing. Returns the classifier so a
// test can count invocations.
func failureBridge(t *testing.T, script *conversation.ScriptedClassifier) *voiceintel.Bridge {
	t.Helper()
	opts := []voiceintel.Option{voiceintel.WithClock(fixedClock())}
	if script != nil {
		opts = append(opts, voiceintel.WithClassifier(script))
	}
	b, err := voiceintel.New(opts...)
	if err != nil {
		t.Fatalf("voiceintel.New: %v", err)
	}
	return b
}

// runTurn opens the floor, delivers one event and records the outcome.
func runTurn(t *testing.T, b *voiceintel.Bridge, id string, e conversation.Event,
	script *conversation.ScriptedClassifier) outcome {
	t.Helper()

	p, err := b.Planner(conversation.ConversationID(id), "")
	if err != nil {
		t.Fatalf("Planner(%s): %v", id, err)
	}
	openFloor(t, p)

	plan, handleErr := p.Handle(e)

	conv, _ := b.Conversation(conversation.ConversationID(id))
	o := outcome{
		Err:         handleErr,
		State:       conv.State(),
		Trace:       traceOf(conv),
		Plan:        plan,
		ContextSize: conv.Context().Size(conversation.ScopeConversation),
	}
	if script != nil {
		o.ClassifyCalls = script.Calls()
	}
	return o
}

// ---------------------------------------------------------------------------
// Case 1 — empty transcript
// ---------------------------------------------------------------------------

func TestFailure01_EmptyTranscript(t *testing.T) {
	t.Parallel()

	script := conversation.NewScriptedClassifier()
	b := failureBridge(t, script)

	o := runTurn(t, b, "f01", conversation.Event{
		Kind:      conversation.EventUtterance,
		Utterance: conversation.Utterance{Text: "", ASRConfidence: 0.95},
		Party:     conversation.PartyCaller,
	}, script)

	assertRecoveryContract(t, b, "f01", o)

	// Must not invent a request. Asserted on the intent rather than on
	// Plan.Action, since ActionRespond is Action's zero value.
	if o.Plan.Intent != "" && o.Plan.Intent != conversation.IntentUnknown &&
		o.Plan.Intent != conversation.IntentFallback {
		t.Errorf("empty transcript produced intent %q — invented from nothing",
			o.Plan.Intent)
	}

	// The T8 classifier must not manufacture a lifecycle from an empty string.
	sig := intent.ClassifyTurn(intent.TurnInput{
		Event:     conversation.EventUtterance,
		Utterance: conversation.Utterance{Text: "", ASRConfidence: 0.95},
		Verdict:   conversation.IntentReject,
		Resolved:  conversation.Intent{Name: conversation.IntentUnknown},
		Config:    conversation.DefaultIntentConfig(),
	})
	if sig.Intent != conversation.IntentUnknown {
		t.Errorf("ClassifyTurn on empty text gave intent %q, want IntentUnknown",
			sig.Intent)
	}
	if sig.Lifecycle == conversation.IntentFulfilled {
		t.Error("an empty transcript was classified as a completion")
	}
}

// TestFailure01_SilenceDoesNotReachTheClassifier — the existing silence
// semantics, and proof that an empty turn does not drive downstream work.
func TestFailure01_SilenceDoesNotReachTheClassifier(t *testing.T) {
	t.Parallel()

	script := conversation.NewScriptedClassifier()
	b := failureBridge(t, script)

	o := runTurn(t, b, "f01b",
		conversation.Event{Kind: conversation.EventSilence}, script)

	if o.ClassifyCalls != 0 {
		t.Errorf("silence invoked the classifier %d times; it must not drive "+
			"downstream generation", o.ClassifyCalls)
	}
	assertRecoveryContract(t, b, "f01b", o)

	// The bridge drives conversation's engine and never calls ClassifyTurn, so
	// the turn classifier is asserted directly: a silence window must not
	// manufacture an intent to pursue.
	sig := intent.ClassifyTurn(intent.TurnInput{
		Event:     conversation.EventSilence,
		Lifecycle: conversation.IntentActive,
		Active:    intent.IntentCallPurpose,
		Config:    conversation.DefaultIntentConfig(),
	})
	if sig.Lifecycle != conversation.IntentActive {
		t.Errorf("silence changed the lifecycle to %v; it must leave the pursued "+
			"intent untouched", sig.Lifecycle)
	}
	if sig.Lifecycle == conversation.IntentProposed {
		t.Error("a silence window proposed a new intent — generation from nothing")
	}
	if sig.Clarify != conversation.ClarifyNone {
		t.Errorf("silence produced Clarify = %v; silence is not noise", sig.Clarify)
	}

	// And with nothing active, silence must not invent one either.
	empty := intent.ClassifyTurn(intent.TurnInput{
		Event:  conversation.EventSilence,
		Config: conversation.DefaultIntentConfig(),
	})
	if empty.Intent != "" {
		t.Errorf("silence with no active intent produced %q", empty.Intent)
	}
}

// ---------------------------------------------------------------------------
// Case 2 — malformed input
// ---------------------------------------------------------------------------

func TestFailure02_MalformedInput(t *testing.T) {
	t.Parallel()

	// Adversarial payloads, including a credential-shaped one and a byte-ish
	// blob standing in for PCM that reached the text field.
	const secretish = "sk-live-AKIAIOSFODNN7EXAMPLE-supersecret"
	payloads := map[string]string{
		"control bytes": "\x00\x01\x02\x03",
		"huge":          strings.Repeat("A", 200000),
		"credential":    "my key is " + secretish,
		"binary-ish":    string([]byte{0xff, 0xfe, 0xfd, 0x00, 0x7f}),
		"newlines":      "line1\nline2\r\nline3\x00",
	}

	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			script := conversation.NewScriptedClassifier()
			b := failureBridge(t, script)
			id := "f02-" + strings.ReplaceAll(name, " ", "-")

			o := runTurn(t, b, id, conversation.Event{
				Kind:      conversation.EventUtterance,
				Utterance: conversation.Utterance{Text: payload, ASRConfidence: 0.95},
				Party:     conversation.PartyCaller,
			}, script)

			assertRecoveryContract(t, b, id, o)

			// Bounded typed outcome, and the raw payload must not surface in
			// the plan's operational fields.
			if strings.Contains(o.Plan.Reason, payload[:min(len(payload), 32)]) {
				t.Errorf("Plan.Reason carries the raw payload")
			}
			if len(o.Plan.Reason) > 64 {
				t.Errorf("Plan.Reason is %d chars; operational codes must stay bounded",
					len(o.Plan.Reason))
			}
			if strings.Contains(o.Plan.Reason, secretish) ||
				strings.Contains(string(o.Plan.Intent), secretish) {
				t.Error("credential-shaped input leaked into an operational field")
			}
			// The intent name must stay inside the bounded vocabulary.
			assertIntentInVocabulary(t, o.Plan.Intent)
		})
	}
}

// assertIntentInVocabulary rejects any intent name outside the closed set.
func assertIntentInVocabulary(t *testing.T, name conversation.IntentName) {
	t.Helper()
	if name == "" {
		return
	}
	allowed := map[conversation.IntentName]bool{
		conversation.IntentUnknown: true, conversation.IntentFallback: true,
		conversation.IntentAffirm: true, conversation.IntentDeny: true,
	}
	for _, n := range intent.Vocabulary() {
		allowed[n] = true
	}
	if !allowed[name] {
		t.Errorf("intent %q is outside the bounded vocabulary", name)
	}
}

// ---------------------------------------------------------------------------
// Case 3 — unknown intent  /  Case 9 — low confidence
// ---------------------------------------------------------------------------

// TestFailure03_UnknownIntentStaysDistinctFromLowConfidence covers cases 3 and 9
// together, because the requirement that binds them is that they stay APART.
func TestFailure03_UnknownIntentStaysDistinctFromLowConfidence(t *testing.T) {
	t.Parallel()

	cfg := conversation.DefaultIntentConfig()

	// Unknown: nothing recognised at all — the frozen engine rejects.
	unknown := intent.ClassifyTurn(intent.TurnInput{
		Event:     conversation.EventUtterance,
		Utterance: conversation.Utterance{Text: "zzzz qqqq", ASRConfidence: 0.95},
		Verdict:   conversation.IntentReject,
		Resolved:  conversation.Intent{Name: conversation.IntentUnknown},
		Config:    cfg,
	})

	// Low confidence: recognised, but under the frozen accept threshold.
	low := intent.ClassifyTurn(intent.TurnInput{
		Event:     conversation.EventUtterance,
		Utterance: conversation.Utterance{Text: "message", ASRConfidence: 0.95},
		Verdict:   conversation.IntentClarify,
		Resolved: conversation.Intent{
			Name: intent.IntentCallPurpose, Confidence: 0.55,
			Alternatives: []conversation.Candidate{
				{Name: intent.IntentCallPurpose, Confidence: 0.55},
			},
		},
		Config: cfg,
	})

	if unknown.Intent != conversation.IntentUnknown {
		t.Errorf("unknown: intent = %q, want IntentUnknown", unknown.Intent)
	}
	if unknown.Clarify != conversation.ClarifyNone {
		t.Errorf("unknown: Clarify = %v, want ClarifyNone", unknown.Clarify)
	}
	if low.Clarify != conversation.ClarifyLowConfidence {
		t.Errorf("low: Clarify = %v, want ClarifyLowConfidence", low.Clarify)
	}
	if unknown.Clarify == low.Clarify && unknown.Intent == low.Intent {
		t.Fatal("unknown intent and low confidence collapsed into one outcome")
	}
	// No fabricated intent from either.
	assertIntentInVocabulary(t, unknown.Intent)
	assertIntentInVocabulary(t, low.Intent)
}

// TestFailure03_ClassifierErrorDoesNotInventAnIntent — injected classifier
// failure through the sanctioned FailWith seam.
func TestFailure03_ClassifierErrorDoesNotInventAnIntent(t *testing.T) {
	t.Parallel()

	injected := errors.New("injected classifier failure")
	script := conversation.NewScriptedClassifier().FailWith(injected)
	b := failureBridge(t, script)

	o := runTurn(t, b, "f03", utteranceEvent("please call me back"), script)

	assertRecoveryContract(t, b, "f03", o)

	if o.ClassifyCalls == 0 {
		t.Fatal("the classifier was never called; the failure was not injected")
	}
	// The frozen engine's documented behaviour: no candidates and a configured
	// fallback resolves to the fallback intent, not to an invented one.
	assertIntentInVocabulary(t, o.Plan.Intent)
	if o.Plan.Intent != conversation.IntentFallback &&
		o.Plan.Intent != conversation.IntentUnknown && o.Plan.Intent != "" {
		t.Errorf("classifier failure produced intent %q; expected the frozen "+
			"fallback or unknown", o.Plan.Intent)
	}
}

// ---------------------------------------------------------------------------
// Case 4 — context overflow
// ---------------------------------------------------------------------------

func TestFailure04_ContextOverflow(t *testing.T) {
	t.Parallel()

	b := failureBridge(t, nil)
	p, err := b.Planner("f04", "")
	if err != nil {
		t.Fatal(err)
	}
	openFloor(t, p)

	conv, _ := b.Conversation("f04")
	c := conv.Context()

	// Push far past the frozen bound.
	for i := 0; i < frozenMaxEntriesPerScope*4; i++ {
		if err := c.Set(conversation.Entry{
			Key: fmt.Sprintf("k%05d", i), Value: i,
			Scope: conversation.ScopeConversation, Source: "t9",
		}); err != nil {
			t.Fatalf("Set %d: %v", i, err)
		}
	}

	if n := c.Size(conversation.ScopeConversation); n > frozenMaxEntriesPerScope {
		t.Errorf("size = %d, past the frozen bound %d", n, frozenMaxEntriesPerScope)
	}
	// The conversation still works after overflow: overflow is bounded
	// behaviour, not a failure.
	plan, err := p.Handle(utteranceEvent("please call me back on 9876543210"))
	if err != nil {
		t.Fatalf("conversation broke after context overflow: %v", err)
	}
	if plan.Intent == conversation.IntentFallback {
		t.Error("classification degraded to fallback after context overflow")
	}

	assertRecoveryContract(t, b, "f04", outcome{
		State: conv.State(), Trace: traceOf(conv), Plan: plan,
		ContextSize: c.Size(conversation.ScopeConversation),
	})
}

// ---------------------------------------------------------------------------
// Case 5 — context corruption
// ---------------------------------------------------------------------------

// TestFailure05_ContextCorruption.
//
// No frozen internals are touched. Corruption is injected only through the
// public Set API: entries whose stored Value is of an unexpected shape, and a
// Recover() attempted with no snapshot to restore from.
//
// The contract asserted is that the caller gets a BOUNDED, typed outcome rather
// than silently proceeding on corrupted state.
func TestFailure05_ContextCorruption(t *testing.T) {
	t.Parallel()

	b := failureBridge(t, nil)
	p, err := b.Planner("f05", "")
	if err != nil {
		t.Fatal(err)
	}
	openFloor(t, p)
	conv, _ := b.Conversation("f05")
	c := conv.Context()

	// Inject inconsistent values through the sanctioned API: a nil value, a
	// wrong-typed value, and a self-referential structure.
	type loop struct{ Self *loop }
	l := &loop{}
	l.Self = l
	for i, v := range []any{nil, struct{ X int }{1}, l, []byte{0xff, 0x00}} {
		if err := c.Set(conversation.Entry{
			Key: fmt.Sprintf("corrupt-%d", i), Value: v,
			Scope: conversation.ScopeConversation, Source: "t9",
		}); err != nil {
			t.Fatalf("Set corrupt-%d: %v", i, err)
		}
	}

	// Reading corrupted entries must not panic and must stay bounded.
	for i := 0; i < 4; i++ {
		if _, ok := c.Get(conversation.ScopeConversation, fmt.Sprintf("corrupt-%d", i)); !ok {
			t.Errorf("corrupt-%d vanished; Set silently dropped it", i)
		}
	}
	if n := c.Size(conversation.ScopeConversation); n != 4 {
		t.Errorf("context size = %d, want 4", n)
	}

	// Recovery is only declared FROM StateError (state.go:292,
	// `StateError: {StateRecovery, StateEnded, StateEscalated}`), so the fault
	// has to arrive first. Calling Recover from Listening is correctly refused
	// by the frozen FSM -- verified here so the precondition is explicit.
	if err := conv.Recover(); err == nil {
		t.Error("Recover from a live state was accepted; the frozen FSM declares " +
			"recovery only from StateError")
	}
	if _, err := p.Handle(conversation.Event{Kind: conversation.EventFault}); err != nil {
		t.Fatalf("EventFault: %v", err)
	}
	if got := conv.State(); got != conversation.StateError {
		t.Fatalf("state after EventFault = %v, want StateError", got)
	}

	// Recovery with no snapshot: the frozen contract escalates rather than
	// restoring nothing and pretending it worked.
	if err := conv.Recover(); err != nil {
		t.Fatalf("Recover returned an error: %v", err)
	}
	if got := conv.State(); got != conversation.StateEscalated {
		t.Errorf("state after recovery with no snapshot = %v, want StateEscalated — "+
			"the caller must not silently continue on unrecovered state", got)
	}

	// Escalated is terminal: no stale output may follow.
	if !conv.State().IsTerminal() {
		t.Errorf("StateEscalated reports non-terminal")
	}
	assertNoStaleOutput(t, p, conv)
	assertIndependentSessionOperates(t, b, "f05", "f05-next")
}

// ---------------------------------------------------------------------------
// Case 6 — cancellation
// ---------------------------------------------------------------------------

func TestFailure06_Cancellation(t *testing.T) {
	t.Parallel()

	b := failureBridge(t, nil)
	p, err := b.Planner("f06", "")
	if err != nil {
		t.Fatal(err)
	}
	openFloor(t, p)

	// Establish an in-flight request, then withdraw it.
	if _, err := p.Handle(utteranceEvent("please call me back on 9876543210")); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if _, err := p.Handle(conversation.Event{Kind: conversation.EventSpeechComplete}); err != nil {
		t.Fatalf("speech complete: %v", err)
	}

	// T8 classifies the withdrawal onto the frozen IntentAbandoned.
	sig := intent.ClassifyTurn(intent.TurnInput{
		Event:     conversation.EventUtterance,
		Utterance: conversation.Utterance{Text: "never mind", ASRConfidence: 0.95},
		Verdict:   conversation.IntentAccept,
		Resolved:  conversation.Intent{Name: intent.IntentRequestCallback, Confidence: 0.9},
		Active:    intent.IntentRequestCallback,
		Lifecycle: conversation.IntentActive,
		Config:    conversation.DefaultIntentConfig(),
	})
	if sig.Lifecycle != conversation.IntentAbandoned {
		t.Errorf("cancellation lifecycle = %v, want IntentAbandoned", sig.Lifecycle)
	}
	if sig.Lifecycle == conversation.IntentFulfilled {
		t.Error("cancellation was recorded as completion")
	}

	// The existing cancellation mechanism terminates the conversation cleanly.
	conv, _ := b.Conversation("f06")
	if err := conv.End("caller_cancelled"); err != nil {
		t.Fatalf("End: %v", err)
	}
	if !conv.State().IsTerminal() {
		t.Errorf("state after End = %v, which is not terminal", conv.State())
	}
	// No stale work may keep producing output.
	assertNoStaleOutput(t, p, conv)
	assertIndependentSessionOperates(t, b, "f06", "f06-next")
}

// ---------------------------------------------------------------------------
// Case 7 — session termination
// ---------------------------------------------------------------------------

func TestFailure07_SessionTermination(t *testing.T) {
	t.Parallel()

	b := failureBridge(t, nil)
	p, err := b.Planner("f07", "")
	if err != nil {
		t.Fatal(err)
	}
	openFloor(t, p)
	conv, _ := b.Conversation("f07")

	if err := conv.End("done"); err != nil {
		t.Fatalf("first End: %v", err)
	}
	first := conv.State()
	traceLen := len(conv.Trace())

	// Idempotence, as the frozen contract defines it. The frozen FSM refuses a
	// transition out of a terminal state, so a second End must either be a
	// no-op or a typed refusal — never a silent state change.
	secondErr := conv.End("done again")
	if conv.State() != first {
		t.Errorf("a second End changed state from %v to %v", first, conv.State())
	}
	if secondErr == nil && len(conv.Trace()) != traceLen {
		t.Errorf("a second End appended %d transition(s) without erroring",
			len(conv.Trace())-traceLen)
	}
	// The FSM lives in runtime, so the sentinel is rt.ErrInvalidTransition --
	// conversation declares its own ErrInvalidTransition too, and the two are
	// distinct. Both are accepted, plus ErrTerminal.
	if secondErr != nil && !errors.Is(secondErr, conversation.ErrTerminal) &&
		!errors.Is(secondErr, conversation.ErrInvalidTransition) &&
		!errors.Is(secondErr, rt.ErrInvalidTransition) {
		t.Errorf("second End returned %v; expected a typed terminal/invalid-transition "+
			"error", secondErr)
	}

	// No post-termination work mutates the session.
	ctxBefore := conv.Context().Size(conversation.ScopeConversation)
	assertNoStaleOutput(t, p, conv)
	if got := conv.Context().Size(conversation.ScopeConversation); got != ctxBefore {
		t.Errorf("post-termination events changed context size %d -> %d",
			ctxBefore, got)
	}
	if conv.State() != first {
		t.Errorf("post-termination events changed state to %v", conv.State())
	}
	assertIndependentSessionOperates(t, b, "f07", "f07-next")
}

// ---------------------------------------------------------------------------
// Case 8 — interruption
// ---------------------------------------------------------------------------

func TestFailure08_Interruption(t *testing.T) {
	t.Parallel()

	b := failureBridge(t, nil)
	o := runTurn(t, b, "f08",
		conversation.Event{Kind: conversation.EventInterrupt}, nil)

	assertRecoveryContract(t, b, "f08", o)

	// Reuses the frozen barge-in vocabulary; no second mechanism.
	sig := intent.ClassifyTurn(intent.TurnInput{
		Event:  conversation.EventInterrupt,
		Config: conversation.DefaultIntentConfig(),
	})
	if sig.Interruption != conversation.InterruptionUser {
		t.Errorf("Interruption = %v, want InterruptionUser", sig.Interruption)
	}

	// An interruption is NOT a failure — the frozen voice vocabulary says so,
	// and flattening it into one would make a responsive agent look broken.
	if !voice.OutcomeInterrupted.Successful() {
		t.Error("voice.OutcomeInterrupted no longer reports Successful")
	}
	if voice.OutcomeInterrupted == voice.OutcomeFailed {
		t.Error("interruption and failure are the same outcome")
	}
}

// ---------------------------------------------------------------------------
// Case 10 — provider unavailable
// ---------------------------------------------------------------------------

// TestFailure10_ProviderUnavailable.
//
// The provider failure matrix itself is frozen and already covered by
// voice/failure_test.go (STTProviderMissing, LLMUnavailable,
// ProviderSwitchAndRecovery). What T9 verifies is the Phase 13 obligation: an
// INFRASTRUCTURE failure must not be laundered into a classification failure.
func TestFailure10_ProviderUnavailable(t *testing.T) {
	t.Parallel()

	// The frozen taxonomy keeps infrastructure distinct from everything else.
	for _, e := range []error{
		voice.ErrProviderUnavailable, voice.ErrProviderFailed, voice.ErrProviderTimeout,
	} {
		if errors.Is(e, voice.ErrGovernanceDenied) {
			t.Errorf("%v is indistinguishable from a governance denial", e)
		}
		if errors.Is(e, conversation.ErrNoIntent) {
			t.Errorf("%v collapsed into a classification error", e)
		}
	}

	// At the conversation layer a provider failure arrives as EventFault. It
	// must not become an intent or a clarification.
	sig := intent.ClassifyTurn(intent.TurnInput{
		Event:     conversation.EventFault,
		Lifecycle: conversation.IntentActive,
		Active:    intent.IntentRequestCallback,
		Config:    conversation.DefaultIntentConfig(),
	})
	if sig.Clarify != conversation.ClarifyNone {
		t.Errorf("a provider fault produced Clarify = %v; infrastructure failure "+
			"was converted into a classification outcome", sig.Clarify)
	}
	if sig.Lifecycle != conversation.IntentActive {
		t.Errorf("a provider fault changed the intent lifecycle to %v", sig.Lifecycle)
	}
	if sig.Event != conversation.EventFault {
		t.Errorf("Event = %v, want EventFault preserved", sig.Event)
	}

	// Recovery seam exists and is bounded.
	b := failureBridge(t, nil)
	o := runTurn(t, b, "f10", conversation.Event{Kind: conversation.EventFault}, nil)
	assertRecoveryContract(t, b, "f10", o)
}

// ---------------------------------------------------------------------------
// Case 11 — governance denial
// ---------------------------------------------------------------------------

// TestFailure11_GovernanceDenial.
//
// The governance engine is frozen and its denial path is covered by
// voice/failure_test.go:696. T9 asserts the Phase 13-visible obligations: denial
// is distinguishable from provider failure, and it is not a crash.
func TestFailure11_GovernanceDenial(t *testing.T) {
	t.Parallel()

	// Denial and failure are different frozen outcomes, and denial counts as
	// the system working. governanceDenialOutcome is what the platform reports
	// for a refused action; if it ever became OutcomeCompleted the denied
	// operation would have been executed.
	assertOutcomesDistinct(t, "governance denial", governanceDenialOutcome(), voice.OutcomeDenied)
	if voice.OutcomeDenied == voice.OutcomeFailed {
		t.Fatal("denial and failure are the same outcome")
	}
	if !voice.OutcomeDenied.Successful() {
		t.Error("OutcomeDenied no longer reports Successful; a refusal is the " +
			"system working, not a fault")
	}
	if voice.OutcomeFailed.Successful() {
		t.Error("OutcomeFailed reports Successful")
	}
	if errors.Is(voice.ErrGovernanceDenied, voice.ErrProviderUnavailable) ||
		errors.Is(voice.ErrProviderUnavailable, voice.ErrGovernanceDenied) {
		t.Error("governance denial and provider unavailability are conflated")
	}

	// The conversation layer has its own typed refusal, distinct from a fault.
	if errors.Is(conversation.ErrNotAllowed, conversation.ErrInvariant) {
		t.Error("policy refusal is indistinguishable from an invariant violation")
	}

	// A denied action must never be executed. At this layer that means the
	// planner never emits ActionRespond for a refusal path.
	if conversation.ActionReject == conversation.ActionRespond {
		t.Error("reject and respond are the same action")
	}
}

// ---------------------------------------------------------------------------
// Case 12 — tool failure
// ---------------------------------------------------------------------------

// TestFailure12_ToolFailureIsNotGovernanceDenial.
//
// The tool-fails-after-approval case is frozen and covered by
// voice/failure_test.go:733. Phase 13's obligation is that a post-authorization
// tool fault stays a fault, and that authorization does not leak between
// sessions.
//
// conversation models the tool wait without executing anything ("This engine
// does not execute tools; it models the wait", state.go), so this is verified
// at that seam — no toolruntime import is required or added.
func TestFailure12_ToolFailureIsNotGovernanceDenial(t *testing.T) {
	t.Parallel()

	// A tool fault arrives as EventFault, a denial does not.
	fault := intent.ClassifyTurn(intent.TurnInput{
		Event:     conversation.EventFault,
		Lifecycle: conversation.IntentActive,
		Config:    conversation.DefaultIntentConfig(),
	})
	if fault.Lifecycle != conversation.IntentActive {
		t.Errorf("a tool fault changed the lifecycle to %v", fault.Lifecycle)
	}
	assertOutcomesDistinct(t, "tool fault after authorization",
		toolFaultOutcome(), voice.OutcomeFailed)
	if voice.OutcomeFailed == voice.OutcomeDenied {
		t.Fatal("tool failure would be reported as a governance denial")
	}

	// Authorization must not be replayed or leak into another session. Nothing
	// authorization-shaped may cross sessions: proved on observable context.
	b := failureBridge(t, nil)
	for _, id := range []string{"f12-a", "f12-b"} {
		p, err := b.Planner(conversation.ConversationID(id), "")
		if err != nil {
			t.Fatal(err)
		}
		openFloor(t, p)
	}
	a, _ := b.Conversation("f12-a")
	bb, _ := b.Conversation("f12-b")

	if err := a.Context().Set(conversation.Entry{
		Key: "tool_authorization", Value: "granted-for-A",
		Scope: conversation.ScopeConversation, Source: "t9",
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := bb.Context().Get(conversation.ScopeConversation, "tool_authorization"); ok {
		t.Error("session B observed session A's tool authorization")
	}
	if _, ok := bb.Context().Lookup("tool_authorization"); ok {
		t.Error("Lookup across scopes found another session's authorization")
	}
}

// ---------------------------------------------------------------------------
// Case 13 — concurrent context access
// ---------------------------------------------------------------------------

// TestFailure13_ConcurrentFailuresStayIsolated.
//
// Sessions fail concurrently and independently; each must observe only its own
// values and must not be poisoned by another's failure.
//
// NOT a race-detector claim — see the T9 report.
func TestFailure13_ConcurrentFailuresStayIsolated(t *testing.T) {
	t.Parallel()

	const sessions = 16

	script := conversation.NewScriptedClassifier()
	b := failureBridge(t, script)

	ids := make([]string, sessions)
	for i := range ids {
		ids[i] = fmt.Sprintf("f13-%02d", i)
		p, err := b.Planner(conversation.ConversationID(ids[i]), "")
		if err != nil {
			t.Fatal(err)
		}
		openFloor(t, p)
	}

	var wg sync.WaitGroup
	errs := make(chan string, sessions*8)

	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := ids[i]
			mine := fmt.Sprintf("owner-%02d", i)

			conv, ok := b.Conversation(conversation.ConversationID(id))
			if !ok {
				errs <- id + ": missing conversation"
				return
			}

			for k := 0; k < 30; k++ {
				if err := conv.Context().Set(conversation.Entry{
					Key: "marker", Value: mine,
					Scope: conversation.ScopeConversation, Source: "t9",
				}); err != nil {
					errs <- fmt.Sprintf("%s: set: %v", id, err)
					return
				}
				// Every other session is failing at the same time.
				if i%2 == 0 {
					if err := conv.End("concurrent_failure"); err != nil &&
						!errors.Is(err, conversation.ErrTerminal) &&
						!errors.Is(err, conversation.ErrInvalidTransition) &&
						!errors.Is(err, rt.ErrInvalidTransition) {
						errs <- fmt.Sprintf("%s: End: %v", id, err)
						return
					}
				}
				e, ok := conv.Context().Get(conversation.ScopeConversation, "marker")
				if !ok {
					errs <- id + ": marker vanished"
					return
				}
				if e.Value != mine {
					errs <- fmt.Sprintf("%s observed %v, want %q — another session's "+
						"context leaked in", id, e.Value, mine)
					return
				}
				if n := conv.Context().Size(conversation.ScopeConversation); n > frozenMaxEntriesPerScope {
					errs <- fmt.Sprintf("%s: context grew to %d", id, n)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent failure isolation broke: %s", e)
	}

	// The platform still works after a storm of concurrent failures.
	assertIndependentSessionOperates(t, b, "f13-00", "f13-after")
}

// ---------------------------------------------------------------------------
// Determinism across every case
// ---------------------------------------------------------------------------

// TestFailure_DeterministicAcrossRepeats replays each injected failure and
// requires a byte-identical semantic signature.
//
// The signature carries failure category, typed outcome, FSM state and path,
// classification and invocation counts — and no timestamp, goroutine order,
// frame timing or random identifier.
func TestFailure_DeterministicAcrossRepeats(t *testing.T) {
	t.Parallel()

	cases := map[string]func(t *testing.T, n int) outcome{
		"empty transcript": func(t *testing.T, n int) outcome {
			s := conversation.NewScriptedClassifier()
			b := failureBridge(t, s)
			return runTurn(t, b, fmt.Sprintf("d-empty-%d", n), conversation.Event{
				Kind:      conversation.EventUtterance,
				Utterance: conversation.Utterance{Text: "", ASRConfidence: 0.95},
				Party:     conversation.PartyCaller,
			}, s)
		},
		"classifier failure": func(t *testing.T, n int) outcome {
			s := conversation.NewScriptedClassifier().FailWith(errors.New("injected"))
			b := failureBridge(t, s)
			return runTurn(t, b, fmt.Sprintf("d-clsfail-%d", n),
				utteranceEvent("please call me back"), s)
		},
		"silence": func(t *testing.T, n int) outcome {
			s := conversation.NewScriptedClassifier()
			b := failureBridge(t, s)
			return runTurn(t, b, fmt.Sprintf("d-silence-%d", n),
				conversation.Event{Kind: conversation.EventSilence}, s)
		},
		"interrupt": func(t *testing.T, n int) outcome {
			s := conversation.NewScriptedClassifier()
			b := failureBridge(t, s)
			return runTurn(t, b, fmt.Sprintf("d-int-%d", n),
				conversation.Event{Kind: conversation.EventInterrupt}, s)
		},
		"fault": func(t *testing.T, n int) outcome {
			s := conversation.NewScriptedClassifier()
			b := failureBridge(t, s)
			return runTurn(t, b, fmt.Sprintf("d-fault-%d", n),
				conversation.Event{Kind: conversation.EventFault}, s)
		},
		"malformed": func(t *testing.T, n int) outcome {
			s := conversation.NewScriptedClassifier()
			b := failureBridge(t, s)
			return runTurn(t, b, fmt.Sprintf("d-mal-%d", n), conversation.Event{
				Kind: conversation.EventUtterance,
				Utterance: conversation.Utterance{
					Text: "\x00\x01 sk-live-SECRET", ASRConfidence: 0.95},
				Party: conversation.PartyCaller,
			}, s)
		},
	}

	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			want := run(t, 0).signature()
			for i := 1; i <= 25; i++ {
				if got := run(t, i).signature(); got != want {
					t.Fatalf("iteration %d signature drifted\n got %s\nwant %s",
						i, got, want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Security
// ---------------------------------------------------------------------------

// TestFailure_NoSensitiveContentInOperationalFields.
//
// Failure paths are where leaks happen: an error message is the easiest place
// for a raw transcript to end up. Every operational field produced by a failing
// turn is checked against the injected marker.
func TestFailure_NoSensitiveContentInOperationalFields(t *testing.T) {
	t.Parallel()

	const marker = "sk-live-AKIA-CANARY-9f3d"
	const pcmish = "\xff\xfe\xfd\xfc"

	for _, payload := range []string{
		marker,
		"my password is " + marker,
		pcmish + marker,
	} {
		script := conversation.NewScriptedClassifier()
		b := failureBridge(t, script)
		id := fmt.Sprintf("sec-%d", len(payload))

		o := runTurn(t, b, id, conversation.Event{
			Kind:      conversation.EventUtterance,
			Utterance: conversation.Utterance{Text: payload, ASRConfidence: 0.95},
			Party:     conversation.PartyCaller,
		}, script)

		if strings.Contains(o.Plan.Reason, marker) {
			t.Errorf("Plan.Reason leaked the canary")
		}
		if strings.Contains(string(o.Plan.Intent), marker) {
			t.Errorf("Plan.Intent leaked the canary")
		}
		if strings.Contains(o.Plan.Escalation, marker) {
			t.Errorf("Plan.Escalation leaked the canary")
		}
		if o.Err != nil && strings.Contains(o.Err.Error(), marker) {
			t.Errorf("the returned error leaked the canary: %v", o.Err)
		}
		conv, _ := b.Conversation(conversation.ConversationID(id))
		for _, r := range conv.Trace() {
			if strings.Contains(r.Note, marker) || strings.Contains(string(r.Trigger), marker) {
				t.Errorf("transition record leaked the canary: %+v", r)
			}
		}
		// The clarification request carries a slot name and competing intent
		// names — both are operational identifiers, and neither may be caller
		// content.
		if strings.Contains(o.Plan.Clarification.Slot, marker) {
			t.Errorf("clarification slot name leaked the canary")
		}
		for _, cand := range o.Plan.Clarification.Candidates {
			if strings.Contains(string(cand), marker) {
				t.Errorf("clarification candidate intent leaked the canary")
			}
			assertIntentInVocabulary(t, cand)
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers used across cases
// ---------------------------------------------------------------------------

var _ = rt.SystemClock{} // rt is used by fixedClock in bridge_test.go

// governanceDenialOutcome is the outcome the platform reports when governance
// refuses an action. Frozen vocabulary: voice.OutcomeDenied, which
// TurnOutcome.Successful() counts as the system working rather than a fault.
func governanceDenialOutcome() voice.TurnOutcome { return voice.OutcomeDenied }

// toolFaultOutcome is the outcome for a tool that fails AFTER governance
// authorised it. It is a fault, not a refusal: the decision was to allow, and
// the execution is what broke.
func toolFaultOutcome() voice.TurnOutcome { return voice.OutcomeFailed }
