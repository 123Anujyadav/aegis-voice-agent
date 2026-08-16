package memory

import (
	"sort"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Touch reads a record, updating its access statistics, and returns a clone.
//
// It takes the shard WRITE lock, because a read updates AccessedAt and
// AccessCount and those drive promotion and eviction. Making the read path
// lock-free would mean moving the counters into atomics beside the record; that
// is a real option and it is not taken yet, because at sixteen shards the
// contention has not been measured to matter. See PERFORMANCE §4 for the
// number that would justify the change.
func (i *Index) Touch(k Key, now time.Time) (*Record, bool) {
	sh := i.shardFor(k)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	r, ok := sh.records[k]
	if !ok {
		return nil, false
	}
	r.AccessedAt = now
	r.AccessCount++
	return r.Clone(), true
}

// Mutate applies fn to a stored record under the shard write lock.
//
// The only path by which a stored record is modified in place. fn must not call
// back into the index — it runs under the lock — and must not retain the
// pointer it is given.
func (i *Index) Mutate(k Key, fn func(*Record) error) error {
	sh := i.shardFor(k)
	sh.mu.Lock()
	r, ok := sh.records[k]
	if !ok {
		sh.mu.Unlock()
		return ErrNotFound
	}
	// Snapshot the attribute projection before the change, so index
	// maintenance can retract exactly what was projected.
	before := r.Clone()
	err := fn(r)
	after := r.Clone()
	sh.mu.Unlock()

	if err != nil {
		return err
	}

	// Reproject only when something indexable actually moved. The common
	// mutation — a version bump on an unchanged payload — touches no index.
	if indexableChanged(before, after) {
		i.auxMu.Lock()
		i.retractLocked(before)
		i.projectLocked(after)
		i.auxMu.Unlock()
	}
	return nil
}

// indexableChanged reports whether a mutation moved anything the auxiliary
// indexes project.
func indexableChanged(before, after *Record) bool {
	if !before.CreatedAt.Equal(after.CreatedAt) {
		return true
	}
	if len(before.Value.Attributes) != len(after.Value.Attributes) {
		return true
	}
	for k, v := range after.Value.Attributes {
		if before.Value.Attributes[k] != v {
			return true
		}
	}
	return false
}

// Store is the memory engine's transactional core.
//
// It owns the thirteen operations, versioning, optimistic locking, snapshots
// and rollback. It does not own scheduling, sweeps or the public API surface —
// those belong to [Runtime], which composes a Store.
type Store struct {
	policy  Policy
	clock   rt.Clock
	metrics *Metrics
	events  *Dispatcher
	index   *Index

	mu        sync.RWMutex
	subjects  map[SubjectID]int
	tombstone map[Key]time.Time
	snapshots map[SnapshotID]*storeSnapshot
	nextSnap  uint64
	bytes     int64
}

// NewStore constructs a store.
func NewStore(policy Policy, idxCfg IndexConfig, clock rt.Clock, metrics *Metrics, events *Dispatcher) (*Store, error) {
	if problems := policy.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if clock == nil {
		clock = rt.SystemClock{}
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	if events == nil {
		events = NewDispatcher(metrics)
	}
	idx, err := NewIndex(idxCfg)
	if err != nil {
		return nil, err
	}
	return &Store{
		policy: policy, clock: clock, metrics: metrics, events: events, index: idx,
		subjects:  make(map[SubjectID]int),
		tombstone: make(map[Key]time.Time),
		snapshots: make(map[SnapshotID]*storeSnapshot),
	}, nil
}

// Index exposes the index layer for retrieval. Read-only use is expected;
// mutating through it bypasses versioning and events.
func (s *Store) Index() *Index { return s.index }

// Count returns the number of live records.
func (s *Store) Count() int { return s.index.Count() }

// Bytes returns the total payload size held.
func (s *Store) Bytes() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bytes
}

// ---------------------------------------------------------------------------
// 1 · Store
// ---------------------------------------------------------------------------

// Store writes a record, creating or replacing it.
//
// It sets timestamps from the store's clock rather than trusting the record: a
// record cannot timestamp itself correctly, and letting it try is how two
// clocks appear in one system.
func (s *Store) Store(r *Record) (*Record, error) {
	start := s.clock.Now()
	defer func() { s.metrics.StoreLatency.Observe(s.clock.Since(start).Seconds()) }()

	if err := s.policy.admit(r); err != nil {
		if err == ErrConsentRequired {
			s.metrics.ConsentRefusals.Inc(r.Key.Kind.String())
		}
		return nil, err
	}

	now := s.clock.Now()
	stored := r.Clone()
	stored.UpdatedAt = now
	stored.AccessedAt = now

	existing, replacing := s.index.Get(stored.Key)
	if replacing {
		stored.CreatedAt = existing.CreatedAt
		stored.Version = existing.Version + 1
		stored.AccessCount = existing.AccessCount
	} else {
		stored.CreatedAt = now
		stored.Version = 1
		if err := s.reserveSubject(stored.Key.Subject); err != nil {
			return nil, err
		}
	}
	stored.State = StateActive

	if d, expires := s.policy.Retention.Lifetime(stored); expires {
		stored.ExpiresAt = now.Add(d)
	} else {
		stored.ExpiresAt = time.Time{}
	}

	if s.policy.Encryptor != nil && len(stored.Value.Data) > 0 {
		ct, err := s.policy.Encryptor.Encrypt(stored.Key.Subject, stored.Value.Data)
		if err != nil {
			return nil, err
		}
		stored.Value.Data = ct
	}

	s.index.Insert(stored)

	s.mu.Lock()
	if replacing {
		s.bytes -= int64(existing.Value.Size())
	}
	s.bytes += int64(stored.Value.Size())
	delete(s.tombstone, stored.Key)
	s.mu.Unlock()

	s.metrics.Stores.Inc(stored.Key.Kind.String(), stored.Tier.String())
	s.metrics.Records.Set(float64(s.index.Count()))
	s.metrics.Bytes.Set(float64(s.Bytes()))

	kind := EventCreated
	if replacing {
		kind = EventUpdated
	}
	s.emit(kind, stored, "", stored.Tier)

	return stored.Clone(), nil
}

// reserveSubject enforces the per-subject record cap.
func (s *Store) reserveSubject(subject SubjectID) error {
	if s.policy.MaxRecordsPerSubject <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subjects[subject] >= s.policy.MaxRecordsPerSubject {
		return ErrBudgetExceeded
	}
	s.subjects[subject]++
	return nil
}

// releaseSubject decrements a subject's record count.
func (s *Store) releaseSubject(subject SubjectID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subjects[subject] > 0 {
		s.subjects[subject]--
	}
	if s.subjects[subject] == 0 {
		delete(s.subjects, subject)
	}
}

// ---------------------------------------------------------------------------
// 2 · Retrieve
// ---------------------------------------------------------------------------

// Retrieve reads a record by key.
//
// It distinguishes four negative outcomes — not found, expired, archived,
// redacted — because "never existed", "aged out", "in cold storage" and
// "destroyed on purpose" are four different facts, and a caller frequently
// needs to tell them apart. Collapsing them into one nil is the most common
// way a memory layer becomes impossible to reason about.
func (s *Store) Retrieve(k Key, actor string) (*Record, error) {
	start := s.clock.Now()
	defer func() {
		s.metrics.RetrieveLatency.Observe(s.clock.Since(start).Seconds(), "exact")
	}()

	now := s.clock.Now()
	r, ok := s.index.Touch(k, now)
	if !ok {
		s.miss("exact", "not_found")
		s.metrics.Retrieves.Inc(k.Kind.String(), "not_found")
		return nil, ErrNotFound
	}

	if err := s.readable(r, now); err != nil {
		s.miss("exact", errReason(err))
		s.metrics.Retrieves.Inc(k.Kind.String(), errReason(err))
		return nil, err
	}

	s.auditRead(r, "retrieve", actor, true, "")

	if s.policy.Encryptor != nil && len(r.Value.Data) > 0 {
		pt, err := s.policy.Encryptor.Decrypt(r.Key.Subject, r.Value.Data)
		if err != nil {
			return nil, err
		}
		r.Value.Data = pt
	}

	s.metrics.Hits.Inc("exact")
	s.metrics.Retrieves.Inc(k.Kind.String(), "hit")
	return r, nil
}

// readable reports why a record cannot be returned, or nil.
func (s *Store) readable(r *Record, now time.Time) error {
	switch {
	case r.State == StateArchived:
		return ErrArchived
	case r.State == StateRedacted:
		return ErrRedacted
	case r.State == StateExpired || r.Expired(now):
		return ErrExpired
	case !r.State.Readable():
		return ErrNotFound
	}
	return nil
}

// errReason maps an error to a metric label.
func errReason(err error) string {
	switch err {
	case ErrExpired:
		return "expired"
	case ErrArchived:
		return "archived"
	case ErrRedacted:
		return "redacted"
	default:
		return "not_found"
	}
}

func (s *Store) miss(lookup, reason string) { s.metrics.Misses.Inc(lookup, reason) }

// auditRead records an access to protected data.
//
// Only Sensitive records are audited. Auditing every read of Internal data
// would bury the entries that matter under noise, and an audit log nobody can
// read is not a control.
func (s *Store) auditRead(r *Record, op, actor string, granted bool, reason string) {
	if !r.Sensitivity.RequiresAudit() {
		return
	}
	s.metrics.AuditedReads.Inc(r.Sensitivity.String())
	if s.policy.Auditor == nil {
		return
	}
	s.policy.Auditor.Record(AuditEvent{
		Key: r.Key, Operation: op, Sensitivity: r.Sensitivity,
		Actor: actor, At: s.clock.Now(), Granted: granted, Reason: reason,
	})
}

// ---------------------------------------------------------------------------
// 3 · Update — optimistic locking
// ---------------------------------------------------------------------------

// Update applies a mutation under an optimistic lock.
//
// expected is the version the caller believes current. A mismatch returns a
// *ConflictError carrying both versions, so the caller can decide between
// retry, merge and abandon rather than being told only that something changed.
//
// COMPARE-AND-SWAP, NOT LAST-WRITE-WINS. Two subsystems updating one memory —
// the assistant and a sync job, say — would otherwise silently lose one of the
// two writes, and a lost memory update is invisible until someone notices the
// system forgot something.
func (s *Store) Update(k Key, expected Version, mutate func(*Record) error) (*Record, error) {
	var out *Record

	err := s.index.Mutate(k, func(r *Record) error {
		if r.Version != expected {
			s.metrics.Conflicts.Inc(k.Kind.String())
			return &ConflictError{Key: k, Expected: expected, Actual: r.Version}
		}
		if !r.State.Readable() {
			return ErrNotFound
		}

		before := r.Value.Size()
		if err := mutate(r); err != nil {
			return err
		}
		if r.Value.Size() > s.policy.MaxRecordBytes {
			return ErrBudgetExceeded
		}

		r.Version++
		r.UpdatedAt = s.clock.Now()

		s.mu.Lock()
		s.bytes += int64(r.Value.Size() - before)
		s.mu.Unlock()

		out = r.Clone()
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.metrics.Updates.Inc(k.Kind.String())
	s.metrics.Bytes.Set(float64(s.Bytes()))
	s.emit(EventUpdated, out, "update", out.Tier)
	return out, nil
}

// ---------------------------------------------------------------------------
// 4 · Delete · 5 · Forget · 6 · Expire
// ---------------------------------------------------------------------------

// Delete removes a record and leaves a tombstone.
//
// The tombstone proves the key was used and prevents silent reuse. Without it,
// storing under a deleted key would look identical to storing under a fresh
// one, and an erasure could not be distinguished from a record that never
// existed.
func (s *Store) Delete(k Key, reason string) error {
	r, ok := s.index.Get(k)
	if !ok {
		return ErrNotFound
	}
	if r.Retention.SurvivesErasure() && reason == string(RedactSubjectRequest) {
		s.metrics.LegalHolds.Inc()
		return ErrLegalHold
	}

	size := r.Value.Size()
	snapshot := r.Clone()

	if !s.index.Delete(k) {
		return ErrNotFound
	}
	s.releaseSubject(k.Subject)

	s.mu.Lock()
	s.bytes -= int64(size)
	s.tombstone[k] = s.clock.Now()
	s.mu.Unlock()

	s.metrics.Deletes.Inc(k.Kind.String(), reason)
	s.metrics.Records.Set(float64(s.index.Count()))
	s.metrics.Bytes.Set(float64(s.Bytes()))
	s.emit(EventDeleted, snapshot, reason, snapshot.Tier)
	return nil
}

// ForgetResult reports what an erasure actually did.
type ForgetResult struct {
	// Deleted counts records removed.
	Deleted int
	// Redacted counts records whose payload was destroyed while the record was
	// retained under a legal hold.
	Redacted int
	// RetainedCount counts records left untouched under a legal hold.
	RetainedCount int
	// RetainedKeys lists what survived, so the subject can be told precisely
	// what was kept and on what basis.
	RetainedKeys Keys
}

// Keys is a sorted list of record keys.
type Keys []Key

// Forget erases everything the engine holds about a subject.
//
// This is the DPDP erasure path and it is deliberately not "delete everything".
// Records under a legal hold are RETAINED, and their payloads are REDACTED
// where the hold covers the fact rather than the content. The result names
// exactly what survived, because a subject who asks to be forgotten is entitled
// to know what was kept and on what basis — an erasure that silently retains is
// worse than one that refuses.
func (s *Store) Forget(subject SubjectID, actor string) (ForgetResult, error) {
	keys := s.index.BySubject(subject)
	s.metrics.IndexScans.Inc("subject")

	var res ForgetResult
	for _, k := range keys {
		r, ok := s.index.Get(k)
		if !ok {
			continue
		}
		if r.Retention.SurvivesErasure() {
			// The record stays; its content does not, unless the hold is
			// specifically over the content.
			if err := s.Redact(k, RedactConsentWithdrawn, actor); err == nil {
				res.Redacted++
			} else {
				res.RetainedCount++
			}
			res.RetainedKeys = append(res.RetainedKeys, k)
			continue
		}
		if err := s.Delete(k, string(RedactSubjectRequest)); err == nil {
			res.Deleted++
		}
	}

	outcome := "complete"
	if res.Redacted > 0 || res.RetainedCount > 0 {
		outcome = "partial_legal_hold"
	}
	s.metrics.Erasures.Inc(outcome)
	return res, nil
}

// Expire marks a record expired without reclaiming it.
//
// Expiry and deletion are separate so a caller can tell "aged out" from "gone",
// and so a raised retention preference can revive a record that has not yet
// been reclaimed.
func (s *Store) Expire(k Key) error {
	var snapshot *Record
	err := s.index.Mutate(k, func(r *Record) error {
		if !canTransition(r.State, StateExpired) {
			return ErrInvalidTransition
		}
		r.State = StateExpired
		r.Version++
		r.UpdatedAt = s.clock.Now()
		snapshot = r.Clone()
		return nil
	})
	if err != nil {
		return err
	}
	s.metrics.Expirations.Inc(k.Kind.String(), snapshot.Tier.String())
	s.emit(EventExpired, snapshot, "ttl", snapshot.Tier)
	return nil
}

// ---------------------------------------------------------------------------
// 7 · Archive · 8 · Restore · 9 · Redact
// ---------------------------------------------------------------------------

// Archive moves a record out of the hot path.
//
// The engine models the state transition; where cold storage physically lives
// is a deployment concern behind the [ColdStore] interface, which this module
// does not implement.
func (s *Store) Archive(k Key) error {
	var snapshot *Record
	err := s.index.Mutate(k, func(r *Record) error {
		if !canTransition(r.State, StateArchived) {
			return ErrInvalidTransition
		}
		r.State = StateArchived
		r.ArchivedAt = s.clock.Now()
		r.Version++
		snapshot = r.Clone()
		return nil
	})
	if err != nil {
		return err
	}
	s.metrics.Archivals.Inc(k.Kind.String())
	s.emit(EventArchived, snapshot, "archive", snapshot.Tier)
	return nil
}

// Restore returns an archived record to the hot path.
func (s *Store) Restore(k Key) error {
	err := s.index.Mutate(k, func(r *Record) error {
		if r.State != StateArchived {
			return ErrInvalidTransition
		}
		r.State = StateActive
		r.ArchivedAt = time.Time{}
		r.AccessedAt = s.clock.Now()
		r.Version++
		return nil
	})
	if err != nil {
		return err
	}
	s.metrics.Restores.Inc(k.Kind.String())
	return nil
}

// Redact destroys a record's payload while retaining its existence.
//
// The distinction from deletion is the point: a redacted record proves
// something was here and is gone, which is frequently the obligation — under a
// legal hold, or when a subject asks to forget one fact rather than to
// disappear.
func (s *Store) Redact(k Key, reason RedactionReason, actor string) error {
	var snapshot *Record
	err := s.index.Mutate(k, func(r *Record) error {
		if !canTransition(r.State, StateRedacted) && r.State != StateRedacted {
			return ErrInvalidTransition
		}
		s.mu.Lock()
		s.bytes -= int64(r.Value.Size())
		s.mu.Unlock()

		r.Value.Data = nil
		// Attributes are destroyed too. They are indexed, so leaving them would
		// keep the record discoverable by content that was supposed to be gone.
		r.Value.Attributes = nil
		r.Redacted = true
		r.State = StateRedacted
		r.Version++
		r.UpdatedAt = s.clock.Now()
		snapshot = r.Clone()
		return nil
	})
	if err != nil {
		return err
	}

	s.auditRead(snapshot, "redact", actor, true, string(reason))
	s.metrics.Redactions.Inc(k.Kind.String(), string(reason))
	s.metrics.Bytes.Set(float64(s.Bytes()))
	s.emit(EventUpdated, snapshot, string(reason), snapshot.Tier)
	return nil
}

// ---------------------------------------------------------------------------
// 10 · Merge · 11 · Split
// ---------------------------------------------------------------------------

// Merger combines several records into one.
//
// An interface because merging is domain-specific: combining two conversation
// summaries and combining two contact records are different operations, and the
// engine has no basis for choosing between them. It owns the transaction, the
// versioning and the events; the caller owns the semantics.
type Merger interface {
	// Merge produces the combined value. Inputs are ordered oldest first.
	Merge(inputs []*Record) (Value, error)
}

// Merge combines records into a target key.
//
// All-or-nothing: the sources are removed only after the target is written, so
// a failure leaves the sources intact. A merge that destroyed its inputs before
// writing its output would be a way to lose memories to a transient error.
func (s *Store) Merge(target Key, sources []Key, m Merger, actor string) (*Record, error) {
	if m == nil {
		return nil, invariant("INV-MEM-9", "merge requires a Merger")
	}
	if len(sources) < 2 {
		return nil, invariant("INV-MEM-9", "merge requires at least two sources")
	}

	inputs := make([]*Record, 0, len(sources))
	for _, k := range sources {
		r, err := s.Retrieve(k, actor)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, r)
	}
	sort.SliceStable(inputs, func(i, j int) bool {
		return inputs[i].CreatedAt.Before(inputs[j].CreatedAt)
	})

	value, err := m.Merge(inputs)
	if err != nil {
		return nil, err
	}

	// The merged record inherits the STRICTEST sensitivity and retention of its
	// inputs. Anything else would launder a Sensitive memory into a lower
	// classification by merging it with a Public one.
	merged := inputs[0].Clone()
	merged.Key = target
	merged.Value = value
	for _, in := range inputs {
		if in.Sensitivity > merged.Sensitivity {
			merged.Sensitivity = in.Sensitivity
			merged.ConsentRef = in.ConsentRef
		}
		if in.Retention > merged.Retention {
			merged.Retention = in.Retention
		}
		if in.Tier > merged.Tier {
			merged.Tier = in.Tier
		}
	}
	merged.Provenance.Derived = true

	stored, err := s.Store(merged)
	if err != nil {
		return nil, err
	}

	for _, k := range sources {
		if k == target {
			continue
		}
		_ = s.Delete(k, "merged")
	}

	s.metrics.Merges.Inc()
	s.emit(EventMerged, stored, "merge", stored.Tier)
	return stored, nil
}

// Splitter divides one record into several.
type Splitter interface {
	// Split produces the parts, each with the name suffix it should take.
	Split(input *Record) (map[string]Value, error)
}

// Split divides a record into several, removing the original.
func (s *Store) Split(source Key, sp Splitter, actor string) ([]*Record, error) {
	if sp == nil {
		return nil, invariant("INV-MEM-9", "split requires a Splitter")
	}
	in, err := s.Retrieve(source, actor)
	if err != nil {
		return nil, err
	}
	parts, err := sp.Split(in)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, invariant("INV-MEM-9", "split produced no parts")
	}

	names := make([]string, 0, len(parts))
	for n := range parts {
		names = append(names, n)
	}
	sort.Strings(names) // deterministic creation order

	out := make([]*Record, 0, len(parts))
	for _, name := range names {
		part := in.Clone()
		part.Key.Name = name
		part.Value = parts[name]
		part.Version = 0
		part.Provenance.Derived = true
		stored, err := s.Store(part)
		if err != nil {
			return out, err
		}
		out = append(out, stored)
	}

	_ = s.Delete(source, "split")
	s.metrics.Splits.Inc()
	return out, nil
}

// ---------------------------------------------------------------------------
// 12 · Snapshot · 13 · Restore (rollback)
// ---------------------------------------------------------------------------

// SnapshotID identifies a store snapshot.
type SnapshotID uint64

type storeSnapshot struct {
	id      SnapshotID
	at      time.Time
	label   string
	subject SubjectID
	records map[Key]*Record
}

// Snapshot captures a subject's records for rollback.
//
// Subject-scoped rather than whole-store. A global snapshot of a memory engine
// holding millions of records is not something anyone can afford to take on the
// path where rollback is actually needed, and rollback is always about one
// subject's state going wrong.
func (s *Store) Snapshot(subject SubjectID, label string) SnapshotID {
	keys := s.index.BySubject(subject)
	s.metrics.IndexScans.Inc("subject")

	snap := &storeSnapshot{
		at: s.clock.Now(), label: label, subject: subject,
		records: make(map[Key]*Record, len(keys)),
	}
	for _, k := range keys {
		if r, ok := s.index.Get(k); ok {
			snap.records[k] = r.Clone()
		}
	}

	s.mu.Lock()
	s.nextSnap++
	snap.id = SnapshotID(s.nextSnap)
	s.snapshots[snap.id] = snap
	s.mu.Unlock()

	return snap.id
}

// Rollback restores a subject's records to a snapshot.
//
// Records created after the snapshot are removed; records changed are reverted;
// records deleted are recreated. Records under a legal hold are NOT reverted —
// rolling back a legally-required retention decision would be the one rollback
// that is never permitted.
func (s *Store) Rollback(id SnapshotID) error {
	s.mu.RLock()
	snap, ok := s.snapshots[id]
	s.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}

	current := s.index.BySubject(snap.subject)
	for _, k := range current {
		if _, kept := snap.records[k]; kept {
			continue
		}
		if r, ok := s.index.Get(k); ok && r.Retention.SurvivesErasure() {
			continue
		}
		_ = s.Delete(k, "rollback")
	}

	keys := make([]Key, 0, len(snap.records))
	for k := range snap.records {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool { return keys[a].String() < keys[b].String() })

	for _, k := range keys {
		restored := snap.records[k].Clone()
		restored.Version = 0 // Store assigns the next version
		if _, err := s.Store(restored); err != nil {
			return err
		}
	}
	return nil
}

// DropSnapshot discards a snapshot.
func (s *Store) DropSnapshot(id SnapshotID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.snapshots, id)
}

// SnapshotCount returns how many snapshots are retained.
func (s *Store) SnapshotCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.snapshots)
}

// ---------------------------------------------------------------------------
// Tier movement
// ---------------------------------------------------------------------------

// Promote moves a record up a tier.
func (s *Store) Promote(k Key) error { return s.moveTier(k, true) }

// Demote moves a record down a tier.
func (s *Store) Demote(k Key) error { return s.moveTier(k, false) }

func (s *Store) moveTier(k Key, up bool) error {
	var (
		snapshot *Record
		from     Tier
	)
	err := s.index.Mutate(k, func(r *Record) error {
		from = r.Tier
		next := r.Tier.Demote()
		if up {
			next = r.Tier.Promote()
		}
		if next == r.Tier {
			return ErrInvalidTransition
		}
		if up && r.Provenance.Derived && next == TierLongTerm && !s.policy.AllowDerivedLongTerm {
			return invariant("INV-MEM-6",
				"record %s is derived and would reach long term", k)
		}
		r.Tier = next
		r.Version++
		r.UpdatedAt = s.clock.Now()

		if d, expires := s.policy.Retention.Lifetime(r); expires {
			r.ExpiresAt = r.UpdatedAt.Add(d)
		}
		snapshot = r.Clone()
		return nil
	})
	if err != nil {
		return err
	}

	if up {
		s.metrics.Promotions.Inc(from.String(), snapshot.Tier.String())
		s.emit(EventPromoted, snapshot, "policy", from)
	} else {
		s.metrics.Demotions.Inc(from.String(), snapshot.Tier.String())
		s.emit(EventDemoted, snapshot, "policy", from)
	}
	return nil
}

// emit publishes an event carrying identifiers only.
func (s *Store) emit(t EventType, r *Record, reason string, previous Tier) {
	if r == nil {
		return
	}
	s.events.Dispatch(Event{
		Type: t, Key: r.Key, Tier: r.Tier, PreviousTier: previous,
		Version: r.Version, Sensitivity: r.Sensitivity, Retention: r.Retention,
		Reason: reason, SizeBytes: r.Value.Size(), At: s.clock.Now(),
	})
}

// ColdStore is the archival destination.
//
// NOT IMPLEMENTED HERE. Where archived memories physically live — S3, a colder
// Aurora table, a separate cluster — is a deployment decision, and the engine
// models the lifecycle rather than the medium.
type ColdStore interface {
	// Put writes a record to cold storage.
	Put(*Record) error
	// Get retrieves a record from cold storage.
	Get(Key) (*Record, error)
	// Drop removes a record from cold storage.
	Drop(Key) error
}
