package governance

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// ConsentState is a consent record's stage.
type ConsentState uint8

// The consent states.
const (
	// ConsentGranted is live and valid.
	ConsentGranted ConsentState = iota

	// ConsentExpired lapsed with time. Distinct from revoked because the
	// distinction is legal and operational: a lapsed consent can be renewed by
	// asking again, a revoked one means the subject said no.
	ConsentExpired

	// ConsentRevoked was withdrawn by the subject.
	ConsentRevoked

	// ConsentSuperseded was given against terms that have since changed.
	// NOT automatically valid for the new terms: consenting to one thing is
	// not consenting to a different thing that happens to have the same name.
	ConsentSuperseded
)

// String renders the state.
func (s ConsentState) String() string {
	switch s {
	case ConsentExpired:
		return "expired"
	case ConsentRevoked:
		return "revoked"
	case ConsentSuperseded:
		return "superseded"
	default:
		return "granted"
	}
}

// Valid reports whether the state permits processing.
func (s ConsentState) Valid() bool { return s == ConsentGranted }

// consentTransitions declares every legal state change.
//
// One table, and the ABSENT edges carry the weight:
//
//	Revoked → Granted     does not exist. Re-consenting mints a NEW record with
//	                      a new identifier, so the revocation stays in the
//	                      history. A revocation that can be erased by a later
//	                      grant is a revocation nobody can prove happened.
//
//	Expired → Granted     does not exist, for the same reason. Renewal is a new
//	                      record; silently reviving an expired one would make
//	                      "when did they consent" unanswerable.
//
//	Superseded → Granted  does not exist. New terms need a new agreement.
func consentTransitions() map[ConsentState][]ConsentState {
	return map[ConsentState][]ConsentState{
		ConsentGranted:    {ConsentExpired, ConsentRevoked, ConsentSuperseded},
		ConsentExpired:    {ConsentRevoked},
		ConsentRevoked:    {},
		ConsentSuperseded: {ConsentRevoked},
	}
}

// canTransition reports whether a consent state change is declared.
//
// A pure function over the table rather than an FSM instance per record: ten
// thousand consent records would otherwise mean ten thousand state machines
// holding ten thousand copies of one immutable table.
func canTransition(from, to ConsentState) bool {
	for _, allowed := range consentTransitions()[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// ConsentRecord is one subject's consent to one basis.
type ConsentRecord struct {
	// ID identifies this record. A new one is minted on every grant, including
	// renewals, so the history is append-only in effect.
	ID ConsentID

	// Subject is who consented.
	Subject SubjectID

	// Basis names what was consented to: "call_recording", "contact_sync".
	// The vocabulary is the platform's, not this engine's.
	Basis string

	// TermsVersion is the version of the terms the subject agreed to.
	// REQUIRED: consent without a terms version is consent to something
	// nobody can reconstruct.
	TermsVersion int

	// State is the stage.
	State ConsentState

	// Purpose narrows what the consent covers. Empty means the basis alone
	// defines it.
	Purpose string

	// Method records how it was obtained: "ivr", "app", "web", "written".
	// A bounded vocabulary by convention; it becomes a metric label.
	Method string

	// GrantedAt, ExpiresAt and RevokedAt are engine-clock instants.
	GrantedAt time.Time
	ExpiresAt time.Time
	RevokedAt time.Time

	// Evidence fingerprints the artefact proving consent — a recording, a
	// signed form. THE FINGERPRINT, NOT THE ARTEFACT: the artefact is personal
	// data and belongs in the system that captured it, under that system's
	// retention.
	Evidence Fingerprint
}

func (c ConsentRecord) validate() []string {
	var problems []string
	if c.Subject == "" {
		problems = append(problems, "consent: subject is required")
	}
	if c.Basis == "" {
		problems = append(problems, "consent: basis is required")
	}
	if c.TermsVersion < 1 {
		problems = append(problems, fmt.Sprintf(
			"consent %s/%s: TermsVersion must be at least 1; consent with no terms "+
				"version is consent to something nobody can reconstruct", c.Subject, c.Basis))
	}
	if c.Method == "" {
		problems = append(problems, fmt.Sprintf(
			"consent %s/%s: method is required; how consent was obtained is the first "+
				"thing a regulator asks", c.Subject, c.Basis))
	}
	if !c.ExpiresAt.IsZero() && !c.GrantedAt.IsZero() && !c.GrantedAt.Before(c.ExpiresAt) {
		problems = append(problems, fmt.Sprintf(
			"consent %s/%s: expiry is not after grant", c.Subject, c.Basis))
	}
	return problems
}

// Live reports whether the record permits processing at an instant.
func (c ConsentRecord) Live(now time.Time) bool {
	if c.State != ConsentGranted {
		return false
	}
	if !c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt) {
		return false
	}
	return true
}

// key is the registry index key.
func (c ConsentRecord) key() consentKey {
	return consentKey{Subject: c.Subject, Basis: c.Basis}
}

type consentKey struct {
	Subject SubjectID
	Basis   string
}

// ConsentRegistry holds consent records.
//
// One LIVE record per subject and basis, plus the full history. The history is
// the point: a governance engine that can say "consent is valid" but not "and
// here is when it was given, under which terms, and by what method" cannot
// answer a DPDP request.
type ConsentRegistry struct {
	clock   rt.Clock
	metrics *Metrics
	audit   Auditor

	// termsVersion is the current terms version per basis. A record against an
	// older version is superseded on the next check rather than in a batch
	// job, so the transition is visible at the moment it matters.
	mu      sync.RWMutex
	live    map[consentKey]*ConsentRecord
	history map[consentKey][]ConsentRecord
	terms   map[string]int
}

// NewConsentRegistry builds an empty registry.
func NewConsentRegistry(clock rt.Clock, metrics *Metrics, audit Auditor) *ConsentRegistry {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	return &ConsentRegistry{
		clock: clock, metrics: metrics, audit: audit,
		live:    make(map[consentKey]*ConsentRecord),
		history: make(map[consentKey][]ConsentRecord),
		terms:   make(map[string]int),
	}
}

// SetTermsVersion declares the current terms version for a basis.
//
// Raising it SUPERSEDES every record against an older version — not by
// deleting them, but by making them fail the next check with
// [ErrConsentSuperseded]. The subject must be asked again.
//
// That is deliberately disruptive. A platform that could quietly carry old
// consent forward across changed terms would have no reason ever to ask again.
func (r *ConsentRegistry) SetTermsVersion(basis string, version int) error {
	if basis == "" {
		return &ConfigError{Problems: []string{"consent: basis is required"}}
	}
	if version < 1 {
		return &ConfigError{Problems: []string{"consent: terms version must be at least 1"}}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.terms[basis]; ok && version < current {
		return &ConfigError{Problems: []string{fmt.Sprintf(
			"consent %s: terms version %d is below the current %d; terms do not go "+
				"backwards, and pretending they do would revalidate consent that was "+
				"superseded", basis, version, current)}}
	}
	r.terms[basis] = version
	return nil
}

// TermsVersion returns the current terms version for a basis, defaulting to 1.
func (r *ConsentRegistry) TermsVersion(basis string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if v, ok := r.terms[basis]; ok {
		return v
	}
	return 1
}

// Grant records consent.
//
// Always mints a NEW record, even when one exists. The previous record moves to
// history rather than being mutated, so "when did they consent, and to what"
// has an answer for every point in time.
func (r *ConsentRegistry) Grant(c ConsentRecord) (ConsentRecord, error) {
	now := r.clock.Now()
	if c.GrantedAt.IsZero() {
		c.GrantedAt = now
	}
	if c.TermsVersion == 0 {
		c.TermsVersion = r.TermsVersion(c.Basis)
	}
	if problems := c.validate(); len(problems) > 0 {
		sort.Strings(problems)
		return ConsentRecord{}, &ConfigError{Problems: problems}
	}

	c.ID = NewConsentID()
	c.State = ConsentGranted
	c.RevokedAt = time.Time{}

	r.mu.Lock()
	key := c.key()
	if prev, ok := r.live[key]; ok {
		superseded := *prev
		if superseded.State == ConsentGranted {
			superseded.State = ConsentSuperseded
		}
		r.history[key] = append(r.history[key], superseded)
	}
	stored := c
	r.live[key] = &stored
	count := len(r.live)
	r.mu.Unlock()

	if r.metrics != nil {
		r.metrics.ConsentGrants.Inc(c.Basis)
		r.metrics.ConsentRecords.Set(float64(count))
	}
	r.record(AuditConsentGranted, c, "granted")
	return c, nil
}

// Revoke withdraws consent.
//
// Revocation is immediate and takes effect on the very next check. There is no
// cache and no grace period, because a subject who withdraws consent has
// withdrawn it — and a system that keeps processing for another five minutes
// is processing without a basis for five minutes.
func (r *ConsentRegistry) Revoke(subject SubjectID, basis, reason string) (ConsentRecord, error) {
	key := consentKey{Subject: subject, Basis: basis}

	r.mu.Lock()
	rec, ok := r.live[key]
	if !ok {
		r.mu.Unlock()
		return ConsentRecord{}, fmt.Errorf("%w: %s/%s", ErrConsentNotFound, subject, basis)
	}
	if rec.State == ConsentRevoked {
		out := *rec
		r.mu.Unlock()
		// Revoking twice is not an error. A subject repeating themselves is
		// not a fault condition, and returning one would push callers into
		// checking first, which is a race.
		return out, nil
	}
	if !canTransition(rec.State, ConsentRevoked) {
		state := rec.State
		r.mu.Unlock()
		return ConsentRecord{}, invariant("INV-GOV-4",
			"consent %s/%s cannot move from %s to revoked", subject, basis, state)
	}

	rec.State = ConsentRevoked
	rec.RevokedAt = r.clock.Now()
	out := *rec
	r.mu.Unlock()

	if r.metrics != nil {
		r.metrics.ConsentRevokes.Inc(basis)
	}
	r.record(AuditConsentRevoked, out, reason)
	return out, nil
}

// ConsentCheck is the result of a check.
type ConsentCheck struct {
	// Valid reports whether processing may proceed.
	Valid bool
	// State is the record's state, or Revoked-equivalent when absent.
	State ConsentState
	// Record is the live record, zero when none exists.
	Record ConsentRecord
	// Reason is a short machine-readable code.
	Reason string
	// RequiredTerms is the current terms version, populated when the record is
	// superseded so a caller knows what to ask for.
	RequiredTerms int
	// Err is the typed error, nil when valid.
	Err error
}

// Check reports whether a subject's consent to a basis is currently valid.
//
// FOUR DISTINCT NEGATIVE OUTCOMES, and the distinction is not pedantry:
//
//	not found   the subject was never asked          → ask
//	expired     they agreed and it lapsed            → ask again
//	revoked     they said no                         → do not ask again casually
//	superseded  they agreed to different terms       → ask about the new terms
//
// Collapsing them into "no consent" means a platform that asks a subject who
// revoked to consent again, which is exactly the behaviour that turns a privacy
// control into a nuisance and then into a complaint.
func (r *ConsentRegistry) Check(subject SubjectID, basis string) ConsentCheck {
	now := r.clock.Now()
	key := consentKey{Subject: subject, Basis: basis}

	r.mu.RLock()
	rec, ok := r.live[key]
	currentTerms, hasTerms := r.terms[basis]
	var snapshot ConsentRecord
	if ok {
		snapshot = *rec
	}
	r.mu.RUnlock()

	if !hasTerms {
		currentTerms = 1
	}

	var check ConsentCheck
	switch {
	case !ok:
		check = ConsentCheck{State: ConsentRevoked, Reason: "not_found",
			Err: fmt.Errorf("%w: %s/%s", ErrConsentNotFound, subject, basis)}
	case snapshot.State == ConsentRevoked:
		check = ConsentCheck{State: ConsentRevoked, Record: snapshot, Reason: "revoked",
			Err: fmt.Errorf("%w: %s/%s", ErrConsentRevoked, subject, basis)}
	case snapshot.TermsVersion < currentTerms:
		check = ConsentCheck{State: ConsentSuperseded, Record: snapshot,
			Reason: "superseded", RequiredTerms: currentTerms,
			Err: fmt.Errorf("%w: %s/%s agreed to terms v%d, current is v%d",
				ErrConsentSuperseded, subject, basis, snapshot.TermsVersion, currentTerms)}
	case !snapshot.Live(now):
		check = ConsentCheck{State: ConsentExpired, Record: snapshot, Reason: "expired",
			Err: fmt.Errorf("%w: %s/%s", ErrConsentExpired, subject, basis)}
	default:
		check = ConsentCheck{Valid: true, State: ConsentGranted, Record: snapshot,
			Reason: "valid"}
	}

	if r.metrics != nil {
		r.metrics.ConsentChecks.Inc(check.Reason)
	}
	return check
}

// Sweep moves lapsed records to the expired state and returns how many moved.
//
// Expiry is detected at check time as well, so a sweep is not required for
// correctness — it exists so that expiry produces an AUDIT ENTRY and a metric
// at roughly the time it happens, rather than only when somebody next asks.
// A consent that lapsed three weeks ago and was never checked is still a fact
// worth recording.
func (r *ConsentRegistry) Sweep() int {
	now := r.clock.Now()

	r.mu.Lock()
	var expired []ConsentRecord
	for _, rec := range r.live {
		if rec.State != ConsentGranted {
			continue
		}
		if rec.ExpiresAt.IsZero() || now.Before(rec.ExpiresAt) {
			continue
		}
		if !canTransition(rec.State, ConsentExpired) {
			continue
		}
		rec.State = ConsentExpired
		expired = append(expired, *rec)
	}
	r.mu.Unlock()

	// Sorted so two sweeps over the same state produce the same audit order.
	sort.Slice(expired, func(i, j int) bool {
		if expired[i].Subject != expired[j].Subject {
			return expired[i].Subject < expired[j].Subject
		}
		return expired[i].Basis < expired[j].Basis
	})

	for _, rec := range expired {
		if r.metrics != nil {
			r.metrics.ConsentExpiries.Inc(rec.Basis)
		}
		r.record(AuditConsentExpired, rec, "expired")
	}
	return len(expired)
}

// History returns every record for a subject and basis, oldest first, with the
// live record last.
//
// This is the DPDP access-request answer: what did they agree to, when, under
// which terms, by what method, and when did it end.
func (r *ConsentRegistry) History(subject SubjectID, basis string) []ConsentRecord {
	key := consentKey{Subject: subject, Basis: basis}
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := append([]ConsentRecord(nil), r.history[key]...)
	if rec, ok := r.live[key]; ok {
		out = append(out, *rec)
	}
	return out
}

// ForSubject returns every live record for a subject, sorted by basis.
func (r *ConsentRegistry) ForSubject(subject SubjectID) []ConsentRecord {
	r.mu.RLock()
	var out []ConsentRecord
	for key, rec := range r.live {
		if key.Subject == subject {
			out = append(out, *rec)
		}
	}
	r.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].Basis < out[j].Basis })
	return out
}

// RevokeAll withdraws every consent a subject holds, returning what it revoked.
//
// The erasure-adjacent operation: a subject withdrawing everything must not
// have to enumerate bases they never knew existed.
func (r *ConsentRegistry) RevokeAll(subject SubjectID, reason string) []ConsentRecord {
	var bases []string
	r.mu.RLock()
	for key, rec := range r.live {
		if key.Subject == subject && rec.State != ConsentRevoked {
			bases = append(bases, key.Basis)
		}
	}
	r.mu.RUnlock()

	// Sorted so the audit trail of a bulk revocation is deterministic.
	sort.Strings(bases)

	out := make([]ConsentRecord, 0, len(bases))
	for _, basis := range bases {
		if rec, err := r.Revoke(subject, basis, reason); err == nil {
			out = append(out, rec)
		}
	}
	return out
}

// Len returns the live record count.
func (r *ConsentRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.live)
}

func (r *ConsentRegistry) record(kind AuditKind, c ConsentRecord, reason string) {
	if r.audit == nil {
		return
	}
	err := r.audit.Record(AuditEntry{
		At: r.clock.Now(), Kind: kind, Subject: c.Subject, Reason: reason,
		Details: map[string]string{
			"basis":    c.Basis,
			"terms":    fmt.Sprint(c.TermsVersion),
			"method":   c.Method,
			"consent":  string(c.ID),
			"evidence": string(c.Evidence),
		},
	})
	if r.metrics == nil {
		return
	}
	if err != nil {
		r.metrics.AuditFailed.Inc(string(kind))
		return
	}
	r.metrics.AuditWritten.Inc(string(kind))
}

// String renders a record for diagnostics.
func (c ConsentRecord) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s/%s v%d %s via %s", c.Subject, c.Basis, c.TermsVersion, c.State, c.Method)
	if !c.ExpiresAt.IsZero() {
		fmt.Fprintf(&b, " until %s", c.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return b.String()
}
