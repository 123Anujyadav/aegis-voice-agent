package toolruntime

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Lifecycle is a registration's stage.
//
// Five stages, because a tool registry with only "registered" and "not
// registered" forces every rollout and every deprecation to be a hard cutover.
// Draining in particular is the difference between a graceful version
// retirement and a wave of failed executions.
type Lifecycle uint8

// The lifecycle stages.
const (
	// LifecyclePending is registered but not yet serving. Discovery does not
	// return it. Used to stage a version before traffic reaches it.
	LifecyclePending Lifecycle = iota

	// LifecycleActive is serving normally.
	LifecycleActive

	// LifecycleDeprecated still serves but is not preferred. Discovery picks
	// it only when nothing active satisfies the request, so a deprecated
	// version keeps working while callers migrate.
	LifecycleDeprecated

	// LifecycleDraining serves executions already planned against it and
	// accepts no new ones. THIS IS THE STAGE THAT MAKES RETIREMENT SAFE: a
	// plan pinned to a version must be able to finish even after somebody has
	// decided that version is going away.
	LifecycleDraining

	// LifecycleRetired serves nothing. Retained in the registry so that an
	// audit record naming the version still resolves to a contract.
	LifecycleRetired
)

// String renders the stage.
func (l Lifecycle) String() string {
	switch l {
	case LifecycleActive:
		return "active"
	case LifecycleDeprecated:
		return "deprecated"
	case LifecycleDraining:
		return "draining"
	case LifecycleRetired:
		return "retired"
	default:
		return "pending"
	}
}

// Dispatchable reports whether new executions may be planned against it.
func (l Lifecycle) Dispatchable() bool {
	return l == LifecycleActive || l == LifecycleDeprecated
}

// lifecycleTransitions declares every legal stage change.
//
// One table, validated at construction by the frozen Phase 10A FSM, rather than
// a scattering of if-statements. The two ABSENT edges carry the most weight:
//
//	Retired → Active   does not exist. Reviving a retired version means an
//	                   audit record's "retired at" timestamp is a lie. Register
//	                   a new version instead; that is what versions are for.
//
//	Draining → Active  does not exist. A drain is a decision somebody made with
//	                   a reason; un-deciding it silently would make "is this
//	                   version going away" unanswerable.
func lifecycleTransitions() map[Lifecycle][]Lifecycle {
	return map[Lifecycle][]Lifecycle{
		LifecyclePending:    {LifecycleActive, LifecycleRetired},
		LifecycleActive:     {LifecycleDeprecated, LifecycleDraining, LifecycleRetired},
		LifecycleDeprecated: {LifecycleActive, LifecycleDraining, LifecycleRetired},
		LifecycleDraining:   {LifecycleRetired},
		LifecycleRetired:    {},
	}
}

// canTransition reports whether a stage change is declared.
//
// A pure function over the table rather than an FSM instance per registration.
// Ten thousand registrations would otherwise mean ten thousand state machines
// holding ten thousand copies of one immutable table.
func canTransition(from, to Lifecycle) bool {
	for _, allowed := range lifecycleTransitions()[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// Health is a tool's current usability.
type Health uint8

// The health states.
const (
	// HealthUnknown is the state before any probe or execution. Treated as
	// usable: refusing to dispatch until a probe has run would make every cold
	// start an outage.
	HealthUnknown Health = iota
	HealthHealthy
	// HealthDegraded still serves. Discovery ranks it below healthy tools but
	// above nothing, because a slow calendar beats no calendar.
	HealthDegraded
	HealthUnhealthy
)

// String renders the health.
func (h Health) String() string {
	switch h {
	case HealthHealthy:
		return "healthy"
	case HealthDegraded:
		return "degraded"
	case HealthUnhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}

// Usable reports whether discovery may return a tool in this state.
func (h Health) Usable() bool { return h != HealthUnhealthy }

// Registration is one tool at one version, with everything the runtime knows
// about it.
type Registration struct {
	// Contract fully describes the tool.
	Contract Contract
	// Tool is the implementation. Nil is legal and means "contract only" —
	// useful for planning against a tool served by another process, and used
	// by every plan-shape test in this module.
	Tool Tool
	// Lifecycle is the registration's stage.
	Lifecycle Lifecycle
	// Health is the current usability.
	Health Health
	// Priority breaks ties between tools providing the same capability.
	// Higher wins. Equal priorities fall back to version, then to tool ID, so
	// selection is always total and always deterministic.
	Priority int
	// RegisteredAt and UpdatedAt are runtime-clock instants.
	RegisteredAt time.Time
	UpdatedAt    time.Time
	// LastHealthChange records when Health last moved, for flap detection.
	LastHealthChange time.Time
	// Executions and Failures count lifetime outcomes for this registration.
	Executions uint64
	Failures   uint64
}

// Descriptor returns the tool and version.
func (r Registration) Descriptor() Descriptor { return r.Contract.Descriptor }

// clone returns an independent copy.
//
// The registry hands out clones for the same reason the memory engine does:
// returning a pointer into a shared structure is returning a promise the lock
// cannot keep, and a caller that mutates a registration would change what every
// concurrent planner sees.
func (r Registration) clone() Registration {
	c := r
	c.Contract.Capabilities = append([]CapabilityID(nil), r.Contract.Capabilities...)
	c.Contract.Input = append([]FieldSpec(nil), r.Contract.Input...)
	c.Contract.Output = append([]FieldSpec(nil), r.Contract.Output...)
	c.Contract.RequiredPermissions = append([]Permission(nil), r.Contract.RequiredPermissions...)
	if r.Contract.Tags != nil {
		tags := make(map[string]string, len(r.Contract.Tags))
		for k, v := range r.Contract.Tags {
			tags[k] = v
		}
		c.Contract.Tags = tags
	}
	return c
}

// Registry holds every known tool.
//
// COPY-ON-WRITE. Reads take no lock at all: they load an immutable snapshot
// pointer and walk it. Writes build a new snapshot and swap the pointer.
//
// That trade is right for this structure because the read:write ratio is
// enormous — every execution reads the registry, and registrations change on
// deploy. It also gives a property that matters more than the speed: a plan
// resolved against one snapshot cannot have the tool changed underneath it
// mid-execution, because the snapshot it read is immutable and still alive as
// long as it holds a reference. Registry churn during a rollout therefore
// cannot corrupt an in-flight execution (INV-TOOL-9).
type Registry struct {
	clock   rt.Clock
	metrics *Metrics
	audit   Auditor

	snapshot atomic.Pointer[registrySnapshot]
	writeMu  sync.Mutex

	maxTimeout time.Duration
}

// registrySnapshot is an immutable view of every registration.
type registrySnapshot struct {
	// byDescriptor is the primary index.
	byDescriptor map[Descriptor]Registration
	// byCapability lists descriptors per capability, pre-sorted by preference
	// so discovery does no sorting on the read path.
	byCapability map[CapabilityID][]Descriptor
	// order is every descriptor, sorted, for deterministic enumeration.
	order []Descriptor
}

// NewRegistry builds an empty registry.
func NewRegistry(clock rt.Clock, metrics *Metrics, audit Auditor, maxTimeout time.Duration) *Registry {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	r := &Registry{clock: clock, metrics: metrics, audit: audit, maxTimeout: maxTimeout}
	r.snapshot.Store(&registrySnapshot{
		byDescriptor: map[Descriptor]Registration{},
		byCapability: map[CapabilityID][]Descriptor{},
	})
	return r
}

// Register adds or replaces a registration.
//
// The contract is validated here and never again. Every downstream stage —
// planning, permission, validation, audit — assumes a registered contract is
// well-formed, which is only safe because nothing enters the registry without
// passing this.
func (r *Registry) Register(reg Registration) error {
	if problems := reg.Contract.validate(r.maxTimeout); len(problems) > 0 {
		return &ConfigError{Problems: problems}
	}
	if reg.Contract.Compensable {
		if _, ok := reg.Tool.(CompensatingTool); reg.Tool != nil && !ok {
			return &ConfigError{Problems: []string{fmt.Sprintf(
				"%s: contract declares Compensable but the implementation does not "+
					"implement CompensatingTool; a plan would promise a rollback that "+
					"cannot run", reg.Contract.Descriptor)}}
		}
	}
	if reg.Contract.Streaming {
		if _, ok := reg.Tool.(StreamingTool); reg.Tool != nil && !ok {
			return &ConfigError{Problems: []string{fmt.Sprintf(
				"%s: contract declares Streaming but the implementation does not "+
					"implement StreamingTool", reg.Contract.Descriptor)}}
		}
	}

	now := r.clock.Now()
	reg.UpdatedAt = now

	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	old := r.snapshot.Load()
	if prev, exists := old.byDescriptor[reg.Descriptor()]; exists {
		// Re-registering preserves counters and the original registration
		// instant. A redeploy is not a new tool, and resetting its failure
		// count on every rollout would hide a tool that fails on every deploy.
		reg.RegisteredAt = prev.RegisteredAt
		reg.Executions, reg.Failures = prev.Executions, prev.Failures
		if reg.Health == HealthUnknown {
			reg.Health = prev.Health
		}
		reg.LastHealthChange = prev.LastHealthChange
	} else {
		reg.RegisteredAt = now
		reg.LastHealthChange = now
	}

	r.commit(old, func(next map[Descriptor]Registration) {
		next[reg.Descriptor()] = reg
	})

	if r.audit != nil {
		_ = r.audit.Record(AuditEntry{At: now, Kind: AuditRegistered,
			Descriptor: reg.Descriptor(), Reason: reg.Lifecycle.String(),
			Details: map[string]string{"owner": reg.Contract.Owner}})
	}
	return nil
}

// Unregister removes a registration entirely.
//
// Distinct from retiring it. Retirement keeps the contract resolvable so an old
// audit record still means something; unregistration is for a tool that should
// never have been there. Use retirement for a version you are done with.
func (r *Registry) Unregister(d Descriptor) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	old := r.snapshot.Load()
	if _, ok := old.byDescriptor[d]; !ok {
		return fmt.Errorf("%w: %s", ErrNotRegistered, d)
	}
	r.commit(old, func(next map[Descriptor]Registration) {
		delete(next, d)
	})

	if r.audit != nil {
		_ = r.audit.Record(AuditEntry{At: r.clock.Now(), Kind: AuditUnregistered, Descriptor: d})
	}
	return nil
}

// SetLifecycle moves a registration to a new stage.
func (r *Registry) SetLifecycle(d Descriptor, to Lifecycle) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	old := r.snapshot.Load()
	reg, ok := old.byDescriptor[d]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotRegistered, d)
	}
	if reg.Lifecycle == to {
		return nil
	}
	if !canTransition(reg.Lifecycle, to) {
		return invariant("INV-TOOL-9", "%s cannot move from %s to %s",
			d, reg.Lifecycle, to)
	}
	reg.Lifecycle = to
	reg.UpdatedAt = r.clock.Now()

	r.commit(old, func(next map[Descriptor]Registration) { next[d] = reg })
	return nil
}

// SetHealth updates a registration's health.
//
// Called by a health prober, and by the executor after an outcome. Both are
// legitimate: a probe knows whether the tool answers, and an execution knows
// whether it answered correctly, which is a different and more useful fact.
func (r *Registry) SetHealth(d Descriptor, h Health) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	old := r.snapshot.Load()
	reg, ok := old.byDescriptor[d]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotRegistered, d)
	}
	if reg.Health == h {
		return nil
	}
	now := r.clock.Now()
	reg.Health, reg.LastHealthChange, reg.UpdatedAt = h, now, now

	r.commit(old, func(next map[Descriptor]Registration) { next[d] = reg })
	return nil
}

// RecordOutcome updates a registration's lifetime counters.
func (r *Registry) RecordOutcome(d Descriptor, ok bool) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	old := r.snapshot.Load()
	reg, exists := old.byDescriptor[d]
	if !exists {
		return
	}
	reg.Executions++
	if !ok {
		reg.Failures++
	}
	r.commit(old, func(next map[Descriptor]Registration) { next[d] = reg })
}

// commit rebuilds the snapshot and swaps it in. Caller holds writeMu.
func (r *Registry) commit(old *registrySnapshot, mutate func(map[Descriptor]Registration)) {
	next := make(map[Descriptor]Registration, len(old.byDescriptor)+1)
	for k, v := range old.byDescriptor {
		next[k] = v
	}
	mutate(next)

	snap := &registrySnapshot{
		byDescriptor: next,
		byCapability: make(map[CapabilityID][]Descriptor),
		order:        make([]Descriptor, 0, len(next)),
	}
	for d := range next {
		snap.order = append(snap.order, d)
	}
	sortDescriptors(snap.order)

	for _, d := range snap.order {
		for _, cap := range next[d].Contract.Capabilities {
			snap.byCapability[cap] = append(snap.byCapability[cap], d)
		}
	}
	// Pre-sort each capability's candidates by preference, so the read path
	// never sorts. Discovery runs on every execution; registration does not.
	for cap, list := range snap.byCapability {
		regs := next
		sort.SliceStable(list, func(i, j int) bool {
			return preferOver(regs[list[i]], regs[list[j]])
		})
		snap.byCapability[cap] = list
	}

	r.snapshot.Store(snap)
	if r.metrics != nil {
		r.metrics.RegistryVersions.Set(float64(len(next)))
	}
}

// preferOver is the total ordering discovery uses.
//
// Every tier is a deliberate choice and the last one exists purely so the
// ordering is TOTAL. Two equally good candidates must still order consistently,
// or the same intent resolves differently on different runs and a fallback test
// passes on a Tuesday.
func preferOver(a, b Registration) bool {
	if ah, bh := healthRank(a.Health), healthRank(b.Health); ah != bh {
		return ah < bh
	}
	if al, bl := lifecycleRank(a.Lifecycle), lifecycleRank(b.Lifecycle); al != bl {
		return al < bl
	}
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	if c := b.Contract.Descriptor.Version.Compare(a.Contract.Descriptor.Version); c != 0 {
		return c < 0 // higher version first
	}
	return a.Contract.Descriptor.Tool < b.Contract.Descriptor.Tool
}

func healthRank(h Health) int {
	switch h {
	case HealthHealthy:
		return 0
	case HealthUnknown:
		return 1
	case HealthDegraded:
		return 2
	default:
		return 3
	}
}

func lifecycleRank(l Lifecycle) int {
	switch l {
	case LifecycleActive:
		return 0
	case LifecycleDeprecated:
		return 1
	default:
		return 2
	}
}

func sortDescriptors(in []Descriptor) {
	sort.Slice(in, func(i, j int) bool {
		if in[i].Tool != in[j].Tool {
			return in[i].Tool < in[j].Tool
		}
		return in[i].Version.Compare(in[j].Version) < 0
	})
}

// Get returns a registration by descriptor.
func (r *Registry) Get(d Descriptor) (Registration, bool) {
	snap := r.snapshot.Load()
	reg, ok := snap.byDescriptor[d]
	if !ok {
		return Registration{}, false
	}
	return reg.clone(), true
}

// All returns every registration, sorted by tool then version.
func (r *Registry) All() []Registration {
	snap := r.snapshot.Load()
	out := make([]Registration, 0, len(snap.order))
	for _, d := range snap.order {
		out = append(out, snap.byDescriptor[d].clone())
	}
	return out
}

// Capabilities returns every capability any registered tool provides, sorted.
func (r *Registry) Capabilities() []CapabilityID {
	snap := r.snapshot.Load()
	out := make([]CapabilityID, 0, len(snap.byCapability))
	for c := range snap.byCapability {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Len returns the registration count.
func (r *Registry) Len() int { return len(r.snapshot.Load().byDescriptor) }

// Owners returns the owning team for each tool, sorted by tool.
//
// Exists because the first question during an incident is "whose is this", and
// answering it should not require reading a deployment manifest.
func (r *Registry) Owners() map[ToolID]string {
	snap := r.snapshot.Load()
	out := make(map[ToolID]string, len(snap.byDescriptor))
	for d, reg := range snap.byDescriptor {
		out[d.Tool] = reg.Contract.Owner
	}
	return out
}
