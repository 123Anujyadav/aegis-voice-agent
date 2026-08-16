package evaluation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Harness wires an evaluation runtime for test with a controllable clock.
//
// Exported rather than test-only because every adapter author needs it: writing
// a [Subject] adapter without a way to run a scenario against it means writing
// it blind, and a harness only this package can use pushes every adapter into
// reinventing one.
type Harness struct {
	// Clock is the controllable clock.
	Clock *rt.FakeClock
	// Metrics is the platform's instrument set.
	Metrics *Metrics
	// Runtime is the system under test.
	Runtime *EvaluationRuntime
	// Storage is the in-memory store.
	Storage *MemoryStorage
	// Fake is the scriptable subject, present unless real subjects were
	// supplied.
	Fake *FakeSubject
}

// HarnessOption customises a harness.
type HarnessOption func(*harnessOptions)

type harnessOptions struct {
	cfg      *Config
	logger   *slog.Logger
	subjects []Subject
}

// WithHarnessConfig overrides the configuration.
func WithHarnessConfig(c Config) HarnessOption {
	return func(o *harnessOptions) { o.cfg = &c }
}

// WithHarnessLogger sets the logger. Defaults to discarding, so a passing test
// is silent and a failing one is readable.
func WithHarnessLogger(l *slog.Logger) HarnessOption {
	return func(o *harnessOptions) { o.logger = l }
}

// WithHarnessSubjects supplies real subjects instead of the fake.
func WithHarnessSubjects(subjects ...Subject) HarnessOption {
	return func(o *harnessOptions) { o.subjects = subjects }
}

// NewHarness builds an evaluation runtime wired for test.
func NewHarness(opts ...HarnessOption) (*Harness, error) {
	o := &harnessOptions{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, opt := range opts {
		opt(o)
	}

	clock := rt.NewFakeClock(rt.SystemClock{}.Now().Truncate(0))
	metrics := NewMetrics()
	storage := NewMemoryStorage()

	cfg := DefaultConfig()
	if o.cfg != nil {
		cfg = *o.cfg
	}

	h := &Harness{Clock: clock, Metrics: metrics, Storage: storage}

	subjects := o.subjects
	if len(subjects) == 0 {
		h.Fake = NewFakeSubject("fake")
		subjects = []Subject{h.Fake}
	}

	r, err := New(cfg, NewSubjectSet(subjects...),
		WithClock(clock), WithMetrics(metrics), WithLogger(o.logger), WithStorage(storage))
	if err != nil {
		return nil, err
	}
	h.Runtime = r
	return h, nil
}

// Register adds scenarios, failing loudly on an invalid one.
//
// Panics rather than returning an error because a test that registers an
// invalid scenario has a bug in the test, and threading an error return through
// every setup line buries the assertion the test is actually about.
func (h *Harness) Register(scenarios ...Scenario) *Harness {
	if err := h.Runtime.Registry().RegisterScenarios(scenarios...); err != nil {
		panic("evaluation: harness registration failed: " + err.Error())
	}
	return h
}

// RegisterSuite adds a suite.
func (h *Harness) RegisterSuite(s Suite) *Harness {
	if err := h.Runtime.Registry().RegisterSuites(s); err != nil {
		panic("evaluation: harness suite registration failed: " + err.Error())
	}
	return h
}

// Approve records and approves a golden from a fresh run, for tests that need a
// baseline in place.
func (h *Harness) Approve(s Scenario) Golden {
	res := h.Runtime.Execute(context.Background(), s)
	g, err := h.Runtime.Goldens().RecordAndApprove(s, res.Observation,
		"harness", "baseline established in test")
	if err != nil {
		panic("evaluation: harness approve failed: " + err.Error())
	}
	return g
}

// Run executes every registered scenario.
func (h *Harness) Run(label string) Run {
	return h.Runtime.RunAll(context.Background(), label)
}

// ---------------------------------------------------------------------------
// Scenario builders
// ---------------------------------------------------------------------------

// SimpleScenario builds a one-step scenario, the common test shape.
func SimpleScenario(id ScenarioID, subject SubjectName, op string) Scenario {
	return Scenario{
		ID: id, Version: 1, Kind: KindRuntime, Title: string(id), Owner: "test",
		SubjectName: subject,
		Steps:       []Step{{Name: "only", Op: op}},
	}
}

// StepsScenario builds a scenario from operations, naming steps by position.
func StepsScenario(id ScenarioID, subject SubjectName, kind ScenarioKind, ops ...string) Scenario {
	steps := make([]Step, 0, len(ops))
	for i, op := range ops {
		steps = append(steps, Step{Name: fmt.Sprintf("s%d", i), Op: op})
	}
	return Scenario{
		ID: id, Version: 1, Kind: kind, Title: string(id), Owner: "test",
		SubjectName: subject, Steps: steps,
	}
}

// ---------------------------------------------------------------------------
// Test double
// ---------------------------------------------------------------------------

// FakeSubject is a scriptable [Subject].
//
// It is the ONLY subject implementation in this module, and it evaluates
// nothing real. The adapters for the five frozen phases live in
// packages/go/evalsubjects, because putting them here would make the platform
// depend on what it evaluates — the one thing the design refuses.
//
// This exists to prove the PLATFORM: the scenario engine, the golden framework,
// the comparison, the determinism engine, the regression engine.
type FakeSubject struct {
	name SubjectName

	// Handlers scripts an operation's response. An unscripted operation
	// produces a deterministic echo.
	Handlers map[string]func(Step, int) StepResult

	// Caps declares capabilities. Empty means every capability is claimed,
	// which keeps a simple test from having to enumerate them.
	Caps []Capability

	// NonDeterministic makes the fake return a different output every call,
	// for testing the determinism engine. Deliberately explicit: a fake that
	// was accidentally non-deterministic would make every test flaky.
	NonDeterministic bool

	// FailOnStep makes a named step fail.
	FailOnStep string

	// PanicOnStep makes a named step panic, for testing the platform's own
	// recovery.
	PanicOnStep string

	// Delay is how long each step takes on the injected clock.
	Delay time.Duration

	// Clock drives Delay.
	Clock rt.Clock

	opens    atomic.Int64
	executes atomic.Int64
	counter  atomic.Int64
}

// NewFakeSubject builds a scriptable subject.
func NewFakeSubject(name SubjectName) *FakeSubject {
	return &FakeSubject{name: name, Handlers: map[string]func(Step, int) StepResult{}}
}

// Name identifies the subject.
func (f *FakeSubject) Name() SubjectName { return f.name }

// Capabilities lists what it claims.
func (f *FakeSubject) Capabilities() []Capability {
	if len(f.Caps) > 0 {
		return f.Caps
	}
	caps := []Capability{"basic"}
	for _, k := range AllFailureKinds() {
		caps = append(caps, InjectionCapability(k))
	}
	return caps
}

// Open starts a session.
func (f *FakeSubject) Open(ctx context.Context, spec SessionSpec) (Session, error) {
	f.opens.Add(1)
	return &fakeSession{subject: f, spec: spec}, nil
}

// Opens returns how many sessions were opened.
func (f *FakeSubject) Opens() int64 { return f.opens.Load() }

// Executes returns how many steps were executed.
func (f *FakeSubject) Executes() int64 { return f.executes.Load() }

type fakeSession struct {
	subject *FakeSubject
	spec    SessionSpec
	mu      sync.Mutex
	state   Values
	events  []EventRecord
	step    int
}

func (s *fakeSession) Execute(ctx context.Context, step Step) StepResult {
	s.subject.executes.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.step++

	if step.Name == s.subject.PanicOnStep {
		panic("fake subject scripted panic at " + step.Name)
	}
	if s.subject.Delay > 0 && s.subject.Clock != nil {
		_ = s.subject.Clock.Sleep(ctx, s.subject.Delay)
	}
	if step.Name == s.subject.FailOnStep {
		return StepResult{Failed: true, Outcome: "scripted_failure",
			Detail: "the harness was told to fail this step"}
	}

	if handler, ok := s.subject.Handlers[step.Op]; ok {
		result := handler(step, s.step)
		s.applyState(step, result)
		return result
	}

	// The default is a deterministic echo: the same step always produces the
	// same output, which is what makes the fake usable as a baseline for
	// testing the platform's determinism machinery.
	out := Values{"op": S(step.Op), "step": N(float64(s.step))}
	if s.subject.NonDeterministic {
		out["nonce"] = N(float64(s.subject.counter.Add(1)))
	}
	for k, v := range step.Args {
		out["arg_"+k] = v
	}

	outcome := "ok"
	if step.Inject != nil {
		outcome = string(step.Inject.Kind)
		s.events = append(s.events, EventRecord{
			Type: "injected", Fields: Values{"kind": S(string(step.Inject.Kind))}})
	}

	result := StepResult{Output: out, Outcome: outcome}
	s.applyState(step, result)
	s.events = append(s.events, EventRecord{
		Type: "step_done", Fields: Values{"op": S(step.Op)}})
	return result
}

func (s *fakeSession) applyState(step Step, result StepResult) {
	if s.state == nil {
		s.state = Values{}
	}
	s.state["last_op"] = S(step.Op)
	s.state["steps"] = N(float64(s.step))
	if result.Outcome != "" {
		s.state["last_outcome"] = S(result.Outcome)
	}
}

func (s *fakeSession) State() Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Clone()
}

func (s *fakeSession) Events() []EventRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.events
	s.events = nil
	return out
}

func (s *fakeSession) Close() error { return nil }

// FailingSubject is a subject whose sessions cannot be opened, for testing the
// platform's own failure handling.
type FailingSubject struct {
	SubjectName SubjectName
	Err         error
}

// Name identifies the subject.
func (f FailingSubject) Name() SubjectName { return f.SubjectName }

// Capabilities claims everything, so a scenario reaches Open rather than being
// skipped — which is the path this double exists to exercise.
func (f FailingSubject) Capabilities() []Capability { return []Capability{"basic"} }

// Open always fails.
func (f FailingSubject) Open(context.Context, SessionSpec) (Session, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return nil, errors.New("failing subject: scripted open failure")
}
