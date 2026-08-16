package conversation

import (
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Stage names a phase of the decision cycle that carries its own budget.
type Stage int

const (
	// StageTurnDetection decides whether the caller has finished.
	StageTurnDetection Stage = iota
	// StageIntent classifies the utterance.
	StageIntent
	// StageContext reads and updates context.
	StageContext
	// StagePolicy evaluates rules.
	StagePolicy
	// StagePlanning decides the action.
	StagePlanning
	// StageTransition applies the state change.
	StageTransition
	// StageTotal is the whole decision cycle.
	StageTotal
)

// String renders the stage for logs and metric labels.
func (s Stage) String() string {
	switch s {
	case StageTurnDetection:
		return "turn_detection"
	case StageIntent:
		return "intent"
	case StageContext:
		return "context"
	case StagePolicy:
		return "policy"
	case StagePlanning:
		return "planning"
	case StageTransition:
		return "transition"
	default:
		return "total"
	}
}

// Skippable reports whether a stage may be dropped under budget pressure.
//
// POLICY IS NEVER SKIPPABLE. Under load the platform's frozen invariant I11
// permits shedding at admission and downgrading a tier; it forbids skipping the
// safety layer. Policy evaluation is where safety rules live, so a budget
// controller that could skip it would defeat the invariant from a direction
// nobody was watching.
//
// State transition is also unskippable, for a different reason: skipping it
// would leave the conversation in a state that does not match what it did,
// which is worse than being slow.
func (s Stage) Skippable() bool {
	switch s {
	case StagePolicy, StageTransition, StageTotal:
		return false
	default:
		return true
	}
}

// LatencyConfig assigns a budget to each stage.
//
// The total is 150 ms, deliberately small. ADR-0011 budgets 900 ms p50
// end-to-end and allocates the great majority to speech recognition, model
// inference and synthesis. The conversation engine's decision cycle sits
// between them and must be close to free — if this engine costs 100 ms, it has
// taken an eighth of the budget to decide what to do, before anything has been
// said.
type LatencyConfig struct {
	// Budgets per stage.
	Budgets map[Stage]time.Duration

	// Total bounds the whole decision cycle.
	Total time.Duration

	// DegradeThreshold is the fraction of Total at which skippable stages
	// begin to be dropped.
	DegradeThreshold float64
}

// DefaultLatencyConfig returns the budget allocation used unless overridden.
func DefaultLatencyConfig() LatencyConfig {
	return LatencyConfig{
		Budgets: map[Stage]time.Duration{
			StageTurnDetection: 10 * time.Millisecond,
			StageIntent:        60 * time.Millisecond,
			StageContext:       15 * time.Millisecond,
			StagePolicy:        20 * time.Millisecond,
			StagePlanning:      35 * time.Millisecond,
			StageTransition:    10 * time.Millisecond,
		},
		Total:            150 * time.Millisecond,
		DegradeThreshold: 0.75,
	}
}

func (c LatencyConfig) validate() []string {
	var p []string
	if c.Total <= 0 {
		p = append(p, "latency: Total must be positive")
	}
	if c.DegradeThreshold <= 0 || c.DegradeThreshold > 1 {
		p = append(p, "latency: DegradeThreshold must be in (0,1]")
	}
	var sum time.Duration
	for s, d := range c.Budgets {
		if d <= 0 {
			p = append(p, "latency: budget for "+s.String()+" must be positive")
		}
		sum += d
	}
	if sum > c.Total {
		p = append(p, "latency: stage budgets exceed Total; a cycle that spends "+
			"every stage budget would already have overrun")
	}
	return p
}

// Reservation is one stage's measured spend.
type Reservation struct {
	Stage   Stage
	Started time.Time
	Ended   time.Time
	Budget  time.Duration
	Skipped bool
}

// Spent returns the stage's duration.
func (r Reservation) Spent() time.Duration {
	if r.Ended.IsZero() {
		return 0
	}
	return r.Ended.Sub(r.Started)
}

// Overran reports whether the stage exceeded its budget.
func (r Reservation) Overran() bool { return r.Spent() > r.Budget }

// LatencyController budgets each stage of one decision cycle.
//
// One controller per cycle, not per conversation: budgets are per-decision, and
// a controller that accumulated across turns would report a conversation as
// over budget because it had been going on for a while, which is not what the
// budget means.
type LatencyController struct {
	cfg     LatencyConfig
	clock   rt.Clock
	metrics *Metrics

	mu       sync.Mutex
	started  time.Time
	records  []Reservation
	degraded bool
}

// NewLatencyController constructs a controller for one decision cycle.
func NewLatencyController(cfg LatencyConfig, clock rt.Clock, metrics *Metrics) (*LatencyController, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if clock == nil {
		clock = rt.SystemClock{}
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	return &LatencyController{cfg: cfg, clock: clock, metrics: metrics}, nil
}

// Begin starts the cycle.
func (l *LatencyController) Begin() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.started = l.clock.Now()
	l.records = l.records[:0]
	l.degraded = false
}

// Enter reserves a stage and returns a function to end it.
//
// It reports whether the stage should run. A skippable stage is refused once
// spend passes DegradeThreshold — that is the degradation path, and it is why
// Skippable() exists: the controller drops optional work rather than
// overrunning, and never drops policy.
func (l *LatencyController) Enter(s Stage) (end func(), run bool) {
	l.mu.Lock()

	now := l.clock.Now()
	if l.started.IsZero() {
		l.started = now
	}
	elapsed := now.Sub(l.started)

	budget, ok := l.cfg.Budgets[s]
	if !ok {
		budget = l.cfg.Total - elapsed
	}

	overThreshold := float64(elapsed) > float64(l.cfg.Total)*l.cfg.DegradeThreshold
	if overThreshold && s.Skippable() {
		l.degraded = true
		l.records = append(l.records, Reservation{Stage: s, Started: now, Ended: now,
			Budget: budget, Skipped: true})
		l.mu.Unlock()
		l.metrics.BudgetExceeded.Inc(s.String())
		return func() {}, false
	}

	idx := len(l.records)
	l.records = append(l.records, Reservation{Stage: s, Started: now, Budget: budget})
	l.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			end := l.clock.Now()
			l.records[idx].Ended = end
			rec := l.records[idx]
			l.mu.Unlock()

			l.metrics.StageLatency.Observe(rec.Spent().Seconds(), s.String())
			if rec.Overran() {
				l.metrics.BudgetExceeded.Inc(s.String())
			}
		})
	}, true
}

// Remaining returns the unspent portion of the total budget. It may be
// negative, which is meaningful: it says by how much the cycle overran.
func (l *LatencyController) Remaining() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.started.IsZero() {
		return l.cfg.Total
	}
	return l.cfg.Total - l.clock.Now().Sub(l.started)
}

// Degraded reports whether any skippable stage was dropped.
func (l *LatencyController) Degraded() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.degraded
}

// End completes the cycle and records the total.
func (l *LatencyController) End() time.Duration {
	l.mu.Lock()
	total := l.clock.Now().Sub(l.started)
	l.mu.Unlock()

	l.metrics.StageLatency.Observe(total.Seconds(), StageTotal.String())
	l.metrics.BudgetRemain.Observe((l.cfg.Total - total).Seconds())
	if total > l.cfg.Total {
		l.metrics.BudgetExceeded.Inc(StageTotal.String())
	}
	return total
}

// Records returns a copy of every stage reservation, for diagnostics.
func (l *LatencyController) Records() []Reservation {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Reservation, len(l.records))
	copy(out, l.records)
	return out
}

// scaleBudgets returns a copy of the config with every budget multiplied.
//
// Used to apply a persona's latency profile: an emergency persona gets half the
// budget because taking longer to get out of the way is itself a failure, while
// a fraud interaction gets more because a verification exchange is not urgent.
func (c LatencyConfig) scaleBudgets(factor float64) LatencyConfig {
	if factor <= 0 {
		factor = 1
	}
	out := LatencyConfig{
		Budgets:          make(map[Stage]time.Duration, len(c.Budgets)),
		Total:            time.Duration(float64(c.Total) * factor),
		DegradeThreshold: c.DegradeThreshold,
	}
	for s, d := range c.Budgets {
		out.Budgets[s] = time.Duration(float64(d) * factor)
	}
	return out
}
