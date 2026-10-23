package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/metricsexport"
	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/services/go/telephony-gateway/internal/fence"
)

// scrape renders m's registry exactly as the service's /metrics does.
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	metricsexport.New(metricsexport.Source{Subsystem: "telephony-gateway", Registry: m.Registry()}).
		Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics = %d", rec.Code)
	}
	return rec.Body.String()
}

// hasSample reports whether the exposition has a sample line for series with
// exactly value.
func hasSample(body, series string, value float64) bool {
	want := fmt.Sprintf("%s %g", series, value)
	for _, line := range strings.Split(body, "\n") {
		if line == want {
			return true
		}
	}
	return false
}

func checksSeries(result string) string {
	return fmt.Sprintf(`%s{result=%q}`, MetricFenceChecks, result)
}

type stubChecker struct {
	name string
	err  error
}

func (s stubChecker) Name() string                { return s.name }
func (s stubChecker) Check(context.Context) error { return s.err }

func TestMetrics_ClassifiesEveryOutcome(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		err       error
		result    string
		wantOwned float64
	}{
		{"owned", nil, ResultOwned, 1},
		{"not owner", fence.ErrNotOwner, ResultNotOwner, 0},
		{"not configured (wrapped)", fmt.Errorf("%w: not configured; set X", fence.ErrNotOwner), ResultNotOwner, 0},
		{"credentials unavailable", fence.ErrCredentialsUnavailable, ResultError, 0},
		{"other", errors.New("boom"), ResultError, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := NewMetrics()
			m.owned.Set(0.5) // a sentinel, so the assertion proves the gauge was written
			m.observeFenceCheck(tc.err)
			if got := m.checks.Value(tc.result); got != 1 {
				t.Errorf("%s = %d, want 1", checksSeries(tc.result), got)
			}
			if got := m.owned.Value(); got != tc.wantOwned {
				t.Errorf("%s = %v, want %v", MetricFenceOwned, got, tc.wantOwned)
			}
		})
	}
}

// The wrapper is transparent: /readyz reports the same name and the same
// error, so wrapping changes what is recorded, never what readiness decides.
func TestObservedChecker_PassesThroughNameAndError(t *testing.T) {
	t.Parallel()
	inner := stubChecker{name: fence.Name, err: fmt.Errorf("wrapped: %w", fence.ErrNotOwner)}
	c := observe(inner, NewMetrics())
	if c.Name() != fence.Name {
		t.Errorf("Name = %q, want %q", c.Name(), fence.Name)
	}
	if err := c.Check(context.Background()); err != inner.err { //nolint:errorlint // identity is the property under test
		t.Errorf("Check = %v, want the inner error itself", err)
	}
}

func TestObserve_NilMetricsLeavesTheCheckerUnchanged(t *testing.T) {
	t.Parallel()
	inner := stubChecker{name: "x"}
	if got := observe(inner, nil); got != platform.Checker(inner) {
		t.Errorf("observe(c, nil) = %#v, want c itself", got)
	}
}

// Probes overlap (the kubelet and a scraper, or two kubelet probes), so every
// concurrent evaluation must be counted exactly once.
func TestMetrics_ConcurrentEvaluationsAreAllCounted(t *testing.T) {
	t.Parallel()
	const workers, each = 16, 250
	m := NewMetrics()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		var err error
		if w%2 == 1 {
			err = fence.ErrNotOwner
		}
		c := observe(stubChecker{name: fence.Name, err: err}, m)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				_ = c.Check(context.Background())
			}
		}()
	}
	wg.Wait()
	if got := m.checks.Value(ResultOwned); got != workers/2*each {
		t.Errorf("owned = %d, want %d", got, workers/2*each)
	}
	if got := m.checks.Value(ResultNotOwner); got != workers/2*each {
		t.Errorf("not_owner = %d, want %d", got, workers/2*each)
	}
}

// Re-registering a name returns the existing instrument (metrics.Registry),
// so a second registration cannot reset a live counter; separate Metrics are
// separate registries.
func TestMetrics_DuplicateRegistrationReturnsTheLiveInstruments(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.observeFenceCheck(nil)
	if again := m.Registry().Counter(MetricFenceChecks, "result"); again != m.checks || again.Value(ResultOwned) != 1 {
		t.Error("re-registering the counter did not return the live instrument")
	}
	if again := m.Registry().Gauge(MetricFenceOwned); again != m.owned {
		t.Error("re-registering the gauge did not return the live instrument")
	}
	if other := NewMetrics(); other.checks.Value(ResultOwned) != 0 {
		t.Error("a second Metrics shares state with the first")
	}
}

// Real readiness probes update the exposition: each /readyz evaluation of the
// unconfigured fence is one not_owner sample.
func TestWireFenceWithMetrics_ReadinessProbesUpdateTheExposition(t *testing.T) {
	t.Parallel()
	svc, h, _ := newService(t)
	m := NewMetrics()
	if f, err := WireFenceWithMetrics(svc, h, prefix, env(nil), quiet, m); err != nil || f != nil {
		t.Fatalf("= %v, %v", f, err)
	}
	h.MarkReady()

	// Before any probe a counter has no series at all; the gauge reads 0.
	body := scrape(t, m)
	if strings.Contains(body, MetricFenceChecks+"{") {
		t.Errorf("series before any probe:\n%s", body)
	}
	for i := 1; i <= 3; i++ {
		if code, _ := readiness(h); code != http.StatusServiceUnavailable {
			t.Fatalf("readyz = %d, want 503", code)
		}
		body = scrape(t, m)
		if !hasSample(body, checksSeries(ResultNotOwner), float64(i)) {
			t.Fatalf("after %d probes, no %s %d:\n%s", i, checksSeries(ResultNotOwner), i, body)
		}
	}
	if !hasSample(body, MetricFenceOwned, 0) {
		t.Errorf("no %s 0:\n%s", MetricFenceOwned, body)
	}
}

// The configured fence's readiness name and verdict are unchanged by the
// wrapper, and its evaluations are recorded.
func TestWireFenceWithMetrics_ConfiguredFenceIsObservedUnderItsOwnName(t *testing.T) {
	t.Parallel()
	svc, h, _ := newService(t)
	m := NewMetrics()
	f, err := WireFenceWithMetrics(svc, h, prefix, env(full(t, unreachable(t))), quiet, m)
	if err != nil || f == nil {
		t.Fatalf("= %v, %v", f, err)
	}
	h.MarkReady()
	code, body := readiness(h)
	if code != http.StatusServiceUnavailable || !strings.Contains(body, fence.Name) {
		t.Fatalf("readyz = %d:\n%s", code, body)
	}
	if got := m.checks.Value(ResultNotOwner); got != 1 {
		t.Errorf("not_owner = %d, want 1", got)
	}
}

// During shutdown /readyz answers without running checks, so nothing is
// recorded: the metric describes evaluations, not probe traffic.
func TestWireFenceWithMetrics_ShutdownRecordsNothing(t *testing.T) {
	t.Parallel()
	svc, h, _ := newService(t)
	m := NewMetrics()
	if _, err := WireFenceWithMetrics(svc, h, prefix, env(nil), quiet, m); err != nil {
		t.Fatal(err)
	}
	h.MarkReady()
	readiness(h)
	h.MarkShuttingDown()
	readiness(h)
	readiness(h)
	if got := m.checks.Value(ResultNotOwner); got != 1 {
		t.Errorf("not_owner = %d, want 1 (only the pre-shutdown probe)", got)
	}
}

// fakeSession is a primary that always grants and confirms the lock, so a real
// fence.Fence becomes owner without a database.
type fakeSession struct{ closed atomic.Bool }

func (*fakeSession) Primary(context.Context) (bool, error)        { return true, nil }
func (*fakeSession) TryLock(context.Context, int64) (bool, error) { return true, nil }
func (s *fakeSession) Holds(context.Context, int64) (bool, error) { return !s.closed.Load(), nil }
func (*fakeSession) Unlock(context.Context, int64) error          { return nil }
func (s *fakeSession) Close(context.Context) error                { s.closed.Store(true); return nil }

// The whole path with a real fence: it acquires ownership, probes record
// "owned" and the gauge reads 1; after shutdown it no longer owns, and the
// next evaluation records not_owner and the gauge returns to 0.
func TestMetrics_RealFenceOwnershipIsObservedThenLost(t *testing.T) {
	t.Parallel()
	cfg := fence.Config{LockKey: 8813, HeartbeatInterval: 100 * time.Millisecond, HeartbeatTimeout: 50 * time.Millisecond}
	f, err := fence.New(cfg, func(context.Context) (fence.Session, error) { return &fakeSession{}, nil }, quiet)
	if err != nil {
		t.Fatal(err)
	}
	m := NewMetrics()
	c := observe(f, m)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for c.Check(context.Background()) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the fence never became owner")
		}
		time.Sleep(10 * time.Millisecond)
	}
	body := scrape(t, m)
	if !hasSample(body, checksSeries(ResultOwned), 1) || !hasSample(body, MetricFenceOwned, 1) {
		t.Fatalf("ownership not exposed:\n%s", body)
	}

	cancel()
	if err := f.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if err := c.Check(context.Background()); !errors.Is(err, fence.ErrNotOwner) {
		t.Fatalf("after shutdown Check = %v, want ErrNotOwner", err)
	}
	if body = scrape(t, m); !hasSample(body, MetricFenceOwned, 0) {
		t.Errorf("gauge not back to 0 after the loss:\n%s", body)
	}
}
