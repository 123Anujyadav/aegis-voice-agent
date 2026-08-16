package toolruntime

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Class is an execution's scheduling class.
//
// Three, matching the three genuinely different deadlines this platform has. A
// finer grading would be a priority number, and priority numbers drift upwards
// until everything is priority one.
type Class uint8

// The scheduling classes.
const (
	// ClassInteractive is work a person is waiting on, inside a live call.
	// Its deadline is measured against ADR-0011's 900 ms p50 budget.
	ClassInteractive Class = iota

	// ClassBackground is work triggered by a conversation that nobody is
	// waiting on — a follow-up, a sync, an enrichment.
	ClassBackground

	// ClassBulk is batch work. Always yields to the other two.
	ClassBulk
)

// String renders the class. Used as a metric label.
func (c Class) String() string {
	switch c {
	case ClassBackground:
		return "background"
	case ClassBulk:
		return "bulk"
	default:
		return "interactive"
	}
}

const classCount = 3

// waiter is one blocked acquisition.
type waiter struct {
	ch       chan struct{}
	class    Class
	enqueued time.Time
	seq      uint64
}

// ToolScheduler admits executions and bounds concurrency.
//
// It is the runtime's load-shedding boundary. Frozen invariant I11 says that
// under load the platform sheds AT ADMISSION rather than degrading something
// safety-critical mid-flight, and this is where a tool execution is shed.
//
// The design is a bounded concurrency limit plus a bounded per-class wait
// queue. Two bounds, not one, because they answer different questions: the
// concurrency limit bounds how much work is in flight, and the queue bound
// answers "how long is it reasonable to make a caller wait before telling them
// no". An unbounded queue turns overload into latency, and latency in a live
// call is indistinguishable from failure to the person on the phone — except
// that it also holds resources while being indistinguishable.
type ToolScheduler struct {
	clock   rt.Clock
	metrics *Metrics

	maxConcurrent int
	maxQueued     [classCount]int
	// starvationRatio is how many higher-class grants may precede a forced
	// grant to a lower class. Without it a busy interactive load starves
	// background work indefinitely, and the background work is frequently the
	// thing that keeps the interactive path healthy.
	starvationRatio int

	mu       sync.Mutex
	inFlight int
	queues   [classCount][]*waiter
	// consecutive counts grants to classes above the one that is waiting.
	consecutive int
	seq         uint64

	admitted atomic.Uint64
	shed     atomic.Uint64
	granted  [classCount]atomic.Uint64
}

// SchedulerConfig configures a scheduler.
type SchedulerConfig struct {
	// MaxConcurrent bounds executions in flight.
	MaxConcurrent int
	// MaxQueuedInteractive, MaxQueuedBackground and MaxQueuedBulk bound each
	// class's wait queue.
	MaxQueuedInteractive int
	MaxQueuedBackground  int
	MaxQueuedBulk        int
	// StarvationRatio forces a lower-class grant after this many higher-class
	// grants while a lower class waits.
	StarvationRatio int
}

// DefaultSchedulerConfig returns the default configuration.
//
// The interactive queue is the SHORTEST, which looks backwards and is not.
// Interactive work has a deadline measured in hundreds of milliseconds; a deep
// queue for it means admitting work that will time out before it is served,
// which wastes a slot and still fails the caller. Bulk work has no deadline, so
// queueing it is free.
func DefaultSchedulerConfig() SchedulerConfig {
	return SchedulerConfig{
		MaxConcurrent:        64,
		MaxQueuedInteractive: 32,
		MaxQueuedBackground:  128,
		MaxQueuedBulk:        512,
		StarvationRatio:      8,
	}
}

func (c SchedulerConfig) validate() []string {
	var problems []string
	if c.MaxConcurrent <= 0 {
		problems = append(problems, "scheduler: MaxConcurrent must be positive")
	}
	if c.MaxQueuedInteractive < 0 || c.MaxQueuedBackground < 0 || c.MaxQueuedBulk < 0 {
		problems = append(problems, "scheduler: queue bounds must not be negative")
	}
	if c.StarvationRatio < 0 {
		problems = append(problems, "scheduler: StarvationRatio must not be negative")
	}
	return problems
}

// NewToolScheduler builds a scheduler.
func NewToolScheduler(cfg SchedulerConfig, clock rt.Clock, m *Metrics) (*ToolScheduler, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if clock == nil {
		clock = rt.SystemClock{}
	}
	s := &ToolScheduler{
		clock: clock, metrics: m,
		maxConcurrent:   cfg.MaxConcurrent,
		starvationRatio: cfg.StarvationRatio,
	}
	s.maxQueued = [classCount]int{cfg.MaxQueuedInteractive, cfg.MaxQueuedBackground, cfg.MaxQueuedBulk}
	return s, nil
}

// Acquire admits an execution, blocking until a slot is free.
//
// Returns ErrQueueFull immediately when the class's queue is at capacity —
// shedding at admission rather than accepting work it cannot serve. A caller
// receiving ErrQueueFull can degrade gracefully; a caller waiting behind a
// thousand others cannot do anything at all.
//
// The returned release function must be called exactly once. It is idempotent
// so that a defer plus an explicit call cannot double-free a slot, which would
// let the scheduler admit more than its limit and quietly stop being a limit.
func (s *ToolScheduler) Acquire(ctx context.Context, class Class) (func(), error) {
	if class >= classCount {
		class = ClassBulk
	}

	s.mu.Lock()
	if s.inFlight < s.maxConcurrent {
		s.inFlight++
		s.granted[class].Add(1)
		s.mu.Unlock()
		s.admitted.Add(1)
		s.observe(class, 0)
		return s.releaser(), nil
	}

	if len(s.queues[class]) >= s.maxQueued[class] {
		s.mu.Unlock()
		s.shed.Add(1)
		if s.metrics != nil {
			s.metrics.QueueRejected.Inc()
		}
		return nil, fmt.Errorf("%w: %s queue holds %d, limit is %d",
			ErrQueueFull, class, len(s.queues[class]), s.maxQueued[class])
	}

	s.seq++
	w := &waiter{ch: make(chan struct{}), class: class, enqueued: s.clock.Now(), seq: s.seq}
	s.queues[class] = append(s.queues[class], w)
	depth := s.depthLocked()
	s.mu.Unlock()

	if s.metrics != nil {
		s.metrics.QueueDepth.Set(float64(depth))
	}

	select {
	case <-w.ch:
		s.admitted.Add(1)
		s.observe(class, s.clock.Since(w.enqueued))
		return s.releaser(), nil
	case <-ctx.Done():
		// Remove from the queue. Leaving it would mean a later release grants a
		// slot to a waiter that has gone, and the slot stays occupied by
		// nobody — a leak that presents as gradually falling throughput.
		s.mu.Lock()
		s.removeLocked(w)
		s.mu.Unlock()
		return nil, ctx.Err()
	}
}

// TryAcquire admits without blocking.
func (s *ToolScheduler) TryAcquire(class Class) (func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlight >= s.maxConcurrent {
		return nil, false
	}
	s.inFlight++
	s.granted[class].Add(1)
	s.admitted.Add(1)
	return s.releaser(), true
}

func (s *ToolScheduler) releaser() func() {
	var once sync.Once
	return func() { once.Do(s.release) }
}

func (s *ToolScheduler) release() {
	s.mu.Lock()
	next := s.nextWaiterLocked()
	if next == nil {
		s.inFlight--
		if s.inFlight < 0 {
			s.inFlight = 0
		}
		depth := s.depthLocked()
		s.mu.Unlock()
		if s.metrics != nil {
			s.metrics.QueueDepth.Set(float64(depth))
			s.metrics.InFlight.Set(float64(s.InFlight()))
		}
		return
	}
	// The slot passes straight to the waiter, so inFlight does not change.
	s.granted[next.class].Add(1)
	depth := s.depthLocked()
	s.mu.Unlock()

	close(next.ch)
	if s.metrics != nil {
		s.metrics.QueueDepth.Set(float64(depth))
	}
}

// nextWaiterLocked picks the next waiter, applying anti-starvation.
//
// Strict priority with a forced yield: after starvationRatio consecutive grants
// while a lower class waits, the oldest lower-class waiter is served. Strict
// priority alone is correct until the moment the interactive load never drops,
// at which point background work stops entirely and nobody notices for a week.
func (s *ToolScheduler) nextWaiterLocked() *waiter {
	highest := -1
	lowestWaiting := -1
	for c := 0; c < classCount; c++ {
		if len(s.queues[c]) == 0 {
			continue
		}
		if highest < 0 {
			highest = c
		}
		lowestWaiting = c
	}
	if highest < 0 {
		s.consecutive = 0
		return nil
	}

	pick := highest
	if s.starvationRatio > 0 && lowestWaiting > highest && s.consecutive >= s.starvationRatio {
		pick = lowestWaiting
		s.consecutive = 0
	} else if lowestWaiting > highest {
		s.consecutive++
	} else {
		s.consecutive = 0
	}

	w := s.queues[pick][0]
	s.queues[pick] = s.queues[pick][1:]
	return w
}

func (s *ToolScheduler) removeLocked(target *waiter) {
	for c := 0; c < classCount; c++ {
		for i, w := range s.queues[c] {
			if w == target {
				s.queues[c] = append(s.queues[c][:i], s.queues[c][i+1:]...)
				return
			}
		}
	}
}

func (s *ToolScheduler) depthLocked() int {
	n := 0
	for c := 0; c < classCount; c++ {
		n += len(s.queues[c])
	}
	return n
}

func (s *ToolScheduler) observe(class Class, wait time.Duration) {
	if s.metrics != nil {
		s.metrics.QueueWait.Observe(wait.Seconds(), class.String())
		s.metrics.InFlight.Set(float64(s.InFlight()))
	}
}

// InFlight returns the current execution count.
func (s *ToolScheduler) InFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inFlight
}

// Depth returns the total queued count.
func (s *ToolScheduler) Depth() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.depthLocked()
}

// DepthByClass returns each class's queue depth.
func (s *ToolScheduler) DepthByClass() map[Class]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[Class]int{
		ClassInteractive: len(s.queues[ClassInteractive]),
		ClassBackground:  len(s.queues[ClassBackground]),
		ClassBulk:        len(s.queues[ClassBulk]),
	}
}

// Stats returns admission counters.
func (s *ToolScheduler) Stats() SchedulerStats {
	return SchedulerStats{
		Admitted:    s.admitted.Load(),
		Shed:        s.shed.Load(),
		Interactive: s.granted[ClassInteractive].Load(),
		Background:  s.granted[ClassBackground].Load(),
		Bulk:        s.granted[ClassBulk].Load(),
		InFlight:    s.InFlight(),
		Depth:       s.Depth(),
	}
}

// SchedulerStats is a scheduler's counters.
type SchedulerStats struct {
	Admitted    uint64
	Shed        uint64
	Interactive uint64
	Background  uint64
	Bulk        uint64
	InFlight    int
	Depth       int
}
