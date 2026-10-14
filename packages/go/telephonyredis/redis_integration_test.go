package telephonyredis

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
)

// Integration tests against a REAL Redis server. Only these prove atomicity,
// leases and owner fencing; the unit tests prove only what is sent and how
// replies are read.
//
// They run only when TestAddrEnv names a DISPOSABLE Redis (host:port). An
// optional password comes from TestPasswordEnv and is never printed. When the
// address is unset the suite SKIPS — a skip, not a pass. Every test uses its
// own random key prefix and short TTLs, and deletes the keys it wrote.

const (
	TestAddrEnv     = "CS_TELEPHONYREDIS_TEST_ADDR"
	TestPasswordEnv = "CS_TELEPHONYREDIS_TEST_PASSWORD"
)

func realStore(t *testing.T, lease, retention time.Duration) *Store {
	t.Helper()
	addr := strings.TrimSpace(os.Getenv(TestAddrEnv))
	if addr == "" {
		t.Skipf("%s is not set; skipping the real-Redis suite (this is a SKIP, not a PASS)", TestAddrEnv)
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	s := realStoreWithPrefix(t, addr, "it:"+hex.EncodeToString(b)+":", lease, retention)
	// Registered once per test prefix, and run after every other cleanup:
	// deletes every key this test created, by SCAN of its own prefix. Never
	// FLUSHDB or FLUSHALL — the instance may be shared.
	t.Cleanup(func() { deletePrefix(t, s) })
	return s
}

// deletePrefix removes every key under s's prefix, bounded.
func deletePrefix(t *testing.T, s *Store) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cursor := "0"
	for i := 0; i < 10_000; i++ {
		v, err := s.cl.do(ctx, "SCAN", cursor, "MATCH", s.cfg.KeyPrefix+"*", "COUNT", "100")
		if err != nil || v.kind != '*' || len(v.arr) != 2 {
			t.Errorf("cleanup SCAN failed: %v", err)
			return
		}
		cursor = v.arr[0].str
		for _, k := range v.arr[1].arr {
			if _, err := s.cl.do(ctx, "DEL", k.str); err != nil {
				t.Errorf("cleanup DEL failed: %v", err)
				return
			}
		}
		if cursor == "0" {
			return
		}
	}
	t.Error("cleanup SCAN did not finish")
}

// awaitGone polls until key no longer exists, bounded — a TTL is Redis's
// clock, so this waits on Redis rather than guessing with a fixed sleep.
func awaitGone(t *testing.T, s *Store, key string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		v, err := s.cl.do(context.Background(), "EXISTS", key)
		if err != nil {
			t.Fatalf("EXISTS: %v", err)
		}
		if v.num == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("key did not expire within %s", within)
}

func realStoreWithPrefix(t *testing.T, addr, prefix string, lease, retention time.Duration) *Store {
	t.Helper()
	cc := ClientConfig{
		Addr: addr, Password: os.Getenv(TestPasswordEnv),
		DialTimeout: 5 * time.Second, OpTimeout: 5 * time.Second, PoolSize: 16,
	}
	s, err := New(cc, Config{KeyPrefix: prefix, Lease: lease, Retention: retention, MaxConflicts: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// replica returns a second, independent Store over the same keys: another
// process, as far as Redis can tell.
func replica(t *testing.T, s *Store) *Store {
	t.Helper()
	return realStoreWithPrefix(t, s.cl.cfg.Addr, s.cfg.KeyPrefix, s.cfg.Lease, s.cfg.Retention)
}

func cleanup(t *testing.T, s *Store, keys ...string) {
	t.Cleanup(func() {
		for _, k := range keys {
			_, _ = s.cl.do(context.Background(), "DEL", k)
		}
	})
}

func TestRedis_ConcurrentReservationsAcrossReplicas(t *testing.T) {
	a := realStore(t, 30*time.Second, time.Minute)
	b := replica(t, a)
	k, _ := a.eventKey(key)
	cleanup(t, a, k)

	const n = 32
	var wg sync.WaitGroup
	statuses := make(chan callback.ReserveStatus, n)
	for i := 0; i < n; i++ {
		s := a
		if i%2 == 1 {
			s = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := s.Reserve(context.Background(), key, fp)
			if err != nil {
				t.Errorf("Reserve: %v", err)
				return
			}
			statuses <- res.Status
		}()
	}
	wg.Wait()
	close(statuses)
	count := map[callback.ReserveStatus]int{}
	for st := range statuses {
		count[st]++
	}
	if count[callback.StatusReserved] != 1 || count[callback.StatusInFlight] != n-1 {
		t.Errorf("statuses = %v, want exactly one reserved and the rest in flight", count)
	}
}

func TestRedis_LeaseExpiryAllowsTakeoverAndFencesTheStaleOwner(t *testing.T) {
	a := realStore(t, 200*time.Millisecond, time.Minute)
	b := replica(t, a)
	k, _ := a.eventKey(key)
	cleanup(t, a, k)
	ctx := context.Background()

	stale, err := a.Reserve(ctx, key, fp)
	if err != nil || stale.Status != callback.StatusReserved {
		t.Fatalf("first Reserve = %+v, %v", stale, err)
	}
	if res, _ := b.Reserve(ctx, key, fp); res.Status != callback.StatusInFlight {
		t.Fatalf("Reserve within the lease = %+v, want in flight", res)
	}

	awaitGone(t, a, k, 10*time.Second) // the holder "crashed"; its lease expires

	fresh, err := b.Reserve(ctx, key, fp)
	if err != nil || fresh.Status != callback.StatusReserved || fresh.Token == stale.Token {
		t.Fatalf("Reserve after the lease = %+v, %v; want a new reservation", fresh, err)
	}
	if err := a.Settle(ctx, key, stale.Token, callback.Result{Outcome: callback.OutcomeApplied}); !errors.Is(err, callback.ErrNotOwner) {
		t.Errorf("stale Settle = %v, want ErrNotOwner", err)
	}
	if err := a.Release(ctx, key, stale.Token); !errors.Is(err, callback.ErrNotOwner) {
		t.Errorf("stale Release = %v, want ErrNotOwner", err)
	}
	want := callback.Result{Outcome: callback.OutcomeApplied, Call: "call_it"}
	if err := b.Settle(ctx, key, fresh.Token, want); err != nil {
		t.Fatalf("owner Settle: %v", err)
	}
	dup, err := a.Reserve(ctx, key, fp)
	if err != nil || dup.Status != callback.StatusDuplicate || dup.Prior != want {
		t.Errorf("Reserve after settle = %+v, %v; want duplicate of %+v", dup, err, want)
	}
	if err := b.Release(ctx, key, fresh.Token); !errors.Is(err, callback.ErrNotOwner) {
		t.Errorf("Release after Settle = %v, want ErrNotOwner", err)
	}
}

func TestRedis_ReleaseAllowsRetryAndMismatchIsReported(t *testing.T) {
	s := realStore(t, 30*time.Second, time.Minute)
	k, _ := s.eventKey(key)
	cleanup(t, s, k)
	ctx := context.Background()

	first, err := s.Reserve(ctx, key, fp)
	if err != nil || first.Status != callback.StatusReserved {
		t.Fatalf("Reserve = %+v, %v", first, err)
	}
	if err := s.Release(ctx, key, first.Token); err != nil {
		t.Fatalf("Release: %v", err)
	}
	retry, err := s.Reserve(ctx, key, fp)
	if err != nil || retry.Status != callback.StatusReserved {
		t.Fatalf("retry Reserve = %+v, %v", retry, err)
	}
	other := callback.Fingerprint{ProviderCallID: "pc-2", Kind: callback.KindRinging}
	if res, _ := s.Reserve(ctx, key, other); res.Status != callback.StatusMismatch {
		t.Errorf("different fingerprint = %+v, want mismatch", res)
	}
}

func TestRedis_RetentionIsEnforcedByTTL(t *testing.T) {
	s := realStore(t, time.Second, 2*time.Second)
	ctx := context.Background()
	k, _ := s.eventKey(key)
	term, _ := s.terminalKey("call_it_ttl")
	cleanup(t, s, k, term, s.conflictKey())

	res, err := s.Reserve(ctx, key, fp)
	if err != nil {
		t.Fatal(err)
	}
	pttl := func(key string) int64 {
		v, err := s.cl.do(ctx, "PTTL", key)
		if err != nil {
			t.Fatal(err)
		}
		return v.num
	}
	if ttl := pttl(k); ttl <= 0 || ttl > 1000 {
		t.Errorf("reservation TTL = %dms, want within the 1s lease", ttl)
	}
	if err := s.Settle(ctx, key, res.Token, callback.Result{Outcome: callback.OutcomeApplied}); err != nil {
		t.Fatal(err)
	}
	if ttl := pttl(k); ttl <= 1000 || ttl > 2000 {
		t.Errorf("settled TTL = %dms, want the 2s retention", ttl)
	}
	if err := s.MarkTerminal(ctx, "call_it_ttl"); err != nil {
		t.Fatal(err)
	}
	if ttl := pttl(term); ttl <= 0 || ttl > 2000 {
		t.Errorf("terminal mark TTL = %dms", ttl)
	}
	for i := 0; i < 5; i++ {
		if err := s.RecordConflict(ctx, callback.Conflict{Reason: "r" + itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	cs, err := s.Conflicts(ctx)
	if err != nil || len(cs) != 3 || cs[0].Reason != "r4" {
		t.Errorf("conflicts = %+v, %v; want the newest 3", cs, err)
	}

	awaitGone(t, s, k, 10*time.Second)
	awaitGone(t, s, term, 10*time.Second)
	if again, err := s.Reserve(ctx, key, fp); err != nil || again.Status != callback.StatusReserved {
		t.Errorf("after retention = %+v, %v; want the key forgotten", again, err)
	}
	if ok, err := s.Terminal(ctx, "call_it_ttl"); err != nil || ok {
		t.Errorf("terminal mark after retention = %v, %v", ok, err)
	}
}

// TestRedis_IngestorsOnTwoReplicasApplyOnce: the full P1-4 ingestor, twice,
// sharing one Redis, racing on one event.
func TestRedis_IngestorsOnTwoReplicasApplyOnce(t *testing.T) {
	a := realStore(t, 30*time.Second, time.Minute)
	b := replica(t, a)

	h, err := telephony.NewHarness()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = h.Stop(context.Background()) })
	sess, err := h.BeginInbound(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := callback.NewBindings(callback.RegistryProviders(h.Runtime.Registry()), 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindings.Bind(callback.Binding{Provider: h.Provider.ID(), ProviderCallID: "pc-it", Call: sess.ID()}); err != nil {
		t.Fatal(err)
	}
	life := callback.RuntimeLifecycle(h.Runtime)
	inA, _ := callback.NewIngestor(a, bindings, life)
	inB, _ := callback.NewIngestor(b, bindings, life)
	e := callback.Event{
		Provider: h.Provider.ID(), EventID: "it-ring-1", ProviderCallID: "pc-it",
		Kind: callback.KindRinging, ReceivedAt: time.Now(),
	}
	k, _ := a.eventKey(e.Key())
	cleanup(t, a, k)
	before := sess.HistoryLen()

	const n = 16
	var wg sync.WaitGroup
	outcomes := make(chan callback.Outcome, n)
	for i := 0; i < n; i++ {
		in := inA
		if i%2 == 1 {
			in = inB
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := in.Ingest(context.Background(), e)
			if err != nil {
				t.Errorf("Ingest: %v", err)
				return
			}
			outcomes <- res.Outcome
		}()
	}
	wg.Wait()
	close(outcomes)
	applied := 0
	for o := range outcomes {
		if o == callback.OutcomeApplied {
			applied++
		}
	}
	if applied != 1 || sess.HistoryLen() != before+1 {
		t.Errorf("applied %d times, history grew by %d; want exactly once", applied, sess.HistoryLen()-before)
	}
}
