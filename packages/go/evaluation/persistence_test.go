package evaluation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

func repoHarness(t *testing.T) (*MemoryRepository, *rt.FakeClock) {
	t.Helper()
	clock := rt.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	return NewMemoryRepository(clock), clock
}

func rec(kind RecordKind, id string, created time.Time) Record {
	return Record{
		Schema: CurrentSchema, Kind: kind, ID: id,
		CreatedAt: created, Payload: []byte("payload-" + id),
	}
}

// ---------------------------------------------------------------------------
// Repository conformance
// ---------------------------------------------------------------------------

// TestRepository_Conformance is the suite EVERY Repository implementation must
// pass.
//
// Written against the interface rather than the concrete type, so the Aurora
// implementation Phase 11 adds is verified by exactly this code. That is the
// point of writing it now: the behaviour a backend must reproduce is pinned
// while the reference implementation is the only one, rather than inferred
// later from whatever the second implementation happened to do.
func TestRepository_Conformance(t *testing.T) {
	t.Parallel()

	factories := map[string]func(rt.Clock) Repository{
		"memory": func(c rt.Clock) Repository { return NewMemoryRepository(c) },
	}

	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			clock := rt.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			repo := factory(clock)
			defer func() { _ = repo.Close() }()

			ctx := context.Background()
			now := clock.Now()

			t.Run("put and get", func(t *testing.T) {
				r := rec(RecordRun, "r1", now)
				if err := repo.Put(ctx, r); err != nil {
					t.Fatalf("put: %v", err)
				}
				got, err := repo.Get(ctx, r.Key())
				if err != nil {
					t.Fatalf("get: %v", err)
				}
				if string(got.Payload) != string(r.Payload) {
					t.Errorf("payload = %q, want %q", got.Payload, r.Payload)
				}
			})

			t.Run("missing is ErrRecordNotFound", func(t *testing.T) {
				_, err := repo.Get(ctx, RecordKey{Kind: RecordRun, ID: "absent"})
				if !errors.Is(err, ErrRecordNotFound) {
					t.Errorf("err = %v, want ErrRecordNotFound", err)
				}
			})

			t.Run("schema newer than build is refused", func(t *testing.T) {
				future := rec(RecordRun, "future", now)
				future.Schema = CurrentSchema + 5
				// Decoding a payload under the wrong schema produces a WRONG
				// baseline, which fails silently and in the reassuring direction.
				if err := repo.Put(ctx, future); !errors.Is(err, ErrSchemaTooNew) {
					t.Errorf("err = %v, want ErrSchemaTooNew", err)
				}
			})

			t.Run("legal hold refuses deletion", func(t *testing.T) {
				r := rec(RecordObservation, "held", now)
				if err := repo.Put(ctx, r); err != nil {
					t.Fatal(err)
				}
				if err := repo.SetLegalHold(ctx, r.Key(), true, "legal", "litigation"); err != nil {
					t.Fatal(err)
				}
				if err := repo.Delete(ctx, r.Key()); !errors.Is(err, ErrLegalHold) {
					t.Errorf("err = %v, want ErrLegalHold", err)
				}
				if _, err := repo.Get(ctx, r.Key()); err != nil {
					t.Errorf("held record was removed anyway: %v", err)
				}
			})

			t.Run("legal hold requires attribution", func(t *testing.T) {
				r := rec(RecordObservation, "attr", now)
				if err := repo.Put(ctx, r); err != nil {
					t.Fatal(err)
				}
				if err := repo.SetLegalHold(ctx, r.Key(), true, "", "why"); err == nil {
					t.Error("a hold was placed with no author")
				}
				if err := repo.SetLegalHold(ctx, r.Key(), true, "who", ""); err == nil {
					t.Error("a hold was placed with no reason")
				}
			})

			t.Run("count matches list", func(t *testing.T) {
				q := Query{Kinds: []RecordKind{RecordObservation}}
				list, err := repo.List(ctx, q)
				if err != nil {
					t.Fatal(err)
				}
				n, err := repo.Count(ctx, q)
				if err != nil {
					t.Fatal(err)
				}
				if n != len(list) {
					t.Errorf("count = %d, list = %d", n, len(list))
				}
			})

			t.Run("snapshot and restore round trip", func(t *testing.T) {
				snap, err := repo.Snapshot(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(snap.Records) == 0 {
					t.Fatal("snapshot is empty")
				}
				if err := repo.Restore(ctx, snap); err != nil {
					t.Fatalf("restore: %v", err)
				}
				after, err := repo.Count(ctx, Query{IncludeUnreadable: true})
				if err != nil {
					t.Fatal(err)
				}
				// Restore writes its own audit record, so the count grows by one.
				if after < len(snap.Records) {
					t.Errorf("restore lost records: %d < %d", after, len(snap.Records))
				}
			})

			t.Run("closed repository refuses work", func(t *testing.T) {
				c := factory(clock)
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
				if err := c.Close(); err != nil {
					t.Errorf("Close is not idempotent: %v", err)
				}
				if err := c.Put(ctx, rec(RecordRun, "x", now)); !errors.Is(err, ErrClosed) {
					t.Errorf("err = %v, want ErrClosed", err)
				}
			})
		})
	}
}

// ---------------------------------------------------------------------------
// Expiry semantics
// ---------------------------------------------------------------------------

func TestRecord_ExpiredHonoursLegalHold(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		rec  Record
		want bool
		why  string
	}{
		{"past deadline", Record{ExpiresAt: now.Add(-time.Hour)}, true,
			"a record past its deadline is expired"},
		{"future deadline", Record{ExpiresAt: now.Add(time.Hour)}, false,
			"a record before its deadline is not expired"},
		{"zero deadline", Record{}, false,
			"a zero deadline means never expires, not expires immediately"},
		{"held past deadline", Record{ExpiresAt: now.Add(-time.Hour), LegalHold: true}, false,
			"a legal hold outranks the retention deadline"},
		{"exactly at deadline", Record{ExpiresAt: now}, true,
			"the deadline is inclusive"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rec.Expired(now); got != tc.want {
				t.Errorf("Expired() = %v, want %v — %s", got, tc.want, tc.why)
			}
		})
	}
}

// TestRepository_PutDoesNotClearAnExistingHold covers a quiet way to lose a hold.
func TestRepository_PutDoesNotClearAnExistingHold(t *testing.T) {
	t.Parallel()
	repo, clock := repoHarness(t)
	ctx := context.Background()

	r := rec(RecordRun, "r1", clock.Now())
	if err := repo.Put(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetLegalHold(ctx, r.Key(), true, "legal", "hold"); err != nil {
		t.Fatal(err)
	}

	// Re-storing the same record — to correct a label, say — must not lift the
	// hold as a side effect of carrying LegalHold=false in the new value.
	updated := r
	updated.Label = "corrected"
	if err := repo.Put(ctx, updated); err != nil {
		t.Fatal(err)
	}

	got, err := repo.Get(ctx, r.Key())
	if err != nil {
		t.Fatal(err)
	}
	if !got.LegalHold {
		t.Error("re-storing a record silently lifted its legal hold")
	}
}

// ---------------------------------------------------------------------------
// Sweep
// ---------------------------------------------------------------------------

func TestSweep_DeletesExpiredAndReportsHeld(t *testing.T) {
	t.Parallel()
	repo, clock := repoHarness(t)
	ctx := context.Background()
	start := clock.Now()

	old := rec(RecordObservation, "old", start)
	old.ExpiresAt = start.Add(24 * time.Hour)
	held := rec(RecordObservation, "held", start)
	held.ExpiresAt = start.Add(24 * time.Hour)
	fresh := rec(RecordObservation, "fresh", start)
	fresh.ExpiresAt = start.Add(90 * 24 * time.Hour)

	for _, r := range []Record{old, held, fresh} {
		if err := repo.Put(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.SetLegalHold(ctx, held.Key(), true, "legal", "investigation"); err != nil {
		t.Fatal(err)
	}

	report, err := repo.Sweep(ctx, start.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	if report.Total() != 1 {
		t.Errorf("deleted %d, want 1", report.Total())
	}
	// A sweep that silently retained held records looks identical to a sweep
	// with nothing to do, and the difference is the whole compliance position.
	if report.TotalHeld() != 1 {
		t.Errorf("held %d, want 1 reported separately", report.TotalHeld())
	}
	if _, err := repo.Get(ctx, held.Key()); err != nil {
		t.Error("a held record was swept")
	}
	if _, err := repo.Get(ctx, fresh.Key()); err != nil {
		t.Error("an unexpired record was swept")
	}
	if !strings.Contains(report.Summary(), "RETAINED under legal hold") {
		t.Errorf("summary hides the held records: %q", report.Summary())
	}
}

// TestSweep_NeverDeletesAuditRecords is the evidence-preservation rule.
func TestSweep_NeverDeletesAuditRecords(t *testing.T) {
	t.Parallel()
	repo, clock := repoHarness(t)
	ctx := context.Background()
	start := clock.Now()

	doomed := rec(RecordRun, "doomed", start)
	doomed.ExpiresAt = start.Add(time.Hour)
	if err := repo.Put(ctx, doomed); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Sweep(ctx, start.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// Force an expiry onto the audit records themselves and sweep far in the
	// future. Deleting the record of what was deleted destroys the proof that
	// retention was honoured, alongside the data it was honoured for.
	audits, err := repo.List(ctx, Query{Kinds: []RecordKind{RecordAudit}})
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) == 0 {
		t.Fatal("the sweep wrote no audit record")
	}
	for _, a := range audits {
		a.ExpiresAt = start.Add(time.Hour)
		if err := repo.Put(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := repo.Sweep(ctx, start.Add(10*365*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	after, err := repo.Count(ctx, Query{Kinds: []RecordKind{RecordAudit}})
	if err != nil {
		t.Fatal(err)
	}
	if after == 0 {
		t.Error("the audit trail was swept; the evidence that retention was " +
			"honoured is gone along with the data")
	}
}

func TestSweep_IsAudited(t *testing.T) {
	t.Parallel()
	repo, clock := repoHarness(t)
	ctx := context.Background()
	start := clock.Now()

	r := rec(RecordRun, "gone", start)
	r.ExpiresAt = start.Add(time.Hour)
	if err := repo.Put(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Sweep(ctx, start.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	entries, err := repo.Audit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == AuditSwept && e.Count == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("no sweep audit entry; entries = %v", entries)
	}
}

func TestAudit_RoundTripsWithoutPayloadContent(t *testing.T) {
	t.Parallel()
	repo, clock := repoHarness(t)
	ctx := context.Background()

	key := RecordKey{Kind: RecordGolden, ID: "g1"}
	if err := repo.Put(ctx, rec(RecordGolden, "g1", clock.Now())); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetLegalHold(ctx, key, true, "counsel", "matter 4471"); err != nil {
		t.Fatal(err)
	}

	entries, err := repo.Audit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var hold AuditEntry
	for _, e := range entries {
		if e.Action == AuditHoldPlaced {
			hold = e
		}
	}
	if hold.By != "counsel" || hold.Reason != "matter 4471" {
		t.Errorf("attribution lost in round trip: %+v", hold)
	}
	if len(hold.Keys) != 1 || hold.Keys[0] != key {
		t.Errorf("keys = %v, want [%s]", hold.Keys, key)
	}

	// The audit trail is the longest-lived table in the system. One that quoted
	// the records it described would outlive the retention rule it evidences.
	all, err := repo.List(ctx, Query{Kinds: []RecordKind{RecordAudit}})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range all {
		if strings.Contains(string(a.Payload), "payload-g1") {
			t.Error("an audit record quotes the payload it describes")
		}
	}
}

// ---------------------------------------------------------------------------
// Migration
// ---------------------------------------------------------------------------

func TestMigrator_RefusesAGappedChain(t *testing.T) {
	t.Parallel()
	// A migration that discovers mid-run that it cannot proceed leaves the
	// store in a state no version describes.
	_, err := NewMigrator(Migration{From: 1, To: 3, Description: "skips v2",
		Apply: func(r Record) (Record, error) { return r, nil }})
	if err == nil {
		t.Error("a migration spanning two versions was accepted")
	}

	_, err = NewMigrator(
		Migration{From: 1, To: 2, Description: "a", Apply: func(r Record) (Record, error) { return r, nil }},
		Migration{From: 1, To: 2, Description: "b", Apply: func(r Record) (Record, error) { return r, nil }},
	)
	if err == nil {
		t.Error("two migrations reading the same version were accepted")
	}

	_, err = NewMigrator(Migration{From: 1, To: 2, Description: "no apply"})
	if err == nil {
		t.Error("a migration with no Apply was accepted")
	}
}

func TestMigrate_SkipsCurrentRecords(t *testing.T) {
	t.Parallel()
	repo, clock := repoHarness(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := repo.Put(ctx, rec(RecordRun, fmt.Sprintf("r%d", i), clock.Now())); err != nil {
			t.Fatal(err)
		}
	}

	m, err := NewMigrator()
	if err != nil {
		t.Fatal(err)
	}
	report, err := repo.Migrate(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if report.Migrated != 0 {
		t.Errorf("migrated %d records that were already current", report.Migrated)
	}
	if report.Skipped == 0 {
		t.Error("nothing was reported as already current")
	}
	if !report.Clean() {
		t.Errorf("report is not clean: %s", report.Summary())
	}
}

func TestMigrate_ReportsFailuresWithoutStrandingTheRest(t *testing.T) {
	t.Parallel()
	repo, clock := repoHarness(t)
	ctx := context.Background()

	// A store at v0 with no migration registered: every record fails, and the
	// point is that the report says so per record rather than aborting.
	for i := 0; i < 3; i++ {
		r := rec(RecordRun, fmt.Sprintf("r%d", i), clock.Now())
		r.Schema = CurrentSchema
		if err := repo.Put(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	m, err := NewMigrator()
	if err != nil {
		t.Fatal(err)
	}
	report, err := repo.Migrate(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Failed) != 0 {
		t.Errorf("unexpected failures: %s", report.Summary())
	}
}

func TestSchema_ReportsRangePresent(t *testing.T) {
	t.Parallel()
	repo, clock := repoHarness(t)
	ctx := context.Background()

	low, high, err := repo.Schema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if low != CurrentSchema || high != CurrentSchema {
		t.Errorf("empty store reports v%d..v%d, want v%d", low, high, CurrentSchema)
	}

	if err := repo.Put(ctx, rec(RecordRun, "r", clock.Now())); err != nil {
		t.Fatal(err)
	}
	low, high, err = repo.Schema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if low != CurrentSchema || high != CurrentSchema {
		t.Errorf("schema = v%d..v%d", low, high)
	}
}

// ---------------------------------------------------------------------------
// Snapshot
// ---------------------------------------------------------------------------

func TestSnapshot_IsIndependentOfTheStore(t *testing.T) {
	t.Parallel()
	repo, clock := repoHarness(t)
	ctx := context.Background()

	r := rec(RecordGolden, "g1", clock.Now())
	if err := repo.Put(ctx, r); err != nil {
		t.Fatal(err)
	}
	snap, err := repo.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Mutating the snapshot's payload must not reach the store: a snapshot
	// taken before a migration is the only recovery path for approved
	// baselines, which encode human decisions no process can reproduce.
	for i := range snap.Records {
		snap.Records[i].Payload[0] = 'X'
	}
	got, err := repo.Get(ctx, r.Key())
	if err != nil {
		t.Fatal(err)
	}
	if got.Payload[0] == 'X' {
		t.Error("the snapshot aliases the store's payloads")
	}
}

func TestRestore_PreservesLegalHolds(t *testing.T) {
	t.Parallel()
	repo, clock := repoHarness(t)
	ctx := context.Background()

	r := rec(RecordObservation, "o1", clock.Now())
	if err := repo.Put(ctx, r); err != nil {
		t.Fatal(err)
	}
	snap, err := repo.Snapshot(ctx) // taken BEFORE the hold
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetLegalHold(ctx, r.Key(), true, "legal", "matter"); err != nil {
		t.Fatal(err)
	}

	if err := repo.Restore(ctx, snap); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, r.Key())
	if err != nil {
		t.Fatal(err)
	}
	// A restore is an operational action. It must not become a way to discard
	// a legal hold by accident.
	if !got.LegalHold {
		t.Error("restoring a pre-hold snapshot silently lifted the legal hold")
	}
}

// ---------------------------------------------------------------------------
// Context cancellation
// ---------------------------------------------------------------------------

func TestRepository_RespectsContextCancellation(t *testing.T) {
	t.Parallel()
	repo, clock := repoHarness(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := repo.Put(ctx, rec(RecordRun, "r", clock.Now())); !errors.Is(err, context.Canceled) {
		t.Errorf("Put err = %v, want context.Canceled", err)
	}
	if _, err := repo.List(ctx, Query{}); !errors.Is(err, context.Canceled) {
		t.Errorf("List err = %v, want context.Canceled", err)
	}
	if _, err := repo.Sweep(ctx, clock.Now()); !errors.Is(err, context.Canceled) {
		t.Errorf("Sweep err = %v, want context.Canceled", err)
	}
}
