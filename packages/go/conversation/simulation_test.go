package conversation

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// newHarness builds a test harness, failing the test on error.
func newHarness(t *testing.T, opts ...HarnessOption) *Harness {
	t.Helper()
	h, err := NewHarness(opts...)
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	return h
}

// sim builds a started simulator over a fresh conversation.
func sim(t *testing.T, h *Harness, id ConversationID, persona PersonaID) *Simulator {
	t.Helper()
	c, err := h.Engine.Begin(id, persona)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	s := NewSimulator(c, h.Clock)
	s.Start()
	if len(s.Errors) > 0 {
		t.Fatalf("start errors: %v", s.Errors)
	}
	return s
}

// ---------------------------------------------------------------------------
// Conversation simulation
// ---------------------------------------------------------------------------

func TestSim_HappyPath(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Classifier.On("hours", Candidate{Name: "ask_hours", Confidence: 0.95})

	s := sim(t, h, "happy", PersonaBusinessReceptionist)
	s.Exchange("what are your opening hours")

	if len(s.Errors) > 0 {
		t.Fatalf("unexpected errors: %v\ntrace: %s", s.Errors, s.TraceString())
	}
	if got := s.Plans[2].Action; got != ActionRespond {
		t.Fatalf("a confident, complete intent should be answered, got %s\ntrace: %s",
			got, s.TraceString())
	}
	if s.conv.State() != StateListening {
		t.Fatalf("after answering, the floor returns to the caller; state = %s", s.conv.State())
	}
}

// TestSim_GreetingAlwaysPrecedesConversation asserts the structural guarantee
// end to end: no dialogue can occur before the announcement has played.
func TestSim_GreetingAlwaysPrecedesConversation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c, _ := h.Begin("greeting")

	// An utterance before Start cannot be processed as a turn, because Idle is
	// not an awaiting state.
	plan, _ := c.Handle(Event{Kind: EventUtterance, Party: PartyCaller,
		Utterance: Utterance{Text: "hello", ASRConfidence: 1}})
	if plan.Action == ActionRespond {
		t.Fatal("the engine answered before the announcement played")
	}

	states := NewSimulator(c, h.Clock).Start().States()
	if len(states) < 2 || states[1] != StateGreeting {
		t.Fatalf("the first transition must be into Greeting, got %v", states)
	}
}

func TestSim_ClarificationLadderThenEscalation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// Two candidates a hair apart: permanently ambiguous, so the caller can
	// never resolve it. This is the loop the budget exists to break.
	h.Classifier.On("thing",
		Candidate{Name: "option_a", Confidence: 0.60},
		Candidate{Name: "option_b", Confidence: 0.58})

	s := sim(t, h, "clarify", PersonaBusinessReceptionist)
	for i := 0; i < 5; i++ {
		s.Exchange("the thing")
		if s.conv.State().IsTerminal() {
			break
		}
	}

	if !s.conv.State().IsTerminal() {
		t.Fatalf("a permanently ambiguous caller must not loop forever; state = %s\ntrace: %s",
			s.conv.State(), s.TraceString())
	}
	if s.conv.Outcome() != OutcomeEscalated {
		t.Fatalf("exhausted clarification should escalate, got %s", s.conv.Outcome())
	}
	if s.CountAction(ActionClarify)+s.CountAction(ActionConfirm) > 4 {
		t.Fatalf("asked too many times: %d clarify, %d confirm",
			s.CountAction(ActionClarify), s.CountAction(ActionConfirm))
	}
}

func TestSim_ConfirmationYesIsInterpretedAsAnswer(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Classifier.
		On("book", Candidate{Name: "book", Confidence: 0.60}). // low → confirm
		Fallback(Candidate{Name: "other", Confidence: 0.99})

	s := sim(t, h, "confirm", PersonaBusinessReceptionist)
	s.Exchange("book it")

	if s.conv.State() != StateConfirmation {
		t.Fatalf("a low-confidence intent should establish a yes/no expectation; state = %s\ntrace: %s",
			s.conv.State(), s.TraceString())
	}

	before := h.Classifier.Calls()
	s.Say("yes")
	if h.Classifier.Calls() != before {
		t.Fatal("a yes answering a confirmation must not be re-classified as a new request")
	}
}

func TestSim_BackchannelDoesNotDerailTheAgent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Classifier.Fallback(Candidate{Name: "generic", Confidence: 0.99})

	s := sim(t, h, "backchannel", PersonaBusinessReceptionist)
	s.Say("tell me about your services") // agent now speaking

	if s.conv.State() != StateSpeaking {
		t.Fatalf("expected Speaking, got %s\ntrace: %s", s.conv.State(), s.TraceString())
	}

	s.Do(Event{Kind: EventOverlap, Party: PartyCaller}) // "mm-hm"
	if s.LastPlan().Action != ActionIgnore {
		t.Fatalf("a backchannel should be ignored, got %s", s.LastPlan().Action)
	}
	if s.conv.State() != StateSpeaking {
		t.Fatalf("the agent must keep the floor through a backchannel; state = %s", s.conv.State())
	}
}

func TestSim_BargeInYieldsTheFloor(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Classifier.Fallback(Candidate{Name: "generic", Confidence: 0.99})

	s := sim(t, h, "bargein", PersonaBusinessReceptionist)
	s.Say("tell me about your services")
	s.Interrupt(InterruptionUser, "barge_in")

	if s.conv.State() != StateListening {
		t.Fatalf("a barge-in hands the floor to the caller; state = %s\ntrace: %s",
			s.conv.State(), s.TraceString())
	}
	if s.conv.Interruptions().Count(InterruptionUser) != 1 {
		t.Fatal("the interruption should be recorded")
	}
}

// TestSim_EmergencyEndsTheConversationImmediately is the U7 end-to-end test.
func TestSim_EmergencyEndsTheConversationImmediately(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Classifier.Fallback(Candidate{Name: "generic", Confidence: 0.99})

	s := sim(t, h, "emergency", PersonaBusinessReceptionist)
	s.Say("there has been an accident")
	s.Interrupt(InterruptionEmergency, "emergency_intent")

	if s.conv.State() != StateEscalated {
		t.Fatalf("an emergency must escalate immediately; state = %s\ntrace: %s",
			s.conv.State(), s.TraceString())
	}
	if s.conv.Persona().ID != PersonaEmergencyAssistant {
		t.Fatalf("an emergency must switch persona, got %s", s.conv.Persona().ID)
	}
	if s.conv.Outcome() != OutcomeEscalated {
		t.Fatalf("outcome = %s, want escalated", s.conv.Outcome())
	}
	// And nothing further may happen.
	if _, err := s.conv.Handle(Event{Kind: EventUtterance}); !errors.Is(err, ErrTerminal) {
		t.Fatalf("an escalated conversation must accept nothing further, got %v", err)
	}
}

func TestSim_NoiseIsIgnoredThenEscalated(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := sim(t, h, "noise", PersonaBusinessReceptionist)

	for i := 0; i < 6 && !s.conv.State().IsTerminal(); i++ {
		s.SayWith("...", 0.05, false)
		if s.conv.State() == StateSpeaking {
			s.Reply()
		}
	}

	if !s.conv.State().IsTerminal() {
		t.Fatalf("an unusable line must not be endured forever; state = %s\ntrace: %s",
			s.conv.State(), s.TraceString())
	}
}

func TestSim_FraudShieldRefusesToDisclose(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Classifier.Fallback(Candidate{Name: "ask_identity", Confidence: 0.99})

	s := sim(t, h, "fraud", PersonaFraudShield)
	s.Say("who am I speaking to")

	// Fraud Shield forbids CapAnswerQuestion, so responding is denied and the
	// planner must find something else.
	if s.LastPlan().Action == ActionRespond {
		t.Fatalf("fraud shield must not answer a hostile caller; plan = %+v\ntrace: %s",
			s.LastPlan(), s.TraceString())
	}
}

func TestSim_EmergencyPersonaNeverClarifies(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Classifier.On("help",
		Candidate{Name: "a", Confidence: 0.55},
		Candidate{Name: "b", Confidence: 0.54})

	s := sim(t, h, "emergency-persona", PersonaEmergencyAssistant)
	s.Say("help me something happened")

	if s.LastPlan().Action != ActionEscalate {
		t.Fatalf("the emergency persona escalates rather than clarifying; got %s\ntrace: %s",
			s.LastPlan().Action, s.TraceString())
	}
}

func TestSim_TurnLimitTerminates(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	h := newHarness(t, WithHarnessConfig(cfg))
	h.Classifier.Fallback(Candidate{Name: "generic", Confidence: 0.99})

	s := sim(t, h, "turnlimit", PersonaPersonalAssistant)
	persona := BuiltinPersonas()[PersonaPersonalAssistant]

	for i := 0; i < persona.MaxTurns+10 && !s.conv.State().IsTerminal(); i++ {
		s.Exchange(fmt.Sprintf("message %d", i))
	}

	if !s.conv.State().IsTerminal() {
		t.Fatalf("a conversation must not exceed its turn boundary; %d turns, state %s",
			s.conv.Turns().Count(), s.conv.State())
	}
}

// TestSim_DeterministicReplay is the property the whole engine is designed
// around: identical inputs produce an identical trace.
func TestSim_DeterministicReplay(t *testing.T) {
	t.Parallel()

	run := func() ([]State, []Action) {
		h := newHarness(t)
		h.Classifier.
			On("hours", Candidate{Name: "ask_hours", Confidence: 0.95}).
			On("book", Candidate{Name: "book_a", Confidence: 0.60},
				Candidate{Name: "book_b", Confidence: 0.58}).
			Fallback(Candidate{Name: "generic", Confidence: 0.80})

		s := sim(t, h, "replay", PersonaBusinessReceptionist)
		s.Exchange("what are your hours").
			Exchange("book something").
			Say("yes").Reply().
			Do(Event{Kind: EventHangup, Reason: "caller_hangup"})
		return s.States(), s.Actions()
	}

	states, actions := run()
	for i := 0; i < 15; i++ {
		gotStates, gotActions := run()
		if len(gotStates) != len(states) {
			t.Fatalf("run %d produced %d states, first run produced %d", i, len(gotStates), len(states))
		}
		for j := range states {
			if gotStates[j] != states[j] {
				t.Fatalf("run %d diverged at state %d: %s vs %s", i, j, gotStates[j], states[j])
			}
		}
		for j := range actions {
			if gotActions[j] != actions[j] {
				t.Fatalf("run %d diverged at action %d: %s vs %s", i, j, gotActions[j], actions[j])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Failure injection
// ---------------------------------------------------------------------------

func TestFailure_ClassifierErrorFallsBack(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Classifier.FailWith(errors.New("model unavailable"))

	s := sim(t, h, "classifier-fail", PersonaBusinessReceptionist)
	s.Say("anything at all")

	if len(s.Errors) > 0 {
		t.Fatalf("a classifier failure must not fail the conversation: %v", s.Errors)
	}
	if s.LastPlan().Action != ActionRespond || s.LastPlan().Reason != "fallback" {
		t.Fatalf("expected a fallback response, got %s/%s",
			s.LastPlan().Action, s.LastPlan().Reason)
	}
}

func TestFailure_ProviderInterruptionEntersRecoverableError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Classifier.Fallback(Candidate{Name: "generic", Confidence: 0.99})

	s := sim(t, h, "provider-fail", PersonaBusinessReceptionist)
	s.Say("hello there")
	s.Interrupt(InterruptionProvider, "stream_died")

	if s.conv.State() != StateError {
		t.Fatalf("a provider failure is recoverable, not terminal; state = %s\ntrace: %s",
			s.conv.State(), s.TraceString())
	}
	if s.conv.State().IsTerminal() {
		t.Fatal("Error must not be terminal — that is the difference between an error and a failure")
	}
}

func TestFailure_RecoveryWithoutSnapshotEscalates(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c, _ := h.Begin("recover-nosnap")
	s := NewSimulator(c, h.Clock).Start()

	s.Do(Event{Kind: EventFault, Reason: "internal"})
	if c.State() != StateError {
		t.Fatalf("expected Error, got %s", c.State())
	}
	if err := c.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if c.State() != StateEscalated {
		t.Fatal("recovery with no snapshot must escalate rather than continue on " +
			"context that may be half-written")
	}
}

func TestFailure_RecoveryWithSnapshotResumes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c, _ := h.Begin("recover-snap")
	s := NewSimulator(c, h.Clock).Start()

	_ = c.Context().Set(Entry{Key: "caller_name_known", Value: true,
		Scope: ScopeConversation, Sensitivity: Internal})
	c.Context().TakeSnapshot("before_fault")

	s.Do(Event{Kind: EventFault, Reason: "internal"})
	if err := c.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if c.State() != StateListening {
		t.Fatalf("recovery with a snapshot should resume listening, got %s", c.State())
	}
	if _, ok := c.Context().Get(ScopeConversation, "caller_name_known"); !ok {
		t.Fatal("restored context should be present")
	}
}

func TestFailure_HangupAtAnyPointIsClean(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Classifier.Fallback(Candidate{Name: "generic", Confidence: 0.99})

	// Hang up from every non-terminal state we can reach.
	for _, script := range []func(*Simulator){
		func(s *Simulator) {},
		func(s *Simulator) { s.Say("hello") },
		func(s *Simulator) { s.Say("hello").Reply() },
		func(s *Simulator) { s.Say("hello").Reply().Say("again") },
	} {
		h2 := newHarness(t)
		h2.Classifier.Fallback(Candidate{Name: "generic", Confidence: 0.99})
		s := sim(t, h2, "hangup", PersonaBusinessReceptionist)
		script(s)

		if s.conv.State().IsTerminal() {
			continue
		}
		s.Do(Event{Kind: EventHangup, Reason: "caller_hangup"})
		if s.conv.State() != StateEnded {
			t.Errorf("hangup should end cleanly from %s, got %s", s.conv.State(), s.conv.State())
		}
		if s.conv.Outcome() != OutcomeCompleted {
			t.Errorf("outcome = %s, want completed", s.conv.Outcome())
		}
	}
}

func TestFailure_ContradictionTriggersConfirmation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Classifier.Fallback(Candidate{Name: "generic", Confidence: 0.99})

	s := sim(t, h, "contradiction", PersonaBusinessReceptionist)
	s.Do(Event{
		Kind: EventUtterance, Party: PartyCaller, Contradicts: true,
		Utterance: Utterance{Text: "actually make it tuesday", ASRConfidence: 1},
	})

	if s.LastPlan().Action != ActionConfirm {
		t.Fatalf("a contradiction should be confirmed, not assumed; got %s\ntrace: %s",
			s.LastPlan().Action, s.TraceString())
	}
}

// ---------------------------------------------------------------------------
// Stress and concurrency
// ---------------------------------------------------------------------------

func TestStress_ManyConcurrentConversations(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Classifier.
		On("hours", Candidate{Name: "ask_hours", Confidence: 0.95}).
		Fallback(Candidate{Name: "generic", Confidence: 0.85})

	const n = 200
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := h.Engine.Begin(ConversationID(fmt.Sprintf("c%d", i)), PersonaBusinessReceptionist)
			if err != nil {
				errs <- fmt.Errorf("begin %d: %w", i, err)
				return
			}
			s := NewSimulator(c, h.Clock).Start()
			s.Exchange("what are your hours").Exchange("thanks")
			s.Do(Event{Kind: EventHangup, Reason: "done"})

			if len(s.Errors) > 0 {
				errs <- fmt.Errorf("conversation %d: %v", i, s.Errors)
				return
			}
			if c.Outcome() == "" {
				errs <- fmt.Errorf("conversation %d never completed", i)
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
	if got := h.Metrics.Started.Total(); got != n {
		t.Errorf("started = %d, want %d", got, n)
	}
	if got := h.Metrics.Completed.Total(); got != n {
		t.Errorf("completed = %d, want %d", got, n)
	}
	if got := h.Metrics.Active.Value(); got != 0 {
		t.Errorf("active gauge leaked: %v", got)
	}
}

func TestStress_ConcurrentEventsOnOneConversation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Classifier.Fallback(Candidate{Name: "generic", Confidence: 0.9})

	c, _ := h.Begin("concurrent")
	NewSimulator(c, h.Clock).Start()

	// Hammer one conversation from many goroutines. Most events will be
	// refused as invalid transitions, which is correct and is the point: the
	// engine must remain internally consistent under contention rather than
	// racing into an impossible state.
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			kinds := []EventKind{EventUtterance, EventSpeechComplete,
				EventOverlap, EventSilence}
			_, _ = c.Handle(Event{
				Kind: kinds[i%len(kinds)], Party: PartyCaller,
				Utterance: Utterance{Text: "hello", ASRConfidence: 1},
			})
		}(i)
	}
	wg.Wait()

	// The invariant is not "every event succeeded" — it is that the machine is
	// in a legal state and its trace is coherent.
	state := c.State()
	found := false
	for _, s := range AllStates() {
		if s == state {
			found = true
		}
	}
	if !found {
		t.Fatalf("conversation reached an undeclared state: %v", state)
	}
	for i, tr := range c.Trace() {
		legal := false
		for _, to := range transitionTable()[tr.From] {
			if to == tr.To {
				legal = true
				break
			}
		}
		if !legal {
			t.Fatalf("trace entry %d records an undeclared transition %s -> %s",
				i, tr.From, tr.To)
		}
	}
}

func TestStress_LongConversationBounded(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Classifier.Fallback(Candidate{Name: "generic", Confidence: 0.95})

	s := sim(t, h, "long", PersonaBusinessReceptionist)
	for i := 0; i < 500 && !s.conv.State().IsTerminal(); i++ {
		s.Exchange("keep going")
		h.Clock.Advance(time.Second)
	}

	if !s.conv.State().IsTerminal() {
		t.Fatal("an unbounded conversation is a call that never ends")
	}
	if s.conv.Turns().Count() > 200 {
		t.Fatalf("turn count %d far exceeds the persona boundary", s.conv.Turns().Count())
	}
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

func TestMetrics_ConversationAccounting(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Classifier.Fallback(Candidate{Name: "generic", Confidence: 0.95})

	s := sim(t, h, "metrics", PersonaBusinessReceptionist)
	s.Exchange("hello").Exchange("goodbye")
	s.Do(Event{Kind: EventHangup, Reason: "done"})

	if got := h.Metrics.Started.Total(); got != 1 {
		t.Errorf("started = %d, want 1", got)
	}
	if got := h.Metrics.Completed.Value(string(OutcomeCompleted)); got != 1 {
		t.Errorf("completed[completed] = %d, want 1", got)
	}
	if h.Metrics.Transitions.Total() == 0 {
		t.Error("transitions should be counted")
	}
	if h.Metrics.Duration.Count() != 1 {
		t.Error("conversation duration should be observed exactly once")
	}
}

func TestMetrics_SnapshotIsStablyOrdered(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.Transitions.Inc("a", "b", "c")
	m.Active.Set(3)
	m.TurnDuration.Observe(1.5, "caller")

	first := m.Snapshot()
	second := m.Snapshot()
	if len(first) != len(second) {
		t.Fatalf("snapshot length unstable: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Name != second[i].Name {
			t.Fatalf("snapshot order unstable at %d", i)
		}
	}
}
