package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/platform"
)

// P1-30B — fence startup wiring: configuration, fail-closed readiness and the
// lifecycle, without a database (fence_postgres_test.go has the real one).

const prefix = "CS_TELEPHONY_GATEWAY"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func env(m map[string]string) Lookup {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func dsnFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dsn")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func full(t *testing.T, dsn string) map[string]string {
	t.Helper()
	return map[string]string{
		prefix + "_DATABASE_DSN_FILE":        dsnFile(t, dsn),
		prefix + "_FENCE_LOCK_KEY":           "8813",
		prefix + "_FENCE_HEARTBEAT_INTERVAL": "200ms",
		prefix + "_FENCE_HEARTBEAT_TIMEOUT":  "100ms",
		prefix + "_FENCE_AUTH_MODE":          "password",
		prefix + "_ENVIRONMENT":              "development",
	}
}

// unreachable is a DSN for a port nothing listens on.
func unreachable(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return "postgres://gw:hunter2@127.0.0.1:" + strconv.Itoa(port) + "/telephony?sslmode=disable&connect_timeout=1"
}

func TestLoadFence_NothingSetMeansNotConfigured(t *testing.T) {
	t.Parallel()
	s, err := LoadFence(prefix, env(nil))
	if err != nil || s.Configured || len(s.Missing) != 6 {
		t.Fatalf("= %+v, %v", s, err)
	}
}

func TestLoadFence_AllValid(t *testing.T) {
	t.Parallel()
	s, err := LoadFence(prefix, env(full(t, "postgres://gw@127.0.0.1:5432/telephony")))
	if err != nil || !s.Configured || s.Config.LockKey != 8813 ||
		s.Config.HeartbeatInterval != 200*time.Millisecond || s.Config.HeartbeatTimeout != 100*time.Millisecond || s.Conn == nil {
		t.Fatalf("= %+v, %v", s, err)
	}
}

// Every invalid value is refused; no error carries a value or the DSN.
func TestLoadFence_InvalidOrPartialConfigurationIsRefusedWithoutEchoingValues(t *testing.T) {
	t.Parallel()
	secretDSN := "postgres://gw:hunter2@[unparseable"
	cases := map[string]func(m map[string]string){
		"partial":            func(m map[string]string) { delete(m, prefix+"_FENCE_LOCK_KEY") },
		"zero key":           func(m map[string]string) { m[prefix+"_FENCE_LOCK_KEY"] = "0" },
		"key not an integer": func(m map[string]string) { m[prefix+"_FENCE_LOCK_KEY"] = "hunter2" },
		"bad interval":       func(m map[string]string) { m[prefix+"_FENCE_HEARTBEAT_INTERVAL"] = "soon" },
		"negative timeout":   func(m map[string]string) { m[prefix+"_FENCE_HEARTBEAT_TIMEOUT"] = "-1s" },
		"timeout ≥ interval": func(m map[string]string) { m[prefix+"_FENCE_HEARTBEAT_TIMEOUT"] = "200ms" },
		"whitespace":         func(m map[string]string) { m[prefix+"_FENCE_LOCK_KEY"] = " 8813" },
		"no DSN file":        func(m map[string]string) { m[prefix+"_DATABASE_DSN_FILE"] = filepath.Join(t.TempDir(), "absent") },
		"empty DSN file":     func(m map[string]string) { m[prefix+"_DATABASE_DSN_FILE"] = dsnFile(t, "  \n") },
		"two-line DSN":       func(m map[string]string) { m[prefix+"_DATABASE_DSN_FILE"] = dsnFile(t, "postgres://a\npostgres://b") },
		"oversized DSN": func(m map[string]string) {
			m[prefix+"_DATABASE_DSN_FILE"] = dsnFile(t, strings.Repeat("x", MaxDSNFileBytes+1))
		},
		"unparseable DSN":    func(m map[string]string) { m[prefix+"_DATABASE_DSN_FILE"] = dsnFile(t, secretDSN) },
		"DSN is a directory": func(m map[string]string) { m[prefix+"_DATABASE_DSN_FILE"] = t.TempDir() },
	}
	for name, mutate := range cases {
		m := full(t, "postgres://gw@127.0.0.1:5432/telephony")
		mutate(m)
		_, err := LoadFence(prefix, env(m))
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: %v, want ErrInvalidConfig", name, err)
			continue
		}
		if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "unparseable") {
			t.Errorf("%s: error echoes a value: %v", name, err)
		}
	}
}

func readiness(h *platform.Health) (int, string) {
	rec := httptest.NewRecorder()
	h.ReadinessHandler()(rec, httptest.NewRequest(http.MethodGet, "/readyz", http.NoBody))
	return rec.Code, rec.Body.String()
}

func newService(t *testing.T) (*platform.Service, *platform.Health, platform.ServiceConfig) {
	t.Helper()
	// Both listeners stay open until both ports are known, so the OS cannot
	// hand out the same free port twice (a flake seen under -count=2).
	var ports [2]int
	var listeners []net.Listener
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	for i := range ports {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, l)
		ports[i] = l.Addr().(*net.TCPAddr).Port
	}
	cfg := platform.ServiceConfig{
		Name: "telephony-gateway", Environment: "development", Region: "in-south-1", Version: "test",
		HTTPPort: ports[0], HealthPort: ports[1], LogLevel: "info", LogFormat: "json",
		ShutdownTimeout: 2 * time.Second, ReadHeaderTimeout: time.Second,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	h := platform.NewHealth(cfg)
	return platform.NewService(cfg, quiet, h), h, cfg
}

// Unconfigured: the service starts and is NEVER ready; the check names the
// missing variables (not values).
func TestWireFence_UnconfiguredStartsButIsNeverReady(t *testing.T) {
	t.Parallel()
	svc, h, _ := newService(t)
	f, err := WireFence(svc, h, prefix, env(nil), quiet)
	if err != nil || f != nil {
		t.Fatalf("= %v, %v", f, err)
	}
	h.MarkReady()
	code, body := readiness(h)
	if code != http.StatusServiceUnavailable {
		t.Errorf("readyz = %d, want 503", code)
	}
	for _, v := range FenceVariables(prefix) {
		if !strings.Contains(body, v) {
			t.Errorf("readiness does not name %s:\n%s", v, body)
		}
	}
}

func TestWireFence_InvalidConfigurationStopsStartup(t *testing.T) {
	t.Parallel()
	svc, h, _ := newService(t)
	m := full(t, "postgres://gw@127.0.0.1:5432/telephony")
	m[prefix+"_FENCE_HEARTBEAT_INTERVAL"] = "never"
	if _, err := WireFence(svc, h, prefix, env(m), quiet); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("= %v", err)
	}
	if _, err := WireFence(nil, h, prefix, env(m), quiet); err == nil {
		t.Error("nil service accepted")
	}
}

// Configured, no database reachable: the service runs, readiness stays 503,
// and a cancelled lifecycle shuts down cleanly.
func TestWireFence_StartupWithoutADatabaseNeverBecomesReadyAndShutsDown(t *testing.T) {
	t.Parallel()
	svc, h, _ := newService(t)
	f, err := WireFence(svc, h, prefix, env(full(t, unreachable(t))), quiet)
	if err != nil || f == nil {
		t.Fatalf("= %v, %v", f, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if code, _ := readiness(h); code == http.StatusOK {
			t.Fatal("ready without a database")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if f.Owned() {
		t.Fatal("owner without a database")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("service did not shut down")
	}
}
