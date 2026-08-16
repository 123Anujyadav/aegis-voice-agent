package governance

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ActionKind classifies what a subsystem wants to do.
//
// FIVE KINDS, MATCHING THE FIVE THINGS THE PLATFORM CAN DO. They are a label on
// one generic [Action], not five types with five entry points — see the package
// documentation for why that distinction is the whole architecture.
type ActionKind uint8

// The action kinds.
const (
	// ActionConversation is something the dialogue wants to say or decide.
	ActionConversation ActionKind = iota

	// ActionMemory is a read or write against the memory engine.
	ActionMemory

	// ActionTool is a tool execution.
	ActionTool

	// ActionNotification is a message to a person outside the live call.
	ActionNotification

	// ActionExternal is anything that leaves the platform's boundary and is
	// not one of the above. The catch-all, and deliberately the strictest
	// default: a kind nobody classified is a kind nobody reasoned about.
	ActionExternal
)

// String renders the kind. Used as a metric label and in policy matching.
func (k ActionKind) String() string {
	switch k {
	case ActionMemory:
		return "memory"
	case ActionTool:
		return "tool"
	case ActionNotification:
		return "notification"
	case ActionExternal:
		return "external"
	default:
		return "conversation"
	}
}

// ParseActionKind maps a string back, reporting whether it was known.
func ParseActionKind(s string) (ActionKind, bool) {
	switch s {
	case "conversation":
		return ActionConversation, true
	case "memory":
		return ActionMemory, true
	case "tool":
		return ActionTool, true
	case "notification":
		return ActionNotification, true
	case "external":
		return ActionExternal, true
	default:
		return ActionExternal, false
	}
}

// Reversibility describes whether an action can be undone.
//
// Carried on the action rather than inferred, because only the caller knows.
// It is the single most useful input to a policy: the difference between "read
// a preference" and "place a call" is not the subsystem, it is this.
type Reversibility uint8

// The reversibility classes.
const (
	// ReversibleNone means the action changes nothing.
	ReversibleNone Reversibility = iota
	// ReversibleFully means it can be undone completely.
	ReversibleFully
	// ReversiblePartially means some of it can be undone.
	ReversiblePartially
	// ReversibleNever means it cannot be undone by any means: a sent message,
	// a placed call, a released payment.
	ReversibleNever
)

// String renders the class.
func (r Reversibility) String() string {
	switch r {
	case ReversibleFully:
		return "fully"
	case ReversiblePartially:
		return "partially"
	case ReversibleNever:
		return "never"
	default:
		return "none"
	}
}

// Mutating reports whether the action changes anything.
func (r Reversibility) Mutating() bool { return r != ReversibleNone }

// AttrKind classifies an [Attr].
type AttrKind uint8

// The attribute kinds. Four, and deliberately no lists, maps or blobs.
//
// Phase 10D's Value carries eight kinds including nested structures, because a
// tool argument genuinely can be a list of objects. A POLICY CONDITION cannot:
// a condition over a nested structure needs a path language, a path language
// needs a parser, and a parser is the first half of a programming language
// living inside a policy file that nobody can review.
//
// Anything that needs more structure is fingerprinted by the caller and matched
// as a string, or it belongs in a [Detector].
const (
	AttrString AttrKind = iota
	AttrNumber
	AttrBool
	AttrAbsent
)

// String renders the kind.
func (k AttrKind) String() string {
	switch k {
	case AttrNumber:
		return "number"
	case AttrBool:
		return "bool"
	case AttrAbsent:
		return "absent"
	default:
		return "string"
	}
}

// Attr is one attribute of an action or a request context.
//
// Immutable by construction: unexported fields, no setters. An attribute that
// could change after a decision was fingerprinted would make the audit record a
// story rather than a record.
type Attr struct {
	kind AttrKind
	str  string
	num  float64
	flag bool
}

// Str builds a string attribute.
func Str(s string) Attr { return Attr{kind: AttrString, str: s} }

// Num builds a numeric attribute.
func Num(f float64) Attr { return Attr{kind: AttrNumber, num: f} }

// Flag builds a boolean attribute.
func Flag(b bool) Attr { return Attr{kind: AttrBool, flag: b} }

// Absent is the explicitly-absent attribute. Distinct from a missing key: a
// caller saying "this subject has no consent on file" and a caller forgetting
// to look are different facts, and a policy frequently must treat them
// differently.
func Absent() Attr { return Attr{kind: AttrAbsent} }

// Kind reports the attribute's kind.
func (a Attr) Kind() AttrKind { return a.kind }

// IsAbsent reports explicit absence.
func (a Attr) IsAbsent() bool { return a.kind == AttrAbsent }

// AsString returns the string, and false when the attribute is not a string.
func (a Attr) AsString() (string, bool) { return a.str, a.kind == AttrString }

// AsNumber returns the number.
func (a Attr) AsNumber() (float64, bool) { return a.num, a.kind == AttrNumber }

// AsBool returns the boolean.
func (a Attr) AsBool() (bool, bool) { return a.flag, a.kind == AttrBool }

// Display renders the attribute for a trace. Never used for comparison.
func (a Attr) Display() string {
	switch a.kind {
	case AttrNumber:
		return strconv.FormatFloat(a.num, 'g', -1, 64)
	case AttrBool:
		return strconv.FormatBool(a.flag)
	case AttrAbsent:
		return "<absent>"
	default:
		return a.str
	}
}

// canonical appends a deterministic encoding.
//
// Kind-tagged and length-prefixed so no two distinct attributes share an
// encoding — otherwise the string "1" and the number 1 would collide, and a
// fingerprint that cannot tell them apart is a fingerprint that cannot tell two
// different decisions apart.
func (a Attr) canonical(b *strings.Builder) {
	b.WriteByte(byte('0' + a.kind))
	switch a.kind {
	case AttrString:
		b.WriteString(strconv.Itoa(len(a.str)))
		b.WriteByte(':')
		b.WriteString(a.str)
	case AttrNumber:
		b.WriteString(strconv.FormatFloat(a.num, 'g', -1, 64))
	case AttrBool:
		if a.flag {
			b.WriteByte('t')
		} else {
			b.WriteByte('f')
		}
	}
}

// Attrs is a named attribute set.
type Attrs map[string]Attr

// Clone returns an independent copy.
func (a Attrs) Clone() Attrs {
	if a == nil {
		return nil
	}
	c := make(Attrs, len(a))
	for k, v := range a {
		c[k] = v
	}
	return c
}

// Keys returns the attribute names, sorted. Sorted because every caller of this
// either logs it or hashes it, and both want stability.
func (a Attrs) Keys() []string {
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Get returns an attribute, or Absent when the key is missing.
//
// Missing and explicitly-absent both yield an absent Attr here, and the caller
// that needs to distinguish them uses the two-value form. Conditions
// deliberately do not distinguish: a policy that behaved differently depending
// on whether a caller forgot a key or set it to absent would be a policy nobody
// could reason about.
func (a Attrs) Get(key string) Attr {
	if v, ok := a[key]; ok {
		return v
	}
	return Absent()
}

// Lookup returns an attribute and whether the key was present at all.
func (a Attrs) Lookup(key string) (Attr, bool) {
	v, ok := a[key]
	return v, ok
}

// canonicalBytes renders the attribute set deterministically, with sorted keys.
func (a Attrs) canonicalBytes() []byte {
	var b strings.Builder
	for _, k := range a.Keys() {
		b.WriteString(strconv.Itoa(len(k)))
		b.WriteByte(':')
		b.WriteString(k)
		b.WriteByte('=')
		a[k].canonical(&b)
		b.WriteByte(';')
	}
	return []byte(b.String())
}

// Fingerprint returns a stable fingerprint of the attribute set.
func (a Attrs) Fingerprint() Fingerprint { return fingerprintOf(a.canonicalBytes()) }

// Action is what a subsystem wants to do.
//
// GENERIC ON PURPOSE. There is no ConversationAction, MemoryAction or
// ToolAction type. Five types would mean five evaluation paths, five sets of
// policy-matching code and five places for an exemption to grow.
//
// A subsystem describes what it wants in four fields and lets policy decide.
type Action struct {
	// Kind classifies the action.
	Kind ActionKind

	// Operation is what is being done, in the caller's vocabulary: "speak",
	// "store", "invoke", "send". Matched by policies as an exact string.
	Operation string

	// Resource is what it is being done to: a capability, a memory kind, a
	// notification channel. Matched as an exact string or a prefix.
	Resource string

	// Reversibility is whether it can be undone. The single most useful input
	// a policy has.
	Reversibility Reversibility

	// Classification is the data sensitivity involved.
	Classification Classification

	// Attributes carry everything else a policy might condition on. Flat,
	// typed and small — see [AttrKind].
	//
	// THEY MUST NOT CARRY CONTENT. An attribute becomes part of a decision
	// fingerprint and can appear in a trace, and a trace is written to a
	// durable audit store. Callers pass a fingerprint or a classification, not
	// the words somebody said. Enforced by convention and flagged in
	// SECURITY_REVIEW; see also [Action.validate].
	Attributes Attrs
}

func (a Action) validate() []string {
	var problems []string
	if a.Operation == "" {
		problems = append(problems, "action: operation is required")
	}
	if a.Kind > ActionExternal {
		problems = append(problems, fmt.Sprintf("action: unknown kind %d", a.Kind))
	}
	if a.Reversibility > ReversibleNever {
		problems = append(problems, fmt.Sprintf("action: unknown reversibility %d", a.Reversibility))
	}
	if a.Classification > ClassSecret {
		problems = append(problems, fmt.Sprintf("action: unknown classification %d", a.Classification))
	}
	for _, k := range a.Attributes.Keys() {
		if k == "" {
			problems = append(problems, "action: empty attribute name")
		}
	}
	return problems
}

// String renders the action compactly, for traces and metric labels.
func (a Action) String() string {
	return a.Kind.String() + ":" + a.Operation + ":" + a.Resource
}

// Label returns the metric label form: kind and operation only.
//
// Resource is deliberately excluded. It is frequently a business identifier or
// a memory key, and a metric label built from one is an unbounded-cardinality
// incident waiting for its first busy day.
func (a Action) Label() string { return a.Kind.String() + ":" + a.Operation }

// canonicalBytes renders the action deterministically.
func (a Action) canonicalBytes() []byte {
	var b strings.Builder
	b.WriteString(a.Kind.String())
	b.WriteByte('|')
	b.WriteString(a.Operation)
	b.WriteByte('|')
	b.WriteString(a.Resource)
	b.WriteByte('|')
	b.WriteString(a.Reversibility.String())
	b.WriteByte('|')
	b.WriteString(a.Classification.String())
	b.WriteByte('|')
	b.Write(a.Attributes.canonicalBytes())
	return []byte(b.String())
}

// Fingerprint returns a stable fingerprint of the action.
func (a Action) Fingerprint() Fingerprint { return fingerprintOf(a.canonicalBytes()) }

// Request is one governance question.
//
// It carries WHO is asking, ON WHOSE BEHALF, in WHAT organisational context,
// and WHAT they want to do. Those four are the inputs to every policy scope;
// nothing else is needed and nothing else is accepted, which is what keeps the
// evaluator a pure function.
type Request struct {
	// Action is what the caller wants to do.
	Action Action

	// Actor is who is asking.
	Actor ActorID

	// Subject is the person the action is about, when there is one.
	Subject SubjectID

	// Org and Business locate the request in the organisational hierarchy,
	// which is how organisation- and business-scoped policies are selected.
	Org      OrgID
	Business BusinessID

	// Session and Correlation are carried for audit and trace assembly. Never
	// used to select policies — see [SessionID].
	Session     SessionID
	Correlation CorrelationID

	// Roles the actor holds, expanded by the caller. This engine does not
	// resolve roles for the same reason Phase 10D does not: the role map is
	// Identity's, and a second copy here would drift.
	Roles []string

	// Risk carries signals other phases produced. Aggregated, never computed
	// here: this module runs no model.
	Risk RiskAssessment

	// Context carries request-level attributes policies may condition on, in
	// addition to the action's own. Same content prohibition applies.
	Context Attrs
}

func (r Request) validate() []string {
	problems := r.Action.validate()
	if r.Actor == "" {
		problems = append(problems, "request: actor is required; a decision with "+
			"no actor cannot be attributed, and an unattributable decision is not "+
			"a governance decision")
	}
	return problems
}

// Validate reports whether the request is well-formed.
//
// Structure only. It consults no registry, so a caller can validate before the
// engine is even reachable, and a malformed request is distinguishable from a
// denied one — two facts that call for entirely different responses.
func (r Request) Validate() error {
	if problems := r.validate(); len(problems) > 0 {
		sort.Strings(problems)
		return &ConfigError{Problems: problems}
	}
	return nil
}

// Fingerprint returns a stable fingerprint of everything that determines the
// outcome.
//
// Covers the action, actor, subject, org, business, roles, risk and context.
// Deliberately EXCLUDES the session and correlation, which differ between two
// identical questions — that is what makes the fingerprint usable for replay
// comparison and for detecting that the same question got a different answer.
func (r Request) Fingerprint() Fingerprint {
	var b strings.Builder
	b.Write(r.Action.canonicalBytes())
	b.WriteByte('|')
	b.WriteString(string(r.Actor))
	b.WriteByte('|')
	b.WriteString(string(r.Subject))
	b.WriteByte('|')
	b.WriteString(string(r.Org))
	b.WriteByte('|')
	b.WriteString(string(r.Business))
	b.WriteByte('|')

	roles := append([]string(nil), r.Roles...)
	sort.Strings(roles)
	b.WriteString(strings.Join(roles, ","))
	b.WriteByte('|')
	b.WriteString(r.Risk.Level.String())
	b.WriteByte('|')
	b.Write(r.Context.canonicalBytes())
	return fingerprintOf([]byte(b.String()))
}
