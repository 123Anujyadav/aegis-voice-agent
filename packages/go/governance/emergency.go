package governance

import (
	"fmt"
	"sort"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Emergency is a bounded, attributed relaxation of policy during an incident.
//
// EMERGENCY OVERRIDES EXIST BECAUSE SYSTEMS THAT CANNOT BE OVERRIDDEN GET
// OVERRIDDEN ANYWAY — at 3 a.m., by somebody with database access, leaving no
// record. Making the path explicit does not weaken the platform; it moves an
// action that was going to happen anyway into somewhere it can be bounded,
// attributed and reviewed.
//
// The limits are structural rather than advisory:
//
//   - it expires, and the expiry is required at construction;
//   - it names a human, and the name is required;
//   - it states a reason, and the reason is required;
//   - it CANNOT touch [ScopeCompliance], and there is no field that would
//     let it.
//
// The last one is the important one. An emergency may relax an organisation's
// own rule. It may not relax a legal one, because "we had an incident" is not a
// defence a regulator accepts, and a mechanism that could be used that way
// would eventually be.
type Emergency struct {
	// Name identifies the activation. Becomes a metric label, so it should be
	// an incident identifier rather than a sentence.
	Name string

	// Policies are the emergency-scope policies this activation installs.
	// Registered when the emergency activates and withdrawn when it ends.
	Policies []Policy

	// Scopes lists which scopes this activation is permitted to relax.
	// Compliance is refused at validation; listing it is a configuration
	// error rather than a silently ignored entry, so an operator who tried
	// finds out.
	Scopes []Scope

	// ActivatedAt and ExpiresAt bound it. ExpiresAt is REQUIRED.
	ActivatedAt time.Time
	ExpiresAt   time.Time

	// AuthorisedBy names the accountable human.
	AuthorisedBy string

	// Reason is recorded verbatim in the audit trail. The one place in this
	// module where free text is not only permitted but wanted: an emergency
	// review reads reasons, and a bounded code cannot carry "carrier outage in
	// AP South, failing over".
	Reason string

	// Ticket links the incident record.
	Ticket string
}

func (e Emergency) validate() []string {
	var problems []string
	if e.Name == "" {
		problems = append(problems, "emergency: name is required")
	}
	if len(e.Policies) == 0 {
		problems = append(problems, fmt.Sprintf(
			"emergency %s: installs no policies and therefore does nothing", e.Name))
	}
	if e.ExpiresAt.IsZero() {
		problems = append(problems, fmt.Sprintf(
			"emergency %s: ExpiresAt is required; an emergency that never expires is "+
				"a permanent policy change with no review", e.Name))
	}
	if e.AuthorisedBy == "" {
		problems = append(problems, fmt.Sprintf(
			"emergency %s: AuthorisedBy is required; an anonymous emergency cannot be "+
				"reviewed afterwards", e.Name))
	}
	if e.Reason == "" {
		problems = append(problems, fmt.Sprintf("emergency %s: reason is required", e.Name))
	}
	for _, s := range e.Scopes {
		if !s.Overridable() {
			problems = append(problems, fmt.Sprintf(
				"emergency %s: lists the %s scope, which no emergency may relax; a "+
					"legal rule is changed by changing the policy, not by declaring an "+
					"incident", e.Name, s))
		}
	}
	for _, p := range e.Policies {
		if p.Scope != ScopeEmergency {
			problems = append(problems, fmt.Sprintf(
				"emergency %s: policy %s is in the %s scope; an emergency installs "+
					"emergency-scope policies only", e.Name, p.ID, p.Scope))
		}
		problems = append(problems, p.validate()...)
	}
	return problems
}

// Active reports whether the emergency is in force at an instant.
func (e Emergency) Active(now time.Time) bool {
	if e.ExpiresAt.IsZero() {
		return false
	}
	if !e.ActivatedAt.IsZero() && now.Before(e.ActivatedAt) {
		return false
	}
	return now.Before(e.ExpiresAt)
}

// Remaining returns how long the emergency has left, or zero when it has ended.
func (e Emergency) Remaining(now time.Time) time.Duration {
	if !e.Active(now) {
		return 0
	}
	return e.ExpiresAt.Sub(now)
}

// EmergencyEngine activates and expires emergencies.
//
// It owns the coupling between an activation and the policy registry: an
// emergency IS its policies, and activating one registers them. That means an
// emergency cannot take effect through some side channel the policy trace does
// not see — the relaxation appears in the trace as an emergency-scope policy,
// like any other policy.
type EmergencyEngine struct {
	clock    rt.Clock
	metrics  *Metrics
	audit    Auditor
	registry *PolicyRegistry

	mu     sync.RWMutex
	active map[string]Emergency
	// history retains ended activations, because "what did we relax, when, and
	// who said so" is the question an incident review asks and a deleted
	// record cannot answer.
	history []Emergency
	maxHist int
}

// NewEmergencyEngine builds an engine over a policy registry.
func NewEmergencyEngine(clock rt.Clock, m *Metrics, a Auditor, r *PolicyRegistry) *EmergencyEngine {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	return &EmergencyEngine{
		clock: clock, metrics: m, audit: a, registry: r,
		active: make(map[string]Emergency), maxHist: 256,
	}
}

// Activate installs an emergency and its policies.
//
// Audited at ACTIVATION, not only at use. An emergency that is declared and
// never fires is still a change to the platform's safety posture and still
// deserves a record — and the gap between "we declared six emergencies" and "we
// used one" is itself a useful signal about how emergencies are being used.
func (e *EmergencyEngine) Activate(em Emergency) error {
	if problems := em.validate(); len(problems) > 0 {
		sort.Strings(problems)
		return &ConfigError{Problems: problems}
	}

	now := e.clock.Now()
	if em.ActivatedAt.IsZero() {
		em.ActivatedAt = now
	}
	if !em.Active(now) {
		return &ConfigError{Problems: []string{fmt.Sprintf(
			"emergency %s: expires at %s, which is not in the future",
			em.Name, em.ExpiresAt.UTC().Format(time.RFC3339))}}
	}

	e.mu.Lock()
	if _, exists := e.active[em.Name]; exists {
		e.mu.Unlock()
		return &ConfigError{Problems: []string{fmt.Sprintf(
			"emergency %s: already active; extend it by deactivating and "+
				"reactivating, so the extension is a separate audited decision", em.Name)}}
	}
	e.active[em.Name] = em
	count := len(e.active)
	e.mu.Unlock()

	if err := e.registry.RegisterAll(em.Policies...); err != nil {
		e.mu.Lock()
		delete(e.active, em.Name)
		e.mu.Unlock()
		return fmt.Errorf("emergency %s: %w", em.Name, err)
	}

	if e.metrics != nil {
		e.metrics.EmergencyActivations.Inc(em.Name)
		e.metrics.EmergencyActive.Set(float64(count))
	}
	e.record(AuditEmergencyActivated, em, "activated")
	return nil
}

// Deactivate ends an emergency and disables its policies.
//
// Policies are DISABLED rather than unregistered, so an audit record naming one
// still resolves to the rules that were in force. The same argument the policy
// registry makes for disabling rather than deleting.
func (e *EmergencyEngine) Deactivate(name, reason string) error {
	e.mu.Lock()
	em, ok := e.active[name]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("%w: emergency %s", ErrNotRegistered, name)
	}
	delete(e.active, name)
	e.history = append(e.history, em)
	if len(e.history) > e.maxHist {
		e.history = e.history[1:]
	}
	count := len(e.active)
	e.mu.Unlock()

	for _, p := range em.Policies {
		_ = e.registry.SetEnabled(p.ID, false)
	}

	if e.metrics != nil {
		e.metrics.EmergencyActive.Set(float64(count))
	}
	e.record(AuditEmergencyExpired, em, reason)
	return nil
}

// Sweep deactivates expired emergencies and returns how many ended.
//
// The safety net behind the required expiry: an emergency that nobody
// remembered to end ends anyway. That is the entire reason ExpiresAt is
// mandatory rather than optional.
func (e *EmergencyEngine) Sweep() int {
	now := e.clock.Now()

	e.mu.RLock()
	var expired []string
	for name, em := range e.active {
		if !em.Active(now) {
			expired = append(expired, name)
		}
	}
	e.mu.RUnlock()

	sort.Strings(expired) // deterministic audit order
	for _, name := range expired {
		_ = e.Deactivate(name, "expired")
	}
	return len(expired)
}

// Active returns the emergencies in force, sorted by name.
func (e *EmergencyEngine) Active() []Emergency {
	now := e.clock.Now()
	e.mu.RLock()
	out := make([]Emergency, 0, len(e.active))
	for _, em := range e.active {
		if em.Active(now) {
			out = append(out, em)
		}
	}
	e.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// History returns ended activations, oldest first.
func (e *EmergencyEngine) History() []Emergency {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]Emergency(nil), e.history...)
}

// NoteUse records that an emergency policy influenced a decision.
//
// Called by the engine when a decision was decided in the emergency scope. The
// distinction between "declared" and "actually used" is what an incident review
// needs: an emergency declared and never used is a false alarm, and one used a
// thousand times is a policy gap wearing an incident's clothes.
func (e *EmergencyEngine) NoteUse(name string, d Decision) {
	if name == "" {
		return
	}
	if e.metrics != nil {
		e.metrics.EmergencyUses.Inc(name)
	}
	if e.audit == nil {
		return
	}
	_ = e.audit.Record(AuditEntry{
		At: e.clock.Now(), Kind: AuditEmergencyUsed, Decision: d.ID,
		Correlation: d.Correlation, Actor: d.Actor, Subject: d.Subject,
		Policy: d.DecidedBy, Scope: d.Scope, Outcome: d.Outcome,
		Reason: d.Reason, ActionLabel: d.ActionLabel,
		Details: map[string]string{"emergency": name},
	})
}

// ActiveNameFor returns the emergency that owns a policy, or empty.
//
// Used to attribute a decision made in the emergency scope back to the incident
// that installed the rule, so a trace says "emergency inc-4471" rather than
// only naming a policy identifier nobody recognises.
func (e *EmergencyEngine) ActiveNameFor(p PolicyID) string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for name, em := range e.active {
		for _, policy := range em.Policies {
			if policy.ID == p {
				return name
			}
		}
	}
	return ""
}

func (e *EmergencyEngine) record(kind AuditKind, em Emergency, reason string) {
	if e.audit == nil {
		return
	}
	_ = e.audit.Record(AuditEntry{
		At: e.clock.Now(), Kind: kind, Actor: ActorID(em.AuthorisedBy),
		Reason: reason,
		Details: map[string]string{
			"emergency": em.Name,
			"expires":   em.ExpiresAt.UTC().Format(time.RFC3339),
			"ticket":    em.Ticket,
			"detail":    em.Reason,
			"policies":  fmt.Sprint(len(em.Policies)),
		},
	})
}
