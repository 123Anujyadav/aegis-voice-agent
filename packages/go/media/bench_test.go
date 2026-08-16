package media

import (
	"context"
	"testing"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

func benchFormat() AudioFormat { return PCM16Mono8k() }

func benchClock() *rt.FakeClock {
	return rt.NewFakeClock(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
}

// ---------------------------------------------------------------------------
// Frame model
// ---------------------------------------------------------------------------

func BenchmarkFrame_Validate(b *testing.B) {
	gen := NewFrameGenerator(benchFormat(), 20*time.Millisecond)
	f := gen.Next(time.Now())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := f.Validate(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFrame_Duration(b *testing.B) {
	gen := NewFrameGenerator(benchFormat(), 20*time.Millisecond)
	f := gen.Next(time.Now())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = f.Duration()
	}
}

func BenchmarkFrame_Clone(b *testing.B) {
	gen := NewFrameGenerator(benchFormat(), 20*time.Millisecond)
	f := gen.Next(time.Now())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = f.Clone()
	}
}

// ---------------------------------------------------------------------------
// Ring buffer — the path that must not allocate
// ---------------------------------------------------------------------------

func BenchmarkRingBuffer_WriteRead(b *testing.B) {
	buf, err := NewRingBuffer(DefaultBufferConfig(benchFormat()))
	if err != nil {
		b.Fatal(err)
	}
	gen := NewFrameGenerator(benchFormat(), 20*time.Millisecond)
	now := time.Now()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := buf.Write(gen.Next(now)); err != nil {
			b.Fatal(err)
		}
		if _, err := buf.Read(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRingBuffer_Write(b *testing.B) {
	buf, err := NewRingBuffer(DefaultBufferConfig(benchFormat()))
	if err != nil {
		b.Fatal(err)
	}
	gen := NewFrameGenerator(benchFormat(), 20*time.Millisecond)
	now := time.Now()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := buf.Write(gen.Next(now)); err != nil {
			buf.Flush()
		}
	}
}

func BenchmarkRingBuffer_Peek(b *testing.B) {
	buf, err := NewRingBuffer(DefaultBufferConfig(benchFormat()))
	if err != nil {
		b.Fatal(err)
	}
	gen := NewFrameGenerator(benchFormat(), 20*time.Millisecond)
	if err := buf.Write(gen.Next(time.Now())); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := buf.Peek(); err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// Jitter buffer — clones by design, so this is expected to allocate
// ---------------------------------------------------------------------------

func BenchmarkJitterBuffer_PutGet(b *testing.B) {
	clock := benchClock()
	jb, err := NewJitterBuffer(DefaultJitterConfig(), clock)
	if err != nil {
		b.Fatal(err)
	}
	gen := NewFrameGenerator(benchFormat(), 20*time.Millisecond)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		jb.Put(gen.Next(clock.Now()))
		clock.Advance(20 * time.Millisecond)
		for {
			if _, err := jb.Get(); err != nil {
				break
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Pipeline
// ---------------------------------------------------------------------------

func BenchmarkPipeline_PushPump(b *testing.B) {
	clock := benchClock()
	p, err := NewPipeline(DefaultPipelineConfig(benchFormat()), clock)
	if err != nil {
		b.Fatal(err)
	}
	gen := NewFrameGenerator(benchFormat(), 20*time.Millisecond)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Push(gen.Next(clock.Now()))
		clock.Advance(20 * time.Millisecond)
		p.Pump()
		for {
			if _, err := p.Buffer().Read(); err != nil {
				break
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Throughput at concurrency
// ---------------------------------------------------------------------------

// benchmarkStreams measures aggregate throughput at a given stream count.
func benchmarkStreams(b *testing.B, streams int) {
	b.Helper()

	cfg := TestConfig()
	if cfg.MaxStreams < streams {
		cfg.MaxStreams = streams
	}
	if cfg.MaxStreamsPerSource < streams {
		cfg.MaxStreamsPerSource = streams
	}

	h, err := NewHarness(WithHarnessConfig(cfg))
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	if _, err := h.Start(ctx); err != nil {
		b.Fatal(err)
	}
	defer func() { _, _ = h.Stop(context.Background()) }()

	open := make([]*Stream, 0, streams)
	gens := make([]*FrameGenerator, 0, streams)
	for i := 0; i < streams; i++ {
		s, err := h.Open(ctx)
		if err != nil {
			b.Fatalf("open %d: %v", i, err)
		}
		open = append(open, s)
		gens = append(gens, NewFrameGenerator(h.Format, 20*time.Millisecond))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		now := h.Clock.Now()
		for j, s := range open {
			if _, err := s.Write(gens[j].Next(now)); err != nil {
				b.Fatal(err)
			}
		}
		h.Clock.Advance(20 * time.Millisecond)
		for _, s := range open {
			s.Pump()
			for {
				if _, err := s.Read(); err != nil {
					break
				}
			}
		}
	}
	b.ReportMetric(float64(streams), "streams")
}

func BenchmarkStreams_1(b *testing.B)    { benchmarkStreams(b, 1) }
func BenchmarkStreams_100(b *testing.B)  { benchmarkStreams(b, 100) }
func BenchmarkStreams_1000(b *testing.B) { benchmarkStreams(b, 1000) }

func BenchmarkStream_ConcurrentWriteRead(b *testing.B) {
	h, err := NewHarness()
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	if _, err := h.Start(ctx); err != nil {
		b.Fatal(err)
	}
	defer func() { _, _ = h.Stop(context.Background()) }()

	s, err := h.Open(ctx)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		gen := NewFrameGenerator(h.Format, 20*time.Millisecond)
		for pb.Next() {
			_, _ = s.Write(gen.Next(time.Now()))
			_, _ = s.Read()
		}
	})
}

// ---------------------------------------------------------------------------
// Allocation guard
// ---------------------------------------------------------------------------

// TestZeroAllocation_SteadyState guards doc.go's zero-allocation claim.
//
// ONLY operations measured at zero are asserted here. The jitter buffer clones
// on Put by design and therefore allocates; see PERFORMANCE.md for the figure
// and doc.go for the scoped claim. A guard that asserted zero everywhere would
// either fail permanently or be quietly loosened until it meant nothing.
func TestZeroAllocation_SteadyState(t *testing.T) {
	buf, err := NewRingBuffer(DefaultBufferConfig(benchFormat()))
	if err != nil {
		t.Fatal(err)
	}
	gen := NewFrameGenerator(benchFormat(), 20*time.Millisecond)
	now := time.Now()

	cases := []struct {
		name string
		fn   func()
	}{
		{"ring write+read", func() {
			if err := buf.Write(gen.Next(now)); err != nil {
				return
			}
			if _, err := buf.Read(); err != nil {
				return
			}
		}},
		{"frame validate", func() {
			f := gen.Next(now)
			_ = f.Validate()
		}},
		{"frame duration", func() {
			f := gen.Next(now)
			_ = f.Duration()
		}},
	}

	for _, tc := range cases {
		if got := testing.AllocsPerRun(100, tc.fn); got != 0 {
			t.Errorf("%s allocates %.1f times per operation, want 0", tc.name, got)
		}
	}
}
