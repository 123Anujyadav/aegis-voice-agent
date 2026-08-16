package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// HealthStatus enumerates the states a dependency or the service overall can
// report.
type HealthStatus string

const (
	// StatusHealthy indicates normal operation.
	StatusHealthy HealthStatus = "healthy"

	// StatusDegraded indicates the component is serving but with reduced
	// capability — for example a cache is unreachable and every read is falling
	// through to the database.
	//
	// Degraded is deliberately distinct from unhealthy. A service that removes
	// itself from load balancing because its cache is down converts a
	// performance problem into an outage. Degraded means "keep sending me
	// traffic, but page someone".
	StatusDegraded HealthStatus = "degraded"

	// StatusUnhealthy indicates the component cannot serve correctly and must
	// not receive traffic.
	StatusUnhealthy HealthStatus = "unhealthy"
)

// CheckResult is the outcome of evaluating a single dependency.
type CheckResult struct {
	// Name identifies the dependency, for example "postgres" or "kafka".
	Name string `json:"name"`

	// Status is the evaluated health of the dependency.
	Status HealthStatus `json:"status"`

	// Message carries engineer-facing detail on a non-healthy result. It is
	// omitted when healthy to keep probe responses small, since probes run
	// every few seconds forever.
	Message string `json:"message,omitempty"`

	// DurationMS records how long the check took, which is frequently the
	// earliest signal of an impending failure — latency degrades before
	// availability does.
	DurationMS int64 `json:"duration_ms"`

	// Critical records whether this dependency's failure makes the whole
	// service unhealthy, as opposed to merely degraded.
	Critical bool `json:"critical"`
}

// Checker evaluates the health of one dependency.
//
// Implementations must respect the supplied context's deadline and must not
// block indefinitely: a health check that hangs turns a readiness probe into a
// timeout, which the orchestrator reads as failure and responds to by
// restarting an otherwise healthy process.
type Checker interface {
	// Name returns a stable identifier for the dependency.
	Name() string

	// Check evaluates the dependency, returning nil when healthy.
	Check(ctx context.Context) error
}

// CheckerFunc adapts a plain function to the Checker interface.
type CheckerFunc struct {
	// CheckName is the stable dependency identifier.
	CheckName string

	// Fn performs the evaluation.
	Fn func(ctx context.Context) error
}

// Name returns the configured dependency identifier.
func (c CheckerFunc) Name() string { return c.CheckName }

// Check invokes the wrapped function.
func (c CheckerFunc) Check(ctx context.Context) error { return c.Fn(ctx) }

// registeredCheck pairs a Checker with the metadata governing how its result is
// interpreted.
type registeredCheck struct {
	checker  Checker
	critical bool
	timeout  time.Duration
}

// Health aggregates dependency checks and serves liveness and readiness probes.
//
// LIVENESS VERSUS READINESS — the distinction this type exists to enforce:
//
//	Liveness  answers "is this process broken beyond recovery?" A failing
//	          liveness probe causes a RESTART. It must therefore never consult
//	          a dependency: if the database is down and liveness checks it,
//	          every replica restarts in a loop, turning a database incident
//	          into a total outage plus a thundering herd on recovery.
//
//	Readiness answers "should this instance receive traffic right now?" A
//	          failing readiness probe removes the instance from the load
//	          balancer but leaves it running. This is where dependency checks
//	          belong.
//
// Conflating the two is among the most common and most damaging Kubernetes
// misconfigurations, so the two are separate methods here and cannot be wired
// to the same handler by accident.
type Health struct {
	mu       sync.RWMutex
	checks   []registeredCheck
	ready    bool
	version  string
	service  string
	started  time.Time
	shutdown bool
}

// NewHealth constructs a Health aggregator for a service.
//
// The service begins NOT ready. Readiness is asserted explicitly via
// MarkReady once initialisation has completed, so that an instance cannot
// receive traffic during startup — for example before its connection pools are
// warm or its configuration has been fully applied.
func NewHealth(cfg ServiceConfig) *Health {
	return &Health{
		service: cfg.Name,
		version: cfg.Version,
		started: time.Now(),
		ready:   false,
	}
}

// Register adds a dependency check that runs as part of readiness evaluation.
//
// critical determines the consequence of failure: a critical dependency failing
// makes the service unhealthy and removes it from load balancing, whereas a
// non-critical failure reports degraded and keeps traffic flowing.
//
// timeout bounds the individual check. It must be comfortably shorter than the
// orchestrator's probe timeout, or a slow check produces a probe timeout whose
// cause is invisible in the response body.
func (h *Health) Register(c Checker, critical bool, timeout time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks = append(h.checks, registeredCheck{
		checker:  c,
		critical: critical,
		timeout:  timeout,
	})
}

// MarkReady declares the service ready to receive traffic.
func (h *Health) MarkReady() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ready = true
}

// MarkShuttingDown records that graceful shutdown has begun.
//
// This is what makes zero-downtime deployment work. On receiving SIGTERM the
// service marks itself shutting down and immediately begins failing readiness,
// so the load balancer stops sending new requests. Only after the balancer has
// observed that — which takes one or more probe intervals — does the process
// stop accepting connections and drain. Skipping this step is why deployments
// that "look graceful" still drop requests.
func (h *Health) MarkShuttingDown() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.shutdown = true
	h.ready = false
}

// livenessResponse is the payload returned by the liveness probe.
type livenessResponse struct {
	Status    HealthStatus `json:"status"`
	Service   string       `json:"service"`
	Version   string       `json:"version"`
	UptimeSec int64        `json:"uptime_seconds"`
}

// readinessResponse is the payload returned by the readiness probe.
type readinessResponse struct {
	Status    HealthStatus  `json:"status"`
	Service   string        `json:"service"`
	Version   string        `json:"version"`
	UptimeSec int64         `json:"uptime_seconds"`
	Checks    []CheckResult `json:"checks"`
}

// LivenessHandler serves the liveness probe.
//
// It answers purely from process-local state and never consults a dependency,
// for the reason documented on Health. It returns 200 whenever the process is
// running and able to serve HTTP at all.
func (h *Health) LivenessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.mu.RLock()
		started := h.started
		h.mu.RUnlock()

		writeJSON(w, http.StatusOK, livenessResponse{
			Status:    StatusHealthy,
			Service:   h.service,
			Version:   h.version,
			UptimeSec: int64(time.Since(started).Seconds()),
		})
	}
}

// ReadinessHandler serves the readiness probe, evaluating every registered
// dependency concurrently.
//
// Checks run in parallel rather than in sequence because probe latency is
// bounded by the orchestrator; ten sequential 200 ms checks exceed a typical
// one-second probe timeout, while ten concurrent ones do not.
func (h *Health) ReadinessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.mu.RLock()
		ready := h.ready
		shuttingDown := h.shutdown
		checks := make([]registeredCheck, len(h.checks))
		copy(checks, h.checks)
		started := h.started
		h.mu.RUnlock()

		// During shutdown, report not-ready immediately without running checks.
		// The answer cannot change and running them wastes time in the drain
		// window.
		if shuttingDown {
			writeJSON(w, http.StatusServiceUnavailable, readinessResponse{
				Status:    StatusUnhealthy,
				Service:   h.service,
				Version:   h.version,
				UptimeSec: int64(time.Since(started).Seconds()),
				Checks:    []CheckResult{},
			})
			return
		}

		results := h.runChecks(r.Context(), checks)

		status := StatusHealthy
		for _, res := range results {
			switch {
			case res.Status == StatusUnhealthy && res.Critical:
				status = StatusUnhealthy
			case res.Status != StatusHealthy && status == StatusHealthy:
				status = StatusDegraded
			}
		}
		if !ready {
			status = StatusUnhealthy
		}

		code := http.StatusOK
		if status == StatusUnhealthy {
			code = http.StatusServiceUnavailable
		}

		writeJSON(w, code, readinessResponse{
			Status:    status,
			Service:   h.service,
			Version:   h.version,
			UptimeSec: int64(time.Since(started).Seconds()),
			Checks:    results,
		})
	}
}

// runChecks evaluates every registered dependency concurrently and collects the
// results. Each check receives its own timeout-bounded context so that one slow
// dependency cannot consume the whole probe budget.
func (h *Health) runChecks(ctx context.Context, checks []registeredCheck) []CheckResult {
	if len(checks) == 0 {
		return []CheckResult{}
	}

	results := make([]CheckResult, len(checks))
	var wg sync.WaitGroup
	wg.Add(len(checks))

	for i, rc := range checks {
		go func(idx int, rc registeredCheck) {
			defer wg.Done()

			checkCtx, cancel := context.WithTimeout(ctx, rc.timeout)
			defer cancel()

			start := time.Now()
			err := rc.checker.Check(checkCtx)
			elapsed := time.Since(start)

			res := CheckResult{
				Name:       rc.checker.Name(),
				Status:     StatusHealthy,
				DurationMS: elapsed.Milliseconds(),
				Critical:   rc.critical,
			}
			if err != nil {
				res.Status = StatusUnhealthy
				res.Message = err.Error()
			}
			results[idx] = res
		}(i, rc)
	}

	wg.Wait()
	return results
}

// writeJSON serialises v to w with the given status code.
//
// Probe responses are explicitly marked no-store: an intermediary caching a
// healthy response would mask a subsequent failure, which is the worst possible
// failure mode for a health endpoint.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.WriteHeader(code)
	// An encoding failure here cannot be reported to the client, since the
	// status line is already written. The error is deliberately discarded
	// rather than panicking a probe handler.
	_ = json.NewEncoder(w).Encode(v)
}
