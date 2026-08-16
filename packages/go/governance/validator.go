package governance

import (
	"fmt"
	"sort"
	"strings"
)

// KindSpec declares what a well-formed action of one kind looks like.
//
// Structural validation, deliberately separate from policy. "This action is
// malformed" and "this action is refused" are different facts that call for
// entirely different responses: the first is a caller bug, the second is the
// system working. Collapsing them would mean a subsystem with a typo in an
// operation name sees a denial and concludes the platform is over-restrictive.
type KindSpec struct {
	// Operations lists the permitted operations. Empty permits any.
	//
	// Worth setting. An operation vocabulary that nobody enumerated is one
	// where "delete" and "Delete" are two operations and a policy matching the
	// first silently misses the second.
	Operations []string

	// RequiredAttributes must all be present. This is how a kind states the
	// facts its policies need in order to decide at all: a memory write with
	// no subject cannot be governed by a subject-scoped policy, and finding
	// that out at the policy stage produces a confusing default-deny rather
	// than a clear error.
	RequiredAttributes []string

	// RequireSubject demands a subject on the request.
	RequireSubject bool

	// MutatingOperations lists operations that must NOT declare
	// ReversibleNone. Naming the operations rather than the kind is the point:
	// a memory READ genuinely changes nothing, and a validator that demanded
	// otherwise of every memory action would refuse the single most common
	// action in the platform. See ENGINEERING_AUDIT F2.
	MutatingOperations []string

	// RequireReversibility demands an explicit reversibility for every
	// operation of the kind. Used for [ActionExternal], where there is no
	// operation vocabulary to enumerate and anything crossing the boundary is
	// assumed to change something.
	RequireReversibility bool

	// MaxResourceLen bounds the resource string. Zero takes the validator's
	// default.
	MaxResourceLen int
}

// Validator checks that actions are well-formed before they reach policy.
type Validator struct {
	// Specs declares each kind's shape. A kind with no spec is permitted with
	// only the universal checks applied.
	Specs map[ActionKind]KindSpec

	// MaxAttributeLen bounds an attribute's string length.
	//
	// THIS IS THE ONE MECHANICAL DEFENCE OF INVARIANT INV-GOV-7. Attributes
	// must not carry content, because they are fingerprinted into decisions,
	// rendered into traces and written to a durable audit store. That is
	// otherwise a convention, and conventions are followed until a deadline.
	//
	// A length bound does not prove an attribute is not content — a short
	// phone number sails through — but it does stop the failure mode that
	// actually happens: somebody passes the whole utterance as an attribute
	// because it was convenient. Stated honestly as partial in
	// SECURITY_REVIEW §R2.
	MaxAttributeLen int

	// MaxResourceLen bounds resources when a spec does not.
	MaxResourceLen int

	// MaxAttributes bounds how many attributes an action may carry.
	MaxAttributes int
}

// DefaultValidator returns the platform baseline.
//
// The attribute limit is 256 characters. Long enough for any identifier,
// fingerprint, enum or reference the platform uses; far too short for a
// sentence somebody said on a phone call, which is the thing it exists to
// keep out.
func DefaultValidator() Validator {
	return Validator{
		Specs: map[ActionKind]KindSpec{
			ActionConversation: {
				Operations:     []string{"speak", "listen", "summarise", "classify", "end_call", "transfer"},
				RequireSubject: true,
			},
			ActionMemory: {
				Operations:         []string{"read", "write", "update", "delete", "export", "search"},
				MutatingOperations: []string{"write", "update", "delete", "export"},
				RequireSubject:     true,
			},
			ActionTool: {
				RequiredAttributes: []string{"capability"},
			},
			ActionNotification: {
				Operations:         []string{"send", "schedule", "cancel"},
				MutatingOperations: []string{"send", "schedule"},
				RequiredAttributes: []string{"channel"},
				RequireSubject:     true,
			},
			ActionExternal: {
				RequiredAttributes:   []string{"destination"},
				RequireReversibility: true,
			},
		},
		MaxAttributeLen: 256,
		MaxResourceLen:  512,
		MaxAttributes:   32,
	}
}

func (v Validator) validate() []string {
	var problems []string
	if v.MaxAttributeLen <= 0 {
		problems = append(problems, "validator: MaxAttributeLen must be positive; without "+
			"a bound, nothing stops a caller passing an utterance as an attribute")
	}
	if v.MaxResourceLen <= 0 {
		problems = append(problems, "validator: MaxResourceLen must be positive")
	}
	if v.MaxAttributes <= 0 {
		problems = append(problems, "validator: MaxAttributes must be positive")
	}
	return problems
}

// Check validates a request structurally.
//
// Returns every problem at once. A caller fixing one attribute at a time,
// redeploying between each, is a caller who will eventually give up and route
// around the engine.
func (v Validator) Check(r Request) error {
	problems := r.validate()
	problems = append(problems, v.checkAttributes("action", r.Action.Attributes)...)
	problems = append(problems, v.checkAttributes("context", r.Context)...)

	maxRes := v.MaxResourceLen
	spec, hasSpec := v.Specs[r.Action.Kind]
	if hasSpec && spec.MaxResourceLen > 0 {
		maxRes = spec.MaxResourceLen
	}
	if maxRes > 0 && len(r.Action.Resource) > maxRes {
		problems = append(problems, fmt.Sprintf(
			"action: resource is %d characters, limit is %d; a resource that long is "+
				"usually content that belongs somewhere else",
			len(r.Action.Resource), maxRes))
	}

	if hasSpec {
		problems = append(problems, v.checkSpec(spec, r)...)
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return &ConfigError{Problems: problems}
	}
	return nil
}

func (v Validator) checkSpec(spec KindSpec, r Request) []string {
	var problems []string
	kind := r.Action.Kind.String()

	if len(spec.Operations) > 0 {
		found := false
		for _, op := range spec.Operations {
			if op == r.Action.Operation {
				found = true
				break
			}
		}
		if !found {
			problems = append(problems, fmt.Sprintf(
				"action: %s operation %q is not one of %v; an unenumerated operation "+
					"is one no policy was written for",
				kind, r.Action.Operation, spec.Operations))
		}
	}

	for _, name := range spec.RequiredAttributes {
		if _, ok := r.Action.Attributes.Lookup(name); !ok {
			problems = append(problems, fmt.Sprintf(
				"action: %s requires the %q attribute; without it a policy cannot "+
					"distinguish this action from any other of its kind", kind, name))
		}
	}

	if spec.RequireSubject && r.Subject == "" {
		problems = append(problems, fmt.Sprintf(
			"request: %s actions require a subject; without one, subject-scoped "+
				"policies and consent cannot be evaluated at all", kind))
	}

	if r.Action.Reversibility == ReversibleNone {
		mutating := spec.RequireReversibility
		for _, op := range spec.MutatingOperations {
			if op == r.Action.Operation {
				mutating = true
				break
			}
		}
		if mutating {
			problems = append(problems, fmt.Sprintf(
				"action: %s %q declares reversibility 'none', meaning it changes "+
					"nothing; if that is true it is a read, and if it is not, say which",
				kind, r.Action.Operation))
		}
	}
	return problems
}

func (v Validator) checkAttributes(where string, attrs Attrs) []string {
	var problems []string
	if v.MaxAttributes > 0 && len(attrs) > v.MaxAttributes {
		problems = append(problems, fmt.Sprintf(
			"%s: %d attributes, limit is %d", where, len(attrs), v.MaxAttributes))
	}
	for _, name := range attrs.Keys() {
		a := attrs[name]
		s, ok := a.AsString()
		if !ok {
			continue
		}
		if v.MaxAttributeLen > 0 && len(s) > v.MaxAttributeLen {
			problems = append(problems, fmt.Sprintf(
				"%s: attribute %q is %d characters, limit is %d; attributes are "+
					"fingerprinted into decisions and written to a durable audit store, "+
					"so they carry references and codes, never content",
				where, name, len(s), v.MaxAttributeLen))
		}
		if strings.ContainsAny(s, "\n\r") {
			problems = append(problems, fmt.Sprintf(
				"%s: attribute %q contains a line break, which no identifier or code "+
					"does and every transcript does", where, name))
		}
	}
	return problems
}

// KnownOperations returns every operation the validator recognises, sorted.
//
// Operator-facing. "Which operations can I write a policy against?" is the
// first question somebody authoring policy has, and answering it from a
// scattering of string literals in five subsystems is how policy files end up
// matching operations that do not exist.
func (v Validator) KnownOperations() map[ActionKind][]string {
	out := make(map[ActionKind][]string, len(v.Specs))
	for kind, spec := range v.Specs {
		ops := append([]string(nil), spec.Operations...)
		sort.Strings(ops)
		out[kind] = ops
	}
	return out
}
