package governance

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Scope is where a policy sits in the precedence order.
//
// NINE SCOPES, IN A FIXED ORDER, AND THE ORDER IS THE ENGINE'S MOST
// CONSEQUENTIAL CONSTANT. Every position is an answer to "if these two
// disagree, which should the platform obey?"
type Scope uint8

// The scopes, in DESCENDING precedence. Lower numeric value wins.
const (
	// ScopeCompliance is legal and regulatory. HIGHEST, and the only scope no
	// emergency override can touch. A deployment that needs to relax one needs
	// a lawyer and a policy change, not a button an on-call engineer presses
	// at 3 a.m.
	ScopeCompliance Scope = iota

	// ScopeEmergency is an active, bounded, attributed incident override. It
	// may relax an organisation's own rules; it may not relax a legal one.
	ScopeEmergency

	// ScopeGlobal is the platform safety floor — rules that hold for every
	// tenant. Above organisation, so a tenant cannot configure below the
	// floor.
	ScopeGlobal

	// ScopeOrganization is a tenant's own rules.
	ScopeOrganization

	// ScopeBusiness is one business within a tenant.
	ScopeBusiness

	// ScopeUser is one subscriber's preferences expressed as policy.
	ScopeUser

	// ScopeSession is scoped to one conversation. Used for "the caller asked
	// us not to record this call".
	ScopeSession

	// ScopeTemporary is time-boxed: a migration, a trial, a rollback window.
	// Below session so a temporary rule cannot quietly override what a person
	// asked for during a call.
	ScopeTemporary

	// ScopeFeatureFlag is the lowest. A flag may restrict; it can never be the
	// reason something dangerous was permitted, because anything above it that
	// says deny wins.
	ScopeFeatureFlag
)

// String renders the scope. Used as a metric label and in traces.
func (s Scope) String() string {
	switch s {
	case ScopeCompliance:
		return "compliance"
	case ScopeEmergency:
		return "emergency"
	case ScopeGlobal:
		return "global"
	case ScopeOrganization:
		return "organization"
	case ScopeBusiness:
		return "business"
	case ScopeUser:
		return "user"
	case ScopeSession:
		return "session"
	case ScopeTemporary:
		return "temporary"
	default:
		return "feature_flag"
	}
}

// AllScopes returns every scope in precedence order.
func AllScopes() []Scope {
	return []Scope{ScopeCompliance, ScopeEmergency, ScopeGlobal, ScopeOrganization,
		ScopeBusiness, ScopeUser, ScopeSession, ScopeTemporary, ScopeFeatureFlag}
}

// Overridable reports whether an emergency override may relax this scope.
//
// Everything except compliance. This single method is the whole of the
// emergency engine's limit, and it is a method rather than a configuration
// field on purpose: a configurable "can compliance be overridden" flag is a
// flag that gets set to true during an incident.
func (s Scope) Overridable() bool { return s != ScopeCompliance }

// Selector is a comparison operator in a [Condition].
type Selector uint8

// The selectors. Nine, and deliberately no arithmetic, no regular expressions
// and no boolean algebra beyond the implicit AND across a rule's conditions.
//
// The same argument as Phase 10D's six condition operators, with more force: a
// policy language grows until it is a programming language, and then the policy
// file is a program nobody reviews — which defeats the point of writing policy
// down. Anything needing more expressiveness belongs in a [Detector] or a
// signal, where it is named, owned and testable.
const (
	SelEquals Selector = iota
	SelNotEquals
	SelGreaterThan
	SelLessThan
	SelAtLeast
	SelIn
	SelNotIn
	SelExists
	SelAbsent
	SelPrefix
)

// String renders the selector.
func (s Selector) String() string {
	switch s {
	case SelNotEquals:
		return "!="
	case SelGreaterThan:
		return ">"
	case SelLessThan:
		return "<"
	case SelAtLeast:
		return ">="
	case SelIn:
		return "in"
	case SelNotIn:
		return "not_in"
	case SelExists:
		return "exists"
	case SelAbsent:
		return "absent"
	case SelPrefix:
		return "prefix"
	default:
		return "=="
	}
}

// Field names what a condition reads.
//
// The built-in fields are spelled out rather than reached through attributes,
// because they are the ones every policy uses and a typo in "classification"
// should be a validation error rather than a condition that silently never
// matches.
type Field string

// The built-in fields. Anything else is looked up in the action's attributes
// and then the request context, in that order.
const (
	FieldKind           Field = "kind"
	FieldOperation      Field = "operation"
	FieldResource       Field = "resource"
	FieldClassification Field = "classification"
	FieldReversibility  Field = "reversibility"
	FieldRisk           Field = "risk"
	FieldActor          Field = "actor"
	FieldSubject        Field = "subject"
	FieldOrg            Field = "org"
	FieldBusiness       Field = "business"
	FieldRole           Field = "role"
)

func builtinFields() map[Field]bool {
	return map[Field]bool{
		FieldKind: true, FieldOperation: true, FieldResource: true,
		FieldClassification: true, FieldReversibility: true, FieldRisk: true,
		FieldActor: true, FieldSubject: true, FieldOrg: true,
		FieldBusiness: true, FieldRole: true,
	}
}

// Condition is one test.
type Condition struct {
	// Field is what to read.
	Field Field
	// Selector is the comparison.
	Selector Selector
	// Value is the right-hand side for scalar comparisons.
	Value Attr
	// Values is the right-hand side for In and NotIn.
	Values []string
}

func (c Condition) validate(where string) []string {
	var problems []string
	if c.Field == "" {
		problems = append(problems, where+": condition field is required")
	}
	if c.Selector > SelPrefix {
		problems = append(problems, fmt.Sprintf("%s: unknown selector %d", where, c.Selector))
	}
	switch c.Selector {
	case SelIn, SelNotIn:
		if len(c.Values) == 0 {
			problems = append(problems, fmt.Sprintf(
				"%s: %s needs a non-empty value set; an empty set matches nothing and "+
					"is almost never what an author meant", where, c.Selector))
		}
	case SelExists, SelAbsent:
		// No operand needed.
	case SelPrefix:
		if _, ok := c.Value.AsString(); !ok {
			problems = append(problems, where+": prefix needs a string value")
		}
	case SelGreaterThan, SelLessThan, SelAtLeast:
		if c.Value.Kind() != AttrNumber && c.Field != FieldRisk && c.Field != FieldClassification {
			problems = append(problems, fmt.Sprintf(
				"%s: %s needs a numeric value, or the risk or classification field",
				where, c.Selector))
		}
	default:
		if c.Value.IsAbsent() {
			problems = append(problems, fmt.Sprintf(
				"%s: %s needs a value; use exists or absent to test presence", where, c.Selector))
		}
	}
	return problems
}

// resolve reads a field from a request.
//
// Built-ins first, then action attributes, then request context. The order
// matters and is fixed: a caller cannot shadow "classification" with an
// attribute of the same name, which would be a quiet way to defeat a policy.
func resolveField(f Field, r Request) Attr {
	switch f {
	case FieldKind:
		return Str(r.Action.Kind.String())
	case FieldOperation:
		return Str(r.Action.Operation)
	case FieldResource:
		return Str(r.Action.Resource)
	case FieldClassification:
		return Num(float64(r.Action.Classification))
	case FieldReversibility:
		return Str(r.Action.Reversibility.String())
	case FieldRisk:
		return Num(float64(r.Risk.Level))
	case FieldActor:
		return Str(string(r.Actor))
	case FieldSubject:
		return Str(string(r.Subject))
	case FieldOrg:
		return Str(string(r.Org))
	case FieldBusiness:
		return Str(string(r.Business))
	case FieldRole:
		// Roles are a set; a scalar read returns the sorted join so Equals is
		// still meaningful, but In is the sensible selector and validation
		// does not force it — a policy author comparing the whole set to a
		// literal is unusual, not wrong.
		roles := append([]string(nil), r.Roles...)
		sort.Strings(roles)
		return Str(strings.Join(roles, ","))
	}
	if v, ok := r.Action.Attributes.Lookup(string(f)); ok {
		return v
	}
	if v, ok := r.Context.Lookup(string(f)); ok {
		return v
	}
	return Absent()
}

// Matches evaluates the condition against a request.
//
// A PURE FUNCTION with no error return. A condition that cannot be evaluated —
// a numeric comparison against a string — is FALSE rather than an error,
// because a policy file that fails the whole evaluation on one malformed
// condition takes the platform down, and validation at registration is where
// malformed conditions are supposed to be caught.
func (c Condition) Matches(r Request) bool {
	// The role field is a set, and In against a set means membership rather
	// than string equality. Handled before the scalar path because treating it
	// as a joined string would make the obvious policy silently wrong.
	if c.Field == FieldRole && (c.Selector == SelIn || c.Selector == SelNotIn || c.Selector == SelEquals) {
		held := make(map[string]bool, len(r.Roles))
		for _, role := range r.Roles {
			held[role] = true
		}
		switch c.Selector {
		case SelEquals:
			s, _ := c.Value.AsString()
			return held[s]
		case SelIn:
			for _, want := range c.Values {
				if held[want] {
					return true
				}
			}
			return false
		default: // SelNotIn
			for _, want := range c.Values {
				if held[want] {
					return false
				}
			}
			return true
		}
	}

	got := resolveField(c.Field, r)

	switch c.Selector {
	case SelExists:
		return !got.IsAbsent()
	case SelAbsent:
		return got.IsAbsent()
	}

	if got.IsAbsent() {
		// Every other selector against an absent field is false. Notably
		// NotEquals too: "the field is not X" is not true of a field that is
		// not there, and treating it as true is how a policy author writes a
		// deny rule that never fires.
		return false
	}

	switch c.Selector {
	case SelEquals:
		return attrEqual(got, c.Value)
	case SelNotEquals:
		return !attrEqual(got, c.Value)
	case SelPrefix:
		want, _ := c.Value.AsString()
		have, ok := got.AsString()
		return ok && strings.HasPrefix(have, want)
	case SelIn, SelNotIn:
		have := got.Display()
		found := false
		for _, want := range c.Values {
			if have == want {
				found = true
				break
			}
		}
		return found == (c.Selector == SelIn)
	case SelGreaterThan, SelLessThan, SelAtLeast:
		have, hok := numericOf(got, c.Field)
		want, wok := numericOf(c.Value, c.Field)
		if !hok || !wok {
			return false
		}
		switch c.Selector {
		case SelGreaterThan:
			return have > want
		case SelLessThan:
			return have < want
		default:
			return have >= want
		}
	}
	return false
}

func attrEqual(a, b Attr) bool {
	if a.Kind() == b.Kind() {
		var x, y strings.Builder
		a.canonical(&x)
		b.canonical(&y)
		return x.String() == y.String()
	}
	// Cross-kind equality compares display forms, so a policy author writing
	// risk == "high" against a numeric field works. The alternative — refusing
	// — would make every policy file carry the engine's internal encoding.
	return a.Display() == b.Display()
}

// numericOf coerces an attribute to a number, understanding the named enums so
// a policy can say risk >= "high" rather than risk >= 2.
func numericOf(a Attr, f Field) (float64, bool) {
	if n, ok := a.AsNumber(); ok {
		return n, true
	}
	s, ok := a.AsString()
	if !ok {
		return 0, false
	}
	switch f {
	case FieldRisk:
		if lvl, ok := ParseRiskLevel(s); ok {
			return float64(lvl), true
		}
	case FieldClassification:
		switch s {
		case "public":
			return float64(ClassPublic), true
		case "internal":
			return float64(ClassInternal), true
		case "personal":
			return float64(ClassPersonal), true
		case "sensitive":
			return float64(ClassSensitive), true
		case "secret":
			return float64(ClassSecret), true
		}
	}
	return 0, false
}

// Rule is one decision within a policy.
type Rule struct {
	// Name identifies the rule within its policy, for traces. Required: a
	// trace naming "rule 3" is a trace nobody can act on.
	Name string
	// When are the conditions, combined with AND. Empty matches everything,
	// which is how a policy expresses a default.
	When []Condition
	// Then is the outcome.
	Then Outcome
	// Reason is a short machine-readable code.
	Reason string
	// Explanation is one human-readable sentence for operators.
	Explanation string
	// Obligations are imposed when this rule fires.
	Obligations []Obligation
	// RetryAfter is set on OutcomeRetryLater.
	RetryAfter time.Duration
}

func (r Rule) validate(where string) []string {
	var problems []string
	if r.Name == "" {
		problems = append(problems, where+": rule name is required; a trace naming "+
			"an anonymous rule cannot be acted on")
	}
	if r.Reason == "" {
		problems = append(problems, fmt.Sprintf("%s rule %s: reason is required; "+
			"every denial must be explainable", where, r.Name))
	} else if bad := checkReasonCode(r.Reason); bad != "" {
		problems = append(problems, fmt.Sprintf("%s rule %s: %s", where, r.Name, bad))
	}
	if r.Then > OutcomeDefer {
		problems = append(problems, fmt.Sprintf("%s rule %s: unknown outcome", where, r.Name))
	}
	if r.Then == OutcomeRetryLater && r.RetryAfter <= 0 {
		problems = append(problems, fmt.Sprintf(
			"%s rule %s: retry_later needs a positive RetryAfter; telling a caller "+
				"to try again without saying when is telling it to spin", where, r.Name))
	}
	for i, c := range r.When {
		problems = append(problems, c.validate(fmt.Sprintf("%s rule %s condition %d", where, r.Name, i))...)
	}
	return problems
}

// Matches reports whether every condition holds.
func (r Rule) Matches(req Request) bool {
	for _, c := range r.When {
		if !c.Matches(req) {
			return false
		}
	}
	return true
}

// Match narrows which requests a policy applies to at all.
//
// Separate from a rule's conditions on purpose. A policy that does not apply is
// SKIPPED and says so in the trace; a policy that applies but whose rules do
// not match is EVALUATED and says that instead. Those are different facts, and
// the second is the one an operator wants when a rule they wrote appears to
// have done nothing.
type Match struct {
	// Kinds limits the policy to action kinds. Empty matches every kind.
	Kinds []ActionKind
	// Operations limits by exact operation. Empty matches every operation.
	Operations []string
	// ResourcePrefix limits by resource prefix. Empty matches everything.
	ResourcePrefix string
	// Orgs, Businesses, Subjects and Actors limit by identity. Empty matches
	// every value.
	Orgs       []OrgID
	Businesses []BusinessID
	Subjects   []SubjectID
	Actors     []ActorID
}

// applies reports whether the policy is in scope for a request, and why not.
func (m Match) applies(r Request) (bool, string) {
	if len(m.Kinds) > 0 {
		found := false
		for _, k := range m.Kinds {
			if k == r.Action.Kind {
				found = true
				break
			}
		}
		if !found {
			return false, "kind"
		}
	}
	if len(m.Operations) > 0 {
		found := false
		for _, op := range m.Operations {
			if op == r.Action.Operation {
				found = true
				break
			}
		}
		if !found {
			return false, "operation"
		}
	}
	if m.ResourcePrefix != "" && !strings.HasPrefix(r.Action.Resource, m.ResourcePrefix) {
		return false, "resource"
	}
	if len(m.Orgs) > 0 && !containsID(m.Orgs, r.Org) {
		return false, "org"
	}
	if len(m.Businesses) > 0 && !containsID(m.Businesses, r.Business) {
		return false, "business"
	}
	if len(m.Subjects) > 0 && !containsID(m.Subjects, r.Subject) {
		return false, "subject"
	}
	if len(m.Actors) > 0 && !containsID(m.Actors, r.Actor) {
		return false, "actor"
	}
	return true, ""
}

func containsID[T ~string](list []T, want T) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// Policy is a declarative rule set.
//
// DATA, NOT CODE. A policy has no methods that reach anywhere, holds no
// closures, and does nothing when evaluated except answer questions about
// itself. That is what lets a policy be reviewed before it is loaded, compared
// between two deployments, and recomputed from an audit record years later.
type Policy struct {
	// ID identifies the policy across versions.
	ID PolicyID

	// Version increments on every change. Carried into the trace, so a
	// decision names not just which policy decided but which revision of it.
	Version int

	// Scope places it in the precedence order.
	Scope Scope

	// Priority orders policies WITHIN a scope. Higher wins. Two policies at
	// the same priority in the same scope that disagree are a configuration
	// error, reported as [ConflictError] rather than resolved by coin toss.
	Priority int

	// Title and Description document it for operators.
	Title       string
	Description string

	// Owner names the team accountable. Required, for the same reason a tool
	// needs one: a policy nobody owns is a policy nobody updates when the law
	// changes.
	Owner string

	// Match narrows which requests it applies to.
	Match Match

	// Rules are evaluated in order; THE FIRST MATCH WINS.
	//
	// First-match rather than most-specific-match, because "most specific" needs
	// a specificity metric, and every specificity metric surprises somebody. A
	// policy author reading top to bottom can predict the outcome.
	Rules []Rule

	// Default is the outcome when the policy applies but no rule matches.
	// Zero value is Deny, deliberately.
	Default Outcome

	// DefaultReason is the reason for the default outcome.
	DefaultReason string

	// Override lets this policy win over LOWER-precedence scopes even when its
	// outcome is less severe. Permitted only in the emergency and compliance
	// scopes; validation refuses it elsewhere, because an override in a
	// business policy is a business quietly exempting itself from the platform
	// safety floor.
	Override bool

	// Enabled allows a policy to be staged without taking effect.
	Enabled bool

	// EffectiveFrom and EffectiveUntil bound the policy in time. Zero means
	// unbounded. A temporary policy without an end is a permanent policy
	// wearing a temporary label, and [Policy.validate] says so for
	// ScopeTemporary.
	EffectiveFrom  time.Time
	EffectiveUntil time.Time

	// Tags carry operator metadata. Never used for matching — a tag that
	// affected behaviour would be an untyped configuration channel.
	Tags map[string]string
}

func (p Policy) validate() []string {
	var problems []string
	where := string(p.ID)
	if where == "" {
		where = "<unnamed policy>"
		problems = append(problems, "policy: id is required")
	}
	if p.Version < 1 {
		problems = append(problems, where+": version must be at least 1")
	}
	if p.Scope > ScopeFeatureFlag {
		problems = append(problems, where+": unknown scope")
	}
	if p.Owner == "" {
		problems = append(problems, where+": owner is required; a policy nobody owns "+
			"is a policy nobody updates when the law changes")
	}
	if p.DefaultReason == "" {
		problems = append(problems, where+": DefaultReason is required; a policy that "+
			"denies by default must be able to say why")
	} else if bad := checkReasonCode(p.DefaultReason); bad != "" {
		problems = append(problems, where+": DefaultReason "+bad)
	}
	if p.Default > OutcomeDefer {
		problems = append(problems, where+": unknown default outcome")
	}
	if len(p.Rules) == 0 && p.Default == OutcomeAllow {
		problems = append(problems, where+": a policy with no rules that defaults to "+
			"allow is a blanket permission; state it as a rule so it appears in traces")
	}
	if p.Override && p.Scope != ScopeEmergency && p.Scope != ScopeCompliance {
		problems = append(problems, fmt.Sprintf(
			"%s: Override is permitted only in the emergency and compliance scopes, "+
				"not %s; an override below those is a tenant exempting itself from the "+
				"platform safety floor", where, p.Scope))
	}
	if p.Scope == ScopeTemporary && p.EffectiveUntil.IsZero() {
		problems = append(problems, where+": a temporary policy requires EffectiveUntil; "+
			"without one it is a permanent policy wearing a temporary label")
	}
	if !p.EffectiveFrom.IsZero() && !p.EffectiveUntil.IsZero() &&
		!p.EffectiveFrom.Before(p.EffectiveUntil) {
		problems = append(problems, where+": EffectiveFrom is not before EffectiveUntil")
	}

	seen := make(map[string]bool, len(p.Rules))
	known := builtinFields()
	for _, r := range p.Rules {
		problems = append(problems, r.validate(where)...)
		if seen[r.Name] {
			problems = append(problems, fmt.Sprintf("%s: duplicate rule name %s", where, r.Name))
		}
		seen[r.Name] = true
		_ = known
	}
	return problems
}

// Active reports whether the policy is enabled and within its effective window.
func (p Policy) Active(now time.Time) (bool, string) {
	if !p.Enabled {
		return false, "disabled"
	}
	if !p.EffectiveFrom.IsZero() && now.Before(p.EffectiveFrom) {
		return false, "not_yet_effective"
	}
	if !p.EffectiveUntil.IsZero() && !now.Before(p.EffectiveUntil) {
		return false, "expired"
	}
	return true, ""
}

// Evaluate applies the policy to a request, returning the outcome, the rule
// that fired, and the obligations.
//
// PURE. No clock — the caller has already established that the policy is active
// — no I/O, no mutation. Given the same policy and the same request it returns
// the same answer forever.
func (p Policy) Evaluate(r Request) (Outcome, Rule, bool) {
	for _, rule := range p.Rules {
		if rule.Matches(r) {
			return rule.Then, rule, true
		}
	}
	return p.Default, Rule{Name: "<default>", Reason: p.DefaultReason}, false
}

// canonicalBytes renders the policy deterministically, for versioning digests.
func (p Policy) canonicalBytes() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%d|%d|%d|%v|", p.ID, p.Version, p.Scope, p.Priority, p.Override)
	for _, r := range p.Rules {
		fmt.Fprintf(&b, "%s>%d>%s;", r.Name, r.Then, r.Reason)
		for _, c := range r.When {
			fmt.Fprintf(&b, "%s%s%s,", c.Field, c.Selector, c.Value.Display())
		}
	}
	fmt.Fprintf(&b, "|%d|%s", p.Default, p.DefaultReason)
	return []byte(b.String())
}

// Digest fingerprints the policy's decision-relevant content.
//
// Excludes the title, description, owner and tags: two policies that decide
// identically have the same digest even if one has a better description, which
// is what makes the digest useful for answering "did the rules actually change
// in this deploy".
func (p Policy) Digest() Fingerprint { return fingerprintOf(p.canonicalBytes()) }

// maxReasonLen bounds a reason code.
const maxReasonLen = 64

// checkReasonCode enforces that a reason is a bounded machine-readable code,
// returning a problem description or empty.
//
// Lowercase letters, digits and underscores only, at most 64 characters.
//
// TWO INDEPENDENT REASONS, and either alone would justify it. A reason becomes
// a METRIC LABEL, so unbounded text is an unbounded-cardinality incident
// waiting for its first unusual policy. And a reason travels into a permanent
// event topic, so a policy author who writes a subject identifier or a phrase
// somebody said into a reason has put it somewhere no erasure request can
// reach.
//
// This does not make INV-GOV-7 airtight — a determined author can still write a
// short identifier in snake_case — but it removes the failure that actually
// happens, which is a reason like "needs_consent_from_+919876543210". Stated as
// partial in SECURITY_REVIEW R2.
func checkReasonCode(reason string) string {
	if len(reason) > maxReasonLen {
		return fmt.Sprintf("reason %q is %d characters, limit is %d; a reason is a code, "+
			"not a sentence", reason, len(reason), maxReasonLen)
	}
	for _, r := range reason {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
		default:
			return fmt.Sprintf("reason %q contains %q; reasons become metric labels and "+
				"travel into a permanent event topic, so they are lowercase, digits and "+
				"underscores only", reason, r)
		}
	}
	return ""
}
