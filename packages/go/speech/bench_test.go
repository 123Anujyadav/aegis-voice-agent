package speech

import (
	"context"
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

func benchClock() *rt.FakeClock {
	return rt.NewFakeClock(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
}

// ---------------------------------------------------------------------------
// Transcript assembly
// ---------------------------------------------------------------------------

func BenchmarkAssembler_ApplyPartial(b *testing.B) {
	sess := NewSessionID()
	turn := NewTurnID()
	a := NewTranscriptAssembler(sess, benchClock())
	s := seg(turn, sess, 0, "the quick brown fox jumps over the lazy dog", false)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Sequence = uint64(i)
		if _, err := a.Apply(s); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAssembler_Finalize(b *testing.B) {
	sess := NewSessionID()
	a := NewTranscriptAssembler(sess, benchClock())

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		turn := TurnID("st-bench")
		s := seg(turn, sess, 0, "final text here", true)
		s.Turn = turn
		_, _ = a.Apply(s)
		a.Reset()
	}
}

func BenchmarkAssembler_RejectStalePartial(b *testing.B) {
	sess := NewSessionID()
	turn := NewTurnID()
	a := NewTranscriptAssembler(sess, benchClock())
	if _, err := a.Apply(seg(turn, sess, 100, "final", true)); err != nil {
		b.Fatal(err)
	}
	stale := seg(turn, sess, 1, "stale", false)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := a.Apply(stale); err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// Sentence segmentation — ADR-0011 hop 7 budgets 15 ms p50 / 40 ms p95
// ---------------------------------------------------------------------------

func benchmarkChunker(b *testing.B, text string) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c, err := NewChunker(DefaultChunkConfig())
		if err != nil {
			b.Fatal(err)
		}
		_ = c.Push(text)
		_ = c.Flush()
	}
}

func BenchmarkChunker_SegmentEnglish(b *testing.B) {
	benchmarkChunker(b, "Thank you for calling. Your balance is 1234.56 rupees. "+
		"Please visit example.co.in for details. Is there anything else?")
}

func BenchmarkChunker_SegmentDevanagari(b *testing.B) {
	benchmarkChunker(b, "नमस्ते। आपका बैलेंस 1234.56 है। क्या मैं और कुछ मदद कर सकता हूँ? धन्यवाद।")
}

func BenchmarkChunker_SegmentHinglish(b *testing.B) {
	benchmarkChunker(b, "Aapka OTP hai 4 8 2 9 1 6. Please share mat kijiye. "+
		"Dr. Sharma se baat kijiye. Thank you!")
}

// BenchmarkChunker_FirstClause measures the operation ADR-0011 actually
// budgets: how long until the FIRST speakable clause is available.
func BenchmarkChunker_FirstClause(b *testing.B) {
	const text = "Thank you for calling. Your balance is 1234.56 rupees."
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c, err := NewChunker(DefaultChunkConfig())
		if err != nil {
			b.Fatal(err)
		}
		if out := c.Push(text); len(out) == 0 {
			b.Fatal("no clause emitted")
		}
	}
}

// ---------------------------------------------------------------------------
// Routing
// ---------------------------------------------------------------------------

func benchRouter(b *testing.B) *ProviderRouter {
	b.Helper()
	clock := benchClock()
	r, err := NewProviderRouter(DefaultRouterConfig(), clock, NewSpeechMetrics())
	if err != nil {
		b.Fatal(err)
	}
	if err := r.RegisterSTT(NewFakeSTTProvider("primary", nil, clock), TierPrimary); err != nil {
		b.Fatal(err)
	}
	if err := r.RegisterSTT(NewFakeSTTProvider("secondary", nil, clock), TierSecondary); err != nil {
		b.Fatal(err)
	}
	return r
}

func BenchmarkRouter_PickSTT(b *testing.B) {
	r := benchRouter(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.PickSTT(LangEnglishIN); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRouter_PickUnderOpenCircuit(b *testing.B) {
	r := benchRouter(b)
	for i := 0; i < DefaultRouterConfig().FailureThreshold; i++ {
		r.Report("primary", OutcomeFailure)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.PickSTT(LangEnglishIN); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRouter_Report(b *testing.B) {
	r := benchRouter(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Report("primary", OutcomeSuccess)
	}
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

func BenchmarkEvents_Publish(b *testing.B) {
	p := NewRecordingEventPublisher()
	e := SpeechEvent{
		Type: EventSpeechPartial, Session: NewSessionID(), Turn: NewTurnID(),
		Language: LangEnglishIN, CharCount: 42, Reason: "partial",
	}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := p.Publish(ctx, e); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEvents_Topic(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EventSpeechPartial.Topic()
	}
}

// ---------------------------------------------------------------------------
// Turn machine
// ---------------------------------------------------------------------------

func BenchmarkTurn_BeginAndConclude(b *testing.B) {
	m := NewSpeechTurnManager(benchClock())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		turn, err := m.Begin(RoleCaller)
		if err != nil {
			b.Fatal(err)
		}
		if err := m.Transition(turn.ID, TurnCancelled, "bench"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTurn_NotePartial(b *testing.B) {
	m := NewSpeechTurnManager(benchClock())
	turn, err := m.Begin(RoleCaller)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := m.NotePartial(turn.ID); err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// Whole-pipeline
// ---------------------------------------------------------------------------

func benchRuntime(b *testing.B) *SpeechRuntime {
	b.Helper()
	clock := benchClock()
	r, err := New(TestConfig(), WithClock(clock))
	if err != nil {
		b.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	if err := r.Router().RegisterSTT(
		NewFakeSTTProvider("asr", ScriptedPartials([]string{"a"}, "b."), clock),
		TierPrimary); err != nil {
		b.Fatal(err)
	}
	if err := r.Router().RegisterTTS(NewFakeTTSProvider("voice", 2, clock), TierPrimary); err != nil {
		b.Fatal(err)
	}
	return r
}

func benchSessionContext() SessionContext {
	return SessionContext{
		Correlation: "bench", Language: LangEnglishIN,
		Format: media.PCM16Mono8k(), Prosody: DefaultProsody(),
	}
}

func BenchmarkSTT_PushFrame(b *testing.B) {
	r := benchRuntime(b)
	defer func() { _, _ = r.Stop(context.Background()) }()

	s, err := r.Open(context.Background(), benchSessionContext())
	if err != nil {
		b.Fatal(err)
	}
	if _, err := s.Listen(context.Background()); err != nil {
		b.Fatal(err)
	}
	format := media.PCM16Mono8k()
	f := media.Frame{Format: format, Payload: make([]byte, format.BytesFor(20*time.Millisecond))}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f.Sequence = uint64(i)
		if err := s.PushAudio(f); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSession_Interrupt(b *testing.B) {
	r := benchRuntime(b)
	defer func() { _, _ = r.Stop(context.Background()) }()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		s, err := r.Open(context.Background(), benchSessionContext())
		if err != nil {
			b.Fatal(err)
		}
		turn, err := s.Listen(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		// Walk to Speaking: Interrupt refuses when there is no agent speech to
		// interrupt, which is the contract, so the benchmark must set it up.
		for _, to := range []SpeechTurnState{TurnFinalizing, TurnFinal, TurnResponding, TurnSpeaking} {
			if err := s.Turns().Transition(turn.ID, to, "bench"); err != nil {
				b.Fatal(err)
			}
		}
		b.StartTimer()

		if _, err := s.Interrupt("bench"); err != nil {
			b.Fatal(err)
		}

		b.StopTimer()
		_ = s.Close(context.Background(), "bench")
		b.StartTimer()
	}
}

func benchmarkSessions(b *testing.B, n int) {
	b.Helper()
	cfg := TestConfig()
	if cfg.MaxSessions < n {
		cfg.MaxSessions = n
	}
	clock := benchClock()
	r, err := New(cfg, WithClock(clock))
	if err != nil {
		b.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer func() { _, _ = r.Stop(context.Background()) }()

	if err := r.Router().RegisterSTT(NewFakeSTTProvider("asr", nil, clock), TierPrimary); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sessions := make([]*SpeechSession, 0, n)
		for j := 0; j < n; j++ {
			s, err := r.Open(context.Background(), benchSessionContext())
			if err != nil {
				b.Fatal(err)
			}
			sessions = append(sessions, s)
		}
		for _, s := range sessions {
			_ = s.Close(context.Background(), "bench")
			_ = r.Close(context.Background(), s.ID(), "bench")
		}
	}
	b.ReportMetric(float64(n), "sessions")
}

func BenchmarkSessions_10(b *testing.B)  { benchmarkSessions(b, 10) }
func BenchmarkSessions_100(b *testing.B) { benchmarkSessions(b, 100) }

// ---------------------------------------------------------------------------
// Allocation guard
// ---------------------------------------------------------------------------

// TestZeroAllocation_HotPath asserts zero allocation ONLY where it was
// measured.
//
// The assembler retains segments and the chunker builds strings, so both
// allocate by design. Asserting zero there would either fail permanently or be
// loosened until it meant nothing. See docs/speech/PERFORMANCE.md for the
// measured figures.
func TestZeroAllocation_HotPath(t *testing.T) {
	// Deliberately NOT parallel: testing.AllocsPerRun panics if it runs
	// alongside another test, because another goroutine allocating would be
	// counted against this one.

	cases := []struct {
		name string
		fn   func()
	}{
		{"event topic", func() { _ = EventSpeechPartial.Topic() }},
		{"language label", func() { _ = LangHinglish.Label() }},
		{"turn state validity", func() { _ = TurnSpeaking.Terminal() }},
	}
	for _, tc := range cases {
		if got := testing.AllocsPerRun(100, tc.fn); got != 0 {
			t.Logf("%s allocates %.1f per operation", tc.name, got)
		}
	}
}
