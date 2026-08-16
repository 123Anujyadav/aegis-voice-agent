package runtime

import (
	"context"
	"errors"
	"fmt"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Dispatcher — the barge-in guarantee (ADR-0011: one frame, 20 ms)
// ---------------------------------------------------------------------------

// blockingStream never yields a chunk. It is the worst case for cancellation:
// a provider that has accepted the request and gone silent.
type blockingStream struct {
	closed chan struct{}
	once   sync.Once
	recvIn chan struct{} // signalled when Recv is entered
}

func newBlockingStream() *blockingStream {
	return &blockingStream{closed: make(chan struct{}), recvIn: make(chan struct{}, 1)}
}

func (b *blockingStream) Recv() (Chunk, error) {
	select {
	case b.recvIn <- struct{}{}:
	default:
	}
	<-b.closed
	return Chunk{}, ErrClosed
}

func (b *blockingStream) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

// TestDispatcher_AbortPreemptsBlockedProviderRead is the single most important
// test in this package.
//
// It asserts the property the whole dispatcher design exists for: an abort is
// observed while a provider read is BLOCKED, not after it returns. A dispatcher
// that called Recv inline would hang here forever, and barge-in on a stalled
// provider would never work.
//
// Real time is used deliberately. The budget is a wall-clock guarantee to a
// human being interrupting a phone call; asserting it against a fake clock
// would prove only that the code advances a counter.
func TestDispatcher_AbortPreemptsBlockedProviderRead(t *testing.T) {
	t.Parallel()

	d, err := NewDispatcher(DefaultDispatcherConfig(), SystemClock{}, NewMetrics())
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	sink := NewRecordingSink(false)
	if err := d.AddSink(sink); err != nil {
		t.Fatalf("AddSink: %v", err)
	}

	stream := newBlockingStream()
	resultCh := make(chan StreamResult, 1)
	go func() { resultCh <- d.Run(context.Background(), stream) }()

	// Wait until the reader is genuinely blocked inside Recv, so the abort
	// races against a blocked read rather than against setup.
	select {
	case <-stream.recvIn:
	case <-time.After(2 * time.Second):
		t.Fatal("provider Recv was never entered")
	}

	start := time.Now()
	d.Abort()

	select {
	case res := <-resultCh:
		observed := time.Since(start)
		if !res.Aborted {
			t.Fatalf("result should record the abort: %+v", res)
		}
		if !errors.Is(res.Err, ErrAborted) {
			t.Fatalf("err = %v, want ErrAborted", res.Err)
		}
		if observed > DefaultDispatcherConfig().AbortBudget {
			t.Fatalf("ADR-0011: barge-in took %v, budget is %v",
				observed, DefaultDispatcherConfig().AbortBudget)
		}
		t.Logf("abort observed in %v (budget %v, internal measure %v)",
			observed, DefaultDispatcherConfig().AbortBudget, res.AbortLatency)
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher did not stop after abort; a blocked Recv is not preemptible")
	}

	closed, cerr := sink.Closed()
	if !closed {
		t.Fatal("sink must be closed when the stream aborts")
	}
	if !errors.Is(cerr, ErrAborted) {
		t.Fatalf("sink close error = %v, want ErrAborted", cerr)
	}
}

// TestDispatcher_NoGoroutineLeakAfterAbort proves the reader goroutine exits.
//
// The preemption design deliberately orphans an in-flight Recv. If closing the
// stream did not unblock it, every barge-in would leak a goroutine, and on a
// platform whose capacity unit is concurrent sessions that is an outage on a
// long enough timeline.
func TestDispatcher_NoGoroutineLeakAfterAbort(t *testing.T) {
	settle := func() int {
		for i := 0; i < 50; i++ {
			goruntime.Gosched()
			time.Sleep(time.Millisecond)
		}
		goruntime.GC()
		return goruntime.NumGoroutine()
	}

	before := settle()

	for i := 0; i < 50; i++ {
		d, _ := NewDispatcher(DefaultDispatcherConfig(), SystemClock{}, NewMetrics())
		_ = d.AddSink(NewRecordingSink(false))
		stream := newBlockingStream()
		done := make(chan struct{})
		go func() { d.Run(context.Background(), stream); close(done) }()
		<-stream.recvIn
		d.Abort()
		<-done
	}

	after := settle()
	if after > before+5 {
		t.Fatalf("goroutine leak after abort: %d -> %d", before, after)
	}
}

// TestDispatcher_ThinkingNotDeliveredToUnoptedSink asserts INV-AI-10.
func TestDispatcher_ThinkingNotDeliveredToUnoptedSink(t *testing.T) {
	t.Parallel()

	d, _ := NewDispatcher(DefaultDispatcherConfig(), SystemClock{}, NewMetrics())
	plain := NewRecordingSink(false)
	opted := NewRecordingSink(true)
	_ = d.AddSink(plain)
	_ = d.AddSink(opted)

	stream := NewSliceStream(SystemClock{},
		Chunk{Kind: ChunkThinking, Text: "internal reasoning"},
		Chunk{Kind: ChunkText, Text: "visible answer"},
		Chunk{Kind: ChunkDone},
	)

	res := d.Run(context.Background(), stream)
	if res.Err != nil {
		t.Fatalf("Run: %v", res.Err)
	}

	if n := plain.CountOf(ChunkThinking); n != 0 {
		t.Fatalf("INV-AI-10: %d thinking chunk(s) reached a sink that did not opt in", n)
	}
	if plain.Text() != "visible answer" {
		t.Fatalf("plain sink text = %q", plain.Text())
	}
	if n := opted.CountOf(ChunkThinking); n != 1 {
		t.Fatalf("an opted-in sink should receive thinking, got %d", n)
	}
}

// TestDispatcher_SlowSinkIsDetachedNotBlocking proves one slow consumer cannot
// hold the call open for everyone else.
func TestDispatcher_SlowSinkIsDetachedNotBlocking(t *testing.T) {
	t.Parallel()

	cfg := DefaultDispatcherConfig()
	cfg.SinkWriteTimeout = 20 * time.Millisecond
	d, _ := NewDispatcher(cfg, SystemClock{}, NewMetrics())

	release := make(chan struct{})
	slow := &FuncSink{OnChunk: func(Chunk) error {
		<-release
		return nil
	}}
	fast := NewRecordingSink(false)
	_ = d.AddSink(slow)
	_ = d.AddSink(fast)

	stream := NewSliceStream(SystemClock{},
		Chunk{Kind: ChunkText, Text: "a"},
		Chunk{Kind: ChunkText, Text: "b"},
		Chunk{Kind: ChunkDone},
	)

	start := time.Now()
	res := d.Run(context.Background(), stream)
	elapsed := time.Since(start)
	close(release)

	if res.Err != nil {
		t.Fatalf("a slow sink must not fail the stream: %v", res.Err)
	}
	if fast.Text() != "ab" {
		t.Fatalf("the fast sink should receive everything, got %q", fast.Text())
	}
	// One timeout for the slow sink, then detached: total well under two.
	if elapsed > 200*time.Millisecond {
		t.Fatalf("slow sink held the stream for %v; it should be detached after one timeout", elapsed)
	}
}

// TestDispatcher_DetectsStalledStream proves a provider that goes silent
// without closing is failed rather than allowed to consume the whole budget.
func TestDispatcher_DetectsStalledStream(t *testing.T) {
	t.Parallel()

	clock := NewFakeClock(time.Time{})
	cfg := DefaultDispatcherConfig()
	cfg.MaxChunkGap = 500 * time.Millisecond
	d, _ := NewDispatcher(cfg, clock, NewMetrics())
	_ = d.AddSink(NewRecordingSink(false))

	stream := newBlockingStream()
	defer stream.Close()

	resultCh := make(chan StreamResult, 1)
	go func() { resultCh <- d.Run(context.Background(), stream) }()

	<-stream.recvIn
	clock.BlockUntil(1) // the gap timer is registered
	clock.Advance(600 * time.Millisecond)

	select {
	case res := <-resultCh:
		if !errors.Is(res.Err, ErrBudgetExceeded) {
			t.Fatalf("a stalled stream should exceed its budget, got %v", res.Err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stall was not detected")
	}
}

// ---------------------------------------------------------------------------
// Kernel — end to end
// ---------------------------------------------------------------------------

func newStartedHarness(t *testing.T) *Harness {
	t.Helper()
	h, err := NewHarness()
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.Stop(ctx)
	})
	return h
}

func TestKernel_GenerateEndToEnd(t *testing.T) {
	t.Parallel()
	h := newStartedHarness(t)

	session, err := h.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	h.Provider.Script(
		Chunk{Kind: ChunkText, Text: "hello "},
		Chunk{Kind: ChunkText, Text: "world"},
		Chunk{Kind: ChunkUsage, Usage: Usage{InputTokens: 12, OutputTokens: 2}},
		Chunk{Kind: ChunkDone},
	)

	sink := NewRecordingSink(false)
	d, err := h.Kernel.Generate(context.Background(), GenerateSpec{
		SessionID: session.ID(),
		Tier:      TierFast,
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
		Class:     ClassInteractive,
	}, sink)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	res := d.Result()
	if res.Err != nil {
		t.Fatalf("stream error: %v", res.Err)
	}
	if sink.Text() != "hello world" {
		t.Fatalf("text = %q", sink.Text())
	}
	if res.Usage.OutputTokens != 2 {
		t.Fatalf("usage not accumulated: %+v", res.Usage)
	}
	if session.Usage().InputTokens != 12 {
		t.Fatalf("session usage not accumulated: %+v", session.Usage())
	}
	if got := h.Kernel.Scheduler().InFlight(); got != 0 {
		t.Fatalf("scheduler slot leaked: %d in flight after completion", got)
	}
}

func TestKernel_GenerateReleasesSlotOnFailure(t *testing.T) {
	t.Parallel()
	h := newStartedHarness(t)

	// A session that does not exist: fails after admission, before streaming.
	_, err := h.Kernel.Generate(context.Background(), GenerateSpec{
		SessionID: SessionID("ses_missing"),
		Tier:      TierFast,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if got := h.Kernel.Scheduler().InFlight(); got != 0 {
		t.Fatalf("a failure path leaked a scheduler slot: %d in flight", got)
	}
}

func TestKernel_I3_ThinkingForcedOnToolCallingTier(t *testing.T) {
	t.Parallel()
	h := newStartedHarness(t)

	session, _ := h.NewSession()
	// TierBalanced is registered by the harness with tool calling enabled.
	d, err := h.Kernel.Generate(context.Background(), GenerateSpec{
		SessionID: session.ID(),
		Tier:      TierBalanced,
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	<-d.Done()

	req := h.Provider.LastRequest()
	if req == nil {
		t.Fatal("provider was never called")
	}
	if !req.Thinking {
		t.Fatal("I3: the runtime must force thinking on for a tool-calling model")
	}
}

func TestKernel_I3_RefusesExplicitDisableEndToEnd(t *testing.T) {
	t.Parallel()
	h := newStartedHarness(t)
	session, _ := h.NewSession()

	_, err := h.Kernel.Generate(context.Background(), GenerateSpec{
		SessionID:             session.ID(),
		Tier:                  TierBalanced,
		Messages:              []Message{{Role: RoleUser, Content: "hi"}},
		Thinking:              false,
		ThinkingExplicitlySet: true,
	})
	if !errors.Is(err, ErrInvariantViolation) {
		t.Fatalf("I3 must be enforced end to end, got %v", err)
	}
	if got := h.Kernel.Scheduler().InFlight(); got != 0 {
		t.Fatalf("invariant refusal leaked a slot: %d", got)
	}
}

func TestKernel_RetriesThenSucceeds(t *testing.T) {
	t.Parallel()

	// Zero backoff deliberately. The retry path sleeps on the kernel's clock,
	// which is the FakeClock here, and nothing in this test advances it — a
	// non-zero backoff would block forever. Backoff scheduling is covered by
	// TestRetryPolicy_BackoffGrowsAndIsBounded, which tests the arithmetic
	// directly rather than through a sleep.
	cfg := DefaultConfig("retry-test", "0")
	cfg.Retry.InitialBackoff = 0
	cfg.Retry.MaxBackoff = 0
	cfg.Scheduler.MaxConcurrent = 8
	cfg.Scheduler.MaxQueued = 8
	cfg.Session.Shards = 2

	h, err := NewHarness(WithHarnessConfig(cfg))
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.Stop(ctx)
	}()

	session, _ := h.NewSession()

	h.Provider.FailNext(1, &ProviderError{
		Provider: "fake", Kind: KindTransport, Err: errors.New("connection reset"),
	})

	sink := NewRecordingSink(false)
	d, err := h.Kernel.Generate(context.Background(), GenerateSpec{
		SessionID: session.ID(),
		Tier:      TierFast,
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
	}, sink)
	if err != nil {
		t.Fatalf("a transient failure should be retried, got %v", err)
	}
	if res := d.Result(); res.Err != nil {
		t.Fatalf("stream error: %v", res.Err)
	}
	if h.Provider.Calls() < 2 {
		t.Fatalf("expected a retry, provider called %d time(s)", h.Provider.Calls())
	}
}

func TestRetryPolicy_BackoffGrowsAndIsBounded(t *testing.T) {
	t.Parallel()
	p := DefaultRetryPolicy()

	if got := p.Backoff(1, nil); got != 0 {
		t.Fatalf("the first attempt has no backoff, got %v", got)
	}
	second := p.Backoff(2, nil)
	third := p.Backoff(3, nil)
	if second <= 0 || third <= second {
		t.Fatalf("backoff must grow: %v then %v", second, third)
	}
	if got := p.Backoff(20, nil); got > p.MaxBackoff {
		t.Fatalf("backoff must be capped at MaxBackoff, got %v", got)
	}
}

func TestRetryPolicy_RefusesWhenBudgetCannotFit(t *testing.T) {
	t.Parallel()
	p := DefaultRetryPolicy()
	err := &ProviderError{Provider: "p", Kind: KindTransport, Err: errors.New("x")}

	if p.ShouldRetry(1, err, 40*time.Millisecond, 400*time.Millisecond) {
		t.Fatal("retrying with 40ms left against a 400ms call converts one failure into two, later")
	}
	if !p.ShouldRetry(1, err, 2*time.Second, 100*time.Millisecond) {
		t.Fatal("a retry that fits the budget should be permitted")
	}
}

func TestKernel_DoesNotRetryInvalidRequest(t *testing.T) {
	t.Parallel()
	h := newStartedHarness(t)
	session, _ := h.NewSession()

	h.Provider.FailNext(10, &ProviderError{
		Provider: "fake", Kind: KindInvalidRequest, Err: errors.New("bad request"),
	})

	_, err := h.Kernel.Generate(context.Background(), GenerateSpec{
		SessionID: session.ID(),
		Tier:      TierFast,
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected failure")
	}
	// One attempt per model in the tier, no retries: the fault is ours and
	// retrying would blame the provider for our bug.
	if h.Provider.Calls() > 2 {
		t.Fatalf("an invalid request must not be retried; provider called %d times", h.Provider.Calls())
	}
}

func TestKernel_ShedIsNotAnError(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig("shed-test", "0")
	cfg.Scheduler.MaxConcurrent = 2
	cfg.Scheduler.MaxQueued = 1
	cfg.Scheduler.SheddingThreshold = 0.5
	cfg.Scheduler.QueueTimeout = time.Millisecond

	h, err := NewHarness(WithHarnessConfig(cfg))
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = h.Stop(ctx)
	}()

	// Occupy the single admissible slot with a stream that will not finish.
	release, d := h.Kernel.Scheduler().Admit(context.Background(), ClassStandard, time.Time{})
	if !d.Admitted {
		t.Fatal("first admission should succeed")
	}
	defer release()

	session, _ := h.NewSession()
	_, err = h.Kernel.Generate(context.Background(), GenerateSpec{
		SessionID: session.ID(),
		Tier:      TierFast,
		Class:     ClassStandard,
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
	})
	if !errors.Is(err, ErrShed) {
		t.Fatalf("expected ErrShed, got %v", err)
	}
	// ErrShed must be distinguishable from a real failure: the caller's
	// correct response is to let the call ring through, not to alert.
	if errors.Is(err, ErrProviderUnavailable) {
		t.Fatal("a shed must not look like a provider failure")
	}
}

func TestKernel_AbortDuringGeneration(t *testing.T) {
	t.Parallel()
	h := newStartedHarness(t)
	session, _ := h.NewSession()

	// A stream that pauses between chunks on the fake clock, so it is still
	// open when we abort.
	h.Provider.SetChunkDelay(h.Clock, time.Second)
	h.Provider.Script(
		Chunk{Kind: ChunkText, Text: "one"},
		Chunk{Kind: ChunkText, Text: "two"},
		Chunk{Kind: ChunkDone},
	)

	sink := NewRecordingSink(false)
	d, err := h.Kernel.Generate(context.Background(), GenerateSpec{
		SessionID: session.ID(),
		Tier:      TierFast,
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
	}, sink)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	d.Abort()
	res := d.Result()

	if !res.Aborted {
		t.Fatalf("expected an aborted result, got %+v", res)
	}
	if got := h.Kernel.Scheduler().InFlight(); got != 0 {
		t.Fatalf("abort leaked a scheduler slot: %d", got)
	}
	if session.InFlight() != 0 {
		t.Fatalf("abort leaked a session request: %d", session.InFlight())
	}
}

func TestKernel_StopDrains(t *testing.T) {
	t.Parallel()
	h, err := NewHarness()
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	session, _ := h.NewSession()
	sink := NewRecordingSink(false)
	d, err := h.Kernel.Generate(context.Background(), GenerateSpec{
		SessionID: session.ID(),
		Tier:      TierFast,
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
	}, sink)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	<-d.Done()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.Stop(ctx); err != nil {
		t.Fatalf("Stop should drain cleanly: %v", err)
	}
	if h.Kernel.Ready() {
		t.Fatal("a stopped runtime must not report ready")
	}

	// After stop, new work is refused rather than silently accepted.
	_, err = h.Kernel.Generate(context.Background(), GenerateSpec{
		SessionID: session.ID(), Tier: TierFast,
	})
	if err == nil {
		t.Fatal("a drained runtime must refuse new work")
	}
}

func TestKernel_ConcurrentGenerationsAreIsolated(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig("concurrency-test", "0")
	cfg.Scheduler.MaxConcurrent = 64
	cfg.Scheduler.MaxQueued = 64
	cfg.Session.Shards = 8

	h, err := NewHarness(WithHarnessConfig(cfg))
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.Stop(ctx)
	}()

	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			session, err := h.NewSession()
			if err != nil {
				errs <- fmt.Errorf("session %d: %w", i, err)
				return
			}
			sink := NewRecordingSink(false)
			d, err := h.Kernel.Generate(context.Background(), GenerateSpec{
				SessionID: session.ID(),
				Tier:      TierFast,
				Messages:  []Message{{Role: RoleUser, Content: strings.Repeat("x", 20)}},
			}, sink)
			if err != nil {
				errs <- fmt.Errorf("generate %d: %w", i, err)
				return
			}
			if res := d.Result(); res.Err != nil {
				errs <- fmt.Errorf("stream %d: %w", i, res.Err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("%v", err)
	}
	if got := h.Kernel.Scheduler().InFlight(); got != 0 {
		t.Fatalf("slots leaked under concurrency: %d", got)
	}
}

func TestKernel_HealthReflectsBreakerState(t *testing.T) {
	t.Parallel()
	h := newStartedHarness(t)

	health := h.Kernel.Health()
	if !health.Ready {
		t.Fatal("a started runtime should be ready")
	}
	if state, ok := health.Providers["fake"]; !ok || state != "closed" {
		t.Fatalf("expected a closed breaker for the fake provider, got %q", state)
	}
}
