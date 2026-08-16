package governance

import (
	"fmt"
	"sort"
	"strings"
)

// RiskLevel is an aggregated risk band.
//
// FOUR BANDS, NOT A SCORE. A continuous score invites threshold-tuning as a
// substitute for policy, and a policy that says "deny above 0.73" is a policy
// nobody can review or defend. Bands force the question "what does high mean
// here" to be answered once, by a person, in the aggregator's configuration.
type RiskLevel uint8

// The risk levels.
const (
	RiskLow RiskLevel = iota
	RiskMedium
	RiskHigh
	RiskCritical
)

// String renders the level. Used as a metric label and in policy conditions.
func (r RiskLevel) String() string {
	switch r {
	case RiskMedium:
		return "medium"
	case RiskHigh:
		return "high"
	case RiskCritical:
		return "critical"
	default:
		return "low"
	}
}

// ParseRiskLevel maps a string back, reporting whether it was known.
func ParseRiskLevel(s string) (RiskLevel, bool) {
	switch s {
	case "low":
		return RiskLow, true
	case "medium":
		return RiskMedium, true
	case "high":
		return RiskHigh, true
	case "critical":
		return RiskCritical, true
	default:
		return RiskLow, false
	}
}

// AtLeast reports whether the level is at or above another.
func (r RiskLevel) AtLeast(other RiskLevel) bool { return r >= other }

// Signal is one risk observation from elsewhere in the platform.
//
// PRODUCED ELSEWHERE, AGGREGATED HERE. This module runs no fraud model, no
// anomaly detector and no scoring heuristic — the brief excludes all three, and
// they would be the wrong thing to put behind a governance boundary anyway. A
// signal is a fact another phase asserts; this engine's job is to combine facts
// deterministically and explain the combination.
type Signal struct {
	// Source names the subsystem that produced it: "fraud", "telephony",
	// "identity". Becomes a metric label, so it is a bounded vocabulary by
	// convention.
	Source string
	// Kind names what was observed: "velocity", "spoofed_cli", "new_device".
	Kind string
	// Level is the risk this signal alone implies.
	Level RiskLevel
	// Weight scales its contribution in [0, 1]. Zero means the signal is
	// informational and does not raise the level — useful for a detector that
	// is being shadow-deployed.
	Weight float64
	// Detail is a short machine-readable code. Never free text, never content.
	Detail string
}

func (s Signal) validate() []string {
	var problems []string
	if s.Source == "" {
		problems = append(problems, "signal: source is required")
	}
	if s.Kind == "" {
		problems = append(problems, "signal: kind is required")
	}
	if s.Weight < 0 || s.Weight > 1 {
		problems = append(problems, fmt.Sprintf(
			"signal %s/%s: weight %g is outside [0,1]", s.Source, s.Kind, s.Weight))
	}
	if s.Level > RiskCritical {
		problems = append(problems, fmt.Sprintf("signal %s/%s: unknown level", s.Source, s.Kind))
	}
	return problems
}

// RiskAssessment is the aggregated view a decision sees.
type RiskAssessment struct {
	// Level is the aggregate.
	Level RiskLevel
	// Signals are what produced it, sorted for determinism.
	Signals []Signal
	// Explanation is one line naming the dominant contributors. Safe for an
	// operator; it contains source and kind codes, never content.
	Explanation string
	// Capped reports that the aggregate was limited by a configured ceiling
	// rather than by the signals themselves. Surfaced because "we saw critical
	// signals but the ceiling said high" is a fact an operator must not have
	// to infer.
	Capped bool
}

// Dominant returns the signals at the assessment's level, sorted.
func (r RiskAssessment) Dominant() []Signal {
	var out []Signal
	for _, s := range r.Signals {
		if s.Level == r.Level {
			out = append(out, s)
		}
	}
	return out
}

// String renders the assessment.
func (r RiskAssessment) String() string {
	if len(r.Signals) == 0 {
		return r.Level.String() + " (no signals)"
	}
	return fmt.Sprintf("%s from %d signals: %s", r.Level, len(r.Signals), r.Explanation)
}

// Aggregator combines signals into an assessment.
//
// The aggregation rule is deliberately simple and deliberately MONOTONIC:
// adding a signal never lowers the level (INV-GOV-11). A non-monotonic
// aggregator — one where a reassuring signal cancels an alarming one — is a
// system where an attacker who can produce cheap reassuring signals can
// suppress expensive alarming ones.
//
// Reassurance belongs in policy, where it is visible: a rule may allow an
// action despite high risk, and the trace will say so.
type Aggregator struct {
	// Ceiling caps the aggregate. Zero value RiskCritical means no cap.
	Ceiling RiskLevel

	// MinWeight ignores signals below a weight. A signal with weight zero
	// never contributes regardless.
	MinWeight float64

	// EscalateOnCount raises the level one band when this many distinct
	// sources agree at the current level. Zero disables it.
	//
	// The one piece of genuine aggregation logic here: three independent
	// subsystems each calling something medium is a stronger statement than
	// one subsystem calling it medium three times, and counting SOURCES rather
	// than signals is what makes that true.
	EscalateOnCount int
}

// DefaultAggregator returns the platform baseline.
func DefaultAggregator() Aggregator {
	return Aggregator{Ceiling: RiskCritical, MinWeight: 0.01, EscalateOnCount: 3}
}

func (a Aggregator) validate() []string {
	var problems []string
	if a.Ceiling > RiskCritical {
		problems = append(problems, "aggregator: unknown ceiling level")
	}
	if a.MinWeight < 0 || a.MinWeight > 1 {
		problems = append(problems, fmt.Sprintf(
			"aggregator: MinWeight %g is outside [0,1]", a.MinWeight))
	}
	if a.EscalateOnCount < 0 {
		problems = append(problems, "aggregator: EscalateOnCount must not be negative")
	}
	return problems
}

// Aggregate combines signals deterministically.
//
// A PURE FUNCTION. No clock, no state, no I/O. Given the same signals it
// returns the same assessment forever, which is what lets a decision be
// recomputed from an audit record years later and compared.
func (a Aggregator) Aggregate(signals ...Signal) RiskAssessment {
	sorted := append([]Signal(nil), signals...)
	// Sorted so the explanation and the assessment are byte-stable regardless
	// of the order signals arrived in.
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Level != sorted[j].Level {
			return sorted[i].Level > sorted[j].Level
		}
		if sorted[i].Source != sorted[j].Source {
			return sorted[i].Source < sorted[j].Source
		}
		return sorted[i].Kind < sorted[j].Kind
	})

	assessment := RiskAssessment{Level: RiskLow, Signals: sorted}
	if len(sorted) == 0 {
		assessment.Explanation = "no signals"
		return assessment
	}

	level := RiskLow
	sourcesAt := make(map[RiskLevel]map[string]bool)
	for _, s := range sorted {
		if s.Weight <= 0 || s.Weight < a.MinWeight {
			continue
		}
		if s.Level > level {
			level = s.Level
		}
		if sourcesAt[s.Level] == nil {
			sourcesAt[s.Level] = make(map[string]bool)
		}
		sourcesAt[s.Level][s.Source] = true
	}

	// Corroboration across distinct sources raises the band by one.
	if a.EscalateOnCount > 0 && level < RiskCritical {
		if len(sourcesAt[level]) >= a.EscalateOnCount {
			level++
		}
	}

	ceiling := a.Ceiling
	if ceiling > RiskCritical {
		ceiling = RiskCritical
	}
	if level > ceiling {
		level = ceiling
		assessment.Capped = true
	}

	assessment.Level = level
	assessment.Explanation = explainRisk(level, sorted, assessment.Capped)
	return assessment
}

func explainRisk(level RiskLevel, sorted []Signal, capped bool) string {
	var contributors []string
	for _, s := range sorted {
		if s.Level >= level && s.Weight > 0 {
			contributors = append(contributors, s.Source+"/"+s.Kind)
		}
		if len(contributors) == 3 {
			break
		}
	}
	if len(contributors) == 0 {
		// Every signal was below the weight floor. Saying so is far more
		// useful than reporting "low" with no explanation, because it tells an
		// operator the detector is running but not counted.
		return "all signals below the weight floor"
	}
	out := strings.Join(contributors, ", ")
	if capped {
		out += " (capped by aggregator ceiling)"
	}
	return out
}

// RiskEvent is published when an assessment crosses a threshold.
//
// It carries source and kind codes, never content and never the signals'
// details, for the same reason every other event in this platform does not:
// a topic is forever.
type RiskEvent struct {
	// Level is the aggregate that triggered it.
	Level RiskLevel
	// Previous is the level before, so a consumer can see direction.
	Previous RiskLevel
	// Sources are the distinct contributing sources, sorted.
	Sources []string
	// Correlation ties it to a turn.
	Correlation CorrelationID
	// Subject is who it is about.
	Subject SubjectID
	// Decision is the decision it influenced, when there was one.
	Decision DecisionID
}

// Threshold declares the level at which an action kind needs more than an
// allow.
//
// Expressed as data so it can be reviewed alongside policies rather than living
// in code. The evaluator turns a crossed threshold into an obligation, so risk
// and policy meet at the one decision every action already passes through.
type Threshold struct {
	// Kind is the action kind the threshold applies to. Matching every kind is
	// expressed by [Thresholds.Any].
	Kind ActionKind
	// At is the level at which it fires.
	At RiskLevel
	// Then is what the outcome becomes.
	Then Outcome
	// Reason is a short machine-readable code.
	Reason string
}

// Thresholds is an ordered threshold set.
type Thresholds struct {
	// Any applies to every action kind.
	Any []Threshold
	// ByKind applies to one kind, and is checked after Any.
	ByKind []Threshold
}

// DefaultThresholds returns the platform baseline.
//
// Critical risk requires a supervisor for anything irreversible, and high risk
// requires confirmation for anything mutating. Both are deliberately mild:
// thresholds that deny outright make the risk engine a blunt instrument, and
// the first thing an operator does with a blunt instrument is raise the
// threshold until it stops firing.
func DefaultThresholds() Thresholds {
	return Thresholds{
		Any: []Threshold{
			{At: RiskCritical, Then: OutcomeRequireSupervisor, Reason: "risk_critical"},
			{At: RiskHigh, Then: OutcomeRequireConfirmation, Reason: "risk_high"},
		},
	}
}

// Apply returns the outcome a risk assessment forces for an action, and whether
// any threshold fired.
//
// Only ever raises. A threshold that could lower an outcome would let a risk
// signal overrule a policy denial, which inverts the entire precedence model.
func (t Thresholds) Apply(a Action, r RiskAssessment) (Outcome, string, bool) {
	best, reason, fired := OutcomeAllow, "", false

	consider := func(th Threshold, kindMatters bool) {
		if kindMatters && th.Kind != a.Kind {
			return
		}
		if !r.Level.AtLeast(th.At) {
			return
		}
		// Irreversible-only thresholds are expressed by the caller through the
		// action; here we simply take the most severe that fired.
		if th.Then.severity() > best.severity() {
			best, reason, fired = th.Then, th.Reason, true
		}
	}

	for _, th := range t.Any {
		consider(th, false)
	}
	for _, th := range t.ByKind {
		consider(th, true)
	}
	return best, reason, fired
}
