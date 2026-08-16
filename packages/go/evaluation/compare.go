package evaluation

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Verdict is the outcome of comparing an observation against a baseline.
//
// FIVE VALUES, AND THE IMPORTANT DISTINCTION IS DRIFT VERSUS FAIL.
//
// A test suite has two: pass and fail. That is why "the tests are red" is a
// sentence carrying no information — it could mean the code broke, or it could
// mean somebody deliberately improved an output and has not updated the
// expectation.
//
// Here they are different words with different meanings, and a report that
// separates them lets a release decision be made by reading it.
type Verdict uint8

// The verdicts.
const (
	// VerdictPass means the observation matched the baseline within tolerance.
	VerdictPass Verdict = iota

	// VerdictDrift means the BEHAVIOUR changed against an approved baseline.
	// Not necessarily bad: a deliberate improvement drifts. It requires a
	// human decision, which is exactly what a golden approval is.
	VerdictDrift

	// VerdictFail means the scenario itself broke — a step could not be
	// performed, or the run errored. Never intended.
	VerdictFail

	// VerdictNoBaseline means no approved golden exists. The state every new
	// scenario starts in, reported as its own value rather than as a red
	// result nobody can act on.
	VerdictNoBaseline

	// VerdictSkipped means the subject lacks a required capability.
	VerdictSkipped
)

// String renders the verdict. Used as a metric label and a heatmap cell.
func (v Verdict) String() string {
	switch v {
	case VerdictDrift:
		return "drift"
	case VerdictFail:
		return "fail"
	case VerdictNoBaseline:
		return "no_baseline"
	case VerdictSkipped:
		return "skipped"
	default:
		return "pass"
	}
}

// Blocking reports whether the verdict should stop a release on its own.
//
// Fail blocks. Drift does NOT — it requires a decision, and a platform that
// blocked on every drift would train people to approve baselines without
// reading them, which is the same as auto-approving.
//
// NoBaseline does not block either: a new scenario with no approved golden is a
// scenario nobody has reviewed yet, and blocking would make adding one a release
// event.
func (v Verdict) Blocking() bool { return v == VerdictFail }

// Compared reports whether the verdict came from an actual comparison against a
// baseline.
//
// [VerdictNoBaseline] and [VerdictSkipped] did not compare anything. The
// distinction matters to the regression engine: a scenario that was compared
// yesterday and is not compared today has lost coverage, and that is invisible
// to any check that looks only at behaviour fingerprints — an unevaluated
// scenario's fingerprint is perfectly stable.
func (v Verdict) Compared() bool {
	return v == VerdictPass || v == VerdictDrift || v == VerdictFail
}

// Tolerances bound acceptable variation.
//
// Behaviour has NO tolerance — outputs either match or they do not, and a
// "mostly the same answer" threshold is a threshold somebody raises until the
// drift stops being reported. Tolerances apply only to timings and numeric
// metrics, where variation is physics rather than behaviour.
type Tolerances struct {
	// LatencyRatio permits a step to take up to this multiple of the
	// baseline's duration. Zero disables the latency check entirely.
	//
	// A RATIO, NOT AN ABSOLUTE. A 2 ms step doubling is interesting; a 2 ms
	// step gaining 1 ms of absolute jitter is not, and an absolute threshold
	// cannot tell the two apart across steps that differ by three orders of
	// magnitude.
	LatencyRatio float64

	// LatencyFloor ignores steps faster than this. Without it, a 3 µs step
	// that becomes 7 µs trips a 2× ratio on scheduler noise alone.
	LatencyFloor time.Duration

	// MetricDeltas bound a named metric's relative change. A metric with no
	// entry is reported but never fails.
	MetricDeltas map[string]float64
}

// DefaultTolerances returns the platform baseline.
//
// A 2× latency ratio with a 50 µs floor. Deliberately loose: this platform runs
// on developer machines and CI runners with noisy neighbours, and a tight
// latency tolerance produces a flaky evaluation platform — which is worse than
// no latency check, because people learn to ignore it.
//
// Benchmarks are where latency is measured properly, over distributions rather
// than single observations. See [BenchmarkResult].
func DefaultTolerances() Tolerances {
	return Tolerances{LatencyRatio: 2.0, LatencyFloor: 50 * time.Microsecond}
}

func (t Tolerances) validate(where string) []string {
	var problems []string
	if t.LatencyRatio < 0 {
		problems = append(problems, where+": LatencyRatio must not be negative")
	}
	if t.LatencyRatio > 0 && t.LatencyRatio < 1 {
		problems = append(problems, fmt.Sprintf(
			"%s: LatencyRatio %g is below 1, which fails every run that is not "+
				"faster than the baseline", where, t.LatencyRatio))
	}
	if t.LatencyFloor < 0 {
		problems = append(problems, where+": LatencyFloor must not be negative")
	}
	for name, d := range t.MetricDeltas {
		if d < 0 {
			problems = append(problems, fmt.Sprintf(
				"%s: metric tolerance %s must not be negative", where, name))
		}
	}
	return problems
}

// DiffKind classifies one difference.
type DiffKind string

// The difference kinds.
const (
	DiffStepCount   DiffKind = "step_count"
	DiffStepMissing DiffKind = "step_missing"
	DiffStepAdded   DiffKind = "step_added"
	DiffOutcome     DiffKind = "outcome"
	DiffOutput      DiffKind = "output"
	DiffState       DiffKind = "state"
	DiffEvents      DiffKind = "events"
	DiffFailure     DiffKind = "failure"
	DiffLatency     DiffKind = "latency"
	DiffMetric      DiffKind = "metric"
	DiffScenario    DiffKind = "scenario_changed"
)

// Behavioural reports whether the difference is a behaviour change rather than
// a timing or measurement one.
//
// The classification that decides whether a difference is drift. A slower step
// that produced identical output is a performance observation, not a behaviour
// change, and conflating them makes a drift report unreadable on a busy CI box.
func (k DiffKind) Behavioural() bool {
	switch k {
	case DiffLatency, DiffMetric:
		return false
	default:
		return true
	}
}

// Difference is one thing that changed.
type Difference struct {
	// Kind classifies it.
	Kind DiffKind
	// Step names where, empty for whole-run differences.
	Step string
	// Field names what, for output, state and metric differences.
	Field string
	// Was and Now render the two values. Rendered rather than typed, because a
	// difference is read by a person and the types are already gone by the
	// time it is stored.
	Was string
	Now string
	// Ratio is populated for latency and metric differences.
	Ratio float64
}

// String renders the difference.
func (d Difference) String() string {
	var b strings.Builder
	b.WriteString(string(d.Kind))
	if d.Step != "" {
		b.WriteString(" at " + d.Step)
	}
	if d.Field != "" {
		b.WriteString("." + d.Field)
	}
	fmt.Fprintf(&b, ": %s → %s", d.Was, d.Now)
	if d.Ratio != 0 {
		fmt.Fprintf(&b, " (×%.2f)", d.Ratio)
	}
	return b.String()
}

// Comparison is the result of comparing an observation against a baseline.
type Comparison struct {
	// Verdict is the outcome.
	Verdict Verdict
	// Scenario and Subject identify what was compared.
	Scenario ScenarioID
	Subject  SubjectName
	// Golden names the baseline, empty when there was none.
	Golden GoldenID
	// BaselineBehaviour and ObservedBehaviour are the two fingerprints.
	BaselineBehaviour Fingerprint
	ObservedBehaviour Fingerprint
	// Differences are everything that changed.
	Differences []Difference
	// Reason is a short machine-readable code for the verdict.
	Reason string
	// Skipped names the missing capabilities, when skipped.
	Skipped []Capability
}

// BehaviouralDifferences returns only the differences that are behaviour
// changes.
func (c Comparison) BehaviouralDifferences() []Difference {
	var out []Difference
	for _, d := range c.Differences {
		if d.Kind.Behavioural() {
			out = append(out, d)
		}
	}
	return out
}

// Summary renders the comparison for a report.
func (c Comparison) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s on %s: %s (%s)", c.Scenario, c.Subject, c.Verdict, c.Reason)
	if c.BaselineBehaviour != "" {
		fmt.Fprintf(&b, " %s → %s", c.BaselineBehaviour, c.ObservedBehaviour)
	}
	b.WriteByte('\n')
	for _, d := range c.Differences {
		fmt.Fprintf(&b, "    %s\n", d)
	}
	return b.String()
}

// Compare measures an observation against a baseline.
//
// A PURE FUNCTION. No I/O, no clock, no state. Given the same golden,
// observation and tolerances it returns a byte-identical comparison forever,
// which is what lets a drift report be recomputed from stored artefacts years
// later and compared against the one that was read at the time.
func Compare(g Golden, obs Observation, tol Tolerances) Comparison {
	c := Comparison{
		Scenario: obs.Scenario, Subject: obs.Subject,
		Golden: g.ID, BaselineBehaviour: g.Behaviour,
		ObservedBehaviour: obs.BehaviourPrint(),
	}

	// A failed run is a FAILURE regardless of what the baseline says. A
	// baseline that recorded a failure and a run that reproduces it is still a
	// failure — "it has always been broken" is not a passing result.
	if obs.Failed() {
		c.Verdict = VerdictFail
		c.Reason = "scenario_failed"
		c.Differences = append(c.Differences, failureDifferences(g.Observation, obs)...)
		return c
	}

	// A golden from a different scenario version is not a baseline for this
	// question. Refused outright rather than compared, because the resulting
	// drift would be real and unexplainable.
	if g.Observation.ScenarioVersion != obs.ScenarioVersion {
		c.Verdict = VerdictFail
		c.Reason = "scenario_version_mismatch"
		c.Differences = append(c.Differences, Difference{
			Kind: DiffScenario,
			Was:  fmt.Sprintf("v%d", g.Observation.ScenarioVersion),
			Now:  fmt.Sprintf("v%d", obs.ScenarioVersion),
		})
		return c
	}

	c.Differences = append(c.Differences, behaviourDifferences(g.Observation, obs)...)
	c.Differences = append(c.Differences, latencyDifferences(g.Observation, obs, tol)...)
	c.Differences = append(c.Differences, metricDifferences(g.Observation, obs, tol)...)

	sortDifferences(c.Differences)

	switch {
	case c.BaselineBehaviour != c.ObservedBehaviour:
		c.Verdict = VerdictDrift
		c.Reason = "behaviour_changed"
	case len(c.Differences) > 0:
		// Timings or metrics moved beyond tolerance while behaviour held.
		// Still drift — a subsystem that answers identically and takes four
		// times as long has changed in a way somebody must decide about — but
		// the reason distinguishes it, so a report can separate "it does
		// something different" from "it does the same thing slower".
		c.Verdict = VerdictDrift
		c.Reason = "measurement_changed"
	default:
		c.Verdict = VerdictPass
		c.Reason = "matched"
	}
	return c
}

func failureDifferences(base, obs Observation) []Difference {
	var out []Difference
	if obs.Error != "" {
		out = append(out, Difference{Kind: DiffFailure, Was: base.Error, Now: obs.Error})
	}
	for _, s := range obs.Steps {
		if !s.Failed {
			continue
		}
		was := "ok"
		if b, found := base.Step(s.Name); found && b.Failed {
			was = "failed"
		}
		out = append(out, Difference{
			Kind: DiffFailure, Step: s.Name, Was: was, Now: "failed: " + s.Detail})
	}
	return out
}

func behaviourDifferences(base, obs Observation) []Difference {
	var out []Difference

	if len(base.Steps) != len(obs.Steps) {
		out = append(out, Difference{
			Kind: DiffStepCount,
			Was:  fmt.Sprint(len(base.Steps)), Now: fmt.Sprint(len(obs.Steps))})
	}

	baseByName := make(map[string]StepObservation, len(base.Steps))
	for _, s := range base.Steps {
		baseByName[s.Name] = s
	}
	obsByName := make(map[string]StepObservation, len(obs.Steps))
	for _, s := range obs.Steps {
		obsByName[s.Name] = s
	}

	for _, s := range base.Steps {
		if _, ok := obsByName[s.Name]; !ok {
			out = append(out, Difference{Kind: DiffStepMissing, Step: s.Name,
				Was: s.Outcome, Now: "<absent>"})
		}
	}

	for _, s := range obs.Steps {
		b, ok := baseByName[s.Name]
		if !ok {
			out = append(out, Difference{Kind: DiffStepAdded, Step: s.Name,
				Was: "<absent>", Now: s.Outcome})
			continue
		}

		if b.Outcome != s.Outcome {
			out = append(out, Difference{Kind: DiffOutcome, Step: s.Name,
				Was: b.Outcome, Now: s.Outcome})
		}
		for _, field := range b.Output.Diff(s.Output) {
			out = append(out, Difference{Kind: DiffOutput, Step: s.Name, Field: field,
				Was: b.Output.Get(field).Display(), Now: s.Output.Get(field).Display()})
		}
		for _, field := range b.State.Diff(s.State) {
			out = append(out, Difference{Kind: DiffState, Step: s.Name, Field: field,
				Was: b.State.Get(field).Display(), Now: s.State.Get(field).Display()})
		}
		if was, now := eventSignature(b.Events), eventSignature(s.Events); was != now {
			out = append(out, Difference{Kind: DiffEvents, Step: s.Name, Was: was, Now: now})
		}
	}
	return out
}

func eventSignature(events []EventRecord) string {
	if len(events) == 0 {
		return "<none>"
	}
	parts := make([]string, 0, len(events))
	for _, e := range events {
		parts = append(parts, e.Type)
	}
	return strings.Join(parts, ",")
}

func latencyDifferences(base, obs Observation, tol Tolerances) []Difference {
	if tol.LatencyRatio <= 0 {
		return nil
	}

	var out []Difference
	baseTimes := base.Timings()
	for _, s := range obs.Steps {
		was, ok := baseTimes[s.Name]
		if !ok || was <= 0 {
			continue
		}
		// A RATIO NEEDS A MEASURABLE DENOMINATOR. The floor is applied to the
		// BASELINE alone, not to both sides.
		//
		// The first version skipped only when both sides were below the floor,
		// which meant a contended run whose step crossed the floor was compared
		// against a baseline the clock could not resolve — dividing 6 ms by a
		// 525 µs sample whose true value lay anywhere between zero and one
		// millisecond. That produced drift on 2 of 192 evaluations under
		// sixteen-way concurrency: the same code, the same engines, a different
		// verdict because the machine was busy. An evaluation platform whose
		// headline verdict moves with machine load cannot be used to gate a
		// release, which is the entire point of it. See ENGINEERING_AUDIT F5.
		//
		// The cost is that a step which was genuinely quick and is now slow goes
		// unreported here. That is the intended division of labour: single
		// observations do not measure latency, distributions do, and the
		// benchmark engine is where latency regressions are meant to be caught.
		// See [DefaultTolerances].
		if was < tol.LatencyFloor {
			continue
		}
		ratio := float64(s.Duration) / float64(was)
		if ratio > tol.LatencyRatio {
			out = append(out, Difference{Kind: DiffLatency, Step: s.Name,
				Was: was.String(), Now: s.Duration.String(), Ratio: ratio})
		}
	}
	return out
}

func metricDifferences(base, obs Observation, tol Tolerances) []Difference {
	var out []Difference
	for name, allowed := range tol.MetricDeltas {
		was, hadBase := base.Metric(name)
		now, hasNow := obs.Metric(name)
		if !hadBase || !hasNow {
			continue
		}
		if was == 0 {
			if now != 0 {
				out = append(out, Difference{Kind: DiffMetric, Field: name,
					Was: "0", Now: fmt.Sprintf("%g", now)})
			}
			continue
		}
		delta := (now - was) / was
		if delta < 0 {
			delta = -delta
		}
		if delta > allowed {
			out = append(out, Difference{Kind: DiffMetric, Field: name,
				Was: fmt.Sprintf("%g", was), Now: fmt.Sprintf("%g", now),
				Ratio: now / was})
		}
	}
	return out
}

// sortDifferences orders differences deterministically.
//
// Two comparisons of the same pair must produce byte-identical difference
// lists, or a drift report cannot be diffed against the one from yesterday.
func sortDifferences(d []Difference) {
	sort.SliceStable(d, func(i, j int) bool {
		if d[i].Step != d[j].Step {
			return d[i].Step < d[j].Step
		}
		if d[i].Kind != d[j].Kind {
			return d[i].Kind < d[j].Kind
		}
		return d[i].Field < d[j].Field
	})
}
