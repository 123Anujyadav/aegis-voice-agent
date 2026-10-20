package fence

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Real-PostgreSQL fence tests (P1-30A, D6 evaluation). Opt-in: they SKIP
// unless FenceTestDSNEnv names a DISPOSABLE database (a throwaway container);
// they touch nothing but advisory locks and their own sessions. Timing values
// here are TEST values chosen for speed, not proposals for production (D10).
//
// A partition is simulated in-process by a TCP proxy that can black-hole
// traffic without closing either side — the shape of a real partition, where
// neither end learns the other is gone.

// FenceTestDSNEnv names the environment variable holding a disposable DSN.
const FenceTestDSNEnv = "CS_TELEPHONY_GATEWAY_FENCE_TEST_DSN"

var testCfg = Config{HeartbeatInterval: 200 * time.Millisecond, HeartbeatTimeout: 100 * time.Millisecond}

// steadyCfg is for the tests that assert a steady state (one owner; a session
// that survives a password rotation) rather than a timing bound. The heartbeat
// timeout also bounds every dial (holdOnce), and under -race a single SCRAM
// login was measured at 65-115ms, so with testCfg a standby can fail to
// connect at all; these tests assume the timeout comfortably exceeds one
// login. Timing-bound tests keep testCfg. TEST values, not proposals (D10).
var steadyCfg = Config{HeartbeatInterval: time.Second, HeartbeatTimeout: 500 * time.Millisecond}

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(FenceTestDSNEnv))
	if dsn == "" {
		t.Skipf("%s is not set; skipping the real-PostgreSQL fence suite (a SKIP, not a PASS)", FenceTestDSNEnv)
	}
	return dsn
}

// uniqueKey gives each test its own lock.
func uniqueKey() int64 { return time.Now().UnixNano() & 0x7fffffffffff }

func connCfg(t *testing.T, dsn string) *pgx.ConnConfig {
	t.Helper()
	c, err := ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func startFence(t *testing.T, cc *pgx.ConnConfig, key int64) *Fence {
	t.Helper()
	return startFenceWith(t, PgxDialer(cc), key)
}

// startFenceWith is startFence with the test supplying the dialer.
func startFenceWith(t *testing.T, dial Dialer, key int64) *Fence {
	t.Helper()
	return startFenceLogging(t, dial, key, testCfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// startObserved starts a fence with steadyCfg whose records are kept, so the
// test can classify any ownership loss it suffers. If the test fails, they are
// logged (heartbeats as a count) to show what the fence was doing.
func startObserved(t *testing.T, dial Dialer, key int64) (*Fence, *events) {
	t.Helper()
	ev := &events{}
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		t.Logf("fence records (%d heartbeats confirmed):", ev.count(fenceEvent.confirmed))
		for _, e := range ev.all() {
			if !e.confirmed() {
				t.Logf("  %s", e)
			}
		}
	})
	return startFenceLogging(t, dial, key, steadyCfg, slog.New(ev)), ev
}

func startFenceLogging(t *testing.T, dial Dialer, key int64, c Config, log *slog.Logger) *Fence {
	t.Helper()
	c.LockKey = key
	f, err := New(c, dial, log)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = f.Run(context.Background()) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = f.Shutdown(ctx)
	})
	return f
}

// sampler records any instant at which more than one fence claims ownership.
type sampler struct {
	overlaps atomic.Int64
	samples  atomic.Int64
	owned    atomic.Int64 // samples at which some fence owned
	stop     chan struct{}
	done     chan struct{}
	halted   sync.Once
	finished atomic.Bool
}

// sample starts the overlap sampler. It is also stopped when t ends, so a test
// that fails before calling finish (a timed-out waitFor is a t.Fatal) does not
// leave it polling for the rest of the test binary; any overlap it saw is
// still reported then.
func sample(t *testing.T, fences ...*Fence) *sampler {
	t.Helper()
	s := &sampler{stop: make(chan struct{}), done: make(chan struct{})}
	t.Cleanup(func() {
		s.halt()
		if !s.finished.Load() {
			if n := s.overlaps.Load(); n != 0 {
				t.Errorf("OVERLAP: two fences claimed ownership at %d of %d samples", n, s.samples.Load())
			}
		}
	})
	go func() {
		defer close(s.done)
		for {
			select {
			case <-s.stop:
				return
			default:
			}
			owners := 0
			for _, f := range fences {
				if f.Owned() {
					owners++
				}
			}
			if owners > 1 {
				s.overlaps.Add(1)
			}
			if owners > 0 {
				s.owned.Add(1)
			}
			s.samples.Add(1)
			time.Sleep(time.Millisecond)
		}
	}()
	return s
}

// halt stops the sampler and waits for it; it is safe to call more than once.
func (s *sampler) halt() {
	s.halted.Do(func() { close(s.stop) })
	<-s.done
}

func (s *sampler) finish(t *testing.T) {
	t.Helper()
	s.finished.Store(true)
	s.halt()
	if n := s.overlaps.Load(); n != 0 {
		t.Errorf("OVERLAP: two fences claimed ownership at %d of %d samples", n, s.samples.Load())
	}
	t.Logf("no overlap in %d samples (%d with an owner)", s.samples.Load(), s.owned.Load())
}

// requireExercised fails the test if the sampler never saw an owner: the
// overlap check would then have passed without checking anything.
func (s *sampler) requireExercised(t *testing.T) {
	t.Helper()
	if s.owned.Load() == 0 {
		t.Errorf("the overlap check saw no owner in %d samples; safety was not exercised", s.samples.Load())
	}
}

// The sampler must not outlive a test that ends before finish. It needs no
// database: the fence is never run, so it never owns.
func TestSampler_StopsWhenItsTestEndsEarly(t *testing.T) {
	c := testCfg
	c.LockKey = uniqueKey()
	f, err := New(c, func(context.Context) (Session, error) { return nil, errors.New("test: no database") },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	var s *sampler
	t.Run("ends without finish", func(t *testing.T) {
		// Returning without finish is what a t.Fatal before it amounts to.
		// Not t.SkipNow: the gateway-postgres job fails on any skip.
		s = sample(t, f, f)
	})
	select {
	case <-s.done:
	case <-time.After(time.Second):
		s.halt()
		t.Fatal("the sampler was still running after its test ended")
	}
}

func waitFor(t *testing.T, within time.Duration, what string, cond func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for !cond() {
		if time.Since(start) > within {
			t.Fatalf("timed out after %s waiting for %s", within, what)
		}
		time.Sleep(2 * time.Millisecond)
	}
	return time.Since(start)
}

func admin(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := pgx.ConnectConfig(ctx, connCfg(t, dsn))
	if err != nil {
		t.Fatalf("admin connection: %v", redact(err))
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// holderPID finds the backend holding key.
func holderPID(t *testing.T, a *pgx.Conn, key int64) (int, bool) {
	t.Helper()
	var pid int
	err := a.QueryRow(context.Background(), `SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND granted
AND classid = $1::bigint::oid AND objid = $2::bigint::oid AND objsubid = 1`,
		int64(uint64(key)>>32), int64(uint64(key)&0xffffffff)).Scan(&pid)
	if err != nil {
		return 0, false
	}
	return pid, true
}

// ownedHolder waits until f owns and returns the backend holding key. The
// fence must own both before and after the lookup, so the backend found is its
// session.
// Under load an owner can fail closed between the two checks; that is a
// liveness gap, so it waits (bounded) instead of failing on "no holder".
func ownedHolder(t *testing.T, adm *pgx.Conn, f *Fence, key int64) int {
	t.Helper()
	var pid int
	waitFor(t, 10*time.Second, "an owner and its server-side holder", func() bool {
		if !f.Owned() {
			return false
		}
		p, ok := holderPID(t, adm, key)
		pid = p
		return ok && f.Owned()
	})
	return pid
}

// requireStillOwns fails the test if f no longer owns just before its session
// is killed: a fail-closed timing loss got there first, so the scenario would
// test a timing handover instead of the one it names.
func requireStillOwns(t *testing.T, f *Fence) {
	t.Helper()
	if !f.Owned() {
		t.Fatal("the owner lost ownership (a fail-closed timing loss) before its session could be killed; the scenario was not exercised")
	}
}

func terminate(t *testing.T, a *pgx.Conn, pid int) {
	t.Helper()
	var ok bool
	if err := a.QueryRow(context.Background(), `SELECT pg_terminate_backend($1)`, pid).Scan(&ok); err != nil || !ok {
		t.Fatalf("pg_terminate_backend(%d) = %v, %v", pid, ok, redact(err))
	}
}

// Of two fences on one key, exactly one owns.
//
// Safety (never two owners) is the sampler's, over the whole run. Liveness is
// asserted with a bound rather than at one instant: under load an owner can
// correctly fail closed and leave a moment with no owner, so the test requires
// exactly one owner again within the bound, and fails on any loss that is not
// such a fail-closed timing loss.
func TestPostgresFence_ExactlyOneOwner(t *testing.T) {
	dsn := testDSN(t)
	key := uniqueKey()
	a, ea := startObserved(t, PgxDialer(connCfg(t, dsn)), key)
	b, eb := startObserved(t, PgxDialer(connCfg(t, dsn)), key)
	s := sample(t, a, b)
	waitFor(t, 10*time.Second, "an owner", func() bool { return a.Owned() || b.Owned() })
	// Long enough for a standby that wrongly took the lock on its next attempt
	// to pass its grace and be counted, so the sampler would see the overlap.
	time.Sleep(steadyCfg.HeartbeatInterval + 2*steadyCfg.Validity())
	waitFor(t, 10*time.Second, "exactly one owner", func() bool { return a.Owned() != b.Owned() })
	s.finish(t)
	s.requireExercised(t)
	requireFailClosedTimingOnly(t, "a", ea, steadyCfg.HeartbeatTimeout)
	requireFailClosedTimingOnly(t, "b", eb, steadyCfg.HeartbeatTimeout)
}

// Why the fence uses a DEDICATED connection: a session lock taken through a
// pool belongs to whichever backend ran it; another pooled connection does not
// hold it, and returning the connection to the pool leaves the lock held by an
// idle backend nobody is checking.
func TestPostgresFence_APooledConnectionDoesNotCarryTheLock(t *testing.T) {
	dsn := testDSN(t)
	key := uniqueKey()
	ctx := context.Background()
	pcfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("pool config does not parse")
	}
	pcfg.MinConns, pcfg.MaxConns = 2, 2
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		t.Fatal(redact(err))
	}
	defer pool.Close()
	c1, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(redact(err))
	}
	c2, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(redact(err))
	}
	s1, s2 := pgSession{conn: c1.Conn()}, pgSession{conn: c2.Conn()}
	if ok, err := s1.TryLock(ctx, key); err != nil || !ok {
		t.Fatalf("TryLock = %v, %v", ok, redact(err))
	}
	if held, err := s2.Holds(ctx, key); err != nil || held {
		t.Errorf("another pooled connection reports holding the lock: %v, %v", held, redact(err))
	}
	c1.Release() // back to the pool — the lock is NOT released with it
	c2.Release()
	a := admin(t, dsn)
	if _, ok := holderPID(t, a, key); !ok {
		t.Error("expected the lock to stay held by an idle pooled backend")
	} else {
		t.Log("confirmed: releasing a pooled connection leaves the session lock held by an idle backend")
	}
}

// errOwnerCutOff is what the old owner's dialer returns once the test has cut
// it off from the database.
var errOwnerCutOff = errors.New("test: the old owner is cut off from the database")

// The owner's database session is killed server-side: the owner fails closed
// at its next confirmation, and the standby becomes owner only after the
// grace. No instant with two owners.
//
// The old owner is cut off from the database just before its session is
// killed: its live session is untouched, but every reconnect is refused. If it
// could reconnect, its next attempt would race the standby's for the freed
// lock, and either could win; both outcomes are safe, but then the handover
// to the standby that this test asserts would depend on timing (a CI failure
// on 63a80a2 timed out waiting for b to own). So the test removes the race,
// not the assertion, and it waits for a refused reconnect to show that the old
// owner did try.
func TestPostgresFence_ATerminatedOwnerSessionHandsOverWithoutOverlap(t *testing.T) {
	dsn := testDSN(t)
	key := uniqueKey()
	var cutOff atomic.Bool
	var refused atomic.Int32
	toDB := PgxDialer(connCfg(t, dsn))
	a := startFenceWith(t, func(ctx context.Context) (Session, error) {
		if cutOff.Load() {
			refused.Add(1)
			return nil, errOwnerCutOff
		}
		return toDB(ctx)
	}, key)
	waitFor(t, 10*time.Second, "a to own", a.Owned)
	// a's session is found while a is the only fence, which narrows (it cannot
	// close) the window in which a fail-closed timing loss under load hands the
	// lock to b before the kill; requireStillOwns turns that case into a
	// visible failure instead of a pass that tested a timing handover.
	adm := admin(t, dsn)
	pid := ownedHolder(t, adm, a, key)
	b := startFence(t, connCfg(t, dsn), key)
	s := sample(t, a, b)
	cutOff.Store(true)
	requireStillOwns(t, a)
	terminate(t, adm, pid)
	lost := waitFor(t, 5*time.Second, "a to lose ownership", func() bool { return !a.Owned() })
	waitFor(t, 5*time.Second, "a to attempt a reconnect", func() bool { return refused.Load() > 0 })
	took := waitFor(t, 10*time.Second, "b to own", b.Owned)
	s.finish(t)
	t.Logf("a lost ownership %s after its session was killed; b owner %s later (validity %s)", lost, took, testCfg.Validity())
	if lost > testCfg.Validity()+100*time.Millisecond {
		t.Errorf("a took %s to fail closed; bound is %s", lost, testCfg.Validity())
	}
}

// proxy forwards TCP to PostgreSQL and can black-hole it.
type proxy struct {
	ln        net.Listener
	target    string
	blackhole atomic.Bool
	mu        sync.Mutex
	upstream  []net.Conn
}

func newProxy(t *testing.T, target string) *proxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{ln: ln, target: target}
	go p.serve()
	t.Cleanup(func() { _ = ln.Close(); p.severUpstream() })
	return p
}

func (p *proxy) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		u, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = c.Close()
			continue
		}
		p.mu.Lock()
		p.upstream = append(p.upstream, u)
		p.mu.Unlock()
		go p.pipe(c, u)
		go p.pipe(u, c)
	}
}

// pipe copies until either side errors. While black-holed, bytes are read and
// dropped and NOTHING is closed onward — the far side never learns.
func (p *proxy) pipe(from, to net.Conn) {
	buf := make([]byte, 32*1024)
	for {
		n, err := from.Read(buf)
		if n > 0 && !p.blackhole.Load() {
			if _, werr := to.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			if !p.blackhole.Load() {
				_ = to.Close()
			}
			return
		}
	}
}

// severUpstream closes the server side: what the server eventually does when
// its own keepalive notices a dead peer.
func (p *proxy) severUpstream() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, u := range p.upstream {
		_ = u.Close()
	}
	p.upstream = nil
}

func viaProxy(t *testing.T, dsn string, p *proxy) *pgx.ConnConfig {
	t.Helper()
	c := connCfg(t, dsn)
	host, port, err := net.SplitHostPort(p.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(port)
	c.Host, c.Port = host, uint16(n)
	c.Fallbacks = nil
	return c
}

func targetAddr(t *testing.T, dsn string) string {
	t.Helper()
	c := connCfg(t, dsn)
	return net.JoinHostPort(c.Host, strconv.Itoa(int(c.Port)))
}

// A network partition between the owner and the database: the owner
// self-fences within the validity window; the server keeps the old session's
// lock, so NO process is owner (safe, but unavailable) until the server drops
// the session; then the standby takes over after the grace.
func TestPostgresFence_APartitionedOwnerSelfFencesAndNobodyOwnsUntilTheServerReleases(t *testing.T) {
	dsn := testDSN(t)
	key := uniqueKey()
	p := newProxy(t, targetAddr(t, dsn))
	a := startFence(t, viaProxy(t, dsn, p), key)
	waitFor(t, 10*time.Second, "a to own", a.Owned)
	b := startFence(t, connCfg(t, dsn), key)
	s := sample(t, a, b)

	p.blackhole.Store(true)
	lost := waitFor(t, 5*time.Second, "a to self-fence", func() bool { return !a.Owned() })
	time.Sleep(time.Second)
	if b.Owned() {
		t.Error("b became owner while the server still held a's lock")
	}
	adm := admin(t, dsn)
	if _, held := holderPID(t, adm, key); !held {
		t.Error("expected the partitioned session to still hold the lock server-side")
	}
	p.severUpstream() // the server finally notices the dead peer
	took := waitFor(t, 10*time.Second, "b to own", b.Owned)
	s.finish(t)
	t.Logf("partitioned owner self-fenced after %s (bound %s); standby owner %s after the server released",
		lost, testCfg.Validity(), took)
}

// The dangerous case: the server releases the lock while the old owner is cut
// off and STILL inside its validity window. The standby gets the lock at once
// but must not claim ownership until the old owner has self-fenced.
func TestPostgresFence_AServerReleaseDuringAPartitionStillNeverOverlaps(t *testing.T) {
	dsn := testDSN(t)
	key := uniqueKey()
	p := newProxy(t, targetAddr(t, dsn))
	a := startFence(t, viaProxy(t, dsn, p), key)
	waitFor(t, 10*time.Second, "a to own", a.Owned)
	adm := admin(t, dsn)
	pid := ownedHolder(t, adm, a, key) // found while a is the only fence
	b := startFence(t, connCfg(t, dsn), key)
	s := sample(t, a, b)
	p.blackhole.Store(true)
	requireStillOwns(t, a)
	terminate(t, adm, pid) // the server releases immediately; a cannot know
	waitFor(t, 10*time.Second, "b to own", b.Owned)
	s.finish(t)
	if a.Owned() {
		t.Error("a still claims ownership after b became owner")
	}
}

func TestPostgresFence_ShutdownHandsOver(t *testing.T) {
	dsn := testDSN(t)
	key := uniqueKey()
	a := startFence(t, connCfg(t, dsn), key)
	waitFor(t, 10*time.Second, "a to own", a.Owned)
	b := startFence(t, connCfg(t, dsn), key)
	s := sample(t, a, b)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "b to own", b.Owned)
	s.finish(t)
}
