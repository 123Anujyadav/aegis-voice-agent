package telephony

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// RuntimeState is the runtime's own lifecycle stage.
type RuntimeState uint8

// The runtime states.
const (
	// RuntimeNew has been constructed but not started.
	RuntimeNew RuntimeState = iota
	// RuntimeRunning is accepting calls.
	RuntimeRunning
	// RuntimeDraining is refusing new calls and waiting for live ones.
	RuntimeDraining
	// RuntimeStopped is shut down.
	RuntimeStopped
)

// String renders the state.
func (s RuntimeState) String() string {
	switch s {
	case RuntimeRunning:
		return "running"
	case RuntimeDraining:
		return "draining"
	case RuntimeStopped:
		return "stopped"
	default:
		return "new"
	}
}

// Option customises a runtime.
type Option func(*options)

type options struct {
	clock     rt.Clock
	logger    *slog.Logger
	publisher Publisher
	store     SessionStore
	metrics   *RuntimeMetrics
	liveness  LivenessCheck
}

// WithClock injects a clock. A [runtime.FakeClock] makes every timeout testable
// without sleeping.
func WithClock(c rt.Clock) Option { return func(o *options) { o.clock = c } }

// WithLogger injects a logger.
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }

// WithPublisher injects the event publisher.
func WithPublisher(p Publisher) Option { return func(o *options) { o.publisher = p } }

// WithSessionStore injects the durable session store. Without one the runtime
// runs but cannot recover.
func WithSessionStore(s SessionStore) Option { return func(o *options) { o.store = s } }

// WithMetrics injects an instrument set, so a service can share one registry.
func WithMetrics(m *RuntimeMetrics) Option { return func(o *options) { o.metrics = m } }

// WithLivenessCheck injects the provider liveness probe used during recovery.
// Defaults to [AssumeDead].
func WithLivenessCheck(f LivenessCheck) Option { return func(o *options) { o.liveness = f } }

// TelephonyRuntime owns every call in one process.
//
// # It owns everything and shares nothing
//
// Two runtimes in one process have separate registries, schedulers, providers,
// metrics and stores. There is no package-level mutable state anywhere in this
// module, which is what makes the test suite parallel-safe and horizontal
// scaling a deployment decision rather than a code change.
//
// # Start and Stop are ordered, and the order is the reverse of each other
//
// Start: recover sessions, then start the sweeper, then accept calls. Recovery
// runs BEFORE admission opens so a recovered call cannot lose its capacity slot
// to a new one that arrived first.
//
// Stop: refuse new calls, then drain, then snapshot, then stop the sweeper. The
// snapshot happens after the drain so it captures only what genuinely could not
// finish.
type TelephonyRuntime struct {
	cfg       Config
	clock     rt.Clock
	logger    *slog.Logger
	metrics   *RuntimeMetrics
	store     SessionStore
	publisher Publisher
	liveness  LivenessCheck

	registry  *CallRegistry
	scheduler *CallScheduler
	providers *providerRegistry
	lifecycle *CallLifecycle

	mu    sync.RWMutex
	state RuntimeState

	// stop signals the background loops. Closed once, by Stop, guarded by once.
	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	// Counters for the runtime's own reporting.
	admitted  atomic.Uint64
	shed      atomic.Uint64
	recovered atomic.Uint64
}

// New builds a telephony runtime.
func New(cfg Config, opts ...Option) (*TelephonyRuntime, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}

	o := &options{}
	for _, opt := range opts {
		opt(o)
	}

	clock := ensureClock(o.clock)
	logger := o.logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	publisher := o.publisher
	if publisher == nil {
		// A nil publisher would silently discard every event, which is
		// indistinguishable from a broker outage. NopPublisher makes the same
		// behaviour a visible choice.
		publisher = NopPublisher{}
	}
	m := o.metrics
	if m == nil {
		m = NewRuntimeMetrics()
	}
	liveness := o.liveness
	if liveness == nil {
		liveness = AssumeDead
	}

	registry := NewCallRegistry()
	providers := newProviderRegistry()
	scheduler := NewCallScheduler(cfg, registry, m)

	r := &TelephonyRuntime{
		cfg: cfg, clock: clock, logger: logger, metrics: m,
		store: o.store, publisher: publisher, liveness: liveness,
		registry: registry, scheduler: scheduler, providers: providers,
		state: RuntimeNew,
		stop:  make(chan struct{}),
	}

	r.lifecycle = &CallLifecycle{
		registry: registry, publisher: publisher, metrics: m,
		clock: clock, logger: logger, providers: providers, cfg: cfg,
		seqs: &sequencer{reg: registry},
	}
	return r, nil
}

// RegisterProvider adds a provider adapter.
//
// Must be called before Start. A provider registered while calls are in flight
// could not be reached by those calls anyway, and allowing it invites the
// mistake of swapping an adapter under a live call.
func (r *TelephonyRuntime) RegisterProvider(p Provider) error {
	r.mu.RLock()
	state := r.state
	r.mu.RUnlock()

	if state != RuntimeNew {
		return fmt.Errorf("telephony: providers must be registered before Start (runtime is %s)", state)
	}
	return r.providers.register(p)
}

// Start recovers sessions and begins accepting calls.
func (r *TelephonyRuntime) Start(ctx context.Context) (RecoveryReport, error) {
	r.mu.Lock()
	if r.state != RuntimeNew {
		state := r.state
		r.mu.Unlock()
		return RecoveryReport{}, fmt.Errorf("telephony: runtime already %s", state)
	}
	if r.providers.len() == 0 {
		r.mu.Unlock()
		return RecoveryReport{}, &ConfigError{Problems: []string{
			"start: no provider registered; a telephony runtime with no provider " +
				"can accept no calls and would fail every request at admission"}}
	}
	r.mu.Unlock()

	// Recovery BEFORE admission opens, so a recovered call cannot lose its
	// capacity slot to a new call that arrived first.
	report, err := r.recoverSessions(ctx)
	if err != nil {
		return report, err
	}
	r.recovered.Add(uint64(report.Resumed))

	r.mu.Lock()
	r.state = RuntimeRunning
	r.mu.Unlock()

	r.wg.Add(1)
	go r.sweepLoop()

	if r.cfg.SnapshotInterval > 0 && r.store != nil {
		r.wg.Add(1)
		go r.snapshotLoop()
	}

	r.logger.InfoContext(ctx, "telephony runtime started",
		slog.Int("providers", r.providers.len()),
		slog.Int("recovered", report.Resumed),
		slog.Int("concluded", report.Concluded))
	return report, nil
}

// Stop drains and shuts down.
//
// Idempotent. Returns the number of calls that were still live when the drain
// deadline expired — a non-zero value is not an error but is worth alerting on,
// because it means calls were abandoned mid-flight.
func (r *TelephonyRuntime) Stop(ctx context.Context) (int, error) {
	r.mu.Lock()
	if r.state == RuntimeStopped {
		r.mu.Unlock()
		return 0, nil
	}
	// Refuse new calls immediately. Everything after this is about the calls
	// already here.
	r.state = RuntimeDraining
	r.mu.Unlock()

	abandoned := r.drain(ctx)

	// Snapshot after draining, so it captures only what could not finish.
	if r.store != nil {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.cfg.ProviderTimeout)
		if n, err := r.snapshotAll(sctx); err != nil {
			r.logger.ErrorContext(ctx, "shutdown snapshot failed; live calls will not recover",
				slog.String("error", err.Error()))
		} else if n > 0 {
			r.logger.InfoContext(ctx, "snapshotted live calls for recovery", slog.Int("count", n))
		}
		cancel()
	}

	r.stopOnce.Do(func() { close(r.stop) })
	r.wg.Wait()

	r.mu.Lock()
	r.state = RuntimeStopped
	r.mu.Unlock()

	return abandoned, nil
}

// drain waits for live calls to finish, up to the drain timeout.
//
// # The budget is REAL time, not the injected clock
//
// Every call deadline in this runtime is measured against [TelephonyRuntime.clock]
// so a test can advance it. This one is not, and the distinction is load-bearing.
//
// The first version took its deadline from the injected clock while polling with
// a real ticker. Under a FakeClock that nobody advances — which is every test,
// and any deployment that injects a controlled clock — `clock.Now()` never
// moves, the deadline never arrives, and Stop spins forever holding the process
// open. A graceful shutdown that never terminates is worse than an abrupt one:
// the orchestrator waits out its grace period and sends SIGKILL anyway, having
// wasted it. See ENGINEERING_AUDIT F1.
//
// Measuring on the clock that actually drives the loop is also right on its own
// terms. This budget is an operational shutdown allowance — a Kubernetes
// terminationGracePeriod — not call-lifecycle semantics. Call timeouts belong to
// the injected clock; wall-clock patience during shutdown belongs to the wall
// clock.
func (r *TelephonyRuntime) drain(ctx context.Context) int {
	deadline := time.Now().Add(r.cfg.DrainTimeout)

	// Polling rather than a condition variable: the registry is sharded and a
	// broadcast across 64 shards on every call end would cost more on the hot
	// path than a poll costs during the seconds a shutdown lasts.
	pollInterval := r.cfg.DrainTimeout / 50
	if pollInterval < time.Millisecond {
		pollInterval = time.Millisecond
	}
	if pollInterval > 50*time.Millisecond {
		pollInterval = 50 * time.Millisecond
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		live := r.registry.Len()
		if live == 0 {
			return 0
		}
		if !time.Now().Before(deadline) {
			r.logger.WarnContext(ctx, "drain deadline expired with calls still live",
				slog.Int("abandoned", live))
			return live
		}
		select {
		case <-ctx.Done():
			return r.registry.Len()
		case <-ticker.C:
		}
	}
}

// sweepLoop runs the timeout sweeper and the state census.
func (r *TelephonyRuntime) sweepLoop() {
	defer r.wg.Done()

	ticker := time.NewTicker(r.cfg.SweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), r.cfg.SweepInterval)
			r.Sweep(ctx)
			cancel()
		}
	}
}

// Sweep runs one maintenance pass.
//
// Exported so a test drives it directly. A sweeper that could only be triggered
// by its own ticker would force every timeout test to wait in real time, which
// is exactly what the injected clock exists to avoid.
func (r *TelephonyRuntime) Sweep(ctx context.Context) {
	r.lifecycle.SweepTimeouts(ctx)
	r.lifecycle.ReapTerminal(ctx)
	r.metrics.ObserveStates(r.registry.ByState())
	r.metrics.LiveCalls.Set(float64(r.registry.Len()))
}

// snapshotLoop periodically persists live sessions.
func (r *TelephonyRuntime) snapshotLoop() {
	defer r.wg.Done()

	ticker := time.NewTicker(r.cfg.SnapshotInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), r.cfg.SnapshotInterval)
			if _, err := r.snapshotAll(ctx); err != nil {
				r.logger.WarnContext(ctx, "periodic snapshot failed",
					slog.String("error", err.Error()))
			}
			cancel()
		}
	}
}

// State returns the runtime's stage.
func (r *TelephonyRuntime) State() RuntimeState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state
}

// accepting reports whether new calls may be admitted.
func (r *TelephonyRuntime) accepting() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state == RuntimeRunning
}

// Registry returns the call registry.
func (r *TelephonyRuntime) Registry() *CallRegistry { return r.registry }

// Scheduler returns the admission scheduler.
func (r *TelephonyRuntime) Scheduler() *CallScheduler { return r.scheduler }

// Lifecycle returns the lifecycle driver.
func (r *TelephonyRuntime) Lifecycle() *CallLifecycle { return r.lifecycle }

// Metrics returns the instrument set.
func (r *TelephonyRuntime) Metrics() *RuntimeMetrics { return r.metrics }

// Clock returns the injected clock.
func (r *TelephonyRuntime) Clock() rt.Clock { return r.clock }

// Config returns the configuration in force.
func (r *TelephonyRuntime) Config() Config { return r.cfg }

// Providers returns the registered provider identifiers.
func (r *TelephonyRuntime) Providers() []ProviderID { return r.providers.ids() }

// Live returns the live call count.
func (r *TelephonyRuntime) Live() int { return r.registry.Len() }

// Admitted returns how many calls this runtime has admitted.
func (r *TelephonyRuntime) Admitted() uint64 { return r.admitted.Load() }

// Shed returns how many calls this runtime has refused.
func (r *TelephonyRuntime) Shed() uint64 { return r.shed.Load() }

// Recovered returns how many calls this runtime resumed at start-up.
func (r *TelephonyRuntime) Recovered() uint64 { return r.recovered.Load() }

// ---------------------------------------------------------------------------
// CallCoordinator
// ---------------------------------------------------------------------------

// CallCoordinator is the runtime's front door.
//
// # Admission and lifecycle are separated on purpose
//
// [CallScheduler] decides whether a call may start; [CallLifecycle] drives it
// once it has. The coordinator is the only place that does both, and it is what
// guarantees the pairing every capacity bug comes from: a call admitted and
// then failed must release its slot, or the runtime slowly loses capacity to
// calls that never existed.
//
// Every path through Begin either releases the slot or hands the call to a
// session that will release it on termination. There is no third outcome.
type CallCoordinator struct{ rt *TelephonyRuntime }

// Coordinator returns the runtime's coordinator.
func (r *TelephonyRuntime) Coordinator() *CallCoordinator {
	return &CallCoordinator{rt: r}
}

// Begin admits and starts a call.
//
// The single entry point a provider adapter uses. Returns
// [ErrCapacityExceeded] when the call is shed — a refusal the adapter should
// translate into whatever "busy" means for its carrier.
func (c *CallCoordinator) Begin(ctx context.Context, cc CallContext) (*CallSession, error) {
	if !c.rt.accepting() {
		return nil, fmt.Errorf("%w: runtime is %s", ErrRuntimeStopped, c.rt.State())
	}
	if err := cc.Validate(); err != nil {
		return nil, err
	}

	decision := c.rt.scheduler.Admit(cc.Provider)
	if !decision.Admitted {
		c.rt.shed.Add(1)
		return nil, fmt.Errorf("%w: %s", ErrCapacityExceeded, decision.Reason)
	}

	started := c.rt.clock.Now()

	var (
		sess *CallSession
		err  error
	)
	if cc.Direction == DirectionInbound {
		sess, err = c.rt.lifecycle.Incoming(ctx, cc)
	} else {
		sess, err = c.rt.lifecycle.Outgoing(ctx, cc)
	}
	if err != nil {
		// The slot was reserved and the call never started. Releasing here is
		// the pairing that keeps capacity honest.
		c.rt.scheduler.Release(cc.Provider)
		return nil, err
	}

	c.rt.admitted.Add(1)
	c.rt.metrics.SetupLatency.ObserveDuration(c.rt.clock.Now().Sub(started), string(cc.Direction))
	return sess, nil
}

// End concludes a call and releases its capacity slot.
//
// The counterpart to Begin, and the only supported way to finish a call: it is
// what pairs the scheduler release with the lifecycle termination. Calling
// [CallLifecycle.Disconnect] directly ends the call but leaks the slot, which
// is why the lifecycle's terminal helpers are not the documented path.
func (c *CallCoordinator) End(ctx context.Context, id CallID, reason string) error {
	sess, err := c.rt.registry.Get(id)
	if err != nil {
		return err
	}
	provider := sess.Context().Provider

	if err := c.rt.lifecycle.Disconnect(ctx, id, reason); err != nil {
		return err
	}
	c.rt.scheduler.Release(provider)
	if c.rt.store != nil {
		_ = c.rt.store.Delete(ctx, id)
	}
	return nil
}

// Fail concludes a call abnormally and releases its slot.
func (c *CallCoordinator) Fail(ctx context.Context, id CallID, reason string) error {
	sess, err := c.rt.registry.Get(id)
	if err != nil {
		return err
	}
	provider := sess.Context().Provider

	if err := c.rt.lifecycle.Fail(ctx, id, reason); err != nil {
		return err
	}
	c.rt.scheduler.Release(provider)
	if c.rt.store != nil {
		_ = c.rt.store.Delete(ctx, id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// CallDispatcher
// ---------------------------------------------------------------------------

// CallDispatcher delivers provider signals to the right call.
//
// # It exists so a late or unknown signal is not an error
//
// Carriers send callbacks out of order, twice, and after a call has ended. A
// dispatcher that returned an error for each would fill the logs with entries
// nobody can act on, and a real fault would be invisible among them.
//
// This one classifies: a signal for an unknown call is DROPPED and counted; a
// signal that is not a legal transition for the call's current state is
// DISCARDED and counted; only a signal that should have worked and did not is
// an error.
type CallDispatcher struct{ rt *TelephonyRuntime }

// Dispatcher returns the runtime's dispatcher.
func (r *TelephonyRuntime) Dispatcher() *CallDispatcher {
	return &CallDispatcher{rt: r}
}

// SignalOutcome is what the dispatcher did with a signal.
type SignalOutcome string

// The signal outcomes.
const (
	// SignalApplied means the transition happened.
	SignalApplied SignalOutcome = "applied"
	// SignalUnknownCall means no such call — a callback for a call that
	// already concluded and was removed.
	SignalUnknownCall SignalOutcome = "unknown_call"
	// SignalNotApplicable means the transition is not legal from the call's
	// current state — a duplicate or out-of-order callback.
	SignalNotApplicable SignalOutcome = "not_applicable"
	// SignalRejected means the transition should have worked and did not.
	SignalRejected SignalOutcome = "rejected"
)

// Dispatch applies a provider signal to a call.
//
// Returns the outcome and an error only for [SignalRejected]. A caller that
// treats every non-applied outcome as a failure will be wrong most of the time.
func (d *CallDispatcher) Dispatch(ctx context.Context, id CallID,
	to CallState, reason string) (SignalOutcome, error) {
	sess, err := d.rt.registry.Get(id)
	if err != nil {
		d.rt.metrics.InvalidTransitions.Inc("unknown", string(to))
		return SignalUnknownCall, nil
	}

	from := sess.State()
	if !CanTransition(from, to) {
		// Not an error. A carrier that sends "ringing" after we already
		// connected is a carrier, not a bug.
		d.rt.metrics.InvalidTransitions.Inc(string(from), string(to))
		return SignalNotApplicable, nil
	}

	if err := d.rt.lifecycle.transition(ctx, sess, to, reason); err != nil {
		return SignalRejected, err
	}
	return SignalApplied, nil
}
