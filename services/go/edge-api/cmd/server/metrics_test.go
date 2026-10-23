package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/callscreen/callscreen-platform/services/go/edge-api/internal/auth"
)

// With a listener registered, GET /metrics exposes the SAME authenticator's
// outcome counter: a refused request through the application handler appears
// in the scrape.
func TestConfigureAuth_MetricsExposeAuthenticationOutcomes(t *testing.T) {
	setAuthEnv(t)
	made := fakeOpen(t)
	n := 0
	runners, checks := &fakeRunners{}, &fakeChecks{}
	if err := configureAuth(ctx, testCfg, runners, checks, someRoutes(&n), quiet); err != nil {
		t.Fatal(err)
	}
	if runners.metrics == nil {
		t.Fatal("no metrics handler was mounted")
	}
	h := applicationHandler((*made)[0].Authenticator(), someRoutes(&n), nil)
	for i := 0; i < 3; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/protected", http.NoBody))
	}
	rec := httptest.NewRecorder()
	runners.metrics.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", http.NoBody))
	want := auth.MetricAuthRequests + `{outcome="` + auth.OutcomeUnauthenticated + `"} 3`
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("scrape = %d:\n%s\nwant a line %q", rec.Code, rec.Body.String(), want)
	}
	if n != 0 {
		t.Errorf("a refused request reached the route %d times", n)
	}
}

// Without an application listener there is nothing to count, and nothing is
// mounted.
func TestConfigureAuth_NoListenerMountsNoMetrics(t *testing.T) {
	setAuthEnv(t)
	fakeOpen(t)
	runners, checks := &fakeRunners{}, &fakeChecks{}
	if err := configureAuth(ctx, testCfg, runners, checks, nil, quiet); err != nil || runners.metrics != nil {
		t.Fatalf("(%v, metrics mounted: %v)", err, runners.metrics != nil)
	}
	clearAuthEnv(t)
	runners = &fakeRunners{}
	if err := configureAuth(ctx, testCfg, runners, checks, nil, quiet); err != nil || runners.metrics != nil {
		t.Fatalf("inactive: (%v, metrics mounted: %v)", err, runners.metrics != nil)
	}
}
