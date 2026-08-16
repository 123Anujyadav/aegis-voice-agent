package platform

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The metrics route is mounted on the EXISTING health mux rather than a second
// listener (ADR-0013). These tests pin that, and pin that a service which never
// sets a handler is unaffected.
//
// platform must not import the exporter: it takes an http.Handler, which is
// stdlib, so every platform consumer — including services outside the AI plane —
// keeps its dependency set unchanged.

func testService(t *testing.T) *Service {
	t.Helper()
	cfg := ServiceConfig{Name: "test", HTTPPort: 8080, HealthPort: 8081}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewService(cfg, log, NewHealth(cfg))
}

func TestHealthMux_ServesMetricsWhenHandlerIsSet(t *testing.T) {
	t.Parallel()

	svc := testService(t)
	svc.SetMetricsHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = io.WriteString(w, "some_total 1\n")
	}))

	rec := httptest.NewRecorder()
	svc.newHealthServer().Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "some_total 1") {
		t.Errorf("body = %q, want the exposition", rec.Body.String())
	}
}

// TestHealthMux_MetricsSharesTheHealthListener is the ADR-0013 constraint: no
// second listener. If /metrics and /healthz are served by the same mux, they are
// on the same port by construction.
func TestHealthMux_MetricsSharesTheHealthListener(t *testing.T) {
	t.Parallel()

	svc := testService(t)
	svc.SetMetricsHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "shared_total 1\n")
	}))

	srv := svc.newHealthServer()
	if want := ":8081"; srv.Addr != want {
		t.Errorf("health server Addr = %q, want %q — metrics must not open a second port", srv.Addr, want)
	}

	for _, path := range []string{"/healthz", "/metrics"} {
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusNotFound {
			t.Errorf("%s returned 404 from the health mux", path)
		}
	}
}

// TestHealthMux_WithoutMetricsHandlerProbesStillWork — a service that never
// wires metrics must be completely unaffected.
func TestHealthMux_WithoutMetricsHandlerProbesStillWork(t *testing.T) {
	t.Parallel()

	srv := testService(t).newHealthServer()

	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("/metrics status = %d with no handler set, want 404", rec.Code)
	}
}

// TestHealthMux_MetricsHandlerPanicDoesNotAffectProbes is the degradation
// requirement. Liveness and readiness are how an orchestrator decides whether to
// kill the process; a broken exporter must not be able to cause that.
func TestHealthMux_MetricsHandlerPanicDoesNotAffectProbes(t *testing.T) {
	t.Parallel()

	svc := testService(t)
	svc.SetMetricsHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("exporter blew up")
	}))
	srv := svc.newHealthServer()

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("a panicking metrics handler escaped to the mux: %v", r)
			}
		}()
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("panicking exporter returned %d, want 500", rec.Code)
		}
	}()

	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/healthz status = %d after a metrics panic, want 200", rec.Code)
	}
}

// TestSetMetricsHandler_NilIsIgnored — passing nil must not mount a route that
// then nil-panics on the first scrape.
func TestSetMetricsHandler_NilIsIgnored(t *testing.T) {
	t.Parallel()

	svc := testService(t)
	svc.SetMetricsHandler(nil)

	rec := httptest.NewRecorder()
	svc.newHealthServer().Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d after SetMetricsHandler(nil), want 404", rec.Code)
	}
}
