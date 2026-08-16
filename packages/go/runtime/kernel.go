package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// Config is the runtime's complete configuration.
//
// It is validated once at construction and never re-read. A runtime that reloads
// configuration under itself has to make every subsystem tolerate its own
// parameters changing mid-flight, which is a large amount of complexity for a
// capability nothing here needs — a config change is a deploy.
type Config struct {
	// Name identifies this runtime instance in logs, metrics and traces.
	Name string

	// Version is the build version.
	Version string

	// Scheduler tunes admission and concurrency.
	Scheduler SchedulerConfig

	// Session tunes session lifetime.
	Session SessionConfig

	// Dispatcher tunes streaming.
	Dispatcher DispatcherConfig

	// Breaker tunes the per-provider circuit breakers.
	Breaker BreakerConfig

	// Retry tunes provider retries.
	Retry RetryPolicy

	// DefaultDeadline bounds a request whose spec carries no deadline.
	//
	// Defaults to the p99 ceiling from ADR-0011 rather than something round.
	// A request permitted to exceed the ceiling has already failed its
	// contract, and letting it continue only delays the caller's fallback.
	DefaultDeadline time.Duration

	// MaxFallbackHops bounds provider failover for one request. Small: each hop
	// spends latency budget, and a request that has already failed twice is
	// better served by failing fast so the caller can degrade.
	MaxFallbackHops int
}

// DefaultConfig returns a fully-populated configuration.
func DefaultConfig(name, version string) Config {
	return Config{
		Name:            name,
		Version:         version,
		Scheduler:       DefaultSchedulerConfig(),
		Session:         DefaultSessionConfig(),
		Dispatcher:      DefaultDispatcherConfig(),
		Breaker:         DefaultBreakerConfig(),
		Retry:           DefaultRetryPolicy(),
		DefaultDeadline: 2500 * time.Millisecond,
		MaxFallbackHops: 2,
	}
}

func (c Config) validate() []string {
	var p []string
	if c.Name == "" {
		p = append(p, "runtime: Name is required")
	}
	if c.DefaultDeadline <= 0 {
		p = append(p, "runtime: DefaultDeadline must be positive")
	}
	if c.MaxFallbackHops < 0 {
		p = append(p, "runtime: MaxFallbackHops cannot be negative")
	}
	p = append(p, c.Scheduler.validate()...)
	p = append(p, c.Session.validate()...)
	p = append(p, c.Dispatcher.validate()...)
	p = append(p, c.Breaker.validate("default")...)
	return p
}

// ---------------------------------------------------------------------------
// Tracing
// ---------------------------------------------------------------------------

// Span is one unit of traced work.
//
// The interface is deliberately narrow — the subset of OpenTelemetry the
// runtime actually uses. An adapter in a sibling module implements it over the
// real SDK, which keeps the OTel dependency, its transitive graph and its
// version churn out of the kernel entirely.
type Span interface {
	// SetAttribute records a key/value on the span.
	//
	// PRIVACY. Attributes are exported to a tracing backend and are frequently
	// retained longer than application data. Nothing classified PERSONAL or
	// SENSITIVE may be set here — no phone number, no message content, no
	// caller name. Identifiers and enumerations only.
	SetAttribute(key string, value any)

	// RecordError attaches an error.
	RecordError(err error)

	// End completes the span.
	End()
}

// Tracer starts spans.
type Tracer interface {
	// Start begins a span and returns a context carrying it.
	Start(ctx context.Context, name string) (context.Context, Span)
}

// NoopTracer discards every span. It is the default, so the runtime works with
// no tracing backend configured — a runtime that requires a collector to run is
// a runtime that cannot be unit-tested.
type NoopTracer struct{}

// Start returns the context unchanged and a span that does nothing.
func (NoopTracer) Start(ctx context.Context, _ string) (context.Context, Span) {
	return ctx, noopSpan{}
}

type noopSpan struct{}

func (noopSpan) SetAttribute(string, any) {}
func (noopSpan) RecordError(error)        {}
func (noopSpan) End()                     {}

// ---------------------------------------------------------------------------
// Provider registry
// ---------------------------------------------------------------------------

// providerEntry pairs a provider with its breaker.
type providerEntry struct {
	provider Provider
	breaker  *Breaker
}

// ProviderRegistry holds registered providers and their circuit breakers.
type ProviderRegistry struct {
	mu        sync.RWMutex
	entries   map[ProviderID]*providerEntry
	breakerCf BreakerConfig
	clock     Clock
	metrics   *Metrics
}

// NewProviderRegistry constructs a registry.
func NewProviderRegistry(cfg BreakerConfig, clock Clock, metrics *Metrics) *ProviderRegistry {
	if clock == nil {
		clock = SystemClock{}
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	return &ProviderRegistry{
		entries:   make(map[ProviderID]*providerEntry),
		breakerCf: cfg,
		clock:     clock,
		metrics:   metrics,
	}
}

// Register adds a provider, giving it its own circuit breaker.
func (r *ProviderRegistry) Register(p Provider) error {
	if p == nil {
		return errors.New("runtime: cannot register a nil provider")
	}
	id := p.ID()
	if id == "" {
		return errors.New("runtime: provider ID is required")
	}

	b, err := NewBreaker(string(id), r.breakerCf, r.clock)
	if err != nil {
		return err
	}
	metrics := r.metrics
	b.OnStateChange(func(_, to BreakerState) {
		metrics.BreakerTransition.Inc(string(id), to.String())
		metrics.BreakerState.Set(float64(to))
	})

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[id]; exists {
		return fmt.Errorf("runtime: provider %s is already registered", id)
	}
	r.entries[id] = &providerEntry{provider: p, breaker: b}
	return nil
}

// Get returns a provider and its breaker.
func (r *ProviderRegistry) Get(id ProviderID) (Provider, *Breaker, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[id]
	if !ok {
		return nil, nil, fmt.Errorf("%w: provider %s", ErrNotFound, id)
	}
	return e.provider, e.breaker, nil
}

// IDs returns every registered provider identifier.
func (r *ProviderRegistry) IDs() []ProviderID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ProviderID, 0, len(r.entries))
	for id := range r.entries {
		out = append(out, id)
	}
	return out
}

// Close closes every provider, returning the joined error.
func (r *ProviderRegistry) Close() error {
	r.mu.Lock()
	entries := make([]*providerEntry, 0, len(r.entries))
	for _, e := range r.entries {
		entries = append(entries, e)
	}
	r.entries = make(map[ProviderID]*providerEntry)
	r.mu.Unlock()

	var errs []error
	for _, e := range entries {
		if err := e.provider.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

// HealthState summarises runtime health.
type HealthState struct {
	// Ready reports whether the runtime should receive traffic.
	Ready bool

	// Draining reports whether shutdown has begun.
	Draining bool

	// Providers maps each provider to its breaker state.
	Providers map[ProviderID]string

	// Utilisation is scheduler in-flight over capacity.
	Utilisation float64

	// Sessions is the live session count.
	Sessions int
}

// ---------------------------------------------------------------------------
// Kernel
// ---------------------------------------------------------------------------

// Kernel owns every runtime subsystem and their lifecycle.
//
// It is the answer to "no global mutable state": every subsystem is reachable
// only through a Kernel, kernels are constructed rather than initialised, and
// two kernels in one process share nothing. The test suite constructs one per
// test and runs in parallel, which is only possible because of this.
type Kernel struct {
	cfg     Config
	clock   Clock
	logger  *slog.Logger
	tracer  Tracer
	metrics *Metrics

	scheduler *Scheduler
	sessions  *SessionManager
	models    *ModelRegistry
	providers *ProviderRegistry
	prompts   *PromptRegistry

	rndMu sync.Mutex
	rnd   *rand.Rand

	started  atomic.Bool
	ready    atomic.Bool
	draining atomic.Bool
}

// Option customises a Kernel at construction.
type Option func(*Kernel)

// WithClock replaces the clock. Used by tests and by anything needing
// deterministic time.
func WithClock(c Clock) Option {
	return func(k *Kernel) {
		if c != nil {
			k.clock = c
		}
	}
}

// WithLogger replaces the logger.
func WithLogger(l *slog.Logger) Option {
	return func(k *Kernel) {
		if l != nil {
			k.logger = l
		}
	}
}

// WithTracer replaces the tracer.
func WithTracer(t Tracer) Option {
	return func(k *Kernel) {
		if t != nil {
			k.tracer = t
		}
	}
}

// WithMetrics replaces the instrument set.
func WithMetrics(m *Metrics) Option {
	return func(k *Kernel) {
		if m != nil {
			k.metrics = m
		}
	}
}

// New constructs a Kernel.
//
// Construction validates configuration fully and fails rather than defaulting.
// A runtime that starts with invalid configuration is more dangerous than one
// that does not start, because the failure surfaces later and further away —
// the same reasoning platform.LoadConfig applies to services.
func New(cfg Config, opts ...Option) (*Kernel, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}

	k := &Kernel{
		cfg:     cfg,
		clock:   SystemClock{},
		logger:  slog.Default(),
		tracer:  NoopTracer{},
		metrics: NewMetrics(),
		//nolint:gosec // Not cryptographic: this seeds retry jitter only.
		rnd: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	for _, opt := range opts {
		opt(k)
	}

	k.logger = k.logger.With(
		slog.String("runtime", cfg.Name),
		slog.String("version", cfg.Version),
	)

	var err error
	if k.scheduler, err = NewScheduler(cfg.Scheduler, k.clock, k.metrics); err != nil {
		return nil, err
	}
	if k.sessions, err = NewSessionManager(cfg.Session, k.clock, k.metrics); err != nil {
		return nil, err
	}
	k.models = NewModelRegistry()
	k.providers = NewProviderRegistry(cfg.Breaker, k.clock, k.metrics)
	k.prompts = NewPromptRegistry(k.clock)

	return k, nil
}

// Accessors. These return the owned subsystem rather than a copy, because a
// caller registering a model or a provider must affect this kernel.

// Scheduler returns the kernel's scheduler.
func (k *Kernel) Scheduler() *Scheduler { return k.scheduler }

// Sessions returns the kernel's session manager.
func (k *Kernel) Sessions() *SessionManager { return k.sessions }

// Models returns the kernel's model registry.
func (k *Kernel) Models() *ModelRegistry { return k.models }

// Providers returns the kernel's provider registry.
func (k *Kernel) Providers() *ProviderRegistry { return k.providers }

// Prompts returns the kernel's prompt registry.
func (k *Kernel) Prompts() *PromptRegistry { return k.prompts }

// Metrics returns the kernel's instrument set.
func (k *Kernel) Metrics() *Metrics { return k.metrics }

// Logger returns the kernel's logger.
//
// PRIVACY. The returned logger redacts nothing on its own. Callers must not log
// message content, phone numbers or caller names — those are SENSITIVE or
// PERSONAL under annotations.proto. The runtime itself logs identifiers,
// enumerations and durations, and never content.
func (k *Kernel) Logger() *slog.Logger { return k.logger }

// Clock returns the kernel's clock.
func (k *Kernel) Clock() Clock { return k.clock }

// Start brings the runtime up and marks it ready.
//
// Order matters and is the reverse of Stop's: the session sweeper starts before
// readiness is asserted, so no traffic arrives before expiry is running.
func (k *Kernel) Start(ctx context.Context) error {
	if k.started.Swap(true) {
		return errors.New("runtime: kernel already started")
	}

	k.sessions.Start()

	// Probe every provider once so a misconfigured credential fails the deploy
	// rather than the first call. A failing probe does NOT prevent start: a
	// provider may be down at boot and recover, and refusing to start would
	// convert a partial outage into a total one.
	for _, id := range k.providers.IDs() {
		p, _, err := k.providers.Get(id)
		if err != nil {
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = p.Probe(probeCtx)
		cancel()
		if err != nil {
			k.logger.Warn("provider probe failed at start",
				slog.String("provider", string(id)),
				slog.String("error", err.Error()))
		}
	}

	k.ready.Store(true)
	k.logger.Info("runtime started",
		slog.Int("max_concurrent", k.cfg.Scheduler.MaxConcurrent),
		slog.Int("providers", len(k.providers.IDs())),
		slog.Int("models", len(k.models.List())))
	return nil
}

// Stop drains and shuts the runtime down.
//
// This is Invariant I6 at runtime granularity: readiness goes false first, so
// the load balancer stops sending work, and only then does the drain begin.
// Reversing the order is why deployments that look graceful still drop calls.
func (k *Kernel) Stop(ctx context.Context) error {
	if !k.started.Load() {
		return nil
	}
	if k.draining.Swap(true) {
		return nil
	}

	k.ready.Store(false)
	k.logger.Info("runtime draining")

	k.scheduler.Close()

	var errs []error
	if err := k.scheduler.Wait(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := k.sessions.Close(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := k.providers.Close(); err != nil {
		errs = append(errs, err)
	}

	k.logger.Info("runtime stopped", slog.Bool("clean", len(errs) == 0))
	return errors.Join(errs...)
}

// Ready reports whether the runtime should receive traffic.
func (k *Kernel) Ready() bool { return k.ready.Load() }

// Health returns a health snapshot.
func (k *Kernel) Health() HealthState {
	providers := make(map[ProviderID]string)
	for _, id := range k.providers.IDs() {
		if _, b, err := k.providers.Get(id); err == nil {
			providers[id] = b.State().String()
		}
	}
	return HealthState{
		Ready:       k.ready.Load(),
		Draining:    k.draining.Load(),
		Providers:   providers,
		Utilisation: k.scheduler.Utilisation(),
		Sessions:    k.sessions.Count(),
	}
}

// ---------------------------------------------------------------------------
// Generation
// ---------------------------------------------------------------------------

// Generate begins a generation and returns its dispatcher.
//
// It returns as soon as the provider stream is open — before the first token —
// so the caller holds a [Dispatcher] it can [Dispatcher.Abort] immediately.
// That shape is what makes the 20 ms barge-in budget reachable: a Generate that
// blocked until completion would give the caller nothing to cancel.
//
// The caller waits on [Dispatcher.Done] and reads [Dispatcher.Result].
//
// Sequence, and why it is this order:
//
//  1. Admission. Refuse early, before any expensive work, so a shed costs
//     nothing. ClassSafety bypasses capacity (I11).
//  2. Session check. A request against a draining session must not start.
//  3. Context assembly. Local, cheap, and may fail on budget — better to
//     discover that before opening a provider connection.
//  4. Model resolution and invariant enforcement (I3).
//  5. Provider call with breaker, retry and fallback.
//  6. Dispatcher started on its own goroutine.
func (k *Kernel) Generate(ctx context.Context, spec GenerateSpec, sinks ...Sink) (*Dispatcher, error) {
	if !k.started.Load() {
		return nil, fmt.Errorf("%w: runtime not started", ErrClosed)
	}
	if spec.RequestID == "" {
		spec.RequestID = NewRequestID()
	}
	if spec.Deadline.IsZero() {
		spec.Deadline = k.clock.Now().Add(k.cfg.DefaultDeadline)
	}
	if !spec.Tier.Valid() || spec.Tier == TierNone {
		return nil, fmt.Errorf("%w: tier %s does not invoke a model", ErrNotFound, spec.Tier)
	}

	ctx, span := k.tracer.Start(ctx, "runtime.generate")
	span.SetAttribute("request_id", string(spec.RequestID))
	span.SetAttribute("session_id", string(spec.SessionID))
	span.SetAttribute("tier", spec.Tier.String())
	span.SetAttribute("class", spec.Class.String())

	release, decision := k.scheduler.Admit(ctx, spec.Class, spec.Deadline)
	if !decision.Admitted {
		span.SetAttribute("shed_reason", string(decision.Reason))
		span.End()
		return nil, fmt.Errorf("%w: %s", ErrShed, decision.Reason)
	}

	// From here every failure path must release the slot. A leaked slot
	// permanently reduces capacity and is invisible until the runtime wedges.
	fail := func(err error) (*Dispatcher, error) {
		span.RecordError(err)
		span.End()
		release()
		return nil, err
	}

	session, err := k.sessions.Get(spec.SessionID)
	if err != nil {
		return fail(err)
	}
	complete, err := session.BeginRequest()
	if err != nil {
		return fail(err)
	}

	failWithSession := func(err error) (*Dispatcher, error) {
		complete(Usage{})
		return fail(err)
	}

	messages := spec.Messages
	if len(messages) == 0 {
		if messages, err = session.Context().Assemble(0); err != nil {
			return failWithSession(err)
		}
	}
	spec.Messages = messages

	stream, model, err := k.openStream(ctx, spec, span)
	if err != nil {
		return failWithSession(err)
	}

	dispatcher, err := NewDispatcher(k.cfg.Dispatcher, k.clock, k.metrics)
	if err != nil {
		_ = stream.Close()
		return failWithSession(err)
	}
	for _, s := range sinks {
		if err := dispatcher.AddSink(s); err != nil {
			_ = stream.Close()
			return failWithSession(err)
		}
	}

	span.SetAttribute("model", string(model.ID))
	span.SetAttribute("provider", string(model.Provider))
	span.SetAttribute("stream_id", string(dispatcher.ID()))

	// Cleanup runs as a dispatcher finalizer rather than after Run returns, so
	// the scheduler slot and the session request are both released BEFORE
	// Dispatcher.Done closes. A caller that loops on Done to pace itself would
	// otherwise over-admit by however many generations were mid-cleanup — a
	// capacity bug that only appears under load.
	if err := dispatcher.OnComplete(func(result StreamResult) {
		complete(result.Usage)
		release()

		k.metrics.ModelTokens.Add(float64(result.Usage.InputTokens), string(model.ID), "input")
		k.metrics.ModelTokens.Add(float64(result.Usage.OutputTokens), string(model.ID), "output")
		k.metrics.ModelTokens.Add(float64(result.Usage.ThinkingTokens), string(model.ID), "thinking")

		if result.Err != nil && !errors.Is(result.Err, ErrAborted) {
			span.RecordError(result.Err)
		}

		// Logged without any message content. Identifiers, enumerations and
		// durations only — see the note on Kernel.Logger.
		k.logger.Debug("generation complete",
			slog.String("request_id", string(spec.RequestID)),
			slog.String("model", string(model.ID)),
			slog.Int("chunks", result.Chunks),
			slog.Bool("aborted", result.Aborted),
			slog.Duration("ttft", result.TimeToFirstToken),
			slog.Duration("duration", result.Duration))
	}); err != nil {
		_ = stream.Close()
		return failWithSession(err)
	}

	go func() {
		defer span.End()

		runCtx, cancel := k.deadlineContext(ctx, spec.Deadline)
		defer cancel()

		dispatcher.Run(runCtx, stream)
	}()

	return dispatcher, nil
}

// deadlineContext returns a context cancelled at the given instant on the
// KERNEL'S clock.
//
// context.WithDeadline cannot be used here, and the reason is worth stating
// because it is a trap the first version of this file fell into.
// context.WithDeadline schedules against real wall-clock time. Every deadline
// in this runtime is derived from the injected [Clock]. Under a [FakeClock] the
// two disagree completely — the fake clock's instant is an arbitrary fixed date
// — so every request either expires immediately or never. Mixing the two
// produces a runtime whose timeouts cannot be tested and whose behaviour under
// a fake clock is meaningless.
//
// The deadline is delivered as a cancellation CAUSE rather than as ctx.Err(),
// so a consumer can still distinguish "we ran out of budget" from "the caller
// went away" via context.Cause.
func (k *Kernel) deadlineContext(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)

	remaining := deadline.Sub(k.clock.Now())
	if remaining <= 0 {
		cancel(context.DeadlineExceeded)
		return ctx, func() { cancel(context.Canceled) }
	}

	timer := k.clock.NewTimer(remaining)
	stop := make(chan struct{})
	go func() {
		defer timer.Stop()
		select {
		case <-timer.C():
			cancel(context.DeadlineExceeded)
		case <-stop:
		case <-ctx.Done():
		}
	}()

	var once sync.Once
	return ctx, func() {
		once.Do(func() { close(stop) })
		cancel(context.Canceled)
	}
}

// openStream resolves a model and opens a provider stream, applying the circuit
// breaker, the retry policy and fallback.
func (k *Kernel) openStream(ctx context.Context, spec GenerateSpec, span Span) (TokenStream, ModelSpec, error) {
	var (
		tried    []ModelID
		lastErr  error
		hops     int
		attempts int
	)

	for {
		model, err := k.models.ResolveTier(spec.Tier, tried...)
		if err != nil {
			if lastErr != nil {
				return nil, ModelSpec{}, lastErr
			}
			return nil, ModelSpec{}, err
		}

		req, err := k.models.BuildRequest(spec, model)
		if err != nil {
			// An invariant violation is a programming error and is never
			// retried or failed over — moving to another model would hide it.
			return nil, ModelSpec{}, err
		}

		provider, breaker, err := k.providers.Get(model.Provider)
		if err != nil {
			tried = append(tried, model.ID)
			lastErr = err
			if hops++; hops > k.cfg.MaxFallbackHops {
				return nil, ModelSpec{}, lastErr
			}
			continue
		}

		allowed, report := breaker.Allow()
		if !allowed {
			k.metrics.ProviderFailures.Inc(string(model.Provider), "breaker_open")
			tried = append(tried, model.ID)
			lastErr = fmt.Errorf("%w: provider %s circuit is open",
				ErrProviderUnavailable, model.Provider)
			if hops++; hops > k.cfg.MaxFallbackHops {
				return nil, ModelSpec{}, lastErr
			}
			continue
		}

		attempts++
		start := k.clock.Now()
		stream, err := provider.Generate(ctx, req)
		report(err)
		k.metrics.ProviderRequests.Inc(string(model.Provider), string(model.ID))
		k.metrics.ProviderLatency.Observe(k.clock.Since(start).Seconds(),
			string(model.Provider), string(model.ID))

		if err == nil {
			return stream, model, nil
		}

		lastErr = err
		var pe *ProviderError
		kind := "unknown"
		if asProviderError(err, &pe) {
			kind = pe.Kind.String()
		}
		k.metrics.ProviderFailures.Inc(string(model.Provider), kind)
		span.SetAttribute("provider_error_kind", kind)

		remaining := spec.Deadline.Sub(k.clock.Now())
		if k.cfg.Retry.ShouldRetry(attempts, err, remaining, model.TypicalLatency) {
			k.rndMu.Lock()
			backoff := k.cfg.Retry.Backoff(attempts+1, k.rnd)
			k.rndMu.Unlock()

			k.metrics.ProviderRetries.Inc(string(model.Provider))
			if sleepErr := k.clock.Sleep(ctx, backoff); sleepErr != nil {
				return nil, ModelSpec{}, sleepErr
			}
			continue // same model, next attempt
		}

		// Retries exhausted or refused: fail over.
		tried = append(tried, model.ID)
		attempts = 0
		if fb, ok := k.models.Fallback(model.ID); ok {
			// An explicit fallback takes precedence over tier order. Record it
			// so a chain of hops is legible in a trace rather than inferred.
			span.SetAttribute("fallback_from", string(model.ID))
			span.SetAttribute("fallback_to", string(fb.ID))
		}
		if hops++; hops > k.cfg.MaxFallbackHops {
			return nil, ModelSpec{}, lastErr
		}
	}
}
