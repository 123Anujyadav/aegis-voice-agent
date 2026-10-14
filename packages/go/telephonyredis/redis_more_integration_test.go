package telephonyredis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/telephony/callback"
)

// Phase 19 P1-16 — further real-Redis scenarios. Same opt-in rules as
// redis_integration_test.go: they SKIP without CS_TELEPHONYREDIS_TEST_ADDR and
// FAIL on any connection or assertion error when it is set. Keys are under a
// random per-test prefix and are removed by prefix-scoped SCAN + DEL.

// TestRedis_DistinctEventsReserveIndependently: concurrent reservations of
// DIFFERENT events never contend — every one is reserved, with its own token.
func TestRedis_DistinctEventsReserveIndependently(t *testing.T) {
	s := realStore(t, 30*time.Second, time.Minute)
	other := replica(t, s)

	const n = 32
	var wg sync.WaitGroup
	tokens := make(chan string, n)
	for i := 0; i < n; i++ {
		store := s
		if i%2 == 1 {
			store = other
		}
		k := callback.Key{Provider: "fake-carrier", EventID: "distinct-" + strconv.Itoa(i)}
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := store.Reserve(context.Background(), k, fp)
			if err != nil || res.Status != callback.StatusReserved {
				t.Errorf("Reserve(%s) = %+v, %v", k.EventID, res, err)
				return
			}
			tokens <- res.Token
		}()
	}
	wg.Wait()
	close(tokens)
	seen := map[string]bool{}
	for tok := range tokens {
		if seen[tok] {
			t.Error("two reservations share a token")
		}
		seen[tok] = true
	}
	if len(seen) != n {
		t.Errorf("%d reservations, want %d", len(seen), n)
	}
}

// TestRedis_ConcurrentSettleAndReleaseHaveOneWinner: many concurrent Settle and
// Release calls with the owner's token — exactly one succeeds, and the key ends
// either settled (a duplicate) or free (reservable), never half of each.
func TestRedis_ConcurrentSettleAndReleaseHaveOneWinner(t *testing.T) {
	s := realStore(t, 30*time.Second, time.Minute)
	other := replica(t, s)
	ctx := context.Background()

	res, err := s.Reserve(ctx, key, fp)
	if err != nil || res.Status != callback.StatusReserved {
		t.Fatalf("Reserve = %+v, %v", res, err)
	}
	const n = 16
	var wg sync.WaitGroup
	type outcome struct {
		settle bool
		err    error
	}
	results := make(chan outcome, 2*n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(st *Store) {
			defer wg.Done()
			results <- outcome{true, st.Settle(ctx, key, res.Token, callback.Result{Outcome: callback.OutcomeApplied})}
		}(s)
		go func(st *Store) {
			defer wg.Done()
			results <- outcome{false, st.Release(ctx, key, res.Token)}
		}(other)
	}
	wg.Wait()
	close(results)
	var wins, settled int
	for r := range results {
		switch {
		case r.err == nil:
			wins++
			if r.settle {
				settled++
			}
		case !errors.Is(r.err, callback.ErrNotOwner):
			t.Errorf("unexpected error: %v", r.err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d operations succeeded with one token, want exactly 1", wins)
	}
	after, err := s.Reserve(ctx, key, fp)
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case settled == 1 && after.Status != callback.StatusDuplicate:
		t.Errorf("settle won but the key is %d, want duplicate", after.Status)
	case settled == 0 && after.Status != callback.StatusReserved:
		t.Errorf("release won but the key is %d, want reservable", after.Status)
	}
}

// TestRedis_RecoversAfterTheServerDropsTheConnection: the server closes this
// store's connection (CLIENT KILL by ID, from a separate store — it affects
// only that one connection, never other clients). The next operation either
// fails as ErrUnavailable or succeeds on a fresh connection; within two
// attempts it succeeds. It is never misreported.
func TestRedis_RecoversAfterTheServerDropsTheConnection(t *testing.T) {
	admin := realStore(t, 30*time.Second, time.Minute)
	single, err := New(ClientConfig{
		Addr: admin.cl.cfg.Addr, Password: admin.cl.cfg.Password,
		DialTimeout: 5 * time.Second, OpTimeout: 5 * time.Second, PoolSize: 1,
	}, admin.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = single.Close() })
	ctx := context.Background()

	v, err := single.cl.do(ctx, "CLIENT", "ID")
	if err != nil || v.kind != ':' {
		t.Fatalf("CLIENT ID = %+v, %v", v, err)
	}
	killed, err := admin.cl.do(ctx, "CLIENT", "KILL", "ID", fmt.Sprint(v.num))
	if err != nil || killed.num != 1 {
		t.Fatalf("CLIENT KILL = %+v, %v; want exactly one connection killed", killed, err)
	}

	var errs []error
	for attempt := 0; attempt < 2; attempt++ {
		ok, err := single.Terminal(ctx, "call_recovery")
		if err == nil {
			if ok {
				t.Error("an unmarked call reported terminal")
			}
			return
		}
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("attempt %d: %v; a dropped connection must be ErrUnavailable", attempt, err)
		}
		errs = append(errs, err)
	}
	t.Fatalf("no recovery within two attempts: %v", errs)
}

// TestRedis_TimeoutIsBoundedAgainstARealServer: an OpTimeout shorter than a
// server-side block (a 2 s blocking pop on a key nobody writes, under this
// test's prefix) fails as ErrUnavailable wrapping DeadlineExceeded, on time.
func TestRedis_TimeoutIsBoundedAgainstARealServer(t *testing.T) {
	s := realStore(t, 30*time.Second, time.Minute)
	short, err := New(ClientConfig{
		Addr: s.cl.cfg.Addr, Password: s.cl.cfg.Password,
		DialTimeout: 5 * time.Second, OpTimeout: 200 * time.Millisecond, PoolSize: 1,
	}, s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = short.Close() })

	start := time.Now()
	_, err = short.cl.do(context.Background(), "BLPOP", s.cfg.KeyPrefix+"never-written", "2")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("blocked command = %v, want DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("OpTimeout did not bound the command: %s", d)
	}
	// The store still works afterwards, on a new connection.
	if _, err := short.Terminal(context.Background(), "call_after_timeout"); err != nil {
		t.Errorf("after a timeout: %v", err)
	}
}
