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
	c := testCfg
	c.LockKey = key
	f, err := New(c, PgxDialer(cc), slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	t.Logf("no overlap in %d samples", s.samples.Load())
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

func terminate(t *testing.T, a *pgx.Conn, pid int) {
	t.Helper()
	var ok bool
	if err := a.QueryRow(context.Background(), `SELECT pg_terminate_backend($1)`, pid).Scan(&ok); err != nil || !ok {
		t.Fatalf("pg_terminate_backend(%d) = %v, %v", pid, ok, redact(err))
	}
}

func TestPostgresFence_ExactlyOneOwner(t *testing.T) {
	dsn := testDSN(t)
	key := uniqueKey()
	a, b := startFence(t, connCfg(t, dsn), key), startFence(t, connCfg(t, dsn), key)
	s := sample(t, a, b)
	waitFor(t, 10*time.Second, "an owner", func() bool { return a.Owned() || b.Owned() })
	time.Sleep(time.Second)
	s.finish(t)
	if a.Owned() == b.Owned() {
		t.Errorf("owners: a=%v b=%v; want exactly one", a.Owned(), b.Owned())
	}
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

// The owner's database session is killed server-side: the owner fails closed
// at its next confirmation, and the standby becomes owner only after the
// grace. No instant with two owners.
func TestPostgresFence_ATerminatedOwnerSessionHandsOverWithoutOverlap(t *testing.T) {
	dsn := testDSN(t)
	key := uniqueKey()
	a := startFence(t, connCfg(t, dsn), key)
	waitFor(t, 10*time.Second, "a to own", a.Owned)
	b := startFence(t, connCfg(t, dsn), key)
	s := sample(t, a, b)
	adm := admin(t, dsn)
	pid, ok := holderPID(t, adm, key)
	if !ok {
		t.Fatal("no holder found")
	}
	terminate(t, adm, pid)
	lost := waitFor(t, 5*time.Second, "a to lose ownership", func() bool { return !a.Owned() })
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
	b := startFence(t, connCfg(t, dsn), key)
	adm := admin(t, dsn)
	pid, ok := holderPID(t, adm, key)
	if !ok {
		t.Fatal("no holder")
	}
	s := sample(t, a, b)
	p.blackhole.Store(true)
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
