package governance

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Classification is the data sensitivity of an action's subject matter.
//
// The five classes mirror the frozen contracts/proto annotations and Phase
// 10C's memory engine exactly. A sixth class here, or the same five under
// different names, would mean two vocabularies for one idea and a mapping table
// that is wrong on its first day.
type Classification uint8

// The classifications.
const (
	ClassPublic Classification = iota
	ClassInternal
	ClassPersonal
	ClassSensitive
	// ClassSecret is authentication material. The engine can reason about it;
	// no policy in this module ever permits storing it — see [ClassSecret]
	// handling in the baseline policies.
	ClassSecret
)

// String renders the classification. Used as a metric label.
func (c Classification) String() string {
	switch c {
	case ClassInternal:
		return "internal"
	case ClassPersonal:
		return "personal"
	case ClassSensitive:
		return "sensitive"
	case ClassSecret:
		return "secret"
	default:
		return "public"
	}
}

// Personal reports whether the class carries personal data and therefore needs
// a lawful basis.
func (c Classification) Personal() bool { return c >= ClassPersonal && c != ClassSecret }

// Detector finds sensitive material in a value.
//
// AN INTERFACE WITH NO IMPLEMENTATION IN THIS MODULE, and that is the point.
// PII detection is a model problem: it is language-specific, it is
// script-specific, and for an India-first platform it involves Aadhaar, PAN,
// UPI handles and eleven scripts. Shipping a naive regular expression here
// would be worse than shipping nothing, because a detector that catches 60% of
// phone numbers teaches everyone downstream to trust it.
//
// The engine defines the seam, the policy model expresses what to do with a
// finding, and the detector arrives in a later phase with its own evaluation
// corpus.
type Detector interface {
	// Detect reports what sensitive categories a value contains. It receives
	// the value because it must; nothing else in this engine does.
	Detect(value string) []Finding
}

// Finding is one detection.
type Finding struct {
	// Category names what was found: "phone", "email", "government_id".
	Category string
	// Confidence in [0, 1].
	Confidence float64
	// Start and End locate it, for masking.
	Start, End int
}

// Classifier assigns a classification to a resource.
//
// Also an interface with no implementation here. The mapping from "a memory
// record of kind contact" to "personal" belongs to the domain model, not to the
// governance engine, and hard-coding it here would put a second copy of the
// domain's classification rules in the module least able to keep it current.
type Classifier interface {
	// Classify returns the classification for a kind and resource.
	Classify(kind ActionKind, resource string) Classification
}

// Masker transforms a value so it can be shown or logged.
type Masker interface {
	// Mask returns a masked form. It must be idempotent: masking twice yields
	// the same result, because a value may pass through more than one
	// obligation.
	Mask(category, value string) string
}

// StaticClassifier is a table-driven [Classifier] for deployments whose mapping
// genuinely is a table.
//
// Provided because a static mapping IS the right answer for some resources —
// a notification channel's classification does not depend on a model — and
// because it makes the interface exercisable without a detector.
type StaticClassifier struct {
	mu         sync.RWMutex
	byResource map[string]Classification
	fallback   map[ActionKind]Classification
	def        Classification
}

// NewStaticClassifier builds a classifier with a default.
//
// The default is REQUIRED and there is no zero-value constructor, because a
// classifier that silently returns ClassPublic for anything it has not been
// told about is a classifier that downgrades every unknown resource.
func NewStaticClassifier(def Classification) *StaticClassifier {
	return &StaticClassifier{
		byResource: make(map[string]Classification),
		fallback:   make(map[ActionKind]Classification),
		def:        def,
	}
}

// SetResource maps a resource to a classification.
func (c *StaticClassifier) SetResource(resource string, class Classification) *StaticClassifier {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byResource[resource] = class
	return c
}

// SetKind maps an action kind to a fallback classification.
func (c *StaticClassifier) SetKind(kind ActionKind, class Classification) *StaticClassifier {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fallback[kind] = class
	return c
}

// Classify returns the resource's classification, the kind's fallback, or the
// default — in that order.
func (c *StaticClassifier) Classify(kind ActionKind, resource string) Classification {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if cl, ok := c.byResource[resource]; ok {
		return cl
	}
	if cl, ok := c.fallback[kind]; ok {
		return cl
	}
	return c.def
}

// RetentionClass names how long data may be kept.
//
// The same four classes as the frozen annotations and Phase 10C, for the same
// reason the classifications match: one vocabulary.
type RetentionClass uint8

// The retention classes.
const (
	RetentionEphemeral RetentionClass = iota
	RetentionShort
	RetentionStandard
	// RetentionLegalHold has NO duration. Giving it one is how the records
	// that must be kept get accidentally deleted, and [PrivacyRules.validate]
	// refuses a configuration that tries.
	RetentionLegalHold
)

// String renders the class.
func (r RetentionClass) String() string {
	switch r {
	case RetentionShort:
		return "short"
	case RetentionStandard:
		return "standard"
	case RetentionLegalHold:
		return "legal_hold"
	default:
		return "ephemeral"
	}
}

// PrivacyRules declares what may be exported, what must be deleted and how long
// things live.
//
// DECLARATIVE, and evaluated by the same pure evaluator as everything else. The
// alternative — privacy handled by a separate mechanism with its own precedence
// — is how a system ends up with a retention rule that a business policy
// silently overrides.
type PrivacyRules struct {
	// Retention maps a classification to a retention class.
	Retention map[Classification]RetentionClass

	// Durations gives each retention class a lifetime. LegalHold must be
	// absent.
	Durations map[RetentionClass]time.Duration

	// ExportAllowed lists the classifications that may leave the platform.
	// EMPTY MEANS NONE, not "all": a privacy configuration whose empty state
	// permits export is a privacy configuration nobody has written yet.
	ExportAllowed []Classification

	// MaskOnLog lists the classifications that must be masked before appearing
	// in any log, trace or event.
	MaskOnLog []Classification

	// DeleteOnRevocation reports whether withdrawing consent deletes the data
	// rather than merely stopping its use.
	//
	// TRUE by default in [DefaultPrivacyRules]. A record retained after
	// withdrawal and merely filtered at read is still personal data being
	// processed without a basis.
	DeleteOnRevocation bool
}

// DefaultPrivacyRules returns the platform's baseline.
//
// The standard duration is 90 days, matching ADR-0012's transcript retention
// and Phase 10C's memory retention rather than being chosen independently. A
// governance layer whose retention differs from the data it governs is a
// governance layer that will be overruled by whichever number is shorter.
func DefaultPrivacyRules() PrivacyRules {
	return PrivacyRules{
		Retention: map[Classification]RetentionClass{
			ClassPublic:    RetentionStandard,
			ClassInternal:  RetentionStandard,
			ClassPersonal:  RetentionStandard,
			ClassSensitive: RetentionShort,
			ClassSecret:    RetentionEphemeral,
		},
		Durations: map[RetentionClass]time.Duration{
			RetentionEphemeral: 0,
			RetentionShort:     24 * time.Hour,
			RetentionStandard:  90 * 24 * time.Hour,
		},
		ExportAllowed:      []Classification{ClassPublic, ClassInternal},
		MaskOnLog:          []Classification{ClassPersonal, ClassSensitive, ClassSecret},
		DeleteOnRevocation: true,
	}
}

func (p PrivacyRules) validate() []string {
	var problems []string
	if _, ok := p.Durations[RetentionLegalHold]; ok {
		problems = append(problems, "privacy: RetentionLegalHold must have no duration; "+
			"giving it one is how records that must be kept get deleted")
	}
	for class, d := range p.Durations {
		if d < 0 {
			problems = append(problems, fmt.Sprintf(
				"privacy: duration for %s must not be negative", class))
		}
	}
	for _, c := range p.ExportAllowed {
		if c >= ClassPersonal && c != ClassSecret {
			problems = append(problems, fmt.Sprintf(
				"privacy: %s data is listed as exportable; personal data leaving the "+
					"platform needs a policy and a lawful basis, not a configuration flag",
				c))
		}
		if c == ClassSecret {
			problems = append(problems, "privacy: secret material is never exportable")
		}
	}
	return problems
}

// RetentionFor returns the retention class and duration for a classification.
//
// The second return reports whether a duration exists at all: legal hold has
// none, and a caller that treats a zero duration as "delete immediately" would
// destroy exactly the records that must be kept.
func (p PrivacyRules) RetentionFor(c Classification) (RetentionClass, time.Duration, bool) {
	class, ok := p.Retention[c]
	if !ok {
		class = RetentionStandard
	}
	if class == RetentionLegalHold {
		return class, 0, false
	}
	d, ok := p.Durations[class]
	return class, d, ok
}

// MayExport reports whether a classification may leave the platform.
func (p PrivacyRules) MayExport(c Classification) bool {
	for _, allowed := range p.ExportAllowed {
		if allowed == c {
			return true
		}
	}
	return false
}

// MustMask reports whether a classification must be masked in logs and traces.
func (p PrivacyRules) MustMask(c Classification) bool {
	for _, m := range p.MaskOnLog {
		if m == c {
			return true
		}
	}
	return false
}

// PrivacyObligations returns the obligations a classification implies for an
// action, sorted.
//
// This is where the privacy engine meets the decision engine: rather than a
// separate enforcement path, privacy expresses itself as obligations on the one
// decision every action already passes through.
func (p PrivacyRules) PrivacyObligations(a Action, by PolicyID) []Obligation {
	var out []Obligation

	if p.MustMask(a.Classification) {
		out = append(out, Obligation{Kind: ObligationMask, Target: "log",
			Reason: "classification_" + a.Classification.String(), Policy: by})
	}
	if a.Kind == ActionExternal && !p.MayExport(a.Classification) {
		out = append(out, Obligation{Kind: ObligationRedact, Target: "export",
			Reason: "export_not_permitted", Policy: by})
	}
	if a.Classification.Personal() {
		out = append(out, Obligation{Kind: ObligationAudit, Target: a.Kind.String(),
			Reason: "personal_data", Policy: by})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Target < out[j].Target
	})
	return out
}
