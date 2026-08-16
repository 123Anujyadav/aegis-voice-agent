package evaluation

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// StepObservation is what was observed for one step.
type StepObservation struct {
	// Name and Op identify the step.
	Name string
	Op   string
	// Output is what the subject produced.
	Output Values
	// Outcome is the subject's bounded outcome code.
	Outcome string
	// Failed reports that the step could not be performed at all.
	Failed bool
	// Detail explains a failure. EXCLUDED from the behaviour fingerprint, so
	// improving an error message is not drift.
	Detail string
	// State is the subject's observable state after the step.
	State Values
	// Events are what the subject emitted during the step.
	Events []EventRecord
	// Duration is how long it took. EXCLUDED from the behaviour fingerprint.
	Duration time.Duration
	// Injected names the failure applied to this step, empty when none.
	Injected string
}

// Observation is everything recorded from one scenario run.
//
// This is the platform's central artefact. A golden is an approved one, a
// verdict is a comparison of two, a replay reproduces one, and a trend history
// is a sequence of their fingerprints.
type Observation struct {
	// Scenario and Subject identify what ran.
	Scenario ScenarioID
	Subject  SubjectName
	// ScenarioVersion is the scenario revision that produced it. An
	// observation compared against a golden recorded from a different scenario
	// version is comparing two different questions — see [Compare].
	ScenarioVersion int
	// Seed is the seed the session was opened with.
	Seed int64
	// Steps are the step observations, in execution order.
	Steps []StepObservation
	// Metrics are scalar measurements the adapter reported.
	Metrics map[string]float64
	// StartedAt and FinishedAt bound the run.
	StartedAt  time.Time
	FinishedAt time.Time
	// Error is set when the scenario could not complete at all.
	Error string
}

// Duration returns the wall time of the run.
func (o Observation) Duration() time.Duration { return o.FinishedAt.Sub(o.StartedAt) }

// StepCount returns how many steps were observed.
func (o Observation) StepCount() int { return len(o.Steps) }

// Failed reports that any step failed or the scenario errored.
func (o Observation) Failed() bool {
	if o.Error != "" {
		return true
	}
	for _, s := range o.Steps {
		if s.Failed {
			return true
		}
	}
	return false
}

// Outcomes returns each step's outcome code, in order. The compact form used in
// reports and heatmaps.
func (o Observation) Outcomes() []string {
	out := make([]string, 0, len(o.Steps))
	for _, s := range o.Steps {
		out = append(out, s.Outcome)
	}
	return out
}

// EventTypes returns every event type emitted, in order.
func (o Observation) EventTypes() []string {
	var out []string
	for _, s := range o.Steps {
		for _, e := range s.Events {
			out = append(out, e.Type)
		}
	}
	return out
}

// behaviourCanonical encodes everything that is BEHAVIOUR.
//
// THE SEPARATION OF BEHAVIOUR FROM TIME IS THE PLATFORM'S MOST IMPORTANT
// ENCODING DECISION.
//
// Included: step names and operations, outputs, outcome codes, failure flags,
// state, event types and fields, and the scenario version.
//
// Excluded, deliberately: durations, timestamps, seeds, and free-text detail.
//
// Two runs of a deterministic system produce identical outputs and DIFFERENT
// durations — a system that produced identical durations would be a system with
// no clock. Folding time into one "did the run match" fingerprint makes every
// determinism check flaky and every latency check blind, because a slowdown that
// changes no output would be invisible and a timing jitter that changes no
// behaviour would look like a regression.
func (o Observation) behaviourCanonical() []byte {
	var b strings.Builder

	b.WriteString(string(o.Scenario))
	b.WriteByte('|')
	b.WriteString(string(o.Subject))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(o.ScenarioVersion))
	b.WriteByte('|')
	b.WriteString(o.Error)
	b.WriteByte('\n')

	for _, s := range o.Steps {
		b.WriteString(s.Name)
		b.WriteByte('|')
		b.WriteString(s.Op)
		b.WriteByte('|')
		b.WriteString(s.Outcome)
		b.WriteByte('|')
		if s.Failed {
			b.WriteByte('F')
		} else {
			b.WriteByte('-')
		}
		b.WriteByte('|')
		b.WriteString(s.Injected)
		b.WriteByte('|')
		s.Output.canonical(&b)
		b.WriteByte('|')
		s.State.canonical(&b)
		b.WriteByte('|')
		for _, e := range s.Events {
			b.WriteString(e.Type)
			b.WriteByte('{')
			e.Fields.canonical(&b)
			b.WriteString("},")
		}
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// BehaviourPrint fingerprints what the subject DID.
//
// Two observations with the same behaviour print are behaviourally identical:
// the same steps produced the same outputs, the same outcomes, the same state
// and the same events. They may have taken wildly different amounts of time.
func (o Observation) BehaviourPrint() Fingerprint {
	return fingerprintOf(o.behaviourCanonical())
}

// Timings returns each step's duration, keyed by step name.
//
// The other half of the split. Latency drift is measured here and nowhere else.
func (o Observation) Timings() map[string]time.Duration {
	out := make(map[string]time.Duration, len(o.Steps))
	for _, s := range o.Steps {
		out[s.Name] = s.Duration
	}
	return out
}

// TotalStepTime sums every step's duration.
//
// Distinct from [Observation.Duration], which includes setup, teardown and
// platform overhead. A gap between the two is the platform's own cost, and
// reporting both is what makes that gap visible rather than attributed to the
// subject.
func (o Observation) TotalStepTime() time.Duration {
	var total time.Duration
	for _, s := range o.Steps {
		total += s.Duration
	}
	return total
}

// Metric reads a reported metric, and whether it was reported at all.
func (o Observation) Metric(name string) (float64, bool) {
	v, ok := o.Metrics[name]
	return v, ok
}

// MetricNames returns the reported metric names, sorted.
func (o Observation) MetricNames() []string {
	out := make([]string, 0, len(o.Metrics))
	for k := range o.Metrics {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Step returns a step observation by name.
func (o Observation) Step(name string) (StepObservation, bool) {
	for _, s := range o.Steps {
		if s.Name == name {
			return s, true
		}
	}
	return StepObservation{}, false
}

// Summary renders the observation compactly, for reports and failure messages.
func (o Observation) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s on %s (v%d) steps=%d behaviour=%s took=%s\n",
		o.Scenario, o.Subject, o.ScenarioVersion, len(o.Steps),
		o.BehaviourPrint(), o.Duration())
	if o.Error != "" {
		fmt.Fprintf(&b, "  ERROR: %s\n", o.Error)
	}
	for _, s := range o.Steps {
		marker := " "
		if s.Failed {
			marker = "!"
		}
		fmt.Fprintf(&b, "  %s %-24s %-12s → %-14s %s",
			marker, s.Name, s.Op, s.Outcome, s.Duration)
		if s.Injected != "" {
			fmt.Fprintf(&b, " [injected %s]", s.Injected)
		}
		if s.Detail != "" {
			fmt.Fprintf(&b, " (%s)", s.Detail)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// Clone returns an independent copy.
//
// Observations are stored, compared and replayed, frequently from more than one
// goroutine. Handing out the stored one would let a report renderer mutate a
// golden — which is exactly the failure mode a golden exists to prevent.
func (o Observation) Clone() Observation {
	c := o
	c.Steps = make([]StepObservation, len(o.Steps))
	for i, s := range o.Steps {
		cs := s
		cs.Output = s.Output.Clone()
		cs.State = s.State.Clone()
		cs.Events = make([]EventRecord, len(s.Events))
		for j, e := range s.Events {
			ce := e
			ce.Fields = e.Fields.Clone()
			cs.Events[j] = ce
		}
		c.Steps[i] = cs
	}
	if o.Metrics != nil {
		c.Metrics = make(map[string]float64, len(o.Metrics))
		for k, v := range o.Metrics {
			c.Metrics[k] = v
		}
	}
	return c
}
