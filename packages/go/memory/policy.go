package memory

import (
	"sort"
	"sync"
	"time"
)

// RetentionPolicy maps a retention class to a concrete lifetime.
//
// The class lives on the record and the duration lives here, so a policy change
// does not require rewriting every stored record — the same separation the
// frozen annotations.proto makes between a retention class and the schedule
// that interprets it.
type RetentionPolicy struct {
	// Durations per class. RetentionLegalHold has no duration: it does not
	// expire, and giving it one would be a way to accidentally delete the
	// records that must not be deleted.
	Durations map[Retention]time.Duration

	// PerKind overrides the class duration for a specific kind. A conversation
	// memory and a contact memory may both be RetentionStandard and still
	// deserve different lifetimes.
	PerKind map[Kind]time.Duration

	// SubjectMax caps every duration for a subject, so a subscriber who lowers
	// their retention preference lowers it everywhere at once. Zero means no
	// subject cap.
	SubjectMax time.Duration
}

// DefaultRetentionPolicy returns the schedule used unless overridden.
//
// The standard duration is 90 days, matching the frozen transcript retention in
// ADR-0012 rather than being chosen independently. A memory layer that outlived
// the transcripts it summarises would be a second, unregulated copy of the same
// personal data.
func DefaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{
		Durations: map[Retention]time.Duration{
			RetentionEphemeral: 5 * time.Minute,
			RetentionShort:     24 * time.Hour,
			RetentionStandard:  90 * 24 * time.Hour,
			// RetentionLegalHold is deliberately absent.
		},
		PerKind: map[Kind]time.Duration{
			KindScratchpad: 2 * time.Minute,
			KindSession:    2 * time.Hour,
		},
	}
}

// Lifetime returns the duration a record should live, and whether it expires at
// all.
func (p RetentionPolicy) Lifetime(r *Record) (time.Duration, bool) {
	if r.Retention == RetentionLegalHold {
		return 0, false
	}
	d, ok := p.Durations[r.Retention]
	if !ok {
		return 0, false
	}
	if kd, ok := p.PerKind[r.Key.Kind]; ok && kd < d {
		d = kd
	}
	if p.SubjectMax > 0 && p.SubjectMax < d {
		d = p.SubjectMax
	}
	return d, true
}

func (p RetentionPolicy) validate() []string {
	var out []string
	if _, ok := p.Durations[RetentionLegalHold]; ok {
		out = append(out, "retention: RetentionLegalHold must not have a duration; "+
			"giving it one is a way to accidentally delete records that must be kept")
	}
	for class, d := range p.Durations {
		if d <= 0 {
			out = append(out, "retention: duration for "+class.String()+" must be positive")
		}
	}
	for kind, d := range p.PerKind {
		if d <= 0 {
			out = append(out, "retention: per-kind duration for "+kind.String()+" must be positive")
		}
	}
	return out
}

// ConsentChecker reports whether a consent basis is currently valid.
//
// An interface because consent lives in the Identity context, not here. This
// engine enforces that a basis EXISTS and is current; it does not own the
// consent record, and reaching into another context's store to check would be
// exactly the cross-context read the platform's architecture forbids.
//
// A nil checker means consent references are accepted as opaque strings without
// validation — appropriate in test and in a deployment where the caller has
// already validated, and documented so the weaker mode is a choice rather than
// a surprise.
type ConsentChecker interface {
	// Valid reports whether the reference names a currently-granted consent
	// for this subject.
	Valid(subject SubjectID, consentRef string) bool
}

// Encryptor is the encryption hook.
//
// NOT IMPLEMENTED HERE, deliberately. Key management belongs to the platform's
// KMS integration (ADR-0009 §10: customer-managed keys, key policy separate
// from data-plane IAM). A memory engine that shipped its own crypto would be
// both out of scope and the wrong place for the decision.
//
// The hook exists so that at-rest encryption is a wiring choice rather than a
// rewrite. A nil Encryptor stores payloads as supplied.
type Encryptor interface {
	// Encrypt transforms a payload for storage.
	Encrypt(subject SubjectID, plaintext []byte) ([]byte, error)
	// Decrypt reverses Encrypt.
	Decrypt(subject SubjectID, ciphertext []byte) ([]byte, error)
}

// AuditEvent records an access to protected data.
type AuditEvent struct {
	// Key identifies the record touched.
	Key Key
	// Operation names what was done.
	Operation string
	// Sensitivity classifies what was reached.
	Sensitivity Sensitivity
	// Actor names who did it, supplied by the caller. Opaque here.
	Actor string
	// At is the instant.
	At time.Time
	// Granted reports whether the access succeeded.
	Granted bool
	// Reason explains a refusal.
	Reason string
}

// Auditor receives audit events.
//
// Separate from [Publisher] because an audit trail has different durability,
// retention and access requirements from an operational event stream. Sending
// audit records down the same pipe as change notifications would give the
// weakest consumer of that pipe access to the strongest record in the system.
type Auditor interface {
	// Record writes one audit event. It must not block.
	Record(AuditEvent)
}

// NoopAuditor discards audit events. The default, so the engine runs without an
// audit sink configured — but see the note on Policy.RequireAuditor.
type NoopAuditor struct{}

// Record discards the event.
func (NoopAuditor) Record(AuditEvent) {}

// RecordingAuditor captures audit events for assertion.
type RecordingAuditor struct {
	mu     sync.Mutex
	events []AuditEvent
}

// NewRecordingAuditor returns an auditor that records what it receives.
func NewRecordingAuditor() *RecordingAuditor { return &RecordingAuditor{} }

// Record captures the event.
func (a *RecordingAuditor) Record(e AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

// Events returns a copy of everything recorded.
func (a *RecordingAuditor) Events() []AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]AuditEvent, len(a.events))
	copy(out, a.events)
	return out
}

// Count returns how many events were recorded.
func (a *RecordingAuditor) Count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.events)
}

// RedactionReason names why a payload was destroyed.
type RedactionReason string

// The redaction reasons the engine understands.
const (
	// RedactConsentWithdrawn is the subject withdrawing the basis.
	RedactConsentWithdrawn RedactionReason = "consent_withdrawn"
	// RedactSubjectRequest is a direct request to forget one thing.
	RedactSubjectRequest RedactionReason = "subject_request"
	// RedactPolicy is an operator or automated policy action.
	RedactPolicy RedactionReason = "policy"
	// RedactRetention is the retention schedule reclaiming content while a
	// legal hold keeps the record's existence.
	RedactRetention RedactionReason = "retention"
)

// Policy is the engine's complete governance configuration.
type Policy struct {
	// Retention maps classes to lifetimes.
	Retention RetentionPolicy

	// Promotion governs tier movement.
	Promotion PromotionPolicy

	// Consent validates consent references. Nil disables validation.
	Consent ConsentChecker

	// Encryptor transforms payloads at rest. Nil stores as supplied.
	Encryptor Encryptor

	// Auditor receives access records for Sensitive data. Nil discards them.
	Auditor Auditor

	// RequireAuditor refuses to start without an auditor when any Sensitive
	// data could be stored.
	//
	// Defaults to TRUE. An engine holding Sensitive data with no audit trail
	// cannot answer "who read this", which is a DPDP obligation and not an
	// operational nicety. A deployment that genuinely stores nothing sensitive
	// may turn it off, deliberately.
	RequireAuditor bool

	// AllowDerivedLongTerm permits inferred memories to reach the long-term
	// tier. Mirrors PromotionPolicy.PromoteDerived and is checked at write as
	// well as at promotion, so a caller cannot create a long-term derived
	// record directly and bypass the promotion gate.
	AllowDerivedLongTerm bool

	// MaxRecordBytes bounds one payload.
	MaxRecordBytes int

	// MaxRecordsPerSubject bounds a subject's footprint. Zero is unbounded.
	MaxRecordsPerSubject int
}

// DefaultPolicy returns the governance configuration used unless overridden.
func DefaultPolicy() Policy {
	return Policy{
		Retention:            DefaultRetentionPolicy(),
		Promotion:            DefaultPromotionPolicy(),
		RequireAuditor:       true,
		AllowDerivedLongTerm: false,
		MaxRecordBytes:       256 * 1024,
		MaxRecordsPerSubject: 50_000,
	}
}

func (p Policy) validate() []string {
	var out []string
	out = append(out, p.Retention.validate()...)
	out = append(out, p.Promotion.validate()...)
	if p.MaxRecordBytes <= 0 {
		out = append(out, "policy: MaxRecordBytes must be positive")
	}
	if p.MaxRecordsPerSubject < 0 {
		out = append(out, "policy: MaxRecordsPerSubject cannot be negative")
	}
	if p.RequireAuditor && p.Auditor == nil {
		out = append(out, "policy: RequireAuditor is set but no Auditor is configured; "+
			"an engine holding Sensitive data with no audit trail cannot answer "+
			"'who read this', which is an obligation rather than a nicety")
	}
	sort.Strings(out)
	return out
}

// admit decides whether a record may be written, and returns the refusal
// reason when it may not.
//
// The order is deliberate: cheap structural checks first, consent last, so a
// malformed record is refused without consulting an external checker.
func (p Policy) admit(r *Record) error {
	if problems := r.validate(); len(problems) > 0 {
		return &ConfigError{Problems: problems}
	}
	if r.Value.Size() > p.MaxRecordBytes {
		return invariant("INV-MEM-8",
			"record %s is %d bytes, over the %d-byte limit",
			r.Key, r.Value.Size(), p.MaxRecordBytes)
	}
	if r.Provenance.Derived && r.Tier == TierLongTerm && !p.AllowDerivedLongTerm {
		// Checked at write as well as at promotion. Without this a caller could
		// create a long-term derived record directly and bypass the promotion
		// gate entirely — the guard has to sit on every path into the tier, not
		// just the one that walks up to it.
		return invariant("INV-MEM-6",
			"record %s is derived and long-term; the platform does not permanently "+
				"remember inferences about a person by default", r.Key)
	}

	// INVARIANT INV-MEM-2. Personal and Sensitive data requires a basis, and
	// the refusal happens at write. An unlawful memory is never created and
	// then discovered by an audit.
	if r.Sensitivity.RequiresConsent() {
		if r.ConsentRef == "" {
			return ErrConsentRequired
		}
		if p.Consent != nil && !p.Consent.Valid(r.Key.Subject, r.ConsentRef) {
			return ErrConsentRequired
		}
	}
	return nil
}

// staticConsent is a ConsentChecker over a fixed allow list. Exported through
// NewStaticConsent for tests and for deployments with a small, static basis set.
type staticConsent struct {
	mu    sync.RWMutex
	valid map[SubjectID]map[string]bool
}

// NewStaticConsent returns a ConsentChecker backed by an explicit allow list.
func NewStaticConsent() *staticConsent {
	return &staticConsent{valid: make(map[SubjectID]map[string]bool)}
}

// Grant marks a consent reference valid for a subject.
func (s *staticConsent) Grant(subject SubjectID, ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.valid[subject] == nil {
		s.valid[subject] = make(map[string]bool)
	}
	s.valid[subject][ref] = true
}

// Withdraw marks a consent reference invalid.
func (s *staticConsent) Withdraw(subject SubjectID, ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.valid[subject]; m != nil {
		delete(m, ref)
	}
}

// Valid implements ConsentChecker.
func (s *staticConsent) Valid(subject SubjectID, ref string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.valid[subject][ref]
}
