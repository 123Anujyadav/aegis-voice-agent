package pgstore

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

	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/packages/go/pgshared"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Real-PostgreSQL tests. They run only when BOTH are set:
//
//	AEGIS_IDENTITY_PG_DSN=<dsn of a disposable database>
//	AEGIS_IDENTITY_PG_DISPOSABLE=1
//
// Without the DSN they SKIP — a skip, not a pass. With a DSN but without the
// confirmation they FAIL, because pointing them at a database nobody has
// declared disposable is the mistake to catch. Each test creates its own
// schema (identity_test_<random>), migrates it, uses it through two
// independent pools, and drops that schema — and only that schema — after.
const (
	dsnEnv        = "AEGIS_IDENTITY_PG_DSN"
	disposableEnv = "AEGIS_IDENTITY_PG_DISPOSABLE"
)

type pgEnv struct {
	schema     string
	poolA      *pgxpool.Pool
	poolB      *pgxpool.Pool
	openSchema func(t *testing.T) *pgxpool.Pool
}

func requirePostgres(t *testing.T) *pgEnv {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(dsnEnv))
	if dsn == "" {
		t.Skipf("%s is not set: real-PostgreSQL test SKIPPED (not passed)", dsnEnv)
	}
	if os.Getenv(disposableEnv) != "1" {
		t.Fatalf("%s is set but %s is not 1: refusing to touch a database not declared disposable", dsnEnv, disposableEnv)
	}
	c := context.Background()
	// The harness's own first connection is retried, briefly and visibly. Under
	// heavy repetition on Docker Desktop for Windows, a connect occasionally
	// fails client-side with nothing logged by the server (observed once in 30
	// back-to-back suite runs). Only this setup step retries; no assertion does.
	var admin *pgxpool.Pool
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if admin, err = OpenPool(c, dsn, pgshared.AllowPlaintextForDevelopment); err == nil {
			break
		}
		t.Logf("test harness connect attempt %d failed: %v", attempt, err)
		time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "identity_test_" + hex.EncodeToString(b)
	if _, err := admin.Exec(c, `CREATE SCHEMA `+schema); err != nil {
		admin.Close()
		t.Fatalf("creating test schema: %v", err)
	}
	env := &pgEnv{schema: schema}
	var pools []*pgxpool.Pool
	env.openSchema = func(t *testing.T) *pgxpool.Pool {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal("parsing DSN")
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
		p, err := pgxpool.NewWithConfig(c, cfg)
		if err != nil {
			t.Fatal(err)
		}
		pools = append(pools, p)
		return p
	}
	t.Cleanup(func() {
		for _, p := range pools {
			p.Close()
		}
		if _, err := admin.Exec(c, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("dropping test schema %s: %v", schema, err)
		}
		admin.Close()
	})
	env.poolA, env.poolB = env.openSchema(t), env.openSchema(t)
	if _, err := Migrate(c, env.poolA); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	return env
}

func postgresBackend(t *testing.T) backend {
	env := requirePostgres(t)
	return backend{
		a: NewRepository(env.poolA), b: NewRepository(env.poolB),
		ra: NewReplayStore(env.poolA), rb: NewReplayStore(env.poolB),
	}
}

// TestContract_Postgres is the whole contract against PostgreSQL, through two
// independent pools: every concurrent case is a cross-instance case.
func TestContract_Postgres(t *testing.T) {
	if strings.TrimSpace(os.Getenv(dsnEnv)) == "" {
		t.Skipf("%s is not set: real-PostgreSQL contract SKIPPED (not passed)", dsnEnv)
	}
	runContract(t, postgresBackend)
}

// Sessions written by one pool survive that pool's closure and are read by a
// pool opened afterwards: state outlives the process that wrote it.
func TestPostgres_StateSurvivesAClosedInstance(t *testing.T) {
	env := requirePostgres(t)
	writer := env.openSchema(t)
	s := session("durable")
	s.DeviceKeyThumbprint = "jkt_D"
	if err := NewRepository(writer).SaveSession(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := NewRepository(writer).RevokeSession(ctx, "durable"); err != nil {
		t.Fatal(err)
	}
	if err := NewReplayStore(writer).Reserve(ctx, "jkt_D", "jd", t0.Add(time.Hour), t0); err != nil {
		t.Fatal(err)
	}
	writer.Close()

	reader := env.openSchema(t)
	got, err := NewRepository(reader).GetSession(ctx, "durable")
	if err != nil || !got.IsRevoked || got.DeviceKeyThumbprint != "jkt_D" || !got.ExpiresAt.Equal(s.ExpiresAt) {
		t.Errorf("after the writer closed: (%+v, %v)", got, err)
	}
	if err := NewReplayStore(reader).Reserve(ctx, "jkt_D", "jd", t0.Add(time.Hour), t0); !errors.Is(err, dpop.ErrReplayed) {
		t.Errorf("a reservation did not survive: err = %v", err)
	}
}

// A store that cannot answer returns an error — never a success, and never
// ErrReplayed — so the verifier refuses the proof as ErrReplayUnavailable.
func TestPostgres_UnavailableStoreFailsClosed(t *testing.T) {
	env := requirePostgres(t)
	dead := env.openSchema(t)
	dead.Close()
	repo, replay := NewRepository(dead), NewReplayStore(dead)
	if err := replay.Reserve(ctx, "jkt", "j", t0.Add(time.Minute), t0); err == nil || errors.Is(err, dpop.ErrReplayed) {
		t.Errorf("Reserve on a closed pool: %v, want a store error", err)
	}
	if ok, err := repo.ConsumeOtp(ctx, "req"); ok || err == nil {
		t.Errorf("ConsumeOtp: (%v, %v)", ok, err)
	}
	if err := repo.ReplaceSession(ctx, session("x"), th("rt_0")); err == nil || errors.Is(err, app.ErrSessionConflict) {
		t.Errorf("ReplaceSession: %v, want a store error", err)
	}
	if _, err := repo.ReserveOtpVerification(ctx, pk("+14155552671"), t0, policy); err == nil || errors.Is(err, app.ErrRateLimited) || errors.Is(err, app.ErrMSISDNNotPseudonymised) {
		t.Errorf("ReserveOtpVerification: %v, want a store error", err)
	}
	if _, err := repo.GetSession(ctx, "x"); err == nil || errors.Is(err, app.ErrInvalidToken) {
		t.Errorf("GetSession: %v, want a store error", err)
	}
}

// A statement failing midway through a transaction leaves none of it behind.
// A test-only trigger, created in this test's schema, fails the lockout UPDATE
// that the fifth reservation makes AFTER inserting its failure row.
func TestPostgres_FailedTransactionLeavesNoPartialState(t *testing.T) {
	env := requirePostgres(t)
	if _, err := env.poolA.Exec(ctx, `
		CREATE FUNCTION test_fail_lockout() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected failure'; END; $$;
		CREATE TRIGGER test_fail_lockout BEFORE UPDATE ON identity_otp_rate_state
			FOR EACH ROW WHEN (NEW.locked_until IS NOT NULL) EXECUTE FUNCTION test_fail_lockout();`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(env.poolA)
	m := pk("+14155550021")
	for i := 0; i < 4; i++ {
		if _, err := repo.ReserveOtpVerification(ctx, m, t0, policy); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.ReserveOtpVerification(ctx, m, t0, policy); err == nil {
		t.Fatal("the injected failure did not surface")
	}
	var n int
	if err := env.poolA.QueryRow(ctx, `SELECT count(*) FROM identity_otp_failures WHERE msisdn_pseudonym = $1`, m).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("%d failure rows, want 4: the failed reservation's insert survived its rollback", n)
	}
}

// The database refuses, whatever the writer, to change an enrolled key or to
// clear a revocation.
func TestPostgres_TriggersBackstopDirectWrites(t *testing.T) {
	env := requirePostgres(t)
	repo := NewRepository(env.poolA)
	s := session("guarded")
	s.DeviceKeyThumbprint = "jkt_G"
	_ = repo.SaveSession(ctx, s)
	_ = repo.RevokeSession(ctx, "guarded")
	for sql, code := range map[string]string{
		`UPDATE identity_sessions SET device_key_thumbprint = 'jkt_H' WHERE session_id = 'guarded'`: sqlstateKeyChange,
		`UPDATE identity_sessions SET device_key_thumbprint = '' WHERE session_id = 'guarded'`:      sqlstateKeyChange,
		`UPDATE identity_sessions SET is_revoked = FALSE WHERE session_id = 'guarded'`:              sqlstateUnrevoke,
	} {
		_, err := env.poolB.Exec(ctx, sql)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != code {
			t.Errorf("%s: err = %v, want SQLSTATE %s", sql, err, code)
		}
	}
}

// The repository's own guards hold without the triggers: with the triggers
// dropped from THIS test's schema, key changes and un-revocations are still
// refused by the SQL the repository issues. Together with the test above, each
// layer is shown to work alone.
func TestPostgres_RepositoryGuardsHoldWithoutTriggers(t *testing.T) {
	env := requirePostgres(t)
	if _, err := env.poolA.Exec(ctx, `DROP TRIGGER identity_sessions_guard ON identity_sessions`); err != nil {
		t.Fatal(err)
	}
	be := backend{a: NewRepository(env.poolA), b: NewRepository(env.poolB)}
	testKeyImmutable(t, be)
	testRevocationSticky(t, be)
}

// Prune is bounded and removes only expired reservations.
func TestPostgres_PruneIsBoundedAndSafe(t *testing.T) {
	env := requirePostgres(t)
	rs := NewReplayStore(env.poolA)
	for i, exp := range []time.Time{t0.Add(-2 * time.Minute), t0.Add(-time.Minute), t0, t0.Add(time.Minute)} {
		if err := rs.Reserve(ctx, "jkt_p", string(rune('a'+i)), exp, t0.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := rs.Prune(ctx, t0, 2); err != nil || n != 2 {
		t.Fatalf("first prune: (%d, %v), want 2", n, err)
	}
	if n, err := rs.Prune(ctx, t0, 10); err != nil || n != 1 {
		t.Fatalf("second prune: (%d, %v), want the one remaining expired row", n, err)
	}
	if err := rs.Reserve(ctx, "jkt_p", "d", t0.Add(2*time.Minute), t0); !errors.Is(err, dpop.ErrReplayed) {
		t.Error("prune removed a live reservation")
	}
}

// Migrations: idempotent, safe under concurrent callers, and refused against a
// schema from a newer build.
func TestPostgres_Migrations(t *testing.T) {
	env := requirePostgres(t)
	res, err := Migrate(ctx, env.poolB)
	if err != nil || len(res.Applied) != 0 || res.Version != 5 || len(res.AlreadyApplied) != 5 {
		t.Fatalf("re-run: (%+v, %v), want nothing applied at v5", res, err)
	}

	fresh := requirePostgres(t) // already migrated once; drop and race instead
	_, _ = fresh.poolA.Exec(ctx, `DROP SCHEMA `+fresh.schema+` CASCADE; CREATE SCHEMA `+fresh.schema)
	pools := make([]*pgxpool.Pool, 4)
	for i := range pools {
		pools[i] = fresh.openSchema(t)
	}
	var wg sync.WaitGroup
	errs := make([]error, len(pools))
	for i := range pools {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = Migrate(ctx, pools[i])
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent migrate %d: %v", i, err)
		}
	}

	if _, err := env.poolA.Exec(ctx, `INSERT INTO identity_schema_migrations (version, name) VALUES (99, 'future')`); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(ctx, env.poolA); !errors.Is(err, ErrSchemaFromTheFuture) {
		t.Errorf("schema from the future: err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Offline unit tests: no database.
// ---------------------------------------------------------------------------

func TestMigrations_ChainIsContiguousAndScoped(t *testing.T) {
	chain, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 5 || chain[0].Version != 1 || chain[1].Version != 2 || chain[2].Version != 3 || chain[3].Version != 4 || chain[4].Version != 5 {
		t.Fatalf("chain has %d steps", len(chain))
	}

	// 0004 renames to pseudonyms, refuses populated OTP/rate tables, adds the
	// canonical CHECKs and drops the session column — and changes no data.
	var code4 []string
	for _, line := range strings.Split(chain[3].SQL, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			code4 = append(code4, line)
		}
	}
	v4 := strings.Join(code4, "\n")
	for _, want := range []string{
		"IN ACCESS EXCLUSIVE MODE", "ERRCODE = 'CSI04'",
		"IF EXISTS (SELECT 1 FROM identity_otp_challenges)", "OR EXISTS (SELECT 1 FROM identity_otp_rate_state)",
		"OR EXISTS (SELECT 1 FROM identity_otp_requests)", "OR EXISTS (SELECT 1 FROM identity_otp_failures)",
		"ALTER TABLE identity_otp_challenges RENAME COLUMN msisdn TO msisdn_pseudonym",
		"ALTER TABLE identity_otp_rate_state RENAME COLUMN msisdn TO msisdn_pseudonym",
		"ALTER TABLE identity_otp_requests RENAME COLUMN msisdn TO msisdn_pseudonym",
		"ALTER TABLE identity_otp_failures RENAME COLUMN msisdn TO msisdn_pseudonym",
		"ALTER TABLE identity_sessions DROP COLUMN msisdn",
	} {
		if !strings.Contains(v4, want) {
			t.Errorf("0004 lacks %q", want)
		}
	}
	if n := strings.Count(v4, "CHECK (msisdn_pseudonym ~ '^[0-9a-f]{64}$')"); n != 4 {
		t.Errorf("0004 has %d canonical-pseudonym CHECKs, want 4", n)
	}
	for _, forbidden := range []string{"DELETE FROM", "UPDATE ", "INSERT ", "TRUNCATE", "DROP TABLE", "CREATE TABLE"} {
		if strings.Contains(strings.ToUpper(v4), forbidden) {
			t.Errorf("0004 contains %q", forbidden)
		}
	}

	// 0003 only adds the two housekeeping indexes.
	var stmts []string
	for _, line := range strings.Split(chain[2].SQL, "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "--") {
			stmts = append(stmts, l)
		}
	}
	if len(stmts) != 2 ||
		stmts[0] != "CREATE INDEX identity_otp_requests_requested_at ON identity_otp_requests (requested_at);" ||
		stmts[1] != "CREATE INDEX identity_otp_failures_failed_at ON identity_otp_failures (failed_at);" {
		t.Errorf("0003 statements = %q", stmts)
	}

	// 0002 changes exactly one thing, on one table, and refuses rather than
	// converts existing rows. Comments are excluded: they document the manual
	// procedure, which this file must not perform.
	var code []string
	for _, line := range strings.Split(chain[1].SQL, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			code = append(code, line)
		}
	}
	v2 := strings.Join(code, "\n")
	for _, want := range []string{
		"LOCK TABLE identity_sessions IN ACCESS EXCLUSIVE MODE",
		"IF EXISTS (SELECT 1 FROM identity_sessions)", "ERRCODE = 'CSI03'",
		"ALTER TABLE identity_sessions RENAME COLUMN refresh_token TO refresh_token_hash",
		"CHECK (refresh_token_hash ~ '^[0-9a-f]{64}$')",
	} {
		if !strings.Contains(v2, want) {
			t.Errorf("0002 lacks %q", want)
		}
	}
	upper2 := strings.ToUpper(v2)
	for _, forbidden := range []string{"DROP ", "TRUNCATE", "DELETE FROM", "UPDATE ", "INSERT ", "CREATE TABLE"} {
		if strings.Contains(upper2, forbidden) {
			t.Errorf("0002 contains %q", forbidden)
		}
	}
	if strings.Count(upper2, "ALTER TABLE") != strings.Count(v2, "ALTER TABLE identity_sessions") {
		t.Error("0002 alters a table other than identity_sessions")
	}

	sql := chain[0].SQL
	for _, table := range []string{
		"identity_otp_challenges", "identity_otp_rate_state", "identity_otp_requests", "identity_otp_failures",
		"identity_sessions", "identity_device_attestations", "identity_guardian_delegations", "identity_dpop_replay",
	} {
		if !strings.Contains(sql, "CREATE TABLE "+table+" (") {
			t.Errorf("migration does not create %s", table)
		}
	}
	upper := strings.ToUpper(sql)
	for _, forbidden := range []string{"DROP ", "TRUNCATE", "DELETE FROM", "IF NOT EXISTS", "ALTER TABLE"} {
		if strings.Contains(upper, forbidden) {
			t.Errorf("migration contains %q", forbidden)
		}
	}
	for _, line := range strings.Split(sql, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "CREATE TABLE ") && !strings.Contains(line, "identity_") {
			t.Errorf("table outside the identity_ namespace: %s", line)
		}
	}
}

// The replay store now lives in packages/go/pgshared and is shared with
// edge-api; the table it assumes must be exactly the one this service's
// migration 0001 creates.
func TestMigrations_ReplayTableMatchesTheSharedStore(t *testing.T) {
	chain, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(chain[0].SQL, pgshared.ReplayTableDDL) {
		t.Error("migration 0001 no longer creates identity_dpop_replay exactly as pgshared.ReplayTableDDL states")
	}
}

func TestOpenPool_InvalidDSNIsNotEchoed(t *testing.T) {
	const dsn = "postgres://user:s3cret-pass@host:notaport/db"
	_, err := OpenPool(context.Background(), dsn, pgshared.RequireVerifiedTLS)
	if err == nil {
		t.Fatal("an unparsable DSN was accepted")
	}
	if strings.Contains(err.Error(), "s3cret-pass") || strings.Contains(err.Error(), "notaport") {
		t.Errorf("the error echoes the DSN: %v", err)
	}
}

func TestRefusal_MatchesTheMemoryRule(t *testing.T) {
	w := 15 * time.Minute
	for name, tc := range map[string]struct {
		st     rateState
		count  int
		oldest time.Time
		want   time.Time
	}{
		"window full only":       {rateState{}, 3, t0, t0.Add(w)},
		"locked only":            {rateState{lockedUntil: t0.Add(20 * time.Minute)}, 1, t0, t0.Add(20 * time.Minute)},
		"lock later than window": {rateState{lockedUntil: t0.Add(20 * time.Minute)}, 3, t0, t0.Add(20 * time.Minute)},
		"window later than lock": {rateState{lockedUntil: t0.Add(5 * time.Minute)}, 3, t0, t0.Add(w)},
		"expired lock, not full": {rateState{lockedUntil: t0.Add(-time.Minute)}, 1, t0, time.Time{}},
	} {
		t.Run(name, func(t *testing.T) {
			var rl *app.RateLimitError
			if !errors.As(refusal(&tc.st, t0.Add(time.Minute), tc.count, 3, tc.oldest, w), &rl) || !rl.RetryAt.Equal(tc.want) {
				t.Errorf("RetryAt = %v, want %v", rl, tc.want)
			}
		})
	}
}
