package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// FSM
// ---------------------------------------------------------------------------

type tState int

const (
	sA tState = iota
	sB
	sC
	sEnd
)

func testFSM(t *testing.T) *FSM[tState] {
	t.Helper()
	f, err := NewFSM(FSMSpec[tState]{
		Initial: sA,
		Transitions: map[tState][]tState{
			sA: {sB, sEnd},
			sB: {sC, sEnd},
			sC: {sEnd},
		},
		Terminal: []tState{sEnd},
	}, NewFakeClock(time.Time{}))
	if err != nil {
		t.Fatalf("NewFSM: %v", err)
	}
	return f
}

func TestFSM_RefusesUndeclaredTransition(t *testing.T) {
	t.Parallel()
	f := testFSM(t)

	if _, err := f.To(sC); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("A->C should be refused, got %v", err)
	}
	if f.State() != sA {
		t.Fatalf("state changed on a refused transition: %v", f.State())
	}
}

func TestFSM_RefusesExitFromTerminal(t *testing.T) {
	t.Parallel()
	f := testFSM(t)

	if _, err := f.To(sEnd); err != nil {
		t.Fatalf("A->End: %v", err)
	}
	if !f.IsTerminal() {
		t.Fatal("End should be terminal")
	}
	if _, err := f.To(sB); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("exit from terminal should be refused, got %v", err)
	}
}

func TestFSM_GuardRefusalPropagates(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("guard says no")
	f, err := NewFSM(FSMSpec[tState]{
		Initial:     sA,
		Transitions: map[tState][]tState{sA: {sB}},
		Terminal:    []tState{sB},
		Guards: map[tState]map[tState]Guard[tState]{
			sA: {sB: func(tState, tState) error { return sentinel }},
		},
	}, nil)
	if err != nil {
		t.Fatalf("NewFSM: %v", err)
	}
	if _, err := f.To(sB); !errors.Is(err, sentinel) {
		t.Fatalf("guard error should propagate unchanged, got %v", err)
	}
}

func TestFSM_ConcurrentTransitionsElectOneWinner(t *testing.T) {
	t.Parallel()
	f := testFSM(t)

	const racers = 64
	var wg sync.WaitGroup
	won := make(chan struct{}, racers)

	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			if f.TryTo(sB) {
				won <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(won)

	if n := len(won); n != 1 {
		t.Fatalf("exactly one goroutine should win the transition, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// Circuit breaker
// ---------------------------------------------------------------------------

func TestBreaker_OpensAtThreshold(t *testing.T) {
	t.Parallel()
	clock := NewFakeClock(time.Time{})
	b, err := NewBreaker("p", BreakerConfig{
		FailureThreshold: 3, MinimumRequests: 3, Window: time.Second,
		Cooldown: time.Second, HalfOpenProbes: 1, SuccessesToClose: 1,
	}, clock)
	if err != nil {
		t.Fatalf("NewBreaker: %v", err)
	}

	boom := &ProviderError{Provider: "p", Kind: KindTransport, Err: errors.New("x")}
	for i := 0; i < 3; i++ {
		allowed, report := b.Allow()
		if !allowed {
			t.Fatalf("call %d should be allowed while closed", i)
		}
		report(boom)
	}

	if got := b.State(); got != BreakerOpen {
		t.Fatalf("breaker should be open after reaching threshold, got %v", got)
	}
	if allowed, _ := b.Allow(); allowed {
		t.Fatal("an open breaker must refuse")
	}
}

func TestBreaker_RateLimitDoesNotOpen(t *testing.T) {
	t.Parallel()
	clock := NewFakeClock(time.Time{})
	b, _ := NewBreaker("p", BreakerConfig{
		FailureThreshold: 2, MinimumRequests: 2, Window: time.Second,
		Cooldown: time.Second, HalfOpenProbes: 1, SuccessesToClose: 1,
	}, clock)

	throttled := &ProviderError{Provider: "p", Kind: KindRateLimited, Err: errors.New("429")}
	for i := 0; i < 20; i++ {
		allowed, report := b.Allow()
		if !allowed {
			t.Fatalf("rate limiting must never open the breaker; refused at call %d", i)
		}
		report(throttled)
	}
	if got := b.State(); got != BreakerClosed {
		t.Fatalf("rate limiting says we are asking too fast, not that the provider is broken; state %v", got)
	}
}

func TestBreaker_HalfOpenRecovers(t *testing.T) {
	t.Parallel()
	clock := NewFakeClock(time.Time{})
	b, _ := NewBreaker("p", BreakerConfig{
		FailureThreshold: 2, MinimumRequests: 2, Window: time.Minute,
		Cooldown: 5 * time.Second, HalfOpenProbes: 2, SuccessesToClose: 2,
	}, clock)

	boom := &ProviderError{Provider: "p", Kind: KindTransport, Err: errors.New("x")}
	for i := 0; i < 2; i++ {
		_, report := b.Allow()
		report(boom)
	}
	if b.State() != BreakerOpen {
		t.Fatal("expected open")
	}

	clock.Advance(5 * time.Second)
	if got := b.State(); got != BreakerHalfOpen {
		t.Fatalf("cooldown should move the breaker to half-open, got %v", got)
	}

	for i := 0; i < 2; i++ {
		allowed, report := b.Allow()
		if !allowed {
			t.Fatalf("half-open should admit probe %d", i)
		}
		report(nil)
	}
	if got := b.State(); got != BreakerClosed {
		t.Fatalf("consecutive successes should close the breaker, got %v", got)
	}
}

func TestBreaker_HalfOpenLimitsConcurrentProbes(t *testing.T) {
	t.Parallel()
	clock := NewFakeClock(time.Time{})
	b, _ := NewBreaker("p", BreakerConfig{
		FailureThreshold: 1, MinimumRequests: 1, Window: time.Minute,
		Cooldown: time.Second, HalfOpenProbes: 1, SuccessesToClose: 5,
	}, clock)

	_, report := b.Allow()
	report(&ProviderError{Provider: "p", Kind: KindTransport, Err: errors.New("x")})
	clock.Advance(time.Second)

	if allowed, _ := b.Allow(); !allowed {
		t.Fatal("first probe should be admitted")
	}
	if allowed, _ := b.Allow(); allowed {
		t.Fatal("second concurrent probe must be refused; HalfOpenProbes is a limit, not a hint")
	}
}

// ---------------------------------------------------------------------------
// Scheduler — Invariant I11
// ---------------------------------------------------------------------------

func TestScheduler_SafetyClassIsNeverShed(t *testing.T) {
	t.Parallel()
	clock := NewFakeClock(time.Time{})
	s, err := NewScheduler(SchedulerConfig{
		MaxConcurrent: 4, MaxQueued: 1,
		QueueTimeout: 10 * time.Millisecond, SheddingThreshold: 0.5,
	}, clock, NewMetrics())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	// Saturate well beyond capacity.
	var releases []func()
	for i := 0; i < 8; i++ {
		release, d := s.Admit(context.Background(), ClassSafety, time.Time{})
		if !d.Admitted {
			t.Fatalf("I11: ClassSafety was shed at %d in flight with reason %q; "+
				"safety work must never be shed under load", s.InFlight(), d.Reason)
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, r := range releases {
			r()
		}
	}()

	if s.InFlight() != 8 {
		t.Fatalf("expected 8 safety tasks in flight, got %d", s.InFlight())
	}
	if s.Utilisation() <= 1.0 {
		t.Fatalf("utilisation should record the overshoot, got %v", s.Utilisation())
	}

	// Ordinary work must now be refused — the whole point of shedding.
	_, d := s.Admit(context.Background(), ClassStandard, time.Time{})
	if d.Admitted {
		t.Fatal("ClassStandard should be shed while saturated")
	}
	if d.Reason != ShedCapacity {
		t.Fatalf("expected capacity shed, got %q", d.Reason)
	}
}

func TestScheduler_ShedsStandardAboveThreshold(t *testing.T) {
	t.Parallel()
	s, _ := NewScheduler(SchedulerConfig{
		MaxConcurrent: 10, MaxQueued: 2,
		QueueTimeout: 5 * time.Millisecond, SheddingThreshold: 0.8,
	}, NewFakeClock(time.Time{}), NewMetrics())

	var releases []func()
	for i := 0; i < 8; i++ {
		release, d := s.Admit(context.Background(), ClassStandard, time.Time{})
		if !d.Admitted {
			t.Fatalf("admission %d should succeed below threshold", i)
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, r := range releases {
			r()
		}
	}()

	if _, d := s.Admit(context.Background(), ClassStandard, time.Time{}); d.Admitted {
		t.Fatal("should shed at the 80% threshold")
	}
}

func TestScheduler_RefusesExpiredDeadline(t *testing.T) {
	t.Parallel()
	clock := NewFakeClock(time.Time{})
	s, _ := NewScheduler(DefaultSchedulerConfig(), clock, NewMetrics())

	past := clock.Now().Add(-time.Second)
	_, d := s.Admit(context.Background(), ClassStandard, past)
	if d.Admitted {
		t.Fatal("work that cannot finish must not consume a slot")
	}
	if d.Reason != ShedDeadline {
		t.Fatalf("expected deadline shed, got %q", d.Reason)
	}
}

func TestScheduler_ReleaseIsIdempotent(t *testing.T) {
	t.Parallel()
	s, _ := NewScheduler(DefaultSchedulerConfig(), NewFakeClock(time.Time{}), NewMetrics())

	release, d := s.Admit(context.Background(), ClassStandard, time.Time{})
	if !d.Admitted {
		t.Fatal("expected admission")
	}
	release()
	release()
	release()

	if got := s.InFlight(); got != 0 {
		t.Fatalf("double release corrupted the in-flight count: %d", got)
	}
}

// ---------------------------------------------------------------------------
// Model registry — Invariant I3
// ---------------------------------------------------------------------------

func TestModelRegistry_I3_RejectsToolCallingWithoutThinking(t *testing.T) {
	t.Parallel()
	r := NewModelRegistry()

	err := r.Register(ModelSpec{
		ID: "bad", Provider: "p", Tier: TierBalanced,
		MaxContextTokens: 1000, MaxOutputTokens: 100,
		SupportsToolCalling: true, SupportsThinking: false,
		Enabled: true,
	})
	if err == nil {
		t.Fatal("I3: a tool-calling model without thinking must be refused at registration")
	}
	if !strings.Contains(err.Error(), "I3") {
		t.Fatalf("the error should name the invariant it enforces, got: %v", err)
	}
}

func TestModelRegistry_I3_RefusesExplicitThinkingDisable(t *testing.T) {
	t.Parallel()
	r := NewModelRegistry()
	model := ModelSpec{
		ID: "tools", Provider: "p", Tier: TierBalanced,
		MaxContextTokens: 1000, MaxOutputTokens: 100, DefaultMaxOutputTokens: 50,
		SupportsToolCalling: true, SupportsThinking: true, Enabled: true,
	}
	if err := r.Register(model); err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, err := r.BuildRequest(GenerateSpec{
		SessionID: "s", Tier: TierBalanced,
		Thinking: false, ThinkingExplicitlySet: true,
	}, model)
	if !errors.Is(err, ErrInvariantViolation) {
		t.Fatalf("I3: explicitly disabling thinking on a tool-calling model must be refused, got %v", err)
	}
}

func TestModelRegistry_I3_ForcesThinkingByDefault(t *testing.T) {
	t.Parallel()
	r := NewModelRegistry()
	model := ModelSpec{
		ID: "tools", Provider: "p", Tier: TierBalanced,
		MaxContextTokens: 1000, MaxOutputTokens: 100, DefaultMaxOutputTokens: 50,
		SupportsToolCalling: true, SupportsThinking: true, Enabled: true,
	}
	_ = r.Register(model)

	req, err := r.BuildRequest(GenerateSpec{SessionID: "s", Tier: TierBalanced}, model)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if !req.Thinking {
		t.Fatal("I3: thinking must be forced on for a tool-calling model")
	}
}

func TestModelRegistry_ResolvesByPriorityDeterministically(t *testing.T) {
	t.Parallel()
	r := NewModelRegistry()
	base := ModelSpec{
		Provider: "p", Tier: TierFast, MaxContextTokens: 1000,
		MaxOutputTokens: 100, DefaultMaxOutputTokens: 50, Enabled: true,
	}
	for _, m := range []struct {
		id       ModelID
		priority int
	}{{"zebra", 1}, {"alpha", 1}, {"first", 0}} {
		spec := base
		spec.ID = m.id
		spec.Priority = m.priority
		if err := r.Register(spec); err != nil {
			t.Fatalf("Register %s: %v", m.id, err)
		}
	}

	got, err := r.ResolveTier(TierFast)
	if err != nil {
		t.Fatalf("ResolveTier: %v", err)
	}
	if got.ID != "first" {
		t.Fatalf("lowest priority should win, got %s", got.ID)
	}

	// Equal priority must break by ID, not by map order.
	next, err := r.ResolveTier(TierFast, "first")
	if err != nil {
		t.Fatalf("ResolveTier avoid: %v", err)
	}
	if next.ID != "alpha" {
		t.Fatalf("equal priority must tie-break by ID for determinism, got %s", next.ID)
	}
}

func TestModelRegistry_RejectsFallbackCycle(t *testing.T) {
	t.Parallel()
	r := NewModelRegistry()
	base := ModelSpec{
		Provider: "p", Tier: TierFast, MaxContextTokens: 1000,
		MaxOutputTokens: 100, Enabled: true,
	}
	for _, id := range []ModelID{"a", "b", "c"} {
		spec := base
		spec.ID = id
		_ = r.Register(spec)
	}
	if err := r.SetFallback("a", "b"); err != nil {
		t.Fatalf("a->b: %v", err)
	}
	if err := r.SetFallback("b", "c"); err != nil {
		t.Fatalf("b->c: %v", err)
	}
	if err := r.SetFallback("c", "a"); err == nil {
		t.Fatal("a fallback cycle is a hang, and a hang on the screening path is a dropped call")
	}
}

func TestModelTier_LadderIsMonotonicAndBounded(t *testing.T) {
	t.Parallel()
	if TierNone.Escalate() != TierFast {
		t.Fatal("none should escalate to fast")
	}
	if TierDeep.Escalate() != TierDeep {
		t.Fatal("deep is the top and must not escalate past itself")
	}
	if TierNone.Downgrade() != TierNone {
		t.Fatal("none is the bottom")
	}
	if TierBalanced.Escalate() != TierDeep {
		t.Fatal("escalation must be single-step")
	}
}

// ---------------------------------------------------------------------------
// Context window
// ---------------------------------------------------------------------------

func TestContextWindow_EvictsOldestUnpinned(t *testing.T) {
	t.Parallel()
	c := NewContextWindow(100, NewMetrics())

	if err := c.Append(Message{Role: RoleSystem, Content: strings.Repeat("a", 100), Pinned: true}); err != nil {
		t.Fatalf("pinned append: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := c.Append(Message{Role: RoleUser, Content: strings.Repeat("b", 60)}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	msgs := c.Messages()
	if len(msgs) == 0 || !msgs[0].Pinned {
		t.Fatal("the pinned message must survive eviction")
	}
	if c.Used() > c.Budget() {
		t.Fatalf("window is over budget after eviction: %d > %d", c.Used(), c.Budget())
	}
	if c.EvictedCount() == 0 {
		t.Fatal("eviction should be counted so an undersized budget is visible")
	}
}

func TestContextWindow_RefusesOversizedMessage(t *testing.T) {
	t.Parallel()
	c := NewContextWindow(50, NewMetrics())

	err := c.Append(Message{Role: RoleUser, Content: strings.Repeat("x", 10_000)})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("a message larger than the whole budget must be refused, got %v", err)
	}
	if c.Len() != 0 {
		t.Fatal("a refused append must leave no trace")
	}
}

func TestContextWindow_EvictNoneFailsInsteadOfDropping(t *testing.T) {
	t.Parallel()
	c := NewContextWindow(60, NewMetrics())
	c.SetPolicy(EvictNone)

	_ = c.Append(Message{Role: RoleUser, Content: strings.Repeat("a", 100)})
	err := c.Append(Message{Role: RoleUser, Content: strings.Repeat("b", 100)})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("EvictNone must fail rather than silently drop context, got %v", err)
	}
}

func TestContextWindow_AssembleDoesNotMutate(t *testing.T) {
	t.Parallel()
	c := NewContextWindow(10_000, NewMetrics())
	for i := 0; i < 10; i++ {
		_ = c.Append(Message{Role: RoleUser, Content: strings.Repeat("x", 100)})
	}
	before := c.Len()

	if _, err := c.Assemble(50); err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if c.Len() != before {
		t.Fatalf("Assemble must not mutate the window: %d -> %d", before, c.Len())
	}
}

func TestContextWindow_ConcurrentAppendIsSafe(t *testing.T) {
	t.Parallel()
	c := NewContextWindow(1_000_000, NewMetrics())

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = c.Append(Message{Role: RoleUser, Content: "hello"})
			}
		}()
	}
	wg.Wait()

	if c.Len() != 32*50 {
		t.Fatalf("lost messages under concurrency: got %d, want %d", c.Len(), 32*50)
	}
}

func TestHeuristicTokenCounter_OverCountsIndicScripts(t *testing.T) {
	t.Parallel()
	tc := NewHeuristicTokenCounter()

	latin := tc.Count(strings.Repeat("hello world ", 10))
	devanagari := tc.Count(strings.Repeat("नमस्ते दुनिया ", 10))

	if devanagari <= latin {
		t.Fatalf("Indic text must not be under-counted (the market we serve first): "+
			"devanagari=%d latin=%d", devanagari, latin)
	}
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

func TestSessionManager_ExpiresIdleSessions(t *testing.T) {
	t.Parallel()
	clock := NewFakeClock(time.Time{})
	m, err := NewSessionManager(SessionConfig{
		MaxSessions: 10, IdleTTL: 30 * time.Second, MaxLifetime: time.Minute,
		SweepInterval: time.Second, Shards: 2, DefaultContextTokens: 1000,
	}, clock, NewMetrics())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	s, err := m.Create(0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = s.Activate()

	clock.Advance(31 * time.Second)
	m.sweep() // called directly: deterministic, no sweeper goroutine to race

	if m.Count() != 0 {
		t.Fatalf("idle session should have expired, count=%d", m.Count())
	}
	if _, err := m.Get(s.ID()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session should be gone, got %v", err)
	}
}

func TestSessionManager_NeverReapsSessionWithWorkInFlight(t *testing.T) {
	t.Parallel()
	clock := NewFakeClock(time.Time{})
	m, _ := NewSessionManager(SessionConfig{
		MaxSessions: 10, IdleTTL: time.Second, MaxLifetime: 2 * time.Second,
		SweepInterval: time.Second, Shards: 2, DefaultContextTokens: 1000,
	}, clock, NewMetrics())

	s, _ := m.Create(0)
	_ = s.Activate()
	done, err := s.BeginRequest()
	if err != nil {
		t.Fatalf("BeginRequest: %v", err)
	}

	clock.Advance(10 * time.Second)
	m.sweep()

	if m.Count() != 1 {
		t.Fatal("reaping a session mid-request would abandon a live call")
	}

	done(Usage{})
	clock.Advance(10 * time.Second)
	m.sweep()

	if m.Count() != 0 {
		t.Fatal("session should be reaped once work completes")
	}
}

func TestSession_RefusesRequestsWhenNotActive(t *testing.T) {
	t.Parallel()
	clock := NewFakeClock(time.Time{})
	m, _ := NewSessionManager(DefaultSessionConfig(), clock, NewMetrics())

	s, _ := m.Create(0)
	if _, err := s.BeginRequest(); err == nil {
		t.Fatal("an initialising session must not accept requests")
	}

	_ = s.Activate()
	if _, err := s.BeginRequest(); err != nil {
		t.Fatalf("an active session should accept requests: %v", err)
	}
}

func TestSessionManager_ShedsAtCapacity(t *testing.T) {
	t.Parallel()
	m, _ := NewSessionManager(SessionConfig{
		MaxSessions: 2, IdleTTL: time.Minute, MaxLifetime: time.Hour,
		SweepInterval: time.Second, Shards: 1, DefaultContextTokens: 100,
	}, NewFakeClock(time.Time{}), NewMetrics())

	for i := 0; i < 2; i++ {
		if _, err := m.Create(0); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if _, err := m.Create(0); !errors.Is(err, ErrShed) {
		t.Fatalf("over the session cap the runtime must shed, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

func TestMetrics_CounterIsLabelPartitioned(t *testing.T) {
	t.Parallel()
	m := NewMetrics()

	m.SchedulerShed.Inc("standard", "capacity")
	m.SchedulerShed.Inc("standard", "capacity")
	m.SchedulerShed.Inc("safety", "capacity")

	if got := m.SchedulerShed.Value("standard", "capacity"); got != 2 {
		t.Fatalf("standard/capacity = %d, want 2", got)
	}
	if got := m.SchedulerShed.Total(); got != 3 {
		t.Fatalf("total = %d, want 3", got)
	}
}

func TestMetrics_HistogramQuantileTracksDistribution(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	for i := 0; i < 100; i++ {
		m.StreamDuration.Observe(0.1)
	}
	m.StreamDuration.Observe(8.0)

	p50 := m.StreamDuration.Quantile(0.5)
	if p50 > 0.2 {
		t.Fatalf("p50 should sit near the mass of observations, got %v", p50)
	}
	if m.StreamDuration.Count() != 101 {
		t.Fatalf("count = %d, want 101", m.StreamDuration.Count())
	}
}

func TestMetrics_SnapshotIsStablyOrdered(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.SchedulerShed.Inc("a", "b")
	m.SessionsActive.Set(3)
	m.StreamDuration.Observe(0.5)

	first := m.Snapshot()
	second := m.Snapshot()
	if len(first) != len(second) {
		t.Fatalf("snapshot length unstable: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Name != second[i].Name {
			t.Fatalf("snapshot order unstable at %d: %s vs %s", i, first[i].Name, second[i].Name)
		}
	}
}

// ---------------------------------------------------------------------------
// Clock
// ---------------------------------------------------------------------------

func TestFakeClock_FiresTimersInDeadlineOrder(t *testing.T) {
	t.Parallel()
	c := NewFakeClock(time.Time{})

	t1 := c.NewTimer(10 * time.Millisecond)
	t2 := c.NewTimer(5 * time.Millisecond)

	c.Advance(20 * time.Millisecond)

	select {
	case <-t2.C():
	default:
		t.Fatal("the earlier timer must have fired")
	}
	select {
	case <-t1.C():
	default:
		t.Fatal("the later timer must have fired")
	}
}

func TestFakeClock_TickerRepeats(t *testing.T) {
	t.Parallel()
	c := NewFakeClock(time.Time{})
	tk := c.NewTicker(time.Second)
	defer tk.Stop()

	c.Advance(time.Second)
	select {
	case <-tk.C():
	default:
		t.Fatal("ticker should have fired")
	}

	c.Advance(time.Second)
	select {
	case <-tk.C():
	default:
		t.Fatal("ticker should fire repeatedly")
	}
}

// ---------------------------------------------------------------------------
// Prompt registry
// ---------------------------------------------------------------------------

func TestPromptRegistry_RefusesActivationWithoutEvaluation(t *testing.T) {
	t.Parallel()
	r := NewPromptRegistry(NewFakeClock(time.Time{}))

	if err := r.Publish(PromptRecord{ID: "p", Version: 1, Body: "hello", Tier: TierFast}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	err := r.Activate("p", 1)
	if !errors.Is(err, ErrInvariantViolation) {
		t.Fatalf("INV-AI-12: activation without a passing evaluation must be refused, got %v", err)
	}
}

func TestPromptRegistry_VersionsAreImmutable(t *testing.T) {
	t.Parallel()
	r := NewPromptRegistry(NewFakeClock(time.Time{}))

	_ = r.Publish(PromptRecord{ID: "p", Version: 1, Body: "one", Tier: TierFast, EvaluationRef: "e1"})
	err := r.Publish(PromptRecord{ID: "p", Version: 1, Body: "two", Tier: TierFast, EvaluationRef: "e1"})
	if err == nil {
		t.Fatal("republishing a version would destroy the audit trail a rollout depends on")
	}
}
