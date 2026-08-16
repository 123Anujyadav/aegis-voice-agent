package evaluation

import (
	"fmt"
	"sort"
)

// FailureKind names a fault to inject.
//
// The seven the brief enumerates. They are a bounded vocabulary because the
// platform carries them and the ADAPTER interprets them — a free-form injection
// string would mean every adapter inventing its own names and no cross-subsystem
// failure heatmap being possible.
type FailureKind string

// The failure kinds.
const (
	// FailTimeout makes the operation exceed its deadline.
	FailTimeout FailureKind = "timeout"
	// FailDropEvent discards an event the subject would have emitted.
	FailDropEvent FailureKind = "drop_event"
	// FailMemory makes a memory operation fail.
	FailMemory FailureKind = "memory_failure"
	// FailTool makes a tool invocation fail.
	FailTool FailureKind = "tool_failure"
	// FailPermission makes a permission check refuse.
	FailPermission FailureKind = "permission_failure"
	// FailGovernance makes a governance decision unavailable.
	//
	// The most interesting one to inject, because the correct behaviour is
	// almost never "carry on": a subsystem that cannot reach governance must
	// refuse, and a failure scenario is the only place that gets exercised.
	FailGovernance FailureKind = "governance_failure"
	// FailCancellation cancels the operation mid-flight.
	FailCancellation FailureKind = "cancellation"
)

// AllFailureKinds returns every kind, in declaration order.
func AllFailureKinds() []FailureKind {
	return []FailureKind{FailTimeout, FailDropEvent, FailMemory, FailTool,
		FailPermission, FailGovernance, FailCancellation}
}

// Failure is one fault to inject at a step.
//
// DETERMINISTIC BY CONSTRUCTION. There is no probability field and there will
// not be one. A probabilistic injection makes a scenario non-reproducible, and a
// non-reproducible scenario cannot have a golden — which would remove the whole
// point of injecting the fault.
//
// A deployment wanting statistical fault behaviour writes many scenarios, each
// deterministic, and reads the distribution across them.
type Failure struct {
	// Kind is what to inject.
	Kind FailureKind

	// Detail carries adapter-specific configuration: which downstream, which
	// error code. A bounded string; it appears in behaviour fingerprints.
	Detail string

	// Repeat applies the failure to this many consecutive invocations within
	// the step. Zero means once.
	//
	// Present because "fails once then recovers" and "fails forever" exercise
	// completely different paths — the first tests retry, the second tests
	// giving up — and a platform that could only express one would leave the
	// other untested.
	Repeat int
}

func (f Failure) validate(step string) []string {
	var problems []string
	if f.Kind == "" {
		problems = append(problems, fmt.Sprintf("step %s: injection kind is required", step))
	} else if !knownFailureKind(f.Kind) {
		problems = append(problems, fmt.Sprintf(
			"step %s: unknown injection kind %q; the vocabulary is bounded so a "+
				"failure heatmap can have a stable axis", step, f.Kind))
	}
	if f.Repeat < 0 {
		problems = append(problems, fmt.Sprintf("step %s: Repeat must not be negative", step))
	}
	return problems
}

func knownFailureKind(k FailureKind) bool {
	for _, known := range AllFailureKinds() {
		if known == k {
			return true
		}
	}
	return false
}

// String renders the failure.
func (f Failure) String() string {
	s := string(f.Kind)
	if f.Detail != "" {
		s += "(" + f.Detail + ")"
	}
	if f.Repeat > 1 {
		s += fmt.Sprintf("×%d", f.Repeat)
	}
	return s
}

// InjectionSupport declares which failures an adapter can actually apply.
//
// AN ADAPTER THAT CANNOT INJECT A FAULT MUST SAY SO. The alternative — silently
// ignoring an injection — produces a scenario that appears to test failure
// handling, passes, and has tested nothing. That is worse than no failure
// scenario at all, because it carries the belief that the path is covered.
//
// A scenario injecting an unsupported failure is SKIPPED, with the unsupported
// kind named.
type InjectionSupport struct {
	kinds map[FailureKind]bool
}

// NewInjectionSupport declares the supported kinds.
func NewInjectionSupport(kinds ...FailureKind) InjectionSupport {
	s := InjectionSupport{kinds: make(map[FailureKind]bool, len(kinds))}
	for _, k := range kinds {
		s.kinds[k] = true
	}
	return s
}

// Supports reports whether a kind can be injected.
func (s InjectionSupport) Supports(k FailureKind) bool { return s.kinds[k] }

// Kinds returns the supported kinds, sorted.
func (s InjectionSupport) Kinds() []FailureKind {
	out := make([]FailureKind, 0, len(s.kinds))
	for k := range s.kinds {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// InjectionCapability is the capability an adapter declares per failure kind,
// so a scenario's requirements can be checked the same way as any other.
func InjectionCapability(k FailureKind) Capability { return Capability("inject:" + string(k)) }

// RequiredInjections returns the injection capabilities a scenario needs,
// sorted and deduplicated.
func RequiredInjections(s Scenario) []Capability {
	seen := make(map[Capability]bool)
	for _, step := range s.Steps {
		if step.Inject == nil {
			continue
		}
		seen[InjectionCapability(step.Inject.Kind)] = true
	}
	out := make([]Capability, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
