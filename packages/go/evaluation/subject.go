package evaluation

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Capability names something a subject can be asked to do.
//
// A string, not an enum. The platform must be able to evaluate a subsystem it
// has never heard of, and an enum here would mean every new subsystem needs a
// change to the evaluation platform before it can be evaluated — which is the
// coupling the whole design exists to avoid.
type Capability string

// Subject is a subsystem under evaluation.
//
// THE PLATFORM NEVER LEARNS WHAT IT IS EVALUATING. A subject has a name, a set
// of capabilities and a way to open a session. There is no type switch anywhere
// in this module, no per-subsystem branch, and no import of anything the
// platform evaluates.
//
// That is what makes the platform provider agnostic in the sense that matters:
// a subsystem rewritten in Python behind a gRPC adapter is evaluated by the same
// scenarios against the same goldens, with nothing changing here.
type Subject interface {
	// Name identifies the subsystem: "memory", "governance", "toolruntime".
	Name() SubjectName

	// Capabilities lists what this subject can be asked to do. A scenario
	// requiring a capability the subject lacks is SKIPPED rather than failed —
	// see [VerdictSkipped].
	Capabilities() []Capability

	// Open starts an isolated session. Every scenario gets its own, so one
	// scenario's state cannot leak into another's observation.
	Open(ctx context.Context, spec SessionSpec) (Session, error)
}

// SessionSpec configures a session.
type SessionSpec struct {
	// Scenario names what is about to run, for the adapter's own logging.
	Scenario ScenarioID
	// Seed makes any randomness inside the subject reproducible. An adapter
	// that ignores it is declaring the subject non-deterministic, which the
	// determinism engine will report.
	Seed int64
	// Params carry scenario-level configuration to the adapter.
	Params Values
}

// Session is one isolated interaction with a subject.
//
// Sessions are single-goroutine by contract. The platform runs scenarios
// concurrently but never shares a session, so an adapter needs no locking of
// its own — which removes the most likely place for an adapter author to
// introduce a race into a platform whose whole purpose is measuring behaviour.
type Session interface {
	// Execute performs one step and reports what happened.
	//
	// It returns a result rather than an error: a step that fails is an
	// OBSERVATION, not an exception. An adapter that returned an error for a
	// refused permission would make "the subject correctly refused" and "the
	// adapter broke" the same outcome.
	Execute(ctx context.Context, step Step) StepResult

	// State returns the subject's observable state after the last step.
	// Keys and values are the adapter's vocabulary; the platform compares them
	// and never interprets them.
	State() Values

	// Events returns everything the subject emitted since the last call,
	// in order.
	Events() []EventRecord

	// Close releases the session.
	Close() error
}

// Step is one instruction in a scenario.
type Step struct {
	// Name identifies the step within its scenario, for reports and diffs.
	// Required: a drift report naming "step 3" cannot be acted on.
	Name string

	// Op is what to do, in the adapter's vocabulary: "store", "decide",
	// "invoke", "say".
	Op string

	// Args are the operation's arguments.
	Args Values

	// Inject applies a failure to this step. The adapter interprets it; the
	// platform only carries it, which is what keeps failure injection
	// subject-agnostic.
	Inject *Failure

	// Advance moves the subject's clock before the step, when the adapter
	// exposes a controllable one. Zero means no advance.
	//
	// Carried as data rather than performed by the platform because only the
	// adapter knows which clock to move — and a platform that reached for a
	// clock would be a platform that knows what it is evaluating.
	Advance time.Duration
}

func (s Step) validate(index int) []string {
	var problems []string
	if s.Name == "" {
		problems = append(problems, fmt.Sprintf(
			"step %d: name is required; a drift report naming an anonymous step "+
				"cannot be acted on", index))
	}
	if s.Op == "" {
		problems = append(problems, fmt.Sprintf("step %s: op is required", s.Name))
	}
	if s.Inject != nil {
		problems = append(problems, s.Inject.validate(s.Name)...)
	}
	if s.Advance < 0 {
		problems = append(problems, fmt.Sprintf("step %s: Advance must not be negative", s.Name))
	}
	return problems
}

// StepResult is what an adapter reports about one step.
type StepResult struct {
	// Output is what the subject produced.
	Output Values

	// Outcome is a short machine-readable code for what happened: "ok",
	// "denied", "timeout", "not_found".
	//
	// NEVER free text. It appears in behaviour fingerprints, drift reports and
	// failure heatmaps, and free text in a heatmap axis is an unbounded
	// cardinality incident wearing a diagnostic's clothes.
	Outcome string

	// Failed reports that the STEP could not be performed — the adapter broke,
	// not the subject.
	//
	// The distinction is the platform's most important: a subject that
	// correctly refuses an action is working, and reporting that as a failure
	// would make every correctly-governed scenario look broken.
	Failed bool

	// Detail explains a failure for a human. Excluded from behaviour
	// fingerprints, so an improved error message is not drift.
	Detail string

	// Duration is how long the step took.
	Duration time.Duration
}

// OK reports that the step performed and the subject answered normally.
func (r StepResult) OK() bool { return !r.Failed && (r.Outcome == "" || r.Outcome == "ok") }

// EventRecord is one event a subject emitted.
//
// IT CARRIES A TYPE AND BOUNDED FIELDS, NEVER CONTENT. Observations are stored,
// replayed and retained across releases, and a platform that recorded what
// somebody said on a phone call would be a permanent copy of it with a different
// retention schedule from the system of record.
type EventRecord struct {
	// Type classifies the event in the subject's vocabulary.
	Type string
	// Fields carry bounded structured detail — identifiers, codes, counts.
	Fields Values
	// Sequence orders events within a session.
	Sequence int
}

// SubjectSet holds subjects by name.
//
// Not a global registry: an [EvaluationRuntime] owns one, two runtimes in one
// process share nothing, and the test suite is parallel-safe as a result.
type SubjectSet struct {
	byName map[SubjectName]Subject
}

// NewSubjectSet builds a set from subjects.
func NewSubjectSet(subjects ...Subject) *SubjectSet {
	s := &SubjectSet{byName: make(map[SubjectName]Subject, len(subjects))}
	for _, sub := range subjects {
		if sub != nil {
			s.byName[sub.Name()] = sub
		}
	}
	return s
}

// Add registers a subject.
func (s *SubjectSet) Add(sub Subject) *SubjectSet {
	if sub != nil {
		s.byName[sub.Name()] = sub
	}
	return s
}

// Get returns a subject.
func (s *SubjectSet) Get(name SubjectName) (Subject, bool) {
	sub, ok := s.byName[name]
	return sub, ok
}

// Names returns every registered subject name, sorted.
func (s *SubjectSet) Names() []SubjectName {
	out := make([]SubjectName, 0, len(s.byName))
	for n := range s.byName {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Len returns the subject count.
func (s *SubjectSet) Len() int { return len(s.byName) }

// Supports reports whether a subject has every capability.
func Supports(sub Subject, required ...Capability) bool {
	have := make(map[Capability]bool, len(sub.Capabilities()))
	for _, c := range sub.Capabilities() {
		have[c] = true
	}
	for _, c := range required {
		if !have[c] {
			return false
		}
	}
	return true
}

// MissingCapabilities returns the capabilities a subject lacks, sorted.
//
// Used to explain a skip. "Scenario skipped" is not an answer; "scenario skipped
// because the subject does not support streaming" is.
func MissingCapabilities(sub Subject, required []Capability) []Capability {
	have := make(map[Capability]bool, len(sub.Capabilities()))
	for _, c := range sub.Capabilities() {
		have[c] = true
	}
	var out []Capability
	for _, c := range required {
		if !have[c] {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
