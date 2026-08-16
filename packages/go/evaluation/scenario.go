package evaluation

import (
	"fmt"
	"sort"
	"time"
)

// ScenarioKind classifies what a scenario exercises.
//
// The eight kinds the brief enumerates. They are a LABEL on one `Scenario` type,
// not eight types: eight types would mean eight execution paths, and an
// evaluation platform whose execution differs per scenario kind cannot claim
// that two kinds were evaluated the same way.
type ScenarioKind uint8

// The scenario kinds.
const (
	KindConversation ScenarioKind = iota
	KindMemory
	KindTool
	KindGovernance
	KindRuntime
	KindEmergency
	KindFailure
	KindRecovery
)

// String renders the kind. Used as a metric label and a heatmap axis.
func (k ScenarioKind) String() string {
	switch k {
	case KindMemory:
		return "memory"
	case KindTool:
		return "tool"
	case KindGovernance:
		return "governance"
	case KindRuntime:
		return "runtime"
	case KindEmergency:
		return "emergency"
	case KindFailure:
		return "failure"
	case KindRecovery:
		return "recovery"
	default:
		return "conversation"
	}
}

// AllScenarioKinds returns every kind, in declaration order.
func AllScenarioKinds() []ScenarioKind {
	return []ScenarioKind{KindConversation, KindMemory, KindTool, KindGovernance,
		KindRuntime, KindEmergency, KindFailure, KindRecovery}
}

// Scenario is a program of steps run against a subject.
//
// DATA, NOT CODE. A scenario has no closures, no callbacks and no assertions.
// It does nothing when built except describe itself.
//
// That is what lets a scenario be reviewed before it runs, compared between two
// releases, and — critically — replayed years later against a subsystem that has
// been rewritten twice, because the scenario never referenced the subsystem's
// types.
type Scenario struct {
	// ID identifies the scenario across versions.
	ID ScenarioID

	// Version increments on every change.
	//
	// A GOLDEN IS BOUND TO A SCENARIO VERSION. Changing a scenario without
	// bumping the version means comparing today's run against a baseline
	// recorded from a different question, which produces drift nobody can
	// explain. [Compare] refuses the comparison outright.
	Version int

	// Kind classifies it.
	Kind ScenarioKind

	// Title and Description document it.
	Title       string
	Description string

	// Owner names the team accountable. Required, for the same reason a tool
	// and a policy need one: a scenario nobody owns is a scenario nobody fixes
	// when it starts drifting.
	Owner string

	// SubjectName is which subsystem it evaluates.
	SubjectName SubjectName

	// Requires lists the capabilities the subject must have. A subject lacking
	// one produces a SKIP with the missing capability named, not a failure.
	Requires []Capability

	// Params configure the session.
	Params Values

	// Seed makes the run reproducible.
	Seed int64

	// Steps are executed in order.
	Steps []Step

	// Tolerances bound what counts as acceptable variation when comparing
	// against a golden.
	Tolerances Tolerances

	// Timeout bounds the whole scenario. Zero takes the runtime default.
	Timeout time.Duration

	// Tags carry operator metadata. Never used for selection logic — a tag
	// that affected behaviour would be an untyped configuration channel.
	Tags map[string]string
}

func (s Scenario) validate() []string {
	var problems []string
	where := string(s.ID)
	if where == "" {
		where = "<unnamed scenario>"
		problems = append(problems, "scenario: id is required")
	}
	if s.Version < 1 {
		problems = append(problems, where+": version must be at least 1; a golden is "+
			"bound to a scenario version, and an unversioned change compares today's "+
			"run against a different question")
	}
	if s.Owner == "" {
		problems = append(problems, where+": owner is required; a scenario nobody owns "+
			"is one nobody fixes when it starts drifting")
	}
	if s.SubjectName == "" {
		problems = append(problems, where+": subject is required")
	}
	if s.Kind > KindRecovery {
		problems = append(problems, where+": unknown kind")
	}
	if len(s.Steps) == 0 {
		problems = append(problems, where+": at least one step is required; a scenario "+
			"with no steps records an empty observation and passes forever")
	}
	if s.Timeout < 0 {
		problems = append(problems, where+": timeout must not be negative")
	}

	seen := make(map[string]bool, len(s.Steps))
	for i, step := range s.Steps {
		problems = append(problems, prefixAll(where, step.validate(i))...)
		if step.Name != "" {
			if seen[step.Name] {
				problems = append(problems, fmt.Sprintf(
					"%s: duplicate step name %q; a diff cannot name which one changed",
					where, step.Name))
			}
			seen[step.Name] = true
		}
	}
	problems = append(problems, s.Tolerances.validate(where)...)
	return problems
}

func prefixAll(where string, problems []string) []string {
	if len(problems) == 0 {
		return nil
	}
	out := make([]string, 0, len(problems))
	for _, p := range problems {
		out = append(out, where+" "+p)
	}
	return out
}

// Validate reports whether the scenario is well-formed.
func (s Scenario) Validate() error {
	if problems := s.validate(); len(problems) > 0 {
		sort.Strings(problems)
		return &ConfigError{Problems: problems}
	}
	return nil
}

// Key returns the identity a golden is filed under: scenario, version, subject.
//
// All three, because the same scenario at a different version is a different
// question, and the same scenario against a different subject is a different
// answer.
func (s Scenario) Key() GoldenKey {
	return GoldenKey{Scenario: s.ID, Version: s.Version, Subject: s.SubjectName}
}

// Digest fingerprints the scenario's executable content.
//
// Covers the steps, their operations, arguments, injections and advances.
// Excludes the title, description, owner and tags: two scenarios that run
// identically have the same digest even if one is better documented, which is
// what makes the digest useful for answering "did the question actually change
// in this commit".
func (s Scenario) Digest() Fingerprint {
	var b []byte
	b = append(b, s.ID...)
	b = append(b, '|')
	b = append(b, byte('0'+s.Kind))
	b = append(b, '|')
	b = append(b, s.SubjectName...)
	for _, step := range s.Steps {
		b = append(b, '\n')
		b = append(b, step.Name...)
		b = append(b, '|')
		b = append(b, step.Op...)
		b = append(b, '|')
		b = append(b, step.Args.Fingerprint()...)
		b = append(b, '|')
		if step.Inject != nil {
			b = append(b, step.Inject.Kind...)
		}
		b = append(b, '|')
		b = append(b, []byte(step.Advance.String())...)
	}
	return fingerprintOf(b)
}

// ScenarioSet is an ordered scenario collection.
type ScenarioSet []Scenario

// ByKind returns the scenarios of one kind.
func (s ScenarioSet) ByKind(k ScenarioKind) ScenarioSet {
	var out ScenarioSet
	for _, sc := range s {
		if sc.Kind == k {
			out = append(out, sc)
		}
	}
	return out
}

// BySubject returns the scenarios for one subject.
func (s ScenarioSet) BySubject(name SubjectName) ScenarioSet {
	var out ScenarioSet
	for _, sc := range s {
		if sc.SubjectName == name {
			out = append(out, sc)
		}
	}
	return out
}

// Sorted returns the set ordered by subject then identifier.
//
// Deterministic ordering, so a run over a set produces results in a stable
// order regardless of how the set was assembled — which is what lets two run
// reports be diffed line by line.
func (s ScenarioSet) Sorted() ScenarioSet {
	out := append(ScenarioSet(nil), s...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].SubjectName != out[j].SubjectName {
			return out[i].SubjectName < out[j].SubjectName
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Coverage reports how many scenarios exercise each kind, for every kind
// including the ones with none.
//
// A kind with zero scenarios is the useful output. A coverage report that only
// lists what exists cannot tell an operator what is missing, and "we have no
// recovery scenarios" is precisely the thing worth knowing before a release.
func (s ScenarioSet) Coverage() map[ScenarioKind]int {
	out := make(map[ScenarioKind]int, len(AllScenarioKinds()))
	for _, k := range AllScenarioKinds() {
		out[k] = 0
	}
	for _, sc := range s {
		out[sc.Kind]++
	}
	return out
}
