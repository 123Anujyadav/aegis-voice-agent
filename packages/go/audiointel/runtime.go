package audiointel

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// RuntimeState is the runtime's lifecycle position.
type RuntimeState uint8

// The runtime states.
const (
	// RuntimeReady accepts new sessions.
	RuntimeReady RuntimeState = iota
	// RuntimeStopped accepts nothing.
	RuntimeStopped
)

// String implements fmt.Stringer.
func (s RuntimeState) String() string {
	if s == RuntimeStopped {
		return "stopped"
	}
	return "ready"
}

// Option customises a runtime.
type Option func(*options)

type options struct {
	clock   rt.Clock
	logger  *slog.Logger
	metrics *AudioIntelligenceMetrics
	events  EventPublisher
}

// WithClock injects a clock. Every deadline, hangover and latency measurement
// in this engine reads it, so a test advances a FakeClock instead of sleeping.
func WithClock(c rt.Clock) Option { return func(o *options) { o.clock = c } }

// WithLogger attaches a logger.
//
// The engine logs LIFECYCLE only — sessions opening, closing and being refused.
// Nothing per-frame, because fifty log lines a second per session is not a log,
// and nothing carrying audio, a level trace or a classification stream, because
// a debug log is the easiest place for a recording to appear by accident.
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }

// WithMetrics supplies an instrument set, so several subsystems can share one
// scrape.
func WithMetrics(m *AudioIntelligenceMetrics) Option {
	return func(o *options) { o.metrics = m }
}

// WithEventPublisher attaches the event port.
//
// A nil publisher is valid and means events are discarded. [NopEventPublisher]
// says the same thing visibly, which is preferable in a configuration review.
func WithEventPublisher(p EventPublisher) Option { return func(o *options) { o.events = p } }

// AudioIntelligenceRuntime owns the live sessions and the shared instruments.
//
// # It starts no goroutines
//
// There is no pump, no sweeper and no background worker. Analysis happens
// inline on whichever goroutine calls [Session.Analyze], which is what ADR-0004
// §247 requires of the barge-in path and what makes deterministic replay
// possible.
//
// The consequence worth stating: NOTHING HERE EXPIRES A SESSION ON ITS OWN. A
// caller that opens sessions and never closes them will reach MaxSessions and
// stay there. That is deliberate — a sweeper would need a goroutine and a
// policy about what "idle" means, and the caller already knows when its call
// ended. [SessionRegistry.Each] is provided so a supervising service can
// implement whatever expiry policy it wants.
type AudioIntelligenceRuntime struct {
	cfg      Config
	clock    rt.Clock
	logger   *slog.Logger
	metrics  *AudioIntelligenceMetrics
	events   EventPublisher
	registry *SessionRegistry

	mu    sync.RWMutex
	state RuntimeState
}

// New builds a runtime.
//
// The configuration is validated here and copied by value into every session,
// so a session's configuration cannot be mutated underneath it by a
// reconfiguration this package deliberately does not offer.
func New(cfg Config, opts ...Option) (*AudioIntelligenceRuntime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	o := options{clock: rt.SystemClock{}, logger: slog.Default()}
	for _, apply := range opts {
		apply(&o)
	}
	if o.clock == nil {
		o.clock = rt.SystemClock{}
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}
	if o.metrics == nil {
		o.metrics = NewAudioIntelligenceMetrics()
	}

	return &AudioIntelligenceRuntime{
		cfg:      cfg,
		clock:    o.clock,
		logger:   o.logger,
		metrics:  o.metrics,
		events:   o.events,
		registry: NewSessionRegistry(),
		state:    RuntimeReady,
	}, nil
}

// Open creates a session for one direction of one call's audio.
func (r *AudioIntelligenceRuntime) Open(ctx context.Context, sc SessionContext) (*Session, error) {
	if err := sc.Validate(); err != nil {
		return nil, err
	}
	if sc.Format != r.cfg.Format {
		return nil, fmt.Errorf("%w: session is %s, runtime is configured for %s",
			ErrFormatMismatch, sc.Format, r.cfg.Format)
	}

	r.mu.RLock()
	stopped := r.state == RuntimeStopped
	r.mu.RUnlock()

	if stopped {
		return nil, ErrRuntimeStopped
	}

	// CAPACITY IS CHECKED BEFORE THE ANALYSER IS BUILT. A session's fixed
	// windows are allocated at construction, so building one and then refusing
	// it would allocate exactly the memory the bound exists to protect.
	if r.registry.Len() >= r.cfg.MaxSessions {
		r.logger.WarnContext(ctx, "audiointel: refusing session, at capacity",
			slog.Int("live", r.registry.Len()),
			slog.Int("max", r.cfg.MaxSessions))
		return nil, ErrAtCapacity
	}

	analyzer, err := NewAudioAnalyzer(r.cfg, r.clock)
	if err != nil {
		return nil, err
	}

	s := &Session{
		id:        NewSessionID(),
		ctx:       sc,
		clock:     r.clock,
		metrics:   r.metrics,
		events:    r.events,
		direction: string(sc.Direction),
		analyzer:  analyzer,
		createdAt: r.clock.Now(),
		onClose:   func(id SessionID) { r.registry.Remove(id) },
	}

	if err := r.registry.Register(s); err != nil {
		return nil, err
	}

	r.metrics.SessionsOpened.Add(1, string(sc.Direction))
	r.metrics.SessionsActive.Add(1)

	r.logger.DebugContext(ctx, "audiointel: session opened",
		slog.String("session", string(s.id)),
		slog.String("direction", string(sc.Direction)))

	return s, nil
}

// Get returns a live session by identifier.
func (r *AudioIntelligenceRuntime) Get(id SessionID) (*Session, error) {
	return r.registry.Get(id)
}

// Stop closes every live session and refuses new ones.
//
// Returns how many were closed. Idempotent.
func (r *AudioIntelligenceRuntime) Stop(ctx context.Context) (int, error) {
	r.mu.Lock()
	if r.state == RuntimeStopped {
		r.mu.Unlock()
		return 0, nil
	}
	r.state = RuntimeStopped
	r.mu.Unlock()

	// Collected first, then closed. Closing from inside Each would call
	// Registry.Remove while Each holds the shard's read lock, which deadlocks —
	// the hazard Each's own documentation names.
	var live []*Session
	r.registry.Each(func(s *Session) bool {
		live = append(live, s)
		return true
	})

	for _, s := range live {
		_ = s.Close(ctx, ReasonRuntimeStopped)
	}

	r.logger.InfoContext(ctx, "audiointel: runtime stopped",
		slog.Int("sessions_closed", len(live)))

	return len(live), nil
}

// State returns the runtime's lifecycle position.
func (r *AudioIntelligenceRuntime) State() RuntimeState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state
}

// Registry exposes the live sessions, for a service implementing its own expiry
// policy.
func (r *AudioIntelligenceRuntime) Registry() *SessionRegistry { return r.registry }

// Metrics returns the instrument set.
func (r *AudioIntelligenceRuntime) Metrics() *AudioIntelligenceMetrics { return r.metrics }

// Clock returns the injected clock.
func (r *AudioIntelligenceRuntime) Clock() rt.Clock { return r.clock }

// Config returns the runtime's configuration.
func (r *AudioIntelligenceRuntime) Config() Config { return r.cfg }

// Live returns how many sessions are open.
func (r *AudioIntelligenceRuntime) Live() int { return r.registry.Len() }

// Opened returns how many sessions have ever been opened.
func (r *AudioIntelligenceRuntime) Opened() uint64 { return r.registry.Total() }

// Utilisation returns live sessions over capacity, in [0,1].
func (r *AudioIntelligenceRuntime) Utilisation() float64 {
	if r.cfg.MaxSessions == 0 {
		return 0
	}
	return float64(r.registry.Len()) / float64(r.cfg.MaxSessions)
}

// RuntimeStats is a consistent view of the runtime.
type RuntimeStats struct {
	State       RuntimeState
	Live        int
	Opened      uint64
	Capacity    int
	Utilisation float64
	Uptime      time.Duration
}

// String renders the stats.
func (s RuntimeStats) String() string {
	return fmt.Sprintf("runtime %s live=%d/%d (%.0f%%) opened=%d",
		s.State, s.Live, s.Capacity, s.Utilisation*100, s.Opened)
}

// Stats returns a consistent snapshot.
func (r *AudioIntelligenceRuntime) Stats() RuntimeStats {
	return RuntimeStats{
		State:       r.State(),
		Live:        r.registry.Len(),
		Opened:      r.registry.Total(),
		Capacity:    r.cfg.MaxSessions,
		Utilisation: r.Utilisation(),
	}
}
