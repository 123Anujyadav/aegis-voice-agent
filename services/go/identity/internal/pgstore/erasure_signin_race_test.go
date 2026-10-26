package pgstore

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// A sign-in racing an erasure, on real PostgreSQL. EraseSubscriber and a
// fenced SaveSession (app.UserSession.ErasureFence) serialise on the
// subscriber's advisory lock, whichever starts first. Each test holds the
// lock in a transaction of its own, standing in for the other side caught
// mid-transaction, and waits until PostgreSQL reports the repository call
// blocked on it: the interleaving is forced, not hoped for.

func holdSubscriberLock(t *testing.T, env *pgEnv, subscriber string) pgx.Tx {
	t.Helper()
	tx, err := env.poolB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "identity_erasure:"+subscriber); err != nil {
		t.Fatal(err)
	}
	return tx
}

// waitBlockedOnAdvisoryLock waits until some backend is waiting for an
// advisory lock: the repository call under test has reached the lock.
func waitBlockedOnAdvisoryLock(t *testing.T, env *pgEnv, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("the call finished without waiting for the subscriber lock: %v", err)
		default:
		}
		var waiting int
		if err := env.poolA.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND wait_event = 'advisory'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the call never waited for the subscriber lock")
}

func sessionsOf(t *testing.T, env *pgEnv, subscriber string) int {
	t.Helper()
	var n int
	if err := env.poolA.QueryRow(ctx, `SELECT count(*) FROM identity_sessions WHERE subscriber_id = $1`, subscriber).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func fencedSession(id byte, subscriber string, fence time.Time) *app.UserSession {
	return &app.UserSession{
		SessionID: sessID(id), SubscriberID: subscriber, RefreshTokenHash: th("rt_new"), Roles: []string{"subscriber"},
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(24 * time.Hour), DeviceKeyThumbprint: jktOf(id),
		ErasureFence: fence,
	}
}

// An erasure is mid-transaction (it holds the lock and has written its
// record) when a sign-in that began before it saves its session: the save
// waits, then sees the erasure and creates nothing.
func TestErasure_Postgres_ASignInWaitsForAnErasureInProgressAndIsRefused(t *testing.T) {
	env := requirePostgres(t)
	repo := NewRepository(env.poolA)
	began := time.Now().Add(-time.Second)

	erasing := holdSubscriberLock(t, env, usrID('a'))
	if _, err := erasing.Exec(ctx, `INSERT INTO identity_erasures (subscriber_id, requested_at, exceptions) VALUES ($1, now(), '{}')`, usrID('a')); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- repo.SaveSession(ctx, fencedSession('7', usrID('a'), began)) }()
	waitBlockedOnAdvisoryLock(t, env, done)
	if err := erasing.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if err := <-done; !errors.Is(err, app.ErrSubscriberErased) {
		t.Fatalf("SaveSession = %v, want ErrSubscriberErased", err)
	}
	if n := sessionsOf(t, env, usrID('a')); n != 0 {
		t.Fatalf("%d sessions of the erased subscriber", n)
	}

	// A sign-in that began after the erasure is not fenced out (D1 unchanged),
	// and an unfenced save never takes the lock.
	time.Sleep(10 * time.Millisecond)
	if err := repo.SaveSession(ctx, fencedSession('8', usrID('a'), time.Now())); err != nil {
		t.Fatalf("a sign-in begun after the erasure: %v", err)
	}
}

// A sign-in is mid-transaction (it holds the lock and has inserted its
// session) when the erasure runs: the erasure waits for it, then deletes that
// session too. Without the lock, the erasure's DELETE would not see the
// uncommitted session, and it would survive.
func TestErasure_Postgres_AnErasureWaitsForASignInInProgressAndDeletesItsSession(t *testing.T) {
	env := requirePostgres(t)
	repo := NewRepository(env.poolA)
	seedErasure(t, backend{a: repo, b: NewRepository(env.poolB)})

	signingIn := holdSubscriberLock(t, env, usrID('a'))
	if err := saveSession(ctx, signingIn, fencedSession('7', usrID('a'), time.Time{})); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- repo.EraseSubscriber(ctx, eraseOf(usrID('a'), sessID('1'), th("rt_1"), jktOf('1'), time.Now().Add(time.Hour)))
	}()
	waitBlockedOnAdvisoryLock(t, env, done)
	if err := signingIn.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if err := <-done; err != nil {
		t.Fatalf("EraseSubscriber: %v", err)
	}
	if n := sessionsOf(t, env, usrID('a')); n != 0 {
		t.Fatalf("%d sessions of the erased subscriber survived; the one created mid-erasure must go too", n)
	}
	if n := sessionsOf(t, env, usrID('b')); n != 1 {
		t.Fatalf("another subscriber's sessions: %d, want 1", n)
	}
}
