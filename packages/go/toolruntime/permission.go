package toolruntime

import (
	"fmt"
	"sort"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Permission is a named capability an actor must hold to invoke a tool.
//
// A string, not an enum, because the set is owned by Identity and this module
// must not become a second place where the platform's permission vocabulary is
// defined. Two definitions of "may this actor do that" is how a system ends up
// enforcing the less strict one.
type Permission string

// String renders the permission.
func (p Permission) String() string { return string(p) }

// Decision is the outcome of a permission check.
//
// It carries WHY, not just whether. A denial a caller cannot explain is a
// denial the caller reports to a person as "something went wrong", and the
// person then tries again, and the system denies it again.
type Decision struct {
	// Allowed is the outcome.
	Allowed bool
	// Reason is a short machine-readable code. It becomes a metric label and
	// an event field, so it is never free text.
	Reason string
	// Missing lists permissions the actor lacks, sorted. Populated on denial,
	// so a caller can request exactly what is needed rather than guessing.
	Missing []Permission
	// ConsentRef is the consent basis that satisfied the check, when one was
	// required. Recorded in the audit trail: "who allowed this" is the
	// question an audit exists to answer.
	ConsentRef string
	// Override names the emergency override that granted this, if any. Always
	// populated when an override was used, because an override that leaves no
	// trace is indistinguishable from a permission bug.
	Override string
	// EvaluatedAt is the decision instant on the runtime clock.
	EvaluatedAt time.Time
}

// Denied builds a denial.
func Denied(reason string, missing ...Permission) Decision {
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	return Decision{Allowed: false, Reason: reason, Missing: missing}
}

// Allowed builds an approval.
func Allowed(reason string) Decision { return Decision{Allowed: true, Reason: reason} }

// Error renders a denial as an error, or nil when allowed.
func (d Decision) Error() error {
	if d.Allowed {
		return nil
	}
	if d.Reason == "consent_required" {
		return fmt.Errorf("%w: %v", ErrConsentRequired, d.Missing)
	}
	return fmt.Errorf("%w: %s (missing %v)", ErrPermissionDenied, d.Reason, d.Missing)
}

// Grant is what an actor holds.
//
// Grants are supplied by the caller and evaluated here; they are never
// discovered, inferred or cached by this module. The runtime asking Identity
// "what may this actor do" on every execution would put an availability
// dependency on the request path; the caller passing what it already knows does
// not.
type Grant struct {
	// Actor is who holds it.
	Actor ActorID
	// Permissions granted.
	Permissions []Permission
	// Roles the actor holds. Expanded through the engine's role map.
	Roles []string
	// ConsentRefs are consent bases the actor has presented, keyed by the
	// consent name a contract requires.
	ConsentRefs map[string]string
	// ExpiresAt bounds the grant. Zero means no expiry.
	ExpiresAt time.Time
}

// PermissionEngine evaluates whether an execution may proceed.
//
// It knows how to combine permissions, roles, consent and overrides. It does
// NOT know what a "receptionist" may do — the role map is supplied. That
// separation is the difference between a policy engine and a policy.
type PermissionEngine struct {
	clock   rt.Clock
	metrics *Metrics

	mu        sync.RWMutex
	roles     map[string][]Permission
	overrides map[string]Override
	audit     Auditor
}

// Override is an emergency grant.
//
// Emergency overrides exist because systems that cannot be overridden get
// overridden anyway, by someone editing a database at 3 a.m. with no record of
// it. Making the path explicit means the override is bounded, attributed and
// logged.
type Override struct {
	// Name identifies it.
	Name string
	// Permissions it grants.
	Permissions []Permission
	// Actors it applies to. Empty means every actor, which is deliberately
	// possible and deliberately loud in the audit trail.
	Actors []ActorID
	// Tools it applies to. Empty means every tool.
	Tools []ToolID
	// ExpiresAt bounds it. REQUIRED: an override with no expiry is a permission
	// change wearing an emergency's clothes.
	ExpiresAt time.Time
	// AuthorisedBy names the human accountable.
	AuthorisedBy string
	// Reason is recorded verbatim in the audit trail.
	Reason string
}

func (o Override) validate() []string {
	var problems []string
	if o.Name == "" {
		problems = append(problems, "override: name is required")
	}
	if len(o.Permissions) == 0 {
		problems = append(problems, fmt.Sprintf("override %s: grants nothing", o.Name))
	}
	if o.ExpiresAt.IsZero() {
		problems = append(problems, fmt.Sprintf(
			"override %s: ExpiresAt is required; an override that never expires "+
				"is a permanent policy change with no review", o.Name))
	}
	if o.AuthorisedBy == "" {
		problems = append(problems, fmt.Sprintf(
			"override %s: AuthorisedBy is required; an anonymous override cannot "+
				"be reviewed afterwards", o.Name))
	}
	if o.Reason == "" {
		problems = append(problems, fmt.Sprintf("override %s: Reason is required", o.Name))
	}
	return problems
}

func (o Override) applies(actor ActorID, tool ToolID, now time.Time) bool {
	if !now.Before(o.ExpiresAt) {
		return false
	}
	if len(o.Actors) > 0 {
		found := false
		for _, a := range o.Actors {
			if a == actor {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(o.Tools) > 0 {
		found := false
		for _, t := range o.Tools {
			if t == tool {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// NewPermissionEngine builds an engine.
func NewPermissionEngine(clock rt.Clock, metrics *Metrics, audit Auditor) *PermissionEngine {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	return &PermissionEngine{
		clock:     clock,
		metrics:   metrics,
		roles:     make(map[string][]Permission),
		overrides: make(map[string]Override),
		audit:     audit,
	}
}

// DefineRole maps a role to permissions.
func (e *PermissionEngine) DefineRole(role string, perms ...Permission) error {
	if role == "" {
		return &ConfigError{Problems: []string{"role: name is required"}}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.roles[role] = append([]Permission(nil), perms...)
	return nil
}

// AddOverride installs an emergency override.
//
// Every installation is audited at the moment it is installed, not only when it
// is used. An override that is installed and never fires is still a change to
// the platform's security posture and still deserves a record.
func (e *PermissionEngine) AddOverride(o Override) error {
	if problems := o.validate(); len(problems) > 0 {
		return &ConfigError{Problems: problems}
	}
	e.mu.Lock()
	e.overrides[o.Name] = o
	e.mu.Unlock()

	if e.audit != nil {
		e.audit.Record(AuditEntry{
			At:      e.clock.Now(),
			Kind:    AuditOverrideInstalled,
			Actor:   ActorID(o.AuthorisedBy),
			Reason:  o.Reason,
			Details: map[string]string{"override": o.Name, "expires": o.ExpiresAt.UTC().Format(time.RFC3339)},
		})
	}
	return nil
}

// RemoveOverride withdraws an override.
func (e *PermissionEngine) RemoveOverride(name string) {
	e.mu.Lock()
	delete(e.overrides, name)
	e.mu.Unlock()
}

// ActiveOverrides returns overrides that have not expired, sorted by name.
func (e *PermissionEngine) ActiveOverrides() []Override {
	now := e.clock.Now()
	e.mu.RLock()
	out := make([]Override, 0, len(e.overrides))
	for _, o := range e.overrides {
		if now.Before(o.ExpiresAt) {
			out = append(out, o)
		}
	}
	e.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Evaluate decides whether an actor may invoke a tool under a contract.
//
// The order is deliberate and each step is cheaper than the next:
//
//  1. Grant expiry — a cheap comparison that invalidates everything else.
//  2. Consent — a contract requirement the actor either presented or did not.
//  3. Permissions, from the grant and from expanded roles.
//  4. Overrides, last, so an override is only ever consulted for something
//     that would otherwise be denied and therefore always appears in the audit
//     trail as having actually mattered.
func (e *PermissionEngine) Evaluate(c Contract, g Grant) Decision {
	now := e.clock.Now()

	if !g.ExpiresAt.IsZero() && !now.Before(g.ExpiresAt) {
		return e.record(c, g, Decision{Reason: "grant_expired", EvaluatedAt: now})
	}

	consentRef := ""
	if c.RequiresConsent != "" {
		ref, ok := g.ConsentRefs[c.RequiresConsent]
		if !ok || ref == "" {
			d := Denied("consent_required")
			d.EvaluatedAt = now
			return e.record(c, g, d)
		}
		consentRef = ref
	}

	held := make(map[Permission]bool, len(g.Permissions))
	for _, p := range g.Permissions {
		held[p] = true
	}
	e.mu.RLock()
	for _, role := range g.Roles {
		for _, p := range e.roles[role] {
			held[p] = true
		}
	}
	e.mu.RUnlock()

	var missing []Permission
	for _, need := range c.RequiredPermissions {
		if !held[need] {
			missing = append(missing, need)
		}
	}

	if len(missing) == 0 {
		d := Allowed("granted")
		d.ConsentRef, d.EvaluatedAt = consentRef, now
		return e.record(c, g, d)
	}

	// An override may close the gap — but only completely. A partial override
	// that satisfies two of three missing permissions must not allow the call,
	// because the third was a real requirement and nobody decided to waive it.
	if name, ok := e.overrideCovers(g.Actor, c.Descriptor.Tool, missing, now); ok {
		d := Allowed("override")
		d.ConsentRef, d.Override, d.EvaluatedAt = consentRef, name, now
		return e.record(c, g, d)
	}

	d := Denied("missing_permission", missing...)
	d.EvaluatedAt = now
	return e.record(c, g, d)
}

func (e *PermissionEngine) overrideCovers(actor ActorID, tool ToolID, missing []Permission, now time.Time) (string, bool) {
	e.mu.RLock()
	names := make([]string, 0, len(e.overrides))
	for name := range e.overrides {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic: the same override always wins
	defer e.mu.RUnlock()

	for _, name := range names {
		o := e.overrides[name]
		if !o.applies(actor, tool, now) {
			continue
		}
		granted := make(map[Permission]bool, len(o.Permissions))
		for _, p := range o.Permissions {
			granted[p] = true
		}
		covered := true
		for _, m := range missing {
			if !granted[m] {
				covered = false
				break
			}
		}
		if covered {
			return name, true
		}
	}
	return "", false
}

func (e *PermissionEngine) record(c Contract, g Grant, d Decision) Decision {
	if e.metrics != nil {
		if d.Allowed {
			e.metrics.PermissionAllowed.Inc(string(c.Descriptor.Tool), d.Reason)
		} else {
			e.metrics.PermissionDenied.Inc(string(c.Descriptor.Tool), d.Reason)
		}
	}

	// Only denials and overrides are audited. Auditing every allowed call would
	// bury both under routine traffic, and an audit log nobody can read is not
	// a control — the same reasoning the memory engine applies to Sensitive
	// reads.
	if e.audit != nil && (!d.Allowed || d.Override != "") {
		kind := AuditPermissionDenied
		if d.Allowed {
			kind = AuditOverrideUsed
		}
		details := map[string]string{
			"tool":    string(c.Descriptor.Tool),
			"version": string(c.Descriptor.Version),
		}
		if d.Override != "" {
			details["override"] = d.Override
		}
		if len(d.Missing) > 0 {
			names := make([]string, 0, len(d.Missing))
			for _, m := range d.Missing {
				names = append(names, string(m))
			}
			details["missing"] = joinSorted(names)
		}
		e.audit.Record(AuditEntry{
			At: d.EvaluatedAt, Kind: kind, Actor: g.Actor,
			Descriptor: c.Descriptor, Reason: d.Reason, Details: details,
		})
	}
	return d
}

func joinSorted(in []string) string {
	sort.Strings(in)
	out := ""
	for i, s := range in {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}
