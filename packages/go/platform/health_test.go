package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// health.go carries the drain-before-terminate contract that ADR-0004 §9 and
// ARCHITECTURE_FREEZE invariant I6 depend on — "Services drain before
// terminating — readiness false, then close" — and had no test file at all.
//
// Every assertion below is taken from behaviour the source already states. None
// of it invents a policy: the separation of liveness from readiness, the
// critical/non-critical split, the shutdown short-circuit and the no-store
// header are each documented in health.go's own comments.

func testHealth(t *testing.T) *Health {
	t.Helper()
	return NewHealth(ServiceConfig{Name: "edge-api", Version: "v1.2.3"})
}

// probe runs a handler and returns the recorder.
func probe(t *testing.T, h http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/probe", http.NoBody))
	return rec
}

func decodeReadiness(t *testing.T, rec *httptest.ResponseRecorder) readinessResponse {
	t.Helper()
	var got readinessResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding readiness body: %v", err)
	}
	return got
}

// ---------------------------------------------------------------------------
// Liveness
// ---------------------------------------------------------------------------

// TestLiveness_IsIndependentOfReadinessAndDependencies confirms liveness
// answers 200 without running any registered dependency check.
//
// From health.go: liveness "answers purely from process-local state and never
// consults a dependency … if the database is down and liveness checks it, every
// replica restarts in a loop, turning a database incident into a total outage".
//
// The registered check here fails and records whether it ran. Liveness must
// still answer 200 and must never invoke it.
func TestLiveness_IsIndependentOfReadinessAndDependencies(t *testing.T) {
	t.Parallel()

	h := testHealth(t)
	var invoked bool
	h.Register(CheckerFunc{
		CheckName: "database",
		Fn: func(context.Context) error {
			invoked = true
			return errors.New("database is down")
		},
	}, true, time.Second)

	// Not ready, and a critical dependency is failing.
	rec := probe(t, h.LivenessHandler())

	if rec.Code != http.StatusOK {
		t.Errorf("liveness = %d, want 200; a failing dependency must not restart the process", rec.Code)
	}
	if invoked {
		t.Error("liveness invoked a dependency check; that turns a dependency " +
			"outage into a cluster-wide restart loop")
	}

	var got livenessResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding liveness body: %v", err)
	}
	if got.Status != StatusHealthy {
		t.Errorf("status = %q, want %q", got.Status, StatusHealthy)
	}
	if got.Service != "edge-api" || got.Version != "v1.2.3" {
		t.Errorf("service/version = %q/%q, want edge-api/v1.2.3", got.Service, got.Version)
	}
}

// TestLiveness_StaysHealthyDuringShutdown confirms liveness still answers 200
// once shutdown has begun.
//
// Readiness must fail during drain so the balancer stops sending traffic, but
// liveness must NOT — a 503 from liveness would have the orchestrator kill the
// pod mid-drain, which is precisely what the drain window exists to prevent.
func TestLiveness_StaysHealthyDuringShutdown(t *testing.T) {
	t.Parallel()

	h := testHealth(t)
	h.MarkReady()
	h.MarkShuttingDown()

	if rec := probe(t, h.LivenessHandler()); rec.Code != http.StatusOK {
		t.Errorf("liveness during shutdown = %d, want 200; killing a draining "+
			"pod drops the calls the drain exists to protect", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Readiness lifecycle
// ---------------------------------------------------------------------------

// TestReadiness_NotReadyUntilMarkReady confirms readiness reports not-ready
// until MarkReady is called.
//
// NewHealth: "The service begins NOT ready … so that an instance cannot receive
// traffic during startup".
func TestReadiness_NotReadyUntilMarkReady(t *testing.T) {
	t.Parallel()

	h := testHealth(t)

	rec := probe(t, h.ReadinessHandler())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness before MarkReady = %d, want 503; traffic would "+
			"arrive before initialisation completed", rec.Code)
	}
	if got := decodeReadiness(t, rec); got.Status != StatusUnhealthy {
		t.Errorf("status = %q, want %q", got.Status, StatusUnhealthy)
	}

	h.MarkReady()

	rec = probe(t, h.ReadinessHandler())
	if rec.Code != http.StatusOK {
		t.Errorf("readiness after MarkReady = %d, want 200", rec.Code)
	}
	if got := decodeReadiness(t, rec); got.Status != StatusHealthy {
		t.Errorf("status = %q, want %q", got.Status, StatusHealthy)
	}
}

// TestReadiness_ShutdownFailsImmediatelyWithoutRunningChecks confirms
// readiness fails during shutdown without invoking any registered check.
//
// This is the invariant I6 path. From health.go: "During shutdown, report not-ready
// immediately without running checks. The answer cannot change and running them
// wastes time in the drain window".
func TestReadiness_ShutdownFailsImmediatelyWithoutRunningChecks(t *testing.T) {
	t.Parallel()

	h := testHealth(t)
	h.MarkReady()

	var ran bool
	h.Register(CheckerFunc{
		CheckName: "cache",
		Fn: func(context.Context) error {
			ran = true
			return nil
		},
	}, false, time.Second)

	h.MarkShuttingDown()

	rec := probe(t, h.ReadinessHandler())
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readiness during shutdown = %d, want 503; the balancer would "+
			"keep sending traffic into a draining instance", rec.Code)
	}
	if ran {
		t.Error("dependency checks ran during shutdown; the answer cannot " +
			"change and this spends the drain window")
	}
	if got := decodeReadiness(t, rec); len(got.Checks) != 0 {
		t.Errorf("Checks = %v during shutdown, want empty", got.Checks)
	}
}

// TestReadiness_MarkShuttingDownClearsReady confirms the two flags are not
// independent: a service cannot be simultaneously shutting down and ready.
func TestReadiness_MarkShuttingDownClearsReady(t *testing.T) {
	t.Parallel()

	h := testHealth(t)
	h.MarkReady()
	h.MarkShuttingDown()

	h.mu.RLock()
	ready, shutdown := h.ready, h.shutdown
	h.mu.RUnlock()

	if ready {
		t.Error("ready remained true after MarkShuttingDown")
	}
	if !shutdown {
		t.Error("shutdown flag not set")
	}
}

// ---------------------------------------------------------------------------
// Critical vs non-critical
// ---------------------------------------------------------------------------

// TestReadiness_CriticalFailureRemovesFromLoadBalancing confirms a failing
// critical check makes readiness answer 503 with status unhealthy.
//
// Register: "a critical dependency failing makes the service unhealthy and
// removes it from load balancing, whereas a non-critical failure reports
// degraded and keeps traffic flowing".
func TestReadiness_CriticalFailureRemovesFromLoadBalancing(t *testing.T) {
	t.Parallel()

	h := testHealth(t)
	h.MarkReady()
	h.Register(CheckerFunc{
		CheckName: "database",
		Fn:        func(context.Context) error { return errors.New("connection refused") },
	}, true, time.Second)

	rec := probe(t, h.ReadinessHandler())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness with a failing CRITICAL check = %d, want 503", rec.Code)
	}

	got := decodeReadiness(t, rec)
	if got.Status != StatusUnhealthy {
		t.Errorf("status = %q, want %q", got.Status, StatusUnhealthy)
	}
	if len(got.Checks) != 1 {
		t.Fatalf("Checks = %d, want 1", len(got.Checks))
	}
	c := got.Checks[0]
	if c.Name != "database" || !c.Critical || c.Status != StatusUnhealthy {
		t.Errorf("check = %+v; want name=database critical=true status=%s", c, StatusUnhealthy)
	}
	if c.Message == "" {
		t.Error("failing check reported no message; the cause is invisible in the probe body")
	}
}

// TestReadiness_NonCriticalFailureKeepsTrafficFlowing is the other half of the
// same sentence, and the more easily broken one: a degraded dependency must not
// take the instance out of service.
func TestReadiness_NonCriticalFailureKeepsTrafficFlowing(t *testing.T) {
	t.Parallel()

	h := testHealth(t)
	h.MarkReady()
	h.Register(CheckerFunc{
		CheckName: "recommendation-cache",
		Fn:        func(context.Context) error { return errors.New("cache miss storm") },
	}, false, time.Second)

	rec := probe(t, h.ReadinessHandler())
	if rec.Code != http.StatusOK {
		t.Fatalf("readiness with a failing NON-critical check = %d, want 200; "+
			"a degraded dependency must not remove the instance from service", rec.Code)
	}
	if got := decodeReadiness(t, rec); got.Status != StatusDegraded {
		t.Errorf("status = %q, want %q", got.Status, StatusDegraded)
	}
}

// TestReadiness_CriticalFailureOutranksDegraded pins the precedence when both
// kinds fail at once.
func TestReadiness_CriticalFailureOutranksDegraded(t *testing.T) {
	t.Parallel()

	h := testHealth(t)
	h.MarkReady()
	h.Register(CheckerFunc{
		CheckName: "cache",
		Fn:        func(context.Context) error { return errors.New("degraded") },
	}, false, time.Second)
	h.Register(CheckerFunc{
		CheckName: "database",
		Fn:        func(context.Context) error { return errors.New("down") },
	}, true, time.Second)

	rec := probe(t, h.ReadinessHandler())
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readiness = %d, want 503; a critical failure outranks a degraded one", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Check execution
// ---------------------------------------------------------------------------

// TestReadiness_ChecksRunConcurrently confirms registered checks run in
// parallel rather than one after another.
//
// From runChecks: "Checks run in parallel rather than in sequence because probe
// latency is bounded by the orchestrator; ten sequential 200 ms checks exceed a
// typical one-second probe timeout, while ten concurrent ones do not".
//
// Asserted with a rendezvous rather than a stopwatch: both checks must be
// running at once for either to proceed. Sequential execution cannot satisfy
// that, and fails on the bounded wait rather than on a timing threshold.
func TestReadiness_ChecksRunConcurrently(t *testing.T) {
	t.Parallel()

	h := testHealth(t)
	h.MarkReady()

	var wg sync.WaitGroup
	wg.Add(2)
	arrived := make(chan struct{})
	go func() { wg.Wait(); close(arrived) }()

	rendezvous := func(context.Context) error {
		wg.Done()
		select {
		case <-arrived:
			return nil
		case <-time.After(5 * time.Second):
			return errors.New("peer check never started: execution was sequential")
		}
	}

	h.Register(CheckerFunc{CheckName: "a", Fn: rendezvous}, true, 10*time.Second)
	h.Register(CheckerFunc{CheckName: "b", Fn: rendezvous}, true, 10*time.Second)

	rec := probe(t, h.ReadinessHandler())
	if rec.Code != http.StatusOK {
		t.Fatalf("readiness = %d, want 200; checks did not overlap: %s", rec.Code, rec.Body.String())
	}
}

// TestReadiness_SlowCheckIsBoundedByItsOwnTimeout confirms a check that
// outlives its timeout is reported unhealthy instead of hanging the probe.
//
// From runChecks: "Each check receives its own timeout-bounded context so that one
// slow dependency cannot consume the whole probe budget." The check reports the
// context error, so a hung dependency surfaces as an unhealthy check rather
// than as a hung probe.
func TestReadiness_SlowCheckIsBoundedByItsOwnTimeout(t *testing.T) {
	t.Parallel()

	h := testHealth(t)
	h.MarkReady()
	h.Register(CheckerFunc{
		CheckName: "slow-dependency",
		Fn: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}, true, 20*time.Millisecond)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- probe(t, h.ReadinessHandler()) }()

	select {
	case rec := <-done:
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("readiness = %d, want 503 for a timed-out critical check", rec.Code)
		}
		got := decodeReadiness(t, rec)
		if len(got.Checks) != 1 || got.Checks[0].Status != StatusUnhealthy {
			t.Errorf("checks = %+v; want one unhealthy result", got.Checks)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("probe did not return; a slow dependency consumed the whole probe budget")
	}
}

// TestReadiness_NoChecksRegisteredIsReady confirms the zero-dependency case
// returns an empty slice rather than null, so a consumer parsing the body does
// not have to special-case it.
func TestReadiness_NoChecksRegisteredIsReady(t *testing.T) {
	t.Parallel()

	h := testHealth(t)
	h.MarkReady()

	rec := probe(t, h.ReadinessHandler())
	if rec.Code != http.StatusOK {
		t.Fatalf("readiness = %d, want 200", rec.Code)
	}
	if got := decodeReadiness(t, rec); got.Checks == nil {
		t.Error("Checks is null; it should serialise as an empty array")
	}
}

// ---------------------------------------------------------------------------
// Response headers
// ---------------------------------------------------------------------------

// TestProbes_AreNotCacheable confirms both probes forbid caching and answer
// JSON.
//
// From writeJSON: "an intermediary caching a healthy response would mask a
// subsequent failure, which is the worst possible failure mode for a health
// endpoint".
func TestProbes_AreNotCacheable(t *testing.T) {
	t.Parallel()

	h := testHealth(t)
	h.MarkReady()

	for name, handler := range map[string]http.HandlerFunc{
		"liveness":  h.LivenessHandler(),
		"readiness": h.ReadinessHandler(),
	} {
		rec := probe(t, handler)
		if got := rec.Header().Get("Cache-Control"); got != "no-store, no-cache, must-revalidate" {
			t.Errorf("%s Cache-Control = %q; a cached probe masks a real failure", name, got)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
			t.Errorf("%s Content-Type = %q", name, got)
		}
	}
}
