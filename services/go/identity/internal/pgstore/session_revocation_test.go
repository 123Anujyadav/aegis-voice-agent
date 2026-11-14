package pgstore

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// Revoke-all storage (app.SessionRevocationRepository; migration 0012): the
// contract cases run against both backends (contract_test.go); the rest need
// real PostgreSQL.

func revoker(t *testing.T, r app.IdentityRepository) app.SessionRevocationRepository {
	t.Helper()
	sr, ok := r.(app.SessionRevocationRepository)
	if !ok {
		t.Fatalf("%T does not implement app.SessionRevocationRepository", r)
	}
	return sr
}

// keyedSession is a device-bound session of subscriber, holding token.
func keyedSession(id, subscriber, token string) *app.UserSession {
	return &app.UserSession{
		SessionID: id, SubscriberID: subscriber, RefreshTokenHash: th(token), DeviceID: "dev_" + id,
		Roles: []string{"subscriber"}, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(24 * time.Hour),
		DeviceKeyThumbprint: "jkt_" + id,
	}
}

func revokeAllOf(s *app.UserSession) *app.SessionRevocation {
	return &app.SessionRevocation{
		SessionID: s.SessionID, SubscriberID: s.SubscriberID, RefreshTokenHash: s.RefreshTokenHash,
		DeviceKeyThumbprint: s.DeviceKeyThumbprint, RevokedAt: time.Now(),
	}
}

func isRevoked(t *testing.T, r app.IdentityRepository, id string) bool {
	t.Helper()
	s, err := r.GetSession(ctx, id)
	if err != nil {
		t.Fatalf("session %s: %v", id, err)
	}
	return s.IsRevoked
}

func testRevokeAll(t *testing.T, be backend) {
	const x, y = "usr_ra_x", "usr_ra_y"
	x1, x2, x3 := keyedSession("ra_x1", x, "rt_x1"), keyedSession("ra_x2", x, "rt_x2"), keyedSession("ra_x3", x, "rt_x3")
	y1 := keyedSession("ra_y1", y, "rt_y1")
	expired := keyedSession("ra_x_old", x, "rt_old")
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	for _, s := range []*app.UserSession{x1, x2, x3, y1, expired} {
		if err := be.a.SaveSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := be.a.RevokeSession(ctx, x3.SessionID); err != nil {
		t.Fatal(err)
	}
	ra, rb := revoker(t, be.a), revoker(t, be.b)

	// Refused, changing nothing: a session that is missing, another
	// account's, held under another token or key, or expired.
	for name, op := range map[string]*app.SessionRevocation{
		"missing":                 func() *app.SessionRevocation { o := revokeAllOf(x1); o.SessionID = "ra_missing"; return o }(),
		"another account":         func() *app.SessionRevocation { o := revokeAllOf(x1); o.SubscriberID = y; return o }(),
		"another token":           func() *app.SessionRevocation { o := revokeAllOf(x1); o.RefreshTokenHash = th("rt_other"); return o }(),
		"another key":             func() *app.SessionRevocation { o := revokeAllOf(x1); o.DeviceKeyThumbprint = "jkt_other"; return o }(),
		"expired":                 revokeAllOf(expired),
		"revoked, never recorded": revokeAllOf(x3),
	} {
		if n, repeated, err := ra.RevokeAllSessions(ctx, op); !errors.Is(err, app.ErrSessionConflict) || n != 0 || repeated {
			t.Errorf("%s: (%d, %v, %v), want ErrSessionConflict", name, n, repeated, err)
		}
	}
	if isRevoked(t, be.b, x1.SessionID) || isRevoked(t, be.b, x2.SessionID) || isRevoked(t, be.b, y1.SessionID) || isRevoked(t, be.b, expired.SessionID) {
		t.Fatal("a refused revoke-all revoked a session")
	}

	n, repeated, err := ra.RevokeAllSessions(ctx, revokeAllOf(x1))
	if err != nil || repeated || n != 3 {
		t.Fatalf("revoke-all: (%d, %v, %v), want 3 sessions (x1, x2 and the expired one; x3 was revoked)", n, repeated, err)
	}
	for _, s := range []*app.UserSession{x1, x2, x3, expired} {
		if !isRevoked(t, be.b, s.SessionID) {
			t.Errorf("%s survived", s.SessionID)
		}
	}
	if isRevoked(t, be.b, y1.SessionID) {
		t.Error("another account's session was revoked")
	}

	// A retry, through either handle: done, and nothing more is revoked.
	x4 := keyedSession("ra_x4", x, "rt_x4")
	if err := be.b.SaveSession(ctx, x4); err != nil {
		t.Fatal(err)
	}
	for i, r := range []app.SessionRevocationRepository{rb, ra} {
		if n, repeated, err := r.RevokeAllSessions(ctx, revokeAllOf(x1)); err != nil || !repeated || n != 0 {
			t.Errorf("retry %d: (%d, %v, %v), want repeated", i, n, repeated, err)
		}
	}
	if isRevoked(t, be.a, x4.SessionID) {
		t.Fatal("a retry revoked a session saved after the operation")
	}
	retryOther := revokeAllOf(x1)
	retryOther.RefreshTokenHash = th("rt_other")
	if _, _, err := rb.RevokeAllSessions(ctx, retryOther); !errors.Is(err, app.ErrSessionConflict) {
		t.Errorf("a retry under another token: %v", err)
	}
	if _, _, err := rb.RevokeAllSessions(ctx, revokeAllOf(x2)); !errors.Is(err, app.ErrSessionConflict) {
		t.Errorf("revoke-all from a session another operation revoked: %v", err)
	}

	// A new operation from the live session.
	if n, repeated, err := rb.RevokeAllSessions(ctx, revokeAllOf(x4)); err != nil || repeated || n != 1 {
		t.Errorf("a new operation: (%d, %v, %v), want 1", n, repeated, err)
	}
	if isRevoked(t, be.a, y1.SessionID) {
		t.Error("another account's session was revoked")
	}
}

// Concurrent requests of one operation, across both handles: exactly one
// performs it, the rest are answered as repeats.
func testRevokeAllConcurrent(t *testing.T, be backend) {
	const x = "usr_rc_x"
	asking, other := keyedSession("rc_1", x, "rt_1"), keyedSession("rc_2", x, "rt_2")
	for _, s := range []*app.UserSession{asking, other} {
		if err := be.a.SaveSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	performed, repeats := 0, 0
	race(8, func(i int) {
		n, repeated, err := revoker(t, be.pick(i)).RevokeAllSessions(ctx, revokeAllOf(asking))
		mu.Lock()
		defer mu.Unlock()
		switch {
		case err != nil:
			t.Errorf("request %d: %v", i, err)
		case repeated:
			repeats++
		default:
			performed++
			if n != 2 {
				t.Errorf("revoked %d, want 2", n)
			}
		}
	})
	if performed != 1 || repeats != 7 {
		t.Errorf("%d performed and %d repeats, want 1 and 7", performed, repeats)
	}
}

// Refreshes racing a revoke-all: whichever order the locks fall in, no
// session of the account is live afterwards, and no refresh stored a token
// after the revocation.
func testRevokeAllRacesRefresh(t *testing.T, be backend) {
	const x = "usr_rr_x"
	asking := keyedSession("rr_ask", x, "rt_ask")
	if err := be.a.SaveSession(ctx, asking); err != nil {
		t.Fatal(err)
	}
	const n = 6
	for i := 0; i < n; i++ {
		if err := be.a.SaveSession(ctx, keyedSession(fmt.Sprintf("rr_%d", i), x, "rt_0")); err != nil {
			t.Fatal(err)
		}
	}
	race(n+1, func(i int) {
		if i == n {
			if _, _, err := revoker(t, be.pick(i)).RevokeAllSessions(ctx, revokeAllOf(asking)); err != nil {
				t.Errorf("revoke-all: %v", err)
			}
			return
		}
		next := keyedSession(fmt.Sprintf("rr_%d", i), x, "rt_1")
		if err := be.pick(i).ReplaceSession(ctx, next, th("rt_0")); err != nil && !errors.Is(err, app.ErrSessionConflict) {
			t.Errorf("refresh %d: %v", i, err)
		}
	})
	for i := 0; i < n; i++ {
		if !isRevoked(t, be.b, fmt.Sprintf("rr_%d", i)) {
			t.Errorf("rr_%d is live after the revoke-all", i)
		}
	}
	if !isRevoked(t, be.b, asking.SessionID) {
		t.Error("the asking session is live")
	}
}

// ---------------------------------------------------------------------------
// Real PostgreSQL only.
// ---------------------------------------------------------------------------

// The record holds the session id, the token's keyed hash, the time and the
// count, and nothing else; it goes with its session (cascade), whether the
// session is deleted, erased or housekept.
func TestRevokeAll_Postgres_RecordIsMinimalAndGoesWithItsSession(t *testing.T) {
	env := requirePostgres(t)
	repo := NewRepository(env.poolA)
	sub := usrID('7')
	s1 := keyedSession(sessID('1'), sub, "rt_1")
	s2 := keyedSession(sessID('2'), sub, "rt_2")
	for _, s := range []*app.UserSession{s1, s2} {
		if err := repo.SaveSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if n, _, err := repo.RevokeAllSessions(ctx, revokeAllOf(s1)); err != nil || n != 2 {
		t.Fatalf("revoke-all: (%d, %v)", n, err)
	}
	var row string
	if err := env.poolA.QueryRow(ctx, `SELECT row_to_json(r)::text FROM identity_session_revocations r`).Scan(&row); err != nil {
		t.Fatal(err)
	}
	var cols []string
	rows, err := env.poolA.Query(ctx, `SELECT column_name::text FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'identity_session_revocations' ORDER BY ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	rows.Close()
	if fmt.Sprint(cols) != "[session_id refresh_token_hash revoked_at sessions_revoked]" {
		t.Errorf("columns = %v", cols)
	}
	for _, leak := range []string{sub, "rt_1", "jkt_", "dev_"} {
		if strings.Contains(row, leak) {
			t.Errorf("the record %s holds %q", row, leak)
		}
	}
	if !strings.Contains(row, s1.RefreshTokenHash) || !strings.Contains(row, `"sessions_revoked":2`) {
		t.Errorf("record = %s", row)
	}
	// Not a raw token: the CHECK refuses one.
	if _, err := env.poolA.Exec(ctx, `UPDATE identity_session_revocations SET refresh_token_hash = 'rt_1'`); err == nil {
		t.Error("a non-canonical token hash was stored")
	}

	if _, err := env.poolA.Exec(ctx, `DELETE FROM identity_sessions WHERE session_id = $1`, s1.SessionID); err != nil {
		t.Fatal(err)
	}
	if n := countRevocations(t, env); n != 0 {
		t.Errorf("%d records after their session was deleted, want 0", n)
	}
}

func countRevocations(t *testing.T, env *pgEnv) int {
	t.Helper()
	var n int
	if err := env.poolA.QueryRow(ctx, `SELECT count(*) FROM identity_session_revocations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Erasure and housekeeping delete the record with the session.
func TestRevokeAll_Postgres_ErasureAndHousekeepingDeleteTheRecord(t *testing.T) {
	env := requirePostgres(t)
	repo := NewRepository(env.poolA)

	erased := usrID('8')
	e1, e2 := keyedSession(sessID('3'), erased, "rt_3"), keyedSession(sessID('4'), erased, "rt_4")
	old := keyedSession(sessID('5'), usrID('9'), "rt_5")
	for _, s := range []*app.UserSession{e1, e2, old} {
		if err := repo.SaveSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := repo.RevokeAllSessions(ctx, revokeAllOf(e1)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.RevokeAllSessions(ctx, revokeAllOf(old)); err != nil {
		t.Fatal(err)
	}
	if n := countRevocations(t, env); n != 2 {
		t.Fatalf("%d records, want 2", n)
	}
	// e2 is revoked, so the erasure is requested from a fresh session.
	e3 := keyedSession(sessID('6'), erased, "rt_6")
	if err := repo.SaveSession(ctx, e3); err != nil {
		t.Fatal(err)
	}
	if err := repo.EraseSubscriber(ctx, eraseOf(erased, e3.SessionID, e3.RefreshTokenHash, e3.DeviceKeyThumbprint, e3.ExpiresAt)); err != nil {
		t.Fatalf("erase: %v", err)
	}
	if n := countRevocations(t, env); n != 1 {
		t.Errorf("%d records after the erasure, want 1 (the other account's)", n)
	}

	if _, err := env.poolA.Exec(ctx, `UPDATE identity_sessions SET expires_at = now() - interval '3 hours' WHERE session_id = $1`, old.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := housekeeper(t, env.poolB, time.Now(), 100).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countRevocations(t, env); n != 0 {
		t.Errorf("%d records after housekeeping, want 0", n)
	}
}

// The account's lock orders revoke-all against sign-ins: a sign-in holding
// it makes revoke-all wait, and a session it commits first is revoked; a
// fenced sign-in after the operation is untouched.
func TestRevokeAll_Postgres_SignInsAreOrderedByTheAccountLock(t *testing.T) {
	env := requirePostgres(t)
	repo := NewRepository(env.poolA)
	sub := usrID('a')
	asking := keyedSession(sessID('a'), sub, "rt_a")
	if err := repo.SaveSession(ctx, asking); err != nil {
		t.Fatal(err)
	}

	held := holdSubscriberLock(t, env, sub)
	done := make(chan error, 1)
	go func() {
		_, _, err := repo.RevokeAllSessions(ctx, revokeAllOf(asking))
		done <- err
	}()
	waitBlockedOnAdvisoryLock(t, env, done)
	// A sign-in holding the lock commits its session first.
	during := keyedSession(sessID('b'), sub, "rt_b")
	if err := saveSession(ctx, held, during); err != nil {
		t.Fatal(err)
	}
	if err := held.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("revoke-all: %v", err)
	}
	if !isRevoked(t, repo, during.SessionID) {
		t.Error("a session committed before the operation took the lock survived")
	}

	after := fencedSession('c', sub, time.Now())
	if err := repo.SaveSession(ctx, after); err != nil {
		t.Fatal(err)
	}
	if _, repeated, err := repo.RevokeAllSessions(ctx, revokeAllOf(asking)); err != nil || !repeated {
		t.Fatalf("retry: (%v, %v)", repeated, err)
	}
	if isRevoked(t, repo, after.SessionID) {
		t.Error("a retry revoked a session signed in after the operation")
	}
}

// An erasure and a revoke-all of one account take their locks in opposite
// orders and can deadlock. PostgreSQL aborts one; revoke-all then either
// completes (retried) or reports ErrSignOutContended, and in both cases it is
// all or nothing.
func TestRevokeAll_Postgres_ADeadlockWithAnErasureIsAllOrNothing(t *testing.T) {
	env := requirePostgres(t)
	repo := NewRepository(env.poolA)
	sub := usrID('d')
	asking, erasing := keyedSession(sessID('d'), sub, "rt_d"), keyedSession(sessID('e'), sub, "rt_e")
	for _, s := range []*app.UserSession{asking, erasing} {
		if err := repo.SaveSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	// The erasure's first step: its own session, locked.
	erasure, err := env.poolB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = erasure.Rollback(ctx) })
	if _, lockRowErr := erasure.Exec(ctx, `SELECT 1 FROM identity_sessions WHERE session_id = $1 FOR UPDATE`, erasing.SessionID); lockRowErr != nil {
		t.Fatal(lockRowErr)
	}
	done := make(chan error, 1)
	go func() {
		_, _, revokeErr := repo.RevokeAllSessions(ctx, revokeAllOf(asking))
		done <- revokeErr
	}()
	waitBlockedOnRowLock(t, env, done)
	// The erasure's second step, the account's lock: a deadlock.
	_, lockErr := erasure.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "identity_erasure:"+sub)
	_ = erasure.Rollback(ctx)
	err = <-done
	recorded := countRevocations(t, env)
	switch {
	case err == nil:
		if recorded != 1 || !isRevoked(t, repo, asking.SessionID) || !isRevoked(t, repo, erasing.SessionID) {
			t.Errorf("revoke-all succeeded but left %d records, asking revoked %v, other revoked %v",
				recorded, isRevoked(t, repo, asking.SessionID), isRevoked(t, repo, erasing.SessionID))
		}
	case errors.Is(err, app.ErrSignOutContended):
		if recorded != 0 || isRevoked(t, repo, asking.SessionID) || isRevoked(t, repo, erasing.SessionID) {
			t.Error("a contended revoke-all changed something")
		}
	default:
		t.Fatalf("revoke-all: %v", err)
	}
	if lockErr == nil && err == nil {
		t.Log("PostgreSQL chose the erasure as the deadlock victim only after revoke-all's retry; both outcomes are valid")
	}
}

// waitBlockedOnRowLock waits until some backend waits on a row (transaction)
// lock: the call under test reached a session another transaction holds.
func waitBlockedOnRowLock(t *testing.T, env *pgEnv, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("the call finished without waiting for the row lock: %v", err)
		default:
		}
		var waiting int
		if err := env.poolA.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND wait_event IN ('transactionid', 'tuple')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the call never waited for the row lock")
}

// A failure after the revocations, before the transaction commits, leaves
// neither the revocations nor a record.
func TestRevokeAll_Postgres_AFailedRecordRevokesNothing(t *testing.T) {
	env := requirePostgres(t)
	repo := NewRepository(env.poolA)
	sub := usrID('f')
	s1, s2 := keyedSession(sessID('f'), sub, "rt_f"), keyedSession(sessID('g'), sub, "rt_g")
	for _, s := range []*app.UserSession{s1, s2} {
		if err := repo.SaveSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, env.poolA, `CREATE FUNCTION test_refuse_record() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'test: refused'; END; $$`)
	mustExec(t, env.poolA, `CREATE TRIGGER test_refuse_record BEFORE INSERT ON identity_session_revocations
		FOR EACH ROW EXECUTE FUNCTION test_refuse_record()`)
	if _, _, err := repo.RevokeAllSessions(ctx, revokeAllOf(s1)); err == nil {
		t.Fatal("revoke-all succeeded without its record")
	}
	if isRevoked(t, repo, s1.SessionID) || isRevoked(t, repo, s2.SessionID) || countRevocations(t, env) != 0 {
		t.Error("a revoke-all whose record failed changed something")
	}
}

// Offline: only deadlock and serialisation aborts are retried, a bounded
// number of times, then reported as ErrSignOutContended.
func TestRevokeAll_RetriesOnlyContentionAndBoundsIt(t *testing.T) {
	for name, c := range map[string]struct {
		errs      []error
		calls     int
		contended bool
		ok        bool
	}{
		"success":                    {errs: []error{nil}, calls: 1, ok: true},
		"deadlock then success":      {errs: []error{&pgconn.PgError{Code: "40P01"}, nil}, calls: 2, ok: true},
		"serialisation then success": {errs: []error{&pgconn.PgError{Code: "40001"}, nil}, calls: 2, ok: true},
		"deadlock every time":        {errs: []error{&pgconn.PgError{Code: "40P01"}, &pgconn.PgError{Code: "40P01"}, nil}, calls: revokeAllAttempts, contended: true},
		"another error":              {errs: []error{&pgconn.PgError{Code: "23505"}, nil}, calls: 1},
		"a conflict":                 {errs: []error{app.ErrSessionConflict, nil}, calls: 1},
	} {
		calls := 0
		err := retryContended(revokeAllAttempts, func() error {
			calls++
			return c.errs[calls-1]
		})
		if calls != c.calls || (err == nil) != c.ok || errors.Is(err, app.ErrSignOutContended) != c.contended {
			t.Errorf("%s: %d calls, err %v", name, calls, err)
		}
	}
}
