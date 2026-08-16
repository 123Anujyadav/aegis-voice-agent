package toolruntime

import (
	"fmt"
	"sort"
)

// Candidate is one tool that could serve a capability request.
type Candidate struct {
	// Registration is the tool.
	Registration Registration
	// Rank is 0 for the preferred candidate, 1 for the first fallback, and so
	// on. Carried so an audit record can say "we used the second choice",
	// which is a fact an operator wants without having to re-derive the
	// ordering from a registry snapshot that has since changed.
	Rank int
}

// Descriptor returns the candidate's descriptor.
func (c Candidate) Descriptor() Descriptor { return c.Registration.Descriptor() }

// Discovery resolves a capability request to an ordered candidate list.
//
// It resolves to a LIST, not a winner. A planner needs the fallbacks to build a
// fallback step, and a resolver that returned only the best candidate would
// force the planner to ask again — against a registry that may have changed
// between the two calls, producing a fallback chain that was never
// simultaneously true.
type Discovery struct {
	registry *Registry
	metrics  *Metrics
}

// NewDiscovery builds a resolver over a registry.
func NewDiscovery(r *Registry, m *Metrics) *Discovery {
	return &Discovery{registry: r, metrics: m}
}

// Request is what discovery is asked to satisfy.
type Request struct {
	// Capability is what is needed. Required.
	Capability CapabilityID
	// Version constrains which versions may serve it.
	Version VersionConstraint
	// PreferTool nudges selection towards one implementation without requiring
	// it. Used to keep a multi-step plan on one implementation where mixing
	// would be surprising, while still allowing a fallback.
	PreferTool ToolID
	// RequireHealthy refuses degraded and unknown-health tools. Off by
	// default: refusing unknown health means every cold start is an outage,
	// and refusing degraded means a slow tool is treated as no tool.
	RequireHealthy bool
	// MaxCandidates bounds the returned list. Zero means the discovery
	// default; a fallback chain of thirty is not a resilience strategy.
	MaxCandidates int
	// ExcludeTools omits implementations, used when a fallback re-resolves
	// after a failure and must not choose what just failed.
	ExcludeTools []ToolID
}

func (r Request) validate() []string {
	var problems []string
	if r.Capability == "" {
		problems = append(problems, "discovery: capability is required")
	}
	if r.Version.Exact != "" && !r.Version.Exact.Valid() {
		problems = append(problems, fmt.Sprintf(
			"discovery: version %q is not MAJOR.MINOR.PATCH", r.Version.Exact))
	}
	if r.MaxCandidates < 0 {
		problems = append(problems, "discovery: MaxCandidates must not be negative")
	}
	return problems
}

// DefaultMaxCandidates bounds a fallback chain.
//
// Three: the preferred tool, one fallback, and one more for the case where the
// fallback is itself mid-deploy. A longer chain does not add resilience — by
// the fourth attempt the conversation has been waiting long enough that failing
// honestly beats succeeding late.
const DefaultMaxCandidates = 3

// Resolve returns candidates in preference order.
//
// The distinction between the two failure modes is the point of this function:
//
//	ErrNoCapability      nothing in the registry claims to do this — a
//	                     DEPLOYMENT gap, fixed by shipping something.
//	ErrNoHealthyProvider tools exist and none is currently usable — an
//	                     OUTAGE, fixed by fixing something.
//
// Collapsing them into one error means an on-call engineer reads "tool not
// available" and cannot tell whether to page the deploy owner or the
// integration owner.
func (d *Discovery) Resolve(req Request) ([]Candidate, error) {
	if problems := req.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}

	snap := d.registry.snapshot.Load()
	descriptors := snap.byCapability[req.Capability]
	if len(descriptors) == 0 {
		d.count(req.Capability, "no_capability")
		if d.metrics != nil {
			d.metrics.NoHealthyTool.Inc(string(req.Capability))
		}
		return nil, fmt.Errorf("%w: %s", ErrNoCapability, req.Capability)
	}

	excluded := make(map[ToolID]bool, len(req.ExcludeTools))
	for _, t := range req.ExcludeTools {
		excluded[t] = true
	}

	// FILTER ON DESCRIPTORS, MATERIALISE ONLY THE SURVIVORS.
	//
	// The first version appended a full Registration per match and cloned every
	// one of them, then discarded all but the first three — 66 KB of garbage to
	// answer a question about three tools. A descriptor is two strings; a
	// Registration carries a whole contract. See ENGINEERING_AUDIT F5, and note
	// that this is the same mistake Phase 10C made in its retrieval path, which
	// is an argument for looking at allocation counts rather than only at
	// wall-clock time.
	var (
		versionMatched bool
		kept           []Descriptor
	)
	for _, desc := range descriptors {
		if !req.Version.Satisfies(desc.Version) {
			continue
		}
		versionMatched = true

		if excluded[desc.Tool] {
			continue
		}
		reg := snap.byDescriptor[desc]
		if !reg.Lifecycle.Dispatchable() || !reg.Health.Usable() {
			continue
		}
		if req.RequireHealthy && reg.Health != HealthHealthy {
			continue
		}
		kept = append(kept, desc)
	}

	if !versionMatched {
		d.count(req.Capability, "version_unsatisfiable")
		return nil, fmt.Errorf("%w: %s %s", ErrVersionUnsatisfiable, req.Capability, req.Version)
	}
	if len(kept) == 0 {
		d.count(req.Capability, "no_healthy")
		if d.metrics != nil {
			d.metrics.NoHealthyTool.Inc(string(req.Capability))
		}
		return nil, fmt.Errorf("%w: %s", ErrNoHealthyProvider, req.Capability)
	}

	// The registry pre-sorted by preference. PreferTool is applied here rather
	// than in the registry because it is a property of one request, not of the
	// registry — baking it into the snapshot would mean re-sorting per caller.
	if req.PreferTool != "" {
		sort.SliceStable(kept, func(i, j int) bool {
			return kept[i].Tool == req.PreferTool && kept[j].Tool != req.PreferTool
		})
	}

	max := req.MaxCandidates
	if max <= 0 {
		max = DefaultMaxCandidates
	}
	if len(kept) > max {
		kept = kept[:max]
	}

	out := make([]Candidate, 0, len(kept))
	for i, desc := range kept {
		out = append(out, Candidate{Registration: snap.byDescriptor[desc].clone(), Rank: i})
	}

	d.count(req.Capability, "resolved")
	return out, nil
}

// ResolveOne returns the single preferred candidate.
func (d *Discovery) ResolveOne(req Request) (Candidate, error) {
	req.MaxCandidates = 1
	out, err := d.Resolve(req)
	if err != nil {
		return Candidate{}, err
	}
	return out[0], nil
}

func (d *Discovery) count(c CapabilityID, outcome string) {
	if d.metrics != nil {
		d.metrics.Resolutions.Inc(string(c), outcome)
	}
}

// CapabilityReport describes what a capability can currently be served by.
//
// Built for operators. "Why did that intent fail" is answered far faster by a
// list showing three registrations, two retired and one unhealthy, than by
// reading a resolution error.
type CapabilityReport struct {
	Capability CapabilityID
	// Total is every registration providing it, regardless of state.
	Total int
	// Dispatchable is how many are active or deprecated AND usable.
	Dispatchable int
	// Healthy is how many report healthy.
	Healthy int
	// Entries lists each registration's descriptor and state, sorted by
	// preference — the same order Resolve would use.
	Entries []CapabilityEntry
}

// CapabilityEntry is one registration's line in a report.
type CapabilityEntry struct {
	Descriptor Descriptor
	Lifecycle  Lifecycle
	Health     Health
	Priority   int
	Owner      string
	Executions uint64
	Failures   uint64
}

// Report describes every capability, sorted by capability name.
func (d *Discovery) Report() []CapabilityReport {
	snap := d.registry.snapshot.Load()
	caps := make([]CapabilityID, 0, len(snap.byCapability))
	for c := range snap.byCapability {
		caps = append(caps, c)
	}
	sort.Slice(caps, func(i, j int) bool { return caps[i] < caps[j] })

	out := make([]CapabilityReport, 0, len(caps))
	for _, c := range caps {
		rep := CapabilityReport{Capability: c}
		for _, desc := range snap.byCapability[c] {
			reg := snap.byDescriptor[desc]
			rep.Total++
			if reg.Lifecycle.Dispatchable() && reg.Health.Usable() {
				rep.Dispatchable++
			}
			if reg.Health == HealthHealthy {
				rep.Healthy++
			}
			rep.Entries = append(rep.Entries, CapabilityEntry{
				Descriptor: desc, Lifecycle: reg.Lifecycle, Health: reg.Health,
				Priority: reg.Priority, Owner: reg.Contract.Owner,
				Executions: reg.Executions, Failures: reg.Failures,
			})
		}
		out = append(out, rep)
	}
	return out
}
