package toolruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// CompletedWork is one successful mutating step, recorded so it can be undone.
type CompletedWork struct {
	// Step and Execution identify what ran.
	Step      StepID
	Execution ExecutionID
	// Descriptor is the pinned tool.
	Descriptor Descriptor
	// Invocation is what the tool received. Retained in memory for the
	// duration of the plan only: compensating a booking requires the arguments
	// that made it.
	Invocation Invocation
	// Produced is what the tool returned. Compensation frequently needs it —
	// undoing a booking needs the booking reference the tool itself minted.
	Produced Result
	// Tool is the implementation, held so compensation does not have to
	// re-resolve against a registry that may have changed mid-plan.
	Tool CompensatingTool
	// CompletedAt is the completion instant.
	CompletedAt time.Time
	// Effect classifies what was done.
	Effect Effect
}

// CompensationOutcome is what happened when a step was undone.
type CompensationOutcome struct {
	Step       StepID
	Descriptor Descriptor
	// Compensated reports success.
	Compensated bool
	// Skipped reports that no compensation was possible — the tool does not
	// implement one. NOT the same as a failure, and reported separately so an
	// operator can tell "we could not try" from "we tried and it did not work".
	Skipped bool
	// Err is the compensation failure, if any.
	Err error
	// Duration is how long it took.
	Duration time.Duration
}

// CompensationReport is the outcome of undoing a partially completed plan.
type CompensationReport struct {
	// Plan and Correlation identify what was rolled back.
	Plan        PlanID
	Correlation CorrelationID
	// Outcomes are in the order compensation was attempted, which is REVERSE
	// completion order.
	Outcomes []CompensationOutcome
	// Compensated, Skipped and Failed count the outcomes.
	Compensated int
	Skipped     int
	Failed      int
	// Complete reports that everything mutating was successfully undone.
	Complete bool
}

// Err returns a non-nil error when any compensation failed.
//
// A failed compensation is the worst outcome this runtime can produce: the
// world is now in a state nobody chose, and no further automation can be
// trusted to fix it. It is always surfaced, never swallowed, and it is why
// [ExecutionResult] carries the report rather than a boolean.
func (r CompensationReport) Err() error {
	if r.Failed == 0 {
		return nil
	}
	var failed []string
	for _, o := range r.Outcomes {
		if o.Err != nil {
			failed = append(failed, fmt.Sprintf("%s (%s)", o.Step, o.Descriptor))
		}
	}
	return fmt.Errorf("%w: %d of %d steps could not be undone: %v",
		ErrCompensationFailed, r.Failed, len(r.Outcomes), failed)
}

// Journal records completed mutating work for one plan.
//
// Per plan, not per runtime. A journal shared across plans would need to be
// filtered on every rollback and would keep completed work alive long after the
// plan that produced it ended — a memory leak whose contents are, by
// construction, the most sensitive data the runtime touches.
type Journal struct {
	mu    sync.Mutex
	work  []CompletedWork
	plan  PlanID
	corr  CorrelationID
	clock rt.Clock
}

// NewJournal builds a journal for one plan.
func NewJournal(plan PlanID, corr CorrelationID, clock rt.Clock) *Journal {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	return &Journal{plan: plan, corr: corr, clock: clock}
}

// Record adds completed work.
//
// READ-ONLY STEPS ARE NOT RECORDED. There is nothing to undo about a lookup,
// and recording them would mean a rollback report full of steps that were
// "compensated" by doing nothing — which makes the report's success count
// meaningless.
func (j *Journal) Record(w CompletedWork) {
	if !w.Effect.Mutating() {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.work = append(j.work, w)
}

// Len returns how many mutating steps are recorded.
func (j *Journal) Len() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.work)
}

// Work returns a copy in completion order.
func (j *Journal) Work() []CompletedWork {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]CompletedWork(nil), j.work...)
}

// Compensator undoes completed work.
type Compensator struct {
	clock   rt.Clock
	metrics *Metrics
	audit   Auditor
	events  *EventDispatcher
	timeout time.Duration
}

// NewCompensator builds a compensator.
func NewCompensator(clock rt.Clock, m *Metrics, a Auditor, e *EventDispatcher, timeout time.Duration) *Compensator {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Compensator{clock: clock, metrics: m, audit: a, events: e, timeout: timeout}
}

// Rollback undoes a journal's work in reverse completion order.
//
// REVERSE ORDER, ALWAYS. Steps that ran later may depend on state that earlier
// steps created; undoing the earlier one first can make the later one's
// compensation impossible. Reverse order is the only ordering that is correct
// in general, and it is worth being explicit that this is not a preference.
//
// EVERY STEP IS ATTEMPTED EVEN AFTER ONE FAILS (INV-TOOL-6). Stopping at the
// first failure would leave later steps un-compensated for no reason — the
// failure of one rollback says nothing about whether the next would work, and
// abandoning the rest guarantees more of the world stays wrong than necessary.
//
// It runs with its OWN context, derived from Background rather than from the
// execution's. Compensation is most often needed precisely because the
// execution's context was cancelled or timed out, and inheriting a dead context
// would mean rollback never runs in exactly the case it exists for.
func (c *Compensator) Rollback(j *Journal, reason string) CompensationReport {
	work := j.Work()
	report := CompensationReport{Plan: j.plan, Correlation: j.corr}

	for i := len(work) - 1; i >= 0; i-- {
		w := work[i]
		outcome := c.undo(w, reason)
		report.Outcomes = append(report.Outcomes, outcome)
		switch {
		case outcome.Skipped:
			report.Skipped++
		case outcome.Err != nil:
			report.Failed++
		default:
			report.Compensated++
		}
	}
	report.Complete = report.Failed == 0 && report.Skipped == 0
	return report
}

func (c *Compensator) undo(w CompletedWork, reason string) CompensationOutcome {
	out := CompensationOutcome{Step: w.Step, Descriptor: w.Descriptor}

	if w.Tool == nil {
		out.Skipped = true
		if c.metrics != nil {
			c.metrics.Compensations.Inc(string(w.Descriptor.Tool), "skipped")
		}
		c.record(w, AuditCompensated, "not_compensable", 0)
		return out
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	start := c.clock.Now()
	err := w.Tool.Compensate(ctx, w.Invocation, w.Produced)
	out.Duration = c.clock.Since(start)

	if err != nil {
		out.Err = err
		if c.metrics != nil {
			c.metrics.Compensations.Inc(string(w.Descriptor.Tool), "failed")
			c.metrics.CompensationFailure.Inc(string(w.Descriptor.Tool))
		}
		c.record(w, AuditCompensationFailed, shortReason(err), out.Duration)
		c.emit(w, EventRolledBack, "compensation_failed", out.Duration)
		return out
	}

	out.Compensated = true
	if c.metrics != nil {
		c.metrics.Compensations.Inc(string(w.Descriptor.Tool), "compensated")
	}
	c.record(w, AuditCompensated, reason, out.Duration)
	c.emit(w, EventRolledBack, reason, out.Duration)
	return out
}

func (c *Compensator) record(w CompletedWork, kind AuditKind, reason string, d time.Duration) {
	if c.audit == nil {
		return
	}
	_ = c.audit.Record(AuditEntry{
		At: c.clock.Now(), Kind: kind, Execution: w.Execution, Step: w.Step,
		Correlation: w.Invocation.Correlation, Session: w.Invocation.Session,
		Actor: w.Invocation.Actor, Descriptor: w.Descriptor,
		InputPrint: w.Invocation.Args.Fingerprint(), OutputPrint: w.Produced.Fingerprint(),
		Phase: PhaseCompensate, Reason: reason, Duration: d,
	})
}

func (c *Compensator) emit(w CompletedWork, t EventType, reason string, d time.Duration) {
	if c.events == nil {
		return
	}
	c.events.Dispatch(Event{
		Type: t, Execution: w.Execution, Step: w.Step,
		Correlation: w.Invocation.Correlation, Session: w.Invocation.Session,
		Actor: w.Invocation.Actor, Descriptor: w.Descriptor, Effect: w.Effect,
		Phase: PhaseCompensate, InputPrint: w.Invocation.Args.Fingerprint(),
		Reason: reason, Duration: d,
	})
}

// shortReason maps an error to a bounded machine-readable code.
//
// Bounded because it becomes a metric label and an event field. Passing an
// error's text straight through would let a downstream service's error message
// determine this platform's metric cardinality, which is a denial of service
// with extra steps.
func shortReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrTimeout):
		return "timeout"
	case errors.Is(err, ErrCancelled):
		return "cancelled"
	case errors.Is(err, ErrCircuitOpen):
		return "circuit_open"
	case errors.Is(err, ErrPermissionDenied):
		return "permission_denied"
	case errors.Is(err, ErrConsentRequired):
		return "consent_required"
	case errors.Is(err, ErrInvalidInput):
		return "invalid_input"
	case errors.Is(err, ErrInvalidOutput):
		return "invalid_output"
	case errors.Is(err, ErrBudgetExceeded):
		return "budget_exceeded"
	case errors.Is(err, ErrQueueFull):
		return "queue_full"
	case errors.Is(err, ErrDuplicate):
		return "duplicate"
	case errors.Is(err, ErrNoHealthyProvider):
		return "no_healthy_tool"
	case errors.Is(err, ErrNoCapability):
		return "no_capability"
	case errors.Is(err, ErrCompensationFailed):
		return "compensation_failed"
	case errors.Is(err, ErrClosed):
		return "closed"
	case errors.Is(err, ErrInvariant):
		return "invariant"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "tool_error"
	}
}
