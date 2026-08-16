package speech

import (
	"fmt"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Tier is a provider's routing preference.
type Tier int

// The tiers.
const (
	// TierPrimary is tried first.
	TierPrimary Tier = iota
	// TierSecondary is the failover target.
	TierSecondary
)

// String implements fmt.Stringer.
func (t Tier) String() string {
	if t == TierSecondary {
		return "secondary"
	}
	return "primary"
}

// CircuitState is a provider's breaker state.
type CircuitState string

// The circuit states.
const (
	// CircuitClosed is healthy: requests flow.
	CircuitClosed CircuitState = "closed"

	// CircuitOpen refuses immediately.
	//
	// THE POINT IS TO FAIL FAST. A provider known to be down still costs a full
	// timeout to discover that again, and in a budget where the whole turn is
	// 900 ms, spending 250 ms rediscovering a dead provider is most of the
	// failover's cost.
	CircuitOpen CircuitState = "open"

	// CircuitHalfOpen allows exactly one trial request.
	CircuitHalfOpen CircuitState = "half_open"
)

// String implements fmt.Stringer.
func (c CircuitState) String() string { return string(c) }

// Outcome is what happened on a provider call.
type Outcome string

// The outcomes.
const (
	OutcomeSuccess     Outcome = "success"
	OutcomeFailure     Outcome = "failure"
	OutcomeTimeout     Outcome = "timeout"
	OutcomeRateLimited Outcome = "rate_limited"
)

// String implements fmt.Stringer.
func (o Outcome) String() string { return string(o) }

// healthy reports whether the outcome counts as a success.
func (o Outcome) healthy() bool { return o == OutcomeSuccess }

// ProviderHealth is a consistent view of one provider's health.
type ProviderHealth struct {
	Provider ProviderID
	Tier     Tier
	State    CircuitState

	ConsecutiveFailures int
	Successes           uint64
	Failures            uint64
	Timeouts            uint64
	RateLimits          uint64

	LastChange  time.Time
	OpenedCount uint64
}

// Available reports whether the provider may be selected right now.
func (h ProviderHealth) Available() bool {
	return h.State == CircuitClosed || h.State == CircuitHalfOpen
}

// String renders the health.
func (h ProviderHealth) String() string {
	return fmt.Sprintf("%s %s %s consec=%d ok=%d fail=%d timeout=%d opens=%d",
		h.Provider, h.Tier, h.State, h.ConsecutiveFailures,
		h.Successes, h.Failures, h.Timeouts, h.OpenedCount)
}

// RouterConfig configures the provider router.
type RouterConfig struct {
	// FailureThreshold is how many consecutive failures open the circuit.
	FailureThreshold int

	// CooldownPeriod is how long an open circuit waits before allowing a trial.
	CooldownPeriod time.Duration
}

// DefaultRouterConfig returns the baseline.
//
// Five consecutive failures is deliberately not one: a single failed stream is
// ordinary on a real network, and a breaker that opened on it would flap
// between providers on background noise. Thirty seconds of cooldown is long
// enough that a restarting provider has come back and short enough that a
// transient blip does not cost minutes of degraded routing.
func DefaultRouterConfig() RouterConfig {
	return RouterConfig{FailureThreshold: 5, CooldownPeriod: 30 * time.Second}
}

func (c RouterConfig) validate() []string {
	var problems []string
	if c.FailureThreshold <= 0 {
		problems = append(problems, "router: FailureThreshold must be positive")
	}
	if c.CooldownPeriod <= 0 {
		problems = append(problems, "router: CooldownPeriod must be positive; "+
			"an open circuit with no cooldown never recovers")
	}
	return problems
}

// entry is one registered provider.
type entry struct {
	id   ProviderID
	tier Tier
	caps Capabilities

	stt STTProvider
	tts TTSProvider

	state      CircuitState
	consec     int
	successes  uint64
	failures   uint64
	timeouts   uint64
	rateLimits uint64
	openedAt   time.Time
	lastChange time.Time
	opens      uint64
	// trialTaken marks that the single half-open trial has been handed out.
	trialTaken bool
}

// ProviderRouter selects providers and tracks their health.
//
// # It exposes no vendor vocabulary
//
// Selection consults [Capabilities] and health, never an identity. "Is this
// Deepgram" is a question this type cannot ask and cannot answer, which is what
// makes a provider swap a configuration change rather than a code change.
//
// # Two error outcomes that look alike and are not
//
// ErrUnsupportedLanguage means no registered provider declares the language.
// ErrProviderUnavailable means some do and none is healthy. An operator seeing
// the first needs to add a provider; seeing the second, to fix one. Collapsing
// them would send them to the wrong runbook.
type ProviderRouter struct {
	cfg     RouterConfig
	clock   rt.Clock
	metrics *SpeechMetrics

	mu      sync.RWMutex
	sttList []*entry
	ttsList []*entry
	byID    map[ProviderID]*entry
	// lastPicked tracks the provider previously chosen per kind, so a switch
	// can be counted exactly once rather than on every selection.
	lastPicked map[string]ProviderID
}

// NewProviderRouter builds a router.
func NewProviderRouter(cfg RouterConfig, clock rt.Clock, m *SpeechMetrics) (*ProviderRouter, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if clock == nil {
		clock = rt.SystemClock{}
	}
	if m == nil {
		m = NewSpeechMetrics()
	}
	return &ProviderRouter{
		cfg: cfg, clock: clock, metrics: m,
		byID:       make(map[ProviderID]*entry),
		lastPicked: make(map[string]ProviderID),
	}, nil
}

// RegisterSTT adds a recognition provider at a tier.
func (r *ProviderRouter) RegisterSTT(p STTProvider, tier Tier) error {
	if p == nil {
		return fmt.Errorf("%w: nil STT provider", ErrInternalFailure)
	}
	if !p.ID().Valid() {
		return fmt.Errorf("%w: provider id %q is not a valid identifier",
			ErrInternalFailure, p.ID())
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byID[p.ID()]; dup {
		return fmt.Errorf("%w: provider %s is already registered",
			ErrInternalFailure, p.ID())
	}
	e := &entry{
		id: p.ID(), tier: tier, caps: p.Capabilities(), stt: p,
		state: CircuitClosed, lastChange: r.clock.Now(),
	}
	r.sttList = append(r.sttList, e)
	r.byID[e.id] = e
	return nil
}

// RegisterTTS adds a synthesis provider at a tier.
func (r *ProviderRouter) RegisterTTS(p TTSProvider, tier Tier) error {
	if p == nil {
		return fmt.Errorf("%w: nil TTS provider", ErrInternalFailure)
	}
	if !p.ID().Valid() {
		return fmt.Errorf("%w: provider id %q is not a valid identifier",
			ErrInternalFailure, p.ID())
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byID[p.ID()]; dup {
		return fmt.Errorf("%w: provider %s is already registered",
			ErrInternalFailure, p.ID())
	}
	e := &entry{
		id: p.ID(), tier: tier, caps: p.Capabilities(), tts: p,
		state: CircuitClosed, lastChange: r.clock.Now(),
	}
	r.ttsList = append(r.ttsList, e)
	r.byID[e.id] = e
	return nil
}

// PickSTT selects a recognition provider for a language.
func (r *ProviderRouter) PickSTT(l Language) (STTProvider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, err := r.pickLocked(r.sttList, l, "stt")
	if err != nil {
		return nil, err
	}
	return e.stt, nil
}

// PickTTS selects a synthesis provider for a language.
func (r *ProviderRouter) PickTTS(l Language) (TTSProvider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, err := r.pickLocked(r.ttsList, l, "tts")
	if err != nil {
		return nil, err
	}
	return e.tts, nil
}

// pickLocked selects the best entry. Caller holds the lock.
func (r *ProviderRouter) pickLocked(list []*entry, l Language, kind string) (*entry, error) {
	var declares bool

	// Primary first, then secondary. Within a tier, registration order.
	for _, tier := range []Tier{TierPrimary, TierSecondary} {
		for _, e := range list {
			if e.tier != tier {
				continue
			}
			if !e.caps.Supports(l) {
				continue
			}
			declares = true

			r.refreshLocked(e)
			if !r.admitLocked(e) {
				continue
			}
			r.noteSwitchLocked(kind, e.id)
			return e, nil
		}
	}

	if !declares {
		return nil, fmt.Errorf("%w: no registered %s provider declares %s",
			ErrUnsupportedLanguage, kind, l)
	}
	return nil, fmt.Errorf("%w: every %s provider for %s is unhealthy",
		ErrProviderUnavailable, kind, l)
}

// refreshLocked moves an open circuit to half-open once its cooldown expires.
func (r *ProviderRouter) refreshLocked(e *entry) {
	if e.state != CircuitOpen {
		return
	}
	if r.clock.Now().Sub(e.openedAt) < r.cfg.CooldownPeriod {
		return
	}
	e.state = CircuitHalfOpen
	e.trialTaken = false
	e.lastChange = r.clock.Now()
}

// admitLocked reports whether a provider may take this request.
func (r *ProviderRouter) admitLocked(e *entry) bool {
	switch e.state {
	case CircuitClosed:
		return true
	case CircuitHalfOpen:
		// Exactly one trial. A half-open circuit that admitted everything would
		// hand a recovering provider the full load the instant it came back.
		if e.trialTaken {
			return false
		}
		e.trialTaken = true
		return true
	default:
		return false
	}
}

// noteSwitchLocked counts a change of provider for a kind.
func (r *ProviderRouter) noteSwitchLocked(kind string, id ProviderID) {
	prev, had := r.lastPicked[kind]
	if had && prev != id {
		r.metrics.ProviderSwitches.Add(1, string(prev), string(id))
	}
	r.lastPicked[kind] = id
}

// Report records the outcome of a provider call.
//
// The only way health changes. A router that inferred health from its own
// selection would never learn that a provider it picked then failed.
func (r *ProviderRouter) Report(id ProviderID, outcome Outcome) {
	r.mu.Lock()
	defer r.mu.Unlock()

	e := r.byID[id]
	if e == nil {
		return
	}

	switch outcome {
	case OutcomeSuccess:
		e.successes++
	case OutcomeTimeout:
		e.timeouts++
		e.failures++
	case OutcomeRateLimited:
		e.rateLimits++
		e.failures++
	default:
		e.failures++
	}

	if outcome.healthy() {
		e.consec = 0
		if e.state != CircuitClosed {
			e.state = CircuitClosed
			e.trialTaken = false
			e.lastChange = r.clock.Now()
		}
		return
	}

	r.metrics.ProviderFailures.Add(1, string(id), string(outcome))
	e.consec++

	// A failure during the half-open trial reopens immediately: the provider
	// was given one chance and used it to fail.
	if e.state == CircuitHalfOpen || e.consec >= r.cfg.FailureThreshold {
		if e.state != CircuitOpen {
			e.opens++
			r.metrics.CircuitOpens.Add(1, string(id))
		}
		e.state = CircuitOpen
		e.openedAt = r.clock.Now()
		e.lastChange = e.openedAt
		e.trialTaken = false
	}
}

// Health returns a provider's health.
func (r *ProviderRouter) Health(id ProviderID) ProviderHealth {
	r.mu.Lock()
	defer r.mu.Unlock()

	e := r.byID[id]
	if e == nil {
		return ProviderHealth{Provider: id}
	}
	// Refresh first, so a caller observing health sees the same state a caller
	// selecting a provider would.
	r.refreshLocked(e)
	return ProviderHealth{
		Provider: e.id, Tier: e.tier, State: e.state,
		ConsecutiveFailures: e.consec,
		Successes:           e.successes, Failures: e.failures,
		Timeouts: e.timeouts, RateLimits: e.rateLimits,
		LastChange: e.lastChange, OpenedCount: e.opens,
	}
}

// AllHealth returns health for every registered provider.
func (r *ProviderRouter) AllHealth() []ProviderHealth {
	r.mu.RLock()
	ids := make([]ProviderID, 0, len(r.byID))
	for id := range r.byID {
		ids = append(ids, id)
	}
	r.mu.RUnlock()

	out := make([]ProviderHealth, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.Health(id))
	}
	return out
}
