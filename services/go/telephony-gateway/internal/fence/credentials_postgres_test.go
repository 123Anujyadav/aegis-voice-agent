package fence

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// P1-A1 — per-connection credentials against a REAL disposable PostgreSQL
// (the closest offline analogue of per-connection RDS IAM tokens: a password
// that changes between connections). SKIP without FenceTestDSNEnv. The role
// and its passwords are random, created and dropped by the test, never logged.

type rotatingCreds struct {
	mu       sync.Mutex
	password string
	calls    atomic.Int64
}

func (r *rotatingCreds) set(p string) { r.mu.Lock(); r.password = p; r.mu.Unlock() }

func (r *rotatingCreds) Password(context.Context, string, uint16, string) (string, error) {
	r.calls.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.password, nil
}

func randomHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// A rotation does not disturb the live session, and the next connection asks
// the source again and uses the rotated credential.
//
// What is judged is the session, not one instant of ownership: after the
// rotation the live session must confirm twice more with no loss between, and
// the same backend must still hold the lock. If the fence itself gave that
// session up in a fail-closed timing loss (under load), the rotation is not
// what ended it and that run cannot judge it, so the scenario runs again, at
// most rotationAttempts times; the test fails if no attempt could judge it,
// and on any other loss at once.
func TestPostgresFence_CredentialDialerReconnectsWithARotatedCredential(t *testing.T) {
	dsn := testDSN(t)
	const rotationAttempts = 3
	for i := 1; i <= rotationAttempts; i++ {
		if rotateOnce(t, dsn) {
			return
		}
		t.Logf("attempt %d of %d could not judge the rotation: a fail-closed timing loss ended the session first", i, rotationAttempts)
	}
	t.Fatalf("no attempt in %d could judge the rotation; each lost its session to a fail-closed timing loss first", rotationAttempts)
}

// rotateOnce runs the rotation scenario once. It returns false only when a
// fail-closed timing loss, not the rotation, ended the session before it could
// be judged; every other outcome passes or fails the test.
func rotateOnce(t *testing.T, dsn string) bool {
	t.Helper()
	ctx := context.Background()
	adm := admin(t, dsn)
	role := "fence_rot_" + randomHex(t)[:12]
	p1, p2 := randomHex(t), randomHex(t)
	if _, err := adm.Exec(ctx, "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" LOGIN PASSWORD '"+p1+"'"); err != nil {
		t.Fatal(redact(err))
	}
	t.Cleanup(func() {
		_, _ = adm.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = $1`, role)
		_, _ = adm.Exec(ctx, "DROP ROLE IF EXISTS "+pgx.Identifier{role}.Sanitize())
	})

	cc := connCfg(t, dsn)
	cc.User, cc.Password = role, "" // the credential source is the only password
	creds := &rotatingCreds{}
	creds.set(p1)
	key := uniqueKey()
	dial, err := PgxCredentialDialer(cc, creds)
	if err != nil {
		t.Fatal(err)
	}
	f, ev := startObserved(t, dial, key)
	defer func() {
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = f.Shutdown(sctx)
	}()

	waitFor(t, 10*time.Second, "ownership with the first credential", f.Owned)
	// One request per connection attempt. Before ownership was seen there was
	// one per failed attempt, one per attempt whose ownership was then lost
	// (under load), and the one that owns now.
	if got, want := creds.calls.Load(), int64(1+ev.count(fenceEvent.attemptFailed)+ev.count(fenceEvent.loss)); got != want {
		t.Errorf("credential requests before ownership = %d, want %d (one per connection attempt)", got, want)
	}
	pid := ownedHolder(t, adm, f, key)
	judged := len(ev.all())

	// Rotate: the old password stops working; the live session is unaffected.
	if _, err := adm.Exec(ctx, "ALTER ROLE "+pgx.Identifier{role}.Sanitize()+" PASSWORD '"+p2+"'"); err != nil {
		t.Fatal(redact(err))
	}
	beats := ev.count(fenceEvent.confirmed)
	waitFor(t, 10*time.Second, "two confirmations after the rotation, or a loss", func() bool {
		losses, _ := lossesAfter(ev, judged)
		return len(losses) > 0 || ev.count(fenceEvent.confirmed) >= beats+2
	})
	losses, timingOnly := lossesAfter(ev, judged)
	now, held := holderPID(t, adm, key)
	switch {
	case len(losses) == 0 && held && now == pid:
		// The live session survived the rotation and kept confirming.
	case len(losses) > 0 && timingOnly:
		return false
	default:
		t.Fatalf("rotating the password ended the live session (holder %d before; %d, held=%v after); it must not. Losses: %v",
			pid, now, held, losses)
	}

	// Force a re-dial: the source now hands out the new password.
	creds.set(p2)
	asked := creds.calls.Load()
	terminated := len(ev.all())
	terminate(t, adm, pid)
	waitFor(t, 5*time.Second, "loss", func() bool { return !f.Owned() })
	waitFor(t, 10*time.Second, "re-acquisition with the rotated credential", f.Owned)
	if creds.calls.Load() <= asked {
		t.Errorf("credential requests = %d after the re-dial, %d before; the re-dial must ask again", creds.calls.Load(), asked)
	}
	// The termination costs one loss; any other must be a fail-closed timing one.
	losses, _ = lossesAfter(ev, terminated)
	other := 0
	for _, l := range losses {
		if !l.failClosedTiming(steadyCfg.HeartbeatTimeout) {
			other++
		}
	}
	switch {
	case len(losses) == 0:
		t.Error("terminating the session recorded no loss")
	case other > 1:
		t.Errorf("losses after the termination beyond the one it caused: %v", losses)
	}
	return true
}

// lossesAfter returns the losses recorded after the first n events, and
// whether every one of them was a fail-closed timing loss (with steadyCfg).
func lossesAfter(ev *events, n int) ([]fenceEvent, bool) {
	all := ev.all()
	if n > len(all) {
		n = len(all)
	}
	var losses []fenceEvent
	timingOnly := true
	for _, e := range all[n:] {
		if e.loss() {
			losses = append(losses, e)
			timingOnly = timingOnly && e.failClosedTiming(steadyCfg.HeartbeatTimeout)
		}
	}
	return losses, timingOnly
}

// A source handing out a wrong credential never yields ownership.
func TestPostgresFence_AWrongCredentialNeverOwns(t *testing.T) {
	dsn := testDSN(t)
	cc := connCfg(t, dsn)
	cc.Password = ""
	creds := &rotatingCreds{}
	creds.set("wrong-" + randomHex(t))
	c := testCfg
	c.LockKey = uniqueKey()
	dial, _ := PgxCredentialDialer(cc, creds)
	f, _ := New(c, dial, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go func() { _ = f.Run(context.Background()) }()
	t.Cleanup(func() { _ = f.Shutdown(context.Background()) })
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f.Owned() {
			t.Fatal("owner with a wrong credential")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if creds.calls.Load() < 2 {
		t.Errorf("credential requests = %d; each retry must ask again", creds.calls.Load())
	}
}
