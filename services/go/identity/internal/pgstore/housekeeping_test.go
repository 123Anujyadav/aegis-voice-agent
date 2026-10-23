package pgstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Housekeeping against real PostgreSQL. Gated like the rest of this package.

var hkNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

const (
	hkMargin = time.Hour
	hkWindow = 15 * time.Minute
)

func housekeeper(t *testing.T, pool *pgxpool.Pool, now time.Time, batch int) *Housekeeper {
	t.Helper()
	h, err := NewHousekeeper(pool, HousekeepingConfig{
		BatchSize: batch, Margin: hkMargin, RateWindow: hkWindow, IterationTimeout: 10 * time.Second,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func mustExec(t *testing.T, p *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := p.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func ids(t *testing.T, p *pgxpool.Pool, sql string) []string {
	t.Helper()
	rows, err := p.Query(ctx, sql)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func eq(a, b []string) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

func addChallenge(t *testing.T, p *pgxpool.Pool, id string, expires time.Time) {
	mustExec(t, p, `INSERT INTO identity_otp_challenges (request_id, msisdn_pseudonym, subscriber_id, code_hash, expires_at) VALUES ($1, $2, 'usr_x', 'h', $3)`, id, pk("+14155552671"), expires)
}

func addRateState(t *testing.T, p *pgxpool.Pool, number string, lockedUntil *time.Time) {
	mustExec(t, p, `INSERT INTO identity_otp_rate_state (msisdn_pseudonym, locked_until) VALUES ($1, $2)`, pk(number), lockedUntil)
}

func addSession(t *testing.T, p *pgxpool.Pool, id string, expires time.Time, revoked bool) {
	mustExec(t, p, `INSERT INTO identity_sessions (session_id, subscriber_id, refresh_token_hash, created_at, expires_at, is_revoked)
		VALUES ($1, 'u', $2, $3, $4, $5)`, id, th(id), expires.Add(-24*time.Hour), expires, revoked)
}

func ptr(t time.Time) *time.Time { return &t }

// pks is the sorted pseudonyms of the given numbers, as ids() returns them.
func pks(raws ...string) []string {
	out := make([]string, len(raws))
	for i, r := range raws {
		out[i] = pk(r)
	}
	sort.Strings(out)
	return out
}

// Every table's boundary, exactly: what is kept and what goes.
func TestHousekeeping_RetentionRules(t *testing.T) {
	env := requirePostgres(t)
	p := env.poolA
	cutoff := hkNow.Add(-hkMargin)
	rateCutoff := cutoff.Add(-hkWindow)

	addChallenge(t, p, "live", hkNow.Add(5*time.Minute))
	addChallenge(t, p, "expired_within_margin", cutoff.Add(time.Second))
	addChallenge(t, p, "expired_at_cutoff", cutoff) // strict <: kept
	addChallenge(t, p, "expired_beyond", cutoff.Add(-time.Second))

	addRateState(t, p, "+10000000001", nil)                            // has a counted request: kept
	addRateState(t, p, "+10000000002", nil)                            // idle, never locked: deleted
	addRateState(t, p, "+10000000003", ptr(hkNow.Add(10*time.Minute))) // lockout in force: kept
	addRateState(t, p, "+10000000004", ptr(cutoff.Add(time.Second)))   // lock ended within margin: kept
	addRateState(t, p, "+10000000005", ptr(cutoff.Add(-time.Second)))  // lock long over: deleted
	addRateState(t, p, "+10000000006", nil)                            // only stale children: deleted with them
	addRateState(t, p, "+10000000007", nil)                            // a failure still in the window: kept
	mustExec(t, p, `INSERT INTO identity_otp_requests (msisdn_pseudonym, requested_at) VALUES ($1, $2)`, pk("+10000000001"), rateCutoff.Add(time.Second))
	mustExec(t, p, `INSERT INTO identity_otp_requests (msisdn_pseudonym, requested_at) VALUES ($1, $2)`, pk("+10000000006"), rateCutoff.Add(-time.Second))
	mustExec(t, p, `INSERT INTO identity_otp_failures (reservation_id, msisdn_pseudonym, failed_at) VALUES (1, $1, $2)`, pk("+10000000006"), rateCutoff.Add(-time.Second))
	mustExec(t, p, `INSERT INTO identity_otp_failures (reservation_id, msisdn_pseudonym, failed_at) VALUES (2, $1, $2)`, pk("+10000000007"), rateCutoff.Add(time.Second))

	addSession(t, p, "live", hkNow.Add(time.Hour), false)
	addSession(t, p, "revoked_unexpired", hkNow.Add(time.Hour), true)
	addSession(t, p, "expired_within_margin", cutoff.Add(time.Second), false)
	addSession(t, p, "expired_beyond", cutoff.Add(-time.Second), false)
	addSession(t, p, "revoked_expired_beyond", cutoff.Add(-time.Second), true)

	rs := NewReplayStore(p)
	for jti, exp := range map[string]time.Time{
		"valid": hkNow.Add(time.Minute), "within_margin": cutoff.Add(time.Second), "beyond": cutoff.Add(-time.Second),
	} {
		if err := rs.Reserve(ctx, "jkt_h", jti, exp, exp.Add(-2*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	res, err := housekeeper(t, env.poolB, hkNow, 100).RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	want := HousekeepingResult{Challenges: 1, Requests: 1, Failures: 1, RateStates: 3, Sessions: 2, Proofs: 1}
	if res != want {
		t.Errorf("result = %+v\n   want %+v", res, want)
	}
	for sql, want := range map[string][]string{
		`SELECT request_id FROM identity_otp_challenges`:       {"expired_at_cutoff", "expired_within_margin", "live"},
		`SELECT msisdn_pseudonym FROM identity_otp_rate_state`: pks("+10000000001", "+10000000003", "+10000000004", "+10000000007"),
		`SELECT msisdn_pseudonym FROM identity_otp_requests`:   pks("+10000000001"),
		`SELECT msisdn_pseudonym FROM identity_otp_failures`:   pks("+10000000007"),
		`SELECT session_id FROM identity_sessions`:             {"expired_within_margin", "live", "revoked_unexpired"},
		`SELECT jti FROM identity_dpop_replay`:                 {"valid", "within_margin"},
	} {
		if got := ids(t, p, sql); !eq(got, want) {
			t.Errorf("%s\n  kept %v\n  want %v", sql, got, want)
		}
	}
	// Repeatable: nothing further is eligible.
	if again, err := housekeeper(t, env.poolA, hkNow, 100).RunOnce(ctx); err != nil || again.Total() != 0 {
		t.Errorf("second run: (%+v, %v), want nothing", again, err)
	}
}

// After housekeeping, every security decision is what it was: counted
// requests still limit, a lockout still locks, a live session still refreshes
// and a revoked one is still refused.
func TestHousekeeping_SecurityDecisionsAreUnchanged(t *testing.T) {
	env := requirePostgres(t)
	repo := NewRepository(env.poolA)
	pol := app.OtpRatePolicy{MaxRequests: 3, MaxFailures: 5, Window: hkWindow, Lockout: 15 * time.Minute}
	m, l := pk("+14155550101"), pk("+14155550102")
	for i := 0; i < 3; i++ {
		if err := repo.ReserveOtpRequest(ctx, m, hkNow.Add(-14*time.Minute), pol); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		if _, err := repo.ReserveOtpVerification(ctx, l, hkNow.Add(-time.Minute), pol); err != nil {
			t.Fatal(err)
		}
	}
	// Housekeeping with the smallest margin the config allows, at a later time.
	h, err := NewHousekeeper(env.poolB, HousekeepingConfig{BatchSize: 100, Margin: 5 * time.Minute, RateWindow: hkWindow, IterationTimeout: 10 * time.Second},
		func() time.Time { return hkNow })
	if err != nil {
		t.Fatal(err)
	}
	if _, runOnceErr := h.RunOnce(ctx); runOnceErr != nil {
		t.Fatal(runOnceErr)
	}
	wantRetryAt(t, repo.ReserveOtpRequest(ctx, m, hkNow, pol), hkNow.Add(time.Minute))
	_, err = repo.ReserveOtpVerification(ctx, l, hkNow, pol)
	wantRetryAt(t, err, hkNow.Add(14*time.Minute))
}

// A proof reservation blocks replays for its whole life and is removed only
// once it has expired by more than the margin.
func TestHousekeeping_ReplayRecordsOutliveTheAcceptanceWindow(t *testing.T) {
	env := requirePostgres(t)
	rs := NewReplayStore(env.poolA)
	const maxAge = 2 * time.Minute
	iat := hkNow
	exp := iat.Add(maxAge + time.Second) // what dpop.Verifier reserves
	if err := rs.Reserve(ctx, "jkt_w", "jw", exp, iat); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{iat, iat.Add(maxAge), exp, exp.Add(hkMargin)} {
		if _, err := housekeeper(t, env.poolB, at, 100).RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if err := rs.Reserve(ctx, "jkt_w", "jw", exp, iat.Add(time.Second)); !errors.Is(err, dpop.ErrReplayed) {
			t.Fatalf("housekeeping at iat%+v removed a reservation still needed: %v", at.Sub(iat), err)
		}
	}
	if res, err := housekeeper(t, env.poolB, exp.Add(hkMargin+time.Second), 100).RunOnce(ctx); err != nil || res.Proofs != 1 {
		t.Fatalf("after expiry plus margin: (%+v, %v), want the reservation pruned", res, err)
	}
}

// No table loses more than BatchSize rows to one iteration.
func TestHousekeeping_IterationsAreBounded(t *testing.T) {
	env := requirePostgres(t)
	old := hkNow.Add(-48 * time.Hour)
	for i := 0; i < 13; i++ {
		addChallenge(t, env.poolA, fmt.Sprintf("c%02d", i), old)
		addSession(t, env.poolA, fmt.Sprintf("s%02d", i), old, i%2 == 0)
		if err := NewReplayStore(env.poolA).Reserve(ctx, "jkt_b", fmt.Sprintf("j%02d", i), old, old.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	h := housekeeper(t, env.poolB, hkNow, 5)
	for i, want := range []int64{5, 5, 3, 0} {
		res, err := h.RunOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if res.Challenges != want || res.Sessions != want || res.Proofs != want {
			t.Errorf("iteration %d: %+v, want %d per table", i, res, want)
		}
	}
}

// While another replica holds the lock, an iteration skips and deletes
// nothing; once released, the next one works.
func TestHousekeeping_LockContentionSkips(t *testing.T) {
	env := requirePostgres(t)
	addChallenge(t, env.poolA, "old", hkNow.Add(-48*time.Hour))
	tx := holdHousekeepingLock(t, env.poolB)
	res, err := housekeeper(t, env.poolA, hkNow, 100).RunOnce(ctx)
	if err != nil || !res.Skipped || res.Total() != 0 {
		t.Errorf("under contention: (%+v, %v), want skipped", res, err)
	}
	if got := ids(t, env.poolA, `SELECT request_id FROM identity_otp_challenges`); len(got) != 1 {
		t.Error("a skipped iteration deleted rows")
	}
	_ = tx.Rollback(ctx) // releases the transaction-scoped lock
	if res, err := housekeeper(t, env.poolA, hkNow, 100).RunOnce(ctx); err != nil || res.Skipped || res.Challenges != 1 {
		t.Errorf("after release: (%+v, %v)", res, err)
	}
}

// holdHousekeepingLock begins a transaction on pool holding the lock an
// iteration on pool's schema takes — as an iteration in progress there would —
// and returns it; rolling it back releases the lock.
func holdHousekeepingLock(t *testing.T, pool *pgxpool.Pool) interface{ Rollback(context.Context) error } {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(`+housekeepingLockArgs+`)`, housekeepingLockClass); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
	return tx
}

// The lock excludes runners cleaning the SAME tables, and only them: an
// iteration in progress on one schema neither skips nor delays housekeeping of
// an independent schema in the same database, while a second runner on its own
// schema is still excluded and deletes nothing.
func TestHousekeeping_LockIsScopedToTheTablesItCleans(t *testing.T) {
	a, b := requirePostgres(t), requirePostgres(t)
	old := hkNow.Add(-48 * time.Hour)
	addChallenge(t, a.poolA, "a-old", old)
	addChallenge(t, b.poolA, "b-old", old)

	tx := holdHousekeepingLock(t, b.poolB) // an iteration in progress on schema B

	res, err := housekeeper(t, a.poolA, hkNow, 100).RunOnce(ctx)
	if err != nil || res.Skipped || res.Challenges != 1 {
		t.Errorf("independent schema A: (%+v, %v); want its row deleted, not skipped", res, err)
	}
	res, err = housekeeper(t, b.poolA, hkNow, 100).RunOnce(ctx)
	if err != nil || !res.Skipped || res.Total() != 0 {
		t.Errorf("second runner on schema B: (%+v, %v); want skipped", res, err)
	}
	if got := ids(t, b.poolA, `SELECT request_id FROM identity_otp_challenges`); !eq(got, []string{"b-old"}) {
		t.Errorf("a skipped iteration changed schema B: %v", got)
	}
	_ = tx.Rollback(ctx)
	if res, err := housekeeper(t, b.poolA, hkNow, 100).RunOnce(ctx); err != nil || res.Skipped || res.Challenges != 1 {
		t.Errorf("schema B after release: (%+v, %v)", res, err)
	}
}

// Runners reach the same tables however their search_path is spelled, and
// still exclude each other: the lock names the tables, not the setting.
func TestHousekeeping_SameTablesUnderAnotherSearchPathStillContend(t *testing.T) {
	env := requirePostgres(t)
	addChallenge(t, env.poolA, "old", hkNow.Add(-48*time.Hour))
	cfg, err := pgxpool.ParseConfig(strings.TrimSpace(os.Getenv(dsnEnv)))
	if err != nil {
		t.Fatal("parsing DSN (value not shown)")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = `"` + env.schema + `", public`
	other, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	tx := holdHousekeepingLock(t, env.poolB)
	res, err := housekeeper(t, other, hkNow, 100).RunOnce(ctx)
	if err != nil || !res.Skipped || res.Total() != 0 {
		t.Errorf("same tables, other search_path: (%+v, %v); want skipped", res, err)
	}
	_ = tx.Rollback(ctx)
	if res, err := housekeeper(t, other, hkNow, 100).RunOnce(ctx); err != nil || res.Skipped || res.Challenges != 1 {
		t.Errorf("after release: (%+v, %v)", res, err)
	}
}

// The lock names the cleaned tables, so without them an iteration is an
// error — observable, rolled back, zero result — not a skip and not a success.
func TestHousekeeping_MissingTablesAreAnErrorNotASkip(t *testing.T) {
	env := requirePostgres(t)
	mustExec(t, env.poolA, `DROP TABLE identity_sessions CASCADE`)
	res, err := housekeeper(t, env.poolB, hkNow, 100).RunOnce(ctx)
	if err == nil || res.Skipped || res != (HousekeepingResult{}) {
		t.Fatalf("(%+v, %v); want an error", res, err)
	}
}

// Two replicas running iterations concurrently delete each eligible row once,
// with no errors and no double counting.
func TestHousekeeping_ConcurrentReplicas(t *testing.T) {
	env := requirePostgres(t)
	old := hkNow.Add(-48 * time.Hour)
	const n = 120
	for i := 0; i < n; i++ {
		addChallenge(t, env.poolA, fmt.Sprintf("c%03d", i), old)
	}
	hs := []*Housekeeper{housekeeper(t, env.poolA, hkNow, 7), housekeeper(t, env.poolB, hkNow, 7)}
	var mu sync.Mutex
	var total, skipped int64
	var wg sync.WaitGroup
	for r := 0; r < 2; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				res, err := hs[r].RunOnce(ctx)
				mu.Lock()
				if err != nil {
					t.Errorf("replica %d: %v", r, err)
				}
				total += res.Challenges
				if res.Skipped {
					skipped++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	concurrent := total
	if concurrent == 0 || concurrent > n {
		t.Fatalf("the concurrent phase deleted %d of %d", concurrent, n)
	}
	// Contention may skip any number of the 80 iterations (under -race most
	// are), so the concurrent phase need not finish the work. A bounded
	// sequential drain does; the exactly-once total then covers both phases.
	for i := 0; i < n; i++ {
		res, err := hs[i%2].RunOnce(ctx)
		if err != nil {
			t.Fatalf("drain: %v", err)
		}
		total += res.Challenges
		if !res.Skipped && res.Challenges == 0 {
			break
		}
	}
	if total != n {
		t.Errorf("deleted %d, want each of %d exactly once", total, n)
	}
	if got := ids(t, env.poolA, `SELECT request_id FROM identity_otp_challenges`); len(got) != 0 {
		t.Errorf("%d rows left", len(got))
	}
	t.Logf("%d of 80 concurrent iterations skipped on contention; %d rows deleted concurrently", skipped, concurrent)
}

// A failing step rolls back the whole iteration; the next one, once the cause
// is gone, completes the work. Timeouts and cancellation behave the same way.
func TestHousekeeping_FailedIterationsLeaveNothingAndRetry(t *testing.T) {
	env := requirePostgres(t)
	old := hkNow.Add(-48 * time.Hour)
	addChallenge(t, env.poolA, "old", old)
	addSession(t, env.poolA, "old", old, false)

	mustExec(t, env.poolA, `CREATE FUNCTION test_block_delete() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected failure'; END; $$;
		CREATE TRIGGER test_block_delete BEFORE DELETE ON identity_sessions FOR EACH ROW EXECUTE FUNCTION test_block_delete();`)
	if _, err := housekeeper(t, env.poolB, hkNow, 100).RunOnce(ctx); err == nil {
		t.Fatal("the injected failure did not surface")
	}
	if got := ids(t, env.poolA, `SELECT request_id FROM identity_otp_challenges`); len(got) != 1 {
		t.Error("a failed iteration kept its earlier deletes (challenges): not all-or-nothing")
	}

	t.Run("timeout", func(t *testing.T) {
		mustExec(t, env.poolA, `DROP TRIGGER test_block_delete ON identity_sessions;
			CREATE OR REPLACE FUNCTION test_block_delete() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN PERFORM pg_sleep(5); RETURN OLD; END; $$;
			CREATE TRIGGER test_block_delete BEFORE DELETE ON identity_sessions FOR EACH ROW EXECUTE FUNCTION test_block_delete();`)
		h, _ := NewHousekeeper(env.poolB, HousekeepingConfig{BatchSize: 100, Margin: hkMargin, RateWindow: hkWindow, IterationTimeout: 300 * time.Millisecond},
			func() time.Time { return hkNow })
		start := time.Now()
		if _, err := h.RunOnce(ctx); err == nil {
			t.Fatal("a slow iteration was not cut off")
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("the iteration ran %v past a 300ms timeout", d)
		}
		if got := ids(t, env.poolA, `SELECT request_id FROM identity_otp_challenges`); len(got) != 1 {
			t.Error("a timed-out iteration kept partial deletes")
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		c, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := housekeeper(t, env.poolB, hkNow, 100).RunOnce(c); err == nil {
			t.Error("a cancelled iteration reported success")
		}
	})

	mustExec(t, env.poolA, `DROP TRIGGER test_block_delete ON identity_sessions`)
	res, err := housekeeper(t, env.poolB, hkNow, 100).RunOnce(ctx)
	if err != nil || res.Challenges != 1 || res.Sessions != 1 {
		t.Errorf("retry: (%+v, %v), want the work done", res, err)
	}
}

// The database-side bound exists independently of the client's context: the
// iteration's own transaction runs under a statement timeout equal to the
// iteration timeout, so the server stops the work even if the client stalls.
func TestHousekeeping_StatementTimeoutIsSetInTheIteration(t *testing.T) {
	env := requirePostgres(t)
	addChallenge(t, env.poolA, "old", hkNow.Add(-48*time.Hour))
	mustExec(t, env.poolA, `CREATE TABLE test_seen (setting text);
		CREATE FUNCTION test_record_timeout() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN INSERT INTO test_seen VALUES (current_setting('statement_timeout')); RETURN OLD; END; $$;
		CREATE TRIGGER test_record_timeout BEFORE DELETE ON identity_otp_challenges
			FOR EACH ROW EXECUTE FUNCTION test_record_timeout();`)
	h, err := NewHousekeeper(env.poolB, HousekeepingConfig{BatchSize: 100, Margin: hkMargin, RateWindow: hkWindow, IterationTimeout: 7 * time.Second},
		func() time.Time { return hkNow })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ids(t, env.poolA, `SELECT setting FROM test_seen`); !eq(got, []string{"7s"}) {
		t.Errorf("statement_timeout inside the iteration = %v, want [7s]", got)
	}
	// SET LOCAL: the setting does not leak to the pooled connection afterwards.
	var after string
	if err := env.poolB.QueryRow(ctx, `SELECT current_setting('statement_timeout')`).Scan(&after); err != nil || after != "0" {
		t.Errorf("statement_timeout after the iteration = %q (%v), want the default 0", after, err)
	}
}

// Reservations racing housekeeping that deletes their numbers' idle state:
// every reservation either succeeds or is rate-limited, never fails.
func TestHousekeeping_RateStateDeletionRacesReservationsSafely(t *testing.T) {
	env := requirePostgres(t)
	repo := NewRepository(env.poolA)
	pol := app.OtpRatePolicy{MaxRequests: 3, MaxFailures: 5, Window: hkWindow, Lockout: 15 * time.Minute}
	h := housekeeper(t, env.poolB, hkNow, 50)
	stale := hkNow.Add(-3 * time.Hour) // every row these create is already eligible
	var hkWG, workers sync.WaitGroup
	var mu sync.Mutex
	var reserveErrs, hkErrs []error
	stop := make(chan struct{})
	hkWG.Add(1)
	go func() {
		defer hkWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := h.RunOnce(ctx); err != nil {
				mu.Lock()
				hkErrs = append(hkErrs, err)
				mu.Unlock()
			}
		}
	}()
	for w := 0; w < 4; w++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 60; i++ {
				m := pk(fmt.Sprintf("+1415555%04d", (w*60+i)%20))
				err := repo.ReserveOtpRequest(ctx, m, stale, pol)
				if err == nil || errors.Is(err, app.ErrRateLimited) {
					_, err = repo.ReserveOtpVerification(ctx, m, stale, pol)
				}
				if err != nil && !errors.Is(err, app.ErrRateLimited) {
					mu.Lock()
					reserveErrs = append(reserveErrs, err)
					mu.Unlock()
				}
			}
		}()
	}
	workers.Wait()
	close(stop)
	hkWG.Wait()
	if len(reserveErrs) != 0 {
		t.Errorf("%d reservations failed while housekeeping ran; first: %v", len(reserveErrs), reserveErrs[0])
	}
	// The only acceptable housekeeping failure here is the documented foreign-key
	// backstop (23503): a child row committed after the iteration's snapshot.
	// The iteration rolls back and the next one retries.
	for _, err := range hkErrs {
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "23503" {
			t.Errorf("housekeeping failed for a reason other than the FK backstop: %v", err)
		}
	}
	t.Logf("housekeeping iterations rolled back by the FK backstop: %d", len(hkErrs))
}
