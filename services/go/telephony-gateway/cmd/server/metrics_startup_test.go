package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ADR-0013: /metrics is served on the health listener. These tests run the
// real binary and read the exposition over HTTP, as a scraper would.

const (
	metricChecks = "telephony_gateway_fence_readiness_checks_total"
	metricOwned  = "telephony_gateway_fence_owned"
)

// sampleValue returns the value of series in a Prometheus text exposition.
func sampleValue(body, series string) (float64, bool) {
	for _, line := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(line, series+" "); ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			return f, err == nil
		}
	}
	return 0, false
}

func (p *proc) metrics(t *testing.T) string {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", p.hp))
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") || !strings.Contains(ct, "version=0.0.4") {
		t.Errorf("Content-Type = %q, want the Prometheus text format", ct)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading /metrics: %v", err)
	}
	return string(b)
}

// Each /readyz probe of the unconfigured binary is one not_owner evaluation,
// visible on /metrics on the health port.
func TestStartup_MetricsExposeReadinessEvaluations(t *testing.T) {
	bin := buildBinary(t, "1.0.0", "0123456789abcdef0123456789abcdef01234567", "2026-01-01T00:00:00Z")
	p := startBin(t, bin)
	p.waitAnswering(t)

	const probes = 3
	for range probes {
		if code, _ := p.status("/readyz"); code != http.StatusServiceUnavailable {
			t.Fatalf("/readyz = %d, want 503 without a fence", code)
		}
	}
	body := p.metrics(t)
	series := metricChecks + `{result="not_owner"}`
	if v, ok := sampleValue(body, series); !ok || v < probes {
		t.Errorf("%s = %v (present %v), want at least %d:\n%s", series, v, ok, probes, body)
	}
	if v, ok := sampleValue(body, metricOwned); !ok || v != 0 {
		t.Errorf("%s = %v (present %v), want 0:\n%s", metricOwned, v, ok, body)
	}
	if _, ok := sampleValue(body, metricChecks+`{result="owned"}`); ok {
		t.Errorf("an owned evaluation without a fence:\n%s", body)
	}
}

// With a real database the single process becomes owner, and /metrics says
// so. One binary only, on its own lock key, so it cannot interact with the
// takeover test.
func TestStartup_OwnerExposesOwnedMetric(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv(fenceTestDSNEnv))
	if dsn == "" {
		t.Skipf("%s is not set; skipping (a SKIP, not a PASS)", fenceTestDSNEnv)
	}
	bin := buildBinary(t, "1.0.0", "0123456789abcdef0123456789abcdef01234567", "2026-01-01T00:00:00Z")
	file := filepath.Join(t.TempDir(), "dsn")
	if err := os.WriteFile(file, []byte(dsn), 0o600); err != nil {
		t.Fatal(err)
	}
	// Same TEST timings as the takeover test, for the same -race reason.
	const interval, timeout = time.Second, 500 * time.Millisecond
	p := startBin(t, bin,
		envPrefix+"_DATABASE_DSN_FILE="+file,
		envPrefix+"_FENCE_LOCK_KEY="+strconv.FormatInt(time.Now().UnixNano()&0x7fffffffffff|1, 10),
		envPrefix+"_FENCE_HEARTBEAT_INTERVAL="+interval.String(), envPrefix+"_FENCE_HEARTBEAT_TIMEOUT="+timeout.String(),
		envPrefix+"_FENCE_AUTH_MODE=password", envPrefix+"_ENVIRONMENT=development",
	)
	p.waitAnswering(t)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if code, _ := p.status("/readyz"); code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never became ready:\n%s", p.out())
		}
		time.Sleep(20 * time.Millisecond)
	}
	body := p.metrics(t)
	if v, ok := sampleValue(body, metricChecks+`{result="owned"}`); !ok || v < 1 {
		t.Errorf("owned evaluations = %v (present %v), want at least 1:\n%s", v, ok, body)
	}
	if v, ok := sampleValue(body, metricOwned); !ok || v != 1 {
		t.Errorf("%s = %v (present %v), want 1:\n%s", metricOwned, v, ok, body)
	}
}
