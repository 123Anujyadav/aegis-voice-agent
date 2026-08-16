package runtime

import (
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// BreakerState is a circuit breaker's state.
type BreakerState int

const (
	// BreakerClosed passes every call through. Normal operation.
	BreakerClosed BreakerState = iota

	// BreakerOpen refuses every call without attempting it. The provider is
	// believed unhealthy and hammering it delays its recovery while spending
	// our own latency budget on calls that will fail.
	BreakerOpen

	// BreakerHalfOpen admits a limited number of trial calls to discover
	// whether the provider has recovered.
	BreakerHalfOpen
)

// String renders the state for logs and metric labels.
func (s BreakerState) String() string {
	switch s {
	case BreakerClosed:
		return "closed"
	case BreakerOpen:
		return "open"
	case BreakerHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// BreakerConfig tunes a circuit breaker.
type BreakerConfig struct {
	// FailureThreshold is the number of failures within the window that opens
	// the breaker.
	FailureThreshold int

	// MinimumRequests is the number of observations required before the
	// breaker will open at all.
	//
	// Without it a breaker opens on the first failure after a quiet period,
	// because one failure out of one request is a 100% failure rate. That
	// makes a breaker most likely to trip exactly when traffic is lowest and
	// the evidence is weakest.
	MinimumRequests int

	// Window is the sliding period over which failures are counted.
	Window time.Duration

	// Cooldown is how long the breaker stays open before admitting a trial.
	Cooldown time.Duration

	// HalfOpenProbes is how many concurrent trials are admitted while
	// half-open. Kept small: the point is to test the water, not to resume
	// full traffic against a provider that may still be failing.
	HalfOpenProbes int

	// SuccessesToClose is how many consecutive half-open successes close the
	// breaker. Greater than one, so a single lucky response does not restore
	// full traffic to a flapping provider.
	SuccessesToClose int
}

// DefaultBreakerConfig returns the configuration used unless overridden.
//
// The numbers are chosen for the platform's actual traffic shape: sharply
// diurnal, with a high peak-to-mean (ADR-0002 §13). A window shorter than 10 s
// makes the breaker jumpy at trough volume; longer makes it slow to notice a
// peak-hour outage.
func DefaultBreakerConfig() BreakerConfig {
	return BreakerConfig{
		FailureThreshold: 5,
		MinimumRequests:  10,
		Window:           10 * time.Second,
		Cooldown:         5 * time.Second,
		HalfOpenProbes:   2,
		SuccessesToClose: 3,
	}
}

func (c BreakerConfig) validate(name string) []string {
	var p []string
	if c.FailureThreshold <= 0 {
		p = append(p, fmt.Sprintf("breaker %s: FailureThreshold must be positive", name))
	}
	if c.MinimumRequests < c.FailureThreshold {
		p = append(p, fmt.Sprintf(
			"breaker %s: MinimumRequests (%d) must be at least FailureThreshold (%d), "+
				"or the breaker can open before it has enough evidence",
			name, c.MinimumRequests, c.FailureThreshold))
	}
	if c.Window <= 0 {
		p = append(p, fmt.Sprintf("breaker %s: Window must be positive", name))
	}
	if c.Cooldown <= 0 {
		p = append(p, fmt.Sprintf("breaker %s: Cooldown must be positive", name))
	}
	if c.HalfOpenProbes <= 0 {
		p = append(p, fmt.Sprintf("breaker %s: HalfOpenProbes must be positive", name))
	}
	if c.SuccessesToClose <= 0 {
		p = append(p, fmt.Sprintf("breaker %s: SuccessesToClose must be positive", name))
	}
	return p
}

// observation is one recorded outcome within the sliding window.
type observation struct {
	at      time.Time
	failure bool
}

// Breaker is a circuit breaker over one provider.
//
// It uses a sliding window of individual observations rather than a bucketed
// counter. Buckets are cheaper but produce a well-known artefact: a burst of
// failures straddling a bucket boundary is split in two and may not trip a
// threshold either bucket reaches alone. At our request volumes the window is
// small enough that keeping observations is affordable, and correctness is
// worth more than the bytes.
type Breaker struct {
	name  string
	cfg   BreakerConfig
	clock Clock

	mu            sync.Mutex
	state         BreakerState
	observations  []observation
	openedAt      time.Time
	halfOpenCalls int
	halfOpenOK    int
	onChange      func(from, to BreakerState)
}

// NewBreaker constructs a breaker.
func NewBreaker(name string, cfg BreakerConfig, clock Clock) (*Breaker, error) {
	if problems := cfg.validate(name); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if clock == nil {
		clock = SystemClock{}
	}
	return &Breaker{
		name:  name,
		cfg:   cfg,
		clock: clock,
		state: BreakerClosed,
	}, nil
}

// OnStateChange registers a callback fired on every state change. It runs
// synchronously outside the breaker's lock and must not block.
func (b *Breaker) OnStateChange(fn func(from, to BreakerState)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onChange = fn
}

// State returns the current state, applying any pending cooldown expiry.
//
// Cooldown is evaluated lazily on read rather than by a timer. A timer per
// breaker is a goroutine per breaker that exists only to change a field, and
// with one breaker per provider per kernel that is real overhead for no gain —
// nothing observes the state except a call about to be made.
func (b *Breaker) State() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.evaluateLocked()
	return b.state
}

// Allow reports whether a call may proceed, and returns a function to report
// its outcome.
//
// The two-part shape is deliberate. A breaker that only counts outcomes cannot
// limit half-open concurrency, because it learns of a call only after it has
// finished. Reserving a slot at Allow and releasing it at report is what makes
// HalfOpenProbes an actual limit rather than an aspiration.
//
// The returned function must be called exactly once. Calling it twice
// double-counts an outcome; never calling it leaks a half-open slot, which the
// cooldown eventually reclaims but which will suppress recovery until it does.
func (b *Breaker) Allow() (allowed bool, report func(err error)) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.evaluateLocked()

	switch b.state {
	case BreakerOpen:
		return false, func(error) {}

	case BreakerHalfOpen:
		if b.halfOpenCalls >= b.cfg.HalfOpenProbes {
			return false, func(error) {}
		}
		b.halfOpenCalls++
		var once sync.Once
		return true, func(err error) {
			once.Do(func() { b.reportHalfOpen(err) })
		}

	default: // BreakerClosed
		var once sync.Once
		return true, func(err error) {
			once.Do(func() { b.reportClosed(err) })
		}
	}
}

// reportClosed records an outcome while closed.
func (b *Breaker) reportClosed(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	counts := b.shouldCount(err)
	if !counts {
		// A non-counting failure — rate limiting, an invalid request — is not
		// evidence about the provider's health and is deliberately not
		// recorded at all. Recording it as a success would be equally wrong:
		// it would dilute a genuine failure rate.
		return
	}

	b.observations = append(b.observations, observation{at: b.clock.Now(), failure: err != nil})
	b.trimLocked()

	if err == nil {
		return
	}

	total, failures := b.countLocked()
	if total >= b.cfg.MinimumRequests && failures >= b.cfg.FailureThreshold {
		b.transitionLocked(BreakerOpen)
		b.openedAt = b.clock.Now()
	}
}

// reportHalfOpen records an outcome while half-open.
func (b *Breaker) reportHalfOpen(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.halfOpenCalls > 0 {
		b.halfOpenCalls--
	}

	if err != nil && b.shouldCount(err) {
		// Any counting failure during a trial reopens immediately. Half-open
		// exists to answer one question — has it recovered — and a failure is
		// a complete answer.
		b.halfOpenOK = 0
		b.transitionLocked(BreakerOpen)
		b.openedAt = b.clock.Now()
		return
	}
	if err != nil {
		return // non-counting failure: neither evidence for nor against
	}

	b.halfOpenOK++
	if b.halfOpenOK >= b.cfg.SuccessesToClose {
		b.halfOpenOK = 0
		b.observations = nil // start the window clean, not with pre-outage history
		b.transitionLocked(BreakerClosed)
	}
}

// shouldCount reports whether an outcome is evidence about provider health.
func (b *Breaker) shouldCount(err error) bool {
	if err == nil {
		return true
	}
	var pe *ProviderError
	if asProviderError(err, &pe) {
		return pe.Kind.CountsAgainstBreaker()
	}
	// An unclassified error is counted. Assuming otherwise would let an
	// adapter suppress a breaker by failing to classify.
	return true
}

// evaluateLocked applies cooldown expiry. Caller must hold b.mu.
func (b *Breaker) evaluateLocked() {
	if b.state != BreakerOpen {
		return
	}
	if b.clock.Since(b.openedAt) >= b.cfg.Cooldown {
		b.halfOpenCalls = 0
		b.halfOpenOK = 0
		b.transitionLocked(BreakerHalfOpen)
	}
}

// transitionLocked changes state and fires the callback. Caller must hold b.mu.
func (b *Breaker) transitionLocked(to BreakerState) {
	if b.state == to {
		return
	}
	from := b.state
	b.state = to
	if b.onChange != nil {
		// Fired under the lock. The callback is documented as non-blocking and
		// is used only to update a gauge; releasing and reacquiring here would
		// permit two transitions to report out of order.
		b.onChange(from, to)
	}
}

// trimLocked drops observations outside the window. Caller must hold b.mu.
func (b *Breaker) trimLocked() {
	cutoff := b.clock.Now().Add(-b.cfg.Window)
	i := 0
	for ; i < len(b.observations); i++ {
		if b.observations[i].at.After(cutoff) {
			break
		}
	}
	if i > 0 {
		b.observations = append(b.observations[:0], b.observations[i:]...)
	}
}

// countLocked returns total and failing observations in the window.
func (b *Breaker) countLocked() (total, failures int) {
	for _, o := range b.observations {
		total++
		if o.failure {
			failures++
		}
	}
	return total, failures
}

// Reset returns the breaker to closed and clears its history. Intended for
// operational recovery and for tests; not called on the request path.
func (b *Breaker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.observations = nil
	b.halfOpenCalls = 0
	b.halfOpenOK = 0
	b.transitionLocked(BreakerClosed)
}

// asProviderError is a local errors.As specialised to *ProviderError, avoiding
// the reflection errors.As performs. This runs on the hot path for every
// provider outcome, and the allocation errors.As causes was measurable.
func asProviderError(err error, target **ProviderError) bool {
	for err != nil {
		if pe, ok := err.(*ProviderError); ok {
			*target = pe
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// ---------------------------------------------------------------------------
// Retry
// ---------------------------------------------------------------------------

// RetryPolicy governs how a failed provider call is retried.
//
// It is budget-aware rather than attempt-count-driven. On the screening path a
// request has an absolute deadline derived from ADR-0011, and a retry that
// cannot complete before it is pure waste: it spends capacity, delays the
// failure the caller must handle anyway, and makes the p99 worse than not
// retrying at all.
type RetryPolicy struct {
	// MaxAttempts caps total attempts including the first.
	MaxAttempts int

	// InitialBackoff is the delay before the second attempt.
	InitialBackoff time.Duration

	// MaxBackoff caps the delay.
	MaxBackoff time.Duration

	// Multiplier grows the backoff between attempts.
	Multiplier float64

	// JitterFraction randomises the delay by ±fraction.
	//
	// Without jitter, every client that failed at the same instant retries at
	// the same instant, and a provider recovering from an outage is
	// immediately knocked over by the synchronised retry it caused.
	JitterFraction float64
}

// DefaultRetryPolicy returns the policy used unless overridden.
//
// Two attempts, not three. The latency budget is 900 ms p50 (ADR-0011) and a
// typical model call consumes most of it, so a third attempt cannot fit and
// exists only to make the failure slower.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:    2,
		InitialBackoff: 50 * time.Millisecond,
		MaxBackoff:     400 * time.Millisecond,
		Multiplier:     2.0,
		JitterFraction: 0.25,
	}
}

// Backoff returns the delay before the given attempt, which is 1-based. Attempt
// 1 is the first try and has no delay.
func (p RetryPolicy) Backoff(attempt int, rnd *rand.Rand) time.Duration {
	if attempt <= 1 {
		return 0
	}
	d := float64(p.InitialBackoff)
	for i := 2; i < attempt; i++ {
		d *= p.Multiplier
	}
	if d > float64(p.MaxBackoff) {
		d = float64(p.MaxBackoff)
	}
	if p.JitterFraction > 0 && rnd != nil {
		// Full symmetric jitter around the nominal delay.
		delta := d * p.JitterFraction
		d = d - delta + rnd.Float64()*2*delta
	}
	if d < 0 {
		d = 0
	}
	return time.Duration(d)
}

// ShouldRetry decides whether another attempt is warranted.
//
// remaining is the time left before the request's absolute deadline; estimate
// is how long an attempt is expected to take. An attempt is refused when it
// plausibly cannot finish, which is the budget-awareness this policy exists for.
func (p RetryPolicy) ShouldRetry(attempt int, err error, remaining, estimate time.Duration) bool {
	if err == nil {
		return false
	}
	if attempt >= p.MaxAttempts {
		return false
	}

	var pe *ProviderError
	if asProviderError(err, &pe) && !pe.Kind.Retryable() {
		return false
	}

	// Require room for the backoff plus a full attempt. Retrying with 40 ms
	// left against a 400 ms model call converts one failure into two, later.
	needed := p.Backoff(attempt+1, nil) + estimate
	return remaining > needed
}
