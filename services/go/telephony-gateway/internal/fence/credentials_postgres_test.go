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

func TestPostgresFence_CredentialDialerReconnectsWithARotatedCredential(t *testing.T) {
	dsn := testDSN(t)
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
	c := testCfg
	c.LockKey = key
	dial, err := PgxCredentialDialer(cc, creds)
	if err != nil {
		t.Fatal(err)
	}
	f, err := New(c, dial, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = f.Run(ctx) }()
	t.Cleanup(func() { _ = f.Shutdown(context.Background()) })

	waitFor(t, 10*time.Second, "ownership with the first credential", f.Owned)
	if creds.calls.Load() != 1 {
		t.Errorf("credential requests before ownership = %d, want 1", creds.calls.Load())
	}

	// Rotate: the old password stops working; the live session is unaffected.
	if _, err := adm.Exec(ctx, "ALTER ROLE "+pgx.Identifier{role}.Sanitize()+" PASSWORD '"+p2+"'"); err != nil {
		t.Fatal(redact(err))
	}
	time.Sleep(3 * testCfg.Validity())
	if !f.Owned() {
		t.Fatal("rotating the password ended a live session's ownership; it must not")
	}

	// Force a re-dial: the source now hands out the new password.
	creds.set(p2)
	pid, ok := holderPID(t, adm, key)
	if !ok {
		t.Fatal("no holder")
	}
	terminate(t, adm, pid)
	waitFor(t, 5*time.Second, "loss", func() bool { return !f.Owned() })
	waitFor(t, 10*time.Second, "re-acquisition with the rotated credential", f.Owned)
	if creds.calls.Load() < 2 {
		t.Errorf("credential requests = %d; the re-dial must ask again", creds.calls.Load())
	}
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
