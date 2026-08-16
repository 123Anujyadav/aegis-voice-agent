package evaluation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// ---------------------------------------------------------------------------
// Schema versioning
// ---------------------------------------------------------------------------

// SchemaVersion identifies the on-disk shape of a [Record].
//
// Stamped on every record rather than held once for the store, because a store
// is migrated incrementally and a half-migrated store must remain readable. A
// single store-wide version would make the migration a stop-the-world event and
// would lie about the records it had not reached yet.
type SchemaVersion int

// CurrentSchema is the version this build writes.
const CurrentSchema SchemaVersion = 1

// MinReadableSchema is the oldest version this build can read.
//
// Anything older must be migrated before it can be loaded. Refusing to guess at
// an unknown ancestor's layout is deliberate: a golden decoded under the wrong
// schema produces a WRONG BASELINE, and a wrong baseline is worse than a missing
// one because it fails silently and in the direction of "everything is fine".
const MinReadableSchema SchemaVersion = 1

// ---------------------------------------------------------------------------
// Records
// ---------------------------------------------------------------------------

// RecordKind classifies what a stored record holds.
type RecordKind string

// The record kinds. One per durable artifact the platform produces.
const (
	RecordRun         RecordKind = "run"
	RecordObservation RecordKind = "observation"
	RecordGolden      RecordKind = "golden"
	RecordBenchmark   RecordKind = "benchmark"
	RecordTrend       RecordKind = "trend"
	// RecordAudit is the deletion and hold trail. It is a record kind so that
	// it lives under the same retention machinery as everything else — and then
	// is deliberately exempted from expiry, which is a decision that has to be
	// visible rather than implicit in a different storage path.
	RecordAudit RecordKind = "audit"
)

// AllRecordKinds returns every kind, in a stable order.
func AllRecordKinds() []RecordKind {
	return []RecordKind{RecordRun, RecordObservation, RecordGolden,
		RecordBenchmark, RecordTrend, RecordAudit}
}

// Record is the durable envelope.
//
// THE PAYLOAD IS OPAQUE TO THE REPOSITORY. A repository stores bytes and
// indexes the envelope; it never decodes the payload. That is what keeps the
// storage layer free of the platform's domain types, and it is why adding a
// field to Observation does not require a repository change — only a schema
// bump if the encoding changed incompatibly.
type Record struct {
	// Schema is the payload's encoding version.
	Schema SchemaVersion
	// Kind classifies the record.
	Kind RecordKind
	// ID is unique within a kind.
	ID string

	// Scenario and Subject index the record. Empty where a kind has no natural
	// scenario — a run spans many.
	Scenario ScenarioID
	Subject  SubjectName
	// Suite indexes runs.
	Suite SuiteID
	// Label carries the operator's run label, so a release can be found by name.
	Label string

	// CreatedAt is when the artifact was produced. THE RETENTION CLOCK STARTS
	// HERE, not at insertion: a record backfilled from an export must age from
	// when it happened, or an import would silently reset every retention
	// deadline in the store.
	CreatedAt time.Time

	// ExpiresAt is when retention permits deletion. Zero means "never expires",
	// which is correct for audit records and for anything under an explicit
	// indefinite policy.
	ExpiresAt time.Time

	// LegalHold suspends deletion regardless of ExpiresAt. See [Repository].
	LegalHold bool

	// Payload is the encoded artifact.
	Payload []byte
}

// Expired reports whether the record may be deleted at the given instant.
//
// A record under legal hold is NEVER expired, whatever its deadline says. The
// check lives on the record rather than in each repository implementation so
// that a new backend cannot forget it — the most likely way a hold gets broken
// is a second implementation that reimplements the sweep.
func (r Record) Expired(now time.Time) bool {
	if r.LegalHold {
		return false
	}
	if r.ExpiresAt.IsZero() {
		return false
	}
	return !now.Before(r.ExpiresAt)
}

// Readable reports whether this build can decode the record.
func (r Record) Readable() bool {
	return r.Schema >= MinReadableSchema && r.Schema <= CurrentSchema
}

// Key returns the record's identity within the store.
func (r Record) Key() RecordKey { return RecordKey{Kind: r.Kind, ID: r.ID} }

// RecordKey identifies a record.
type RecordKey struct {
	Kind RecordKind
	ID   string
}

// String renders the key.
func (k RecordKey) String() string { return string(k.Kind) + "/" + k.ID }

// ---------------------------------------------------------------------------
// Queries
// ---------------------------------------------------------------------------

// Query selects records. A zero Query matches everything.
//
// Deliberately small. A rich query language in a storage port becomes a
// second, worse SQL that every backend has to implement identically — and the
// backends will not implement it identically. These five predicates are what
// the platform's read paths actually need.
type Query struct {
	// Kinds restricts by record kind. Empty matches all.
	Kinds []RecordKind
	// Scenario, Subject and Suite restrict by index. Empty matches all.
	Scenario ScenarioID
	Subject  SubjectName
	Suite    SuiteID
	// Since and Until bound CreatedAt. Zero means unbounded.
	Since time.Time
	Until time.Time
	// IncludeUnreadable returns records this build cannot decode. Off by
	// default so an ordinary read never hands back a payload the caller will
	// misinterpret; a migration turns it on precisely because it must see them.
	IncludeUnreadable bool
	// Limit caps the result. Zero means no cap.
	Limit int
}

func (q Query) matches(r Record) bool {
	if len(q.Kinds) > 0 {
		var ok bool
		for _, k := range q.Kinds {
			if k == r.Kind {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if q.Scenario != "" && r.Scenario != q.Scenario {
		return false
	}
	if q.Subject != "" && r.Subject != q.Subject {
		return false
	}
	if q.Suite != "" && r.Suite != q.Suite {
		return false
	}
	if !q.Since.IsZero() && r.CreatedAt.Before(q.Since) {
		return false
	}
	if !q.Until.IsZero() && !r.CreatedAt.Before(q.Until) {
		return false
	}
	if !q.IncludeUnreadable && !r.Readable() {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Repository
// ---------------------------------------------------------------------------

// Repository is the durable persistence port.
//
// NOT BOUND TO A DATABASE. Nothing in this interface mentions SQL, a
// connection, a transaction or a driver. An Aurora implementation, a
// filesystem implementation and the in-memory one in this file are all
// substitutable, and the platform's engines depend only on this.
//
// Two rules a backend must honour, both of which the platform relies on and
// neither of which the type system can enforce:
//
//  1. A record under legal hold is never deleted, by expiry sweep or otherwise.
//     [Record.Expired] is provided so no implementation has to restate the rule.
//  2. Deletions are audited. Whatever removes a record writes a [RecordAudit]
//     describing what went and why, in the same operation where the backend can
//     make it atomic.
type Repository interface {
	// Put stores or replaces a record.
	Put(ctx context.Context, rec Record) error
	// PutBatch stores many. Backends with transactions should make it atomic;
	// the interface does not promise atomicity, because a backend that cannot
	// provide it should not be excluded and should not lie about it.
	PutBatch(ctx context.Context, recs []Record) error
	// Get retrieves one record.
	Get(ctx context.Context, key RecordKey) (Record, error)
	// List retrieves matching records, newest first.
	List(ctx context.Context, q Query) ([]Record, error)
	// Count returns how many records match, without materialising them.
	Count(ctx context.Context, q Query) (int, error)
	// Delete removes one record. Refuses a record under legal hold.
	Delete(ctx context.Context, key RecordKey) error

	// SetLegalHold places or lifts a hold, and audits either way.
	SetLegalHold(ctx context.Context, key RecordKey, hold bool, by, reason string) error

	// Sweep deletes everything expired at `now` and returns what it removed.
	Sweep(ctx context.Context, now time.Time) (SweepReport, error)

	// Schema reports the lowest and highest schema versions present, so a
	// caller can tell whether a migration is outstanding without scanning.
	Schema(ctx context.Context) (low, high SchemaVersion, err error)
	// Migrate applies migrations to every record below CurrentSchema.
	Migrate(ctx context.Context, m Migrator) (MigrationReport, error)

	// Snapshot captures the whole store.
	Snapshot(ctx context.Context) (StoreSnapshot, error)
	// Restore replaces the store's contents with a snapshot.
	Restore(ctx context.Context, snap StoreSnapshot) error

	// Close releases resources. Safe to call more than once.
	Close() error
}

// Persistence errors.
var (
	// ErrRecordNotFound is returned by Get and Delete.
	ErrRecordNotFound = errors.New("evaluation: record not found")
	// ErrLegalHold is returned when a deletion is refused.
	ErrLegalHold = errors.New("evaluation: record is under legal hold")
	// ErrSchemaTooNew is returned when a record was written by a later build.
	ErrSchemaTooNew = errors.New("evaluation: record schema is newer than this build")
	// ErrClosed is returned by a closed repository.
	ErrClosed = errors.New("evaluation: repository is closed")
)

// ---------------------------------------------------------------------------
// Snapshots
// ---------------------------------------------------------------------------

// StoreSnapshot is a point-in-time capture of a repository.
//
// Used for backup, for moving a golden set between environments, and — the case
// that motivated it — for taking a copy before a migration. A migration that
// goes wrong on approved baselines is not recoverable by re-running anything;
// the baselines encode human decisions that no process can reproduce.
type StoreSnapshot struct {
	// Schema is the version the snapshot was taken at.
	Schema SchemaVersion
	// TakenAt stamps it.
	TakenAt time.Time
	// Records is the full contents, ordered by kind then ID for a stable diff.
	Records []Record
	// Counts summarises by kind, so a restore can be sanity-checked without
	// walking the payloads.
	Counts map[RecordKind]int
}

// Summary renders the snapshot.
func (s StoreSnapshot) Summary() string {
	kinds := make([]string, 0, len(s.Counts))
	for _, k := range AllRecordKinds() {
		if n, ok := s.Counts[k]; ok && n > 0 {
			kinds = append(kinds, fmt.Sprintf("%s=%d", k, n))
		}
	}
	return fmt.Sprintf("snapshot at %s schema=v%d records=%d [%s]",
		s.TakenAt.UTC().Format(time.RFC3339), s.Schema, len(s.Records),
		strings.Join(kinds, " "))
}

// ---------------------------------------------------------------------------
// Migration
// ---------------------------------------------------------------------------

// Migration upgrades a record from one schema version to the next.
//
// ONE STEP AT A TIME. A migration from v1 to v4 is three registered migrations,
// not one, so a store at v2 shares the v2→v3 and v3→v4 code with a store at v1
// rather than needing its own path. The alternative — a migration per source
// version — is where the combinations become untestable.
type Migration struct {
	// From is the version this migration reads.
	From SchemaVersion
	// To is the version it produces. Must be From+1.
	To SchemaVersion
	// Description says what changed, for the migration report and the audit.
	Description string
	// Apply transforms one record's payload.
	Apply func(rec Record) (Record, error)
}

// Migrator holds an ordered chain of migrations.
type Migrator struct {
	steps map[SchemaVersion]Migration
}

// NewMigrator builds a migrator from a set of migrations.
//
// Refuses a chain with a gap or a duplicate, at construction rather than
// mid-run. A migration that discovers it cannot proceed halfway through a store
// leaves that store in a state no version describes.
func NewMigrator(migrations ...Migration) (Migrator, error) {
	m := Migrator{steps: make(map[SchemaVersion]Migration, len(migrations))}
	var problems []string

	for _, mig := range migrations {
		if mig.To != mig.From+1 {
			problems = append(problems, fmt.Sprintf(
				"migration %d→%d must advance exactly one version", mig.From, mig.To))
			continue
		}
		if mig.Apply == nil {
			problems = append(problems, fmt.Sprintf(
				"migration %d→%d has no Apply function", mig.From, mig.To))
			continue
		}
		if _, dup := m.steps[mig.From]; dup {
			problems = append(problems, fmt.Sprintf(
				"two migrations both read version %d", mig.From))
			continue
		}
		m.steps[mig.From] = mig
	}

	if len(problems) > 0 {
		return Migrator{}, &ConfigError{Problems: problems}
	}
	return m, nil
}

// Path returns the migrations needed to bring `from` to [CurrentSchema].
func (m Migrator) Path(from SchemaVersion) ([]Migration, error) {
	var out []Migration
	for v := from; v < CurrentSchema; v++ {
		step, ok := m.steps[v]
		if !ok {
			return nil, fmt.Errorf("evaluation: no migration from schema v%d; "+
				"the chain to v%d is incomplete", v, CurrentSchema)
		}
		out = append(out, step)
	}
	return out, nil
}

// MigrationReport describes what a migration did.
type MigrationReport struct {
	// From and To bound the versions seen.
	From, To SchemaVersion
	// Migrated counts records upgraded.
	Migrated int
	// Skipped counts records already current.
	Skipped int
	// Failed lists records that could not be migrated, with the reason. A
	// migration reports these rather than aborting: one malformed payload
	// should not strand every other record at the old version.
	Failed []MigrationFailure
	// Steps names the migrations applied.
	Steps []string
	// TookNanos is the wall time.
	TookNanos time.Duration
}

// MigrationFailure is one record a migration could not upgrade.
type MigrationFailure struct {
	Key    RecordKey
	Schema SchemaVersion
	Reason string
}

// Clean reports that every record migrated.
func (r MigrationReport) Clean() bool { return len(r.Failed) == 0 }

// Summary renders the report.
func (r MigrationReport) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "migration v%d→v%d: %d migrated, %d already current, %d failed in %s",
		r.From, r.To, r.Migrated, r.Skipped, len(r.Failed), r.TookNanos.Round(time.Millisecond))
	for _, s := range r.Steps {
		fmt.Fprintf(&b, "\n  applied %s", s)
	}
	for _, f := range r.Failed {
		fmt.Fprintf(&b, "\n  FAILED %s at v%d: %s", f.Key, f.Schema, f.Reason)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Sweep
// ---------------------------------------------------------------------------

// SweepReport describes a retention sweep.
type SweepReport struct {
	// At is the instant the sweep evaluated against.
	At time.Time
	// Deleted counts records removed, by kind.
	Deleted map[RecordKind]int
	// Held counts records that were expired but retained under legal hold.
	//
	// REPORTED SEPARATELY AND ON PURPOSE. A sweep that silently retained a
	// thousand held records looks identical to a sweep with nothing to do, and
	// the difference is the entire compliance position.
	Held map[RecordKind]int
	// Keys lists what was deleted, for the audit trail.
	Keys []RecordKey
	// TookNanos is the wall time.
	TookNanos time.Duration
}

// Total returns how many records were deleted.
func (r SweepReport) Total() int {
	var n int
	for _, c := range r.Deleted {
		n += c
	}
	return n
}

// TotalHeld returns how many expired records were retained under hold.
func (r SweepReport) TotalHeld() int {
	var n int
	for _, c := range r.Held {
		n += c
	}
	return n
}

// Summary renders the report.
func (r SweepReport) Summary() string {
	var parts []string
	for _, k := range AllRecordKinds() {
		if n := r.Deleted[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", k, n))
		}
	}
	s := fmt.Sprintf("sweep at %s: %d deleted [%s] in %s",
		r.At.UTC().Format(time.RFC3339), r.Total(), strings.Join(parts, " "),
		r.TookNanos.Round(time.Millisecond))
	if held := r.TotalHeld(); held > 0 {
		s += fmt.Sprintf("; %d expired records RETAINED under legal hold", held)
	}
	return s
}

// ---------------------------------------------------------------------------
// AuditEntry
// ---------------------------------------------------------------------------

// AuditAction classifies an audited event.
type AuditAction string

// The audited actions.
const (
	AuditDeleted     AuditAction = "deleted"
	AuditSwept       AuditAction = "swept"
	AuditHoldPlaced  AuditAction = "hold_placed"
	AuditHoldLifted  AuditAction = "hold_lifted"
	AuditMigrated    AuditAction = "migrated"
	AuditRestored    AuditAction = "restored"
	AuditPolicyApply AuditAction = "policy_applied"
)

// AuditEntry records a retention or lifecycle event.
//
// Content-free by construction: it names keys, counts and reasons, never
// payloads. That is frozen invariant I7 applied to the audit trail — an audit
// log is the longest-lived table in any system, and one that quoted the records
// it described would outlive the retention rule it exists to evidence.
type AuditEntry struct {
	// ID identifies the entry.
	ID string
	// Action is what happened.
	Action AuditAction
	// Keys names the records affected. Truncated for a large sweep; Count is
	// authoritative.
	Keys []RecordKey
	// Count is how many records were affected.
	Count int
	// Policy names the retention policy responsible, where one was.
	Policy string
	// By and Reason attribute a human action. Empty for an automatic sweep,
	// which is itself informative: an unattributed deletion was systematic.
	By     string
	Reason string
	// At stamps it.
	At time.Time
}

// Summary renders the entry.
func (a AuditEntry) Summary() string {
	s := fmt.Sprintf("%s %s count=%d", a.At.UTC().Format(time.RFC3339), a.Action, a.Count)
	if a.Policy != "" {
		s += " policy=" + a.Policy
	}
	if a.By != "" {
		s += fmt.Sprintf(" by=%s (%s)", a.By, a.Reason)
	}
	return s
}

// maxAuditKeys bounds how many keys one audit entry names.
//
// A sweep removing a hundred thousand records must not write a hundred thousand
// keys into a single audit row. Count remains exact; the key list is a sample.
const maxAuditKeys = 64

// ---------------------------------------------------------------------------
// MemoryRepository
// ---------------------------------------------------------------------------

// MemoryRepository is an in-process [Repository].
//
// The reference implementation and the one the tests run against. It exists to
// keep the platform runnable with no infrastructure — the same property Phase
// 10A established and every phase since has preserved — and to pin the
// behaviour a database-backed implementation must reproduce.
//
// It is NOT the production store. Phase 11's Aurora implementation satisfies
// the same interface and is verified by the same conformance suite.
type MemoryRepository struct {
	clock rt.Clock

	mu      sync.RWMutex
	records map[RecordKey]Record
	closed  bool
	nextID  int
}

// NewMemoryRepository builds an empty repository.
func NewMemoryRepository(clock rt.Clock) *MemoryRepository {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	return &MemoryRepository{clock: clock, records: make(map[RecordKey]Record)}
}

func (r *MemoryRepository) checkOpen() error {
	if r.closed {
		return ErrClosed
	}
	return nil
}

// Put stores or replaces a record.
func (r *MemoryRepository) Put(ctx context.Context, rec Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if rec.Schema > CurrentSchema {
		return fmt.Errorf("%w: record %s is v%d, this build writes v%d",
			ErrSchemaTooNew, rec.Key(), rec.Schema, CurrentSchema)
	}
	if rec.Schema == 0 {
		rec.Schema = CurrentSchema
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = r.clock.Now()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkOpen(); err != nil {
		return err
	}
	// A replacement must not silently drop a hold placed on the existing
	// record. Re-storing a run to correct a label would otherwise lift a legal
	// hold as a side effect.
	if existing, ok := r.records[rec.Key()]; ok && existing.LegalHold {
		rec.LegalHold = true
	}
	r.records[rec.Key()] = rec
	return nil
}

// PutBatch stores many records. Atomic: nothing is written if any record is
// rejected.
func (r *MemoryRepository) PutBatch(ctx context.Context, recs []Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, rec := range recs {
		if rec.Schema > CurrentSchema {
			return fmt.Errorf("%w: record %s is v%d", ErrSchemaTooNew, rec.Key(), rec.Schema)
		}
	}
	for _, rec := range recs {
		if err := r.Put(ctx, rec); err != nil {
			return err
		}
	}
	return nil
}

// Get retrieves one record.
func (r *MemoryRepository) Get(ctx context.Context, key RecordKey) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := r.checkOpen(); err != nil {
		return Record{}, err
	}
	rec, ok := r.records[key]
	if !ok {
		return Record{}, fmt.Errorf("%w: %s", ErrRecordNotFound, key)
	}
	return rec, nil
}

// List retrieves matching records, newest first.
func (r *MemoryRepository) List(ctx context.Context, q Query) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := r.checkOpen(); err != nil {
		return nil, err
	}

	var out []Record
	for _, rec := range r.records {
		if q.matches(rec) {
			out = append(out, rec)
		}
	}
	sortRecords(out)
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// Count returns how many records match.
func (r *MemoryRepository) Count(ctx context.Context, q Query) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := r.checkOpen(); err != nil {
		return 0, err
	}
	var n int
	for _, rec := range r.records {
		if q.matches(rec) {
			n++
		}
	}
	return n, nil
}

// Delete removes one record, refusing a legal hold.
func (r *MemoryRepository) Delete(ctx context.Context, key RecordKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkOpen(); err != nil {
		return err
	}

	rec, ok := r.records[key]
	if !ok {
		return fmt.Errorf("%w: %s", ErrRecordNotFound, key)
	}
	if rec.LegalHold {
		return fmt.Errorf("%w: %s", ErrLegalHold, key)
	}
	delete(r.records, key)
	r.auditLocked(AuditDeleted, []RecordKey{key}, 1, "", "", "")
	return nil
}

// SetLegalHold places or lifts a hold.
func (r *MemoryRepository) SetLegalHold(ctx context.Context, key RecordKey,
	hold bool, by, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var problems []string
	if by == "" {
		problems = append(problems, "legal hold: By is required; an unattributed "+
			"hold cannot be defended and cannot be lifted with confidence")
	}
	if reason == "" {
		problems = append(problems, "legal hold: Reason is required")
	}
	if len(problems) > 0 {
		return &ConfigError{Problems: problems}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkOpen(); err != nil {
		return err
	}

	rec, ok := r.records[key]
	if !ok {
		return fmt.Errorf("%w: %s", ErrRecordNotFound, key)
	}
	rec.LegalHold = hold
	r.records[key] = rec

	action := AuditHoldLifted
	if hold {
		action = AuditHoldPlaced
	}
	r.auditLocked(action, []RecordKey{key}, 1, "", by, reason)
	return nil
}

// Sweep deletes everything expired at `now`.
func (r *MemoryRepository) Sweep(ctx context.Context, now time.Time) (SweepReport, error) {
	if err := ctx.Err(); err != nil {
		return SweepReport{}, err
	}
	started := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkOpen(); err != nil {
		return SweepReport{}, err
	}

	report := SweepReport{
		At:      now,
		Deleted: make(map[RecordKind]int),
		Held:    make(map[RecordKind]int),
	}

	for key, rec := range r.records {
		// Audit records are exempt from the sweep. Retention exists to bound
		// what the platform remembers about behaviour; the record of WHAT WAS
		// DELETED is the evidence that the bound was honoured, and deleting it
		// on the same schedule would erase the proof along with the data.
		if rec.Kind == RecordAudit {
			continue
		}
		if rec.LegalHold && !rec.ExpiresAt.IsZero() && !now.Before(rec.ExpiresAt) {
			report.Held[rec.Kind]++
			continue
		}
		if !rec.Expired(now) {
			continue
		}
		delete(r.records, key)
		report.Deleted[rec.Kind]++
		if len(report.Keys) < maxAuditKeys {
			report.Keys = append(report.Keys, key)
		}
	}

	report.TookNanos = time.Since(started)
	if report.Total() > 0 || report.TotalHeld() > 0 {
		r.auditLocked(AuditSwept, report.Keys, report.Total(), "", "", "")
	}
	return report, nil
}

// Schema reports the lowest and highest versions present.
func (r *MemoryRepository) Schema(ctx context.Context) (SchemaVersion, SchemaVersion, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := r.checkOpen(); err != nil {
		return 0, 0, err
	}
	if len(r.records) == 0 {
		return CurrentSchema, CurrentSchema, nil
	}
	low, high := SchemaVersion(1<<31-1), SchemaVersion(0)
	for _, rec := range r.records {
		if rec.Schema < low {
			low = rec.Schema
		}
		if rec.Schema > high {
			high = rec.Schema
		}
	}
	return low, high, nil
}

// Migrate upgrades every record below the current schema.
func (r *MemoryRepository) Migrate(ctx context.Context, m Migrator) (MigrationReport, error) {
	started := time.Now()

	low, high, err := r.Schema(ctx)
	if err != nil {
		return MigrationReport{}, err
	}
	report := MigrationReport{From: low, To: CurrentSchema}
	if high > CurrentSchema {
		return report, fmt.Errorf("%w: store holds v%d", ErrSchemaTooNew, high)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkOpen(); err != nil {
		return report, err
	}

	steps := map[string]bool{}
	for key, rec := range r.records {
		if rec.Schema >= CurrentSchema {
			report.Skipped++
			continue
		}
		path, perr := m.Path(rec.Schema)
		if perr != nil {
			report.Failed = append(report.Failed, MigrationFailure{
				Key: key, Schema: rec.Schema, Reason: perr.Error()})
			continue
		}

		migrated := rec
		var failed bool
		for _, step := range path {
			next, aerr := step.Apply(migrated)
			if aerr != nil {
				report.Failed = append(report.Failed, MigrationFailure{
					Key: key, Schema: migrated.Schema, Reason: aerr.Error()})
				failed = true
				break
			}
			next.Schema = step.To
			migrated = next
			steps[fmt.Sprintf("v%d→v%d (%s)", step.From, step.To, step.Description)] = true
		}
		if failed {
			continue
		}
		r.records[key] = migrated
		report.Migrated++
	}

	for s := range steps {
		report.Steps = append(report.Steps, s)
	}
	sort.Strings(report.Steps)
	report.TookNanos = time.Since(started)

	if report.Migrated > 0 {
		r.auditLocked(AuditMigrated, nil, report.Migrated, "", "", strings.Join(report.Steps, "; "))
	}
	return report, nil
}

// Snapshot captures the store.
func (r *MemoryRepository) Snapshot(ctx context.Context) (StoreSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return StoreSnapshot{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := r.checkOpen(); err != nil {
		return StoreSnapshot{}, err
	}

	snap := StoreSnapshot{
		Schema:  CurrentSchema,
		TakenAt: r.clock.Now(),
		Records: make([]Record, 0, len(r.records)),
		Counts:  make(map[RecordKind]int),
	}
	for _, rec := range r.records {
		clone := rec
		clone.Payload = append([]byte(nil), rec.Payload...)
		snap.Records = append(snap.Records, clone)
		snap.Counts[rec.Kind]++
	}
	sortRecords(snap.Records)
	return snap, nil
}

// Restore replaces the store's contents.
//
// REFUSES TO DROP A LEGAL HOLD. Records currently held are preserved even if the
// snapshot predates the hold — a restore is an operational action and must not
// become a way to discard a hold by accident.
func (r *MemoryRepository) Restore(ctx context.Context, snap StoreSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkOpen(); err != nil {
		return err
	}

	held := make(map[RecordKey]Record)
	for key, rec := range r.records {
		if rec.LegalHold {
			held[key] = rec
		}
	}

	r.records = make(map[RecordKey]Record, len(snap.Records))
	for _, rec := range snap.Records {
		clone := rec
		clone.Payload = append([]byte(nil), rec.Payload...)
		r.records[rec.Key()] = clone
	}
	for key, rec := range held {
		if restored, ok := r.records[key]; ok {
			restored.LegalHold = true
			r.records[key] = restored
			continue
		}
		r.records[key] = rec
	}

	r.auditLocked(AuditRestored, nil, len(snap.Records), "", "",
		fmt.Sprintf("restored snapshot taken %s; %d held records preserved",
			snap.TakenAt.UTC().Format(time.RFC3339), len(held)))
	return nil
}

// Close marks the repository closed. Idempotent.
func (r *MemoryRepository) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

// Audit returns the audit trail, oldest first.
func (r *MemoryRepository) Audit(ctx context.Context) ([]AuditEntry, error) {
	recs, err := r.List(ctx, Query{Kinds: []RecordKind{RecordAudit}})
	if err != nil {
		return nil, err
	}
	out := make([]AuditEntry, 0, len(recs))
	for i := len(recs) - 1; i >= 0; i-- {
		if entry, ok := decodeAudit(recs[i]); ok {
			out = append(out, entry)
		}
	}
	return out, nil
}

// auditLocked writes an audit record. The caller holds the write lock.
func (r *MemoryRepository) auditLocked(action AuditAction, keys []RecordKey,
	count int, policy, by, reason string) {
	r.nextID++
	entry := AuditEntry{
		ID: fmt.Sprintf("aud_%d", r.nextID), Action: action, Keys: keys,
		Count: count, Policy: policy, By: by, Reason: reason, At: r.clock.Now(),
	}
	rec := encodeAudit(entry)
	r.records[rec.Key()] = rec
}

func sortRecords(recs []Record) {
	sort.Slice(recs, func(i, j int) bool {
		if !recs[i].CreatedAt.Equal(recs[j].CreatedAt) {
			return recs[i].CreatedAt.After(recs[j].CreatedAt) // newest first
		}
		if recs[i].Kind != recs[j].Kind {
			return recs[i].Kind < recs[j].Kind
		}
		return recs[i].ID < recs[j].ID
	})
}
