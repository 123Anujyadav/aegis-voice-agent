package media

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// RuntimeState is the runtime's own lifecycle stage.
type RuntimeState uint8

// The runtime states.
const (
	RuntimeNew RuntimeState = iota
	RuntimeRunning
	RuntimeDraining
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

// Config is the media runtime's tuning.
//
// Every field is a duration or a count, every one is validated, and there is no
// environment or file access anywhere in this package.
type Config struct {
	// MaxStreams bounds live streams. Zero is refused: an unbounded media
	// runtime discovers its memory limit during a traffic spike, and each
	// stream pre-allocates its buffer.
	MaxStreams int

	// MaxStreamsPerSource bounds live streams per source, so one misbehaving
	// source cannot consume the whole runtime. Zero means no per-source limit.
	MaxStreamsPerSource int

	// StallTimeout is how long a stream may go without an accepted frame
	// before it is moved to Timeout.
	StallTimeout time.Duration

	// OpenTimeout bounds how long a stream may sit in Opening.
	OpenTimeout time.Duration

	// RecoveryTimeout bounds how long a stream may sit in Recovering.
	RecoveryTimeout time.Duration

	// DrainTimeout bounds graceful shutdown, in REAL time — see
	// [MediaRuntime.drain].
	DrainTimeout time.Duration

	// SweepInterval is how often the stall sweeper runs.
	SweepInterval time.Duration

	// PumpInterval is how often the runtime moves due frames from jitter
	// buffers into output rings. Zero disables the runtime pump, leaving the
	// caller to drive [Stream.Pump] itself — which a consumer with its own
	// cadence should do.
	PumpInterval time.Duration

	// Snapshot controls stream snapshots.
	Snapshot SnapshotConfig

	// Pipeline is the default pipeline configuration for new streams.
	Pipeline PipelineConfig
}

// DefaultConfig returns the platform baseline for 8 kHz mono telephony.
//
// The durations come from what audio needs:
//
//   - StallTimeout 2s — a source silent for two seconds has stopped, and a
//     conversation cannot absorb longer than that undetected.
//   - PumpInterval 10ms — half a frame, so the pump never becomes the reason a
//     frame is late.
//   - SweepInterval 500ms — a quarter of the stall timeout, so a stall is
//     detected within 25% of its deadline.
func DefaultConfig() Config {
	format := PCM16Mono8k()
	return Config{
		MaxStreams:          2_000,
		MaxStreamsPerSource: 1_000,
		StallTimeout:        2 * time.Second,
		OpenTimeout:         5 * time.Second,
		RecoveryTimeout:     10 * time.Second,
		DrainTimeout:        5 * time.Second,
		SweepInterval:       500 * time.Millisecond,
		PumpInterval:        10 * time.Millisecond,
		Snapshot:            DefaultSnapshotConfig(),
		Pipeline:            DefaultPipelineConfig(format),
	}
}

func (c Config) validate() []string {
	var problems []string

	if c.MaxStreams <= 0 {
		problems = append(problems, "config: MaxStreams must be positive; each stream "+
			"pre-allocates a buffer, so an unbounded runtime discovers its memory "+
			"limit during a traffic spike")
	}
	if c.MaxStreamsPerSource < 0 {
		problems = append(problems, "config: MaxStreamsPerSource must not be negative")
	}
	if c.MaxStreamsPerSource > c.MaxStreams && c.MaxStreams > 0 {
		problems = append(problems, fmt.Sprintf(
			"config: MaxStreamsPerSource (%d) exceeds MaxStreams (%d), so it can never bind",
			c.MaxStreamsPerSource, c.MaxStreams))
	}

	for _, d := range []struct {
		name  string
		value time.Duration
	}{
		{"StallTimeout", c.StallTimeout},
		{"OpenTimeout", c.OpenTimeout},
		{"RecoveryTimeout", c.RecoveryTimeout},
		{"DrainTimeout", c.DrainTimeout},
		{"SweepInterval", c.SweepInterval},
	} {
		if d.value <= 0 {
			problems = append(problems, fmt.Sprintf("config: %s must be positive", d.name))
		}
	}
	if c.PumpInterval < 0 {
		problems = append(problems, "config: PumpInterval must not be negative")
	}

	// The sweeper cannot enforce a deadline shorter than its own period.
	if c.SweepInterval > 0 && c.StallTimeout > 0 && c.SweepInterval > c.StallTimeout {
		problems = append(problems, fmt.Sprintf(
			"config: SweepInterval (%s) exceeds StallTimeout (%s), so a stall cannot be "+
				"detected closer than one sweep period", c.SweepInterval, c.StallTimeout))
	}

	// A pump slower than the frame cadence guarantees late delivery.
	if c.PumpInterval > 0 && c.Pipeline.FrameInterval > 0 &&
		c.PumpInterval > c.Pipeline.FrameInterval {
		problems = append(problems, fmt.Sprintf(
			"config: PumpInterval (%s) exceeds the frame interval (%s), so the pump "+
				"itself would make every frame late", c.PumpInterval, c.Pipeline.FrameInterval))
	}

	problems = append(problems, c.Pipeline.validate()...)
	return problems
}

// Option customises a runtime.
type Option func(*options)

type options struct {
	clock       rt.Clock
	logger      *slog.Logger
	metrics     *MediaMetrics
	store       StreamStore
	sourceCheck SourceCheck
}

// WithClock injects a clock. A FakeClock makes every deadline testable without
// sleeping.
func WithClock(c rt.Clock) Option { return func(o *options) { o.clock = c } }

// WithLogger injects a logger.
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }

// WithMetrics injects an instrument set, so a service can share one registry.
func WithMetrics(m *MediaMetrics) Option { return func(o *options) { o.metrics = m } }

// WithStreamStore injects the durable snapshot store.
func WithStreamStore(s StreamStore) Option { return func(o *options) { o.store = s } }

// MediaRuntime owns every stream in one process.
//
// # It owns everything and shares nothing
//
// Two runtimes in one process have separate registries, schedulers, metrics and
// stores. There is no package-level mutable state anywhere in this module,
// which is what makes the test suite parallel-safe and horizontal scaling a
// deployment decision rather than a code change.
type MediaRuntime struct {
	cfg     Config
	clock   rt.Clock
	logger  *slog.Logger
	metrics *MediaMetrics
	store   StreamStore

	registry    *MediaRegistry
	scheduler   *MediaScheduler
	sourceCheck SourceCheck

	mu    sync.RWMutex
	state RuntimeState

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	opened    atomic.Uint64
	shed      atomic.Uint64
	recovered atomic.Uint64
}

// New builds a media runtime.
func New(cfg Config, opts ...Option) (*MediaRuntime, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}

	o := &options{}
	for _, opt := range opts {
		opt(o)
	}

	clock := o.clock
	if clock == nil {
		clock = rt.SystemClock{}
	}
	logger := o.logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	m := o.metrics
	if m == nil {
		m = NewMediaMetrics()
	}

	registry := NewMediaRegistry()

	check := o.sourceCheck
	if check == nil {
		check = AssumeDetached
	}

	return &MediaRuntime{
		cfg: cfg, clock: clock, logger: logger, metrics: m, store: o.store,
		sourceCheck: check,
		registry:    registry,
		scheduler:   NewMediaScheduler(cfg, registry, m),
		state:       RuntimeNew,
		stop:        make(chan struct{}),
	}, nil
}

// Start recovers streams and begins accepting new ones.
func (r *MediaRuntime) Start(ctx context.Context) (RecoveryReport, error) {
	r.mu.Lock()
	if r.state != RuntimeNew {
		state := r.state
		r.mu.Unlock()
		return RecoveryReport{}, fmt.Errorf("media: runtime already %s", state)
	}
	r.mu.Unlock()

	// Recovery BEFORE admission opens, so a recovered stream cannot lose its
	// capacity slot to a new one that arrived first.
	report, err := r.recoverStreams(ctx)
	if err != nil {
		return report, err
	}
	r.recovered.Add(uint64(report.Resumed))

	r.mu.Lock()
	r.state = RuntimeRunning
	r.mu.Unlock()

	r.wg.Add(1)
	go r.sweepLoop()

	if r.cfg.PumpInterval > 0 {
		r.wg.Add(1)
		go r.pumpLoop()
	}

	r.logger.InfoContext(ctx, "media runtime started",
		slog.Int("recovered", report.Resumed), slog.Int("concluded", report.Concluded))
	return report, nil
}

// Stop drains and shuts down. Idempotent.
//
// Returns the number of streams still live at the drain deadline. Non-zero is
// not an error but is worth alerting on: those streams were abandoned with
// audio in flight.
func (r *MediaRuntime) Stop(ctx context.Context) (int, error) {
	r.mu.Lock()
	if r.state == RuntimeStopped {
		r.mu.Unlock()
		return 0, nil
	}
	r.state = RuntimeDraining
	r.mu.Unlock()

	abandoned := r.drain(ctx)

	if r.store != nil {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.cfg.DrainTimeout)
		if n, err := r.snapshotAll(sctx); err != nil {
			r.logger.ErrorContext(ctx, "shutdown snapshot failed; live streams will not recover",
				slog.String("error", err.Error()))
		} else if n > 0 {
			r.logger.InfoContext(ctx, "snapshotted live streams", slog.Int("count", n))
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

// drain waits for live streams to finish, up to the drain timeout.
//
// # The budget is REAL time, not the injected clock
//
// Every stream deadline is measured against the injected clock so a test can
// advance it. This one is not, and the distinction is load-bearing: Phase 11A
// shipped a drain that took its deadline from the injected clock while polling
// with a real ticker, and under a FakeClock nobody advances the deadline never
// arrived and Stop spun forever. A graceful shutdown that never terminates is
// worse than an abrupt one — the orchestrator waits out its grace period and
// sends SIGKILL anyway, having wasted it.
//
// This budget is an operational allowance, not media semantics.
func (r *MediaRuntime) drain(ctx context.Context) int {
	deadline := time.Now().Add(r.cfg.DrainTimeout)

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
			r.logger.WarnContext(ctx, "drain deadline expired with streams still live",
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

// sweepLoop runs the stall sweeper and the state census.
func (r *MediaRuntime) sweepLoop() {
	defer r.wg.Done()

	ticker := r.clock.NewTicker(r.cfg.SweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C():
			ctx, cancel := context.WithTimeout(context.Background(), r.cfg.SweepInterval)
			r.Sweep(ctx)
			cancel()
		}
	}
}

// pumpLoop moves due frames from jitter buffers into output rings.
func (r *MediaRuntime) pumpLoop() {
	defer r.wg.Done()

	ticker := r.clock.NewTicker(r.cfg.PumpInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C():
			r.PumpAll()
		}
	}
}

// PumpAll pumps every live stream and returns the total frames delivered.
//
// Exported so a test drives it directly. A pump reachable only via its own
// ticker would force every delivery test to wait in real time, which is exactly
// what the injected clock exists to avoid.
func (r *MediaRuntime) PumpAll() int {
	var total int
	r.registry.Each(func(s *Stream) bool {
		// Drops are sampled across the pump because a frame discarded here — the
		// output ring was full — has no caller to be reported to. Counting only
		// the push path makes a stalled consumer look like a healthy stream that
		// simply delivered less.
		before := s.Pipeline().DroppedTotal()
		n := s.Pump()
		dropped := s.Pipeline().DroppedTotal() - before

		dir := string(s.Context().Direction)
		if n > 0 {
			total += n
			r.metrics.FramesDelivered.Add(float64(n), dir)
			r.metrics.PumpBatch.Observe(float64(n))
		}
		if dropped > 0 {
			r.metrics.FramesDropped.Add(float64(dropped), dir, string(DropBufferFull))
		}
		return true
	})
	return total
}

// Sweep runs one maintenance pass: stall detection and the state census.
func (r *MediaRuntime) Sweep(ctx context.Context) int {
	now := r.clock.Now()
	var stalled int

	r.registry.Each(func(s *Stream) bool {
		deadline := r.deadlineFor(s.State())
		if deadline <= 0 {
			return true
		}
		if now.Sub(s.LastActivity()) < deadline {
			return true
		}
		if err := r.transition(s, StateTimeout, "stall_deadline"); err == nil {
			stalled++
		}
		return true
	})

	counts := r.registry.ByState()
	r.metrics.ObserveStates(counts)
	r.metrics.LiveStreams.Set(float64(r.registry.Len()))
	return stalled
}

// deadlineFor returns how long a stream may sit in a state.
//
// Zero means no deadline. Active has one — the stall timeout — because a
// source that stopped producing is the fault this engine most needs to notice.
// Paused has none: a pause is a deliberate act and expiring it would fight the
// caller.
func (r *MediaRuntime) deadlineFor(s StreamState) time.Duration {
	switch s {
	case StateIdle, StateOpening:
		return r.cfg.OpenTimeout
	case StateActive:
		return r.cfg.StallTimeout
	case StateRecovering:
		return r.cfg.RecoveryTimeout
	default:
		// Paused, Closing, Timeout and the terminal states: no deadline.
		return 0
	}
}

// transition moves a stream and records the metric.
func (r *MediaRuntime) transition(s *Stream, to StreamState, reason string) error {
	from := s.State()
	if err := s.Transition(to, reason); err != nil {
		r.metrics.InvalidTransitions.Inc(string(from), string(to))
		return err
	}
	r.metrics.Transitions.Inc(string(from), string(to))
	return nil
}

// State returns the runtime's stage.
func (r *MediaRuntime) State() RuntimeState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state
}

func (r *MediaRuntime) accepting() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state == RuntimeRunning
}

// Registry returns the stream registry.
func (r *MediaRuntime) Registry() *MediaRegistry { return r.registry }

// Scheduler returns the admission scheduler.
func (r *MediaRuntime) Scheduler() *MediaScheduler { return r.scheduler }

// Metrics returns the instrument set.
func (r *MediaRuntime) Metrics() *MediaMetrics { return r.metrics }

// Clock returns the injected clock.
func (r *MediaRuntime) Clock() rt.Clock { return r.clock }

// Config returns the configuration in force.
func (r *MediaRuntime) Config() Config { return r.cfg }

// Live returns the live stream count.
func (r *MediaRuntime) Live() int { return r.registry.Len() }

// Opened returns how many streams this runtime has opened.
func (r *MediaRuntime) Opened() uint64 { return r.opened.Load() }

// Shed returns how many streams this runtime has refused.
func (r *MediaRuntime) Shed() uint64 { return r.shed.Load() }

// Recovered returns how many streams this runtime resumed at start-up.
func (r *MediaRuntime) Recovered() uint64 { return r.recovered.Load() }

// ---------------------------------------------------------------------------
// MediaScheduler
// ---------------------------------------------------------------------------

// AdmissionDecision is the outcome of an admission request.
type AdmissionDecision struct {
	Admitted bool
	Reason   string
	Live     int
	Capacity int
}

// String renders the decision.
func (d AdmissionDecision) String() string {
	if d.Admitted {
		return fmt.Sprintf("admitted (%d/%d)", d.Live, d.Capacity)
	}
	return fmt.Sprintf("refused: %s (%d/%d)", d.Reason, d.Live, d.Capacity)
}

// MediaScheduler decides whether a stream may be admitted.
//
// # It refuses, it does not queue
//
// Same reasoning as Phase 11A's call scheduler, with an extra edge: each stream
// pre-allocates its ring buffer, so admitting beyond capacity does not degrade
// gracefully — it allocates memory the process does not have. A refusal is
// immediate and the caller can fail the call cleanly; a queue would hold a
// carrier waiting for memory that is not coming.
type MediaScheduler struct {
	cfg      Config
	registry *MediaRegistry
	metrics  *MediaMetrics

	mu sync.RWMutex
	// perSource is maintained incrementally: it is read on every admission.
	perSource map[SourceID]int
}

// NewMediaScheduler builds a scheduler.
func NewMediaScheduler(cfg Config, reg *MediaRegistry, m *MediaMetrics) *MediaScheduler {
	return &MediaScheduler{cfg: cfg, registry: reg, metrics: m,
		perSource: make(map[SourceID]int)}
}

// Admit decides whether a stream may open, and reserves a slot if so.
//
// Reservation and decision are ONE atomic step. Checking then reserving lets N
// goroutines observe capacity for one slot and all take it — precisely under
// the burst where it matters.
func (s *MediaScheduler) Admit(source SourceID) AdmissionDecision {
	capacity := s.cfg.MaxStreams

	s.mu.Lock()
	live := s.registry.Len()
	decision := AdmissionDecision{Live: live, Capacity: capacity}

	switch {
	case live >= capacity:
		decision.Reason = "capacity_exceeded"
	case s.cfg.MaxStreamsPerSource > 0 && s.perSource[source] >= s.cfg.MaxStreamsPerSource:
		decision.Reason = "source_capacity_exceeded"
	default:
		decision.Admitted = true
		s.perSource[source]++
	}
	s.mu.Unlock()

	return decision
}

// Release returns a source's slot.
//
// Must be called exactly once per admitted stream. A missed release leaks a
// slot and the runtime slowly loses capacity, which is why the coordinator
// calls this from one place on the terminal path.
func (s *MediaScheduler) Release(source SourceID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.perSource[source]; n > 0 {
		s.perSource[source] = n - 1
		if s.perSource[source] == 0 {
			delete(s.perSource, source)
		}
	}
}

// Live returns the live count for a source.
func (s *MediaScheduler) Live(source SourceID) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.perSource[source]
}

// Sources returns the sources with live streams, sorted.
func (s *MediaScheduler) Sources() []SourceID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SourceID, 0, len(s.perSource))
	for src := range s.perSource {
		out = append(out, src)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Utilisation returns live streams over capacity.
func (s *MediaScheduler) Utilisation() float64 {
	if s.cfg.MaxStreams == 0 {
		return 0
	}
	return float64(s.registry.Len()) / float64(s.cfg.MaxStreams)
}

// ---------------------------------------------------------------------------
// MediaCoordinator
// ---------------------------------------------------------------------------

// MediaCoordinator is the runtime's front door.
//
// Admission and lifecycle are separated: [MediaScheduler] decides whether a
// stream may open, [Stream] drives it once it has. The coordinator is the only
// place that does both, and it guarantees the pairing every capacity bug comes
// from — a stream admitted and then failed must release its slot, or the runtime
// slowly loses capacity to streams that never existed.
type MediaCoordinator struct{ rt *MediaRuntime }

// Coordinator returns the runtime's coordinator.
func (r *MediaRuntime) Coordinator() *MediaCoordinator { return &MediaCoordinator{rt: r} }

// Open admits and opens a stream, leaving it Active.
func (c *MediaCoordinator) Open(ctx context.Context, sc StreamContext) (*Stream, error) {
	if !c.rt.accepting() {
		return nil, fmt.Errorf("%w: runtime is %s", ErrRuntimeStopped, c.rt.State())
	}
	if err := sc.Validate(); err != nil {
		return nil, err
	}

	decision := c.rt.scheduler.Admit(sc.Source)
	if !decision.Admitted {
		c.rt.shed.Add(1)
		return nil, fmt.Errorf("%w: %s", ErrBufferFull, decision.Reason)
	}

	cfg := c.rt.cfg.Pipeline
	cfg.Format = sc.Format
	cfg.Buffer.Format = sc.Format

	stream, err := c.rt.registry.Create(sc, cfg, c.rt.clock)
	if err != nil {
		c.rt.scheduler.Release(sc.Source)
		return nil, err
	}

	if err := c.rt.transition(stream, StateOpening, "opening"); err != nil {
		c.rt.registry.Remove(stream.ID())
		c.rt.scheduler.Release(sc.Source)
		return nil, err
	}
	if err := c.rt.transition(stream, StateActive, "opened"); err != nil {
		c.rt.registry.Remove(stream.ID())
		c.rt.scheduler.Release(sc.Source)
		return nil, err
	}

	c.rt.opened.Add(1)
	c.rt.metrics.StreamsOpened.Inc(string(sc.Direction), string(sc.Source))
	c.rt.metrics.LiveStreams.Set(float64(c.rt.registry.Len()))
	return stream, nil
}

// Pause suspends a stream.
func (c *MediaCoordinator) Pause(id StreamID) error {
	s, err := c.rt.registry.Get(id)
	if err != nil {
		return err
	}
	return c.rt.transition(s, StatePaused, "paused")
}

// Resume restarts a paused stream.
func (c *MediaCoordinator) Resume(id StreamID) error {
	s, err := c.rt.registry.Get(id)
	if err != nil {
		return err
	}
	return c.rt.transition(s, StateActive, "resumed")
}

// Drain begins a graceful close: writes are refused, buffered audio remains
// readable.
func (c *MediaCoordinator) Drain(id StreamID) error {
	s, err := c.rt.registry.Get(id)
	if err != nil {
		return err
	}
	return c.rt.transition(s, StateClosing, "draining")
}

// Close concludes a stream and releases its capacity slot.
//
// The counterpart to Open and the only supported way to finish a stream: it
// pairs the scheduler release with the lifecycle termination.
func (c *MediaCoordinator) Close(ctx context.Context, id StreamID, reason string) error {
	return c.conclude(ctx, id, StateClosed, reason)
}

// Fail concludes a stream abnormally and releases its slot.
func (c *MediaCoordinator) Fail(ctx context.Context, id StreamID, reason string) error {
	return c.conclude(ctx, id, StateFailed, reason)
}

func (c *MediaCoordinator) conclude(ctx context.Context, id StreamID,
	to StreamState, reason string) error {
	s, err := c.rt.registry.Get(id)
	if err != nil {
		return err
	}
	sc := s.Context()

	// A stream that has not begun draining must pass through Closing first —
	// the table declares it, and going straight to Closed from Active is not a
	// legal move because it would discard buffered audio without the drain that
	// makes the discard deliberate.
	if to == StateClosed && s.State() != StateClosing {
		if err := c.rt.transition(s, StateClosing, "closing"); err != nil {
			return err
		}
	}

	if err := c.rt.transition(s, to, reason); err != nil {
		return err
	}

	stats := s.Stats()
	c.rt.metrics.StreamDuration.ObserveDuration(stats.Duration, string(sc.Direction), string(to))
	c.rt.metrics.StreamsClosed.Inc(string(sc.Direction), string(to), reason)
	if to == StateFailed {
		c.rt.metrics.StreamsFailed.Inc(string(sc.Source), reason)
	}

	c.rt.registry.Remove(id)
	c.rt.scheduler.Release(sc.Source)
	if c.rt.store != nil {
		_ = c.rt.store.Delete(ctx, id)
	}
	c.rt.metrics.LiveStreams.Set(float64(c.rt.registry.Len()))
	return nil
}

// ---------------------------------------------------------------------------
// MediaDispatcher
// ---------------------------------------------------------------------------

// MediaDispatcher routes frames to streams and records the outcome.
//
// # It exists so the frame path has exactly one entry point
//
// Every frame that enters the engine goes through Dispatch, which means every
// frame is counted, every drop has a reason, and the metric bookkeeping is in
// one place rather than at each producer.
type MediaDispatcher struct{ rt *MediaRuntime }

// Dispatcher returns the runtime's dispatcher.
func (r *MediaRuntime) Dispatcher() *MediaDispatcher { return &MediaDispatcher{rt: r} }

// Dispatch offers a frame to a stream.
//
// Returns the pipeline result and an error only when the stream could not be
// reached or would not accept. An ordinary drop is reported in the result: a
// dropped frame is normal operation under load, and returning an error for it
// would train callers to ignore errors on the hottest path in the platform.
func (d *MediaDispatcher) Dispatch(id StreamID, f Frame) (PipelineResult, error) {
	s, err := d.rt.registry.Get(id)
	if err != nil {
		return PipelineResult{Reason: DropNotAccepting}, err
	}

	dir := string(s.Context().Direction)
	res, err := s.Write(f)
	if err != nil {
		d.rt.metrics.FramesDropped.Inc(dir, string(DropNotAccepting))
		return res, err
	}

	if res.Accepted {
		d.rt.metrics.FramesAccepted.Inc(dir)
		d.rt.metrics.BytesMoved.Add(float64(len(f.Payload)), dir)
		if res.Disposition == FrameReordered {
			d.rt.metrics.Reordered.Inc(dir)
		}
		if res.Lateness > 0 {
			d.rt.metrics.FrameLateness.ObserveDuration(res.Lateness, dir)
		}
	} else {
		d.rt.metrics.FramesDropped.Inc(dir, string(res.Reason))
	}
	return res, nil
}

// ObserveStream records a stream's current buffer and timing state.
//
// Called by the sweeper rather than per frame: these are histogram observations
// and a gauge set, and doing them fifty thousand times a second would cost more
// than the frames do.
func (d *MediaDispatcher) ObserveStream(s *Stream) {
	stats := s.Stats()
	dir := string(stats.Direction)

	d.rt.metrics.BufferDepth.Observe(stats.Pipeline.Buffer.Depth.Seconds(), dir)
	d.rt.metrics.Jitter.Observe(stats.Pipeline.Jitter.Jitter.Seconds(), dir)
	d.rt.metrics.JitterDelay.Observe(stats.Pipeline.Jitter.Delay.Seconds(), dir)
	d.rt.metrics.BufferUtilisation.Set(stats.Pipeline.Buffer.Utilisation())
	d.rt.metrics.DriftRatio.Set(stats.Clock.DriftRatio)
}
