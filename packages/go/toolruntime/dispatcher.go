package toolruntime

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// PlanResult is the outcome of executing a whole plan.
type PlanResult struct {
	// Plan and Intent identify what ran.
	Plan   PlanID
	Intent IntentID
	// Correlation ties it to the conversation turn.
	Correlation CorrelationID
	// Steps are every step outcome, in completion order.
	Steps []ExecutionResult
	// Results maps each request ref to its output, for the caller to read
	// without walking the step list.
	Results map[string]Result
	// Err is the terminal error, nil when the plan succeeded.
	Err error
	// Compensation reports what was rolled back, populated only when a
	// rollback ran.
	Compensation *CompensationReport
	// Duration is the total elapsed time.
	Duration time.Duration
	// StartedAt and FinishedAt are runtime-clock instants.
	StartedAt  time.Time
	FinishedAt time.Time
}

// OK reports success.
func (r PlanResult) OK() bool { return r.Err == nil }

// Failed returns the step outcomes that failed.
func (r PlanResult) Failed() []ExecutionResult {
	var out []ExecutionResult
	for _, s := range r.Steps {
		if s.Err != nil {
			out = append(out, s)
		}
	}
	return out
}

// ExecutionDispatcher walks a plan tree and runs it.
//
// It interprets; it does not decide. Every choice — which tool, which version,
// what arguments, what order — was made by the planner and is frozen in the
// plan. That separation is what makes a plan reviewable: if the dispatcher
// could choose, reading the plan would tell you what MIGHT happen rather than
// what WILL.
type ExecutionDispatcher struct {
	executor    *Executor
	scheduler   *ToolScheduler
	compensator *Compensator
	clock       rt.Clock
	metrics     *Metrics
	supervisor  *ExecutionSupervisor
}

// Run executes a plan.
//
// The rollback rule is the important part: when a plan fails, everything
// mutating that already succeeded is undone, in reverse order, using a context
// that is not the failing one. A plan that fails halfway and leaves half its
// effects in place is worse than one that fails at the start, because nobody —
// not the caller, not the person on the phone, not the operator reading the
// logs — knows what state the world is in.
//
// Optional steps are exempt from failing the plan but NOT from compensation: an
// optional step that succeeded still changed something, and if the plan is
// being rolled back that change goes too.
func (d *ExecutionDispatcher) Run(ctx context.Context, plan Plan, grant Grant, sink StreamSink) PlanResult {
	start := d.clock.Now()

	res := PlanResult{
		Plan: plan.ID, Intent: plan.Intent, Correlation: plan.Correlation,
		Results: make(map[string]Result), StartedAt: start,
	}

	journal := NewJournal(plan.ID, plan.Correlation, d.clock)
	state := &planState{results: make(map[string]Result), sink: sink}

	d.supervisor.begin(plan.ID)
	defer d.supervisor.end(plan.ID)

	err := d.runStep(ctx, plan.Root, plan, grant, state, journal)

	res.Steps = state.outcomes()
	for ref, out := range state.snapshot() {
		res.Results[ref] = out
	}
	res.Err = err
	res.Duration = d.clock.Since(start)
	res.FinishedAt = d.clock.Now()

	if err != nil && journal.Len() > 0 {
		report := d.compensator.Rollback(journal, shortReason(err))
		res.Compensation = &report
		if cErr := report.Err(); cErr != nil {
			// A compensation failure REPLACES the original error. The original
			// failure is recoverable by retrying; a failed rollback is not, and
			// surfacing the lesser problem would let a caller retry into a
			// world it does not understand.
			res.Err = cErr
		}
	}
	return res
}

// planState is the mutable state of one plan execution.
type planState struct {
	mu       sync.Mutex
	results  map[string]Result
	steps    []ExecutionResult
	sink     StreamSink
	rollback atomic.Bool
}

func (s *planState) put(ref string, out Result) {
	if ref == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[ref] = out
}

func (s *planState) snapshot() map[string]Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Result, len(s.results))
	for k, v := range s.results {
		out[k] = v
	}
	return out
}

func (s *planState) add(r ExecutionResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = append(s.steps, r)
}

func (s *planState) outcomes() []ExecutionResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]ExecutionResult(nil), s.steps...)
	// Sorted by step ID rather than left in completion order, because a
	// parallel group completes in whatever order the scheduler happened to
	// choose and a result slice that reorders between identical runs makes
	// every assertion about it flaky.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Step < out[j].Step })
	return out
}

func (d *ExecutionDispatcher) runStep(ctx context.Context, step Step, plan Plan,
	grant Grant, state *planState, journal *Journal) error {

	switch step.Kind {
	case StepInvoke:
		return d.runInvoke(ctx, step, plan, grant, state, journal)
	case StepSequence:
		return d.runSequence(ctx, step, plan, grant, state, journal)
	case StepParallel:
		return d.runParallel(ctx, step, plan, grant, state, journal)
	case StepConditional:
		return d.runConditional(ctx, step, plan, grant, state, journal)
	case StepFallback:
		return d.runFallback(ctx, step, plan, grant, state, journal)
	default:
		return invariant("INV-TOOL-8", "plan contains unknown step kind %d", step.Kind)
	}
}

func (d *ExecutionDispatcher) runInvoke(ctx context.Context, step Step, plan Plan,
	grant Grant, state *planState, journal *Journal) error {

	bound, err := d.bind(step, state)
	if err != nil {
		out := ExecutionResult{Step: step.ID, Ref: step.Ref, Descriptor: step.Descriptor,
			Err: err, Phase: PhaseValidateIn}
		state.add(out)
		if step.Optional {
			return nil
		}
		return err
	}
	step.Args = bound

	release, err := d.scheduler.Acquire(ctx, classFor(step))
	if err != nil {
		out := ExecutionResult{Step: step.ID, Ref: step.Ref, Descriptor: step.Descriptor,
			Err: err, Phase: PhaseAdmit}
		state.add(out)
		if step.Optional {
			return nil
		}
		return err
	}
	defer release()

	result := d.executor.Execute(ctx, step, plan, grant, state.sink, journal)
	state.add(result)

	if result.Err != nil {
		if step.Optional {
			// An optional step's failure is recorded and forgiven. That is the
			// whole point: failing to enrich a call with the caller's history
			// must not stop the call being answered.
			return nil
		}
		return result.Err
	}
	state.put(step.Ref, result.Result)
	return nil
}

func (d *ExecutionDispatcher) runSequence(ctx context.Context, step Step, plan Plan,
	grant Grant, state *planState, journal *Journal) error {

	for _, child := range step.Children {
		if err := d.runStep(ctx, child, plan, grant, state, journal); err != nil {
			return err
		}
	}
	return nil
}

// runParallel runs children concurrently and waits for all of them.
//
// IT WAITS FOR ALL OF THEM EVEN AFTER ONE FAILS. Cancelling siblings on the
// first failure sounds efficient and produces the worst possible state: a
// mutating sibling cancelled mid-flight may or may not have taken effect, and
// nothing downstream can tell which. Letting every branch reach a definite
// outcome means the journal knows exactly what needs undoing.
func (d *ExecutionDispatcher) runParallel(ctx context.Context, step Step, plan Plan,
	grant Grant, state *planState, journal *Journal) error {

	var wg sync.WaitGroup
	errs := make([]error, len(step.Children))

	for i, child := range step.Children {
		wg.Add(1)
		go func(i int, child Step) {
			defer wg.Done()
			errs[i] = d.runStep(ctx, child, plan, grant, state, journal)
		}(i, child)
	}
	wg.Wait()

	// The first error in CHILD ORDER, not in completion order, so the same
	// failure is reported the same way every time.
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *ExecutionDispatcher) runConditional(ctx context.Context, step Step, plan Plan,
	grant Grant, state *planState, journal *Journal) error {

	if step.Condition == nil {
		return invariant("INV-TOOL-8", "conditional step %s has no condition", step.ID)
	}
	if !step.Condition.Evaluate(state.snapshot()) {
		for _, leaf := range step.Leaves() {
			state.add(ExecutionResult{Step: leaf.ID, Ref: leaf.Ref,
				Descriptor: leaf.Descriptor, Skipped: true, Phase: PhasePlan})
		}
		return nil
	}
	for _, child := range step.Children {
		if err := d.runStep(ctx, child, plan, grant, state, journal); err != nil {
			return err
		}
	}
	return nil
}

// runFallback tries children in order until one succeeds.
//
// A fallback over a mutating tool is dangerous in exactly one way: if the first
// candidate's failure is a TIMEOUT, it may have taken effect. Trying the next
// one then risks doing it twice. That is why the loop stops on a timeout for
// mutating steps rather than falling through, and why the planner refuses a
// fallback chain over an irreversible tool outright.
func (d *ExecutionDispatcher) runFallback(ctx context.Context, step Step, plan Plan,
	grant Grant, state *planState, journal *Journal) error {

	var last error
	for rank, child := range step.Children {
		child.Optional = false // the CHAIN may be optional; a branch is not
		err := d.runInvoke(ctx, child, plan, grant, state, journal)
		if err == nil {
			if rank > 0 && d.metrics != nil {
				d.metrics.FallbacksUsed.Inc(string(step.Capability))
			}
			return nil
		}
		last = err

		if child.Effect.Mutating() && errors.Is(err, ErrTimeout) {
			break
		}
		if errors.Is(err, ErrCancelled) || errors.Is(err, ErrClosed) {
			break
		}
	}
	if step.Optional {
		return nil
	}
	return last
}

// bind fills a step's arguments from earlier results.
func (d *ExecutionDispatcher) bind(step Step, state *planState) (Arguments, error) {
	if len(step.Bindings) == 0 {
		return step.Args, nil
	}
	results := state.snapshot()
	args := step.Args.Clone()
	if args == nil {
		args = Arguments{}
	}

	// Sorted so that two bindings targeting one argument resolve the same way
	// every time. A configuration where that happens is a caller bug, but a
	// non-deterministic caller bug is far harder to find than a consistent one.
	bindings := append([]Binding(nil), step.Bindings...)
	sort.Slice(bindings, func(i, j int) bool {
		if bindings[i].ToArg != bindings[j].ToArg {
			return bindings[i].ToArg < bindings[j].ToArg
		}
		return bindings[i].FromRef < bindings[j].FromRef
	})

	for _, b := range bindings {
		src, ok := results[b.FromRef]
		if !ok {
			if b.Required {
				return nil, invariant("INV-TOOL-8",
					"step %s binds from %s, which produced no result", step.ID, b.FromRef)
			}
			continue
		}
		v, present := src[b.FromField]
		if !present || v.IsNull() {
			if b.Required {
				return nil, invariant("INV-TOOL-8",
					"step %s requires %s.%s, which is absent", step.ID, b.FromRef, b.FromField)
			}
			continue
		}
		args[b.ToArg] = v
	}
	return args, nil
}

// classFor picks a scheduling class for a step.
//
// Read-only work is interactive by default because a lookup is nearly always
// something a conversation is waiting on. Mutating work is background unless
// it is irreversible, which is treated as interactive on the grounds that if
// the platform is about to do something it can never undo, it should not be
// doing it while queued behind a batch job.
func classFor(step Step) Class {
	switch step.Effect {
	case EffectRead, EffectIrreversible:
		return ClassInteractive
	default:
		return ClassBackground
	}
}

// invokeOutcome carries a tool call's result off the goroutine that made it.
//
// A named type rather than an anonymous struct because the supervisor also
// receives the channel when a call is abandoned, and two structurally identical
// anonymous types are still two types as far as a function signature is
// concerned.
type invokeOutcome struct {
	res Result
	err error
}

// ExecutionSupervisor tracks in-flight plans and abandoned goroutines.
//
// It exists because of one thing Go cannot do: kill a goroutine. When a tool
// ignores context cancellation, the runtime abandons it, and an abandoned
// goroutine holds whatever the tool holds — a connection, a buffer, a lock —
// for as long as it takes to finish, which may be never.
//
// Counting abandonments turns an invisible leak into a number on a dashboard.
// A tool with a rising abandonment count is a tool that does not honour
// cancellation, and that is a bug report with a name on it.
type ExecutionSupervisor struct {
	clock rt.Clock

	mu       sync.Mutex
	inFlight map[PlanID]time.Time

	abandoned atomic.Uint64
	byTool    sync.Map // ToolID -> *atomic.Uint64
}

// NewExecutionSupervisor builds a supervisor.
func NewExecutionSupervisor(clock rt.Clock) *ExecutionSupervisor {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	return &ExecutionSupervisor{clock: clock, inFlight: make(map[PlanID]time.Time)}
}

func (s *ExecutionSupervisor) begin(p PlanID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlight[p] = s.clock.Now()
}

func (s *ExecutionSupervisor) end(p PlanID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inFlight, p)
}

// abandon records a goroutine the runtime gave up waiting for, and drains its
// channel in the background so the goroutine can finish rather than blocking
// forever on a send nobody is receiving.
func (s *ExecutionSupervisor) abandon(d Descriptor, ch chan invokeOutcome) {
	s.abandoned.Add(1)
	v, _ := s.byTool.LoadOrStore(d.Tool, new(atomic.Uint64))
	v.(*atomic.Uint64).Add(1)

	go func() { <-ch }()
}

// InFlight returns how many plans are running.
func (s *ExecutionSupervisor) InFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.inFlight)
}

// Oldest returns the age of the longest-running plan, or zero when idle.
func (s *ExecutionSupervisor) Oldest() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	var oldest time.Duration
	now := s.clock.Now()
	for _, started := range s.inFlight {
		if age := now.Sub(started); age > oldest {
			oldest = age
		}
	}
	return oldest
}

// Abandoned returns the total abandoned-goroutine count.
func (s *ExecutionSupervisor) Abandoned() uint64 { return s.abandoned.Load() }

// AbandonedByTool returns the per-tool abandonment counts, sorted by tool.
func (s *ExecutionSupervisor) AbandonedByTool() map[ToolID]uint64 {
	out := make(map[ToolID]uint64)
	s.byTool.Range(func(k, v any) bool {
		out[k.(ToolID)] = v.(*atomic.Uint64).Load()
		return true
	})
	return out
}
