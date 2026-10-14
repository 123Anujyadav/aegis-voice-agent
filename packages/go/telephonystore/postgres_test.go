package telephonystore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
	"github.com/callscreen/callscreen-platform/packages/go/telephony/callback"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Integration tests against a REAL PostgreSQL server. They prove what the unit
// tests cannot: that the constraints make binding atomic across connections,
// and how the adapter behaves against real locks, timeouts and errors.
//
// # Opt-in, and isolated
//
// They run only when TestDSNEnv names a DISPOSABLE test database (see
// tools/scripts/telephony-integration.sh, which starts a throwaway container).
// When it is unset the suite SKIPS — a skip, not a pass. When it IS set, any
// connection or assertion failure FAILS the test.
//
// Every test creates its own schema (it_<random>), points its pools at it with
// search_path, migrates into it, and drops it with CASCADE afterwards. No test
// touches anything outside its own schema, and nothing truncates or drops
// shared objects. The DSN is never printed: errors pass through redact.

// TestDSNEnv names the environment variable holding a disposable test DSN.
const TestDSNEnv = "CS_TELEPHONYSTORE_TEST_DSN"

// itTimeout bounds every integration test's database work.
const itTimeout = 60 * time.Second

type pgEnv struct {
	dsn    string
	schema string
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// isolated creates a fresh schema for this test and drops it afterwards.
func isolated(t *testing.T) pgEnv {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(TestDSNEnv))
	if dsn == "" {
		t.Skipf("%s is not set; skipping the real-PostgreSQL suite (this is a SKIP, not a PASS)", TestDSNEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), itTimeout)
	defer cancel()

	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("admin pool: %v", redact(err))
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Fatalf("ping: %v", redact(err))
	}
	env := pgEnv{dsn: dsn, schema: "it_" + randomHex(t, 6)}
	ident := pgx.Identifier{env.schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
		admin.Close()
		t.Fatalf("create schema: %v", redact(err))
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), itTimeout)
		defer ccancel()
		if _, err := admin.Exec(cctx, "DROP SCHEMA "+ident+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", redact(err))
		}
		admin.Close()
	})
	return env
}

// pool opens a pool whose every connection uses the test's schema.
func (e pgEnv) pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(e.dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", redact(err))
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = e.schema
	ctx, cancel := context.WithTimeout(context.Background(), itTimeout)
	defer cancel()
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pool: %v", redact(err))
	}
	t.Cleanup(p.Close)
	return p
}

// migrated returns an isolated schema with the chain applied, and a pool on it.
func migrated(t *testing.T) (pgEnv, *pgxpool.Pool) {
	t.Helper()
	env := isolated(t)
	p := env.pool(t)
	ctx, cancel := context.WithTimeout(context.Background(), itTimeout)
	defer cancel()
	res, err := Migrate(ctx, p)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if res.Version != 1 || len(res.Applied) != 1 {
		t.Fatalf("first Migrate = %+v, want version 1 applied", res)
	}
	return env, p
}

type itFixture struct {
	env      pgEnv
	pool     *pgxpool.Pool
	b        *Bindings
	provider telephony.ProviderID
	other    telephony.ProviderID
	call1    telephony.CallID
	call2    telephony.CallID
	call3    telephony.CallID
	calls    fakeCalls
}

func fixture(t *testing.T) *itFixture {
	t.Helper()
	env, pool := migrated(t)
	s := randomHex(t, 4)
	f := &itFixture{
		env: env, pool: pool,
		provider: telephony.ProviderID("it-a-" + s),
		other:    telephony.ProviderID("it-b-" + s),
		call1:    telephony.CallID("call_it1_" + s),
		call2:    telephony.CallID("call_it2_" + s),
		call3:    telephony.CallID("call_it3_" + s),
	}
	f.calls = fakeCalls{f.call1: f.provider, f.call2: f.provider, f.call3: f.other}
	f.b = f.store(t, pool, 10*time.Second)
	return f
}

func (f *itFixture) store(t *testing.T, p *pgxpool.Pool, opTimeout time.Duration) *Bindings {
	t.Helper()
	b, err := NewBindings(p, f.calls, Config{OpTimeout: opTimeout})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func itCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), itTimeout)
	t.Cleanup(cancel)
	return ctx
}

// ---------------------------------------------------------------------------
// Schema
// ---------------------------------------------------------------------------

func TestPostgres_MigrationCreatesTheTableInTheIsolatedSchemaAndIsIdempotent(t *testing.T) {
	env, p := migrated(t)
	ctx := itCtx(t)

	var where string
	if err := p.QueryRow(ctx,
		`SELECT table_schema FROM information_schema.tables WHERE table_name = 'telephony_carrier_call_bindings' AND table_schema = $1`,
		env.schema).Scan(&where); err != nil {
		t.Fatalf("table not found in the test schema: %v", redact(err))
	}
	res, err := Migrate(ctx, p)
	if err != nil || len(res.Applied) != 0 || res.Version != 1 {
		t.Errorf("second Migrate = %+v, %v; want nothing applied at v1", res, err)
	}
	var constraints int
	if err := p.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.table_constraints
		 WHERE table_schema = $1 AND table_name = 'telephony_carrier_call_bindings'
		   AND constraint_type IN ('PRIMARY KEY', 'UNIQUE', 'CHECK')`, env.schema).Scan(&constraints); err != nil {
		t.Fatal(redact(err))
	}
	// PK + UNIQUE(call_id) + 3 named CHECKs, plus PostgreSQL's NOT NULL checks.
	if constraints < 5 {
		t.Errorf("%d constraints, want at least 5", constraints)
	}
}

func TestPostgres_SchemaCleanupRemovesEverything(t *testing.T) {
	env := isolated(t)
	p := env.pool(t)
	ctx := itCtx(t)
	if _, err := Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.Close()

	// Drop it here, as the cleanup will, and prove nothing is left.
	admin, err := pgxpool.New(ctx, env.dsn)
	if err != nil {
		t.Fatal(redact(err))
	}
	defer admin.Close()
	ident := pgx.Identifier{env.schema}.Sanitize()
	if _, err := admin.Exec(ctx, "DROP SCHEMA "+ident+" CASCADE"); err != nil {
		t.Fatal(redact(err))
	}
	var n int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = $1`, env.schema).Scan(&n); err != nil {
		t.Fatal(redact(err))
	}
	if n != 0 {
		t.Errorf("%d tables remain after dropping the schema", n)
	}
	// Recreate so the registered cleanup's DROP succeeds.
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
		t.Fatal(redact(err))
	}
}

// ---------------------------------------------------------------------------
// Binding behaviour
// ---------------------------------------------------------------------------

func TestPostgres_BindResolveRemove(t *testing.T) {
	f := fixture(t)
	ctx := itCtx(t)
	b := callback.Binding{Provider: f.provider, ProviderCallID: "pc-1", Call: f.call1}

	if err := f.b.BindCall(ctx, b); err != nil {
		t.Fatalf("BindCall: %v", err)
	}
	if err := f.b.BindCall(ctx, b); err != nil {
		t.Errorf("idempotent BindCall: %v", err)
	}
	if id, err := f.b.ResolveContext(ctx, f.provider, "pc-1"); err != nil || id != f.call1 {
		t.Errorf("Resolve = %s, %v", id, err)
	}
	if ok, err := f.b.Remove(ctx, f.provider, "pc-1"); err != nil || !ok {
		t.Errorf("Remove = %v, %v", ok, err)
	}
	if ok, _ := f.b.Remove(ctx, f.provider, "pc-1"); ok {
		t.Error("second Remove reported a binding")
	}
	if _, err := f.b.ResolveContext(ctx, f.provider, "pc-1"); !errors.Is(err, callback.ErrUnknownCall) {
		t.Errorf("Resolve after Remove = %v, want ErrUnknownCall", err)
	}
}

func TestPostgres_ConflictsAndProviderIsolation(t *testing.T) {
	f := fixture(t)
	ctx := itCtx(t)
	if err := f.b.BindCall(ctx, callback.Binding{Provider: f.provider, ProviderCallID: "same", Call: f.call1}); err != nil {
		t.Fatal(err)
	}
	if err := f.b.BindCall(ctx, callback.Binding{Provider: f.provider, ProviderCallID: "same", Call: f.call2}); !errors.Is(err, callback.ErrBindingConflict) {
		t.Errorf("rebind carrier call = %v, want conflict", err)
	}
	if err := f.b.BindCall(ctx, callback.Binding{Provider: f.provider, ProviderCallID: "other", Call: f.call1}); !errors.Is(err, callback.ErrBindingConflict) {
		t.Errorf("rebind platform call = %v, want conflict", err)
	}
	if _, err := f.b.ResolveContext(ctx, f.other, "same"); !errors.Is(err, callback.ErrUnknownCall) {
		t.Errorf("cross-provider Resolve = %v, want ErrUnknownCall", err)
	}
	if err := f.b.BindCall(ctx, callback.Binding{Provider: f.other, ProviderCallID: "same", Call: f.call3}); err != nil {
		t.Errorf("bind at the other provider: %v", err)
	}
	if id, _ := f.b.ResolveContext(ctx, f.provider, "same"); id != f.call1 {
		t.Errorf("original binding changed to %s", id)
	}
}

// TestPostgres_ConcurrentBindsAcrossReplicas: two pools stand in for two
// replicas. Many binds of one carrier call to two platform calls race; exactly
// one platform call wins, everywhere.
func TestPostgres_ConcurrentBindsAcrossReplicas(t *testing.T) {
	f := fixture(t)
	replica2 := f.store(t, f.env.pool(t), 10*time.Second)
	ctx := itCtx(t)

	const n = 32
	var wg sync.WaitGroup
	won := make(chan telephony.CallID, n)
	for i := 0; i < n; i++ {
		store, target := f.b, f.call1
		if i%2 == 1 {
			store, target = replica2, f.call2
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := store.BindCall(ctx, callback.Binding{Provider: f.provider, ProviderCallID: "contested", Call: target})
			switch {
			case err == nil:
				won <- target
			case errors.Is(err, callback.ErrBindingConflict):
			default:
				t.Errorf("BindCall: %v", err)
			}
		}()
	}
	wg.Wait()
	close(won)
	winners := map[telephony.CallID]int{}
	for id := range won {
		winners[id]++
	}
	if len(winners) != 1 {
		t.Fatalf("winners = %v, want exactly one platform call", winners)
	}
	for id := range winners {
		for name, s := range map[string]*Bindings{"replica 1": f.b, "replica 2": replica2} {
			if got, err := s.ResolveContext(ctx, f.provider, "contested"); err != nil || got != id {
				t.Errorf("%s resolves %s, %v; want %s", name, got, err, id)
			}
		}
	}
}

// waitForLockWaiter polls until a session is waiting on a lock, bounded.
func waitForLockWaiter(t *testing.T, p *pgxpool.Pool) {
	t.Helper()
	ctx := itCtx(t)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := p.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity
			 WHERE wait_event_type = 'Lock' AND datname = current_database() AND pid <> pg_backend_pid()`).Scan(&n); err != nil {
			t.Fatal(redact(err))
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no session ever waited on the lock")
}

// TestPostgres_InProgressConflictingTransactionIsRespected: a bind that meets
// an UNCOMMITTED conflicting insert waits for it, then reports the committed
// truth — conflict if it committed, success if it rolled back. This is the
// behaviour the two-statement design in BindCall exists for.
func TestPostgres_InProgressConflictingTransactionIsRespected(t *testing.T) {
	for _, commit := range []bool{true, false} {
		name := "rollback"
		if commit {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			f := fixture(t)
			ctx := itCtx(t)
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(redact(err))
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, sqlBind, string(f.provider), "pc-race", string(f.call2)); err != nil {
				t.Fatal(redact(err))
			}

			done := make(chan error, 1)
			go func() {
				done <- f.b.BindCall(ctx, callback.Binding{Provider: f.provider, ProviderCallID: "pc-race", Call: f.call1})
			}()
			waitForLockWaiter(t, f.pool)

			if commit {
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(redact(err))
				}
			} else if err := tx.Rollback(ctx); err != nil {
				t.Fatal(redact(err))
			}

			select {
			case err := <-done:
				switch {
				case commit && !errors.Is(err, callback.ErrBindingConflict):
					t.Errorf("after the other transaction committed: %v, want conflict", err)
				case !commit && err != nil:
					t.Errorf("after the other transaction rolled back: %v, want success", err)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("BindCall never returned")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Failures, cancellation, timeouts
// ---------------------------------------------------------------------------

func TestPostgres_DatabaseErrorsAreUnavailableNeverMissing(t *testing.T) {
	f := fixture(t)
	ctx := itCtx(t)
	// Break the store inside this test's own schema only.
	if _, err := f.pool.Exec(ctx, `DROP TABLE telephony_carrier_call_bindings`); err != nil {
		t.Fatal(redact(err))
	}
	_, err := f.b.ResolveContext(ctx, f.provider, "pc-1")
	if !errors.Is(err, ErrStoreUnavailable) || errors.Is(err, callback.ErrUnknownCall) {
		t.Errorf("Resolve on a missing table = %v; want ErrStoreUnavailable and NOT ErrUnknownCall", err)
	}
	if !strings.Contains(err.Error(), "42P01") {
		t.Errorf("error lacks the SQLSTATE: %v", err)
	}
	if err := f.b.BindCall(ctx, callback.Binding{Provider: f.provider, ProviderCallID: "pc-1", Call: f.call1}); !errors.Is(err, ErrStoreUnavailable) {
		t.Errorf("BindCall on a missing table = %v, want ErrStoreUnavailable", err)
	}
	if strings.Contains(err.Error(), f.env.dsn) {
		t.Error("an error disclosed the DSN")
	}
}

func TestPostgres_ConstraintsHoldWithoutTheGoValidation(t *testing.T) {
	f := fixture(t)
	ctx := itCtx(t)
	for name, args := range map[string][]any{
		"bad provider":        {"Bad Provider", "pc-1", string(f.call1)},
		"bad carrier call ID": {string(f.provider), "pc 1", string(f.call1)},
		"bad platform call":   {string(f.provider), "pc-1", "not-a-call"},
	} {
		_, err := f.pool.Exec(ctx, sqlBind, args...)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != sqlStateCheckViolation {
			t.Errorf("%s: err = %v, want a check violation", name, redact(err))
		}
	}
}

func TestPostgres_CancellationIsImmediate(t *testing.T) {
	f := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := f.b.ResolveContext(ctx, f.provider, "pc-1")
	if !errors.Is(err, ErrStoreUnavailable) || !errors.Is(err, context.Canceled) {
		t.Errorf("Resolve with a cancelled context = %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %s", d)
	}
}

// TestPostgres_OpTimeoutBoundsABlockedStatement: the table is locked by
// another transaction; a bind with a short OpTimeout gives up on time.
func TestPostgres_OpTimeoutBoundsABlockedStatement(t *testing.T) {
	f := fixture(t)
	ctx := itCtx(t)
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `LOCK TABLE telephony_carrier_call_bindings IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(redact(err))
	}

	short := f.store(t, f.pool, 300*time.Millisecond)
	start := time.Now()
	err = short.BindCall(context.Background(), callback.Binding{Provider: f.provider, ProviderCallID: "pc-1", Call: f.call1})
	if !errors.Is(err, ErrStoreUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("BindCall against a locked table = %v; want ErrStoreUnavailable wrapping DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the timeout did not bound the statement: %s", d)
	}
}
