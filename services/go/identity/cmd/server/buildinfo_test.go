package main

// THIS FILE IS IDENTICAL IN EVERY Go SERVICE (services/go/*/cmd/server); the
// service-specific startup expectations live in buildinfo_hooks_test.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The metadata contract is platform.BuildInfo (packages/go/platform); these
// tests prove THIS binary is wired to it - in particular that the linker
// flags docker/go/Dockerfile passes (-X main.version/commit/buildTime) reach
// the variables, which before this file existed they silently did not.

// Test values only: no release, commit or build is implied.
const (
	testVersion   = "9.8.7-test.1"
	testCommit    = "0123456789abcdef0123456789abcdef01234567"
	testBuildTime = "2026-10-12T07:30:00Z"
)

func setBuildVars(t *testing.T, v, c, b string) {
	t.Helper()
	ov, oc, ob := version, commit, buildTime
	version, commit, buildTime = v, c, b
	t.Cleanup(func() { version, commit, buildTime = ov, oc, ob })
}

func TestVersion_HonestDefaultsWithoutLinkerValues(t *testing.T) {
	setBuildVars(t, "", "", "")
	var out bytes.Buffer
	if err := writeVersion(&out); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("%q: %v", out.String(), err)
	}
	// `go test` binaries carry no VCS stamp, so the commit is unknown too.
	if got["service"] != serviceName || got["version"] != "dev" || got["commit"] != "unknown" || got["buildTime"] != "unknown" || len(got) != 4 {
		t.Errorf("%v", got)
	}
}

func TestVersion_SuppliedValuesAreReported(t *testing.T) {
	setBuildVars(t, testVersion, testCommit, testBuildTime)
	var out bytes.Buffer
	if err := writeVersion(&out); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`{"service":%q,"version":%q,"commit":%q,"buildTime":%q}`+"\n", serviceName, testVersion, testCommit, testBuildTime)
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

// buildBinary compiles this command with the given linker variables, exactly
// as the Dockerfile's -ldflags would.
func buildBinary(t *testing.T, v, c, b string) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("the go command is not on PATH: linker test SKIPPED (not passed)")
	}
	out := filepath.Join(t.TempDir(), "service")
	if os.PathSeparator == '\\' {
		out += ".exe"
	}
	ld := fmt.Sprintf("-X 'main.version=%s' -X 'main.commit=%s' -X 'main.buildTime=%s'", v, c, b)
	cmd := exec.Command(goBin, "build", "-trimpath", "-ldflags", ld, "-o", out, ".")
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, msg)
	}
	return out
}

func runVersion(t *testing.T, bin string) map[string]any {
	t.Helper()
	// Bounded: a binary that ignored `version` would start the service and
	// never exit.
	runCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, bin, "version")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "SYSTEMROOT=" + os.Getenv("SYSTEMROOT")}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("version: %v (it must print and exit)", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%q: %v", out, err)
	}
	return got
}

// The real linker path: -X values reach the binary's version output.
func TestVersion_LinkerValuesReachTheBinary(t *testing.T) {
	bin := buildBinary(t, testVersion, testCommit, testBuildTime)
	got := runVersion(t, bin)
	if got["service"] != serviceName || got["version"] != testVersion || got["commit"] != testCommit || got["buildTime"] != testBuildTime || got["rejected"] != nil {
		t.Errorf("%v", got)
	}
}

// Malformed linker values: the command still works, reports defaults, and
// names (never echoes) what it rejected.
func TestVersion_MalformedLinkerValuesAreRejectedNotReported(t *testing.T) {
	bin := buildBinary(t, "has_no_spaces_but_!bang", "NOT-A-COMMIT", "last-tuesday")
	got := runVersion(t, bin)
	rej, _ := json.Marshal(got["rejected"])
	if got["version"] != "dev" || got["commit"] != "unknown" || got["buildTime"] != "unknown" ||
		string(rej) != `["version","commit","buildTime"]` {
		t.Errorf("%v", got)
	}
}

// A lockedBuffer collects a child process's output, which exec.Cmd copies from
// its own goroutine while the process runs, so every access is locked.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Normal startup with malformed metadata: the process does not crash on it,
// logs which fields it replaced, and reports the honest default version.
func TestStartup_MalformedMetadataDoesNotStopTheService(t *testing.T) {
	bin := buildBinary(t, "bad version", "NOT-A-COMMIT", "never")
	// Both ports come from one freePorts call, which holds every listener open
	// until all are chosen. Picking them one at a time, closing each listener
	// first, can hand out the same port twice, and the binary then fails to
	// bind its second listener: the flake P1-40 §3 reproduced in the gateway's
	// copy of this test.
	ports := freePorts(t, 2)
	httpPort, hp := ports[0], ports[1]
	cmd := exec.Command(bin)
	cmd.Env = append(startupEnv(), fmt.Sprintf("%s_HTTP_PORT=%d", envPrefix, httpPort), fmt.Sprintf("%s_HEALTH_PORT=%d", envPrefix, hp),
		envPrefix+"_VERSION=from-the-environment")
	var logs lockedBuffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	checkStartup(t, hp, &logs)
}

func wantBuildLogs(t *testing.T, logs string) {
	t.Helper()
	if !strings.Contains(logs, `"msg":"build"`) || !strings.Contains(logs, `"commit":"unknown"`) ||
		!strings.Contains(logs, `"malformed build metadata replaced by defaults"`) || !strings.Contains(logs, `"fields":["version","commit","buildTime"]`) ||
		!strings.Contains(logs, `"variable":"`+envPrefix+`_VERSION"`) {
		t.Errorf("build log lines missing:\n%s", logs)
	}
	for _, bad := range []string{"bad version", "NOT-A-COMMIT", "never\"", "from-the-environment"} {
		if strings.Contains(logs, bad) {
			t.Errorf("logs carry %q", bad)
		}
	}
}

// freePorts returns n distinct free loopback ports. Every listener stays open
// until all n are chosen, so no port is handed out twice.
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
