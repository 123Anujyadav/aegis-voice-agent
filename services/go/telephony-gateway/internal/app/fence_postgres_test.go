package app

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/services/go/telephony-gateway/internal/fence"
	"github.com/jackc/pgx/v5"
)

// fenceTestDSNEnv names a DISPOSABLE test database (same variable as the
// fence package's real-PostgreSQL tests).
const fenceTestDSNEnv = "CS_TELEPHONY_GATEWAY_FENCE_TEST_DSN"

// P1-30B — two gateway SERVICES (platform lifecycle + fence + readiness),
// in-process, against a REAL disposable PostgreSQL. SKIP unless
// fenceTestDSNEnv is set. Touches only advisory locks. Timing values are
// TEST values, not proposals (D10).

type gw struct {
	svc    *platform.Service
	health *platform.Health
	fence  *fence.Fence
	cancel context.CancelFunc
	done   chan error
	once   sync.Once
}

func startGateway(t *testing.T, dsn string, key int64) *gw {
	t.Helper()
	return startGatewayWith(t, dsn, key, nil, quiet)
}

// The steady-state tests' heartbeat. The timeout also bounds every dial, and
// under -race a single SCRAM login was measured at 65-115ms, so with full's
// 100ms a standby can fail to connect at all; these tests assume the timeout
// comfortably exceeds one login. TEST values, not proposals (D10).
const steadyInterval, steadyTimeout = time.Second, 500 * time.Millisecond

var steadyTiming = map[string]string{
	prefix + "_FENCE_HEARTBEAT_INTERVAL": steadyInterval.String(),
	prefix + "_FENCE_HEARTBEAT_TIMEOUT":  steadyTimeout.String(),
}

// startSteady starts a gateway with steadyTiming whose fence losses are kept,
// so the test can classify any it suffers; they are logged if the test fails.
func startSteady(t *testing.T, dsn string, key int64) (*gw, *losses) {
	t.Helper()
	l := &losses{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("fence records: %v", l.records())
		}
	})
	return startGatewayWith(t, dsn, key, steadyTiming, slog.New(l)), l
}

func startGatewayWith(t *testing.T, dsn string, key int64, extra map[string]string, log *slog.Logger) *gw {
	t.Helper()
	svc, h, _ := newService(t)
	m := full(t, dsn)
	m[prefix+"_FENCE_LOCK_KEY"] = strconv.FormatInt(key, 10)
	for k, v := range extra {
		m[k] = v
	}
	f, err := WireFence(svc, h, prefix, env(m), log)
	if err != nil || f == nil {
		t.Fatalf("WireFence = %v, %v", f, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	g := &gw{svc: svc, health: h, fence: f, cancel: cancel, done: make(chan error, 1)}
	go func() { g.done <- svc.Run(ctx) }()
	t.Cleanup(func() { g.stop(t) })
	return g
}

func (g *gw) ready() bool {
	code, _ := readiness(g.health)
	return code == http.StatusOK
}

// stop shuts the gateway down once; later calls (t.Cleanup) are no-ops.
func (g *gw) stop(t *testing.T) {
	t.Helper()
	g.once.Do(func() {
		g.cancel()
		select {
		case <-g.done:
		case <-time.After(15 * time.Second):
			t.Error("gateway did not shut down")
		}
	})
}

func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(fenceTestDSNEnv))
	if dsn == "" {
		t.Skipf("%s is not set; skipping (a SKIP, not a PASS)", fenceTestDSNEnv)
	}
	return dsn
}

func waitUntil(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// watchReadiness fails the test if both gateways are ever ready together. The
// function it returns stops it and reports how many samples saw one ready, so
// a test can require that the check actually saw someone in service. If the
// test ends before calling it (a timed-out wait is a t.Fatal), the watcher is
// stopped when the test ends and any overlap it saw is still reported.
func watchReadiness(t *testing.T, a, b *gw) func() int64 {
	t.Helper()
	var both, samples, served atomic.Int64
	var halted sync.Once
	var finished atomic.Bool
	stop := make(chan struct{})
	done := make(chan struct{})
	halt := func() {
		halted.Do(func() { close(stop) })
		<-done
	}
	report := func() {
		if both.Load() != 0 {
			t.Errorf("BOTH gateways reported ready at %d of %d samples", both.Load(), samples.Load())
		}
	}
	t.Cleanup(func() {
		halt()
		if !finished.Load() {
			report()
		}
	})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			ra, rb := a.ready(), b.ready()
			if ra && rb {
				both.Add(1)
			}
			if ra || rb {
				served.Add(1)
			}
			samples.Add(1)
			time.Sleep(2 * time.Millisecond)
		}
	}()
	return func() int64 {
		finished.Store(true)
		halt()
		report()
		t.Logf("never both ready across %d samples (%d with one ready)", samples.Load(), served.Load())
		return served.Load()
	}
}

func key() int64 { return time.Now().UnixNano() & 0x7fffffffffff }

// requireExercised fails the test if the readiness sampler never saw a gateway
// in service: its overlap check would then have checked nothing.
func requireExercised(t *testing.T, served int64) {
	t.Helper()
	if served == 0 {
		t.Error("no gateway was ready at any sample; the overlap check was not exercised")
	}
}

// holder returns the backend holding key's advisory lock, if any.
func holder(t *testing.T, adm *pgx.Conn, k int64) (int, bool) {
	t.Helper()
	var pid int
	err := adm.QueryRow(context.Background(), `SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND granted
AND classid = $1::bigint::oid AND objid = $2::bigint::oid AND objsubid = 1`,
		int64(uint64(k)>>32), int64(uint64(k)&0xffffffff)).Scan(&pid)
	return pid, err == nil
}

// Of two gateways on one key, exactly one is ready.
//
// Safety (never both ready) is watchReadiness's, over the whole run. Liveness
// is asserted with a bound rather than at one instant: under load the ready
// instance can correctly fail closed and leave a moment with neither ready, so
// the test requires exactly one ready again within the bound, and fails on any
// loss that is not such a fail-closed timing loss.
func TestPostgresGateway_ExactlyOneInstanceIsReady(t *testing.T) {
	dsn := pgDSN(t)
	k := key()
	a, la := startSteady(t, dsn, k)
	b, lb := startSteady(t, dsn, k)
	done := watchReadiness(t, a, b)
	waitUntil(t, 10*time.Second, "one ready instance", func() bool { return a.ready() || b.ready() })
	// Long enough for a standby that wrongly took the lock on its next attempt
	// to pass its grace and report ready, so the sampler would see the overlap.
	time.Sleep(steadyInterval + 2*(steadyInterval+steadyTimeout))
	waitUntil(t, 10*time.Second, "exactly one ready instance", func() bool { return a.ready() != b.ready() })
	requireExercised(t, done())
	requireFailClosedTimingOnly(t, "a", la.all(), steadyTimeout)
	requireFailClosedTimingOnly(t, "b", lb.all(), steadyTimeout)
}

// Lock loss: the ready instance's session is killed; it becomes NOT ready at
// once (stops accepting work) and the other becomes ready after the grace.
//
// The instance killed is whichever is in service when the session is looked
// up (under load a fail-closed timing loss can already have handed over). Once
// it is out of service it is stopped: the free lock could otherwise be won back
// by its own re-dial, which is safe but would make the handover asserted here
// depend on timing (#37 removed the same race from the fence's own test).
func TestPostgresGateway_LockLossTakesTheInstanceOutOfService(t *testing.T) {
	dsn := pgDSN(t)
	k := key()
	a, la := startSteady(t, dsn, k)
	waitUntil(t, 10*time.Second, "a ready", a.ready)
	b, lb := startSteady(t, dsn, k)
	done := watchReadiness(t, a, b)

	ctx := context.Background()
	adm, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("admin connection failed")
	}
	defer adm.Close(ctx)
	var (
		owner, standby   *gw
		ownerL, standbyL *losses
		pid              int
	)
	waitUntil(t, 10*time.Second, "an instance in service and its server-side holder", func() bool {
		switch {
		case a.ready() && !b.ready():
			owner, ownerL, standby, standbyL = a, la, b, lb
		case b.ready() && !a.ready():
			owner, ownerL, standby, standbyL = b, lb, a, la
		default:
			return false
		}
		p, ok := holder(t, adm, k)
		pid = p
		return ok && owner.ready()
	})
	killedAt := len(ownerL.all())
	var killed bool
	if err := adm.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&killed); err != nil || !killed {
		t.Fatalf("terminate failed (killed=%v): the session found was no longer alive", killed)
	}
	waitUntil(t, 5*time.Second, "the killed instance out of service", func() bool { return !owner.ready() })
	owner.stop(t)
	waitUntil(t, 10*time.Second, "the other instance ready", standby.ready)
	requireExercised(t, done())
	// Before the kill only fail-closed timing losses are excused. After it the
	// killed instance must record a loss (the one the test caused, its first);
	// any further one must again be a timing loss.
	before, after := ownerL.all()[:killedAt], ownerL.all()[killedAt:]
	requireFailClosedTimingOnly(t, "the killed instance before the kill", before, steadyTimeout)
	if len(after) == 0 {
		t.Error("killing the session recorded no loss on the instance that held it")
	} else {
		requireFailClosedTimingOnly(t, "the killed instance after its kill", after[1:], steadyTimeout)
	}
	requireFailClosedTimingOnly(t, "the other instance", standbyL.all(), steadyTimeout)
}

// Graceful shutdown of the ready instance releases the fence; the other takes
// over.
func TestPostgresGateway_ShutdownReleasesTheFence(t *testing.T) {
	dsn := pgDSN(t)
	k := key()
	a := startGateway(t, dsn, k)
	waitUntil(t, 10*time.Second, "a ready", a.ready)
	b := startGateway(t, dsn, k)
	done := watchReadiness(t, a, b)
	a.stop(t)
	if a.fence.Owned() {
		t.Error("a still owns the fence after shutdown")
	}
	waitUntil(t, 10*time.Second, "b ready", b.ready)
	done()
}
