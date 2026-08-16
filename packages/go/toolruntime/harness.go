package toolruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Harness wires a tool runtime for test with a controllable clock, a recording
// publisher and a recording auditor.
//
// Exported rather than test-only because every service embedding this runtime
// needs it. A service testing its own reaction to execution events should not
// have to reimplement a fake clock, a fake publisher and a scriptable tool — and
// a harness only this package can use pushes every consumer into real time,
// which is how a test suite becomes slow enough that somebody starts skipping it.
type Harness struct {
	// Clock is the controllable clock.
	Clock *rt.FakeClock
	// Metrics is the runtime's instrument set.
	Metrics *Metrics
	// Runtime is the system under test.
	Runtime *ToolRuntime
	// Events records everything published.
	Events *RecordingPublisher
	// Audit records every audit entry.
	Audit *RecordingAuditor
}

// HarnessOption customises a harness.
type HarnessOption func(*harnessOptions)

type harnessOptions struct {
	cfg    *Config
	logger *slog.Logger
}

// WithHarnessConfig overrides the runtime configuration.
func WithHarnessConfig(c Config) HarnessOption {
	return func(o *harnessOptions) { o.cfg = &c }
}

// WithHarnessLogger sets the logger. Defaults to discarding, so a passing test
// is silent and a failing one is readable.
func WithHarnessLogger(l *slog.Logger) HarnessOption {
	return func(o *harnessOptions) { o.logger = l }
}

// NewHarness builds a tool runtime wired for test.
func NewHarness(opts ...HarnessOption) (*Harness, error) {
	o := &harnessOptions{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, opt := range opts {
		opt(o)
	}

	clock := rt.NewFakeClock(rt.SystemClock{}.Now().Truncate(0))
	metrics := NewMetrics()
	events := NewRecordingPublisher()
	audit := NewRecordingAuditor(0)

	cfg := DefaultConfig()
	if o.cfg != nil {
		cfg = *o.cfg
	}
	// Retry delays are off by default in the harness. Backoff sleeps on the
	// injected clock, so a test exercising a retry would otherwise have to
	// advance the clock for a delay that is orthogonal to what it asserts — and
	// a test that forgets hangs rather than fails, which is the worst failure
	// mode a suite can have. A test that is specifically about backoff sets a
	// RetrySpec with NoBackoff false and drives the clock itself.
	cfg.DefaultRetry.NoBackoff = true

	r, err := New(cfg,
		WithClock(clock), WithMetrics(metrics), WithLogger(o.logger),
		WithPublisher(events), WithAuditor(audit))
	if err != nil {
		return nil, err
	}
	return &Harness{Clock: clock, Metrics: metrics, Runtime: r, Events: events, Audit: audit}, nil
}

// Register adds a tool with a contract, failing loudly on a bad contract.
//
// Panics rather than returning an error because a test that registers an
// invalid contract has a bug in the test, and threading an error return through
// every setup line buries the assertion the test is actually about.
func (h *Harness) Register(c Contract, tool Tool) *Harness {
	reg := Registration{Contract: c, Tool: tool, Lifecycle: LifecycleActive, Health: HealthHealthy}
	if err := h.Runtime.Registry().Register(reg); err != nil {
		panic("toolruntime: harness registration failed: " + err.Error())
	}
	return h
}

// RegisterAt adds a tool in a specific lifecycle stage and health state.
func (h *Harness) RegisterAt(c Contract, tool Tool, l Lifecycle, health Health, priority int) *Harness {
	reg := Registration{Contract: c, Tool: tool, Lifecycle: l, Health: health, Priority: priority}
	if err := h.Runtime.Registry().Register(reg); err != nil {
		panic("toolruntime: harness registration failed: " + err.Error())
	}
	return h
}

// Intent builds a single-capability intent with a permissive grant.
func (h *Harness) Intent(cap CapabilityID, args Arguments, perms ...Permission) ToolIntent {
	return ToolIntent{
		ID:          NewIntentID(),
		Correlation: NewCorrelationID(),
		Session:     "sess-test",
		Actor:       "actor-test",
		Requests: []CapabilityRequest{{
			Ref: "only", Capability: cap, Version: AnyVersion(), Args: args,
		}},
		Grant: Grant{Actor: "actor-test", Permissions: perms},
	}
}

// ---------------------------------------------------------------------------
// Contract builders
// ---------------------------------------------------------------------------

// ReadContract builds a read-only contract, the shape most tests need.
func ReadContract(tool ToolID, version Version, cap CapabilityID) Contract {
	return Contract{
		Descriptor:   Descriptor{Tool: tool, Version: version},
		Capabilities: []CapabilityID{cap},
		Title:        string(tool),
		Owner:        "test-team",
		Effect:       EffectRead,
		Timeout:      time.Second,
		Input: []FieldSpec{
			{Name: "query", Kind: ValueString, Required: true, MaxLen: 256},
		},
		Output: []FieldSpec{
			{Name: "answer", Kind: ValueString, Required: true},
		},
	}
}

// WriteContract builds a mutating, compensable contract.
func WriteContract(tool ToolID, version Version, cap CapabilityID) Contract {
	c := ReadContract(tool, version, cap)
	c.Effect = EffectWrite
	c.Compensable = true
	c.Input = []FieldSpec{{Name: "subject", Kind: ValueString, Required: true, MaxLen: 256}}
	c.Output = []FieldSpec{{Name: "reference", Kind: ValueString, Required: true}}
	return c
}

// IrreversibleContract builds a contract for an action that cannot be undone.
func IrreversibleContract(tool ToolID, version Version, cap CapabilityID) Contract {
	c := WriteContract(tool, version, cap)
	c.Effect = EffectIrreversible
	c.Compensable = false
	c.Retry = RetrySpec{MaxAttempts: 1}
	return c
}

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// FakeTool is a scriptable [Tool].
//
// It is the ONLY tool implementation in this module, and it does nothing real.
// The Phase 10D brief excludes every actual integration — telephony, CRM,
// calendar, payments — so a real adapter here would breach scope. This exists
// to prove the RUNTIME: the retry loop, the budget, the idempotency ledger, the
// compensation journal, the permission gate.
type FakeTool struct {
	// Produce builds the result. Nil returns a fixed echo of the arguments.
	Produce func(Invocation) (Result, error)

	// FailTimes makes the first n calls fail with FailWith, then succeed. The
	// script for "transient failure, then recovery", which is the case a retry
	// engine exists for.
	FailTimes int32

	// FailWith is the error returned while failing. Defaults to a generic
	// transient error, which the classifier treats as retryable.
	FailWith error

	// FailAlways makes every call fail. A separate flag rather than a huge
	// FailTimes, because overloading a count with a sentinel is how a test ends
	// up not testing what it is named after.
	FailAlways bool

	// Delay is how long the tool sleeps before answering, on the injected
	// clock. Used to drive timeout tests without a real wait.
	Delay time.Duration

	// Clock drives Delay. Required when Delay is set.
	Clock rt.Clock

	// PanicOnCall panics on the nth call, 1-based. Zero never panics.
	PanicOnCall int32

	// IgnoreCancellation makes the tool NOT honour context cancellation, which
	// is how a real misbehaving integration behaves and the only way to
	// exercise the supervisor's abandonment path.
	IgnoreCancellation bool

	calls       atomic.Int32
	compensated atomic.Int32

	mu     sync.Mutex
	seen   []Invocation
	undone []Invocation
}

// Invoke performs the scripted behaviour.
func (f *FakeTool) Invoke(ctx context.Context, in Invocation) (Result, error) {
	n := f.calls.Add(1)

	f.mu.Lock()
	f.seen = append(f.seen, in)
	f.mu.Unlock()

	if f.PanicOnCall > 0 && n == f.PanicOnCall {
		panic("fake tool scripted panic")
	}

	if f.Delay > 0 && f.Clock != nil {
		if f.IgnoreCancellation {
			// Deliberately ignores the context: sleeps on a background context
			// so cancellation cannot reach it.
			_ = f.Clock.Sleep(context.Background(), f.Delay)
		} else if err := f.Clock.Sleep(ctx, f.Delay); err != nil {
			return nil, err
		}
	}

	if !f.IgnoreCancellation {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}

	if f.FailAlways || n <= f.FailTimes {
		err := f.FailWith
		if err == nil {
			err = errors.New("fake tool: scripted transient failure")
		}
		return nil, err
	}

	if f.Produce != nil {
		return f.Produce(in)
	}
	return Result{"answer": String(fmt.Sprintf("ok:%d", n))}, nil
}

// Calls returns how many times Invoke was entered.
func (f *FakeTool) Calls() int32 { return f.calls.Load() }

// Seen returns the invocations received, in order.
func (f *FakeTool) Seen() []Invocation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Invocation(nil), f.seen...)
}

// Reset clears the call history and counters.
func (f *FakeTool) Reset() {
	f.calls.Store(0)
	f.compensated.Store(0)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen, f.undone = nil, nil
}

// WriteFake is a mutating [FakeTool] that produces a reference.
type WriteFake struct{ FakeTool }

// Invoke returns a reference derived from the arguments, so a test can assert
// that compensation received the right one.
func (w *WriteFake) Invoke(ctx context.Context, in Invocation) (Result, error) {
	if w.Produce == nil {
		w.Produce = func(inv Invocation) (Result, error) {
			return Result{"reference": String("ref-" + string(inv.Idempotency))}, nil
		}
	}
	return w.FakeTool.Invoke(ctx, in)
}

// CompensatingFake is a mutating tool that can be rolled back.
type CompensatingFake struct {
	WriteFake

	// CompensateErr makes Compensate fail, for rollback-failure tests.
	CompensateErr error
}

// Compensate records the rollback.
func (c *CompensatingFake) Compensate(ctx context.Context, in Invocation, produced Result) error {
	c.compensated.Add(1)
	c.mu.Lock()
	c.undone = append(c.undone, in)
	c.mu.Unlock()
	return c.CompensateErr
}

// Compensations returns how many rollbacks ran.
func (c *CompensatingFake) Compensations() int32 { return c.compensated.Load() }

// Undone returns the invocations that were rolled back, in rollback order.
func (c *CompensatingFake) Undone() []Invocation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Invocation(nil), c.undone...)
}

// StreamingFake emits partial results before returning.
type StreamingFake struct {
	FakeTool

	// Chunks is how many partial chunks to emit.
	Chunks int
	// ChunkPayload is the field value in each chunk. Sized deliberately in
	// budget tests.
	ChunkPayload string
}

// InvokeStream emits chunks then returns the final result.
func (s *StreamingFake) InvokeStream(ctx context.Context, in Invocation, sink StreamSink) (Result, error) {
	payload := s.ChunkPayload
	if payload == "" {
		payload = "partial"
	}
	for i := 0; i < s.Chunks; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		chunk := Chunk{
			Kind: ChunkPartial, Execution: in.Execution, Step: in.Step,
			Fields:   Result{"answer": String(fmt.Sprintf("%s-%d", payload, i))},
			Progress: float64(i+1) / float64(s.Chunks),
		}
		if err := sink.Write(chunk); err != nil {
			// A sink refusing a chunk does NOT fail the execution: the consumer
			// going away is not the tool's fault and the final result is still
			// worth having. A budget refusal is the exception and surfaces
			// through the metered sink at the executor.
			if errors.Is(err, ErrBudgetExceeded) {
				return nil, err
			}
			break
		}
	}
	return s.FakeTool.Invoke(ctx, in)
}

// BlockingTool blocks until released, for concurrency and cancellation tests.
type BlockingTool struct {
	release chan struct{}
	entered chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

// NewBlockingTool builds a tool that blocks in Invoke until Release is called.
func NewBlockingTool() *BlockingTool {
	return &BlockingTool{release: make(chan struct{}), entered: make(chan struct{}, 256)}
}

// Invoke blocks until released or cancelled.
func (b *BlockingTool) Invoke(ctx context.Context, in Invocation) (Result, error) {
	b.calls.Add(1)
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
		return Result{"answer": String("released")}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Release unblocks every waiting invocation. Safe to call more than once.
func (b *BlockingTool) Release() { b.once.Do(func() { close(b.release) }) }

// WaitEntered blocks until at least one invocation has entered.
func (b *BlockingTool) WaitEntered() { <-b.entered }

// Calls returns how many invocations were entered.
func (b *BlockingTool) Calls() int32 { return b.calls.Load() }

// StaticConsent is a consent map for grants, for permission tests.
func StaticConsent(pairs map[string]string) map[string]string {
	out := make(map[string]string, len(pairs))
	for k, v := range pairs {
		out[k] = v
	}
	return out
}
