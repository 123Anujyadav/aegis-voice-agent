package evaluation

import (
	"fmt"
	"sort"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// GoldenKey identifies what a golden is filed under.
//
// All three components, because the same scenario at a different version is a
// different question and the same scenario against a different subject is a
// different answer.
type GoldenKey struct {
	Scenario ScenarioID
	Version  int
	Subject  SubjectName
}

// String renders the key.
func (k GoldenKey) String() string {
	return fmt.Sprintf("%s@v%d/%s", k.Scenario, k.Version, k.Subject)
}

// GoldenState is a golden's stage.
type GoldenState uint8

// The golden states.
const (
	// GoldenCandidate has been recorded but not approved. It is NOT used as a
	// baseline: comparing against an unreviewed recording is comparing against
	// whatever the system happened to do last time.
	GoldenCandidate GoldenState = iota

	// GoldenApproved is the baseline.
	GoldenApproved

	// GoldenSuperseded was the baseline and has been replaced. Retained,
	// because "what did we consider correct in March" is a question a
	// regression investigation asks.
	GoldenSuperseded

	// GoldenRetired is no longer used and no longer expected to be.
	GoldenRetired
)

// String renders the state.
func (s GoldenState) String() string {
	switch s {
	case GoldenApproved:
		return "approved"
	case GoldenSuperseded:
		return "superseded"
	case GoldenRetired:
		return "retired"
	default:
		return "candidate"
	}
}

// Golden is an approved observation: what the platform considers correct.
//
// A GOLDEN IS NEVER RECORDED AUTOMATICALLY.
//
// [GoldenStore.Record] produces a CANDIDATE. Promotion to approved requires
// [GoldenStore.Approve] with an author and a reason.
//
// The alternative — a platform that updates its own baseline when it sees a
// change — is a platform that reports no drift, ever. That is the classic
// golden-file failure and it is the single worst thing an evaluation platform
// can do, because it converts a silent regression into a silent regression with
// a green dashboard.
type Golden struct {
	// ID identifies this record.
	ID GoldenID
	// Key is what it is filed under.
	Key GoldenKey
	// State is its stage.
	State GoldenState
	// Observation is the recorded run.
	Observation Observation
	// Behaviour is the observation's behaviour fingerprint, denormalised so a
	// comparison does not have to re-encode the whole observation.
	Behaviour Fingerprint
	// ScenarioDigest fingerprints the scenario that produced it. A scenario
	// whose steps changed without a version bump is detectable here even
	// though the version check passed.
	ScenarioDigest Fingerprint
	// RecordedAt is when the observation was taken.
	RecordedAt time.Time
	// ApprovedAt and ApprovedBy record the human decision.
	ApprovedAt time.Time
	ApprovedBy string
	// Reason is why this behaviour is considered correct. REQUIRED to approve:
	// a baseline nobody justified is a baseline nobody can argue with later.
	Reason string
	// Supersedes names the golden this replaced, empty for the first.
	Supersedes GoldenID
}

// Approved reports whether the golden may be used as a baseline.
func (g Golden) Approved() bool { return g.State == GoldenApproved }

// Summary renders the golden for a report.
func (g Golden) Summary() string {
	s := fmt.Sprintf("%s %s behaviour=%s recorded=%s",
		g.Key, g.State, g.Behaviour, g.RecordedAt.UTC().Format(time.RFC3339))
	if g.ApprovedBy != "" {
		s += fmt.Sprintf(" approved by %s (%s)", g.ApprovedBy, g.Reason)
	}
	return s
}

// GoldenStore holds goldens and their history.
//
// In-memory for Phase 10F, with the durable implementation behind [Storage].
// The history is the point: a regression investigation asks "when did this
// change, and who approved the change", and a store that keeps only the current
// baseline cannot answer either half.
type GoldenStore struct {
	clock rt.Clock

	mu      sync.RWMutex
	current map[GoldenKey]*Golden
	history map[GoldenKey][]Golden

	// pending counts unapproved candidates across every key.
	//
	// Maintained incrementally because the metric that reports it is set on the
	// hot path, once per candidate filed. Deriving it by walking the history
	// meant deep-copying every pending golden — each carrying a whole
	// observation — on every filing, which is quadratic in the pending count
	// and made a twenty-scenario suite allocate 21 MB. See ENGINEERING_AUDIT F4.
	pending int
}

// NewGoldenStore builds an empty store.
func NewGoldenStore(clock rt.Clock) *GoldenStore {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	return &GoldenStore{
		clock:   clock,
		current: make(map[GoldenKey]*Golden),
		history: make(map[GoldenKey][]Golden),
	}
}

// Record files an observation as a CANDIDATE.
//
// It does not become a baseline and it does not replace an existing one. A
// candidate sitting next to an approved golden is exactly the state a drift
// review needs: here is what we agreed, here is what happens now, decide.
func (s *GoldenStore) Record(sc Scenario, obs Observation) (Golden, error) {
	if obs.Scenario != sc.ID {
		return Golden{}, invariant("INV-EVAL-4",
			"observation is from scenario %s, recording under %s", obs.Scenario, sc.ID)
	}

	g := Golden{
		ID: NewGoldenID(), Key: sc.Key(), State: GoldenCandidate,
		Observation: obs.Clone(), Behaviour: obs.BehaviourPrint(),
		ScenarioDigest: sc.Digest(), RecordedAt: s.clock.Now(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Candidates are kept in history rather than in current, so an approved
	// baseline is never displaced by a recording.
	s.history[g.Key] = append(s.history[g.Key], g)
	s.pending++
	return g, nil
}

// Approve promotes a candidate to the baseline.
//
// Requires an author and a reason. Both are refused if empty: an unattributed
// baseline change is indistinguishable from the platform quietly agreeing with
// whatever it saw last, which is the failure this whole design exists to
// prevent.
func (s *GoldenStore) Approve(id GoldenID, by, reason string) (Golden, error) {
	var problems []string
	if by == "" {
		problems = append(problems, "golden: ApprovedBy is required; an unattributed "+
			"baseline change is the platform agreeing with itself")
	}
	if reason == "" {
		problems = append(problems, "golden: reason is required; a baseline nobody "+
			"justified is a baseline nobody can argue with later")
	}
	if len(problems) > 0 {
		return Golden{}, &ConfigError{Problems: problems}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for key := range s.history {
		// Indexed through the map on every access rather than through a slice
		// captured by the range.
		//
		// The first version ranged over `list` and later wrote `s.history[key]
		// = list`. Between those two lines it appended the superseded record —
		// producing a NEW slice header — so writing the captured one back
		// silently discarded it. Approving a replacement therefore erased the
		// record of what it replaced, which is the one thing the history exists
		// to keep: "what did we consider correct in March" became unanswerable
		// the moment somebody answered it differently in April.
		// See ENGINEERING_AUDIT F3.
		idx := -1
		for i := range s.history[key] {
			if s.history[key][i].ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			continue
		}
		if s.history[key][idx].State == GoldenApproved {
			return Golden{}, fmt.Errorf("%w: %s", ErrAlreadyApproved, id)
		}

		promoted := s.history[key][idx]
		s.pending--
		promoted.State = GoldenApproved
		promoted.ApprovedAt = s.clock.Now()
		promoted.ApprovedBy = by
		promoted.Reason = reason

		if prev, ok := s.current[key]; ok {
			superseded := *prev
			superseded.State = GoldenSuperseded
			s.history[key] = append(s.history[key], superseded)
			promoted.Supersedes = prev.ID
		}

		s.history[key][idx] = promoted
		stored := promoted
		s.current[key] = &stored
		return promoted, nil
	}
	return Golden{}, fmt.Errorf("%w: golden %s", ErrNotRegistered, id)
}

// RecordAndApprove is the bootstrap path for a brand-new scenario.
//
// Separate from [GoldenStore.Record] and named so it cannot be reached by
// accident. It still demands an author and a reason, so the bootstrap is
// attributed like any other approval — the convenience is skipping a round
// trip, not skipping the review.
func (s *GoldenStore) RecordAndApprove(sc Scenario, obs Observation, by, reason string) (Golden, error) {
	candidate, err := s.Record(sc, obs)
	if err != nil {
		return Golden{}, err
	}
	return s.Approve(candidate.ID, by, reason)
}

// Baseline returns the approved golden for a key.
func (s *GoldenStore) Baseline(key GoldenKey) (Golden, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.current[key]
	if !ok {
		return Golden{}, fmt.Errorf("%w: %s", ErrNoGolden, key)
	}
	return *g, nil
}

// Candidates returns unapproved recordings for a key, oldest first.
func (s *GoldenStore) Candidates(key GoldenKey) []Golden {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Golden
	for _, g := range s.history[key] {
		if g.State == GoldenCandidate {
			out = append(out, g)
		}
	}
	return out
}

// History returns every record for a key, oldest first.
func (s *GoldenStore) History(key GoldenKey) []Golden {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Golden(nil), s.history[key]...)
}

// Retire marks a baseline as no longer expected.
//
// Used when a scenario is withdrawn. The record stays, because a trend history
// referencing it must still resolve.
func (s *GoldenStore) Retire(key GoldenKey, by, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.current[key]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoGolden, key)
	}
	retired := *g
	retired.State = GoldenRetired
	retired.ApprovedBy = by
	retired.Reason = reason
	s.history[key] = append(s.history[key], retired)
	delete(s.current, key)
	return nil
}

// Keys returns every key with an approved baseline, sorted.
func (s *GoldenStore) Keys() []GoldenKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]GoldenKey, 0, len(s.current))
	for k := range s.current {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Subject != out[j].Subject {
			return out[i].Subject < out[j].Subject
		}
		if out[i].Scenario != out[j].Scenario {
			return out[i].Scenario < out[j].Scenario
		}
		return out[i].Version < out[j].Version
	})
	return out
}

// Len returns the approved baseline count.
func (s *GoldenStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.current)
}

// PendingCount returns how many candidates await review.
//
// O(1). Use this for a metric or a report count; [GoldenStore.PendingApprovals]
// copies every record and is for the operator queue, where the records
// themselves are the point.
func (s *GoldenStore) PendingCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pending
}

// PendingApprovals returns every candidate awaiting review, across all keys,
// sorted.
//
// The operator-facing queue. A platform that records candidates and never
// surfaces them accumulates a pile of unreviewed drift, which is the same
// failure as auto-approving with extra steps.
func (s *GoldenStore) PendingApprovals() []Golden {
	s.mu.RLock()
	var out []Golden
	for _, list := range s.history {
		for _, g := range list {
			if g.State == GoldenCandidate {
				out = append(out, g)
			}
		}
	}
	s.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		if !out[i].RecordedAt.Equal(out[j].RecordedAt) {
			return out[i].RecordedAt.Before(out[j].RecordedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}
