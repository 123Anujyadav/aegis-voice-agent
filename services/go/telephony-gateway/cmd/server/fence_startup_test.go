package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
//
// The overlap check reads BOTH processes at every sample: b being ready is an
// overlap only while a is ready too. Under load a can correctly fail closed and
// hand over to b while it runs; that is a liveness gap, accepted only when a's
// own log shows a fail-closed timing loss after b started, and never reported
// as two ready. The binary killed is the one in service at the time, so the
// takeover asserted at the end is always a real one.
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
	// The heartbeat timeout also bounds every dial, and under -race a single
	// SCRAM login was measured at 65-115ms, so the timeout must comfortably
	// exceed one login. TEST values, not proposals (D10).
	const interval, timeout = time.Second, 500 * time.Millisecond
	validity := interval + timeout
	cfg := []string{
		envPrefix + "_DATABASE_DSN_FILE=" + file,
		envPrefix + "_FENCE_LOCK_KEY=" + strconv.FormatInt(time.Now().UnixNano()&0x7fffffffffff, 10),
		envPrefix + "_FENCE_HEARTBEAT_INTERVAL=" + interval.String(), envPrefix + "_FENCE_HEARTBEAT_TIMEOUT=" + timeout.String(),
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
	bStarted := time.Now()
	b := startBin(t, bin, cfg...)
	b.waitAnswering(t)

	// a is read before b. Without an overlap b can be ready no sooner than a
	// full validity window after a stopped being ready (b takes the lock only
	// after a's session releases it, then waits the grace), so two "ready"
	// answers read within one window prove an overlap. That assumes the lock is
	// released only by its holder, as here: a release by the server (a killed
	// session) can shorten the gap, which could only cause a false "two ready",
	// never hide one. A sample whose two reads took a window or longer proves
	// nothing either way and is not counted.
	// Sampling lasts long enough for b, had it wrongly taken the lock on its
	// next attempt, to pass its grace and report ready, and until 50 samples saw
	// one of them ready (within a bound: under load both can be briefly not
	// ready, which proves nothing either).
	var aServed, bServed, slow int
	window := time.Now().Add(interval + 2*validity)
	for until := time.Now().Add(15 * time.Second); (aServed+bServed < 50 || time.Now().Before(window)) && time.Now().Before(until); {
		start := time.Now()
		ra, rb := ready(a), ready(b)
		took := time.Since(start)
		switch {
		case took >= validity:
			slow++
		case ra && rb:
			t.Fatalf("two binaries ready at once (both read within %s)\n--- a ---\n%s\n--- b ---\n%s", took, a.out(), b.out())
		case ra:
			aServed++
		case rb:
			bServed++
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("a ready at %d samples, b at %d; %d samples too slow to judge", aServed, bServed, slow)
	if aServed+bServed == 0 {
		t.Fatalf("neither binary was ready while both ran; the overlap check was not exercised\n--- a ---\n%s\n--- b ---\n%s", a.out(), b.out())
	}
	// The binary in service now (the same read-order rule applies), so the kill
	// below is always of the owner and the takeover it asserts is a real one.
	var owner, other *proc
	wait("exactly one binary ready", func() bool {
		start := time.Now()
		ra, rb := ready(a), ready(b)
		switch {
		case ra && rb && time.Since(start) < validity:
			t.Fatalf("two binaries ready at once\n--- a ---\n%s\n--- b ---\n%s", a.out(), b.out())
		case ra && !rb:
			owner, other = a, b
			return true
		case rb && !ra:
			owner, other = b, a
			return true
		}
		return false
	})
	aLosses := fenceLosses(t, a.out())
	if (bServed > 0 || owner == b) && !anyFailClosedTimingSince(t, aLosses, bStarted, timeout) {
		t.Errorf("b took over while a was running, and a logged no fail-closed timing loss after b started: %v\n--- a ---\n%s", aLosses, a.out())
	}
	requireFailClosedTimingOnly(t, "a", aLosses, timeout)
	requireFailClosedTimingOnly(t, "b", fenceLosses(t, b.out()), timeout)

	_ = owner.cmd.Process.Kill() // crash: the OS closes its connection; the server releases the lock
	_, _ = owner.cmd.Process.Wait()
	wait("the other binary ready after the owner died", func() bool { return ready(other) })
	if strings.Contains(a.out()+b.out(), dsn) {
		t.Error("a process logged the DSN")
	}
}

// The fence's ownership-loss lines and network category, as
// internal/fence/fence.go writes them.
const (
	msgFenceLost    = "fence: ownership LOST; no longer accepting work"
	msgFenceLate    = "fence: confirmation arrived after the validity window; ownership dropped"
	errFenceNetwork = "network error reaching the database (details withheld)"
)

// fenceLoss is one ownership loss in a gateway process's JSON log.
type fenceLoss struct {
	Time        string         `json:"time"`
	Msg         string         `json:"msg"`
	Error       string         `json:"error"`
	LockMissing bool           `json:"lock_missing"`
	Phase       map[string]any `json:"phase"`
}

// failClosedTiming reports whether the fence gave ownership up because a
// confirmation did not complete in time: correct behaviour under load, not an
// overlap. That is an answer after the validity window, or a heartbeat that
// ran into its timeout: exactly the context's error or, when the driver was
// blocked on the socket, exactly the network category after the full timeout.
// Never a missing lock, a server error (an SQLSTATE) or an early network error.
func (l fenceLoss) failClosedTiming(timeout time.Duration) bool {
	switch {
	case l.Msg == msgFenceLate:
		return true
	case l.Msg != msgFenceLost || l.LockMissing:
		return false
	}
	ns, _ := l.Phase["heartbeat"].(float64)
	return l.Error == context.DeadlineExceeded.Error() || (l.Error == errFenceNetwork && time.Duration(ns) >= timeout)
}

// fenceLosses returns the ownership losses in a process's output. A final line
// still being written (no newline yet) is left for a later read.
func fenceLosses(t *testing.T, out string) []fenceLoss {
	t.Helper()
	var losses []fenceLoss
	lines := strings.Split(out, "\n")
	lines = lines[:len(lines)-1] // complete lines only
	for _, line := range lines {
		if !strings.Contains(line, msgFenceLost) && !strings.Contains(line, msgFenceLate) {
			continue
		}
		var l fenceLoss
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			t.Errorf("unreadable fence log line %q: %v", line, err)
			continue
		}
		losses = append(losses, l)
	}
	return losses
}

// anyFailClosedTimingSince reports whether a fail-closed timing loss was logged
// at or after since: only such a loss can excuse a handover that followed it.
func anyFailClosedTimingSince(t *testing.T, losses []fenceLoss, since time.Time, timeout time.Duration) bool {
	t.Helper()
	for _, l := range losses {
		at, err := time.Parse(time.RFC3339Nano, l.Time)
		if err != nil {
			t.Errorf("fence log line has no readable time %q: %v", l.Time, err)
			continue
		}
		if !at.Before(since) && l.failClosedTiming(timeout) {
			return true
		}
	}
	return false
}

// requireFailClosedTimingOnly fails the test for any loss that is not a
// fail-closed timing loss, and logs each one it excuses.
func requireFailClosedTimingOnly(t *testing.T, who string, losses []fenceLoss, timeout time.Duration) {
	t.Helper()
	for _, l := range losses {
		if l.failClosedTiming(timeout) {
			t.Logf("%s: fail-closed timing loss (liveness under load, not an overlap): %+v", who, l)
			continue
		}
		t.Errorf("%s: an ownership loss that is not a fail-closed timing loss: %+v", who, l)
	}
}

// The constants above must be the fence's own wording; if fence.go changes it,
// this fails instead of the losses going unrecognised.
func TestFenceLossMessagesMatchTheFence(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "internal", "fence", "fence.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{
		strconv.Quote(msgFenceLost), strconv.Quote(msgFenceLate), strconv.Quote(errFenceNetwork),
		`slog.Bool("lock_missing"`, `slog.String("error"`,
	} {
		if !strings.Contains(string(src), m) {
			t.Errorf("internal/fence/fence.go no longer contains %s", m)
		}
	}
	timing, err := os.ReadFile(filepath.Join("..", "..", "internal", "fence", "timing.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`PhaseHeartbeat\s*=\s*"heartbeat"`).Match(timing) {
		t.Error(`internal/fence/timing.go no longer names the heartbeat phase "heartbeat"`)
	}
}
