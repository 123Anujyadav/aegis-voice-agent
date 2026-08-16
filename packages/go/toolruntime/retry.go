package toolruntime

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// RetrySpec is a tool's retry policy.
//
// It is a spec, not a mechanism: the mechanism is [RetryEngine]. Keeping the
// two apart means a contract can be reviewed and diffed without a reader having
// to know how backoff is computed, and the computation can be tested without a
// contract.
type RetrySpec struct {
	// MaxAttempts includes the first. 1 means no retry.
	MaxAttempts int
	// InitialBackoff is the delay before the second attempt.
	InitialBackoff time.Duration
	// MaxBackoff caps exponential growth.
	MaxBackoff time.Duration
	// Multiplier grows the backoff. Values below 1 shrink it, which is refused.
	Multiplier float64
	// Jitter is the fraction of the delay randomised, in [0, 1].
	Jitter float64

	// NoBackoff retries immediately, with no delay between attempts.
	//
	// A SEPARATE FLAG RATHER THAN InitialBackoff: 0, because zero cannot mean
	// both "unset, take the runtime default" and "none, retry immediately" —
	// and withDefaults has to pick one. It picked "unset", which meant a caller
	// asking for no delay silently got fifty milliseconds. That is fine in
	// production and fatal in a test driving a fake clock: nothing advances the
	// clock, and the retry sleeps forever. See ENGINEERING_AUDIT §F1.
	NoBackoff bool
	// RetryOn optionally narrows retryable errors. Empty means the runtime's
	// classifier decides — which is the right default, because a tool author
	// enumerating retryable errors will eventually enumerate one that is not.
	RetryOn []error
}

// DefaultRetrySpec returns the runtime default.
//
// Three attempts and 50 ms initial backoff, chosen against the frozen ADR-0011
// latency budget rather than by habit: a tool inside a 900 ms p50 conversational
// turn can afford roughly 50 ms + 100 ms of waiting before the caller notices a
// silence, and a fourth attempt would spend more time waiting than the whole
// turn allows.
func DefaultRetrySpec() RetrySpec {
	return RetrySpec{
		MaxAttempts:    3,
		InitialBackoff: 50 * time.Millisecond,
		MaxBackoff:     2 * time.Second,
		Multiplier:     2.0,
		Jitter:         0.2,
	}
}

func (r RetrySpec) validate(d Descriptor, e Effect) []string {
	var problems []string
	if r.MaxAttempts < 0 {
		problems = append(problems, fmt.Sprintf("%s retry: MaxAttempts must not be negative", d))
	}
	if r.InitialBackoff < 0 || r.MaxBackoff < 0 {
		problems = append(problems, fmt.Sprintf("%s retry: backoffs must not be negative", d))
	}
	if r.MaxBackoff > 0 && r.InitialBackoff > r.MaxBackoff {
		problems = append(problems, fmt.Sprintf(
			"%s retry: InitialBackoff %s exceeds MaxBackoff %s", d, r.InitialBackoff, r.MaxBackoff))
	}
	if r.Multiplier != 0 && r.Multiplier < 1 {
		problems = append(problems, fmt.Sprintf(
			"%s retry: Multiplier %g shrinks the backoff", d, r.Multiplier))
	}
	if r.Jitter < 0 || r.Jitter > 1 {
		problems = append(problems, fmt.Sprintf("%s retry: Jitter %g is outside [0,1]", d, r.Jitter))
	}
	if e == EffectIrreversible && r.MaxAttempts > 1 {
		problems = append(problems, fmt.Sprintf(
			"%s retry: irreversible effects must not be retried automatically; "+
				"an unanswered call may still have happened", d))
	}
	return problems
}

func (r RetrySpec) withDefaults(def RetrySpec) RetrySpec {
	// A spec that states no backoff of its own inherits the runtime's stance on
	// it, so a deployment (or a test harness) can turn delays off globally
	// without every contract having to opt in.
	if !r.NoBackoff && def.NoBackoff && r.InitialBackoff == 0 {
		r.NoBackoff = true
	}
	if r.NoBackoff {
		// An explicit "no delay" is honoured whole. Filling in a default
		// multiplier or jitter here would be arithmetic on a delay of zero,
		// which is at best pointless and at worst a delay.
		r.InitialBackoff, r.MaxBackoff, r.Jitter = 0, 0, 0
		if r.MaxAttempts == 0 {
			r.MaxAttempts = def.MaxAttempts
		}
		return r
	}
	if r.MaxAttempts == 0 {
		r.MaxAttempts = def.MaxAttempts
	}
	if r.InitialBackoff == 0 {
		r.InitialBackoff = def.InitialBackoff
	}
	if r.MaxBackoff == 0 {
		r.MaxBackoff = def.MaxBackoff
	}
	if r.Multiplier == 0 {
		r.Multiplier = def.Multiplier
	}
	if r.Jitter == 0 {
		r.Jitter = def.Jitter
	}
	return r
}

// toRuntimePolicy converts to the frozen Phase 10A retry policy.
//
// Reusing 10A's backoff arithmetic rather than writing a second one. Two
// implementations of exponential backoff in one platform means two sets of
// off-by-one bugs and two answers to "why did it wait that long".
func (r RetrySpec) toRuntimePolicy() rt.RetryPolicy {
	return rt.RetryPolicy{
		MaxAttempts:    r.MaxAttempts,
		InitialBackoff: r.InitialBackoff,
		MaxBackoff:     r.MaxBackoff,
		Multiplier:     r.Multiplier,
		JitterFraction: r.Jitter,
	}
}

// Classify decides whether an error is worth another attempt.
//
// THE TOOL DOES NOT GET A VOTE. A tool marking its own errors retryable will,
// eventually, mark a permission denial or a validation failure retryable, and
// the runtime will hammer a downstream with a request that cannot ever succeed.
// The classifier lives here, sees the sentinel set, and is the only opinion
// that counts.
func Classify(err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, ErrPermissionDenied),
		errors.Is(err, ErrConsentRequired),
		errors.Is(err, ErrInvalidInput),
		errors.Is(err, ErrInvalidOutput),
		errors.Is(err, ErrNotRegistered),
		errors.Is(err, ErrNoCapability),
		errors.Is(err, ErrVersionUnsatisfiable),
		errors.Is(err, ErrCancelled),
		errors.Is(err, ErrDuplicate),
		errors.Is(err, ErrBudgetExceeded),
		errors.Is(err, ErrInvariant),
		errors.Is(err, ErrClosed):
		// None of these becomes true by being asked again.
		return false
	case errors.Is(err, ErrCircuitOpen):
		// Not retried on this execution: the breaker is the thing saying stop,
		// and retrying past it defeats the purpose of having one.
		return false
	case errors.Is(err, ErrTimeout), errors.Is(err, ErrNoHealthyProvider):
		return true
	}

	// A provider error from the frozen 10A error model carries its own
	// retryability, which was decided at the transport boundary where the
	// evidence actually is.
	var pe *rt.ProviderError
	if errors.As(err, &pe) {
		return pe.Kind.Retryable()
	}

	// Unknown errors are retryable. The alternative — treating anything
	// unrecognised as permanent — makes a transient network blip a permanent
	// failure, and transient network blips are the single most common cause of
	// a tool call failing.
	return true
}

// retryable applies a spec's optional narrowing on top of the classifier.
func (r RetrySpec) retryable(err error) bool {
	if !Classify(err) {
		return false
	}
	if len(r.RetryOn) == 0 {
		return true
	}
	for _, target := range r.RetryOn {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// RetryEngine computes delays and owns per-tool circuit breakers.
type RetryEngine struct {
	clock   rt.Clock
	metrics *Metrics

	mu       sync.RWMutex
	breakers map[ToolID]*rt.Breaker
	cfg      rt.BreakerConfig

	rndMu sync.Mutex
	rnd   *rand.Rand

	dead *DeadLetterQueue
}

// NewRetryEngine builds a retry engine.
//
// The random source is seeded and owned, not global. Jitter drawn from the
// global source would make two runtimes in one process interfere, and would
// make a test's backoff sequence depend on whatever else ran first.
func NewRetryEngine(clock rt.Clock, metrics *Metrics, cfg rt.BreakerConfig, seed int64, dead *DeadLetterQueue) *RetryEngine {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	return &RetryEngine{
		clock:    clock,
		metrics:  metrics,
		breakers: make(map[ToolID]*rt.Breaker),
		cfg:      cfg,
		rnd:      rand.New(rand.NewSource(seed)),
		dead:     dead,
	}
}

// Breaker returns the breaker for a tool, creating it on first use.
//
// Per tool, not per capability and not global. A global breaker means one
// broken calendar integration stops every unrelated tool in the runtime; a
// per-capability breaker means a healthy fallback is punished for its sibling's
// outage.
func (e *RetryEngine) Breaker(t ToolID) *rt.Breaker {
	e.mu.RLock()
	b, ok := e.breakers[t]
	e.mu.RUnlock()
	if ok {
		return b
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if b, ok = e.breakers[t]; ok {
		return b
	}
	b, err := rt.NewBreaker(string(t), e.cfg, e.clock)
	if err != nil {
		// The config is validated once at runtime construction, so this cannot
		// be reached with a validated config. Falling back to the default keeps
		// a misconfiguration from disabling breakers entirely, which would be
		// the worst possible response to a config error.
		b, _ = rt.NewBreaker(string(t), rt.DefaultBreakerConfig(), e.clock)
	}
	e.breakers[t] = b
	return b
}

// BreakerStates returns each tool's breaker state, sorted by tool.
func (e *RetryEngine) BreakerStates() []BreakerStatus {
	e.mu.RLock()
	out := make([]BreakerStatus, 0, len(e.breakers))
	for id, b := range e.breakers {
		out = append(out, BreakerStatus{Tool: id, State: b.State().String()})
	}
	e.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Tool < out[j].Tool })
	return out
}

// BreakerStatus is one tool's circuit state.
type BreakerStatus struct {
	Tool  ToolID
	State string
}

// Backoff returns the delay before an attempt, jittered.
func (e *RetryEngine) Backoff(spec RetrySpec, attempt int) time.Duration {
	if spec.NoBackoff {
		return 0
	}
	e.rndMu.Lock()
	defer e.rndMu.Unlock()
	return spec.toRuntimePolicy().Backoff(attempt, e.rnd)
}

// Wait sleeps for the backoff, returning early if the context is cancelled.
//
// Sleeps on the injected clock, so a test advancing a fake clock steps through
// a retry sequence in microseconds. A real sleep here would make the retry
// tests the slowest in the suite and, eventually, the ones somebody skips.
func (e *RetryEngine) Wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	return e.clock.Sleep(ctx, d)
}

// DeadLetterQueue holds executions that exhausted every attempt.
//
// Bounded and lossy at the OLDEST end. A dead-letter queue that grows without
// limit turns a downstream outage into an out-of-memory kill, which converts a
// degraded service into a dead one. Dropping the oldest keeps the most recent
// failures, which are the ones an operator is looking at.
type DeadLetterQueue struct {
	mu      sync.Mutex
	max     int
	entries []DeadLetter
	dropped uint64
}

// DeadLetter is one permanently failed execution.
//
// It carries fingerprints, never arguments. A dead-letter queue is read by
// operators during incidents, exported to dashboards, and occasionally pasted
// into tickets — three places personal data must not travel to.
type DeadLetter struct {
	Execution   ExecutionID
	Step        StepID
	Descriptor  Descriptor
	Correlation CorrelationID
	Actor       ActorID
	Attempts    int
	Phase       Phase
	InputPrint  Fingerprint
	Reason      string
	FailedAt    time.Time
}

// NewDeadLetterQueue builds a bounded queue.
func NewDeadLetterQueue(max int) *DeadLetterQueue {
	if max <= 0 {
		max = 256
	}
	return &DeadLetterQueue{max: max}
}

// Add records a permanently failed execution.
func (q *DeadLetterQueue) Add(d DeadLetter) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.entries) >= q.max {
		copy(q.entries, q.entries[1:])
		q.entries = q.entries[:len(q.entries)-1]
		q.dropped++
	}
	q.entries = append(q.entries, d)
}

// Entries returns a copy, oldest first.
func (q *DeadLetterQueue) Entries() []DeadLetter {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]DeadLetter(nil), q.entries...)
}

// Len returns the current depth.
func (q *DeadLetterQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.entries)
}

// Dropped returns how many entries were evicted for capacity. Exposed because
// an operator reading a dead-letter queue must know whether they are reading
// all of it.
func (q *DeadLetterQueue) Dropped() uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropped
}
