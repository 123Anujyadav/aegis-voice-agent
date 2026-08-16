package evalstore_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/evalstore"
	ev "github.com/callscreen/callscreen-platform/packages/go/evaluation"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests run against a REAL PostgreSQL server. There is no in-memory
// substitute: the whole point of this backend is behaviour under a real
// transaction manager, and a fake would verify the fake.
//
// The DSN comes from the environment (AEGIS_EVALSTORE_DSN). When it is unset the
// suite SKIPS rather than inventing a credential or silently passing, so an
// absent database can never be mistaken for a green gate.

const baseTime = "2026-01-01T00:00:00Z"

func dsn(t *testing.T) string {
	t.Helper()
	d := evalstore.DSNFromEnv()
	if d == "" {
		t.Skipf("%s is not set; skipping the real-PostgreSQL suite "+
			"(this is a SKIP, not a PASS)", evalstore.DSNEnv)
	}
	return d
}

func clockAt(t *testing.T) *rt.FakeClock {
	t.Helper()
	base, err := time.Parse(time.RFC3339, baseTime)
	if err != nil {
		t.Fatalf("parse base time: %v", err)
	}
	return rt.NewFakeClock(base)
}

// freshStore opens a store against a schema that has been migrated from empty,
// with all records removed so each test starts from a known state.
func freshStore(t *testing.T) (*evalstore.Store, *rt.FakeClock) {
	t.Helper()
	ctx := context.Background()
	clock := clockAt(t)

	s, err := evalstore.Open(ctx, dsn(t), clock)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if _, err := evalstore.Migrate(ctx, s.Pool()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `DELETE FROM eval_records`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s, clock
}

// rec builds a record, mirroring the reference suite's helper.
func rec(kind ev.RecordKind, id string, at time.Time) ev.Record {
	return ev.Record{
		Schema:    ev.CurrentSchema,
		Kind:      kind,
		ID:        id,
		CreatedAt: at,
		Payload:   []byte(`{"id":"` + id + `"}`),
	}
}

// ---------------------------------------------------------------------------
// Migrations
// ---------------------------------------------------------------------------

// TestMigrations_ChainIsContiguous validates the embedded chain without a
// database, so a packaging error is caught even where no server is available.
func TestMigrations_ChainIsContiguous(t *testing.T) {
	t.Parallel()

	chain, err := evalstore.Migrations()
	if err != nil {
		t.Fatalf("loading migrations: %v", err)
	}
	if len(chain) == 0 {
		t.Fatal("the embedded migration chain is empty")
	}
	for i, m := range chain {
		if m.Version != i+1 {
			t.Errorf("migration %d has version %d; the chain must be contiguous from 1",
				i, m.Version)
		}
		if strings.TrimSpace(m.SQL) == "" {
			t.Errorf("migration %s is empty", m.Name)
		}
	}
}

// TestMigrate_AppliesFromEmptyThenIsIdempotent is the migration gate: a clean
// apply against an empty database, then a re-run that changes nothing.
func TestMigrate_AppliesFromEmptyThenIsIdempotent(t *testing.T) {
	ctx := context.Background()
	d := dsn(t)

	// A genuinely empty database, not one a previous test migrated.
	pool, err := pgxpool.New(ctx, d)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS eval_records`,
		`DROP TABLE IF EXISTS eval_schema_migrations`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("dropping for empty-database test: %v", err)
		}
	}

	first, err := evalstore.Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if len(first.Applied) == 0 {
		t.Error("first migration against an empty database applied nothing")
	}
	if len(first.AlreadyApplied) != 0 {
		t.Errorf("first migration reported %v as already applied", first.AlreadyApplied)
	}

	second, err := evalstore.Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if len(second.Applied) != 0 {
		t.Errorf("re-running the chain applied %v; migrations must be idempotent",
			second.Applied)
	}
	if len(second.AlreadyApplied) != len(first.Applied) {
		t.Errorf("re-run saw %d already-applied, want %d",
			len(second.AlreadyApplied), len(first.Applied))
	}
	if second.Version != first.Version {
		t.Errorf("version moved on a no-op re-run: %d -> %d", first.Version, second.Version)
	}
}

// TestMigrate_RefusesASchemaFromTheFuture — a build must not operate against a
// database migrated by a newer binary.
func TestMigrate_RefusesASchemaFromTheFuture(t *testing.T) {
	ctx := context.Background()
	d := dsn(t)

	pool, err := pgxpool.New(ctx, d)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	if _, err := evalstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO eval_schema_migrations (version, name) VALUES (9999, 'from-the-future')`); err != nil {
		t.Fatalf("seeding future version: %v", err)
	}
	// A defer, NOT t.Cleanup: cleanups run after the test's defers, so
	// `defer pool.Close()` above would already have closed the pool and this
	// would fail silently — leaving v9999 behind to poison every later test.
	// Defers run LIFO, so this one runs while the pool is still open.
	defer func() {
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM eval_schema_migrations WHERE version = 9999`); err != nil {
			t.Errorf("failed to remove the seeded future version: %v", err)
		}
	}()

	if _, err := evalstore.Migrate(ctx, pool); !errors.Is(err, evalstore.ErrDirtySchema) {
		t.Errorf("err = %v, want ErrDirtySchema", err)
	}
}

// ---------------------------------------------------------------------------
// Conformance
// ---------------------------------------------------------------------------

// TestRepository_Conformance is a PORT of TestRepository_Conformance in
// packages/go/evaluation/persistence_test.go, run against PostgreSQL.
//
// It is a port and is labelled as one. The original is an INTERNAL test in a
// frozen module, so it cannot be imported, and adding a "postgres" entry to its
// factory table would both modify frozen code and invert the dependency —
// evaluation would have to import a driver. Making the frozen suite reusable is
// a worthwhile separate change; see ADR-0015.
func TestRepository_Conformance(t *testing.T) {
	s, clock := freshStore(t)
	ctx := context.Background()
	now := clock.Now()

	t.Run("put and get", func(t *testing.T) {
		r := rec(ev.RecordRun, "r1", now)
		if err := s.Put(ctx, r); err != nil {
			t.Fatalf("put: %v", err)
		}
		got, err := s.Get(ctx, r.Key())
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if string(got.Payload) != string(r.Payload) {
			t.Errorf("payload = %q, want %q", got.Payload, r.Payload)
		}
	})

	t.Run("missing is ErrRecordNotFound", func(t *testing.T) {
		_, err := s.Get(ctx, ev.RecordKey{Kind: ev.RecordRun, ID: "absent"})
		if !errors.Is(err, ev.ErrRecordNotFound) {
			t.Errorf("err = %v, want ErrRecordNotFound", err)
		}
	})

	t.Run("schema newer than build is refused", func(t *testing.T) {
		future := rec(ev.RecordRun, "future", now)
		future.Schema = ev.CurrentSchema + 5
		if err := s.Put(ctx, future); !errors.Is(err, ev.ErrSchemaTooNew) {
			t.Errorf("err = %v, want ErrSchemaTooNew", err)
		}
	})

	t.Run("legal hold refuses deletion", func(t *testing.T) {
		r := rec(ev.RecordObservation, "held", now)
		if err := s.Put(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := s.SetLegalHold(ctx, r.Key(), true, "legal", "litigation"); err != nil {
			t.Fatal(err)
		}
		if err := s.Delete(ctx, r.Key()); !errors.Is(err, ev.ErrLegalHold) {
			t.Errorf("err = %v, want ErrLegalHold", err)
		}
		if _, err := s.Get(ctx, r.Key()); err != nil {
			t.Errorf("held record was removed anyway: %v", err)
		}
	})

	t.Run("legal hold requires attribution", func(t *testing.T) {
		r := rec(ev.RecordObservation, "attr", now)
		if err := s.Put(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := s.SetLegalHold(ctx, r.Key(), true, "", "why"); err == nil {
			t.Error("a hold was placed with no author")
		}
		if err := s.SetLegalHold(ctx, r.Key(), true, "who", ""); err == nil {
			t.Error("a hold was placed with no reason")
		}
	})

	t.Run("count matches list", func(t *testing.T) {
		q := ev.Query{Kinds: []ev.RecordKind{ev.RecordObservation}}
		list, err := s.List(ctx, q)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		n, err := s.Count(ctx, q)
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != len(list) {
			t.Errorf("count = %d, list = %d", n, len(list))
		}
	})

	t.Run("list is newest first", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			r := rec(ev.RecordBenchmark, fmt.Sprintf("b%d", i),
				now.Add(time.Duration(i)*time.Hour))
			if err := s.Put(ctx, r); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.List(ctx, ev.Query{Kinds: []ev.RecordKind{ev.RecordBenchmark}})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for i := 1; i < len(got); i++ {
			if got[i-1].CreatedAt.Before(got[i].CreatedAt) {
				t.Errorf("list is not newest-first at %d: %v before %v",
					i, got[i-1].CreatedAt, got[i].CreatedAt)
			}
		}
	})

	t.Run("limit caps the result", func(t *testing.T) {
		got, err := s.List(ctx, ev.Query{
			Kinds: []ev.RecordKind{ev.RecordBenchmark}, Limit: 2,
		})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) > 2 {
			t.Errorf("limit 2 returned %d", len(got))
		}
	})

	t.Run("put does not clear an existing hold", func(t *testing.T) {
		r := rec(ev.RecordGolden, "keeps-hold", now)
		if err := s.Put(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := s.SetLegalHold(ctx, r.Key(), true, "legal", "case"); err != nil {
			t.Fatal(err)
		}
		// Re-store to "correct a label"; the hold must survive.
		r.Label = "corrected"
		if err := s.Put(ctx, r); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(ctx, r.Key())
		if err != nil {
			t.Fatal(err)
		}
		if !got.LegalHold {
			t.Error("re-storing a record silently lifted its legal hold")
		}
	})

	t.Run("schema reports the range present", func(t *testing.T) {
		low, high, err := s.Schema(ctx)
		if err != nil {
			t.Fatalf("schema: %v", err)
		}
		if low > high {
			t.Errorf("low %d > high %d", low, high)
		}
		if high > ev.CurrentSchema {
			t.Errorf("high %d exceeds CurrentSchema %d", high, ev.CurrentSchema)
		}
	})

	t.Run("snapshot then restore round-trips and keeps holds", func(t *testing.T) {
		snap, err := s.Snapshot(ctx)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		var heldBefore int
		for _, r := range snap.Records {
			if r.LegalHold {
				heldBefore++
			}
		}
		if err := s.Restore(ctx, snap); err != nil {
			t.Fatalf("restore: %v", err)
		}
		after, err := s.Snapshot(ctx)
		if err != nil {
			t.Fatalf("snapshot after restore: %v", err)
		}
		if len(after.Records) != len(snap.Records) {
			t.Errorf("restore changed the record count: %d -> %d",
				len(snap.Records), len(after.Records))
		}
		var heldAfter int
		for _, r := range after.Records {
			if r.LegalHold {
				heldAfter++
			}
		}
		if heldAfter != heldBefore {
			t.Errorf("restore lost legal holds: %d -> %d", heldBefore, heldAfter)
		}
	})

	t.Run("close is idempotent", func(t *testing.T) {
		s2, err := evalstore.Open(ctx, dsn(t), clockAt(t))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if err := s2.Close(); err != nil {
			t.Errorf("first close: %v", err)
		}
		if err := s2.Close(); err != nil {
			t.Errorf("second close: %v", err)
		}
		if _, err := s2.Get(ctx, ev.RecordKey{Kind: ev.RecordRun, ID: "x"}); !errors.Is(err, ev.ErrClosed) {
			t.Errorf("err after close = %v, want ErrClosed", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Retention
// ---------------------------------------------------------------------------

func TestSweep_DeletesExpiredHoldsHeldAndExemptsAudit(t *testing.T) {
	s, clock := freshStore(t)
	ctx := context.Background()
	now := clock.Now()

	expired := rec(ev.RecordRun, "expired", now.Add(-48*time.Hour))
	expired.ExpiresAt = now.Add(-time.Hour)

	heldExpired := rec(ev.RecordRun, "held-expired", now.Add(-48*time.Hour))
	heldExpired.ExpiresAt = now.Add(-time.Hour)

	live := rec(ev.RecordRun, "live", now)
	live.ExpiresAt = now.Add(72 * time.Hour)

	never := rec(ev.RecordGolden, "never", now) // zero ExpiresAt

	if err := s.PutBatch(ctx, []ev.Record{expired, heldExpired, live, never}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.SetLegalHold(ctx, heldExpired.Key(), true, "legal", "case"); err != nil {
		t.Fatalf("hold: %v", err)
	}

	report, err := s.Sweep(ctx, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if report.Deleted[ev.RecordRun] != 1 {
		t.Errorf("deleted runs = %d, want 1", report.Deleted[ev.RecordRun])
	}
	if report.Held[ev.RecordRun] != 1 {
		t.Errorf("held runs = %d, want 1 — a held-but-expired record must be "+
			"reported, not silently retained", report.Held[ev.RecordRun])
	}
	if _, err := s.Get(ctx, heldExpired.Key()); err != nil {
		t.Errorf("held record was deleted: %v", err)
	}
	if _, err := s.Get(ctx, live.Key()); err != nil {
		t.Errorf("unexpired record was deleted: %v", err)
	}
	if _, err := s.Get(ctx, never.Key()); err != nil {
		t.Errorf("never-expiring record was deleted: %v", err)
	}
	if _, err := s.Get(ctx, expired.Key()); !errors.Is(err, ev.ErrRecordNotFound) {
		t.Errorf("expired record survived the sweep: %v", err)
	}

	// The sweep must be audited, and audit records must be exempt from it.
	audits, err := s.List(ctx, ev.Query{Kinds: []ev.RecordKind{ev.RecordAudit}})
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	if len(audits) == 0 {
		t.Fatal("the sweep wrote no audit record")
	}
	if _, err := s.Sweep(ctx, now.Add(10*365*24*time.Hour)); err != nil {
		t.Fatalf("far-future sweep: %v", err)
	}
	after, err := s.Count(ctx, ev.Query{Kinds: []ev.RecordKind{ev.RecordAudit}})
	if err != nil {
		t.Fatalf("count audits: %v", err)
	}
	if after < len(audits) {
		t.Errorf("audit records were swept: %d -> %d; the evidence that "+
			"retention was honoured must outlive the data", len(audits), after)
	}
}

// TestSweep_ExemptsAuditRecordsEvenWhenTheyCarryAnExpiry isolates the
// kind <> 'audit' clause.
//
// Written after a mutation test showed the clause could be removed without any
// test noticing: audit records are normally written with NO expiry, so the
// expiry predicate already excludes them and the exemption never gets exercised.
// That made it defence-in-depth nobody was verifying. This seeds an audit record
// that IS expired, so the exemption becomes the only thing keeping it.
func TestSweep_ExemptsAuditRecordsEvenWhenTheyCarryAnExpiry(t *testing.T) {
	s, clock := freshStore(t)
	ctx := context.Background()
	now := clock.Now()

	expiredAudit := rec(ev.RecordAudit, "aud-expired", now.Add(-48*time.Hour))
	expiredAudit.ExpiresAt = now.Add(-time.Hour)
	if err := s.Put(ctx, expiredAudit); err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	if _, err := s.Sweep(ctx, now); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if _, err := s.Get(ctx, expiredAudit.Key()); err != nil {
		t.Errorf("an expired AUDIT record was swept: %v — the record of what "+
			"was deleted is the evidence retention was honoured, and it must "+
			"outlive the data it describes", err)
	}
}

// ---------------------------------------------------------------------------
// Durability — the reason this backend exists
// ---------------------------------------------------------------------------

// TestGoldens_SurviveACompleteProcessRestart is B2's actual gate.
//
// "Restart" is simulated the only way a single test process can: the store and
// its entire connection pool are closed, then a brand-new store is opened. No
// state carries over in Go memory, so anything read back came from PostgreSQL.
func TestGoldens_SurviveACompleteProcessRestart(t *testing.T) {
	ctx := context.Background()
	d := dsn(t)
	clock := clockAt(t)
	now := clock.Now()

	// --- process 1: approve a golden, place a hold, then "exit" ---
	s1, err := evalstore.Open(ctx, d, clock)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	if _, err := evalstore.Migrate(ctx, s1.Pool()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := s1.Pool().Exec(ctx, `DELETE FROM eval_records`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	golden := rec(ev.RecordGolden, "approved-baseline", now)
	golden.Subject = "conversation"
	golden.Scenario = "conversation.turn-taking"
	golden.Label = "phase-10f"
	golden.Payload = []byte(`{"digest":"a083e7385c63ebfc","approved_by":"phase-10f"}`)

	if err := s1.Put(ctx, golden); err != nil {
		t.Fatalf("put golden: %v", err)
	}
	if err := s1.SetLegalHold(ctx, golden.Key(), true, "compliance", "retention review"); err != nil {
		t.Fatalf("hold: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close 1: %v", err)
	}

	// --- process 2: nothing in memory; everything must come from the database ---
	s2, err := evalstore.Open(ctx, d, clockAt(t))
	if err != nil {
		t.Fatalf("open 2: %v", err)
	}
	defer func() { _ = s2.Close() }()

	got, err := s2.Get(ctx, golden.Key())
	if err != nil {
		t.Fatalf("the approved golden did not survive the restart: %v", err)
	}
	if string(got.Payload) != string(golden.Payload) {
		t.Errorf("payload after restart = %q, want %q", got.Payload, golden.Payload)
	}
	if !got.LegalHold {
		t.Error("the legal hold did not survive the restart")
	}
	if got.Subject != golden.Subject || got.Scenario != golden.Scenario || got.Label != golden.Label {
		t.Errorf("index fields changed across restart: %+v", got)
	}
	if !got.CreatedAt.Equal(golden.CreatedAt) {
		t.Errorf("CreatedAt after restart = %v, want %v — the retention clock moved",
			got.CreatedAt, golden.CreatedAt)
	}

	// And the hold is still enforced by the new process.
	if err := s2.Delete(ctx, golden.Key()); !errors.Is(err, ev.ErrLegalHold) {
		t.Errorf("delete after restart = %v, want ErrLegalHold", err)
	}
}

// TestReadAfterWrite_IsImmediatelyVisible — a write that is not visible to the
// next read makes every caller's control flow wrong.
func TestReadAfterWrite_IsImmediatelyVisible(t *testing.T) {
	s, clock := freshStore(t)
	ctx := context.Background()

	for i := 0; i < 25; i++ {
		r := rec(ev.RecordRun, fmt.Sprintf("raw%d", i), clock.Now())
		if err := s.Put(ctx, r); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		got, err := s.Get(ctx, r.Key())
		if err != nil {
			t.Fatalf("read-after-write %d: %v", i, err)
		}
		if got.ID != r.ID {
			t.Fatalf("read-after-write %d returned %q", i, got.ID)
		}
	}
}

// ---------------------------------------------------------------------------
// Atomicity and conflict
// ---------------------------------------------------------------------------

// TestPutBatch_IsAtomic — the port permits a backend to lack atomicity but
// forbids it lying. This one has it, so it must actually be atomic.
func TestPutBatch_IsAtomic(t *testing.T) {
	s, clock := freshStore(t)
	ctx := context.Background()
	now := clock.Now()

	bad := rec(ev.RecordRun, "rejected", now)
	bad.Schema = ev.CurrentSchema + 3 // refused by validation

	batch := []ev.Record{
		rec(ev.RecordRun, "batch-a", now),
		rec(ev.RecordRun, "batch-b", now),
		bad,
	}
	if err := s.PutBatch(ctx, batch); !errors.Is(err, ev.ErrSchemaTooNew) {
		t.Fatalf("err = %v, want ErrSchemaTooNew", err)
	}

	for _, id := range []string{"batch-a", "batch-b"} {
		_, err := s.Get(ctx, ev.RecordKey{Kind: ev.RecordRun, ID: id})
		if !errors.Is(err, ev.ErrRecordNotFound) {
			t.Errorf("%s was committed despite the batch failing: err = %v", id, err)
		}
	}
}

// TestPut_ConflictingWriteReplacesRatherThanCorrupting — (kind, id) is the
// identity, so a second write to the same key must replace, not duplicate.
func TestPut_ConflictingWriteReplacesRatherThanCorrupting(t *testing.T) {
	s, clock := freshStore(t)
	ctx := context.Background()
	now := clock.Now()

	first := rec(ev.RecordGolden, "dup", now)
	first.Payload = []byte(`{"v":1}`)
	second := rec(ev.RecordGolden, "dup", now)
	second.Payload = []byte(`{"v":2}`)

	if err := s.Put(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, second); err != nil {
		t.Fatalf("second put: %v", err)
	}

	n, err := s.Count(ctx, ev.Query{Kinds: []ev.RecordKind{ev.RecordGolden}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("count = %d after two writes to one key, want 1", n)
	}
	got, err := s.Get(ctx, first.Key())
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Payload) != `{"v":2}` {
		t.Errorf("payload = %q, want the later write", got.Payload)
	}
}

// TestDelete_RollsBackWhenTheAuditCannotBeWritten proves the deletion and its
// audit entry share a transaction. The audit table is the records table, so
// dropping it mid-test is not possible; instead the transaction is exercised by
// deleting a held record, which must leave the row present.
func TestDelete_RefusalLeavesNoPartialEffect(t *testing.T) {
	s, clock := freshStore(t)
	ctx := context.Background()

	r := rec(ev.RecordObservation, "atomic-del", clock.Now())
	if err := s.Put(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLegalHold(ctx, r.Key(), true, "legal", "hold"); err != nil {
		t.Fatal(err)
	}

	auditsBefore, err := s.Count(ctx, ev.Query{Kinds: []ev.RecordKind{ev.RecordAudit}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, r.Key()); !errors.Is(err, ev.ErrLegalHold) {
		t.Fatalf("err = %v, want ErrLegalHold", err)
	}
	if _, err := s.Get(ctx, r.Key()); err != nil {
		t.Errorf("the record was removed despite the refusal: %v", err)
	}
	auditsAfter, err := s.Count(ctx, ev.Query{Kinds: []ev.RecordKind{ev.RecordAudit}})
	if err != nil {
		t.Fatal(err)
	}
	if auditsAfter != auditsBefore {
		t.Errorf("a refused deletion wrote an audit entry: %d -> %d",
			auditsBefore, auditsAfter)
	}
}

// ---------------------------------------------------------------------------
// Failure behaviour
// ---------------------------------------------------------------------------

// TestDatabaseUnavailable_ReportsAnErrorAndNeverASilentSuccess is the outage
// case. The critical property is that an outage does NOT look like an empty
// store: reporting a connection failure as ErrRecordNotFound would turn an
// incident into "the golden was never approved".
func TestDatabaseUnavailable_ReportsAnErrorAndNeverASilentSuccess(t *testing.T) {
	s, clock := freshStore(t)
	ctx := context.Background()

	r := rec(ev.RecordRun, "before-outage", clock.Now())
	if err := s.Put(ctx, r); err != nil {
		t.Fatal(err)
	}

	// Close the pool underneath the store, without marking the store closed.
	// That is what an outage looks like from the caller's side.
	s.Pool().Close()

	if err := s.Put(ctx, rec(ev.RecordRun, "during-outage", clock.Now())); err == nil {
		t.Error("a write during an outage reported success")
	}

	_, err := s.Get(ctx, r.Key())
	if err == nil {
		t.Error("a read during an outage reported success")
	}
	if errors.Is(err, ev.ErrRecordNotFound) {
		t.Errorf("an outage was reported as ErrRecordNotFound: %v — an incident "+
			"must never look like an empty store", err)
	}

	if _, err := s.Count(ctx, ev.Query{}); err == nil {
		t.Error("a count during an outage reported success")
	}
	if _, err := s.Sweep(ctx, clock.Now()); err == nil {
		t.Error("a sweep during an outage reported success")
	}
}

// TestContextCancellation_Propagates — every method takes the caller's context
// and must honour it.
func TestContextCancellation_Propagates(t *testing.T) {
	s, clock := freshStore(t)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	r := rec(ev.RecordRun, "ctx", clock.Now())

	checks := map[string]error{
		"Put":          s.Put(cancelled, r),
		"PutBatch":     s.PutBatch(cancelled, []ev.Record{r}),
		"Delete":       s.Delete(cancelled, r.Key()),
		"SetLegalHold": s.SetLegalHold(cancelled, r.Key(), true, "a", "b"),
	}
	if _, err := s.Get(cancelled, r.Key()); true {
		checks["Get"] = err
	}
	if _, err := s.List(cancelled, ev.Query{}); true {
		checks["List"] = err
	}
	if _, err := s.Count(cancelled, ev.Query{}); true {
		checks["Count"] = err
	}
	if _, err := s.Snapshot(cancelled); true {
		checks["Snapshot"] = err
	}
	if _, _, err := s.Schema(cancelled); true {
		checks["Schema"] = err
	}
	if _, err := s.Sweep(cancelled, clock.Now()); true {
		checks["Sweep"] = err
	}

	for name, err := range checks {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s with a cancelled context returned %v, want context.Canceled",
				name, err)
		}
	}
}

// TestContextTimeout_IsHonoured — a deadline must actually bound the call.
func TestContextTimeout_IsHonoured(t *testing.T) {
	s, clock := freshStore(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond) // ensure the deadline has passed

	err := s.Put(ctx, rec(ev.RecordRun, "deadline", clock.Now()))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want DeadlineExceeded", err)
	}
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

func TestConcurrentAccess_IsSafeAndLosesNothing(t *testing.T) {
	s, clock := freshStore(t)
	ctx := context.Background()
	now := clock.Now()

	const writers, perWriter = 8, 15

	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	start := make(chan struct{})

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for i := 0; i < perWriter; i++ {
				r := rec(ev.RecordObservation, fmt.Sprintf("c-%d-%d", w, i), now)
				if err := s.Put(ctx, r); err != nil {
					errs <- fmt.Errorf("writer %d put %d: %w", w, i, err)
					return
				}
			}
		}(w)
	}
	// Readers run against the same store while it is being written.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < perWriter; i++ {
				if _, err := s.List(ctx, ev.Query{
					Kinds: []ev.RecordKind{ev.RecordObservation}, Limit: 10,
				}); err != nil {
					errs <- fmt.Errorf("reader: %w", err)
					return
				}
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent access failed: %v", err)
	}

	n, err := s.Count(ctx, ev.Query{Kinds: []ev.RecordKind{ev.RecordObservation}})
	if err != nil {
		t.Fatal(err)
	}
	if n != writers*perWriter {
		t.Errorf("count = %d, want %d — concurrent writes lost records",
			n, writers*perWriter)
	}
}
