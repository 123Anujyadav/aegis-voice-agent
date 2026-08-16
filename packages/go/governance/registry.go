package governance

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// PolicyRegistry holds every policy.
//
// COPY-ON-WRITE. Reads take no lock: they load an immutable snapshot pointer
// and walk it. Writes build a new snapshot and swap the pointer.
//
// The trade is right because the read:write ratio is enormous — every decision
// reads the whole registry, and policies change on deploy. But the property
// that matters more than the speed is this: A DECISION IS MADE AGAINST EXACTLY
// ONE SNAPSHOT. A policy reload part-way through an evaluation cannot produce a
// decision that half-obeys the old rules and half the new, and the snapshot
// version travels in the [Decision] so the decision can be recomputed against
// the same rules years later (INV-GOV-9).
type PolicyRegistry struct {
	clock   rt.Clock
	metrics *Metrics
	audit   Auditor

	snapshot atomic.Pointer[PolicySnapshot]
	writeMu  sync.Mutex
	version  atomic.Uint64
}

// PolicySnapshot is an immutable view of every policy.
type PolicySnapshot struct {
	// Version identifies the snapshot. Monotonic per registry.
	Version uint64
	// byID is the primary index.
	byID map[PolicyID]Policy
	// byScope lists policies per scope, PRE-SORTED by descending priority so
	// the evaluator does no sorting on the read path.
	byScope map[Scope][]PolicyID
	// order is every policy, sorted, for deterministic enumeration.
	order []PolicyID
	// Digest fingerprints the whole set's decision-relevant content.
	Digest Fingerprint
	// BuiltAt is when the snapshot was created.
	BuiltAt time.Time
}

// Len returns the policy count.
func (s *PolicySnapshot) Len() int { return len(s.byID) }

// Get returns a policy by identifier.
func (s *PolicySnapshot) Get(id PolicyID) (Policy, bool) {
	p, ok := s.byID[id]
	return p, ok
}

// InScope returns the policy identifiers for a scope, in evaluation order.
func (s *PolicySnapshot) InScope(scope Scope) []PolicyID { return s.byScope[scope] }

// All returns every policy, sorted by scope then descending priority then ID.
func (s *PolicySnapshot) All() []Policy {
	out := make([]Policy, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.byID[id])
	}
	return out
}

// NewPolicyRegistry builds an empty registry.
//
// AN EMPTY REGISTRY DENIES EVERYTHING. That is the correct starting state and
// the reason [BaselinePolicies] is an explicit call rather than a default: a
// safety engine that ships with permissive defaults is a safety engine whose
// most common production configuration was never reviewed.
func NewPolicyRegistry(clock rt.Clock, metrics *Metrics, audit Auditor) *PolicyRegistry {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	r := &PolicyRegistry{clock: clock, metrics: metrics, audit: audit}
	r.snapshot.Store(&PolicySnapshot{
		Version: 0,
		byID:    map[PolicyID]Policy{},
		byScope: map[Scope][]PolicyID{},
		BuiltAt: clock.Now(),
	})
	return r
}

// Snapshot returns the current immutable view.
func (r *PolicyRegistry) Snapshot() *PolicySnapshot { return r.snapshot.Load() }

// Version returns the current snapshot version.
func (r *PolicyRegistry) Version() uint64 { return r.snapshot.Load().Version }

// Len returns the policy count.
func (r *PolicyRegistry) Len() int { return r.snapshot.Load().Len() }

// Register adds or replaces a policy.
//
// Validated here and never again. Every downstream stage — resolution, merge,
// conflict detection, trace assembly — assumes a registered policy is
// well-formed, which is only safe because nothing enters without passing this.
func (r *PolicyRegistry) Register(p Policy) error {
	if problems := p.validate(); len(problems) > 0 {
		sort.Strings(problems)
		return &ConfigError{Problems: problems}
	}

	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	old := r.snapshot.Load()
	if prev, exists := old.byID[p.ID]; exists && p.Version <= prev.Version {
		return &ConfigError{Problems: []string{fmt.Sprintf(
			"%s: version %d does not advance the registered version %d; a policy "+
				"change that does not bump the version is a change no audit record "+
				"can distinguish from the old one", p.ID, p.Version, prev.Version)}}
	}

	r.commit(old, func(next map[PolicyID]Policy) { next[p.ID] = p })

	if r.audit != nil {
		_ = r.audit.Record(AuditEntry{
			At: r.clock.Now(), Kind: AuditPolicyRegistered, Policy: p.ID,
			Reason: p.Scope.String(),
			Details: map[string]string{
				"version": fmt.Sprint(p.Version),
				"owner":   p.Owner,
				"digest":  string(p.Digest()),
			},
		})
	}
	return nil
}

// RegisterAll registers several policies atomically.
//
// ALL OR NOTHING. A partial policy load is the worst possible state: the
// platform is running under half a rule set, and which half depends on the
// order somebody wrote the calls in.
func (r *PolicyRegistry) RegisterAll(policies ...Policy) error {
	var problems []string
	seen := make(map[PolicyID]bool, len(policies))
	for _, p := range policies {
		problems = append(problems, p.validate()...)
		if seen[p.ID] {
			problems = append(problems, fmt.Sprintf("%s: registered twice in one batch", p.ID))
		}
		seen[p.ID] = true
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return &ConfigError{Problems: problems}
	}

	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	old := r.snapshot.Load()
	for _, p := range policies {
		if prev, exists := old.byID[p.ID]; exists && p.Version <= prev.Version {
			return &ConfigError{Problems: []string{fmt.Sprintf(
				"%s: version %d does not advance %d", p.ID, p.Version, prev.Version)}}
		}
	}

	r.commit(old, func(next map[PolicyID]Policy) {
		for _, p := range policies {
			next[p.ID] = p
		}
	})

	if r.audit != nil {
		for _, p := range policies {
			_ = r.audit.Record(AuditEntry{
				At: r.clock.Now(), Kind: AuditPolicyRegistered, Policy: p.ID,
				Reason:  p.Scope.String(),
				Details: map[string]string{"version": fmt.Sprint(p.Version), "owner": p.Owner},
			})
		}
	}
	return nil
}

// Unregister removes a policy.
func (r *PolicyRegistry) Unregister(id PolicyID) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	old := r.snapshot.Load()
	if _, ok := old.byID[id]; !ok {
		return fmt.Errorf("%w: %s", ErrNotRegistered, id)
	}
	r.commit(old, func(next map[PolicyID]Policy) { delete(next, id) })

	if r.audit != nil {
		_ = r.audit.Record(AuditEntry{
			At: r.clock.Now(), Kind: AuditPolicyUnregistered, Policy: id})
	}
	return nil
}

// SetEnabled enables or disables a policy without changing its content.
//
// Disabling rather than unregistering keeps the policy resolvable, so an audit
// record naming it still means something — the same argument Phase 10D makes
// for retiring a tool rather than removing it.
func (r *PolicyRegistry) SetEnabled(id PolicyID, enabled bool) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	old := r.snapshot.Load()
	p, ok := old.byID[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotRegistered, id)
	}
	if p.Enabled == enabled {
		return nil
	}
	p.Enabled = enabled
	r.commit(old, func(next map[PolicyID]Policy) { next[id] = p })

	if r.audit != nil {
		reason := "disabled"
		if enabled {
			reason = "enabled"
		}
		_ = r.audit.Record(AuditEntry{
			At: r.clock.Now(), Kind: AuditPolicyToggled, Policy: id, Reason: reason})
	}
	return nil
}

// commit rebuilds the snapshot and swaps it in. Caller holds writeMu.
func (r *PolicyRegistry) commit(old *PolicySnapshot, mutate func(map[PolicyID]Policy)) {
	next := make(map[PolicyID]Policy, len(old.byID)+1)
	for k, v := range old.byID {
		next[k] = v
	}
	mutate(next)

	snap := &PolicySnapshot{
		Version: r.version.Add(1),
		byID:    next,
		byScope: make(map[Scope][]PolicyID, len(AllScopes())),
		order:   make([]PolicyID, 0, len(next)),
		BuiltAt: r.clock.Now(),
	}

	for id := range next {
		snap.order = append(snap.order, id)
	}
	sort.Slice(snap.order, func(i, j int) bool {
		a, b := next[snap.order[i]], next[snap.order[j]]
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		return a.ID < b.ID
	})

	for _, id := range snap.order {
		s := next[id].Scope
		snap.byScope[s] = append(snap.byScope[s], id)
	}

	// The digest covers every policy's decision-relevant content, in snapshot
	// order. Two deployments with the same digest decide identically, which is
	// the question a change review actually asks.
	var buf []byte
	for _, id := range snap.order {
		buf = append(buf, next[id].canonicalBytes()...)
		buf = append(buf, ';')
	}
	snap.Digest = fingerprintOf(buf)

	r.snapshot.Store(snap)
	if r.metrics != nil {
		r.metrics.Policies.Set(float64(len(next)))
		r.metrics.SnapshotVersion.Set(float64(snap.Version))
	}
}

// Get returns a policy.
func (r *PolicyRegistry) Get(id PolicyID) (Policy, bool) { return r.snapshot.Load().Get(id) }

// All returns every policy in evaluation order.
func (r *PolicyRegistry) All() []Policy { return r.snapshot.Load().All() }

// Expired returns policies whose effective window has passed, sorted.
//
// Used by the scheduler's sweep. Expired policies are REPORTED rather than
// removed: a policy that lapsed is a fact an operator should see, and silently
// deleting it would make "why did this stop being enforced" unanswerable.
func (r *PolicyRegistry) Expired() []Policy {
	now := r.clock.Now()
	var out []Policy
	for _, p := range r.All() {
		if !p.EffectiveUntil.IsZero() && !now.Before(p.EffectiveUntil) {
			out = append(out, p)
		}
	}
	return out
}

// Coverage reports which action kinds have at least one policy that could
// match them.
//
// An operator-facing answer to "is anything actually governing memory writes?"
// A kind with no covering policy is denied by default, which is safe but
// usually means somebody has not finished writing the rules.
func (r *PolicyRegistry) Coverage() map[ActionKind]int {
	out := make(map[ActionKind]int, 5)
	for _, k := range []ActionKind{ActionConversation, ActionMemory, ActionTool,
		ActionNotification, ActionExternal} {
		out[k] = 0
	}
	for _, p := range r.All() {
		if !p.Enabled {
			continue
		}
		if len(p.Match.Kinds) == 0 {
			for k := range out {
				out[k]++
			}
			continue
		}
		for _, k := range p.Match.Kinds {
			out[k]++
		}
	}
	return out
}
