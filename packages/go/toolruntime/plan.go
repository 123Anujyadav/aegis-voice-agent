package toolruntime

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// StepKind classifies a plan node.
type StepKind uint8

// The five step kinds, matching the five shapes the Phase 10D brief requires.
const (
	// StepInvoke calls one tool at one pinned version. The only kind that
	// executes anything; every other kind is control flow over these.
	StepInvoke StepKind = iota

	// StepSequence runs children in order, stopping at the first failure.
	StepSequence

	// StepParallel runs children concurrently.
	StepParallel

	// StepConditional runs its single child only when the condition holds.
	StepConditional

	// StepFallback tries children in order until one succeeds.
	StepFallback
)

// String renders the kind. Used as a metric label and in plan explanations.
func (k StepKind) String() string {
	switch k {
	case StepSequence:
		return "sequence"
	case StepParallel:
		return "parallel"
	case StepConditional:
		return "conditional"
	case StepFallback:
		return "fallback"
	default:
		return "invoke"
	}
}

// Step is one node of a plan.
//
// A tree, not a graph. A DAG would let one step feed two others without
// re-running, which sounds better and is worse: the execution order of a
// general DAG is not obvious from reading it, and a plan whose order is not
// obvious from reading it cannot be reviewed by the person who has to approve
// it. Data dependencies are expressed with [Binding], which reads results from
// a shared map rather than requiring a shared edge.
type Step struct {
	// ID identifies the step within the plan. Derived from the request ref
	// where there is one, so a step keeps its identity across replans and two
	// traces of the same intent can be compared line by line.
	ID StepID

	// Kind classifies the node.
	Kind StepKind

	// Ref is the originating request's ref, empty for synthesised nodes.
	Ref string

	// Capability is what this step satisfies. Present on composites too, so a
	// fallback node says what it is falling back FOR.
	Capability CapabilityID

	// Descriptor is the pinned tool and version. Invoke steps only.
	//
	// PINNED AT PLAN TIME. Resolving again at execution time would let a
	// registry change between planning and execution silently substitute a
	// different implementation, and the audit record would name a tool that
	// never ran.
	Descriptor Descriptor

	// Contract is the snapshot the step was planned against. Carried rather
	// than looked up for exactly the same reason the descriptor is pinned.
	Contract Contract

	// Args are the statically-known arguments.
	Args Arguments

	// Bindings fill further arguments from earlier results.
	Bindings []Binding

	// Condition gates a conditional step.
	Condition *Condition

	// Children are the sub-steps of a composite.
	Children []Step

	// Optional means a failure here does not fail the plan.
	Optional bool

	// Compensable reports that the tool can undo this step.
	Compensable bool

	// Effect classifies what this step does to the world, copied from the
	// contract so a plan can be assessed without dereferencing contracts.
	Effect Effect
}

// Leaves returns every invoke step beneath this one, in execution order.
func (s Step) Leaves() []Step {
	if s.Kind == StepInvoke {
		return []Step{s}
	}
	var out []Step
	for _, c := range s.Children {
		out = append(out, c.Leaves()...)
	}
	return out
}

// Plan is an inert description of what would happen.
//
// BUILDING A PLAN EXECUTES NOTHING (INV-TOOL-8). No tool is invoked, no
// registry is mutated, no event is published, and nothing outside the runtime
// observes that planning occurred. That is what makes a plan reviewable before
// it runs, replayable after it runs, and testable without a tool in sight —
// and it is why every plan-shape test in this module registers contracts with
// a nil implementation.
type Plan struct {
	// ID identifies the plan.
	ID PlanID
	// Intent is what it was built from.
	Intent IntentID
	// Correlation, Session and Actor are carried for audit and events.
	Correlation CorrelationID
	Session     SessionID
	Actor       ActorID
	// Root is the top-level step.
	Root Step
	// Deadline bounds the whole plan.
	Deadline time.Time
	// Budget bounds the whole plan.
	Budget Budget
	// BuiltAt is the planning instant on the runtime clock.
	BuiltAt time.Time
	// Shape names the plan's structure, for metrics and at-a-glance reading.
	Shape string
}

// Steps returns every invoke step, in execution order.
func (p Plan) Steps() []Step { return p.Root.Leaves() }

// StepCount returns how many tools would be invoked at most.
func (p Plan) StepCount() int { return len(p.Steps()) }

// Mutates reports whether any step changes the world.
//
// The question a caller asks before deciding whether a plan needs confirmation
// from a person. A read-only plan can run on a hunch; a mutating one should not.
func (p Plan) Mutates() bool {
	for _, s := range p.Steps() {
		if s.Effect.Mutating() {
			return true
		}
	}
	return false
}

// Irreversible reports whether any step cannot be undone by any means.
func (p Plan) Irreversible() bool {
	for _, s := range p.Steps() {
		if s.Effect == EffectIrreversible {
			return true
		}
	}
	return false
}

// FullyCompensable reports whether every mutating step can be rolled back.
//
// ANSWERED BEFORE ANYTHING RUNS. A caller that learns after a partial failure
// that half of what it did cannot be undone has learned it too late. This lets
// the decision be made while it is still a decision.
func (p Plan) FullyCompensable() bool {
	for _, s := range p.Steps() {
		if s.Effect.Mutating() && !s.Compensable {
			return false
		}
	}
	return true
}

// Tools returns the distinct pinned descriptors, sorted.
func (p Plan) Tools() []Descriptor {
	seen := make(map[Descriptor]bool)
	var out []Descriptor
	for _, s := range p.Steps() {
		if !seen[s.Descriptor] {
			seen[s.Descriptor] = true
			out = append(out, s.Descriptor)
		}
	}
	sortDescriptors(out)
	return out
}

// Explain renders the plan as an indented tree.
//
// For operators, incident reviews and test failure messages. A plan that can
// only be understood by reading Go structs is a plan that will be approved
// without being understood.
func (p Plan) Explain() string {
	var b strings.Builder
	fmt.Fprintf(&b, "plan %s (%s) intent=%s steps=%d mutates=%v compensable=%v\n",
		p.ID, p.Shape, p.Intent, p.StepCount(), p.Mutates(), p.FullyCompensable())
	explainStep(&b, p.Root, 1)
	return b.String()
}

func explainStep(b *strings.Builder, s Step, depth int) {
	indent := strings.Repeat("  ", depth)
	switch s.Kind {
	case StepInvoke:
		fmt.Fprintf(b, "%s%s %s [%s]", indent, s.ID, s.Descriptor, s.Effect)
		if s.Optional {
			b.WriteString(" optional")
		}
		if len(s.Bindings) > 0 {
			b.WriteString(" bindings=")
			for i, bind := range s.Bindings {
				if i > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(b, "%s.%s->%s", bind.FromRef, bind.FromField, bind.ToArg)
			}
		}
		b.WriteByte('\n')
	default:
		fmt.Fprintf(b, "%s%s %s", indent, s.ID, s.Kind)
		if s.Condition != nil {
			fmt.Fprintf(b, " if %s.%s %s", s.Condition.FromRef, s.Condition.Field, s.Condition.Op)
		}
		if s.Capability != "" {
			fmt.Fprintf(b, " for %s", s.Capability)
		}
		b.WriteByte('\n')
		for _, c := range s.Children {
			explainStep(b, c, depth+1)
		}
	}
}

// Fingerprint returns a stable fingerprint of the plan's structure.
//
// Two plans with the same fingerprint would do the same thing. Used by tests to
// assert determinism and by operators to answer "did the plan change" without
// diffing two trees by eye.
func (p Plan) Fingerprint() Fingerprint {
	var b strings.Builder
	fingerprintStep(&b, p.Root)
	return fingerprintOf([]byte(b.String()))
}

func fingerprintStep(b *strings.Builder, s Step) {
	b.WriteString(string(s.ID))
	b.WriteByte('|')
	b.WriteString(s.Kind.String())
	b.WriteByte('|')
	b.WriteString(s.Descriptor.String())
	b.WriteByte('|')
	b.Write(s.Args.canonicalBytes())
	b.WriteByte('|')
	for _, bind := range s.Bindings {
		fmt.Fprintf(b, "%s.%s>%s,", bind.FromRef, bind.FromField, bind.ToArg)
	}
	b.WriteByte('{')
	for _, c := range s.Children {
		fingerprintStep(b, c)
		b.WriteByte(';')
	}
	b.WriteByte('}')
}

// Planner turns intents into plans.
type Planner struct {
	discovery *Discovery
	metrics   *Metrics
	now       func() time.Time
	defBudget Budget
	defTTL    time.Duration
}

// NewPlanner builds a planner.
func NewPlanner(d *Discovery, m *Metrics, now func() time.Time, def Budget, ttl time.Duration) *Planner {
	return &Planner{discovery: d, metrics: m, now: now, defBudget: def, defTTL: ttl}
}

// Plan builds an execution plan from an intent.
//
// Ordering is derived from dependencies, never from declaration order.
// Independent requests become a parallel group; dependent ones become a
// sequence of parallel groups — a level-by-level topological sort. That gives
// the maximum concurrency the dependencies permit without the caller having to
// think about it, and it is deterministic because each level is sorted by ref.
func (p *Planner) Plan(intent ToolIntent) (Plan, error) {
	start := p.now()

	if err := intent.Validate(); err != nil {
		p.failed("invalid_intent")
		return Plan{}, err
	}

	// Resolve every request first, so a plan is either fully resolvable or
	// rejected. A partially-resolved plan that fails at step four has already
	// executed steps one to three, and if any of them mutated something the
	// caller is now in a state nobody planned for.
	resolved := make(map[string]Step, len(intent.Requests))
	order := make([]string, 0, len(intent.Requests))

	for idx, req := range intent.Requests {
		ref := req.Ref
		if ref == "" {
			ref = fmt.Sprintf("r%d", idx)
		}
		step, err := p.planRequest(ref, req, intent)
		if err != nil {
			p.failed("unresolvable")
			return Plan{}, err
		}
		resolved[ref] = step
		order = append(order, ref)
	}

	levels, err := levelise(intent.Requests, order)
	if err != nil {
		p.failed("cycle")
		return Plan{}, err
	}

	root, shape := assemble(levels, resolved)

	budget := p.defBudget
	budget = budget.tighten(intent.Budget)

	deadline := intent.Deadline
	if deadline.IsZero() {
		ttl := p.defTTL
		if budget.WallClock > 0 && budget.WallClock < ttl {
			ttl = budget.WallClock
		}
		deadline = start.Add(ttl)
	}

	plan := Plan{
		ID:          NewPlanID(),
		Intent:      intent.ID,
		Correlation: intent.Correlation,
		Session:     intent.Session,
		Actor:       intent.Actor,
		Root:        root,
		Deadline:    deadline,
		Budget:      budget,
		BuiltAt:     start,
		Shape:       shape,
	}

	if p.metrics != nil {
		p.metrics.PlansBuilt.Inc(shape)
		p.metrics.PlanSteps.Observe(float64(plan.StepCount()), shape)
		p.metrics.PlanLatency.Observe(p.now().Sub(start).Seconds())
	}
	return plan, nil
}

func (p *Planner) failed(reason string) {
	if p.metrics != nil {
		p.metrics.PlanFailures.Inc(reason)
	}
}

// planRequest resolves one request into a step, with a fallback chain when the
// request asked for one.
func (p *Planner) planRequest(ref string, req CapabilityRequest, intent ToolIntent) (Step, error) {
	max := 1
	if req.Fallback {
		max = DefaultMaxCandidates
	}
	candidates, err := p.discovery.Resolve(Request{
		Capability:    req.Capability,
		Version:       req.Version,
		PreferTool:    req.PreferTool,
		MaxCandidates: max,
	})
	if err != nil {
		return Step{}, fmt.Errorf("request %s: %w", ref, err)
	}

	invokes := make([]Step, 0, len(candidates))
	for i, c := range candidates {
		id := StepID(ref)
		if len(candidates) > 1 {
			id = StepID(fmt.Sprintf("%s#%d", ref, i))
		}
		s := Step{
			ID:          id,
			Kind:        StepInvoke,
			Ref:         ref,
			Capability:  req.Capability,
			Descriptor:  c.Descriptor(),
			Contract:    c.Registration.Contract,
			Args:        req.Args.Clone(),
			Bindings:    append([]Binding(nil), req.Bindings...),
			Optional:    req.Optional,
			Compensable: c.Registration.Contract.Compensable,
			Effect:      c.Registration.Contract.Effect,
		}
		if err := p.checkStatic(s); err != nil {
			return Step{}, err
		}
		invokes = append(invokes, s)
	}

	step := invokes[0]
	if len(invokes) > 1 {
		// A fallback chain over an irreversible step is refused. "Try the next
		// one" after an unanswered irreversible call means possibly doing it
		// twice, and the whole point of the classification is that nobody can
		// tell whether the first one landed.
		if invokes[0].Effect == EffectIrreversible {
			return Step{}, invariant("INV-TOOL-6",
				"request %s asks for a fallback chain over an irreversible tool %s",
				ref, invokes[0].Descriptor)
		}
		step = Step{
			ID: StepID(ref), Kind: StepFallback, Ref: ref,
			Capability: req.Capability, Children: invokes,
			Optional: req.Optional,
			// A fallback is compensable only if every branch is: the caller
			// cannot know in advance which branch will have run.
			Compensable: allCompensable(invokes),
			Effect:      invokes[0].Effect,
		}
	}

	if req.Condition != nil {
		cond := *req.Condition
		step = Step{
			ID: StepID(ref + "?"), Kind: StepConditional, Ref: ref,
			Capability: req.Capability, Condition: &cond,
			Children: []Step{step}, Optional: req.Optional,
			Compensable: step.Compensable, Effect: step.Effect,
		}
	}
	return step, nil
}

func allCompensable(steps []Step) bool {
	for _, s := range steps {
		if !s.Compensable {
			return false
		}
	}
	return true
}

// checkStatic validates what can be known at plan time.
//
// Arguments arriving through a binding are not known yet, so this checks only
// that every binding targets a field the contract actually declares, and that
// the statically-supplied arguments are individually well-formed. Full
// validation happens at invoke time and is authoritative; this is a fast fail
// for the mistakes that do not need a tool to be reachable.
func (p *Planner) checkStatic(s Step) error {
	bound := make(map[string]bool, len(s.Bindings))
	for _, b := range s.Bindings {
		spec, ok := s.Contract.InputSpec(b.ToArg)
		if !ok {
			return fmt.Errorf("%w: %s has no input field %s to bind into",
				ErrInvalidInput, s.Descriptor, b.ToArg)
		}
		_ = spec
		bound[b.ToArg] = true
	}

	for name, v := range s.Args {
		spec, ok := s.Contract.InputSpec(name)
		if !ok {
			return fmt.Errorf("%w: %s has no input field %s",
				ErrInvalidInput, s.Descriptor, name)
		}
		if err := spec.check(v); err != nil {
			return fmt.Errorf("%s: %w", s.Descriptor, err)
		}
	}

	for _, f := range s.Contract.Input {
		if !f.Required || f.HasDefault {
			continue
		}
		if _, given := s.Args[f.Name]; !given && !bound[f.Name] {
			return fmt.Errorf("%w: %s requires input %s, which is neither supplied "+
				"nor bound", ErrInvalidInput, s.Descriptor, f.Name)
		}
	}
	return nil
}

// levelise groups refs into dependency levels.
//
// Kahn's algorithm with a sorted ready set, so the same intent always produces
// the same levels in the same order. An unsorted ready set would produce plans
// that differ between runs, and a plan fingerprint that changes for no reason
// is a plan fingerprint nobody trusts.
func levelise(reqs []CapabilityRequest, order []string) ([][]string, error) {
	deps := make(map[string]map[string]bool, len(order))
	index := make(map[string]CapabilityRequest, len(order))
	for i, r := range reqs {
		ref := r.Ref
		if ref == "" {
			ref = order[i]
		}
		index[ref] = r
		set := make(map[string]bool)
		for _, d := range r.DependsOn {
			set[d] = true
		}
		for _, b := range r.Bindings {
			set[b.FromRef] = true
		}
		if r.Condition != nil {
			set[r.Condition.FromRef] = true
		}
		deps[ref] = set
	}

	done := make(map[string]bool, len(order))
	var levels [][]string

	for len(done) < len(order) {
		var ready []string
		for _, ref := range order {
			if done[ref] {
				continue
			}
			satisfied := true
			for d := range deps[ref] {
				if _, known := deps[d]; known && !done[d] {
					satisfied = false
					break
				}
			}
			if satisfied {
				ready = append(ready, ref)
			}
		}
		if len(ready) == 0 {
			return nil, invariant("INV-TOOL-8",
				"plan has a dependency cycle among %v", remaining(order, done))
		}
		sort.Strings(ready)
		levels = append(levels, ready)
		for _, ref := range ready {
			done[ref] = true
		}
	}
	return levels, nil
}

func remaining(order []string, done map[string]bool) []string {
	var out []string
	for _, ref := range order {
		if !done[ref] {
			out = append(out, ref)
		}
	}
	return out
}

// assemble builds the step tree from levels, choosing the simplest shape that
// expresses it.
//
// The shape names matter: they are metric labels and the first thing an
// operator reads. A plan reported as "sequential" when it is one tool call
// makes every dashboard harder to read for no gain.
func assemble(levels [][]string, resolved map[string]Step) (Step, string) {
	if len(levels) == 1 && len(levels[0]) == 1 {
		s := resolved[levels[0][0]]
		return s, shapeOf(s)
	}

	groups := make([]Step, 0, len(levels))
	parallel := false
	for i, level := range levels {
		if len(level) == 1 {
			groups = append(groups, resolved[level[0]])
			continue
		}
		parallel = true
		children := make([]Step, 0, len(level))
		for _, ref := range level {
			children = append(children, resolved[ref])
		}
		groups = append(groups, Step{
			ID: StepID(fmt.Sprintf("level%d", i)), Kind: StepParallel, Children: children,
		})
	}

	if len(groups) == 1 {
		return groups[0], "parallel"
	}

	shape := "sequential"
	if parallel {
		shape = "mixed"
	}
	return Step{ID: "root", Kind: StepSequence, Children: groups}, shape
}

func shapeOf(s Step) string {
	switch s.Kind {
	case StepFallback:
		return "fallback"
	case StepConditional:
		return "conditional"
	case StepParallel:
		return "parallel"
	case StepSequence:
		return "sequential"
	default:
		return "single"
	}
}
