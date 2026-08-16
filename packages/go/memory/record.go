// Package memory is the Enterprise Memory Engine: the permanent memory layer
// for the platform.
//
// It serves the consumer assistant, the business receptionist, fraud
// intelligence, telephony intelligence and any future multi-agent runtime. It
// is a peer of the conversation engine, not a part of it, and imports neither
// it nor any vendor.
//
// # What this is
//
// A typed, indexed, policy-governed store with an explicit lifecycle. It knows
// what a memory IS — its kind, its tier, its subject, its classification, its
// consent basis, its version — and how memories are promoted, demoted, expired,
// archived and erased.
//
// # What this is deliberately not
//
// No embeddings, no vector store, no semantic search, no LLM summarisation, no
// prompt templates, no conversation behaviour, no telephony, no fraud logic and
// no business workflows. Where such a thing is needed the engine defines an
// interface it does not implement — [Summarizer] and [Encryptor] are the clear
// cases, and neither has an implementation anywhere in this module.
//
// # Kind and Tier — the distinction that shapes everything
//
// The Phase 10C brief lists eleven "memory types". Read carefully, they are two
// orthogonal ideas wearing one name:
//
//	WorkingMemory, ShortTermMemory, LongTermMemory  →  a TIER (lifetime)
//	Conversation, Session, User, Business,
//	Preference, Contact, Scratchpad, Policy         →  a KIND (subject matter)
//
// Modelling them as one enum makes "a long-lived user preference" and "a
// scratchpad note" inexpressible as different things, and forces every promotion
// rule to special-case eight values. Here they are a grid: [Kind] × [Tier].
// The eleven named types are constructors over that grid — see [KindOf] and the
// helpers at the foot of this file.
//
// # Determinism
//
// Given the same operations and the same injected clock, the engine produces
// the same records, the same index contents, the same eviction order and the
// same events. Nothing consults wall time, a random source, or map iteration
// order where order is observable.
package memory

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Tier is a memory's lifetime class.
//
// Tier governs HOW LONG and HOW EAGERLY a record is kept. It says nothing about
// what the record is about — that is [Kind].
type Tier int

const (
	// TierWorking is scratch state for the current unit of work. Cheapest to
	// write, first to be evicted, and never a system of record.
	TierWorking Tier = iota

	// TierShortTerm survives the unit of work but not the relationship. A
	// session's accumulated facts live here.
	TierShortTerm

	// TierLongTerm is durable knowledge about a subject. Promotion into this
	// tier is deliberate and policy-gated, because a long-term memory is a
	// commitment to remember something about a person.
	TierLongTerm
)

// String renders the tier for logs, metric labels and index keys.
func (t Tier) String() string {
	switch t {
	case TierShortTerm:
		return "short_term"
	case TierLongTerm:
		return "long_term"
	default:
		return "working"
	}
}

// Valid reports whether t is a defined tier.
func (t Tier) Valid() bool { return t >= TierWorking && t <= TierLongTerm }

// Promote returns the next tier up, or t if already at the top.
//
// Single-step by design. A record that could jump from working to long-term in
// one move would make the promotion record useless for explaining why something
// is remembered — and "why does it know that about me" is the question this
// engine must always be able to answer.
func (t Tier) Promote() Tier {
	if t >= TierLongTerm {
		return TierLongTerm
	}
	return t + 1
}

// Demote returns the next tier down, or t if already at the bottom.
func (t Tier) Demote() Tier {
	if t <= TierWorking {
		return TierWorking
	}
	return t - 1
}

// Kind is what a memory is about.
type Kind int

const (
	// KindConversation is what was said and concluded in one dialogue.
	KindConversation Kind = iota

	// KindSession spans one authenticated interaction, possibly several
	// conversations.
	KindSession

	// KindUser is durable knowledge about a subscriber.
	KindUser

	// KindBusiness is organisation configuration and knowledge. Reference data
	// from a conversation's point of view.
	KindBusiness

	// KindPreference is a stated choice. Distinct from KindUser because a
	// preference is something the subject ASKED for, and erasing or overriding
	// it has a different weight from forgetting an observation.
	KindPreference

	// KindContact is knowledge about a third party who called or was called.
	KindContact

	// KindScratchpad is intermediate working state for one decision. Always
	// TierWorking; see NewScratchpad.
	KindScratchpad

	// KindPolicy is an operating rule — retention choices, consent state,
	// routing constraints. Read-mostly and never inferred.
	KindPolicy
)

// String renders the kind for logs, metric labels and index keys.
func (k Kind) String() string {
	switch k {
	case KindSession:
		return "session"
	case KindUser:
		return "user"
	case KindBusiness:
		return "business"
	case KindPreference:
		return "preference"
	case KindContact:
		return "contact"
	case KindScratchpad:
		return "scratchpad"
	case KindPolicy:
		return "policy"
	default:
		return "conversation"
	}
}

// Valid reports whether k is a defined kind.
func (k Kind) Valid() bool { return k >= KindConversation && k <= KindPolicy }

// AllKinds returns every kind, in declaration order. Used by sweeps and by
// exhaustive tests.
func AllKinds() []Kind {
	return []Kind{KindConversation, KindSession, KindUser, KindBusiness,
		KindPreference, KindContact, KindScratchpad, KindPolicy}
}

// AllTiers returns every tier, in declaration order.
func AllTiers() []Tier { return []Tier{TierWorking, TierShortTerm, TierLongTerm} }

// Sensitivity classifies a memory's contents for handling.
//
// The vocabulary mirrors the platform's frozen annotations.proto rather than
// inventing a parallel scheme. Everything in this engine that gates access,
// export, audit or erasure reads this field.
type Sensitivity int

const (
	// Public carries no information about a person.
	Public Sensitivity = iota
	// Internal is operational data with no personal content.
	Internal
	// Personal relates to an identifiable individual.
	Personal
	// Sensitive is personal data whose disclosure creates elevated harm.
	Sensitive
	// Secret is authentication material. Never persisted here.
	Secret
)

// String renders the sensitivity for logs and audit entries.
func (s Sensitivity) String() string {
	switch s {
	case Internal:
		return "internal"
	case Personal:
		return "personal"
	case Sensitive:
		return "sensitive"
	case Secret:
		return "secret"
	default:
		return "public"
	}
}

// RequiresConsent reports whether a record of this sensitivity may only exist
// against a recorded consent basis.
//
// This is the DPDP obligation made structural: [Store] refuses a Personal or
// Sensitive record with no ConsentRef, so an unlawful memory cannot be created
// rather than being detected later by an audit.
func (s Sensitivity) RequiresConsent() bool { return s >= Personal }

// RequiresAudit reports whether every read must produce an audit entry.
func (s Sensitivity) RequiresAudit() bool { return s >= Sensitive }

// Retention names the deletion policy class, mirroring annotations.proto.
type Retention int

const (
	// RetentionEphemeral lives only for the current unit of work.
	RetentionEphemeral Retention = iota
	// RetentionShort is a diagnostic lifetime, measured in days.
	RetentionShort
	// RetentionStandard is the product lifetime the subject expects.
	RetentionStandard
	// RetentionLegalHold survives an erasure request under a legal obligation.
	RetentionLegalHold
)

// String renders the retention class.
func (r Retention) String() string {
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

// SurvivesErasure reports whether records of this class remain after a subject
// erasure request.
func (r Retention) SurvivesErasure() bool { return r == RetentionLegalHold }

// SubjectID identifies whose memory a record is — a subscriber, an
// organisation, or a contact. Opaque to this engine.
type SubjectID string

// String implements fmt.Stringer.
func (s SubjectID) String() string { return string(s) }

// Key uniquely identifies a record.
//
// It is a struct rather than an opaque string so that the index layer can
// partition on its components without parsing. Two records may share a Name
// across different subjects or kinds; the triple is what is unique.
type Key struct {
	// Subject is whose memory this is.
	Subject SubjectID
	// Kind is what it is about.
	Kind Kind
	// Name identifies it within (Subject, Kind). Authored by the caller.
	Name string
}

// String renders a stable, readable key for logs and index maps.
func (k Key) String() string {
	return string(k.Subject) + "/" + k.Kind.String() + "/" + k.Name
}

// Valid reports whether the key is well-formed.
func (k Key) Valid() bool {
	return k.Subject != "" && k.Kind.Valid() && k.Name != ""
}

// Version is a record's optimistic-lock counter. It increments on every
// mutation and never decreases.
type Version uint64

// Value is a record's payload.
//
// Data is deliberately []byte rather than any. A typed payload would tempt this
// engine into interpreting it, and interpretation is exactly what the brief
// excludes — no semantic search, no summarisation, no reasoning. The engine
// stores bytes with a stated content type and never looks inside.
type Value struct {
	// ContentType names the encoding, for example "application/json" or
	// "text/plain". The engine does not parse by it; consumers do.
	ContentType string

	// Data is the payload. Opaque.
	Data []byte

	// Attributes are indexable scalars extracted by the CALLER, not by this
	// engine. They exist so a secondary index is possible without the engine
	// parsing Data — the caller states what is searchable.
	//
	// Attribute values are indexed and therefore appear in index keys. They
	// must never carry Personal or Sensitive content; see INV-MEM-7.
	Attributes map[string]string
}

// Size returns the payload size in bytes, used for budget accounting.
func (v Value) Size() int {
	n := len(v.Data) + len(v.ContentType)
	for k, val := range v.Attributes {
		n += len(k) + len(val)
	}
	return n
}

// Provenance records where a memory came from.
//
// Kept because "why does it know that" is a question this engine must answer,
// and an unattributed long-term memory about a person is indefensible.
type Provenance struct {
	// Source names the producing subsystem, for example "conversation" or
	// "contacts_sync". Authored, closed vocabulary per deployment.
	Source string
	// Ref is an opaque identifier in the source system.
	Ref string
	// Derived reports whether this memory was inferred rather than stated.
	// An inferred memory about a person is weaker evidence and, under
	// DerivedRequiresConsent policy, may be held to a stricter basis.
	Derived bool
}

// Record is one memory.
type Record struct {
	// Key uniquely identifies it.
	Key Key

	// Tier is its lifetime class.
	Tier Tier

	// Value is its payload.
	Value Value

	// Sensitivity classifies its contents.
	Sensitivity Sensitivity

	// Retention names its deletion class.
	Retention Retention

	// ConsentRef references the consent record authorising this memory.
	// Mandatory for Personal and Sensitive; see INV-MEM-2.
	ConsentRef string

	// Provenance records where it came from.
	Provenance Provenance

	// Version increments on every mutation. Used for optimistic locking.
	Version Version

	// State is the lifecycle position.
	State State

	// CreatedAt is when the record was first stored.
	CreatedAt time.Time
	// UpdatedAt is the last mutation.
	UpdatedAt time.Time
	// AccessedAt is the last read. Drives demotion and eviction.
	AccessedAt time.Time
	// ExpiresAt is when the record becomes invisible. Zero means no expiry.
	ExpiresAt time.Time
	// ArchivedAt is when the record left the hot store.
	ArchivedAt time.Time

	// AccessCount is how many times the record has been read. Drives promotion.
	AccessCount uint64

	// Pinned prevents eviction and demotion. Used for records whose absence
	// would change meaning rather than merely reduce it.
	Pinned bool

	// Redacted reports that the payload has been removed while the record's
	// existence is retained. Distinct from deletion: a redacted record proves
	// something was here and is gone.
	Redacted bool
}

// Expired reports whether the record has passed its expiry.
func (r *Record) Expired(now time.Time) bool {
	return !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt)
}

// Age returns how long the record has existed.
func (r *Record) Age(now time.Time) time.Duration { return now.Sub(r.CreatedAt) }

// Idle returns how long since the record was last read.
func (r *Record) Idle(now time.Time) time.Duration { return now.Sub(r.AccessedAt) }

// Clone returns a deep copy.
//
// Every read path returns a clone. Handing out a pointer into the store would
// let a caller mutate a record without a version bump, defeating optimistic
// locking and every audit guarantee that depends on it.
func (r *Record) Clone() *Record {
	if r == nil {
		return nil
	}
	c := *r
	if r.Value.Data != nil {
		c.Value.Data = make([]byte, len(r.Value.Data))
		copy(c.Value.Data, r.Value.Data)
	}
	if r.Value.Attributes != nil {
		c.Value.Attributes = make(map[string]string, len(r.Value.Attributes))
		for k, v := range r.Value.Attributes {
			c.Value.Attributes[k] = v
		}
	}
	return &c
}

// validate reports every problem with a record, rather than the first.
func (r *Record) validate() []string {
	var p []string
	if !r.Key.Valid() {
		p = append(p, "record: key requires a subject, a valid kind and a name")
	}
	if !r.Tier.Valid() {
		p = append(p, fmt.Sprintf("record %s: tier %d is not valid", r.Key, r.Tier))
	}
	if r.Sensitivity == Secret {
		// The engine refuses to be a credential store. Authentication material
		// belongs in Identity, under its own handling, and a memory layer that
		// accepted it would become one more place a token can leak from.
		p = append(p, fmt.Sprintf("record %s: Secret material is never stored in memory", r.Key))
	}
	// CONSENT IS NOT CHECKED HERE, deliberately.
	//
	// validate answers "is this record well-formed"; Policy.admit answers "may
	// this record exist here". A missing consent reference is a policy refusal,
	// not a structural defect, and checking it in both places meant the
	// structural error shadowed the typed ErrConsentRequired sentinel — so a
	// caller testing errors.Is(err, ErrConsentRequired) never matched. Two
	// paths enforcing one rule is how the wrong one wins.
	if r.Key.Kind == KindScratchpad && r.Tier != TierWorking {
		p = append(p, fmt.Sprintf(
			"record %s: scratchpad memory is working-tier by definition", r.Key))
	}
	if r.Provenance.Source == "" {
		p = append(p, fmt.Sprintf("record %s: provenance source is required", r.Key))
	}
	for name := range r.Value.Attributes {
		if strings.TrimSpace(name) == "" {
			p = append(p, fmt.Sprintf("record %s: attribute names cannot be blank", r.Key))
		}
	}
	return p
}

// attributeNames returns the record's attribute names in sorted order, so index
// maintenance is deterministic.
func (r *Record) attributeNames() []string {
	if len(r.Value.Attributes) == 0 {
		return nil
	}
	out := make([]string, 0, len(r.Value.Attributes))
	for k := range r.Value.Attributes {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// The eleven named memory types
// ---------------------------------------------------------------------------

// The brief names eleven memory types. They are constructors over the
// [Kind] × [Tier] grid rather than eleven separate concepts — see the package
// doc for why. Each helper below fixes the axes that type implies and leaves
// the caller the rest.

// NewWorking returns a working-tier record of any kind.
func NewWorking(k Key, v Value, s Sensitivity) *Record {
	return newRecord(k, TierWorking, v, s, RetentionEphemeral)
}

// NewShortTerm returns a short-term record.
func NewShortTerm(k Key, v Value, s Sensitivity) *Record {
	return newRecord(k, TierShortTerm, v, s, RetentionShort)
}

// NewLongTerm returns a long-term record.
func NewLongTerm(k Key, v Value, s Sensitivity) *Record {
	return newRecord(k, TierLongTerm, v, s, RetentionStandard)
}

// NewConversation returns a conversation memory, short-term by default because
// a dialogue's working detail rarely deserves permanence without promotion.
func NewConversation(subject SubjectID, name string, v Value, s Sensitivity) *Record {
	return newRecord(Key{subject, KindConversation, name}, TierShortTerm, v, s, RetentionStandard)
}

// NewSession returns a session memory.
func NewSession(subject SubjectID, name string, v Value, s Sensitivity) *Record {
	return newRecord(Key{subject, KindSession, name}, TierShortTerm, v, s, RetentionShort)
}

// NewUser returns durable knowledge about a subscriber.
func NewUser(subject SubjectID, name string, v Value, s Sensitivity) *Record {
	return newRecord(Key{subject, KindUser, name}, TierLongTerm, v, s, RetentionStandard)
}

// NewBusiness returns organisation configuration or knowledge.
func NewBusiness(subject SubjectID, name string, v Value, s Sensitivity) *Record {
	return newRecord(Key{subject, KindBusiness, name}, TierLongTerm, v, s, RetentionStandard)
}

// NewPreference returns a stated choice, pinned by default.
//
// Pinned because a preference is something the subject ASKED for. Evicting it
// under memory pressure and silently reverting to a default is a worse failure
// than holding the memory, and it is the failure users notice.
func NewPreference(subject SubjectID, name string, v Value, s Sensitivity) *Record {
	r := newRecord(Key{subject, KindPreference, name}, TierLongTerm, v, s, RetentionStandard)
	r.Pinned = true
	return r
}

// NewContact returns knowledge about a third party.
func NewContact(subject SubjectID, name string, v Value, s Sensitivity) *Record {
	return newRecord(Key{subject, KindContact, name}, TierLongTerm, v, s, RetentionStandard)
}

// NewScratchpad returns intermediate decision state. Always working-tier.
func NewScratchpad(subject SubjectID, name string, v Value) *Record {
	return newRecord(Key{subject, KindScratchpad, name}, TierWorking, v, Internal, RetentionEphemeral)
}

// NewPolicy returns an operating rule, pinned and long-term.
func NewPolicy(subject SubjectID, name string, v Value) *Record {
	r := newRecord(Key{subject, KindPolicy, name}, TierLongTerm, v, Internal, RetentionLegalHold)
	r.Pinned = true
	return r
}

// newRecord builds a record with the common defaults. Timestamps are left zero
// and set by the store from its clock — a record cannot timestamp itself
// correctly, and letting it try is how two clocks appear in one system.
func newRecord(k Key, t Tier, v Value, s Sensitivity, ret Retention) *Record {
	return &Record{
		Key: k, Tier: t, Value: v, Sensitivity: s, Retention: ret,
		State: StateActive, Version: 0,
	}
}

// KindOf reports the kind a named memory type maps to, for callers translating
// the brief's vocabulary. Returns false for the three tier names, which are
// tiers rather than kinds.
func KindOf(namedType string) (Kind, bool) {
	switch strings.ToLower(namedType) {
	case "conversationmemory", "conversation":
		return KindConversation, true
	case "sessionmemory", "session":
		return KindSession, true
	case "usermemory", "user":
		return KindUser, true
	case "businessmemory", "business":
		return KindBusiness, true
	case "preferencememory", "preference":
		return KindPreference, true
	case "contactmemory", "contact":
		return KindContact, true
	case "scratchpadmemory", "scratchpad":
		return KindScratchpad, true
	case "policymemory", "policy":
		return KindPolicy, true
	default:
		return 0, false
	}
}
