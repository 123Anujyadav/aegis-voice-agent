package runtime

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// Benchmarks for the hot path. Every number in docs/runtime/PERFORMANCE.md is
// produced by these, on the machine and toolchain named there — not estimated.
//
// What is measured is deliberately narrow: the runtime's OWN overhead, with a
// fake provider that returns instantly. Model latency dominates any real call
// by three orders of magnitude, so measuring end-to-end would measure the fake
// and tell us nothing about the code under test.

func BenchmarkScheduler_AdmitRelease(b *testing.B) {
	s, err := NewScheduler(SchedulerConfig{
		MaxConcurrent: 1 << 16, MaxQueued: 1 << 16,
		QueueTimeout: time.Second, SheddingThreshold: 0.99,
	}, SystemClock{}, NewMetrics())
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		release, d := s.Admit(ctx, ClassStandard, time.Time{})
		if !d.Admitted {
			b.Fatal("unexpected shed")
		}
		release()
	}
}

func BenchmarkScheduler_AdmitReleaseParallel(b *testing.B) {
	s, _ := NewScheduler(SchedulerConfig{
		MaxConcurrent: 1 << 16, MaxQueued: 1 << 16,
		QueueTimeout: time.Second, SheddingThreshold: 0.99,
	}, SystemClock{}, NewMetrics())
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			release, d := s.Admit(ctx, ClassStandard, time.Time{})
			if d.Admitted {
				release()
			}
		}
	})
}

// BenchmarkScheduler_ShedPath measures the refusal path, which must be cheaper
// than the admission path — under overload it is the one taken most.
func BenchmarkScheduler_ShedPath(b *testing.B) {
	s, _ := NewScheduler(SchedulerConfig{
		MaxConcurrent: 2, MaxQueued: 0,
		QueueTimeout: time.Millisecond, SheddingThreshold: 0.01,
	}, SystemClock{}, NewMetrics())
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, d := s.Admit(ctx, ClassStandard, time.Time{})
		if d.Admitted {
			b.Fatal("expected a shed")
		}
	}
}

func BenchmarkDispatcher_Run64Chunks(b *testing.B) {
	chunks := make([]Chunk, 0, 66)
	for i := 0; i < 64; i++ {
		chunks = append(chunks, Chunk{Kind: ChunkText, Text: "token "})
	}
	chunks = append(chunks,
		Chunk{Kind: ChunkUsage, Usage: Usage{InputTokens: 100, OutputTokens: 64}},
		Chunk{Kind: ChunkDone})

	cfg := DefaultDispatcherConfig()
	metrics := NewMetrics()
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d, _ := NewDispatcher(cfg, SystemClock{}, metrics)
		_ = d.AddSink(NewBufferedSink(128, false))
		d.Run(ctx, NewSliceStream(SystemClock{}, chunks...))
	}
}

// BenchmarkDispatcher_FanOut4 measures the per-sink cost of fan-out. The
// dispatcher spawns a bounded-write goroutine per sink per chunk, which is the
// price of not letting one slow consumer hold the barge-in budget open. This
// benchmark is what tells us whether that price is acceptable.
func BenchmarkDispatcher_FanOut4(b *testing.B) {
	chunks := []Chunk{
		{Kind: ChunkText, Text: "a"},
		{Kind: ChunkText, Text: "b"},
		{Kind: ChunkText, Text: "c"},
		{Kind: ChunkText, Text: "d"},
		{Kind: ChunkDone},
	}
	cfg := DefaultDispatcherConfig()
	metrics := NewMetrics()
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d, _ := NewDispatcher(cfg, SystemClock{}, metrics)
		for j := 0; j < 4; j++ {
			_ = d.AddSink(NewBufferedSink(16, false))
		}
		d.Run(ctx, NewSliceStream(SystemClock{}, chunks...))
	}
}

// BenchmarkDispatcher_AbortLatency measures the barge-in path against a
// provider that has gone silent. This is the ADR-0011 number.
func BenchmarkDispatcher_AbortLatency(b *testing.B) {
	cfg := DefaultDispatcherConfig()
	metrics := NewMetrics()
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		d, _ := NewDispatcher(cfg, SystemClock{}, metrics)
		_ = d.AddSink(NewBufferedSink(8, false))
		stream := newBlockingStream()
		done := make(chan struct{})
		go func() { d.Run(ctx, stream); close(done) }()
		<-stream.recvIn
		b.StartTimer()

		d.Abort()
		<-done
	}
}

func BenchmarkSessionManager_CreateGetRemove(b *testing.B) {
	m, err := NewSessionManager(SessionConfig{
		MaxSessions: 0, IdleTTL: time.Minute, MaxLifetime: time.Hour,
		SweepInterval: time.Minute, Shards: 64, DefaultContextTokens: 4096,
	}, SystemClock{}, NewMetrics())
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s, err := m.Create(0)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := m.Get(s.ID()); err != nil {
			b.Fatal(err)
		}
		m.Remove(s.ID())
	}
}

// BenchmarkSessionManager_GetParallel measures lookup under contention, which
// is what the sharded map exists for.
func BenchmarkSessionManager_GetParallel(b *testing.B) {
	m, _ := NewSessionManager(SessionConfig{
		MaxSessions: 0, IdleTTL: time.Minute, MaxLifetime: time.Hour,
		SweepInterval: time.Minute, Shards: 64, DefaultContextTokens: 4096,
	}, SystemClock{}, NewMetrics())

	ids := make([]SessionID, 1024)
	for i := range ids {
		s, _ := m.Create(0)
		ids[i] = s.ID()
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_, _ = m.Get(ids[i&1023])
			i++
		}
	})
}

// BenchmarkSessionManager_Sweep measures the expiry scan, which runs on a timer
// and must not stall lookups. 10k sessions is a realistic per-instance load.
func BenchmarkSessionManager_Sweep(b *testing.B) {
	clock := NewFakeClock(time.Time{})
	m, _ := NewSessionManager(SessionConfig{
		MaxSessions: 0, IdleTTL: time.Hour, MaxLifetime: 24 * time.Hour,
		SweepInterval: time.Minute, Shards: 64, DefaultContextTokens: 4096,
	}, clock, NewMetrics())

	for i := 0; i < 10_000; i++ {
		s, _ := m.Create(0)
		_ = s.Activate()
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.sweep()
	}
}

func BenchmarkContextWindow_Append(b *testing.B) {
	c := NewContextWindow(1<<20, NewMetrics())
	msg := Message{Role: RoleUser, Content: strings.Repeat("hello world ", 8)}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Append(msg)
	}
}

// BenchmarkContextWindow_AppendWithEviction measures the steady state of a long
// session: every append forces an eviction.
func BenchmarkContextWindow_AppendWithEviction(b *testing.B) {
	c := NewContextWindow(2048, NewMetrics())
	msg := Message{Role: RoleUser, Content: strings.Repeat("hello world ", 8)}
	for i := 0; i < 64; i++ {
		_ = c.Append(msg)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Append(msg)
	}
}

func BenchmarkContextWindow_Assemble(b *testing.B) {
	c := NewContextWindow(1<<16, NewMetrics())
	for i := 0; i < 200; i++ {
		_ = c.Append(Message{Role: RoleUser, Content: strings.Repeat("x", 200)})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Assemble(8192); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTokenCounter_Latin(b *testing.B) {
	tc := NewHeuristicTokenCounter()
	s := strings.Repeat("the quick brown fox jumps over the lazy dog ", 16)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tc.Count(s)
	}
}

func BenchmarkTokenCounter_Devanagari(b *testing.B) {
	tc := NewHeuristicTokenCounter()
	s := strings.Repeat("नमस्ते दुनिया यह एक परीक्षण है ", 16)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tc.Count(s)
	}
}

func BenchmarkMetrics_CounterInc(b *testing.B) {
	m := NewMetrics()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			m.SchedulerShed.Inc("standard", "capacity")
		}
	})
}

func BenchmarkMetrics_HistogramObserve(b *testing.B) {
	m := NewMetrics()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			m.StreamDuration.Observe(0.42)
		}
	})
}

func BenchmarkBreaker_AllowReport(b *testing.B) {
	br, err := NewBreaker("p", DefaultBreakerConfig(), SystemClock{})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		allowed, report := br.Allow()
		if allowed {
			report(nil)
		}
	}
}

func BenchmarkFSM_Transition(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, _ := NewFSM(FSMSpec[tState]{
			Initial:     sA,
			Transitions: map[tState][]tState{sA: {sB}, sB: {sEnd}},
			Terminal:    []tState{sEnd},
		}, SystemClock{})
		_, _ = f.To(sB)
		_, _ = f.To(sEnd)
	}
}

func BenchmarkNewID(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = NewSessionID()
		}
	})
}

// BenchmarkKernel_GenerateEndToEnd measures the full runtime path with an
// instant provider: admission, session, context assembly, model resolution,
// breaker, dispatch and cleanup. This is the number that matters for the
// ADR-0011 budget, because it is what the runtime adds to model latency.
func BenchmarkKernel_GenerateEndToEnd(b *testing.B) {
	cfg := DefaultConfig("bench", "0")
	cfg.Scheduler.MaxConcurrent = 4096
	cfg.Scheduler.MaxQueued = 4096
	cfg.Session.Shards = 64

	h, err := NewHarness(WithHarnessConfig(cfg))
	if err != nil {
		b.Fatal(err)
	}
	// A real clock: the deadline path must be exercised as it runs in
	// production, and a fake clock would remove the timer goroutine entirely.
	k, err := New(cfg, WithMetrics(h.Metrics))
	if err != nil {
		b.Fatal(err)
	}
	provider := NewFakeProvider("fake")
	_ = k.Providers().Register(provider)
	_ = k.Models().Register(ModelSpec{
		ID: "bench-model", Provider: "fake", Tier: TierFast,
		MaxContextTokens: 8192, MaxOutputTokens: 512, DefaultMaxOutputTokens: 128,
		SupportsThinking: true, TypicalLatency: time.Millisecond, Enabled: true,
	})
	if err := k.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = k.Stop(ctx)
	}()

	session, _ := k.Sessions().Create(0)
	_ = session.Activate()
	msgs := []Message{{Role: RoleUser, Content: "hello"}}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d, err := k.Generate(ctx, GenerateSpec{
			SessionID: session.ID(), Tier: TierFast, Messages: msgs,
		}, NewBufferedSink(8, false))
		if err != nil {
			b.Fatal(err)
		}
		<-d.Done()
	}
}

// BenchmarkKernel_GenerateParallel measures the same path under contention,
// which is the shape production actually has: many concurrent sessions.
func BenchmarkKernel_GenerateParallel(b *testing.B) {
	cfg := DefaultConfig("bench-par", "0")
	cfg.Scheduler.MaxConcurrent = 8192
	cfg.Scheduler.MaxQueued = 8192
	cfg.Session.Shards = 128

	k, err := New(cfg)
	if err != nil {
		b.Fatal(err)
	}
	_ = k.Providers().Register(NewFakeProvider("fake"))
	_ = k.Models().Register(ModelSpec{
		ID: "bench-model", Provider: "fake", Tier: TierFast,
		MaxContextTokens: 8192, MaxOutputTokens: 512, DefaultMaxOutputTokens: 128,
		SupportsThinking: true, TypicalLatency: time.Millisecond, Enabled: true,
	})
	if err := k.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = k.Stop(ctx)
	}()

	var sessionPool sync.Pool
	ctx := context.Background()
	msgs := []Message{{Role: RoleUser, Content: "hello"}}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		s, _ := sessionPool.Get().(*Session)
		if s == nil {
			s, _ = k.Sessions().Create(0)
			_ = s.Activate()
		}
		defer sessionPool.Put(s)

		for pb.Next() {
			d, err := k.Generate(ctx, GenerateSpec{
				SessionID: s.ID(), Tier: TierFast, Messages: msgs,
			}, NewBufferedSink(8, false))
			if err != nil {
				continue // a shed is a legitimate outcome, not a benchmark failure
			}
			<-d.Done()
		}
	})
}
