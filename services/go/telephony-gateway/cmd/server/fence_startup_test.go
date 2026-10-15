package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fenceTestDSNEnv names a DISPOSABLE test database (same variable as the
// fence package's real-PostgreSQL tests).
const fenceTestDSNEnv = "CS_TELEPHONY_GATEWAY_FENCE_TEST_DSN"

// P1-30B — the real binary's startup with the fence (decision D6). Reuses
// buildBinary and startupEnv from the existing build-info tests.

// freePorts returns n distinct free ports: every listener stays open until all
// are known, so the OS cannot hand out the same port twice.
func freePorts(t *testing.T, n int) []int {
	t.Helper()
	ports := make([]int, n)
	listeners := make([]net.Listener, 0, n)
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
	return ports
}

type proc struct {
	cmd  *exec.Cmd
	hp   int
	mu   sync.Mutex
	logs bytes.Buffer
}

func (p *proc) Write(b []byte) (int, error) { p.mu.Lock(); defer p.mu.Unlock(); return p.logs.Write(b) }
func (p *proc) out() string                 { p.mu.Lock(); defer p.mu.Unlock(); return p.logs.String() }

func startBin(t *testing.T, bin string, extra ...string) *proc {
	t.Helper()
	ports := freePorts(t, 2)
	p := &proc{hp: ports[0]}
	p.cmd = exec.Command(bin)
	p.cmd.Env = append(startupEnv(), append([]string{
		fmt.Sprintf("%s_HTTP_PORT=%d", envPrefix, ports[1]),
		fmt.Sprintf("%s_HEALTH_PORT=%d", envPrefix, p.hp),
	}, extra...)...)
	p.cmd.Stdout, p.cmd.Stderr = p, p
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.cmd.Process.Kill(); _, _ = p.cmd.Process.Wait() })
	return p
}

// status returns /readyz's status code and body, or 0 when not answering.
func (p *proc) status(path string) (int, string) {
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", p.hp, path))
	if err != nil {
		return 0, ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (p *proc) waitAnswering(t *testing.T) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if code, _ := p.status("/healthz"); code != 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("health endpoint never answered:\n%s", p.out())
}

// Without fence configuration the binary starts, is alive, and is NEVER ready;
// readiness names the missing variables.
func TestStartup_WithoutFenceConfigurationIsNeverReady(t *testing.T) {
	bin := buildBinary(t, "1.0.0", "0123456789abcdef0123456789abcdef01234567", "2026-01-01T00:00:00Z")
	p := startBin(t, bin)
	p.waitAnswering(t)
	if code, _ := p.status("/healthz"); code != http.StatusOK {
		t.Errorf("/healthz = %d", code)
	}
	for range 25 {
		code, body := p.status("/readyz")
		if code == http.StatusOK {
			t.Fatalf("ready without a fence:\n%s", body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, body := p.status("/readyz")
	if !strings.Contains(body, envPrefix+"_FENCE_LOCK_KEY") || !strings.Contains(body, envPrefix+"_DATABASE_DSN_FILE") {
		t.Errorf("readiness does not name the missing variables:\n%s", body)
	}
	if !strings.Contains(p.out(), "fence NOT configured") {
		t.Errorf("no startup diagnostic:\n%s", p.out())
	}
}

// A partial or invalid fence configuration stops the process, naming the
// variable but never its value.
func TestStartup_InvalidFenceConfigurationStopsTheProcess(t *testing.T) {
	bin := buildBinary(t, "1.0.0", "0123456789abcdef0123456789abcdef01234567", "2026-01-01T00:00:00Z")
	dsn := filepath.Join(t.TempDir(), "dsn")
	if err := os.WriteFile(dsn, []byte("postgres://gw:hunter2@[broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, extra := range map[string][]string{
		"partial": {envPrefix + "_FENCE_LOCK_KEY=8813"},
		"bad DSN": {
			envPrefix + "_DATABASE_DSN_FILE=" + dsn, envPrefix + "_FENCE_LOCK_KEY=8813",
			envPrefix + "_FENCE_HEARTBEAT_INTERVAL=200ms", envPrefix + "_FENCE_HEARTBEAT_TIMEOUT=100ms",
			envPrefix + "_FENCE_AUTH_MODE=password", envPrefix + "_ENVIRONMENT=development",
		},
	} {
		p := startBin(t, bin, extra...)
		errc := make(chan error, 1)
		go func() { errc <- p.cmd.Wait() }()
		select {
		case err := <-errc:
			if err == nil {
				t.Errorf("%s: exited 0", name)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: process kept running on an invalid fence configuration", name)
		}
		if !strings.Contains(p.out(), "configure fence") || strings.Contains(p.out(), "hunter2") {
			t.Errorf("%s: output:\n%s", name, p.out())
		}
	}
}

// Real PostgreSQL: of two binaries sharing a fence, exactly one is ready; when
// that process dies, the other becomes ready.
func TestStartup_TwoBinariesOneReadyThenTakeover(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv(fenceTestDSNEnv))
	if dsn == "" {
		t.Skipf("%s is not set; skipping (a SKIP, not a PASS)", fenceTestDSNEnv)
	}
	bin := buildBinary(t, "1.0.0", "0123456789abcdef0123456789abcdef01234567", "2026-01-01T00:00:00Z")
	file := filepath.Join(t.TempDir(), "dsn")
	if err := os.WriteFile(file, []byte(dsn), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := []string{
		envPrefix + "_DATABASE_DSN_FILE=" + file,
		envPrefix + "_FENCE_LOCK_KEY=" + strconv.FormatInt(time.Now().UnixNano()&0x7fffffffffff, 10),
		envPrefix + "_FENCE_HEARTBEAT_INTERVAL=200ms", envPrefix + "_FENCE_HEARTBEAT_TIMEOUT=100ms",
		envPrefix + "_FENCE_AUTH_MODE=password", envPrefix + "_ENVIRONMENT=development",
	}
	ready := func(p *proc) bool { code, _ := p.status("/readyz"); return code == http.StatusOK }
	wait := func(what string, cond func() bool) {
		t.Helper()
		for i := 0; i < 1000; i++ {
			if cond() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	a := startBin(t, bin, cfg...)
	wait("a ready", func() bool { return ready(a) })
	b := startBin(t, bin, cfg...)
	b.waitAnswering(t)
	for range 50 {
		if ready(b) {
			t.Fatal("two binaries ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = a.cmd.Process.Kill() // crash: the OS closes its connection; the server releases the lock
	_, _ = a.cmd.Process.Wait()
	wait("b ready after a died", func() bool { return ready(b) })
	if strings.Contains(a.out()+b.out(), dsn) {
		t.Error("a process logged the DSN")
	}
}
