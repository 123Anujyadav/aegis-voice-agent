package fence

import (
	"context"
	"math"
	"testing"
	"time"
)

// Phase 19 P1-31 — real-PostgreSQL audit checks. SKIP without FenceTestDSNEnv.

// Every int64 key — negative, high-bit, extremes — is confirmed by the
// per-backend pg_locks check (classid/objid split), so no valid key makes the
// fence fail to recognise its own lock.
func TestPostgresFence_KeysAcrossTheInt64RangeAreConfirmed(t *testing.T) {
	dsn := testDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := PgxDialer(connCfg(t, dsn))(ctx)
	if err != nil {
		t.Fatal(redact(err))
	}
	defer sess.Close(ctx)
	if primary, err := sess.Primary(ctx); err != nil || !primary {
		t.Fatalf("Primary on a primary = %v, %v", primary, redact(err))
	}
	for _, key := range []int64{1, -1, 0x7fffffff, 0x80000000, 1<<40 | 7, math.MaxInt64, math.MinInt64, math.MinInt64 + 1, -0x435349444d494752} {
		ok, err := sess.TryLock(ctx, key)
		if err != nil || !ok {
			t.Fatalf("TryLock(%d) = %v, %v", key, ok, redact(err))
		}
		if held, err := sess.Holds(ctx, key); err != nil || !held {
			t.Errorf("Holds(%d) = %v, %v: the fence would not recognise its own lock", key, held, redact(err))
		}
		if err := sess.Unlock(ctx, key); err != nil {
			t.Fatal(redact(err))
		}
		if held, err := sess.Holds(ctx, key); err != nil || held {
			t.Errorf("Holds(%d) after unlock = %v, %v", key, held, redact(err))
		}
	}
}

// A different key held by the same backend is NOT mistaken for ours, and the
// two-int4 advisory form (identity's housekeeping) never aliases a bigint key.
func TestPostgresFence_OnlyTheExactKeyCounts(t *testing.T) {
	dsn := testDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := PgxDialer(connCfg(t, dsn))(ctx)
	if err != nil {
		t.Fatal(redact(err))
	}
	defer sess.Close(ctx)
	ps := sess.(pgSession)
	const key int64 = 0x0000_0001_0000_0002 // classid 1, objid 2
	// The two-int4 form with the same halves: objsubid 2, a different lock.
	if _, err := ps.conn.Exec(ctx, `SELECT pg_advisory_lock(1, 2)`); err != nil {
		t.Fatal(redact(err))
	}
	if held, err := sess.Holds(ctx, key); err != nil || held {
		t.Errorf("a two-int4 lock was taken for the bigint key: %v, %v", held, redact(err))
	}
	if ok, err := sess.TryLock(ctx, key+1); err != nil || !ok {
		t.Fatal("TryLock(other key) failed")
	}
	if held, err := sess.Holds(ctx, key); err != nil || held {
		t.Errorf("another bigint key was taken for ours: %v, %v", held, redact(err))
	}
}
