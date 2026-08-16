package conversation

import (
	"errors"
	"testing"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// ---------------------------------------------------------------------------
// State machine
// ---------------------------------------------------------------------------

// TestStateMachine_TableIsWellFormed proves three structural properties of the
// transition table that a hand-read cannot guarantee once it has seventeen
// states: nothing is unreachable, nothing is a dead end, and terminal means
// terminal.
func TestStateMachine_TableIsWellFormed(t *testing.T) {
	t.Parallel()
	table := transitionTable()

	// Every state appears in the table.
	for _, s := range AllStates() {
		if _, ok := table[s]; !ok {
			t.Errorf("state %s has no entry in the transition table", s)
		}
	}

	// Terminal states have no outgoing edges.
	for _, s := range terminalStates() {
		if len(table[s]) != 0 {
			t.Errorf("terminal state %s declares %d outgoing edge(s)", s, len(table[s]))
		}
	}

	// Every state is reachable from Idle.
	reached := map[State]bool{StateIdle: true}
	queue := []State{StateIdle}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range table[cur] {
			if !reached[next] {
				reached[next] = true
				queue = append(queue, next)
			}
		}
	}
	for _, s := range AllStates() {
		if !reached[s] {
			t.Errorf("state %s is unreachable from Idle — it can never occur", s)
		}
	}

	// Every non-terminal state can reach a terminal state. A state that cannot
	// is a conversation that can never end, which on a phone line is a call
	// that never hangs up.
	for _, s := range AllStates() {
		if s.IsTerminal() {
			continue
		}
		seen := map[State]bool{s: true}
		q := []State{s}
		ok := false
		for len(q) > 0 && !ok {
			cur := q[0]
			q = q[1:]
			for _, next := range table[cur] {
				if next.IsTerminal() {
					ok = true
					break
				}
				if !seen[next] {
					seen[next] = true
					q = append(q, next)
				}
			}
		}
		if !ok {
			t.Errorf("state %s cannot reach any terminal state", s)
		}
	}
}

func TestStateMachine_RefusesUndeclaredTransition(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c, err := h.Begin("c1")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	// Idle -> Speaking is not declared: the greeting must come first.
	if err := c.transition(StateSpeaking, TriggerPlanned, ""); !errors.Is(err, rt.ErrInvalidTransition) {
		t.Fatalf("expected an invalid-transition error, got %v", err)
	}
	if c.State() != StateIdle {
		t.Fatalf("a refused transition changed state to %s", c.State())
	}
	if h.Metrics.InvalidAttempts.Total() != 1 {
		t.Fatal("an invalid attempt should be counted, so a bad call path is visible")
	}
}

// TestStateMachine_GreetingCannotBeBypassed asserts the structural absence that
// enforces the announcement guarantee: there is no edge from Idle to Listening.
func TestStateMachine_GreetingCannotBeBypassed(t *testing.T) {
	t.Parallel()
	for _, to := range transitionTable()[StateIdle] {
		if to == StateListening {
			t.Fatal("Idle -> Listening exists; the greeting could be skipped, " +
				"and the greeting carries the caller's lawful basis (I1)")
		}
	}
}

func TestStateMachine_TerminalIsTerminal(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c, _ := h.Begin("c1")

	_ = c.transition(StateGreeting, TriggerStart, "")
	if err := c.transition(StateEnded, TriggerHangup, ""); err != nil {
		t.Fatalf("Greeting -> Ended: %v", err)
	}
	if err := c.transition(StateListening, TriggerUtterance, ""); err == nil {
		t.Fatal("a terminal conversation must refuse further transitions")
	}
	if _, err := c.Handle(Event{Kind: EventUtterance}); !errors.Is(err, ErrTerminal) {
		t.Fatalf("Handle on a terminal conversation should return ErrTerminal, got %v", err)
	}
}

func TestState_AwaitingClassification(t *testing.T) {
	t.Parallel()
	if !StateListening.IsAwaiting() || StateListening.IsConstrained() {
		t.Fatal("Listening is awaiting but unconstrained")
	}
	for _, s := range []State{StateClarification, StateConfirmation, StateQuestion} {
		if !s.IsAwaiting() || !s.IsConstrained() {
			t.Fatalf("%s must be a constrained awaiting state", s)
		}
	}
	if StateSpeaking.IsAwaiting() {
		t.Fatal("Speaking is not an awaiting state")
	}
}

// ---------------------------------------------------------------------------
// Turn manager
// ---------------------------------------------------------------------------

func newTurnManager(t *testing.T, clock *rt.FakeClock) *TurnManager {
	t.Helper()
	tm, err := NewTurnManager(DefaultTurnConfig(), clock, NewMetrics())
	if err != nil {
		t.Fatalf("NewTurnManager: %v", err)
	}
	return tm
}

func TestTurnManager_HalfDuplex(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Time{})
	tm := newTurnManager(t, clock)

	tm.Acquire(PartyAgent, false)
	if tm.Holder() != PartyAgent {
		t.Fatal("agent should hold the floor")
	}
	// Sustained overlap: the caller takes it.
	clock.Advance(time.Second)
	tm.Acquire(PartyCaller, false)
	clock.Advance(time.Second)
	turn, decision := tm.Acquire(PartyCaller, false)
	if decision != FloorGranted || turn.Owner != PartyCaller {
		t.Fatalf("caller should win a sustained overlap, got %s / %s", decision, turn.Owner)
	}
	if tm.Holder() != PartyCaller {
		t.Fatal("only one party may hold the floor")
	}
}

func TestTurnManager_BackchannelDoesNotStealFloor(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Time{})
	tm := newTurnManager(t, clock)

	tm.Acquire(PartyAgent, false)

	// A brief "mm-hm" while the agent speaks.
	if d := tm.NoteOverlap(PartyCaller); d != FloorBackchannel {
		t.Fatalf("instant overlap should be a backchannel, got %s", d)
	}
	clock.Advance(300 * time.Millisecond)
	if d := tm.NoteOverlap(PartyCaller); d != FloorBackchannel {
		t.Fatalf("300ms overlap is still a backchannel, got %s", d)
	}
	if tm.Holder() != PartyAgent {
		t.Fatal("a backchannel must not take the floor; the agent would stop " +
			"every time the caller agreed with it")
	}

	// Sustained overlap becomes a barge-in.
	clock.Advance(time.Second)
	if d := tm.NoteOverlap(PartyCaller); d != FloorGranted {
		t.Fatalf("sustained overlap should grant the floor, got %s", d)
	}
}

// TestTurnManager_GreetingIsNonYieldingButQueues asserts INV-CV-2 and the
// design decision that makes it tolerable: the caller's intent to speak is
// deferred, not discarded.
func TestTurnManager_GreetingIsNonYieldingButQueues(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Time{})
	tm := newTurnManager(t, clock)

	tm.Acquire(PartyAgent, true) // the announcement

	clock.Advance(2 * time.Second)
	_, decision := tm.Acquire(PartyCaller, false)
	if decision != FloorQueued {
		t.Fatalf("a barge-in during the announcement must be queued, not %s", decision)
	}
	if tm.Holder() != PartyAgent {
		t.Fatal("the announcement is the caller's lawful basis and must complete")
	}

	tm.Release(PartyAgent, ExpectNothing)
	if tm.Holder() != PartyCaller {
		t.Fatal("the queued request must apply once the announcement ends, " +
			"or the caller has to repeat themselves")
	}
}

// TestTurnManager_EmergencyPreemptsNonYielding asserts that U7 outranks the
// non-yielding greeting.
func TestTurnManager_EmergencyPreemptsNonYielding(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Time{})
	tm := newTurnManager(t, clock)

	tm.Acquire(PartyAgent, true)
	turn := tm.ForceYield(PartySystem, InterruptionEmergency)
	if turn.Owner != PartySystem {
		t.Fatal("an emergency must take the floor even from a non-yielding turn")
	}
}

func TestTurnManager_RecordsExpectation(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Time{})
	tm := newTurnManager(t, clock)

	tm.Acquire(PartyAgent, false)
	tm.Release(PartyAgent, ExpectYesNo)

	if got := tm.LastExpectation(); got != ExpectYesNo {
		t.Fatalf("expectation should survive into history, got %s", got)
	}
}

// ---------------------------------------------------------------------------
// Interruption
// ---------------------------------------------------------------------------

func TestInterruption_PriorityOrder(t *testing.T) {
	t.Parallel()
	if InterruptionEmergency.Priority() <= InterruptionTransfer.Priority() {
		t.Fatal("emergency must outrank transfer")
	}
	if InterruptionUser.Priority() >= InterruptionProvider.Priority() {
		t.Fatal("a provider failure outranks a user barge-in: the user can " +
			"barge in again, a dead stream cannot recover itself")
	}
}

func TestInterruption_ResumePolicies(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Time{})
	e := NewInterruptionEngine(clock, NewMetrics())

	if got := e.Raise(InterruptionUser, PartyCaller, "barge_in", 1).Resume; got != ResumeAbandon {
		t.Fatalf("a user barge-in must abandon: replaying the rest of the "+
			"sentence is the most irritating thing a voice system can do; got %s", got)
	}
	if got := e.Raise(InterruptionEmergency, PartySystem, "emergency", 2).Resume; got != ResumeNever {
		t.Fatalf("an emergency never resumes, got %s", got)
	}

	// A provider failure long after the last checkpoint resumes from it.
	e.Checkpoint(Checkpoint{TurnID: 3, Offset: 40})
	clock.Advance(time.Second)
	if got := e.Raise(InterruptionProvider, PartySystem, "stream_died", 3).Resume; got != ResumeFromCheckpoint {
		t.Fatalf("a provider failure with an established checkpoint should resume, got %s", got)
	}
}

func TestInterruption_ProviderFailureEarlyRestarts(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Time{})
	e := NewInterruptionEngine(clock, NewMetrics())

	e.Checkpoint(Checkpoint{TurnID: 1, Offset: 2})
	clock.Advance(100 * time.Millisecond) // barely anything was said

	if got := e.Raise(InterruptionProvider, PartySystem, "stream_died", 1).Resume; got != ResumeRestart {
		t.Fatalf("resuming from two words in is incoherent; expected restart, got %s", got)
	}
}

func TestInterruption_EmergencyIsIrreversible(t *testing.T) {
	t.Parallel()
	e := NewInterruptionEngine(rt.NewFakeClock(time.Time{}), NewMetrics())

	if e.EmergencyRaised() {
		t.Fatal("no emergency yet")
	}
	e.Raise(InterruptionEmergency, PartySystem, "emergency", 1)
	e.Raise(InterruptionUser, PartyCaller, "barge_in", 2)

	if !e.EmergencyRaised() {
		t.Fatal("an emergency is irreversible; a later interruption must not clear it")
	}
}

// ---------------------------------------------------------------------------
// Intent
// ---------------------------------------------------------------------------

func newIntentEngine(t *testing.T, c IntentClassifier) *IntentEngine {
	t.Helper()
	e, err := NewIntentEngine(DefaultIntentConfig(), c, rt.NewFakeClock(time.Time{}), NewMetrics())
	if err != nil {
		t.Fatalf("NewIntentEngine: %v", err)
	}
	return e
}

func TestIntent_NoiseIsNotLowConfidence(t *testing.T) {
	t.Parallel()
	e := newIntentEngine(t, NewScriptedClassifier())

	_, verdict := e.Resolve(Utterance{Text: "...", ASRConfidence: 0.1}, ExpectNothing)
	if verdict != IntentNoise {
		t.Fatalf("unintelligible audio is noise, not a weak intent; got %s", verdict)
	}
}

func TestIntent_AmbiguityDetectedDespiteHighConfidence(t *testing.T) {
	t.Parallel()
	sc := NewScriptedClassifier().On("book",
		Candidate{Name: "book_appointment", Confidence: 0.90},
		Candidate{Name: "book_table", Confidence: 0.85})
	e := newIntentEngine(t, sc)

	in, verdict := e.Resolve(Utterance{Text: "book something", ASRConfidence: 1}, ExpectNothing)
	if verdict != IntentClarify {
		t.Fatalf("a 0.05 margin is ambiguous however high the top score; got %s", verdict)
	}
	if in.Margin() > 0.06 {
		t.Fatalf("margin computed wrongly: %v", in.Margin())
	}
}

func TestIntent_YesNoShortCircuitsClassification(t *testing.T) {
	t.Parallel()
	sc := NewScriptedClassifier().Fallback(Candidate{Name: "something_else", Confidence: 0.99})
	e := newIntentEngine(t, sc)

	in, verdict := e.Resolve(Utterance{Text: "yes please", ASRConfidence: 1}, ExpectYesNo)
	if verdict != IntentAccept || in.Name != IntentAffirm {
		t.Fatalf("a yes to a yes/no question must be an affirmation, got %s/%s", in.Name, verdict)
	}
	if sc.Calls() != 0 {
		t.Fatal("a confirmation must not go through general classification; " +
			"that is how a yes gets read as a new request")
	}
}

func TestIntent_YesNoRecognisesHindi(t *testing.T) {
	t.Parallel()
	e := newIntentEngine(t, NewScriptedClassifier())

	for text, want := range map[string]IntentName{
		"haan": IntentAffirm, "ji haan": IntentAffirm,
		"nahi": IntentDeny, "nahin bilkul": IntentDeny,
	} {
		in, verdict := e.Resolve(Utterance{Text: text, ASRConfidence: 1}, ExpectYesNo)
		if verdict != IntentAccept || in.Name != want {
			t.Errorf("%q: got %s/%s, want %s", text, in.Name, verdict, want)
		}
	}
}

func TestIntent_UnrecognisedAnswerToYesNoClarifies(t *testing.T) {
	t.Parallel()
	e := newIntentEngine(t, NewScriptedClassifier())

	_, verdict := e.Resolve(Utterance{Text: "what do you mean", ASRConfidence: 1}, ExpectYesNo)
	if verdict != IntentClarify {
		t.Fatalf("a non-answer to a yes/no is ambiguous, not a rejection; got %s", verdict)
	}
}

func TestIntent_MissingRequiredSlotClarifies(t *testing.T) {
	t.Parallel()
	sc := NewScriptedClassifier().
		On("appointment", Candidate{Name: "book_appointment", Confidence: 0.95}).
		WithSlots("book_appointment",
			Slot{Name: "date", Required: true, Filled: false},
			Slot{Name: "time", Required: true, Filled: true})
	e := newIntentEngine(t, sc)

	in, verdict := e.Resolve(Utterance{Text: "an appointment", ASRConfidence: 1}, ExpectNothing)
	if verdict != IntentClarify {
		t.Fatalf("an incomplete intent must clarify, got %s", verdict)
	}
	if got := in.MissingRequired(); len(got) != 1 || got[0] != "date" {
		t.Fatalf("missing slots = %v, want [date]", got)
	}
}

func TestIntent_LifecycleCannotMoveBackwards(t *testing.T) {
	t.Parallel()
	sc := NewScriptedClassifier().Fallback(Candidate{Name: "x", Confidence: 0.99})
	e := newIntentEngine(t, sc)
	e.Resolve(Utterance{Text: "hello", ASRConfidence: 1}, ExpectNothing)

	if err := e.Advance(IntentFulfilled); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if err := e.Advance(IntentActive); !errors.Is(err, ErrInvariant) {
		t.Fatalf("a fulfilled intent cannot become active again, got %v", err)
	}
}

func TestIntent_TieBreakIsDeterministic(t *testing.T) {
	t.Parallel()
	sc := NewScriptedClassifier().On("x",
		Candidate{Name: "zebra", Confidence: 0.9},
		Candidate{Name: "alpha", Confidence: 0.9})

	for i := 0; i < 20; i++ {
		e := newIntentEngine(t, sc)
		in, _ := e.Resolve(Utterance{Text: "x", ASRConfidence: 1}, ExpectNothing)
		if in.Name != "alpha" {
			t.Fatalf("equal confidence must tie-break by name for determinism, got %s", in.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// Context
// ---------------------------------------------------------------------------

func newContextEngine(t *testing.T, clock *rt.FakeClock) *ContextEngine {
	t.Helper()
	c, err := NewContextEngine(DefaultContextConfig(), clock, NewMetrics())
	if err != nil {
		t.Fatalf("NewContextEngine: %v", err)
	}
	return c
}

func TestContext_ScopesAreIsolated(t *testing.T) {
	t.Parallel()
	c := newContextEngine(t, rt.NewFakeClock(time.Time{}))

	_ = c.Set(Entry{Key: "k", Value: "conversation", Scope: ScopeConversation})
	_ = c.Set(Entry{Key: "k", Value: "business", Scope: ScopeBusiness})

	got, _ := c.Get(ScopeBusiness, "k")
	if got.Value != "business" {
		t.Fatal("a conversation write must not overwrite business reference data")
	}
	// Lookup precedence: conversation beats business.
	l, _ := c.Lookup("k")
	if l.Value != "conversation" {
		t.Fatalf("lookup precedence wrong: got %v", l.Value)
	}
}

func TestContext_ExpiryIsLazyAndCorrect(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Time{})
	c := newContextEngine(t, clock)

	_ = c.Set(Entry{Key: "temp", Value: 1, Scope: ScopeTemporary})
	if _, ok := c.Get(ScopeTemporary, "temp"); !ok {
		t.Fatal("entry should be live immediately")
	}

	clock.Advance(31 * time.Second) // TemporaryTTL is 30s
	if _, ok := c.Get(ScopeTemporary, "temp"); ok {
		t.Fatal("temporary entry should have expired")
	}
}

func TestContext_BusinessScopeDoesNotExpire(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Time{})
	c := newContextEngine(t, clock)

	_ = c.Set(Entry{Key: "hours", Value: "9-6", Scope: ScopeBusiness})
	clock.Advance(24 * time.Hour)

	if _, ok := c.Get(ScopeBusiness, "hours"); !ok {
		t.Fatal("business reference data must not expire mid-call; the agent " +
			"would forget the opening hours it just used")
	}
}

func TestContext_SnapshotRestoreExcludesBusiness(t *testing.T) {
	t.Parallel()
	c := newContextEngine(t, rt.NewFakeClock(time.Time{}))

	_ = c.Set(Entry{Key: "a", Value: 1, Scope: ScopeConversation})
	_ = c.Set(Entry{Key: "hours", Value: "9-6", Scope: ScopeBusiness})
	snap := c.TakeSnapshot("before")

	_ = c.Set(Entry{Key: "a", Value: 2, Scope: ScopeConversation})
	_ = c.Set(Entry{Key: "hours", Value: "10-4", Scope: ScopeBusiness})

	if err := c.Restore(snap.ID); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got, _ := c.Get(ScopeConversation, "a"); got.Value != 1 {
		t.Fatalf("conversation scope should roll back, got %v", got.Value)
	}
	if got, _ := c.Get(ScopeBusiness, "hours"); got.Value != "10-4" {
		t.Fatal("business reference data must NOT roll back; restoring stale " +
			"opening hours because a conversation errored is worse than the error")
	}
}

func TestContext_ExportHonoursSensitivityCeiling(t *testing.T) {
	t.Parallel()
	c := newContextEngine(t, rt.NewFakeClock(time.Time{}))

	_ = c.Set(Entry{Key: "pub", Value: 1, Scope: ScopeConversation, Sensitivity: Public})
	_ = c.Set(Entry{Key: "per", Value: 2, Scope: ScopeConversation, Sensitivity: Personal})
	_ = c.Set(Entry{Key: "sen", Value: 3, Scope: ScopeConversation, Sensitivity: SensitiveValue})

	got := c.Export(Internal)
	for _, e := range got {
		if e.Sensitivity > Internal {
			t.Fatalf("export leaked %s data past an Internal ceiling", e.Sensitivity)
		}
	}
	if len(got) != 1 {
		t.Fatalf("expected only the public entry, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// Persona
// ---------------------------------------------------------------------------

func TestPersona_EmergencyAlwaysReachableAndOneWay(t *testing.T) {
	t.Parallel()
	for from := range BuiltinPersonas() {
		if from == PersonaEmergencyAssistant {
			continue
		}
		if !switchAllowed(from, PersonaEmergencyAssistant) {
			t.Errorf("%s cannot reach emergency; U7 makes that unconditional", from)
		}
	}
	for to := range BuiltinPersonas() {
		if to == PersonaEmergencyAssistant {
			continue
		}
		if switchAllowed(PersonaEmergencyAssistant, to) {
			t.Errorf("emergency must never switch to %s", to)
		}
	}
}

func TestPersona_FraudShieldNeverBroadens(t *testing.T) {
	t.Parallel()
	if switchAllowed(PersonaFraudShield, PersonaPersonalAssistant) {
		t.Fatal("a caller who talked their way out of fraud screening is the attack")
	}
	if !switchAllowed(PersonaPersonalAssistant, PersonaFraudShield) {
		t.Fatal("narrowing capability must always be permitted")
	}
}

func TestPersona_EmergencyCanOnlyEscalateOrHandOver(t *testing.T) {
	t.Parallel()
	p := BuiltinPersonas()[PersonaEmergencyAssistant]

	for _, c := range []Capability{CapEscalate, CapHandOverDialer, CapEndCall} {
		if !p.Allows(c) {
			t.Errorf("emergency persona must allow %s", c)
		}
	}
	for _, c := range []Capability{CapAnswerQuestion, CapAskClarification,
		CapTakeMessage, CapTransfer, CapDiscloseIdentity} {
		if p.Allows(c) {
			t.Errorf("emergency persona must forbid %s; its job is to get out of the way", c)
		}
	}
}

func TestPersona_FraudShieldDisclosesNothing(t *testing.T) {
	t.Parallel()
	p := BuiltinPersonas()[PersonaFraudShield]
	for _, c := range []Capability{CapDiscloseIdentity, CapDiscloseAvailability,
		CapTakeMessage, CapAnswerQuestion} {
		if p.Allows(c) {
			t.Errorf("fraud shield must give a hostile caller no surface; %s is a surface", c)
		}
	}
}

func TestPersona_SwitchLocksAfterNarrowing(t *testing.T) {
	t.Parallel()
	pr, err := NewPersonaRuntime(PersonaBusinessReceptionist, rt.NewFakeClock(time.Time{}), NewMetrics())
	if err != nil {
		t.Fatalf("NewPersonaRuntime: %v", err)
	}
	if err := pr.Switch(PersonaFraudShield, "suspicion"); err != nil {
		t.Fatalf("switch to fraud shield: %v", err)
	}
	if !pr.Locked() {
		t.Fatal("narrowing should lock the persona")
	}
	if err := pr.Switch(PersonaBusinessReceptionist, "oops"); !errors.Is(err, ErrPersonaSwitchDenied) {
		t.Fatalf("a locked persona must not broaden, got %v", err)
	}
	// Emergency remains reachable even when locked.
	if err := pr.Switch(PersonaEmergencyAssistant, "emergency"); err != nil {
		t.Fatalf("emergency must remain reachable from a locked persona: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Policy
// ---------------------------------------------------------------------------

func TestPolicy_SafetyDeniesEverythingButExitDuringEmergency(t *testing.T) {
	t.Parallel()
	p := NewPolicyEngine(NewMetrics())
	base := PolicyInput{
		Persona:         BuiltinPersonas()[PersonaBusinessReceptionist],
		EmergencyRaised: true,
	}

	for _, a := range []Action{ActionRespond, ActionClarify, ActionTransfer, ActionAsk} {
		in := base
		in.Action = a
		if v := p.Evaluate(in); v.Allowed() {
			t.Errorf("%s must be denied during an emergency", a)
		}
	}
	for _, a := range []Action{ActionEscalate, ActionEnd} {
		in := base
		in.Action = a
		if v := p.Evaluate(in); !v.Allowed() {
			t.Errorf("%s must remain available during an emergency: %s", a, v.Reason)
		}
	}
}

func TestPolicy_CapabilityIsRequired(t *testing.T) {
	t.Parallel()
	p := NewPolicyEngine(NewMetrics())

	v := p.Evaluate(PolicyInput{
		Action:  ActionTransfer,
		Persona: BuiltinPersonas()[PersonaPersonalAssistant], // forbids transfer
	})
	if v.Allowed() {
		t.Fatal("a persona without the capability must be denied")
	}
	if v.Class != ClassPersona {
		t.Fatalf("denial should be classified persona, got %s", v.Class)
	}
}

func TestPolicy_SafetyOutranksLaterClasses(t *testing.T) {
	t.Parallel()
	p := NewPolicyEngine(NewMetrics())

	// Add a business rule that would allow everything. It cannot reverse a
	// safety denial because evaluation stops at the first deny and safety runs
	// first — there is no mechanism for a later rule to permit.
	p.Add(Rule{Name: "business.allow_all", Class: ClassBusiness, Priority: 0,
		Eval: func(PolicyInput) (Decision, string) { return Allow, "" }})

	v := p.Evaluate(PolicyInput{
		Action: ActionRespond, EmergencyRaised: true,
		Persona: BuiltinPersonas()[PersonaBusinessReceptionist],
	})
	if v.Allowed() {
		t.Fatal("no business rule may reverse a safety denial")
	}
	if v.Class != ClassSafety {
		t.Fatalf("the deciding rule should be safety, got %s", v.Class)
	}
}

func TestPolicy_BoundariesStillPermitExit(t *testing.T) {
	t.Parallel()
	p := NewPolicyEngine(NewMetrics())
	persona := BuiltinPersonas()[PersonaBusinessReceptionist]

	in := PolicyInput{Action: ActionRespond, Persona: persona, TurnCount: persona.MaxTurns}
	if p.Evaluate(in).Allowed() {
		t.Fatal("responding past the turn limit must be denied")
	}
	in.Action = ActionEnd
	if !p.Evaluate(in).Allowed() {
		t.Fatal("a conversation at its limit must still be able to end, or it wedges")
	}
}

func TestPolicy_RulesArePureAndDeterministic(t *testing.T) {
	t.Parallel()
	p := NewPolicyEngine(NewMetrics())
	in := PolicyInput{Action: ActionClarify,
		Persona: BuiltinPersonas()[PersonaBusinessReceptionist], ClarificationsUsed: 99}

	first := p.Evaluate(in)
	for i := 0; i < 50; i++ {
		if got := p.Evaluate(in); got != first {
			t.Fatalf("policy evaluation is not deterministic: %+v vs %+v", got, first)
		}
	}
}

// ---------------------------------------------------------------------------
// Clarification
// ---------------------------------------------------------------------------

func TestClarification_BudgetExhaustionStopsAsking(t *testing.T) {
	t.Parallel()
	ce, err := NewClarificationEngine(DefaultClarificationConfig(), NewMetrics())
	if err != nil {
		t.Fatalf("NewClarificationEngine: %v", err)
	}

	req := Request{Kind: ClarifyLowConfidence}
	if _, ok := ce.Reserve(req, 3); !ok {
		t.Fatal("first attempt should be allowed")
	}
	if _, ok := ce.Reserve(req, 3); !ok {
		t.Fatal("second attempt should be allowed")
	}
	if _, ok := ce.Reserve(req, 3); ok {
		t.Fatal("the same question a third time is the loop this engine exists to prevent")
	}
}

func TestClarification_ResolvedFreesPerSubjectBudget(t *testing.T) {
	t.Parallel()
	ce, _ := NewClarificationEngine(DefaultClarificationConfig(), NewMetrics())
	req := Request{Kind: ClarifyMissingSlot, Slot: "date"}

	ce.Reserve(req, 10)
	ce.Reserve(req, 10)
	if _, ok := ce.Reserve(req, 10); ok {
		t.Fatal("per-subject budget should be spent")
	}

	ce.Resolved(req)
	if _, ok := ce.Reserve(req, 10); !ok {
		t.Fatal("a resolved subject must free its budget; otherwise a caller who " +
			"clarified successfully is refused later for the same ambiguity")
	}
}

func TestClarification_AssessOrdersDiagnosisCorrectly(t *testing.T) {
	t.Parallel()
	ce, _ := NewClarificationEngine(DefaultClarificationConfig(), NewMetrics())

	// Noise outranks everything: the caller may not have spoken to us at all.
	got := ce.Assess(Utterance{Truncated: true}, Intent{}, IntentNoise, true)
	if got.Kind != ClarifyNoise {
		t.Fatalf("noise must be diagnosed first, got %s", got.Kind)
	}
	// Truncation outranks contradiction: it was cut off before it could mean.
	got = ce.Assess(Utterance{Truncated: true}, Intent{}, IntentClarify, true)
	if got.Kind != ClarifyIncomplete {
		t.Fatalf("truncation outranks contradiction, got %s", got.Kind)
	}
	// Contradiction outranks ambiguity.
	got = ce.Assess(Utterance{}, Intent{}, IntentClarify, true)
	if got.Kind != ClarifyContradiction {
		t.Fatalf("contradiction outranks ambiguity, got %s", got.Kind)
	}
}

func TestClarification_KindDeterminesExpectation(t *testing.T) {
	t.Parallel()
	for kind, want := range map[ClarificationKind]Expectation{
		ClarifyAmbiguous:     ExpectDisambiguation,
		ClarifyLowConfidence: ExpectYesNo,
		ClarifyMissingSlot:   ExpectSlotValue,
		ClarifyNoise:         ExpectNothing,
	} {
		if got := kind.Expectation(); got != want {
			t.Errorf("%s: expectation %s, want %s", kind, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Planner
// ---------------------------------------------------------------------------

// TestPlanner_DecisionTable enumerates the planner's decision space. It is a
// table because the planner is a pure function, which is the property that
// makes exhaustive assertion possible at all.
func TestPlanner_DecisionTable(t *testing.T) {
	t.Parallel()
	p := NewPlanner(NewMetrics())
	reception := BuiltinPersonas()[PersonaBusinessReceptionist]
	emergency := BuiltinPersonas()[PersonaEmergencyAssistant]

	cases := []struct {
		name string
		in   PlanInput
		want Action
	}{
		{"emergency outranks everything",
			PlanInput{Persona: reception, EmergencyRaised: true, Verdict: IntentAccept},
			ActionEscalate},
		{"emergency persona escalates on any uncertainty",
			PlanInput{Persona: emergency, Verdict: IntentClarify},
			ActionEscalate},
		{"accepted intent responds",
			PlanInput{Persona: reception, Verdict: IntentAccept,
				Intent: Intent{Name: "book", Confidence: 0.9}},
			ActionRespond},
		{"ambiguity clarifies",
			PlanInput{Persona: reception, Verdict: IntentClarify,
				Clarification: Request{Kind: ClarifyAmbiguous}, ClarificationAllowed: true},
			ActionClarify},
		{"missing slot asks",
			PlanInput{Persona: reception, Verdict: IntentClarify,
				Clarification:        Request{Kind: ClarifyMissingSlot, Slot: "date"},
				ClarificationAllowed: true},
			ActionAsk},
		{"low confidence confirms",
			PlanInput{Persona: reception, Verdict: IntentClarify,
				Clarification: Request{Kind: ClarifyLowConfidence}, ClarificationAllowed: true},
			ActionConfirm},
		{"exhausted clarification escalates rather than repeating",
			PlanInput{Persona: reception, Verdict: IntentClarify,
				Clarification: Request{Kind: ClarifyLowConfidence}, ClarificationAllowed: false},
			ActionEscalate},
		{"turn limit exits",
			PlanInput{Persona: reception, Verdict: IntentAccept, TurnCount: reception.MaxTurns},
			ActionEscalate},
		{"interruption storm exits",
			PlanInput{Persona: reception, Verdict: IntentAccept, InterruptionCount: 6},
			ActionEscalate},
		{"single noise is ignored",
			PlanInput{Persona: reception, Verdict: IntentNoise,
				Clarification: Request{Kind: ClarifyNone}, ClarificationAllowed: true},
			ActionIgnore},
		{"repeated noise past budget escalates",
			PlanInput{Persona: reception, Verdict: IntentNoise,
				Clarification: Request{Kind: ClarifyNoise}, ClarificationAllowed: false},
			ActionEscalate},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := p.Plan(tc.in)
			if got.Action != tc.want {
				t.Fatalf("action = %s (%s), want %s", got.Action, got.Reason, tc.want)
			}
		})
	}
}

func TestPlanner_IsDeterministic(t *testing.T) {
	t.Parallel()
	p := NewPlanner(NewMetrics())
	in := PlanInput{
		Persona: BuiltinPersonas()[PersonaBusinessReceptionist],
		Verdict: IntentClarify,
		Clarification: Request{Kind: ClarifyAmbiguous,
			Candidates: []IntentName{"a", "b"}},
		ClarificationAllowed: true,
	}
	first := p.Plan(in)
	for i := 0; i < 100; i++ {
		got := p.Plan(in)
		if got.Action != first.Action || got.Reason != first.Reason ||
			got.Expectation != first.Expectation {
			t.Fatalf("planner is not deterministic: %+v vs %+v", got, first)
		}
	}
}

// ---------------------------------------------------------------------------
// Latency
// ---------------------------------------------------------------------------

// TestLatency_PolicyIsNeverSkippable is the engine's expression of invariant
// I11: under pressure the runtime may shed or downgrade, but it may not skip
// the safety layer — and policy evaluation is where safety rules live.
func TestLatency_PolicyIsNeverSkippable(t *testing.T) {
	t.Parallel()
	if StagePolicy.Skippable() {
		t.Fatal("I11: policy evaluation carries the safety rules and must never be skipped")
	}
	if StageTransition.Skippable() {
		t.Fatal("skipping the transition would leave state disagreeing with behaviour")
	}
	if !StageIntent.Skippable() {
		t.Fatal("intent classification is optional under pressure — the fallback exists")
	}
}

func TestLatency_DegradesSkippableStagesOnly(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Time{})
	lc, err := NewLatencyController(DefaultLatencyConfig(), clock, NewMetrics())
	if err != nil {
		t.Fatalf("NewLatencyController: %v", err)
	}
	lc.Begin()

	// Burn past the degrade threshold (75% of 150ms).
	clock.Advance(120 * time.Millisecond)

	if _, run := lc.Enter(StageIntent); run {
		t.Fatal("a skippable stage should be dropped past the threshold")
	}
	if _, run := lc.Enter(StagePolicy); !run {
		t.Fatal("policy must run whatever the budget says")
	}
	if !lc.Degraded() {
		t.Fatal("degradation should be recorded so it is visible in metrics")
	}
}

func TestLatency_BudgetsFitWithinTotal(t *testing.T) {
	t.Parallel()
	cfg := DefaultLatencyConfig()
	var sum time.Duration
	for _, d := range cfg.Budgets {
		sum += d
	}
	if sum > cfg.Total {
		t.Fatalf("stage budgets total %v, exceeding the cycle budget of %v", sum, cfg.Total)
	}
}

func TestLatency_PersonaScalesBudget(t *testing.T) {
	t.Parallel()
	base := DefaultLatencyConfig()
	fast := base.scaleBudgets(0.5)

	if fast.Total != base.Total/2 {
		t.Fatalf("scaled total = %v, want %v", fast.Total, base.Total/2)
	}
	if fast.Budgets[StagePolicy] != base.Budgets[StagePolicy]/2 {
		t.Fatal("stage budgets should scale with the profile")
	}
}
