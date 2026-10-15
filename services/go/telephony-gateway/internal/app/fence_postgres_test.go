package app

import (
	"context"
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
	svc, h, _ := newService(t)
	m := full(t, dsn)
	m[prefix+"_FENCE_LOCK_KEY"] = strconv.FormatInt(key, 10)
	f, err := WireFence(svc, h, prefix, env(m), quiet)
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

// watchReadiness fails the test if both gateways are ever ready together.
func watchReadiness(t *testing.T, a, b *gw) func() {
	t.Helper()
	var both, samples atomic.Int64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if a.ready() && b.ready() {
				both.Add(1)
			}
			samples.Add(1)
			time.Sleep(2 * time.Millisecond)
		}
	}()
	return func() {
		close(stop)
		<-done
		if both.Load() != 0 {
			t.Errorf("BOTH gateways reported ready at %d of %d samples", both.Load(), samples.Load())
		}
		t.Logf("never both ready across %d samples", samples.Load())
	}
}

func key() int64 { return time.Now().UnixNano() & 0x7fffffffffff }

func TestPostgresGateway_ExactlyOneInstanceIsReady(t *testing.T) {
	dsn := pgDSN(t)
	k := key()
	a, b := startGateway(t, dsn, k), startGateway(t, dsn, k)
	done := watchReadiness(t, a, b)
	waitUntil(t, 10*time.Second, "one ready instance", func() bool { return a.ready() || b.ready() })
	time.Sleep(time.Second)
	done()
	if a.ready() == b.ready() {
		t.Errorf("ready: a=%v b=%v; want exactly one", a.ready(), b.ready())
	}
}

// Lock loss: the ready instance's session is killed; it becomes NOT ready at
// once (stops accepting work) and the other becomes ready after the grace.
func TestPostgresGateway_LockLossTakesTheInstanceOutOfService(t *testing.T) {
	dsn := pgDSN(t)
	k := key()
	a := startGateway(t, dsn, k)
	waitUntil(t, 10*time.Second, "a ready", a.ready)
	b := startGateway(t, dsn, k)
	done := watchReadiness(t, a, b)

	ctx := context.Background()
	adm, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("admin connection failed")
	}
	defer adm.Close(ctx)
	var pid int
	if err := adm.QueryRow(ctx, `SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND granted
AND classid = $1::bigint::oid AND objid = $2::bigint::oid AND objsubid = 1`,
		int64(uint64(k)>>32), int64(uint64(k)&0xffffffff)).Scan(&pid); err != nil {
		t.Fatal("holder not found")
	}
	if _, err := adm.Exec(ctx, `SELECT pg_terminate_backend($1)`, pid); err != nil {
		t.Fatal("terminate failed")
	}
	waitUntil(t, 5*time.Second, "a out of service", func() bool { return !a.ready() })
	waitUntil(t, 10*time.Second, "b ready", b.ready)
	done()
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
