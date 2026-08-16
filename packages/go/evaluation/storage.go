package evaluation

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Storage persists evaluation artefacts.
//
// An interface with one in-memory implementation here. The durable
// implementation — Aurora for runs and results, Redis for the hot trend window
// — is a sibling module's job, which keeps this module's dependency graph at one
// first-party package.
//
// Every method returns an error and NONE of them fails a run. A platform that
// stopped evaluating because it could not write a result would lose the result
// AND the evaluation; storing is best-effort and the failure is counted.
type Storage interface {
	// PutRun stores a completed run.
	PutRun(Run) error
	// GetRun retrieves a run.
	GetRun(RunID) (Run, error)
	// Runs returns runs newest first, bounded by limit.
	Runs(limit int) []Run
	// LatestRun returns the most recent run for a suite, or any suite when the
	// identifier is empty.
	LatestRun(SuiteID) (Run, error)

	// PutObservation stores one observation.
	PutObservation(Observation) error
	// Observations returns stored observations for a scenario, oldest first.
	Observations(ScenarioID) []Observation

	// PutBenchmark stores a benchmark result.
	PutBenchmark(BenchmarkResult) error
	// Benchmarks returns results for a scenario, oldest first.
	Benchmarks(ScenarioID) []BenchmarkResult

	// PutTrend appends a trend point.
	PutTrend(TrendPoint) error
	// Trend returns the history for a scenario, oldest first.
	Trend(ScenarioID) []TrendPoint
}

// TrendPoint is one scenario's outcome at one point in time.
//
// IT CARRIES A FINGERPRINT, NOT AN OBSERVATION. A trend history spans releases
// and is retained for years; storing the observations would make it a permanent
// archive of everything every subsystem ever did, which is a retention problem
// wearing a dashboard's clothes.
//
// The fingerprint answers what a trend actually asks: did the behaviour change,
// and when.
type TrendPoint struct {
	// Run and Scenario identify it.
	Run      RunID
	Scenario ScenarioID
	Subject  SubjectName
	// Verdict is the outcome.
	Verdict Verdict
	// Behaviour fingerprints what the subject did.
	Behaviour Fingerprint
	// StepSeconds is the total step time, for latency trends.
	StepSeconds float64
	// At is when the run happened.
	At time.Time
	// Label is the run's operator-supplied name.
	Label string
}

// MemoryStorage is the in-memory implementation.
//
// Bounded on purpose. An evaluation platform left running in CI accumulates
// observations at the rate of scenarios × runs, and an unbounded store turns a
// long-lived process into an out-of-memory kill — which would take down the
// thing measuring the platform's health.
type MemoryStorage struct {
	mu sync.RWMutex

	runs         map[RunID]Run
	runOrder     []RunID
	maxRuns      int
	observations map[ScenarioID][]Observation
	maxObs       int
	benchmarks   map[ScenarioID][]BenchmarkResult
	maxBench     int
	trends       map[ScenarioID][]TrendPoint
	maxTrend     int
}

// NewMemoryStorage builds an in-memory store with default bounds.
func NewMemoryStorage() *MemoryStorage {
	return NewBoundedMemoryStorage(200, 50, 500)
}

// NewBoundedMemoryStorage builds an in-memory store with explicit bounds.
func NewBoundedMemoryStorage(maxRuns, maxObservationsPerScenario, maxTrendPoints int) *MemoryStorage {
	if maxRuns <= 0 {
		maxRuns = 200
	}
	if maxObservationsPerScenario <= 0 {
		maxObservationsPerScenario = 50
	}
	if maxTrendPoints <= 0 {
		maxTrendPoints = 500
	}
	return &MemoryStorage{
		runs: make(map[RunID]Run), maxRuns: maxRuns,
		observations: make(map[ScenarioID][]Observation), maxObs: maxObservationsPerScenario,
		// Benchmarks share the observation bound. They were originally the one
		// unbounded collection here, in a store whose entire stated purpose is
		// being bounded — a platform benchmarking on a schedule would have grown
		// this map for as long as it ran. See ENGINEERING_AUDIT F8.
		benchmarks: make(map[ScenarioID][]BenchmarkResult),
		maxBench:   maxObservationsPerScenario,
		trends:     make(map[ScenarioID][]TrendPoint), maxTrend: maxTrendPoints,
	}
}

// PutRun stores a run and appends its trend points.
//
// The trend is derived here rather than by the caller, so a run cannot be stored
// without its trend — a history with gaps is a history that reports a behaviour
// change on the wrong day.
func (s *MemoryStorage) PutRun(r Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.runOrder) >= s.maxRuns {
		delete(s.runs, s.runOrder[0])
		s.runOrder = s.runOrder[1:]
	}
	s.runs[r.ID] = r
	s.runOrder = append(s.runOrder, r.ID)

	for _, res := range r.Results {
		point := TrendPoint{
			Run: r.ID, Scenario: res.Scenario, Subject: res.Subject,
			Verdict: res.Verdict, Behaviour: res.Observation.BehaviourPrint(),
			StepSeconds: res.Observation.TotalStepTime().Seconds(),
			At:          r.FinishedAt, Label: r.Label,
		}
		list := append(s.trends[res.Scenario], point)
		if len(list) > s.maxTrend {
			list = list[len(list)-s.maxTrend:]
		}
		s.trends[res.Scenario] = list
	}
	return nil
}

// GetRun retrieves a run.
func (s *MemoryStorage) GetRun(id RunID) (Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.runs[id]
	if !ok {
		return Run{}, fmt.Errorf("%w: run %s", ErrNotRegistered, id)
	}
	return r, nil
}

// Runs returns runs newest first.
func (s *MemoryStorage) Runs(limit int) []Run {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > len(s.runOrder) {
		limit = len(s.runOrder)
	}
	out := make([]Run, 0, limit)
	for i := len(s.runOrder) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.runs[s.runOrder[i]])
	}
	return out
}

// LatestRun returns the most recent run for a suite.
func (s *MemoryStorage) LatestRun(suite SuiteID) (Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := len(s.runOrder) - 1; i >= 0; i-- {
		r := s.runs[s.runOrder[i]]
		if suite == "" || r.Suite == suite {
			return r, nil
		}
	}
	return Run{}, ErrNoBaselineRun
}

// PutObservation stores an observation.
func (s *MemoryStorage) PutObservation(o Observation) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	list := append(s.observations[o.Scenario], o.Clone())
	if len(list) > s.maxObs {
		list = list[len(list)-s.maxObs:]
	}
	s.observations[o.Scenario] = list
	return nil
}

// Observations returns stored observations, oldest first.
func (s *MemoryStorage) Observations(id ScenarioID) []Observation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Observation, 0, len(s.observations[id]))
	for _, o := range s.observations[id] {
		out = append(out, o.Clone())
	}
	return out
}

// PutBenchmark stores a benchmark result.
func (s *MemoryStorage) PutBenchmark(b BenchmarkResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := append(s.benchmarks[b.Scenario], b)
	if len(list) > s.maxBench {
		list = list[len(list)-s.maxBench:]
	}
	s.benchmarks[b.Scenario] = list
	return nil
}

// Benchmarks returns results for a scenario, oldest first.
func (s *MemoryStorage) Benchmarks(id ScenarioID) []BenchmarkResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]BenchmarkResult(nil), s.benchmarks[id]...)
}

// PutTrend appends a trend point.
func (s *MemoryStorage) PutTrend(p TrendPoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := append(s.trends[p.Scenario], p)
	if len(list) > s.maxTrend {
		list = list[len(list)-s.maxTrend:]
	}
	s.trends[p.Scenario] = list
	return nil
}

// Trend returns the history for a scenario, oldest first.
func (s *MemoryStorage) Trend(id ScenarioID) []TrendPoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]TrendPoint(nil), s.trends[id]...)
}

// Scenarios returns every scenario with stored history, sorted.
func (s *MemoryStorage) Scenarios() []ScenarioID {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[ScenarioID]bool, len(s.trends))
	for id := range s.trends {
		seen[id] = true
	}
	for id := range s.observations {
		seen[id] = true
	}
	out := make([]ScenarioID, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Stats reports what the store holds, for operators.
type StorageStats struct {
	Runs         int
	Observations int
	Benchmarks   int
	TrendPoints  int
	// Evicted reports whether any bound has been hit, so a reader knows
	// whether they are looking at the whole history or the recent tail.
	Evicted bool
}

// Stats returns the store's contents.
func (s *MemoryStorage) Stats() StorageStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st := StorageStats{Runs: len(s.runOrder), Evicted: len(s.runOrder) >= s.maxRuns}
	for _, list := range s.observations {
		st.Observations += len(list)
		if len(list) >= s.maxObs {
			st.Evicted = true
		}
	}
	for _, list := range s.benchmarks {
		st.Benchmarks += len(list)
	}
	for _, list := range s.trends {
		st.TrendPoints += len(list)
		if len(list) >= s.maxTrend {
			st.Evicted = true
		}
	}
	return st
}
