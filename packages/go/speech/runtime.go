package speech

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Config configures the speech runtime.
type Config struct {
	// MaxSessions bounds concurrent sessions.
	MaxSessions int

	// Format is the audio carried in both directions.
	Format media.AudioFormat

	// Language is the default when a session does not name one.
	Language Language

	// STT and TTS configure the two orchestrators.
	STT STTOrchestratorConfig
	TTS TTSOrchestratorConfig

	// Router configures provider health and circuit breaking.
	Router RouterConfig

	// CloseTimeout bounds session shutdown.
	CloseTimeout time.Duration
}

// DefaultConfig returns the telephony baseline.
func DefaultConfig() Config {
	format := media.PCM16Mono8k()
	return Config{
		MaxSessions:  1_000,
		Format:       format,
		Language:     LangEnglishIN,
		STT:          DefaultSTTOrchestratorConfig(format),
		TTS:          DefaultTTSOrchestratorConfig(format),
		Router:       DefaultRouterConfig(),
		CloseTimeout: 2 * time.Second,
	}
}

// TestConfig returns [DefaultConfig] with the shutdown budget shortened.
//
// EXPORTED BECAUSE THE ALTERNATIVE IS A FOOTGUN, the same lesson Phase 11B
// recorded: a runtime stopped with live sessions waits its budget out in real
// time, and a test that built its own Config from DefaultConfig would silently
// reintroduce a multi-second shutdown.
func TestConfig() Config {
	cfg := DefaultConfig()
	cfg.CloseTimeout = 50 * time.Millisecond
	return cfg
}

func (c Config) validate() []string {
	var problems []string
	if c.MaxSessions <= 0 {
		problems = append(problems, "config: MaxSessions must be positive")
	}
	if err := c.Format.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	if c.CloseTimeout <= 0 {
		problems = append(problems, "config: CloseTimeout must be positive")
	}
	problems = append(problems, c.STT.validate()...)
	problems = append(problems, c.TTS.validate()...)
	problems = append(problems, c.Router.validate()...)

	// A session whose orchestrators disagree with the runtime about format
	// would fail on its first frame, at which point the call is already live.
	if c.STT.Format != c.Format {
		problems = append(problems, fmt.Sprintf(
			"config: STT format %s does not match runtime format %s", c.STT.Format, c.Format))
	}
	if c.TTS.Format != c.Format {
		problems = append(problems, fmt.Sprintf(
			"config: TTS format %s does not match runtime format %s", c.TTS.Format, c.Format))
	}
	return problems
}

// Option customises a runtime.
type Option func(*options)

type options struct {
	clock   rt.Clock
	logger  *slog.Logger
	metrics *SpeechMetrics
	events  EventPublisher
}

// WithClock injects a clock. Everything timed measures against it.
func WithClock(c rt.Clock) Option { return func(o *options) { o.clock = c } }

// WithLogger attaches a logger.
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }

// WithMetrics attaches an instrument set.
func WithMetrics(m *SpeechMetrics) Option { return func(o *options) { o.metrics = m } }

// WithEventPublisher attaches the event port.
func WithEventPublisher(p EventPublisher) Option { return func(o *options) { o.events = p } }

// SpeechRuntime owns speech sessions.
//
// # No global state
//
// Metrics, router and registry are all runtime-scoped. Two runtimes in one
// process share nothing, which is what makes the test suite parallel-safe and
// what lets a service run an isolated runtime per tenant if it ever needs to.
type SpeechRuntime struct {
	cfg     Config
	clock   rt.Clock
	logger  *slog.Logger
	metrics *SpeechMetrics
	events  EventPublisher
	router  *ProviderRouter

	mu       sync.RWMutex
	sessions map[SessionID]*SpeechSession
	started  bool
	stopped  bool

	opened atomic.Uint64
	shed   atomic.Uint64
}

// New builds a speech runtime.
func New(cfg Config, opts ...Option) (*SpeechRuntime, error) {
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
		m = NewSpeechMetrics()
	}
	events := o.events
	if events == nil {
		events = NopEventPublisher{}
	}

	router, err := NewProviderRouter(cfg.Router, clock, m)
	if err != nil {
		return nil, err
	}

	return &SpeechRuntime{
		cfg: cfg, clock: clock, logger: logger, metrics: m, events: events,
		router:   router,
		sessions: make(map[SessionID]*SpeechSession),
	}, nil
}

// Router returns the provider router, for registration.
func (r *SpeechRuntime) Router() *ProviderRouter { return r.router }

// Metrics returns the instrument set.
func (r *SpeechRuntime) Metrics() *SpeechMetrics { return r.metrics }

// Config returns the configuration.
func (r *SpeechRuntime) Config() Config { return r.cfg }

// Start marks the runtime ready.
func (r *SpeechRuntime) Start(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return fmt.Errorf("%w: runtime already stopped", ErrSpeechSessionClosed)
	}
	r.started = true
	return nil
}

// Live returns how many sessions are open.
func (r *SpeechRuntime) Live() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.sessions)
}

// Opened returns how many sessions have been opened.
func (r *SpeechRuntime) Opened() uint64 { return r.opened.Load() }

// Shed returns how many sessions were refused for capacity.
func (r *SpeechRuntime) Shed() uint64 { return r.shed.Load() }

// Open creates a session.
//
// Refuses beyond MaxSessions rather than queueing: each session pre-allocates
// two bounded queues, so admitting beyond capacity does not degrade gracefully,
// it allocates memory the process does not have.
func (r *SpeechRuntime) Open(ctx context.Context, sc SessionContext) (*SpeechSession, error) {
	if err := sc.Validate(); err != nil {
		return nil, err
	}
	if sc.Format != r.cfg.Format {
		return nil, fmt.Errorf("%w: session is %s, runtime carries %s",
			ErrInvalidAudio, sc.Format, r.cfg.Format)
	}
	if sc.Language == LangUnknown {
		sc.Language = r.cfg.Language
	}
	if sc.Prosody == (Prosody{}) {
		sc.Prosody = DefaultProsody()
	}

	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return nil, ErrSpeechSessionClosed
	}
	if len(r.sessions) >= r.cfg.MaxSessions {
		r.mu.Unlock()
		r.shed.Add(1)
		return nil, fmt.Errorf("%w: %d sessions is capacity",
			ErrBackpressure, r.cfg.MaxSessions)
	}
	r.mu.Unlock()

	id := NewSessionID()
	sessCtx, cancel := context.WithCancel(ctx)

	assembler := NewTranscriptAssembler(id, r.clock)
	turns := NewSpeechTurnManager(r.clock)

	sttCfg := r.cfg.STT
	sttCfg.Format = sc.Format
	stt, err := NewSTTOrchestrator(sttCfg, r.router, assembler, turns, r.clock, r.metrics)
	if err != nil {
		cancel()
		return nil, err
	}

	ttsCfg := r.cfg.TTS
	ttsCfg.Format = sc.Format
	ttsCfg.Voice = sc.Voice
	ttsCfg.Prosody = sc.Prosody
	tts, err := NewTTSOrchestrator(ttsCfg, r.router, turns, id, r.clock, r.metrics)
	if err != nil {
		cancel()
		_ = stt.Close()
		return nil, err
	}

	s := &SpeechSession{
		id: id, ctx: sc, clock: r.clock, metrics: r.metrics, events: r.events,
		turns: turns, assembler: assembler, stt: stt, tts: tts,
		cancel: cancel, done: make(chan struct{}),
		createdAt: r.clock.Now(),
		onClose:   r.deregister,
	}

	r.mu.Lock()
	r.sessions[id] = s
	r.mu.Unlock()

	r.opened.Add(1)
	r.metrics.SessionsActive.Add(1)
	r.metrics.SessionsOpened.Add(1, sc.Language.Label())
	s.publish(EventSpeechSessionCreated, "", "", nil)

	_ = sessCtx
	return s, nil
}

// deregister removes a session from the registry. Called by the session itself
// as it closes, so closing a session by any route frees its slot.
func (r *SpeechRuntime) deregister(id SessionID) {
	r.mu.Lock()
	delete(r.sessions, id)
	r.mu.Unlock()
}

// Session returns a session by identifier.
func (r *SpeechRuntime) Session(id SessionID) (*SpeechSession, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sessions[id]
	return s, ok
}

// Close ends one session and removes it.
func (r *SpeechRuntime) Close(ctx context.Context, id SessionID, reason string) error {
	r.mu.Lock()
	s := r.sessions[id]
	delete(r.sessions, id)
	r.mu.Unlock()

	if s == nil {
		return fmt.Errorf("%w: no session %s", ErrSpeechSessionClosed, id)
	}
	return s.Close(ctx, reason)
}

// Stop ends every session and returns how many were closed.
//
// No goroutine survives this: every session closes both orchestrators, and both
// wait for their pump goroutines before returning.
func (r *SpeechRuntime) Stop(ctx context.Context) (int, error) {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return 0, nil
	}
	r.stopped = true
	live := make([]*SpeechSession, 0, len(r.sessions))
	for _, s := range r.sessions {
		live = append(live, s)
	}
	r.sessions = make(map[SessionID]*SpeechSession)
	r.mu.Unlock()

	for _, s := range live {
		_ = s.Close(ctx, "runtime_stop")
	}
	return len(live), nil
}
