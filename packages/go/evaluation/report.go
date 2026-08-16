package evaluation

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Coordinator performs operations that span the whole platform.
type Coordinator struct{ runtime *EvaluationRuntime }

// Scorecard is one subject's evaluation summary.
//
// SCORES ARE RATIOS OVER SCENARIOS THAT RAN. Skipped and no-baseline results are
// excluded from every denominator, because a suite that improved its score by
// adding scenarios nothing can execute is the metric gaming an evaluation
// platform must be hardest against — it is the one nobody notices.
type Scorecard struct {
	// Subject identifies whose scorecard this is.
	Subject SubjectName
	// Total, Passed, Drifted, Failed, Skipped and NoBaseline tally the
	// verdicts.
	Total      int
	Passed     int
	Drifted    int
	Failed     int
	Skipped    int
	NoBaseline int
	// PassRate, DriftRate and FailRate are over scenarios that produced a
	// verdict.
	PassRate  float64
	DriftRate float64
	FailRate  float64
	// Coverage is the share of scenarios that actually ran.
	Coverage float64
	// TotalTime is the summed step time across the subject's scenarios.
	TotalTime time.Duration
	// ByKind tallies verdicts per scenario kind.
	ByKind map[ScenarioKind]int
}

// Healthy reports that nothing failed.
//
// Drift is NOT unhealthy. A subject that drifted has changed and somebody must
// decide about it; a subject that failed is broken. Conflating them would make
// this method the thing a release gate reads and therefore make every deliberate
// improvement look like an outage.
func (s Scorecard) Healthy() bool { return s.Failed == 0 }

// String renders the scorecard.
func (s Scorecard) String() string {
	return fmt.Sprintf("%-14s %2d scenarios: %2d pass %2d drift %2d fail %2d skip "+
		"%2d new | pass=%.0f%% coverage=%.0f%% time=%s",
		s.Subject, s.Total, s.Passed, s.Drifted, s.Failed, s.Skipped, s.NoBaseline,
		100*s.PassRate, 100*s.Coverage, s.TotalTime)
}

// Scorecards builds one scorecard per subject from a run, sorted by subject.
func Scorecards(run Run) []Scorecard {
	bySubject := make(map[SubjectName]*Scorecard)

	for _, res := range run.Results {
		sc, ok := bySubject[res.Subject]
		if !ok {
			sc = &Scorecard{Subject: res.Subject, ByKind: map[ScenarioKind]int{}}
			bySubject[res.Subject] = sc
		}
		sc.Total++
		sc.ByKind[res.Kind]++
		sc.TotalTime += res.Observation.TotalStepTime()

		switch res.Verdict {
		case VerdictPass:
			sc.Passed++
		case VerdictDrift:
			sc.Drifted++
		case VerdictFail:
			sc.Failed++
		case VerdictSkipped:
			sc.Skipped++
		case VerdictNoBaseline:
			sc.NoBaseline++
		}
	}

	out := make([]Scorecard, 0, len(bySubject))
	for _, sc := range bySubject {
		decided := sc.Passed + sc.Drifted + sc.Failed
		if decided > 0 {
			sc.PassRate = float64(sc.Passed) / float64(decided)
			sc.DriftRate = float64(sc.Drifted) / float64(decided)
			sc.FailRate = float64(sc.Failed) / float64(decided)
		}
		if sc.Total > 0 {
			sc.Coverage = float64(sc.Total-sc.Skipped) / float64(sc.Total)
		}
		out = append(out, *sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Subject < out[j].Subject })
	return out
}

// RunReport is the full account of one run.
type RunReport struct {
	// Run identifies it.
	Run RunID
	// Suite and Label name what ran.
	Suite SuiteID
	Label string
	// RegistryDigest anchors it to a scenario set.
	RegistryDigest Fingerprint
	// Scorecards summarise each subject.
	Scorecards []Scorecard
	// Blocking are the results that should stop a release.
	Blocking []ScenarioResult
	// Drifted are the behaviour changes awaiting a decision.
	Drifted []ScenarioResult
	// Pending are golden candidates awaiting approval.
	Pending int
	// Duration is the run's wall time.
	Duration time.Duration
	// At is when it finished.
	At time.Time
}

// Releasable reports that nothing blocking failed.
//
// The one-line answer a release gate reads. It is deliberately NOT "everything
// passed": drift and missing baselines do not block, because a platform that
// blocked on both would train people to approve baselines without reading them.
func (r RunReport) Releasable() bool { return len(r.Blocking) == 0 }

// Summary renders the report.
func (r RunReport) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "run %s", r.Run)
	if r.Label != "" {
		fmt.Fprintf(&b, " (%s)", r.Label)
	}
	if r.Suite != "" {
		fmt.Fprintf(&b, " suite=%s", r.Suite)
	}
	fmt.Fprintf(&b, " scenarios=%s digest=%s took=%s releasable=%v\n",
		r.At.UTC().Format(time.RFC3339), r.RegistryDigest, r.Duration, r.Releasable())

	for _, sc := range r.Scorecards {
		fmt.Fprintf(&b, "  %s\n", sc)
	}
	if len(r.Blocking) > 0 {
		b.WriteString("  BLOCKING:\n")
		for _, res := range r.Blocking {
			fmt.Fprintf(&b, "    %s on %s: %s\n",
				res.Scenario, res.Subject, res.Comparison.Reason)
		}
	}
	if len(r.Drifted) > 0 {
		b.WriteString("  DRIFT (awaiting a decision):\n")
		for _, res := range r.Drifted {
			fmt.Fprintf(&b, "    %s on %s: %s → %s (%d differences)\n",
				res.Scenario, res.Subject,
				res.Comparison.BaselineBehaviour, res.Comparison.ObservedBehaviour,
				len(res.Comparison.Differences))
		}
	}
	if r.Pending > 0 {
		fmt.Fprintf(&b, "  %d golden candidates awaiting approval\n", r.Pending)
	}
	return b.String()
}

// Report builds the full account of a run.
func (c *Coordinator) Report(run Run) RunReport {
	return RunReport{
		Run: run.ID, Suite: run.Suite, Label: run.Label,
		RegistryDigest: run.RegistryDigest,
		Scorecards:     Scorecards(run),
		Blocking:       run.Blocking(),
		Drifted:        run.Drifted(),
		Pending:        c.runtime.goldens.PendingCount(),
		Duration:       run.Duration(),
		At:             run.FinishedAt,
	}
}

// CompareRuns builds a regression report between two stored runs.
func (c *Coordinator) CompareRuns(baseline, current RunID, latencyRatio float64) (RegressionReport, error) {
	b, err := c.runtime.storage.GetRun(baseline)
	if err != nil {
		return RegressionReport{}, err
	}
	cur, err := c.runtime.storage.GetRun(current)
	if err != nil {
		return RegressionReport{}, err
	}
	if latencyRatio <= 0 {
		latencyRatio = c.runtime.cfg.Tolerances.LatencyRatio
	}

	report := DetectRegressions(b, cur, latencyRatio,
		c.runtime.cfg.Tolerances.LatencyFloor)
	if c.runtime.metrics != nil {
		for _, reg := range report.Regressions {
			c.runtime.metrics.Regressions.Inc(string(reg.Subject), string(reg.Kind))
		}
		for _, imp := range report.Improvements {
			c.runtime.metrics.Improvements.Inc(string(imp.Subject))
		}
	}
	return report, nil
}

// CompareAgainstPrevious compares the latest run against the one before it.
func (c *Coordinator) CompareAgainstPrevious(suite SuiteID) (RegressionReport, error) {
	runs := c.runtime.storage.Runs(0)
	var matching []Run
	for _, r := range runs {
		if suite == "" || r.Suite == suite {
			matching = append(matching, r)
		}
	}
	if len(matching) < 2 {
		return RegressionReport{}, ErrNoBaselineRun
	}
	// Runs() returns newest first.
	return c.CompareRuns(matching[1].ID, matching[0].ID, 0)
}

// ---------------------------------------------------------------------------
// Dashboard model
// ---------------------------------------------------------------------------

// DashboardModel is what an operator console would render.
//
// STRUCTS ONLY, NO RENDERER. The shape of what an operator needs to see is a
// design decision worth freezing and reviewing; the pixels are not, and a
// renderer here would bind the platform to one console.
//
// It is also the honest scope boundary: the brief asks for a dashboard MODEL and
// explicitly excludes the UI.
type DashboardModel struct {
	// Summary is the headline.
	Summary RunSummary
	// Subjects are the per-subsystem panels.
	Subjects []SubsystemSummary
	// Trends are the per-scenario histories.
	Trends []TrendSeries
	// Failures is the failure heatmap.
	Failures Heatmap
	// Latency is the latency heatmap.
	Latency Heatmap
	// Coverage reports what is and is not evaluated.
	Coverage Coverage
	// GeneratedAt stamps it.
	GeneratedAt time.Time
}

// RunSummary is the headline panel.
type RunSummary struct {
	Run            RunID
	Suite          SuiteID
	Label          string
	Total          int
	Passed         int
	Drifted        int
	Failed         int
	Skipped        int
	NoBaseline     int
	Releasable     bool
	Duration       time.Duration
	RegistryDigest Fingerprint
	At             time.Time
}

// SubsystemSummary is one subject's panel.
type SubsystemSummary struct {
	Subject   SubjectName
	Scorecard Scorecard
	// WorstScenarios lists the failing and drifting scenarios, worst first.
	WorstScenarios []ScenarioID
	// SlowestScenarios lists the scenarios by descending step time.
	SlowestScenarios []ScenarioID
}

// TrendSeries is one scenario's history.
type TrendSeries struct {
	Scenario ScenarioID
	Subject  SubjectName
	Points   []TrendPoint
	// BehaviourChanges counts how many times the fingerprint moved. The
	// headline number of a trend: a scenario whose behaviour changed nine
	// times in twenty runs is a scenario nobody should trust as a baseline.
	BehaviourChanges int
	// Stable reports that the behaviour has not changed across the window.
	Stable bool
}

// HeatmapCell is one cell.
type HeatmapCell struct {
	Row   string
	Col   string
	Value float64
	// Count is the sample count behind the value, so a reader can tell a
	// 100% failure rate over one observation from one over two hundred.
	Count int
}

// Heatmap is a two-dimensional summary.
type Heatmap struct {
	// Title names it.
	Title string
	// RowLabel and ColLabel name the axes.
	RowLabel string
	ColLabel string
	// Rows and Cols are the axis values, sorted, so two renderings of the same
	// data produce the same grid.
	Rows []string
	Cols []string
	// Cells are the populated cells, sorted by row then column.
	Cells []HeatmapCell
}

// Cell returns a cell's value and whether it was populated.
func (h Heatmap) Cell(row, col string) (HeatmapCell, bool) {
	for _, c := range h.Cells {
		if c.Row == row && c.Col == col {
			return c, true
		}
	}
	return HeatmapCell{}, false
}

// Dashboard builds the model from a run and the stored history.
func (c *Coordinator) Dashboard(run Run, trendWindow int) DashboardModel {
	counts := run.Counts()

	model := DashboardModel{
		Summary: RunSummary{
			Run: run.ID, Suite: run.Suite, Label: run.Label,
			Total: len(run.Results), Passed: counts[VerdictPass],
			Drifted: counts[VerdictDrift], Failed: counts[VerdictFail],
			Skipped: counts[VerdictSkipped], NoBaseline: counts[VerdictNoBaseline],
			Releasable: len(run.Blocking()) == 0, Duration: run.Duration(),
			RegistryDigest: run.RegistryDigest, At: run.FinishedAt,
		},
		Coverage:    c.runtime.registry.Coverage(),
		GeneratedAt: c.runtime.clock.Now(),
	}

	for _, sc := range Scorecards(run) {
		summary := SubsystemSummary{Subject: sc.Subject, Scorecard: sc}
		var worst, slowest []ScenarioResult
		for _, res := range run.Results {
			if res.Subject != sc.Subject {
				continue
			}
			if res.Verdict == VerdictFail || res.Verdict == VerdictDrift {
				worst = append(worst, res)
			}
			slowest = append(slowest, res)
		}
		sort.SliceStable(worst, func(i, j int) bool {
			// Failures before drift, then alphabetical — a stable order so the
			// panel does not reshuffle between refreshes.
			if worst[i].Verdict != worst[j].Verdict {
				return worst[i].Verdict == VerdictFail
			}
			return worst[i].Scenario < worst[j].Scenario
		})
		sort.SliceStable(slowest, func(i, j int) bool {
			return slowest[i].Observation.TotalStepTime() > slowest[j].Observation.TotalStepTime()
		})
		for _, res := range worst {
			summary.WorstScenarios = append(summary.WorstScenarios, res.Scenario)
		}
		for i, res := range slowest {
			if i >= 5 {
				break
			}
			summary.SlowestScenarios = append(summary.SlowestScenarios, res.Scenario)
		}
		model.Subjects = append(model.Subjects, summary)
	}

	for _, res := range run.Results {
		points := c.runtime.storage.Trend(res.Scenario)
		if trendWindow > 0 && len(points) > trendWindow {
			points = points[len(points)-trendWindow:]
		}
		series := TrendSeries{Scenario: res.Scenario, Subject: res.Subject, Points: points}
		for i := 1; i < len(points); i++ {
			if points[i].Behaviour != points[i-1].Behaviour {
				series.BehaviourChanges++
			}
		}
		series.Stable = series.BehaviourChanges == 0
		model.Trends = append(model.Trends, series)
	}
	sort.SliceStable(model.Trends, func(i, j int) bool {
		return model.Trends[i].Scenario < model.Trends[j].Scenario
	})

	model.Failures = failureHeatmap(run)
	model.Latency = latencyHeatmap(run)
	return model
}

// failureHeatmap plots subject against scenario kind.
//
// Those two axes because they answer the question a heatmap is for: is this
// subsystem broken, or is this KIND of scenario broken everywhere? A heatmap of
// scenario against run would answer neither.
func failureHeatmap(run Run) Heatmap {
	h := Heatmap{Title: "failures", RowLabel: "subject", ColLabel: "kind"}

	type cell struct{ failed, total int }
	grid := make(map[string]map[string]*cell)
	rows := map[string]bool{}
	cols := map[string]bool{}

	for _, res := range run.Results {
		row, col := string(res.Subject), res.Kind.String()
		rows[row], cols[col] = true, true
		if grid[row] == nil {
			grid[row] = make(map[string]*cell)
		}
		if grid[row][col] == nil {
			grid[row][col] = &cell{}
		}
		grid[row][col].total++
		if res.Verdict == VerdictFail {
			grid[row][col].failed++
		}
	}

	h.Rows, h.Cols = sortedKeys(rows), sortedKeys(cols)
	for _, row := range h.Rows {
		for _, col := range h.Cols {
			c, ok := grid[row][col]
			if !ok || c.total == 0 {
				continue
			}
			h.Cells = append(h.Cells, HeatmapCell{Row: row, Col: col,
				Value: float64(c.failed) / float64(c.total), Count: c.total})
		}
	}
	return h
}

// latencyHeatmap plots subject against scenario kind, valued in seconds.
func latencyHeatmap(run Run) Heatmap {
	h := Heatmap{Title: "latency_seconds", RowLabel: "subject", ColLabel: "kind"}

	type cell struct {
		total time.Duration
		n     int
	}
	grid := make(map[string]map[string]*cell)
	rows := map[string]bool{}
	cols := map[string]bool{}

	for _, res := range run.Results {
		row, col := string(res.Subject), res.Kind.String()
		rows[row], cols[col] = true, true
		if grid[row] == nil {
			grid[row] = make(map[string]*cell)
		}
		if grid[row][col] == nil {
			grid[row][col] = &cell{}
		}
		grid[row][col].total += res.Observation.TotalStepTime()
		grid[row][col].n++
	}

	h.Rows, h.Cols = sortedKeys(rows), sortedKeys(cols)
	for _, row := range h.Rows {
		for _, col := range h.Cols {
			c, ok := grid[row][col]
			if !ok || c.n == 0 {
				continue
			}
			h.Cells = append(h.Cells, HeatmapCell{Row: row, Col: col,
				Value: (c.total / time.Duration(c.n)).Seconds(), Count: c.n})
		}
	}
	return h
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Platform readiness
// ---------------------------------------------------------------------------

// PlatformReadiness is the consolidated verdict across every subsystem.
type PlatformReadiness struct {
	// Run identifies the run it was built from.
	Run RunID
	// Subjects are the per-subsystem scorecards.
	Subjects []Scorecard
	// Determinism reports each subject's determinism checks.
	Determinism []DeterminismResult
	// Coverage reports what is evaluated.
	Coverage Coverage
	// Blockers are the results that stop a release.
	Blockers []ScenarioResult
	// UncoveredKinds names scenario kinds with no scenarios at all.
	UncoveredKinds []ScenarioKind
	// UnevaluatedSubjects names registered subjects with no scenarios.
	//
	// The most important field in the struct. A subsystem with zero scenarios
	// scores nothing, appears in no scorecard, and would otherwise be invisible
	// in a readiness report that looked entirely green.
	UnevaluatedSubjects []SubjectName
	// Ready reports that nothing blocks.
	Ready bool
	// At stamps it.
	At time.Time
}

// Summary renders the readiness report.
func (p PlatformReadiness) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "platform readiness from run %s: ready=%v\n", p.Run, p.Ready)
	for _, sc := range p.Subjects {
		fmt.Fprintf(&b, "  %s\n", sc)
	}
	for _, d := range p.Determinism {
		fmt.Fprintf(&b, "  %s\n", d.Summary())
	}
	if len(p.UnevaluatedSubjects) > 0 {
		fmt.Fprintf(&b, "  SUBSYSTEMS WITH NO SCENARIOS: %v\n", p.UnevaluatedSubjects)
	}
	if len(p.UncoveredKinds) > 0 {
		fmt.Fprintf(&b, "  scenario kinds with no coverage: %v\n", p.UncoveredKinds)
	}
	for _, blocker := range p.Blockers {
		fmt.Fprintf(&b, "  BLOCKER: %s on %s (%s)\n",
			blocker.Scenario, blocker.Subject, blocker.Comparison.Reason)
	}
	return b.String()
}

// Readiness runs everything and builds the consolidated verdict.
//
// It checks determinism as well as behaviour, because "the platform is ready"
// and "the platform gives the same answer twice" are different claims and the
// second is a precondition for the first being meaningful.
func (c *Coordinator) Readiness(ctx context.Context, label string, determinismRuns int) PlatformReadiness {
	run := c.runtime.RunAll(ctx, label)

	readiness := PlatformReadiness{
		Run: run.ID, Subjects: Scorecards(run),
		Coverage: c.runtime.registry.Coverage(),
		Blockers: run.Blocking(), At: c.runtime.clock.Now(),
	}
	readiness.UncoveredKinds = readiness.Coverage.UncoveredKinds()

	evaluated := make(map[SubjectName]bool, len(readiness.Subjects))
	for _, sc := range readiness.Subjects {
		evaluated[sc.Subject] = true
	}
	for _, name := range c.runtime.subjects.Names() {
		if !evaluated[name] {
			readiness.UnevaluatedSubjects = append(readiness.UnevaluatedSubjects, name)
		}
	}

	// One determinism check per subject, on its first scenario. Enough to
	// answer "does this subsystem reproduce", cheap enough to run in a
	// readiness pass, and the per-scenario checks remain available for
	// anything that needs more.
	seen := make(map[SubjectName]bool)
	for _, s := range c.runtime.registry.Scenarios() {
		if seen[s.SubjectName] {
			continue
		}
		seen[s.SubjectName] = true
		readiness.Determinism = append(readiness.Determinism,
			c.runtime.CheckDeterminism(ctx, s, determinismRuns))
	}

	nonDeterministic := false
	for _, d := range readiness.Determinism {
		if !d.Deterministic {
			nonDeterministic = true
		}
	}
	readiness.Ready = len(readiness.Blockers) == 0 && !nonDeterministic &&
		len(readiness.UnevaluatedSubjects) == 0

	return readiness
}
