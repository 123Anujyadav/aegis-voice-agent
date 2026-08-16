package runtime

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Class is a scheduling class. It determines queue priority and, critically,
// whether work of this class may be shed under load.
type Class int

const (
	// ClassStandard is ordinary generation work. Shed first.
	ClassStandard Class = iota

	// ClassInteractive is work a human is waiting on in real time — a live
	// screening turn. Shed only after ClassStandard is exhausted.
	ClassInteractive

	// ClassSafety is the safety layer and fraud scoring.
	//
	// INVARIANT I11. Work of this class is NEVER shed and NEVER downgraded.
	// Under load the runtime sheds at admission or downgrades a tier; it does
	// not skip the safety layer. The policy is not configurable: see
	// [Scheduler.Admit], where ClassSafety bypasses every capacity check.
	//
	// Making this a class rather than a boolean flag means the guarantee is
	// visible in every queue, every metric label and every log line, instead of
	// living in one conditional somebody eventually 'simplifies'.
	ClassSafety

	// ClassSystem is runtime-internal work: health probes, warmup, drain.
	// Never shed, but yields to ClassSafety.
	ClassSystem
)

// String renders the class for logs and metric labels.
func (c Class) String() string {
	switch c {
	case ClassStandard:
		return "standard"
	case ClassInteractive:
		return "interactive"
	case ClassSafety:
		return "safety"
	case ClassSystem:
		return "system"
	default:
		return "unknown"
	}
}

// Sheddable reports whether work of this class may be refused under load.
//
// This is the single point at which Invariant I11 is decided. There is no
// configuration that changes it and no override parameter, because a knob that
// can disable a safety guarantee is a safety guarantee that will eventually be
// disabled during an incident by someone trying to restore throughput.
func (c Class) Sheddable() bool {
	return c == ClassStandard || c == ClassInteractive
}

// priority orders classes for dequeue. Higher wins.
func (c Class) priority() int {
	switch c {
	case ClassSafety:
		return 3
	case ClassSystem:
		return 2
	case ClassInteractive:
		return 1
	default:
		return 0
	}
}

// SchedulerConfig tunes admission and concurrency.
type SchedulerConfig struct {
	// MaxConcurrent bounds simultaneously executing tasks. This is the real
	// capacity limit: the platform's capacity unit is concurrent sessions, not
	// requests per second (ADR-0002 §13).
	MaxConcurrent int

	// MaxQueued bounds tasks waiting for a slot, per sheddable class.
	MaxQueued int

	// QueueTimeout bounds how long a task may wait before it is refused. A task
	// that waits longer than its own usefulness should be refused rather than
	// run late — on the screening path, a turn that arrives after the caller
	// has hung up cost money and achieved nothing.
	QueueTimeout time.Duration

	// SheddingThreshold is the fraction of MaxConcurrent above which sheddable
	// work begins to be refused, expressed 0..1.
	//
	// Below 1.0 deliberately. Shedding at exactly 100% means the runtime has no
	// headroom for ClassSafety work when it arrives, and I11 requires that
	// headroom to exist rather than to be hoped for.
	SheddingThreshold float64
}

// DefaultSchedulerConfig returns the configuration used unless overridden.
func DefaultSchedulerConfig() SchedulerConfig {
	n := NumCPU()
	return SchedulerConfig{
		// Model calls are I/O-bound, so concurrency is not CPU-bounded. The
		// multiplier is a starting point to be re-baselined against measured
		// load, exactly as ADR-0011 re-baselines the latency budget at 30 days.
		MaxConcurrent:     n * 32,
		MaxQueued:         n * 64,
		QueueTimeout:      250 * time.Millisecond,
		SheddingThreshold: 0.85,
	}
}

func (c SchedulerConfig) validate() []string {
	var p []string
	if c.MaxConcurrent <= 0 {
		p = append(p, "scheduler: MaxConcurrent must be positive")
	}
	if c.MaxQueued < 0 {
		p = append(p, "scheduler: MaxQueued cannot be negative")
	}
	if c.QueueTimeout <= 0 {
		p = append(p, "scheduler: QueueTimeout must be positive")
	}
	if c.SheddingThreshold <= 0 || c.SheddingThreshold > 1 {
		p = append(p, "scheduler: SheddingThreshold must be in (0, 1]")
	}
	return p
}

// AdmissionDecision records why a task was admitted or refused. It is returned
// rather than logged so the caller can attribute a shed to a cause without
// correlating against a log line.
type AdmissionDecision struct {
	// Admitted reports the outcome.
	Admitted bool

	// Reason explains a refusal.
	Reason ShedReason

	// QueuedFor records how long the task waited.
	QueuedFor time.Duration

	// InFlightAtDecision is the concurrency observed when the decision was
	// made, for diagnosing a shed after the fact.
	InFlightAtDecision int
}

// ShedReason classifies a refusal.
type ShedReason string

const (
	// ShedNone means the task was admitted.
	ShedNone ShedReason = ""

	// ShedCapacity means concurrency was above the shedding threshold.
	ShedCapacity ShedReason = "capacity"

	// ShedQueueFull means the queue for this class was full.
	ShedQueueFull ShedReason = "queue_full"

	// ShedQueueTimeout means the task waited longer than QueueTimeout.
	ShedQueueTimeout ShedReason = "queue_timeout"

	// ShedDeadline means the task's own deadline had passed or was too close
	// for the work to plausibly complete.
	ShedDeadline ShedReason = "deadline"

	// ShedClosed means the scheduler is draining.
	ShedClosed ShedReason = "closed"
)

// Scheduler admits, queues and runs runtime work.
//
// It is a semaphore with priority classes and an admission policy, not a
// goroutine pool. Go's scheduler is already excellent at running goroutines;
// what it does not do is decide which work to refuse when there is too much,
// and that decision is this type's entire purpose.
type Scheduler struct {
	cfg     SchedulerConfig
	clock   Clock
	metrics *Metrics

	// slots is the concurrency semaphore. Buffered channel rather than
	// sync.Cond because acquisition must be selectable against a context and a
	// timeout, which a Cond cannot express.
	slots chan struct{}

	inFlight atomic.Int64
	queued   [4]atomic.Int64 // indexed by Class

	closed  atomic.Bool
	closing chan struct{}

	wg sync.WaitGroup
}

// NewScheduler constructs a scheduler.
func NewScheduler(cfg SchedulerConfig, clock Clock, metrics *Metrics) (*Scheduler, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if clock == nil {
		clock = SystemClock{}
	}
	if metrics == nil {
		metrics = NewMetrics()
	}

	s := &Scheduler{
		cfg:     cfg,
		clock:   clock,
		metrics: metrics,
		slots:   make(chan struct{}, cfg.MaxConcurrent),
		closing: make(chan struct{}),
	}
	for i := 0; i < cfg.MaxConcurrent; i++ {
		s.slots <- struct{}{}
	}
	return s, nil
}

// Admit acquires a concurrency slot for work of the given class.
//
// It returns a release function that must be called exactly once when the work
// completes. On refusal, release is a no-op and the decision explains why.
//
// The admission order below is the load-shedding policy, and its sequence
// matters: cheap local checks precede any wait, so a request that will be
// refused is refused immediately rather than after occupying a queue slot for
// QueueTimeout.
func (s *Scheduler) Admit(ctx context.Context, class Class, deadline time.Time) (release func(), decision AdmissionDecision) {
	noop := func() {}
	start := s.clock.Now()

	if s.closed.Load() {
		// Draining. ClassSafety is not exempt here: a closing runtime cannot
		// complete the work, and admitting it would hold the drain open for a
		// task that must fail anyway. I11 is about load, not shutdown.
		s.metrics.SchedulerShed.Inc(class.String(), string(ShedClosed))
		return noop, AdmissionDecision{Reason: ShedClosed}
	}

	// Refuse work that cannot finish. This is not an optimisation: admitting it
	// consumes a slot that would otherwise serve a request that can succeed.
	if !deadline.IsZero() && !s.clock.Now().Before(deadline) {
		s.metrics.SchedulerShed.Inc(class.String(), string(ShedDeadline))
		return noop, AdmissionDecision{Reason: ShedDeadline}
	}

	inFlight := int(s.inFlight.Load())

	// INVARIANT I11. ClassSafety and ClassSystem bypass every capacity check
	// and take a slot unconditionally, exceeding MaxConcurrent if necessary.
	//
	// Overshooting the concurrency limit is the lesser harm by a wide margin.
	// The alternative is queueing safety work behind ordinary work under load,
	// which is precisely the "skip fraud scoring under pressure" failure the
	// invariant forbids. The overshoot is bounded in practice because safety
	// work is a small fraction of volume, and it is measured so the assumption
	// is checked rather than assumed.
	if !class.Sheddable() {
		select {
		case <-s.slots:
			// A slot was free; ordinary accounting.
		default:
			// None free. Proceed anyway and record the overshoot.
			s.metrics.SchedulerOvershoot.Inc(class.String())
		}
		s.inFlight.Add(1)
		s.metrics.SchedulerInFlight.Set(float64(s.inFlight.Load()))
		return s.releaseFunc(class, start), AdmissionDecision{
			Admitted:           true,
			QueuedFor:          0,
			InFlightAtDecision: inFlight,
		}
	}

	// Sheddable classes: refuse above the threshold before queueing.
	threshold := int(float64(s.cfg.MaxConcurrent) * s.cfg.SheddingThreshold)
	if inFlight >= threshold {
		s.metrics.SchedulerShed.Inc(class.String(), string(ShedCapacity))
		return noop, AdmissionDecision{Reason: ShedCapacity, InFlightAtDecision: inFlight}
	}

	if q := s.queued[class].Load(); int(q) >= s.cfg.MaxQueued {
		s.metrics.SchedulerShed.Inc(class.String(), string(ShedQueueFull))
		return noop, AdmissionDecision{Reason: ShedQueueFull, InFlightAtDecision: inFlight}
	}

	s.queued[class].Add(1)
	defer s.queued[class].Add(-1)

	// Bound the wait by the smaller of QueueTimeout and the task's own
	// remaining budget. Waiting past the caller's deadline produces a task that
	// starts already doomed.
	wait := s.cfg.QueueTimeout
	if !deadline.IsZero() {
		if remaining := deadline.Sub(s.clock.Now()); remaining < wait {
			wait = remaining
		}
	}
	if wait <= 0 {
		s.metrics.SchedulerShed.Inc(class.String(), string(ShedDeadline))
		return noop, AdmissionDecision{Reason: ShedDeadline, InFlightAtDecision: inFlight}
	}

	timer := s.clock.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-s.slots:
		s.inFlight.Add(1)
		queuedFor := s.clock.Since(start)
		s.metrics.SchedulerInFlight.Set(float64(s.inFlight.Load()))
		s.metrics.SchedulerQueueWait.Observe(queuedFor.Seconds(), class.String())
		return s.releaseFunc(class, start), AdmissionDecision{
			Admitted:           true,
			QueuedFor:          queuedFor,
			InFlightAtDecision: inFlight,
		}

	case <-timer.C():
		s.metrics.SchedulerShed.Inc(class.String(), string(ShedQueueTimeout))
		return noop, AdmissionDecision{
			Reason:             ShedQueueTimeout,
			QueuedFor:          s.clock.Since(start),
			InFlightAtDecision: inFlight,
		}

	case <-ctx.Done():
		s.metrics.SchedulerShed.Inc(class.String(), string(ShedQueueTimeout))
		return noop, AdmissionDecision{
			Reason:             ShedQueueTimeout,
			QueuedFor:          s.clock.Since(start),
			InFlightAtDecision: inFlight,
		}

	case <-s.closing:
		s.metrics.SchedulerShed.Inc(class.String(), string(ShedClosed))
		return noop, AdmissionDecision{Reason: ShedClosed, InFlightAtDecision: inFlight}
	}
}

// releaseFunc returns an idempotent release for an admitted task.
func (s *Scheduler) releaseFunc(class Class, start time.Time) func() {
	var once sync.Once
	s.wg.Add(1)
	return func() {
		once.Do(func() {
			defer s.wg.Done()
			s.inFlight.Add(-1)
			s.metrics.SchedulerInFlight.Set(float64(s.inFlight.Load()))
			s.metrics.SchedulerDuration.Observe(s.clock.Since(start).Seconds(), class.String())
			// Return the slot without blocking. A non-sheddable task that
			// overshot took no slot, so the buffer may already be full; the
			// default arm is that case, not an error.
			select {
			case s.slots <- struct{}{}:
			default:
			}
		})
	}
}

// InFlight reports currently executing tasks.
func (s *Scheduler) InFlight() int { return int(s.inFlight.Load()) }

// Queued reports tasks waiting in the given class.
func (s *Scheduler) Queued(c Class) int { return int(s.queued[c].Load()) }

// Utilisation reports in-flight work as a fraction of MaxConcurrent. It may
// exceed 1.0 when non-sheddable work has overshot, and that is meaningful
// rather than a bug — it is the signal that safety work is displacing capacity.
func (s *Scheduler) Utilisation() float64 {
	return float64(s.inFlight.Load()) / float64(s.cfg.MaxConcurrent)
}

// Close begins draining. New admissions are refused; in-flight work continues.
func (s *Scheduler) Close() {
	if s.closed.Swap(true) {
		return
	}
	close(s.closing)
}

// Wait blocks until every admitted task has released, or ctx ends.
//
// This is the drain half of Invariant I6: readiness goes false, then the
// process waits here, then it exits. Skipping the wait is what makes a
// 'graceful' shutdown drop live calls.
func (s *Scheduler) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("scheduler: drain incomplete, %d task(s) still in flight: %w",
			s.inFlight.Load(), ctx.Err())
	}
}
