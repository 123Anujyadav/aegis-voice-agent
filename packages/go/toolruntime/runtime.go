package toolruntime

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Config configures a [ToolRuntime].
type Config struct {
	// Scheduler bounds concurrency and queueing.
	Scheduler SchedulerConfig

	// Breaker configures per-tool circuit breakers.
	Breaker rt.BreakerConfig

	// DefaultRetry applies to tools that do not specify one.
	DefaultRetry RetrySpec

	// DefaultBudget is the runtime ceiling. A contract may tighten it and
	// nothing may loosen it.
	DefaultBudget Budget

	// MaxToolTimeout caps any contract's per-attempt timeout. A registry that
	// accepts a five-minute tool timeout has accepted a five-minute silence in
	// a phone call.
	MaxToolTimeout time.Duration

	// DefaultPlanTTL bounds a plan whose intent carries no deadline.
	DefaultPlanTTL time.Duration

	// IdempotencyTTL is the deduplication window.
	IdempotencyTTL time.Duration

	// MaxLedgerEntries bounds the ledger.
	MaxLedgerEntries int

	// DeadLetterDepth bounds the dead-letter queue.
	DeadLetterDepth int

	// SandboxSlots is total concurrent sandbox capacity.
	SandboxSlots int

	// DefaultToolConcurrency caps concurrent executions per tool when the
	// contract does not say.
	DefaultToolConcurrency int

	// CompensationTimeout bounds one rollback action.
	CompensationTimeout time.Duration

	// SweepInterval is how often the ledger is swept.
	SweepInterval time.Duration

	// DedupeReads applies idempotency to read-only tools as well.
	//
	// OFF BY DEFAULT. A deduplicated read returns a stored answer, and a stored
	// answer to "is this slot still free" is exactly the wrong thing to be
	// confident about. Deployments whose reads are genuinely stable can turn it
	// on and save the calls.
	DedupeReads bool

	// RequireAuditor refuses to start without an auditor.
	//
	// ON BY DEFAULT. This runtime takes actions in the world. One that does so
	// with no audit trail cannot answer "who did that, on whose authority",
	// which is an obligation rather than a preference.
	RequireAuditor bool

	// Policy is the permission engine's role map, applied at construction.
	Roles map[string][]Permission

	// JitterSeed seeds the retry engine's random source. Fixed rather than
	// time-based so a test's backoff sequence is reproducible; production
	// deployments should vary it per replica so replicas do not retry in step.
	JitterSeed int64
}

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	return Config{
		Scheduler:              DefaultSchedulerConfig(),
		Breaker:                rt.DefaultBreakerConfig(),
		DefaultRetry:           DefaultRetrySpec(),
		DefaultBudget:          DefaultBudget(),
		MaxToolTimeout:         30 * time.Second,
		DefaultPlanTTL:         60 * time.Second,
		IdempotencyTTL:         10 * time.Minute,
		MaxLedgerEntries:       10_000,
		DeadLetterDepth:        256,
		SandboxSlots:           64,
		DefaultToolConcurrency: 8,
		CompensationTimeout:    10 * time.Second,
		SweepInterval:          30 * time.Second,
		DedupeReads:            false,
		RequireAuditor:         true,
		JitterSeed:             1,
	}
}

func (c Config) validate() []string {
	problems := c.Scheduler.validate()
	if c.MaxToolTimeout <= 0 {
		problems = append(problems, "config: MaxToolTimeout must be positive")
	}
	if c.DefaultPlanTTL <= 0 {
		problems = append(problems, "config: DefaultPlanTTL must be positive")
	}
	if c.SandboxSlots <= 0 {
		problems = append(problems, "config: SandboxSlots must be positive")
	}
	if c.CompensationTimeout <= 0 {
		problems = append(problems, "config: CompensationTimeout must be positive")
	}
	if c.SweepInterval <= 0 {
		problems = append(problems, "config: SweepInterval must be positive")
	}
	problems = append(problems, c.DefaultBudget.validate(Descriptor{Tool: "config", Version: "0.0.0"})...)
	problems = append(problems,
		c.DefaultRetry.validate(Descriptor{Tool: "config", Version: "0.0.0"}, EffectRead)...)
	return problems
}

// Option customises a runtime.
type Option func(*options)

type options struct {
	clock     rt.Clock
	metrics   *Metrics
	logger    *slog.Logger
	publisher Publisher
	auditor   Auditor
	sandbox   Sandbox
}

// WithClock injects a clock.
func WithClock(c rt.Clock) Option { return func(o *options) { o.clock = c } }

// WithMetrics injects an instrument set.
func WithMetrics(m *Metrics) Option { return func(o *options) { o.metrics = m } }

// WithLogger injects a logger.
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }

// WithPublisher injects an event publisher.
func WithPublisher(p Publisher) Option { return func(o *options) { o.publisher = p } }

// WithAuditor injects an auditor.
func WithAuditor(a Auditor) Option { return func(o *options) { o.auditor = a } }

// WithSandbox injects a sandbox. The seam for an out-of-process implementation.
func WithSandbox(s Sandbox) Option { return func(o *options) { o.sandbox = s } }

// ToolRuntime is the Enterprise Tool Calling Runtime.
//
// It owns everything and shares nothing. Two runtimes in one process have
// separate registries, ledgers, breakers, metrics and schedulers, which is what
// makes the test suite parallel-safe and what makes "no global mutable state"
// a checkable property rather than an aspiration.
type ToolRuntime struct {
	cfg     Config
	clock   rt.Clock
	logger  *slog.Logger
	metrics *Metrics

	registry    *Registry
	discovery   *Discovery
	planner     *Planner
	permissions *PermissionEngine
	ledger      *Ledger
	sandbox     Sandbox
	retries     *RetryEngine
	events      *EventDispatcher
	audit       Auditor
	compensator *Compensator
	scheduler   *ToolScheduler
	supervisor  *ExecutionSupervisor
	executor    *Executor
	dispatcher  *ExecutionDispatcher
	deadLetters *DeadLetterQueue

	started atomic.Bool
	stopped atomic.Bool
	done    chan struct{}
	wg      sync.WaitGroup

	cancelMu sync.Mutex
	cancels  map[CorrelationID][]context.CancelFunc

	plansRun atomic.Uint64
	sweeps   atomic.Uint64
}

// New builds a tool runtime.
func New(cfg Config, opts ...Option) (*ToolRuntime, error) {
	o := &options{
		clock:     rt.SystemClock{},
		publisher: NoopPublisher{},
		logger:    slog.Default(),
	}
	for _, opt := range opts {
		opt(o)
	}

	problems := cfg.validate()
	if cfg.RequireAuditor && o.auditor == nil {
		problems = append(problems, "config: RequireAuditor is set and no auditor was "+
			"supplied; a runtime that takes actions in the world with no audit trail "+
			"cannot answer who did that")
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, &ConfigError{Problems: problems}
	}

	metrics := o.metrics
	if metrics == nil {
		metrics = NewMetrics()
	}
	audit := o.auditor
	if audit == nil {
		audit = NoopAuditor{}
	}

	r := &ToolRuntime{
		cfg: cfg, clock: o.clock, logger: o.logger, metrics: metrics,
		audit: audit, done: make(chan struct{}),
		cancels: make(map[CorrelationID][]context.CancelFunc),
	}

	r.events = NewEventDispatcher(metrics, o.publisher, o.clock.Now)
	r.registry = NewRegistry(o.clock, metrics, audit, cfg.MaxToolTimeout)
	r.discovery = NewDiscovery(r.registry, metrics)
	r.planner = NewPlanner(r.discovery, metrics, o.clock.Now, cfg.DefaultBudget, cfg.DefaultPlanTTL)
	r.permissions = NewPermissionEngine(o.clock, metrics, audit)
	r.ledger = NewLedger(o.clock, metrics, cfg.IdempotencyTTL, cfg.MaxLedgerEntries)
	r.deadLetters = NewDeadLetterQueue(cfg.DeadLetterDepth)
	r.retries = NewRetryEngine(o.clock, metrics, cfg.Breaker, cfg.JitterSeed, r.deadLetters)
	r.compensator = NewCompensator(o.clock, metrics, audit, r.events, cfg.CompensationTimeout)
	r.supervisor = NewExecutionSupervisor(o.clock)

	r.sandbox = o.sandbox
	if r.sandbox == nil {
		r.sandbox = NewBudgetSandbox(cfg.SandboxSlots, cfg.DefaultToolConcurrency)
	}

	sched, err := NewToolScheduler(cfg.Scheduler, o.clock, metrics)
	if err != nil {
		return nil, err
	}
	r.scheduler = sched

	for role, perms := range cfg.Roles {
		if err := r.permissions.DefineRole(role, perms...); err != nil {
			return nil, err
		}
	}

	r.executor = &Executor{
		registry: r.registry, permissions: r.permissions, ledger: r.ledger,
		sandbox: r.sandbox, retries: r.retries, events: r.events, audit: audit,
		metrics: metrics, clock: o.clock, supervisor: r.supervisor,
		defaultRetry: cfg.DefaultRetry, defaultBudget: cfg.DefaultBudget,
		dedupeReads: cfg.DedupeReads,
	}
	r.dispatcher = &ExecutionDispatcher{
		executor: r.executor, scheduler: r.scheduler, compensator: r.compensator,
		clock: o.clock, metrics: metrics, supervisor: r.supervisor,
	}
	return r, nil
}

// Registry returns the tool registry.
func (r *ToolRuntime) Registry() *Registry { return r.registry }

// Discovery returns the capability resolver.
func (r *ToolRuntime) Discovery() *Discovery { return r.discovery }

// Permissions returns the permission engine.
func (r *ToolRuntime) Permissions() *PermissionEngine { return r.permissions }

// Ledger returns the idempotency ledger.
func (r *ToolRuntime) Ledger() *Ledger { return r.ledger }

// Metrics returns the instrument set.
func (r *ToolRuntime) Metrics() *Metrics { return r.metrics }

// Scheduler returns the admission scheduler.
func (r *ToolRuntime) Scheduler() *ToolScheduler { return r.scheduler }

// Supervisor returns the execution supervisor.
func (r *ToolRuntime) Supervisor() *ExecutionSupervisor { return r.supervisor }

// DeadLetters returns the dead-letter queue.
func (r *ToolRuntime) DeadLetters() *DeadLetterQueue { return r.deadLetters }

// Events returns the event dispatcher.
func (r *ToolRuntime) Events() *EventDispatcher { return r.events }

// Clock returns the injected clock.
func (r *ToolRuntime) Clock() rt.Clock { return r.clock }

// Coordinator returns the cross-cutting operations handle.
func (r *ToolRuntime) Coordinator() *ToolCoordinator { return &ToolCoordinator{runtime: r} }

// Start begins background maintenance.
//
// A runtime starts once (INV-TOOL-10). A second Start would launch a second
// sweeper against one ledger, and two sweepers is not twice as good.
func (r *ToolRuntime) Start() error {
	if r.stopped.Load() {
		return ErrClosed
	}
	if !r.started.CompareAndSwap(false, true) {
		return invariant("INV-TOOL-10", "runtime already started")
	}

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := r.clock.NewTicker(r.cfg.SweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C():
				r.Sweep()
			case <-r.done:
				return
			}
		}
	}()
	return nil
}

// Stop halts maintenance and cancels everything in flight.
//
// Cancelling rather than waiting is deliberate. A shutdown that waits for
// in-flight tool calls waits for the slowest downstream in the platform, and a
// deploy that takes as long as the worst third party is a deploy nobody
// performs during an incident. Cancelled executions compensate on the way out,
// so the world is left tidy even though the shutdown was abrupt.
func (r *ToolRuntime) Stop() error {
	if !r.stopped.CompareAndSwap(false, true) {
		return nil
	}
	if r.started.Load() {
		close(r.done)
		r.wg.Wait()
	}
	r.Coordinator().CancelAll("runtime_stopping")
	return nil
}

// Plan builds a plan without executing it.
//
// Exported because planning is genuinely useful on its own: a caller can show a
// person what is about to happen, an operator can review a plan before
// approving it, and a test can assert plan shape without a tool being
// reachable. Building a plan executes nothing (INV-TOOL-8).
func (r *ToolRuntime) Plan(intent ToolIntent) (Plan, error) {
	if r.stopped.Load() {
		return Plan{}, ErrClosed
	}
	return r.planner.Plan(intent)
}

// Execute plans and runs an intent.
func (r *ToolRuntime) Execute(ctx context.Context, intent ToolIntent) (PlanResult, error) {
	return r.ExecuteStreaming(ctx, intent, NoopSink{})
}

// ExecuteStreaming plans and runs an intent, streaming partial results.
func (r *ToolRuntime) ExecuteStreaming(ctx context.Context, intent ToolIntent, sink StreamSink) (PlanResult, error) {
	if r.stopped.Load() {
		return PlanResult{}, ErrClosed
	}

	plan, err := r.planner.Plan(intent)
	if err != nil {
		return PlanResult{Intent: intent.ID, Correlation: intent.Correlation, Err: err}, err
	}
	return r.Run(ctx, plan, intent.Grant, sink)
}

// Run executes an already-built plan.
//
// Separate from Execute so a caller that reviewed a plan can run exactly the
// plan it reviewed. Re-planning after approval would mean the approved plan and
// the executed plan are two different objects that merely look alike.
func (r *ToolRuntime) Run(ctx context.Context, plan Plan, grant Grant, sink StreamSink) (PlanResult, error) {
	if r.stopped.Load() {
		return PlanResult{}, ErrClosed
	}

	runCtx, cancel := context.WithCancel(ctx)
	r.trackCancel(plan.Correlation, cancel)
	defer func() {
		cancel()
		r.untrackCancel(plan.Correlation)
	}()

	r.plansRun.Add(1)
	res := r.dispatcher.Run(runCtx, plan, grant, sink)
	return res, res.Err
}

func (r *ToolRuntime) trackCancel(c CorrelationID, fn context.CancelFunc) {
	if c == "" {
		return
	}
	r.cancelMu.Lock()
	defer r.cancelMu.Unlock()
	r.cancels[c] = append(r.cancels[c], fn)
}

func (r *ToolRuntime) untrackCancel(c CorrelationID) {
	if c == "" {
		return
	}
	r.cancelMu.Lock()
	defer r.cancelMu.Unlock()
	delete(r.cancels, c)
}

// Sweep runs one maintenance pass, returning how many ledger entries expired.
func (r *ToolRuntime) Sweep() int {
	r.sweeps.Add(1)
	n := r.ledger.Sweep()
	if r.metrics != nil {
		if bs, ok := r.sandbox.(*BudgetSandbox); ok {
			r.metrics.SandboxSlots.Set(float64(bs.InUse()))
		}
	}
	return n
}

// Sweeps returns how many maintenance passes have run.
func (r *ToolRuntime) Sweeps() uint64 { return r.sweeps.Load() }

// PlansRun returns how many plans have been executed.
func (r *ToolRuntime) PlansRun() uint64 { return r.plansRun.Load() }

// ToolCoordinator performs operations that span the whole runtime.
//
// It exists for the same reason the memory engine's coordinator does: some
// operations are only correct when they reach everything. Cancelling a
// conversation's tool calls must cancel ALL of them, and leaving each caller to
// remember which ones it started is how one gets left running.
type ToolCoordinator struct{ runtime *ToolRuntime }

// Cancel stops every execution for a correlation.
//
// The single most useful operation during a live call: the caller hung up, and
// everything the platform was doing on their behalf should stop. Cancelled
// executions still compensate, so hanging up does not leave a half-made booking.
func (c *ToolCoordinator) Cancel(correlation CorrelationID, reason string) int {
	c.runtime.cancelMu.Lock()
	fns := c.runtime.cancels[correlation]
	delete(c.runtime.cancels, correlation)
	c.runtime.cancelMu.Unlock()

	for _, fn := range fns {
		fn()
	}
	if len(fns) > 0 && c.runtime.metrics != nil {
		c.runtime.metrics.Cancelled.Add(float64(len(fns)), "coordinator", reason)
	}
	return len(fns)
}

// CancelAll stops every execution in the runtime.
func (c *ToolCoordinator) CancelAll(reason string) int {
	c.runtime.cancelMu.Lock()
	var fns []context.CancelFunc
	for k, list := range c.runtime.cancels {
		fns = append(fns, list...)
		delete(c.runtime.cancels, k)
	}
	c.runtime.cancelMu.Unlock()

	for _, fn := range fns {
		fn()
	}
	return len(fns)
}

// Drain moves a tool version to draining and reports how many plans are still
// running.
//
// Draining rather than retiring, so plans already pinned to the version can
// finish. A retirement that interrupts in-flight executions converts a routine
// version rollover into a wave of failed calls.
func (c *ToolCoordinator) Drain(d Descriptor) (int, error) {
	if err := c.runtime.registry.SetLifecycle(d, LifecycleDraining); err != nil {
		return 0, err
	}
	return c.runtime.supervisor.InFlight(), nil
}

// HealthReport summarises the runtime for an operator.
type HealthReport struct {
	// Registrations is how many tools are registered.
	Registrations int
	// Capabilities lists each capability and how many tools can serve it.
	Capabilities []CapabilityReport
	// Breakers reports each tool's circuit state.
	Breakers []BreakerStatus
	// Scheduler reports admission counters.
	Scheduler SchedulerStats
	// LedgerEntries is the live idempotency-entry count.
	LedgerEntries int
	// DeadLetters is the current dead-letter depth.
	DeadLetters int
	// DeadLettersDropped is how many dead letters were evicted for capacity.
	// Exposed because an operator reading the queue must know whether they are
	// reading all of it.
	DeadLettersDropped uint64
	// InFlightPlans is how many plans are running.
	InFlightPlans int
	// OldestPlan is the age of the longest-running plan.
	OldestPlan time.Duration
	// AbandonedGoroutines counts tool calls the runtime gave up waiting for.
	// A rising number names a tool that does not honour cancellation.
	AbandonedGoroutines uint64
	// ActiveOverrides lists unexpired emergency overrides. Present in the
	// health report rather than buried in an audit query, because an override
	// nobody notices is an override nobody withdraws.
	ActiveOverrides []string
}

// Health builds a health report.
func (c *ToolCoordinator) Health() HealthReport {
	r := c.runtime

	var overrides []string
	for _, o := range r.permissions.ActiveOverrides() {
		overrides = append(overrides, o.Name)
	}

	return HealthReport{
		Registrations:       r.registry.Len(),
		Capabilities:        r.discovery.Report(),
		Breakers:            r.retries.BreakerStates(),
		Scheduler:           r.scheduler.Stats(),
		LedgerEntries:       r.ledger.Len(),
		DeadLetters:         r.deadLetters.Len(),
		DeadLettersDropped:  r.deadLetters.Dropped(),
		InFlightPlans:       r.supervisor.InFlight(),
		OldestPlan:          r.supervisor.Oldest(),
		AbandonedGoroutines: r.supervisor.Abandoned(),
		ActiveOverrides:     overrides,
	}
}
