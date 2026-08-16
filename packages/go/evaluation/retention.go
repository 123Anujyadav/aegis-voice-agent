package evaluation

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// ---------------------------------------------------------------------------
// Retention policies
// ---------------------------------------------------------------------------

// RetentionAction is what happens to a record when its retention expires.
type RetentionAction string

// The retention actions.
const (
	// RetentionDelete removes the record.
	RetentionDelete RetentionAction = "delete"

	// RetentionArchive hands the record to an [Archiver] and then removes it
	// from the live store.
	//
	// The archive is a different retention domain with a different rule; moving
	// a record there is not the same as keeping it, and the audit records which
	// of the two happened.
	RetentionArchive RetentionAction = "archive"

	// RetentionIndefinite never expires. The correct action for the audit
	// trail, and the one that must be chosen explicitly rather than arrived at
	// by leaving a duration unset.
	RetentionIndefinite RetentionAction = "indefinite"
)

// The standard retention windows.
//
// ADR-0012 sets 90 days for transcripts, and memory and governance mirror it.
// The evaluation platform had no counterpart at all until Phase 10.5 — the
// finding that made durable storage a blocker rather than a feature, because a
// store with no retention rule becomes a permanent archive of subsystem
// behaviour the first time it survives a restart.
const (
	Retain30Days  = 30 * 24 * time.Hour
	Retain90Days  = 90 * 24 * time.Hour
	Retain180Days = 180 * 24 * time.Hour
)

// RetentionPolicy decides how long one kind of record is kept.
type RetentionPolicy struct {
	// Name identifies the policy in reports and audit entries.
	Name string
	// Kinds are the record kinds it governs.
	Kinds []RecordKind
	// Duration is how long a record is kept after [Record.CreatedAt]. Ignored
	// when Action is [RetentionIndefinite].
	Duration time.Duration
	// Action is what happens at expiry.
	Action RetentionAction
	// Rationale states why this window. Required: a retention period nobody
	// justified is one nobody can defend to a regulator or shorten with
	// confidence.
	Rationale string
}

func (p RetentionPolicy) validate() []string {
	var problems []string
	if p.Name == "" {
		problems = append(problems, "retention: Name is required")
	}
	if len(p.Kinds) == 0 {
		problems = append(problems, fmt.Sprintf(
			"retention %q: at least one record kind is required; a policy "+
				"governing nothing is a policy somebody believes is in force", p.Name))
	}
	switch p.Action {
	case RetentionDelete, RetentionArchive:
		if p.Duration <= 0 {
			problems = append(problems, fmt.Sprintf(
				"retention %q: Duration must be positive for action %s; zero would "+
					"expire every record the moment it was written", p.Name, p.Action))
		}
	case RetentionIndefinite:
		// Duration is meaningless here and is ignored rather than refused, so a
		// policy can be switched to indefinite without editing two fields.
	default:
		problems = append(problems, fmt.Sprintf(
			"retention %q: unknown action %q", p.Name, p.Action))
	}
	if p.Rationale == "" {
		problems = append(problems, fmt.Sprintf(
			"retention %q: Rationale is required", p.Name))
	}
	return problems
}

// ExpiryFor returns when a record created at `created` may be removed.
// The zero time means never.
func (p RetentionPolicy) ExpiryFor(created time.Time) time.Time {
	if p.Action == RetentionIndefinite {
		return time.Time{}
	}
	return created.Add(p.Duration)
}

// String renders the policy.
func (p RetentionPolicy) String() string {
	kinds := make([]string, len(p.Kinds))
	for i, k := range p.Kinds {
		kinds[i] = string(k)
	}
	window := "indefinite"
	if p.Action != RetentionIndefinite {
		window = strconv.FormatFloat(p.Duration.Hours()/24, 'f', -1, 64) + "d"
	}
	return fmt.Sprintf("%s: %s %s [%s]", p.Name, window, p.Action, strings.Join(kinds, ","))
}

// ---------------------------------------------------------------------------
// Retention schedule
// ---------------------------------------------------------------------------

// RetentionSchedule maps every record kind to exactly one policy.
//
// EXACTLY ONE. Two policies covering the same kind is refused at construction
// rather than resolved by precedence: "which of these two rules applies to this
// record" is a question a compliance answer cannot leave to an implementation
// detail, and an unlucky map iteration deciding it is worse than not booting.
type RetentionSchedule struct {
	policies []RetentionPolicy
	byKind   map[RecordKind]RetentionPolicy
}

// NewRetentionSchedule builds a schedule and validates it.
//
// Every record kind must be covered. An uncovered kind would fall through with
// no expiry and be kept forever — silently, and in the direction that creates
// liability rather than the direction that loses data.
func NewRetentionSchedule(policies ...RetentionPolicy) (RetentionSchedule, error) {
	s := RetentionSchedule{
		policies: append([]RetentionPolicy(nil), policies...),
		byKind:   make(map[RecordKind]RetentionPolicy),
	}
	var problems []string

	seenNames := map[string]bool{}
	for _, p := range policies {
		problems = append(problems, p.validate()...)
		if seenNames[p.Name] {
			problems = append(problems, fmt.Sprintf("retention: duplicate policy name %q", p.Name))
		}
		seenNames[p.Name] = true

		for _, k := range p.Kinds {
			if prev, dup := s.byKind[k]; dup {
				problems = append(problems, fmt.Sprintf(
					"retention: record kind %q is governed by both %q and %q; "+
						"which rule applies must not depend on evaluation order",
					k, prev.Name, p.Name))
				continue
			}
			s.byKind[k] = p
		}
	}

	for _, k := range AllRecordKinds() {
		if _, ok := s.byKind[k]; !ok {
			problems = append(problems, fmt.Sprintf(
				"retention: record kind %q has no policy; it would be kept forever "+
					"and nobody would be told", k))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return RetentionSchedule{}, &ConfigError{Problems: problems}
	}
	return s, nil
}

// DefaultRetentionSchedule is the platform baseline.
//
// Aligned with ADR-0012's 90 days for anything that records what a subsystem
// did, with two deliberate departures:
//
//   - Goldens are kept 180 days. A golden is an approved human decision, and
//     the regression question it answers — "when did this change, and who
//     agreed to it" — is asked across release cycles rather than within one.
//     Ninety days would routinely delete the baseline a quarterly investigation
//     needs.
//   - The audit trail is indefinite. It is the evidence that retention was
//     honoured, and expiring it on the same schedule as the data would destroy
//     the proof alongside the thing proved.
func DefaultRetentionSchedule() RetentionSchedule {
	s, err := NewRetentionSchedule(
		RetentionPolicy{
			Name:     "observations-90d",
			Kinds:    []RecordKind{RecordObservation, RecordRun},
			Duration: Retain90Days,
			Action:   RetentionDelete,
			Rationale: "Mirrors ADR-0012's 90-day transcript window. An observation " +
				"records what a subsystem did on a specific input and is the record " +
				"most likely to hold data derived from a real interaction.",
		},
		RetentionPolicy{
			Name:     "goldens-180d",
			Kinds:    []RecordKind{RecordGolden},
			Duration: Retain180Days,
			Action:   RetentionArchive,
			Rationale: "A golden is an approved human decision. Archived rather than " +
				"deleted because 'what did we consider correct last year' is a real " +
				"question, and archival moves it to a colder retention domain rather " +
				"than destroying it.",
		},
		RetentionPolicy{
			Name:     "measurements-30d",
			Kinds:    []RecordKind{RecordBenchmark, RecordTrend},
			Duration: Retain30Days,
			Action:   RetentionDelete,
			Rationale: "Timings and fingerprints carry no interaction content and lose " +
				"relevance quickly; a benchmark from a previous hardware generation " +
				"misleads more than it informs.",
		},
		RetentionPolicy{
			Name:   "audit-indefinite",
			Kinds:  []RecordKind{RecordAudit},
			Action: RetentionIndefinite,
			Rationale: "The evidence that retention was honoured. Content-free by " +
				"construction, so keeping it indefinitely creates no exposure.",
		},
	)
	if err != nil {
		// Unreachable: the literal above is fixed. A panic here would mean the
		// platform's own default schedule is invalid, which is a build-time
		// defect rather than a runtime condition.
		panic("evaluation: DefaultRetentionSchedule is invalid: " + err.Error())
	}
	return s
}

// For returns the policy governing a record kind.
func (s RetentionSchedule) For(kind RecordKind) (RetentionPolicy, bool) {
	p, ok := s.byKind[kind]
	return p, ok
}

// Policies returns every policy, ordered by name.
func (s RetentionSchedule) Policies() []RetentionPolicy {
	out := append([]RetentionPolicy(nil), s.policies...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Apply stamps a record's ExpiresAt from the schedule.
//
// Called on the write path, so expiry is decided once at creation rather than
// recomputed on every sweep. Changing a policy therefore does not retroactively
// re-age existing records — [RetentionManager.Reapply] does that, explicitly and
// with an audit entry, because silently re-dating a store is how records vanish
// earlier than anyone expected.
func (s RetentionSchedule) Apply(rec Record) Record {
	p, ok := s.byKind[rec.Kind]
	if !ok {
		return rec
	}
	rec.ExpiresAt = p.ExpiryFor(rec.CreatedAt)
	return rec
}

// Summary renders the schedule.
func (s RetentionSchedule) Summary() string {
	var b strings.Builder
	b.WriteString("retention schedule:")
	for _, p := range s.Policies() {
		fmt.Fprintf(&b, "\n  %s", p)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Archiver
// ---------------------------------------------------------------------------

// Archiver receives records whose policy action is [RetentionArchive].
//
// Deliberately minimal, and deliberately not implemented against any particular
// cold store. An S3 or Glacier implementation satisfies this and nothing in the
// platform changes.
type Archiver interface {
	// Archive durably stores records leaving the live store. It must return an
	// error rather than partially succeeding: [RetentionManager] deletes only
	// after Archive reports success, so a false success loses data.
	Archive(ctx context.Context, recs []Record) error
}

// DiscardArchiver satisfies [Archiver] by dropping records.
//
// For tests and for a deployment that has genuinely decided archived records
// need not survive. Named so that choosing it is visible in a config review —
// a nil Archiver would be the same behaviour by accident.
type DiscardArchiver struct{ Count int }

// Archive discards.
func (d *DiscardArchiver) Archive(_ context.Context, recs []Record) error {
	d.Count += len(recs)
	return nil
}

// MemoryArchiver keeps archived records in memory. For tests.
type MemoryArchiver struct{ Records []Record }

// Archive retains the records.
func (m *MemoryArchiver) Archive(_ context.Context, recs []Record) error {
	m.Records = append(m.Records, recs...)
	return nil
}

// ---------------------------------------------------------------------------
// RetentionManager
// ---------------------------------------------------------------------------

// RetentionManager enforces a schedule against a repository.
type RetentionManager struct {
	repo     Repository
	schedule RetentionSchedule
	archiver Archiver
	clock    rt.Clock
}

// NewRetentionManager builds a manager.
//
// A nil archiver is refused when the schedule contains an archive action. The
// alternative — silently deleting what a policy said to archive — is a
// compliance failure that looks exactly like correct operation.
func NewRetentionManager(repo Repository, schedule RetentionSchedule,
	archiver Archiver, clock rt.Clock) (*RetentionManager, error) {
	var problems []string
	if repo == nil {
		problems = append(problems, "retention: a repository is required")
	}
	if archiver == nil {
		for _, p := range schedule.Policies() {
			if p.Action == RetentionArchive {
				problems = append(problems, fmt.Sprintf(
					"retention: policy %q archives but no Archiver was supplied; "+
						"records would be deleted instead, which looks identical to "+
						"correct operation", p.Name))
			}
		}
	}
	if len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if clock == nil {
		clock = rt.SystemClock{}
	}
	return &RetentionManager{repo: repo, schedule: schedule,
		archiver: archiver, clock: clock}, nil
}

// Schedule returns the schedule in force.
func (m *RetentionManager) Schedule() RetentionSchedule { return m.schedule }

// Store stamps a record with its expiry and writes it.
//
// The write path every engine should use. Writing through [Repository.Put]
// directly is legal and leaves ExpiresAt zero — meaning "never" — which is why
// [RetentionManager.Unstamped] exists to find such records.
func (m *RetentionManager) Store(ctx context.Context, rec Record) error {
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = m.clock.Now()
	}
	return m.repo.Put(ctx, m.schedule.Apply(rec))
}

// Unstamped returns records with no expiry that a policy says should have one.
//
// A record written directly through the repository bypasses [RetentionManager.Store]
// and keeps a zero ExpiresAt, which the sweep reads as "never delete". That is
// a retention leak that produces no error and no log line, so it needs an
// explicit check — run it in a start-up assertion or a compliance job.
func (m *RetentionManager) Unstamped(ctx context.Context) ([]RecordKey, error) {
	recs, err := m.repo.List(ctx, Query{IncludeUnreadable: true})
	if err != nil {
		return nil, err
	}
	var out []RecordKey
	for _, rec := range recs {
		p, ok := m.schedule.For(rec.Kind)
		if !ok || p.Action == RetentionIndefinite {
			continue
		}
		if rec.ExpiresAt.IsZero() {
			out = append(out, rec.Key())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

// Reapply re-stamps every record from the current schedule.
//
// Explicit, because changing a policy and having the store silently re-age is
// how records disappear earlier than anybody expected. Returns how many records
// changed expiry, so a shortened window's blast radius is visible BEFORE the
// next sweep runs rather than after it.
func (m *RetentionManager) Reapply(ctx context.Context) (int, error) {
	recs, err := m.repo.List(ctx, Query{IncludeUnreadable: true})
	if err != nil {
		return 0, err
	}
	var changed int
	for _, rec := range recs {
		updated := m.schedule.Apply(rec)
		if updated.ExpiresAt.Equal(rec.ExpiresAt) {
			continue
		}
		if err := m.repo.Put(ctx, updated); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

// Enforce archives what must be archived, then sweeps what must be deleted.
//
// ARCHIVE BEFORE DELETE, and delete only what the archiver confirmed. A sweep
// that ran first would remove records the archive never received, and the loss
// would be discovered only when somebody went looking for them.
func (m *RetentionManager) Enforce(ctx context.Context) (RetentionReport, error) {
	now := m.clock.Now()
	report := RetentionReport{At: now, Archived: make(map[RecordKind]int)}

	for _, p := range m.schedule.Policies() {
		if p.Action != RetentionArchive {
			continue
		}
		due, err := m.dueFor(ctx, p, now)
		if err != nil {
			return report, err
		}
		if len(due) == 0 {
			continue
		}
		if err := m.archiver.Archive(ctx, due); err != nil {
			return report, fmt.Errorf("retention: policy %q archive failed, "+
				"nothing deleted: %w", p.Name, err)
		}
		for _, rec := range due {
			if err := m.repo.Delete(ctx, rec.Key()); err != nil {
				// A hold placed between the query and the delete is not an
				// error: the hold won, which is the correct outcome.
				if errorsIsLegalHold(err) {
					report.Held++
					continue
				}
				return report, err
			}
			report.Archived[rec.Kind]++
		}
		report.Policies = append(report.Policies, p.Name)
	}

	sweep, err := m.repo.Sweep(ctx, now)
	if err != nil {
		return report, err
	}
	report.Sweep = sweep
	report.Held += sweep.TotalHeld()
	return report, nil
}

func (m *RetentionManager) dueFor(ctx context.Context, p RetentionPolicy,
	now time.Time) ([]Record, error) {
	recs, err := m.repo.List(ctx, Query{Kinds: p.Kinds})
	if err != nil {
		return nil, err
	}
	var due []Record
	for _, rec := range recs {
		if rec.Expired(now) {
			due = append(due, rec)
		}
	}
	return due, nil
}

// RetentionReport describes one enforcement pass.
type RetentionReport struct {
	// At is the instant enforced against.
	At time.Time
	// Archived counts records moved to the archive, by kind.
	Archived map[RecordKind]int
	// Sweep is the deletion pass that followed.
	Sweep SweepReport
	// Held counts expired records retained under legal hold.
	Held int
	// Policies names the archive policies that acted.
	Policies []string
}

// TotalArchived returns how many records were archived.
func (r RetentionReport) TotalArchived() int {
	var n int
	for _, c := range r.Archived {
		n += c
	}
	return n
}

// Summary renders the report.
func (r RetentionReport) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "retention at %s: %d archived, %d deleted",
		r.At.UTC().Format(time.RFC3339), r.TotalArchived(), r.Sweep.Total())
	if r.Held > 0 {
		fmt.Fprintf(&b, ", %d RETAINED under legal hold", r.Held)
	}
	for _, p := range r.Policies {
		fmt.Fprintf(&b, "\n  archived under %s", p)
	}
	return b.String()
}

func errorsIsLegalHold(err error) bool {
	return err != nil && strings.Contains(err.Error(), ErrLegalHold.Error())
}

// ---------------------------------------------------------------------------
// Audit encoding
// ---------------------------------------------------------------------------

// encodeAudit renders an audit entry as a record.
//
// A deliberately simple line-oriented encoding rather than JSON: the audit
// payload must remain readable by a human running `SELECT payload FROM ...`
// during an incident, when the tooling that would pretty-print it is often the
// thing that is broken.
func encodeAudit(e AuditEntry) Record {
	var b strings.Builder
	fmt.Fprintf(&b, "action=%s\ncount=%d\n", e.Action, e.Count)
	if e.Policy != "" {
		fmt.Fprintf(&b, "policy=%s\n", e.Policy)
	}
	if e.By != "" {
		fmt.Fprintf(&b, "by=%s\n", e.By)
	}
	if e.Reason != "" {
		fmt.Fprintf(&b, "reason=%s\n", strings.ReplaceAll(e.Reason, "\n", " "))
	}
	for _, k := range e.Keys {
		fmt.Fprintf(&b, "key=%s\n", k)
	}

	return Record{
		Schema:    CurrentSchema,
		Kind:      RecordAudit,
		ID:        e.ID,
		CreatedAt: e.At,
		// No expiry: the audit schedule is indefinite, and stamping one here
		// would let a schedule change expire the evidence.
		Payload: []byte(b.String()),
	}
}

func decodeAudit(rec Record) (AuditEntry, bool) {
	if rec.Kind != RecordAudit {
		return AuditEntry{}, false
	}
	e := AuditEntry{ID: rec.ID, At: rec.CreatedAt}
	for _, line := range strings.Split(string(rec.Payload), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch name {
		case "action":
			e.Action = AuditAction(value)
		case "count":
			e.Count, _ = strconv.Atoi(value)
		case "policy":
			e.Policy = value
		case "by":
			e.By = value
		case "reason":
			e.Reason = value
		case "key":
			if kind, id, cut := strings.Cut(value, "/"); cut {
				e.Keys = append(e.Keys, RecordKey{Kind: RecordKind(kind), ID: id})
			}
		}
	}
	return e, true
}
