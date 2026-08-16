package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Harness is the runtime's testing substrate.
//
// It is exported and lives in the main package rather than a _test file because
// every service that embeds this runtime needs it: a service testing its own
// orchestration must be able to drive a fake provider and a controllable clock
// without reimplementing either. A harness that only the runtime's own tests
// can use pushes every consumer into using real time and real network.
//
// A Harness is single-test-scoped and is NOT safe for concurrent use across
// tests. Its components are individually thread-safe, which is what makes it
// usable to test concurrency.
type Harness struct {
	// Clock is the controllable clock. Advance it to drive timeouts, TTLs and
	// backoff without sleeping.
	Clock *FakeClock

	// Metrics is the kernel's instrument set, exposed for assertions.
	Metrics *Metrics

	// Kernel is the runtime under test.
	Kernel *Kernel

	// Provider is the default fake provider, registered as "fake".
	Provider *FakeProvider
}

// HarnessOption customises a Harness.
type HarnessOption func(*harnessOptions)

type harnessOptions struct {
	config   *Config
	provider *FakeProvider
	logger   *slog.Logger
}

// WithHarnessConfig overrides the kernel configuration.
func WithHarnessConfig(cfg Config) HarnessOption {
	return func(o *harnessOptions) { o.config = &cfg }
}

// WithHarnessProvider replaces the default fake provider.
func WithHarnessProvider(p *FakeProvider) HarnessOption {
	return func(o *harnessOptions) { o.provider = p }
}

// WithHarnessLogger sets the logger. Defaults to a discarding logger, so a
// passing test produces no output and a failing one is readable.
func WithHarnessLogger(l *slog.Logger) HarnessOption {
	return func(o *harnessOptions) { o.logger = l }
}

// NewHarness builds a runtime wired for test.
//
// It registers one fake provider and one model per tier, all bound to it, so a
// test that only cares about scheduling or streaming need not construct a
// catalogue.
func NewHarness(opts ...HarnessOption) (*Harness, error) {
	o := &harnessOptions{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, opt := range opts {
		opt(o)
	}

	clock := NewFakeClock(time.Time{})
	metrics := NewMetrics()

	cfg := DefaultConfig("test-runtime", "0.0.0-test")
	if o.config != nil {
		cfg = *o.config
		if cfg.Name == "" {
			cfg.Name = "test-runtime"
		}
	}
	// Tests must not depend on host CPU count: a shed threshold derived from
	// NumCPU makes a load-shedding test pass on a laptop and fail in CI.
	if o.config == nil {
		cfg.Scheduler.MaxConcurrent = 8
		cfg.Scheduler.MaxQueued = 8
		cfg.Session.Shards = 4
	}

	k, err := New(cfg,
		WithClock(clock),
		WithMetrics(metrics),
		WithLogger(o.logger),
	)
	if err != nil {
		return nil, err
	}

	provider := o.provider
	if provider == nil {
		provider = NewFakeProvider("fake")
	}
	if err := k.Providers().Register(provider); err != nil {
		return nil, err
	}

	for _, tier := range []ModelTier{TierFast, TierBalanced, TierDeep} {
		spec := ModelSpec{
			ID:                     ModelID(fmt.Sprintf("fake-%s", tier)),
			Provider:               provider.ID(),
			Tier:                   tier,
			MaxContextTokens:       8192,
			MaxOutputTokens:        1024,
			DefaultMaxOutputTokens: 256,
			SupportsThinking:       true,
			SupportsToolCalling:    tier != TierFast,
			TypicalLatency:         100 * time.Millisecond,
			Enabled:                true,
		}
		if err := k.Models().Register(spec); err != nil {
			return nil, err
		}
	}

	return &Harness{Clock: clock, Metrics: metrics, Kernel: k, Provider: provider}, nil
}

// Start starts the kernel.
func (h *Harness) Start(ctx context.Context) error { return h.Kernel.Start(ctx) }

// Stop stops the kernel.
func (h *Harness) Stop(ctx context.Context) error { return h.Kernel.Stop(ctx) }

// NewSession creates and activates a session.
func (h *Harness) NewSession() (*Session, error) {
	s, err := h.Kernel.Sessions().Create(0)
	if err != nil {
		return nil, err
	}
	if err := s.Activate(); err != nil {
		return nil, err
	}
	return s, nil
}

// ---------------------------------------------------------------------------
// Fake provider
// ---------------------------------------------------------------------------

// FakeProvider is a scriptable Provider.
//
// It is deterministic by default: the same script produces the same chunks in
// the same order every run. Anything non-deterministic — a delay, a failure — is
// opt-in and driven by the fake clock, so a test that exercises a timeout does
// not become a test that exercises the CI machine's load average.
type FakeProvider struct {
	id           ProviderID
	capabilities Capabilities

	mu       sync.Mutex
	script   []Chunk
	failWith error
	failFor  int
	delay    time.Duration
	clock    Clock

	calls    atomic.Int64
	probeErr atomic.Pointer[error]
	closed   atomic.Bool

	// lastRequest records the most recent request for assertion. Tests need to
	// prove the runtime built the request correctly — notably that Invariant I3
	// forced thinking on — and that is only observable here.
	lastReq atomic.Pointer[GenerateRequest]
}

// NewFakeProvider constructs a fake with a one-chunk default script.
func NewFakeProvider(id ProviderID) *FakeProvider {
	return &FakeProvider{
		id: id,
		capabilities: Capabilities{
			Streaming:        true,
			Thinking:         true,
			ToolCalling:      true,
			MaxContextTokens: 8192,
			MaxOutputTokens:  1024,
		},
		script: []Chunk{
			{Kind: ChunkText, Text: "ok"},
			{Kind: ChunkUsage, Usage: Usage{InputTokens: 10, OutputTokens: 1}},
			{Kind: ChunkDone},
		},
	}
}

// ID returns the provider identifier.
func (f *FakeProvider) ID() ProviderID { return f.id }

// Capabilities returns the fake's capabilities.
func (f *FakeProvider) Capabilities() Capabilities { return f.capabilities }

// SetCapabilities replaces the advertised capabilities.
func (f *FakeProvider) SetCapabilities(c Capabilities) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.capabilities = c
}

// Script sets the chunks the fake emits.
func (f *FakeProvider) Script(chunks ...Chunk) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.script = chunks
}

// FailNext makes the next n Generate calls fail with err.
func (f *FakeProvider) FailNext(n int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failFor = n
	f.failWith = err
}

// SetChunkDelay makes the stream wait d of FAKE time between chunks. clock must
// be the harness clock; without it the delay would be real and the test slow.
func (f *FakeProvider) SetChunkDelay(clock Clock, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock = clock
	f.delay = d
}

// SetProbeError makes Probe fail.
func (f *FakeProvider) SetProbeError(err error) {
	if err == nil {
		f.probeErr.Store(nil)
		return
	}
	f.probeErr.Store(&err)
}

// Calls reports how many times Generate has been called.
func (f *FakeProvider) Calls() int64 { return f.calls.Load() }

// LastRequest returns the most recent request, or nil.
func (f *FakeProvider) LastRequest() *GenerateRequest { return f.lastReq.Load() }

// Generate returns a scripted stream.
func (f *FakeProvider) Generate(ctx context.Context, req GenerateRequest) (TokenStream, error) {
	if f.closed.Load() {
		return nil, ErrClosed
	}
	f.calls.Add(1)
	reqCopy := req
	f.lastReq.Store(&reqCopy)

	f.mu.Lock()
	if f.failFor > 0 {
		f.failFor--
		err := f.failWith
		f.mu.Unlock()
		if err == nil {
			err = &ProviderError{Provider: f.id, Model: req.Model, Kind: KindTransport,
				Err: errors.New("scripted failure")}
		}
		return nil, err
	}
	chunks := make([]Chunk, len(f.script))
	copy(chunks, f.script)
	delay := f.delay
	clock := f.clock
	f.mu.Unlock()

	if delay > 0 && clock != nil {
		return &delayedStream{
			chunks:   chunks,
			delay:    delay,
			clock:    clock,
			ctx:      ctx,
			closedCh: make(chan struct{}),
		}, nil
	}
	return NewSliceStream(clock, chunks...), nil
}

// Probe reports the configured probe result.
func (f *FakeProvider) Probe(context.Context) error {
	if p := f.probeErr.Load(); p != nil {
		return *p
	}
	if f.closed.Load() {
		return ErrClosed
	}
	return nil
}

// Close marks the provider closed.
func (f *FakeProvider) Close() error {
	f.closed.Store(true)
	return nil
}

// delayedStream emits chunks separated by fake-clock delays.
//
// It exists to test two things that cannot otherwise be tested without real
// sleeps: the stalled-stream detector, and — much more importantly — that an
// abort preempts a Recv that is blocked waiting for a chunk that will never
// come. That second case is the whole barge-in guarantee.
type delayedStream struct {
	mu       sync.Mutex
	chunks   []Chunk
	pos      int
	delay    time.Duration
	clock    Clock
	ctx      context.Context
	closeOnc sync.Once
	closedCh chan struct{}
}

// Recv waits the configured fake delay, then returns the next chunk.
//
// The wait selects on Close as well as on the timer. It has to: the delay is
// fake time that nothing may ever advance, so a Recv waiting only on the timer
// would never return and the goroutine would leak on every abort. That is the
// same preemption problem the dispatcher solves for real providers, and the
// fake must reproduce it rather than paper over it — otherwise the leak test
// passes against a fake that could not leak in the first place.
func (d *delayedStream) Recv() (Chunk, error) {
	timer := d.clock.NewTimer(d.delay)
	defer timer.Stop()

	select {
	case <-timer.C():
	case <-d.closedCh:
		return Chunk{}, ErrClosed
	case <-d.ctx.Done():
		return Chunk{}, d.ctx.Err()
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pos >= len(d.chunks) {
		return Chunk{}, io.EOF
	}
	c := d.chunks[d.pos]
	c.Index = d.pos
	c.ReceivedAt = d.clock.Now()
	d.pos++
	return c, nil
}

// Close marks the stream closed. Idempotent and safe alongside Recv.
func (d *delayedStream) Close() error {
	d.closeOnc.Do(func() { close(d.closedCh) })
	return nil
}

// ---------------------------------------------------------------------------
// Assertions
// ---------------------------------------------------------------------------

// RecordingSink captures everything written to it, for assertions.
type RecordingSink struct {
	mu       sync.Mutex
	chunks   []Chunk
	closed   bool
	closeErr error
	thinking bool
}

// NewRecordingSink returns a sink that records chunks.
func NewRecordingSink(acceptThinking bool) *RecordingSink {
	return &RecordingSink{thinking: acceptThinking}
}

// Write records a chunk.
func (r *RecordingSink) Write(c Chunk) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	r.chunks = append(r.chunks, c)
	return nil
}

// Close records the terminating error.
func (r *RecordingSink) Close(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	r.closeErr = err
}

// AcceptsThinking reports the sink's opt-in.
func (r *RecordingSink) AcceptsThinking() bool { return r.thinking }

// Chunks returns a copy of everything recorded.
func (r *RecordingSink) Chunks() []Chunk {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Chunk, len(r.chunks))
	copy(out, r.chunks)
	return out
}

// Text returns the concatenation of every ChunkText.
func (r *RecordingSink) Text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b []byte
	for _, c := range r.chunks {
		if c.Kind == ChunkText {
			b = append(b, c.Text...)
		}
	}
	return string(b)
}

// CountOf returns how many chunks of a kind were recorded.
func (r *RecordingSink) CountOf(k ChunkKind) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.chunks {
		if c.Kind == k {
			n++
		}
	}
	return n
}

// Closed reports whether Close has been called, and with what.
func (r *RecordingSink) Closed() (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed, r.closeErr
}
