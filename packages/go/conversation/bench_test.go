package conversation

import (
	"testing"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Benchmarks for the decision cycle. Every number in
// docs/conversation/PERFORMANCE.md comes from these, on the machine named
// there — nothing is estimated.
//
// What is measured is the engine's OWN cost with a scripted classifier that
// returns instantly. A real classifier is a model call at 40–200 ms, three to
// four orders of magnitude larger than anything below, so benchmarking with one
// would measure the model and say nothing about this code.

func benchHarness(b *testing.B) *Harness {
	b.Helper()
	h, err := NewHarness()
	if err != nil {
		b.Fatal(err)
	}
	h.Classifier.
		On("hours", Candidate{Name: "ask_hours", Confidence: 0.95}).
		Fallback(Candidate{Name: "generic", Confidence: 0.90})
	return h
}

// BenchmarkDecisionCycle is the headline number: one full caller utterance
// through intent, context, clarification, planning, policy and transition.
func BenchmarkDecisionCycle(b *testing.B) {
	h := benchHarness(b)
	e := Event{
		Kind: EventUtterance, Party: PartyCaller,
		Utterance: Utterance{Text: "what are your hours", ASRConfidence: 1, DurationMS: 1200},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		c, _ := h.Engine.Begin(ConversationID("bench"), PersonaBusinessReceptionist)
		_, _ = c.Handle(Event{Kind: EventStart})
		_, _ = c.Handle(Event{Kind: EventGreetingComplete})
		b.StartTimer()

		_, _ = c.Handle(e)

		b.StopTimer()
		_ = c.End("bench")
		b.StartTimer()
	}
}

// BenchmarkFullConversation measures a complete four-turn dialogue including
// setup and teardown — the per-call cost, not the per-turn cost.
func BenchmarkFullConversation(b *testing.B) {
	h := benchHarness(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c, _ := h.Engine.Begin(ConversationID("bench"), PersonaBusinessReceptionist)
		s := NewSimulator(c, h.Clock)
		s.Start().
			Exchange("what are your hours").
			Exchange("thank you").
			Do(Event{Kind: EventHangup, Reason: "done"})
	}
}

func BenchmarkStateTransition(b *testing.B) {
	h := benchHarness(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		c, _ := h.Engine.Begin(ConversationID("bench"), PersonaBusinessReceptionist)
		_, _ = c.Handle(Event{Kind: EventStart})
		b.StartTimer()

		_ = c.transition(StateListening, TriggerGreetingComplete, "")

		b.StopTimer()
		_ = c.End("bench")
		b.StartTimer()
	}
}

func BenchmarkPolicyEvaluate(b *testing.B) {
	p := NewPolicyEngine(NewMetrics())
	in := PolicyInput{
		Action: ActionRespond, State: StateThinking,
		Persona: BuiltinPersonas()[PersonaBusinessReceptionist],
		Intent:  Intent{Name: "ask_hours", Confidence: 0.95}, HasIntent: true,
		TurnCount: 4, Elapsed: 20 * time.Second,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.Evaluate(in)
	}
}

// BenchmarkPolicyDenialPath measures the refusal branch, which must not be
// dramatically more expensive than the allow branch — under a persona that
// forbids much, denial is the common case.
func BenchmarkPolicyDenialPath(b *testing.B) {
	p := NewPolicyEngine(NewMetrics())
	in := PolicyInput{
		Action:  ActionTransfer,
		Persona: BuiltinPersonas()[PersonaPersonalAssistant], // forbids transfer
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.Evaluate(in)
	}
}

func BenchmarkPlanner(b *testing.B) {
	p := NewPlanner(NewMetrics())
	in := PlanInput{
		Persona: BuiltinPersonas()[PersonaBusinessReceptionist],
		Verdict: IntentAccept,
		Intent:  Intent{Name: "ask_hours", Confidence: 0.95},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.Plan(in)
	}
}

func BenchmarkIntentResolve(b *testing.B) {
	sc := NewScriptedClassifier().
		On("hours", Candidate{Name: "ask_hours", Confidence: 0.95})
	e, err := NewIntentEngine(DefaultIntentConfig(), sc, rt.NewFakeClock(time.Time{}), NewMetrics())
	if err != nil {
		b.Fatal(err)
	}
	u := Utterance{Text: "what are your hours", ASRConfidence: 1}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = e.Resolve(u, ExpectNothing)
	}
}

// BenchmarkIntentYesNo measures the deterministic confirmation path, which
// deliberately bypasses classification entirely.
func BenchmarkIntentYesNo(b *testing.B) {
	e, _ := NewIntentEngine(DefaultIntentConfig(), NewScriptedClassifier(),
		rt.NewFakeClock(time.Time{}), NewMetrics())
	u := Utterance{Text: "yes that is correct", ASRConfidence: 1}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = e.Resolve(u, ExpectYesNo)
	}
}

func BenchmarkTurnAcquireRelease(b *testing.B) {
	tm, err := NewTurnManager(DefaultTurnConfig(), rt.NewFakeClock(time.Time{}), NewMetrics())
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tm.Acquire(PartyAgent, false)
		tm.Release(PartyAgent, ExpectNothing)
	}
}

// BenchmarkTurnOverlapArbitration measures the barge-in classification path,
// which runs on every frame of overlapping audio.
func BenchmarkTurnOverlapArbitration(b *testing.B) {
	clock := rt.NewFakeClock(time.Time{})
	tm, _ := NewTurnManager(DefaultTurnConfig(), clock, NewMetrics())
	tm.Acquire(PartyAgent, false)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tm.NoteOverlap(PartyCaller)
	}
}

func BenchmarkContextSetGet(b *testing.B) {
	c, err := NewContextEngine(DefaultContextConfig(), rt.NewFakeClock(time.Time{}), NewMetrics())
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Set(Entry{Key: "k", Value: i, Scope: ScopeConversation, Sensitivity: Internal})
		_, _ = c.Get(ScopeConversation, "k")
	}
}

// BenchmarkContextLookup measures the precedence walk across five scopes, which
// is the read path the planner uses.
func BenchmarkContextLookup(b *testing.B) {
	c, _ := NewContextEngine(DefaultContextConfig(), rt.NewFakeClock(time.Time{}), NewMetrics())
	_ = c.Set(Entry{Key: "hours", Value: "9-6", Scope: ScopeBusiness})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = c.Lookup("hours")
	}
}

func BenchmarkContextSnapshot(b *testing.B) {
	c, _ := NewContextEngine(DefaultContextConfig(), rt.NewFakeClock(time.Time{}), NewMetrics())
	for i := 0; i < 32; i++ {
		_ = c.Set(Entry{Key: string(rune('a' + i%26)), Value: i, Scope: ScopeConversation})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.TakeSnapshot("bench")
	}
}

func BenchmarkClarificationAssess(b *testing.B) {
	ce, err := NewClarificationEngine(DefaultClarificationConfig(), NewMetrics())
	if err != nil {
		b.Fatal(err)
	}
	u := Utterance{Text: "the thing", ASRConfidence: 1}
	in := Intent{
		Name: "a", Confidence: 0.6,
		Alternatives: []Candidate{{Name: "a", Confidence: 0.6}, {Name: "b", Confidence: 0.58}},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ce.Assess(u, in, IntentClarify, false)
	}
}

func BenchmarkLatencyController(b *testing.B) {
	lc, err := NewLatencyController(DefaultLatencyConfig(), rt.SystemClock{}, NewMetrics())
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lc.Begin()
		for _, s := range []Stage{StageIntent, StageContext, StagePolicy, StagePlanning} {
			if end, run := lc.Enter(s); run {
				end()
			}
		}
		lc.End()
	}
}

func BenchmarkPersonaSwitch(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pr, _ := NewPersonaRuntime(PersonaBusinessReceptionist,
			rt.NewFakeClock(time.Time{}), NewMetrics())
		_ = pr.Switch(PersonaEmergencyAssistant, "bench")
	}
}

func BenchmarkMetricsCounter(b *testing.B) {
	m := NewMetrics()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			m.Transitions.Inc("listening", "thinking", "utterance")
		}
	})
}

// BenchmarkConcurrentConversations measures throughput with many independent
// conversations, which is the shape production has: concurrency is the
// platform's capacity unit (ADR-0002 §13), not requests per second.
func BenchmarkConcurrentConversations(b *testing.B) {
	h := benchHarness(b)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c, err := h.Engine.Begin(ConversationID(string(rune(i%26+'a'))+"-p"), PersonaBusinessReceptionist)
			if err != nil {
				continue
			}
			s := NewSimulator(c, h.Clock)
			s.Start().Exchange("what are your hours").
				Do(Event{Kind: EventHangup, Reason: "done"})
			i++
		}
	})
}
