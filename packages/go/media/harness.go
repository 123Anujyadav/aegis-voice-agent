package media

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// TestConfig returns [DefaultConfig] with the drain budget shortened.
//
// EXPORTED BECAUSE THE ALTERNATIVE IS A FOOTGUN. The drain budget is real time
// (see [MediaRuntime.drain]), so a runtime stopped with live streams waits it
// out for real. A test that built its own Config from DefaultConfig would
// silently reintroduce a five-second shutdown, and the symptom is a suite that
// grew slow for no visible reason — exactly what happened in Phase 11A.
//
// Every test that customises configuration should start here.
func TestConfig() Config {
	cfg := DefaultConfig()
	cfg.DrainTimeout = 50 * time.Millisecond
	// The runtime pump is off by default in tests: a background goroutine
	// moving frames makes every assertion about buffer contents racy. Tests
	// call Pump explicitly, which is also how a consumer with its own cadence
	// should drive the engine.
	cfg.PumpInterval = 0
	return cfg
}

// Harness wires a runtime with fakes for testing.
//
// EXPORTED ON PURPOSE, following the convention every phase since 10A has
// established: a service embedding this engine needs to test its own code
// against it, and forcing every consumer to rebuild this scaffolding is how six
// subtly different fakes come to exist.
type Harness struct {
	Runtime     *MediaRuntime
	Clock       *rt.FakeClock
	Store       *MemoryStreamStore
	Metrics     *MediaMetrics
	Coordinator *MediaCoordinator
	Dispatcher  *MediaDispatcher
	Format      AudioFormat
	// Gen produces well-formed frames for the harness format.
	Gen *FrameGenerator
}

// HarnessOption customises a harness.
type HarnessOption func(*harnessOptions)

type harnessOptions struct {
	cfg     Config
	logger  *slog.Logger
	format  AudioFormat
	check   SourceCheck
	noStore bool
}

// WithHarnessConfig overrides the configuration.
func WithHarnessConfig(c Config) HarnessOption {
	return func(o *harnessOptions) { o.cfg = c }
}

// WithHarnessLogger attaches a logger, for debugging a failing test.
func WithHarnessLogger(l *slog.Logger) HarnessOption {
	return func(o *harnessOptions) { o.logger = l }
}

// WithHarnessFormat sets the audio format.
func WithHarnessFormat(f AudioFormat) HarnessOption {
	return func(o *harnessOptions) { o.format = f }
}

// WithHarnessSourceCheck sets the recovery reattachment probe.
func WithHarnessSourceCheck(f SourceCheck) HarnessOption {
	return func(o *harnessOptions) { o.check = f }
}

// WithoutStreamStore builds a runtime with no store, to exercise the
// no-recovery path.
func WithoutStreamStore() HarnessOption {
	return func(o *harnessOptions) { o.noStore = true }
}

// NewHarness builds a runtime with a fake clock and an in-memory store.
//
// The runtime is NOT started: a test that wants recovery seeds the store first
// and then calls Start, and a harness that started eagerly would make that
// impossible.
func NewHarness(opts ...HarnessOption) (*Harness, error) {
	o := &harnessOptions{
		cfg:    TestConfig(),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		format: PCM16Mono8k(),
	}
	for _, opt := range opts {
		opt(o)
	}

	o.cfg.Pipeline.Format = o.format
	o.cfg.Pipeline.Buffer.Format = o.format

	clock := rt.NewFakeClock(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	m := NewMediaMetrics()

	runtimeOpts := []Option{WithClock(clock), WithLogger(o.logger), WithMetrics(m)}

	var store *MemoryStreamStore
	if !o.noStore {
		store = NewMemoryStreamStore()
		runtimeOpts = append(runtimeOpts, WithStreamStore(store))
	}
	if o.check != nil {
		runtimeOpts = append(runtimeOpts, WithSourceCheck(o.check))
	}

	r, err := New(o.cfg, runtimeOpts...)
	if err != nil {
		return nil, err
	}

	return &Harness{
		Runtime: r, Clock: clock, Store: store, Metrics: m,
		Coordinator: r.Coordinator(), Dispatcher: r.Dispatcher(),
		Format: o.format,
		Gen:    NewFrameGenerator(o.format, o.cfg.Pipeline.FrameInterval),
	}, nil
}

// Start starts the runtime.
func (h *Harness) Start(ctx context.Context) (RecoveryReport, error) {
	return h.Runtime.Start(ctx)
}

// Stop stops the runtime.
func (h *Harness) Stop(ctx context.Context) (int, error) { return h.Runtime.Stop(ctx) }

// Context returns a valid inbound stream context.
func (h *Harness) Context() StreamContext {
	return StreamContext{
		Correlation: NewCorrelationID(),
		Source:      "fake-source",
		Direction:   DirectionInbound,
		Format:      h.Format,
		Tags:        []string{"test"},
	}
}

// Open opens an active stream.
func (h *Harness) Open(ctx context.Context) (*Stream, error) {
	return h.Coordinator.Open(ctx, h.Context())
}

// String renders the harness for a failure message.
func (h *Harness) String() string {
	return fmt.Sprintf("harness: runtime=%s live=%d snapshots=%d",
		h.Runtime.State(), h.Runtime.Live(), h.storeLen())
}

func (h *Harness) storeLen() int {
	if h.Store == nil {
		return 0
	}
	return h.Store.Len()
}

// ---------------------------------------------------------------------------
// FrameGenerator
// ---------------------------------------------------------------------------

// FrameGenerator produces well-formed frames on a media timeline.
//
// Exported because every consumer of this engine needs to feed it test audio,
// and hand-rolling sequence numbers and timestamps is exactly where a test grows
// a bug that makes it agree with a broken implementation.
//
// It reuses one payload buffer, so generated frames are BORROWED in the same way
// buffer reads are. That is deliberate: a generator that allocated per frame
// would hide the allocation cost the engine is designed to avoid, and a
// benchmark using it would measure the wrong thing.
type FrameGenerator struct {
	format   AudioFormat
	interval time.Duration
	seq      uint64
	ts       time.Duration
	buf      []byte
}

// NewFrameGenerator builds a generator for the given format and cadence.
func NewFrameGenerator(format AudioFormat, interval time.Duration) *FrameGenerator {
	n := format.BytesFor(interval)
	if n <= 0 {
		n = 320
	}
	return &FrameGenerator{format: format, interval: interval, buf: make([]byte, n)}
}

// Next returns the next frame in sequence.
//
// The payload is BORROWED from the generator and is overwritten by the next
// call. Callers that retain frames must clone.
func (g *FrameGenerator) Next(arrival time.Time) Frame {
	f := Frame{
		Sequence: g.seq, Timestamp: g.ts, Arrival: arrival,
		Format: g.format, Payload: g.buf,
	}
	g.seq++
	g.ts += g.interval
	return f
}

// NextAt returns the next frame with an explicit sequence and timestamp, for
// tests that need to inject reordering, duplication or gaps.
func (g *FrameGenerator) NextAt(seq uint64, ts time.Duration, arrival time.Time) Frame {
	return Frame{
		Sequence: seq, Timestamp: ts, Arrival: arrival,
		Format: g.format, Payload: g.buf,
	}
}

// Skip advances the generator without producing a frame, creating a gap.
func (g *FrameGenerator) Skip(n int) {
	g.seq += uint64(n)
	g.ts += time.Duration(n) * g.interval
}

// Sequence returns the next sequence number.
func (g *FrameGenerator) Sequence() uint64 { return g.seq }

// Timestamp returns the next media timestamp.
func (g *FrameGenerator) Timestamp() time.Duration { return g.ts }

// Reset returns the generator to its initial position.
func (g *FrameGenerator) Reset() {
	g.seq = 0
	g.ts = 0
}

// FrameSize returns the payload size the generator produces.
func (g *FrameGenerator) FrameSize() int { return len(g.buf) }
