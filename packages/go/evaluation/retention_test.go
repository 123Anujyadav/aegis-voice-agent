package evaluation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// ---------------------------------------------------------------------------
// Schedule construction
// ---------------------------------------------------------------------------

func TestRetentionSchedule_DefaultIsValidAndComplete(t *testing.T) {
	t.Parallel()
	s := DefaultRetentionSchedule()

	// Every kind must be covered. An uncovered kind falls through with no
	// expiry and is kept forever, silently, in the direction that creates
	// liability rather than the direction that loses data.
	for _, k := range AllRecordKinds() {
		p, ok := s.For(k)
		if !ok {
			t.Errorf("record kind %q has no policy", k)
			continue
		}
		if p.Rationale == "" {
			t.Errorf("policy %q covering %q has no rationale", p.Name, k)
		}
	}

	if p, _ := s.For(RecordAudit); p.Action != RetentionIndefinite {
		t.Errorf("audit retention = %s, want indefinite: it is the evidence "+
			"that retention was honoured", p.Action)
	}
	if p, _ := s.For(RecordObservation); p.Duration != Retain90Days {
		t.Errorf("observation retention = %v, want 90 days per ADR-0012", p.Duration)
	}
	if p, _ := s.For(RecordGolden); p.Action != RetentionArchive {
		t.Errorf("golden retention action = %s, want archive", p.Action)
	}
}

func TestRetentionSchedule_RefusesAnUncoveredKind(t *testing.T) {
	t.Parallel()
	_, err := NewRetentionSchedule(RetentionPolicy{
		Name: "runs-only", Kinds: []RecordKind{RecordRun},
		Duration: Retain30Days, Action: RetentionDelete, Rationale: "test",
	})
	if err == nil {
		t.Fatal("a schedule leaving five kinds uncovered was accepted")
	}
	if !strings.Contains(err.Error(), "kept forever") {
		t.Errorf("error does not explain the consequence: %v", err)
	}
}

func TestRetentionSchedule_RefusesOverlappingPolicies(t *testing.T) {
	t.Parallel()
	// Which of two rules applies to a record must not depend on map iteration
	// order — that is not a defensible compliance answer.
	_, err := NewRetentionSchedule(
		RetentionPolicy{Name: "a", Kinds: []RecordKind{RecordRun},
			Duration: Retain30Days, Action: RetentionDelete, Rationale: "x"},
		RetentionPolicy{Name: "b", Kinds: []RecordKind{RecordRun},
			Duration: Retain90Days, Action: RetentionDelete, Rationale: "y"},
	)
	if err == nil {
		t.Fatal("two policies governing the same kind were accepted")
	}
	if !strings.Contains(err.Error(), "evaluation order") {
		t.Errorf("error does not name the hazard: %v", err)
	}
}

func TestRetentionPolicy_RefusesZeroDurationAndMissingRationale(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		policy RetentionPolicy
		want   string
	}{
		{"zero duration", RetentionPolicy{Name: "p", Kinds: []RecordKind{RecordRun},
			Action: RetentionDelete, Rationale: "x"}, "expire every record"},
		{"no rationale", RetentionPolicy{Name: "p", Kinds: []RecordKind{RecordRun},
			Duration: Retain30Days, Action: RetentionDelete}, "Rationale is required"},
		{"unknown action", RetentionPolicy{Name: "p", Kinds: []RecordKind{RecordRun},
			Duration: Retain30Days, Action: "purge", Rationale: "x"}, "unknown action"},
		{"no kinds", RetentionPolicy{Name: "p", Duration: Retain30Days,
			Action: RetentionDelete, Rationale: "x"}, "governing nothing"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := tc.policy.validate()
			var found bool
			for _, p := range problems {
				if strings.Contains(p, tc.want) {
					found = true
				}
			}
			if !found {
				t.Errorf("problems = %v, want one containing %q", problems, tc.want)
			}
		})
	}
}

func TestRetentionPolicy_IndefiniteHasNoExpiry(t *testing.T) {
	t.Parallel()
	p := RetentionPolicy{Name: "forever", Kinds: []RecordKind{RecordAudit},
		Action: RetentionIndefinite, Rationale: "evidence"}

	if got := p.ExpiryFor(time.Now()); !got.IsZero() {
		t.Errorf("ExpiryFor = %v, want the zero time", got)
	}
}

// ---------------------------------------------------------------------------
// Stamping
// ---------------------------------------------------------------------------

func TestRetentionManager_StoreStampsExpiry(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	repo := NewMemoryRepository(clock)
	mgr, err := NewRetentionManager(repo, DefaultRetentionSchedule(),
		&MemoryArchiver{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	r := rec(RecordObservation, "o1", clock.Now())
	r.ExpiresAt = time.Time{}
	if err := mgr.Store(ctx, r); err != nil {
		t.Fatal(err)
	}

	got, err := repo.Get(ctx, r.Key())
	if err != nil {
		t.Fatal(err)
	}
	want := clock.Now().Add(Retain90Days)
	if !got.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, want)
	}
}

// TestRetentionManager_UnstampedFindsTheLeak covers the quiet failure mode.
//
// A record written straight through the repository keeps a zero ExpiresAt,
// which the sweep reads as "never delete". No error, no log line — a retention
// leak that only an explicit check will find.
func TestRetentionManager_UnstampedFindsTheLeak(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	repo := NewMemoryRepository(clock)
	mgr, err := NewRetentionManager(repo, DefaultRetentionSchedule(),
		&MemoryArchiver{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Bypassing the manager, as an engine that called Put directly would.
	leaked := rec(RecordObservation, "leaked", clock.Now())
	if err := repo.Put(ctx, leaked); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Store(ctx, rec(RecordObservation, "stamped", clock.Now())); err != nil {
		t.Fatal(err)
	}

	unstamped, err := mgr.Unstamped(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(unstamped) != 1 || unstamped[0] != leaked.Key() {
		t.Errorf("Unstamped = %v, want [%s]", unstamped, leaked.Key())
	}
}

func TestRetentionManager_ReapplyReportsTheBlastRadius(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	repo := NewMemoryRepository(clock)
	ctx := context.Background()

	long, err := NewRetentionSchedule(
		RetentionPolicy{Name: "obs", Kinds: []RecordKind{RecordObservation, RecordRun},
			Duration: Retain180Days, Action: RetentionDelete, Rationale: "x"},
		RetentionPolicy{Name: "gold", Kinds: []RecordKind{RecordGolden},
			Duration: Retain180Days, Action: RetentionDelete, Rationale: "x"},
		RetentionPolicy{Name: "meas", Kinds: []RecordKind{RecordBenchmark, RecordTrend},
			Duration: Retain180Days, Action: RetentionDelete, Rationale: "x"},
		RetentionPolicy{Name: "aud", Kinds: []RecordKind{RecordAudit},
			Action: RetentionIndefinite, Rationale: "x"},
	)
	if err != nil {
		t.Fatal(err)
	}

	mgr, err := NewRetentionManager(repo, long, &MemoryArchiver{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := mgr.Store(ctx, rec(RecordObservation, string(rune('a'+i)), clock.Now())); err != nil {
			t.Fatal(err)
		}
	}

	// Shorten the window. Nothing should silently re-age until Reapply is
	// called, and Reapply must report how much it moved.
	short, err := NewRetentionSchedule(
		RetentionPolicy{Name: "obs", Kinds: []RecordKind{RecordObservation, RecordRun},
			Duration: Retain30Days, Action: RetentionDelete, Rationale: "x"},
		RetentionPolicy{Name: "gold", Kinds: []RecordKind{RecordGolden},
			Duration: Retain180Days, Action: RetentionDelete, Rationale: "x"},
		RetentionPolicy{Name: "meas", Kinds: []RecordKind{RecordBenchmark, RecordTrend},
			Duration: Retain180Days, Action: RetentionDelete, Rationale: "x"},
		RetentionPolicy{Name: "aud", Kinds: []RecordKind{RecordAudit},
			Action: RetentionIndefinite, Rationale: "x"},
	)
	if err != nil {
		t.Fatal(err)
	}
	mgr2, err := NewRetentionManager(repo, short, &MemoryArchiver{}, clock)
	if err != nil {
		t.Fatal(err)
	}

	changed, err := mgr2.Reapply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 5 {
		t.Errorf("Reapply changed %d records, want 5 — the blast radius of a "+
			"shortened window must be visible before the next sweep", changed)
	}
}

// ---------------------------------------------------------------------------
// Enforcement
// ---------------------------------------------------------------------------

func TestRetentionManager_ArchivesBeforeDeleting(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	repo := NewMemoryRepository(clock)
	archive := &MemoryArchiver{}
	mgr, err := NewRetentionManager(repo, DefaultRetentionSchedule(), archive, clock)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := mgr.Store(ctx, rec(RecordGolden, "g1", clock.Now())); err != nil {
		t.Fatal(err)
	}
	clock.Advance(Retain180Days + time.Hour)

	report, err := mgr.Enforce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalArchived() != 1 {
		t.Errorf("archived %d, want 1", report.TotalArchived())
	}
	if len(archive.Records) != 1 || archive.Records[0].ID != "g1" {
		t.Errorf("archiver received %v", archive.Records)
	}
	if _, err := repo.Get(ctx, RecordKey{Kind: RecordGolden, ID: "g1"}); !errors.Is(err, ErrRecordNotFound) {
		t.Error("the archived record is still in the live store")
	}
}

// TestRetentionManager_ArchiveFailureDeletesNothing is the data-loss guard.
func TestRetentionManager_ArchiveFailureDeletesNothing(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	repo := NewMemoryRepository(clock)
	mgr, err := NewRetentionManager(repo, DefaultRetentionSchedule(),
		failingArchiver{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := mgr.Store(ctx, rec(RecordGolden, "g1", clock.Now())); err != nil {
		t.Fatal(err)
	}
	clock.Advance(Retain180Days + time.Hour)

	if _, err := mgr.Enforce(ctx); err == nil {
		t.Fatal("a failing archive reported success")
	}
	// A sweep that ran first would have removed records the archive never
	// received, and the loss would surface only when somebody went looking.
	if _, err := repo.Get(ctx, RecordKey{Kind: RecordGolden, ID: "g1"}); err != nil {
		t.Error("the record was deleted despite the archive failing")
	}
}

func TestRetentionManager_RefusesArchivePolicyWithNoArchiver(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Now())
	repo := NewMemoryRepository(clock)

	// Silently deleting what a policy said to archive is a compliance failure
	// that looks exactly like correct operation.
	_, err := NewRetentionManager(repo, DefaultRetentionSchedule(), nil, clock)
	if err == nil {
		t.Fatal("an archive policy with no archiver was accepted")
	}
	if !strings.Contains(err.Error(), "looks identical to correct operation") {
		t.Errorf("error does not name the hazard: %v", err)
	}
}

func TestRetentionManager_LegalHoldSurvivesEnforcement(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	repo := NewMemoryRepository(clock)
	archive := &MemoryArchiver{}
	mgr, err := NewRetentionManager(repo, DefaultRetentionSchedule(), archive, clock)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	held := rec(RecordObservation, "held", clock.Now())
	if err := mgr.Store(ctx, held); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetLegalHold(ctx, held.Key(), true, "counsel", "matter 1"); err != nil {
		t.Fatal(err)
	}

	clock.Advance(Retain90Days + time.Hour)
	report, err := mgr.Enforce(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if report.Held == 0 {
		t.Error("the held record was not reported as retained")
	}
	if _, err := repo.Get(ctx, held.Key()); err != nil {
		t.Error("a record under legal hold was deleted by enforcement")
	}
	if !strings.Contains(report.Summary(), "RETAINED under legal hold") {
		t.Errorf("summary hides the hold: %q", report.Summary())
	}
}

func TestRetentionManager_NothingDueIsANoOp(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	repo := NewMemoryRepository(clock)
	mgr, err := NewRetentionManager(repo, DefaultRetentionSchedule(),
		&MemoryArchiver{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := mgr.Store(ctx, rec(RecordObservation, "fresh", clock.Now())); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour)

	report, err := mgr.Enforce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalArchived() != 0 || report.Sweep.Total() != 0 {
		t.Errorf("enforcement removed records that were not due: %s", report.Summary())
	}
}

// TestRetentionWindows_AreTheDocumentedDurations pins the three named windows.
func TestRetentionWindows_AreTheDocumentedDurations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		got  time.Duration
		days float64
	}{
		{"30 days", Retain30Days, 30},
		{"90 days", Retain90Days, 90},
		{"180 days", Retain180Days, 180},
	}
	for _, tc := range cases {
		if got := tc.got.Hours() / 24; got != tc.days {
			t.Errorf("%s = %v days, want %v", tc.name, got, tc.days)
		}
	}
}

type failingArchiver struct{}

func (failingArchiver) Archive(context.Context, []Record) error {
	return errors.New("cold storage unreachable")
}
