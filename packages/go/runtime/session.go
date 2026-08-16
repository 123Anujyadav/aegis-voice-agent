package runtime

import (
	"context"
	"fmt"
	"hash/maphash"
	"sync"
	"sync/atomic"
	"time"
)

// SessionState is a runtime session's lifecycle state.
type SessionState int

const (
	// SessionInitialising is the state at creation, before the first request.
	SessionInitialising SessionState = iota

	// SessionActive accepts generation requests.
	SessionActive

	// SessionDraining refuses new requests and waits for in-flight ones. The
	// state that makes Invariant I6 possible at session granularity.
	SessionDraining

	// SessionClosed is terminal, ended normally.
	SessionClosed

	// SessionExpired is terminal, ended by TTL.
	SessionExpired

	// SessionFailed is terminal, ended by error.
	SessionFailed
)

// String renders the state for logs and metric labels.
func (s SessionState) String() string {
	switch s {
	case SessionInitialising:
		return "initialising"
	case SessionActive:
		return "active"
	case SessionDraining:
		return "draining"
	case SessionClosed:
		return "closed"
	case SessionExpired:
		return "expired"
	case SessionFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// sessionFSMSpec declares the legal session lifecycle.
//
// Note what is absent: there is no edge from any terminal state, and none from
// Initialising directly to Closed. A session that never became active but must
// end goes through Failed, so "ended without ever working" is distinguishable
// from "ended cleanly" in every metric.
func sessionFSMSpec() FSMSpec[SessionState] {
	return FSMSpec[SessionState]{
		Initial: SessionInitialising,
		Transitions: map[SessionState][]SessionState{
			SessionInitialising: {SessionActive, SessionFailed, SessionExpired},
			SessionActive:       {SessionDraining, SessionClosed, SessionExpired, SessionFailed},
			SessionDraining:     {SessionClosed, SessionFailed, SessionExpired},
		},
		Terminal: []SessionState{SessionClosed, SessionExpired, SessionFailed},
	}
}

// Session is one runtime execution context.
//
// It owns no conversation, no history semantics and no domain meaning. It owns
// a lifetime, a context window, a token budget and a state machine. What the
// messages MEAN is the orchestration layer's business.
type Session struct {
	id      SessionID
	created time.Time
	clock   Clock

	fsm *FSM[SessionState]
	ctx *ContextWindow

	mu       sync.RWMutex
	lastUsed time.Time
	metadata map[string]string

	inFlight atomic.Int64
	requests atomic.Uint64
	usage    struct {
		mu sync.Mutex
		u  Usage
	}
}

// ID returns the session identifier.
func (s *Session) ID() SessionID { return s.id }

// State returns the current lifecycle state.
func (s *Session) State() SessionState { return s.fsm.State() }

// Context returns the session's context window.
func (s *Session) Context() *ContextWindow { return s.ctx }

// CreatedAt returns the creation instant.
func (s *Session) CreatedAt() time.Time { return s.created }

// LastUsed returns when the session last saw activity.
func (s *Session) LastUsed() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastUsed
}

// Touch records activity, deferring TTL expiry.
func (s *Session) Touch() {
	s.mu.Lock()
	s.lastUsed = s.clock.Now()
	s.mu.Unlock()
}

// Metadata returns a copy of the session's metadata.
//
// A copy, not the map: handing out the live map would let a caller mutate
// session state without synchronisation, which is the single most common way a
// "thread-safe" type turns out not to be.
func (s *Session) Metadata() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.metadata))
	for k, v := range s.metadata {
		out[k] = v
	}
	return out
}

// SetMetadata sets one metadata key.
//
// PRIVACY. Metadata is INTERNAL and is attached to traces and logs. It must
// never carry personal data — no phone number, no caller name, no transcript
// text. Correlation identifiers only.
func (s *Session) SetMetadata(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.metadata == nil {
		s.metadata = make(map[string]string, 4)
	}
	s.metadata[key] = value
}

// Activate moves the session to active.
func (s *Session) Activate() error {
	_, err := s.fsm.To(SessionActive)
	return err
}

// BeginRequest reserves the session for a request.
//
// It returns a completion function that must be called exactly once. The
// in-flight count it maintains is what lets Drain know when it is safe to close.
func (s *Session) BeginRequest() (done func(usage Usage), err error) {
	if err := s.fsm.MustBe(SessionActive); err != nil {
		return nil, err
	}
	s.inFlight.Add(1)
	s.requests.Add(1)
	s.Touch()

	var once sync.Once
	return func(u Usage) {
		once.Do(func() {
			s.inFlight.Add(-1)
			s.usage.mu.Lock()
			s.usage.u.Add(u)
			s.usage.mu.Unlock()
			s.Touch()
		})
	}, nil
}

// InFlight reports requests currently executing in this session.
func (s *Session) InFlight() int { return int(s.inFlight.Load()) }

// RequestCount reports total requests begun.
func (s *Session) RequestCount() uint64 { return s.requests.Load() }

// Usage returns accumulated token usage.
func (s *Session) Usage() Usage {
	s.usage.mu.Lock()
	defer s.usage.mu.Unlock()
	return s.usage.u
}

// Drain refuses new requests and waits for in-flight ones to finish.
func (s *Session) Drain(ctx context.Context) error {
	if _, err := s.fsm.To(SessionDraining); err != nil {
		// Already terminal or already draining: not an error for a caller
		// whose intent is "make sure this is drained".
		if s.fsm.IsTerminal() {
			return nil
		}
	}
	ticker := s.clock.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.inFlight.Load() == 0 {
			return nil
		}
		select {
		case <-ticker.C():
		case <-ctx.Done():
			return fmt.Errorf("session %s: drain incomplete, %d in flight: %w",
				s.id, s.inFlight.Load(), ctx.Err())
		}
	}
}

// Close ends the session.
func (s *Session) Close() error {
	if s.fsm.IsTerminal() {
		return nil
	}
	_, err := s.fsm.To(SessionClosed)
	return err
}

// Fail ends the session in the failed state.
func (s *Session) Fail() {
	s.fsm.TryTo(SessionFailed)
}

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

// SessionConfig tunes session management.
type SessionConfig struct {
	// MaxSessions bounds concurrent sessions. Zero means unbounded, which is
	// appropriate only in test.
	MaxSessions int

	// IdleTTL expires a session with no activity. The platform's sessions are
	// tens of seconds long (ADR-0002 §13), so this is short by the standards of
	// a web session and that is correct.
	IdleTTL time.Duration

	// MaxLifetime expires a session regardless of activity, bounding the damage
	// from a session that is touched forever by a stuck consumer.
	MaxLifetime time.Duration

	// SweepInterval is how often expiry runs.
	SweepInterval time.Duration

	// Shards is the number of lock stripes. Zero derives from NumCPU.
	Shards int

	// DefaultContextTokens is the token budget for a new session's context
	// window.
	DefaultContextTokens int
}

// DefaultSessionConfig returns the configuration used unless overridden.
func DefaultSessionConfig() SessionConfig {
	return SessionConfig{
		MaxSessions:          100_000,
		IdleTTL:              2 * time.Minute,
		MaxLifetime:          15 * time.Minute,
		SweepInterval:        5 * time.Second,
		Shards:               nextPow2(NumCPU() * 4),
		DefaultContextTokens: 8192,
	}
}

func (c SessionConfig) validate() []string {
	var p []string
	if c.MaxSessions < 0 {
		p = append(p, "session: MaxSessions cannot be negative")
	}
	if c.IdleTTL <= 0 {
		p = append(p, "session: IdleTTL must be positive")
	}
	if c.MaxLifetime <= 0 {
		p = append(p, "session: MaxLifetime must be positive")
	}
	if c.MaxLifetime < c.IdleTTL {
		p = append(p, "session: MaxLifetime must be at least IdleTTL, or idle expiry can never fire")
	}
	if c.SweepInterval <= 0 {
		p = append(p, "session: SweepInterval must be positive")
	}
	if c.DefaultContextTokens <= 0 {
		p = append(p, "session: DefaultContextTokens must be positive")
	}
	return p
}

// shard is one lock stripe of the session map.
type shard struct {
	mu       sync.RWMutex
	sessions map[SessionID]*Session
}

// SessionManager owns session lifetime.
//
// Sessions are held in a sharded map rather than a sync.Map. sync.Map is
// optimised for append-mostly workloads with stable keys; ours is a
// high-churn create/delete workload with a periodic full scan for expiry, which
// is close to sync.Map's worst case and squarely in a sharded map's best.
type SessionManager struct {
	cfg     SessionConfig
	clock   Clock
	metrics *Metrics

	shards []*shard
	seed   maphash.Seed
	mask   uint64

	count atomic.Int64

	closed  atomic.Bool
	stopped chan struct{}
	wg      sync.WaitGroup

	onExpire func(*Session, string)
}

// NewSessionManager constructs a manager.
func NewSessionManager(cfg SessionConfig, clock Clock, metrics *Metrics) (*SessionManager, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if clock == nil {
		clock = SystemClock{}
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	n := cfg.Shards
	if n <= 0 {
		n = nextPow2(NumCPU() * 4)
	}
	n = nextPow2(n)

	m := &SessionManager{
		cfg:     cfg,
		clock:   clock,
		metrics: metrics,
		shards:  make([]*shard, n),
		seed:    maphash.MakeSeed(),
		mask:    uint64(n - 1),
		stopped: make(chan struct{}),
	}
	for i := range m.shards {
		m.shards[i] = &shard{sessions: make(map[SessionID]*Session)}
	}
	return m, nil
}

// OnExpire registers a callback fired when a session expires. It runs on the
// sweeper goroutine and must not block.
func (m *SessionManager) OnExpire(fn func(s *Session, reason string)) { m.onExpire = fn }

// shardFor selects the stripe for an id.
func (m *SessionManager) shardFor(id SessionID) *shard {
	var h maphash.Hash
	h.SetSeed(m.seed)
	_, _ = h.WriteString(string(id))
	return m.shards[h.Sum64()&m.mask]
}

// Create makes a new session.
func (m *SessionManager) Create(contextTokens int) (*Session, error) {
	if m.closed.Load() {
		return nil, ErrClosed
	}
	if m.cfg.MaxSessions > 0 && m.count.Load() >= int64(m.cfg.MaxSessions) {
		return nil, fmt.Errorf("%w: session limit %d reached", ErrShed, m.cfg.MaxSessions)
	}
	if contextTokens <= 0 {
		contextTokens = m.cfg.DefaultContextTokens
	}

	fsm, err := NewFSM(sessionFSMSpec(), m.clock)
	if err != nil {
		return nil, err
	}

	now := m.clock.Now()
	s := &Session{
		id:       NewSessionID(),
		created:  now,
		lastUsed: now,
		clock:    m.clock,
		fsm:      fsm,
		ctx:      NewContextWindow(contextTokens, m.metrics),
	}

	sh := m.shardFor(s.id)
	sh.mu.Lock()
	sh.sessions[s.id] = s
	sh.mu.Unlock()

	m.count.Add(1)
	m.metrics.SessionsCreated.Inc()
	m.metrics.SessionsActive.Set(float64(m.count.Load()))
	return s, nil
}

// Get returns a session by ID.
func (m *SessionManager) Get(id SessionID) (*Session, error) {
	sh := m.shardFor(id)
	sh.mu.RLock()
	s, ok := sh.sessions[id]
	sh.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: session %s", ErrNotFound, id)
	}
	return s, nil
}

// Remove deletes a session, closing it first if it is not terminal.
func (m *SessionManager) Remove(id SessionID) bool {
	sh := m.shardFor(id)
	sh.mu.Lock()
	s, ok := sh.sessions[id]
	if ok {
		delete(sh.sessions, id)
	}
	sh.mu.Unlock()

	if !ok {
		return false
	}
	if !s.fsm.IsTerminal() {
		_ = s.Close()
	}
	m.count.Add(-1)
	m.metrics.SessionsActive.Set(float64(m.count.Load()))
	m.metrics.SessionLifetime.Observe(m.clock.Since(s.created).Seconds())
	return true
}

// Count reports live sessions.
func (m *SessionManager) Count() int { return int(m.count.Load()) }

// Start begins the expiry sweeper.
func (m *SessionManager) Start() {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := m.clock.NewTicker(m.cfg.SweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C():
				m.sweep()
			case <-m.stopped:
				return
			}
		}
	}()
}

// sweep expires idle and over-age sessions.
//
// It walks one shard at a time, holding only that shard's lock. A single global
// lock held for a full scan would stall every create and lookup for the
// duration, and at a hundred thousand sessions that is a visible latency spike
// once per sweep interval.
func (m *SessionManager) sweep() {
	now := m.clock.Now()

	for _, sh := range m.shards {
		var expired []*Session

		sh.mu.Lock()
		for id, s := range sh.sessions {
			reason := ""
			switch {
			case s.fsm.IsTerminal():
				reason = "terminal"
			case now.Sub(s.LastUsed()) > m.cfg.IdleTTL:
				reason = "idle"
			case now.Sub(s.created) > m.cfg.MaxLifetime:
				reason = "max_lifetime"
			}
			if reason == "" {
				continue
			}
			// A session with work in flight is never reaped, whatever its age.
			// Reaping one mid-request would abandon a live call, and the TTL
			// exists to reclaim abandoned sessions, not to interrupt busy ones.
			if s.InFlight() > 0 {
				continue
			}
			delete(sh.sessions, id)
			s.SetMetadata("expiry_reason", reason)
			expired = append(expired, s)
		}
		sh.mu.Unlock()

		for _, s := range expired {
			reason := s.Metadata()["expiry_reason"]
			if !s.fsm.IsTerminal() {
				s.fsm.TryTo(SessionExpired)
			}
			m.count.Add(-1)
			m.metrics.SessionsExpired.Inc(reason)
			m.metrics.SessionLifetime.Observe(now.Sub(s.created).Seconds())
			if m.onExpire != nil {
				m.onExpire(s, reason)
			}
		}
	}
	m.metrics.SessionsActive.Set(float64(m.count.Load()))
}

// Close stops the sweeper and drains every session.
func (m *SessionManager) Close(ctx context.Context) error {
	if m.closed.Swap(true) {
		return nil
	}
	close(m.stopped)
	m.wg.Wait()

	var firstErr error
	for _, sh := range m.shards {
		sh.mu.Lock()
		sessions := make([]*Session, 0, len(sh.sessions))
		for _, s := range sh.sessions {
			sessions = append(sessions, s)
		}
		sh.sessions = make(map[SessionID]*Session)
		sh.mu.Unlock()

		for _, s := range sessions {
			if err := s.Drain(ctx); err != nil && firstErr == nil {
				firstErr = err
			}
			_ = s.Close()
			m.count.Add(-1)
		}
	}
	m.metrics.SessionsActive.Set(0)
	return firstErr
}

// nextPow2 rounds up to a power of two, minimum 1. Shard counts must be powers
// of two so the shard index is a mask rather than a modulo, which is measurably
// cheaper on a path taken for every lookup.
func nextPow2(n int) int {
	if n <= 1 {
		return 1
	}
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}
