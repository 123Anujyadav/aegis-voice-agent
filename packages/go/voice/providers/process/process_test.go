package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// A real child process
// ---------------------------------------------------------------------------
//
// Every test here drives an actual program. Process supervision is entirely
// about what happens at the operating-system boundary — orphans, blocked pipes,
// signals ignored — and a mock of that boundary would test the mock.
//
// The helper is compiled once per test binary and behaves differently according
// to its first argument, so one program covers every scenario.

var (
	helperOnce sync.Once
	helperPath string
	helperErr  error
)

const helperSource = `package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	mode := ""
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}

	switch mode {
	case "ready":
		// Announces itself, then echoes stdin uppercased.
		fmt.Println("READY")
		echo(strings.ToUpper)

	case "silent":
		// Never announces anything. Used for the readiness timeout.
		//
		// A long sleep rather than select{}: an empty select makes the Go
		// runtime detect "all goroutines are asleep" and abort the process,
		// so the child would DIE instantly instead of hanging — testing the
		// wrong failure entirely.
		time.Sleep(time.Hour)

	case "echo":
		// No banner; echoes immediately. For a nil Ready probe.
		echo(func(s string) string { return s })

	case "exit-immediately":
		os.Exit(0)

	case "fatal":
		fmt.Fprintln(os.Stderr, "fatal: model not found")
		os.Exit(3)

	case "fatal-stdout":
		// Reports its failure on stdout, where the readiness probe sees it.
		fmt.Println("ERROR: cannot load model")
		time.Sleep(time.Minute)

	case "stderr-flood":
		fmt.Println("READY")
		line := strings.Repeat("x", 1024)
		for i := 0; i < 4096; i++ {
			fmt.Fprintln(os.Stderr, line)
		}
		time.Sleep(time.Hour)

	case "ignore-stdin-close":
		// Announces readiness then refuses to die when stdin closes.
		fmt.Println("READY")
		time.Sleep(time.Hour)

	case "heartbeat":
		// Announces readiness, then appends a byte to the file named by the
		// second argument every 20ms, forever. Whether that file keeps growing
		// after a Stop is how the orphan check works.
		fmt.Println("READY")
		for {
			f, err := os.OpenFile(os.Args[2], os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err == nil {
				_, _ = f.WriteString("x")
				_ = f.Close()
			}
			time.Sleep(20 * time.Millisecond)
		}

	case "burst":
		// Writes a payload small enough to fit the pipe buffer and exits at
		// once, so the data is sitting in the pipe with no writer left alive.
		n, _ := strconv.Atoi(os.Args[2])
		payload := make([]byte, n)
		for i := range payload {
			payload[i] = byte('a' + i%26)
		}
		_, _ = os.Stdout.Write(payload)
		os.Exit(0)

	case "noisy-death":
		// A large diagnostic and then immediate exit.
		//
		// The volume is the point. A one-line message is copied by a single
		// Read, so the drain almost always wins the race against cmd.Wait. Four
		// kilobytes needs several reads, which widens the window to where the
		// race is observable rather than merely present.
		filler := strings.Repeat("diagnostic detail; ", 200)
		fmt.Fprintln(os.Stderr, filler)
		fmt.Fprintln(os.Stderr, "fatal: THE-LAST-WORD")
		os.Exit(5)

	case "last-words":
		// A final diagnostic on the way out. Losing this loses the only
		// explanation of why a provider died.
		fmt.Fprintln(os.Stderr, "fatal: the model file is corrupt")
		os.Exit(4)

	case "crash-after-ready":
		fmt.Println("READY")
		time.Sleep(50 * time.Millisecond)
		os.Exit(9)

	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q\n", mode)
		os.Exit(2)
	}
}

func echo(f func(string) string) {
	s := bufio.NewScanner(os.Stdin)
	for s.Scan() {
		fmt.Println(f(s.Text()))
	}
	os.Exit(0)
}
`

// helper compiles the child program, once.
func helper(t *testing.T) string {
	t.Helper()

	helperOnce.Do(buildHelper)

	if helperErr != nil {
		t.Skipf("cannot build the process helper: %v", helperErr)
	}
	return helperPath
}

// buildHelper compiles the child program. Extracted from helper so benchmarks,
// which have a *testing.B rather than a *testing.T, can share the same binary.
func buildHelper() {
	dir, err := os.MkdirTemp("", "voice-process-helper")
	if err != nil {
		helperErr = err
		return
	}

	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(helperSource), 0o600); err != nil {
		helperErr = err
		return
	}

	bin := filepath.Join(dir, "helper")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}

	build := exec.Command("go", "build", "-o", bin, src)
	if out, err := build.CombinedOutput(); err != nil {
		helperErr = fmt.Errorf("building helper: %v: %s", err, out)
		return
	}
	helperPath = bin
}

// testConfig returns a supervision config for the helper in a given mode.
func testConfig(t *testing.T, mode string) Config {
	t.Helper()

	return Config{
		Executable:     helper(t),
		Args:           []string{mode},
		StartTimeout:   5 * time.Second,
		StopTimeout:    500 * time.Millisecond,
		MaxStderrBytes: 8 << 10,
		Ready: func(line string) (bool, error) {
			switch {
			case strings.HasPrefix(line, "ERROR:"):
				return false, fmt.Errorf("%w: %s", ErrStartFailed, line)
			case line == "READY":
				return true, nil
			}
			return false, nil
		},
	}
}

// assertChildStopped proves a supervised child is no longer EXECUTING.
//
// # Why not os.FindProcess and a signal
//
// That is the obvious check and it is worthless on Windows: os.Process.Signal
// returns EWINDOWS for anything but Kill, so the probe reports "not alive" for
// every process including live ones, and the test passes without testing
// anything. An earlier version of this file did exactly that.
//
// This watches for the child's SIDE EFFECTS instead. The heartbeat mode appends
// to a file every 20ms; if the file stops growing, the child has stopped
// running. That is portable, it is the property §10 actually cares about, and
// it cannot silently degrade into a no-op.
func assertChildStopped(t *testing.T, heartbeatFile string) {
	t.Helper()

	size := func() int64 {
		info, err := os.Stat(heartbeatFile)
		if err != nil {
			return -1
		}
		return info.Size()
	}

	// Let any write already in flight land.
	time.Sleep(150 * time.Millisecond)
	before := size()
	time.Sleep(300 * time.Millisecond)
	after := size()

	if after != before {
		t.Errorf("the child is still running after Stop: its heartbeat file grew "+
			"from %d to %d bytes. Section 10 forbids orphan provider processes",
			before, after)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func TestProcess_StartsBecomesReadyAndStops(t *testing.T) {
	t.Parallel()

	p, err := New(testConfig(t, "ready"))
	if err != nil {
		t.Fatal(err)
	}

	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v\nstderr: %s", err, p.StderrTail())
	}
	if !p.Running() {
		t.Fatal("the process is not running after a successful Start")
	}

	if p.PID() == 0 {
		t.Fatal("no process identifier")
	}

	// It works: stdin in, stdout out.
	if _, err := p.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	select {
	case line := <-p.Lines():
		if line != "HELLO" {
			t.Errorf("read %q, want %q", line, "HELLO")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no output within five seconds")
	}

	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if p.Running() {
		t.Error("still running after Stop")
	}

}

// TestProcess_StopLeavesNoOrphan is §10's central guarantee, checked by
// watching the child's side effects rather than by asking the operating system.
func TestProcess_StopLeavesNoOrphan(t *testing.T) {
	t.Parallel()

	beat := filepath.Join(t.TempDir(), "beat")

	cfg := testConfig(t, "heartbeat")
	cfg.Args = []string{"heartbeat", beat}

	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Confirm it is genuinely beating before proving it stopped.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if info, err := os.Stat(beat); err == nil && info.Size() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the child never wrote a heartbeat; the test would prove nothing")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// This child ignores a closed stdin, so Stop must kill it.
	if err := p.Stop(context.Background()); !errors.Is(err, ErrStopTimeout) {
		t.Fatalf("Stop returned %v, want ErrStopTimeout", err)
	}

	assertChildStopped(t, beat)
}

func TestProcess_StopIsIdempotentAndSafeBeforeStart(t *testing.T) {
	t.Parallel()

	p, err := New(testConfig(t, "ready"))
	if err != nil {
		t.Fatal(err)
	}

	// Before Start.
	if err := p.Stop(context.Background()); err != nil {
		t.Errorf("Stop before Start returned %v", err)
	}

	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Errorf("second Stop returned %v", err)
	}
}

func TestProcess_RefusesASecondStart(t *testing.T) {
	t.Parallel()

	p, err := New(testConfig(t, "ready"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Stop(context.Background()) }()

	if err := p.Start(context.Background()); !errors.Is(err, ErrAlreadyStarted) {
		t.Errorf("a second Start returned %v, want ErrAlreadyStarted", err)
	}
}

// ---------------------------------------------------------------------------
// The failure modes §10 and §23 name
// ---------------------------------------------------------------------------

func TestProcess_MissingExecutableFailsCleanly(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t, "ready")
	cfg.Executable = filepath.Join(t.TempDir(), "does-not-exist")

	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	err = p.Start(context.Background())
	if !errors.Is(err, ErrStartFailed) {
		t.Fatalf("err = %v, want ErrStartFailed", err)
	}
	if p.Running() {
		t.Error("a process that never started reports running")
	}
}

func TestProcess_ReadinessTimeoutKillsTheChild(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t, "silent")
	cfg.StartTimeout = 250 * time.Millisecond

	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	err = p.Start(context.Background())
	elapsed := time.Since(start)

	if !errors.Is(err, ErrReadinessTimeout) {
		t.Fatalf("err = %v, want ErrReadinessTimeout", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("Start took %s to give up on a 250ms readiness timeout", elapsed)
	}

	// A child that never became ready must still be dead.
	if p.Running() {
		t.Error("the child survived a readiness timeout")
	}
}

// TestProcess_FatalStartupIsReportedImmediately proves a child that says it
// failed is not waited on until the timeout.
func TestProcess_FatalStartupIsReportedImmediately(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t, "fatal-stdout")
	cfg.StartTimeout = 10 * time.Second

	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	err = p.Start(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a child that printed a fatal error started successfully")
	}
	if elapsed > 3*time.Second {
		t.Errorf("took %s to notice a fatal error the child reported on line one; "+
			"the readiness probe should fail fast rather than wait out the timeout",
			elapsed)
	}
	if !strings.Contains(err.Error(), "cannot load model") {
		t.Errorf("the error does not carry the child's own message: %v", err)
	}
}

func TestProcess_ExitDuringStartupIsReported(t *testing.T) {
	t.Parallel()

	p, err := New(testConfig(t, "fatal"))
	if err != nil {
		t.Fatal(err)
	}

	err = p.Start(context.Background())
	if err == nil {
		t.Fatal("a child that exited during startup started successfully")
	}
	// The child's own diagnostics must reach the caller — that is what makes a
	// missing model actionable rather than mysterious.
	if !strings.Contains(err.Error(), "model not found") {
		t.Errorf("the error does not carry the child's stderr: %v", err)
	}
}

func TestProcess_CrashAfterReadyIsObservable(t *testing.T) {
	t.Parallel()

	p, err := New(testConfig(t, "crash-after-ready"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Stop(context.Background()) }()

	select {
	case <-p.Exited():
	case <-time.After(5 * time.Second):
		t.Fatal("the crash was never observed")
	}

	if p.Running() {
		t.Error("a crashed process reports running")
	}
	if p.ExitError() == nil {
		t.Error("a non-zero exit produced no error")
	}
	// A write after death must fail rather than block or panic.
	if _, err := p.Write([]byte("x\n")); err == nil {
		t.Error("writing to a dead process succeeded")
	}
}

// TestProcess_UnresponsiveChildIsKilled is the guarantee that there is no
// indefinite wait and no orphan.
func TestProcess_UnresponsiveChildIsKilled(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t, "ignore-stdin-close")
	cfg.StopTimeout = 200 * time.Millisecond

	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	err = p.Stop(context.Background())
	elapsed := time.Since(start)

	if !errors.Is(err, ErrStopTimeout) {
		t.Errorf("Stop returned %v, want ErrStopTimeout — a child that had to be "+
			"killed is worth counting, because it will eventually be killed "+
			"mid-write", err)
	}
	if !p.Killed() {
		t.Error("Killed() is false after a child had to be killed")
	}
	if elapsed > 3*time.Second {
		t.Errorf("Stop took %s against a 200ms grace period", elapsed)
	}
	if p.Running() {
		t.Error("still running after a kill")
	}
}

// TestProcess_StderrIsBoundedAndNeverBlocksTheChild covers the subtlest hazard
// in process supervision.
//
// A pipe nobody reads fills, and then the child blocks on write. A provider
// frozen mid-transcription because its diagnostics had nowhere to go is a hang
// with no obvious cause.
func TestProcess_StderrIsBoundedAndNeverBlocksTheChild(t *testing.T) {
	t.Parallel()

	const limit = 4 << 10

	cfg := testConfig(t, "stderr-flood")
	cfg.MaxStderrBytes = limit

	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Stop(context.Background()) }()

	// The child writes 4 MiB. If stderr were not drained it would block long
	// before finishing, and this would time out.
	deadline := time.Now().Add(10 * time.Second)
	for p.StderrWritten() < 1<<20 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	written := p.StderrWritten()
	if written < 1<<20 {
		t.Fatalf("the child wrote only %d bytes of stderr in ten seconds; it is "+
			"blocked on a pipe nobody is draining", written)
	}

	if got := len(p.StderrTail()); got > limit {
		t.Errorf("retained %d bytes of stderr against a %d limit; the buffer is "+
			"unbounded", got, limit)
	}
	t.Logf("child wrote %d bytes of stderr; %d retained (limit %d)",
		written, len(p.StderrTail()), limit)
}

// TestProcess_NoGoroutineLeak is the §10 guarantee, counted rather than
// assumed.
func TestProcess_NoGoroutineLeak(t *testing.T) {
	// Not parallel: it counts goroutines, and a concurrent test would be
	// counted too.
	settle := func() int {
		for i := 0; i < 50; i++ {
			runtime.GC()
			time.Sleep(10 * time.Millisecond)
			if n := runtime.NumGoroutine(); i > 5 {
				return n
			}
		}
		return runtime.NumGoroutine()
	}

	before := settle()

	for i := 0; i < 5; i++ {
		p, err := New(testConfig(t, "ready"))
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if _, err := p.Write([]byte("ping\n")); err != nil {
			t.Fatal(err)
		}
		<-p.Lines()
		if err := p.Stop(context.Background()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	}

	after := settle()

	// A small tolerance: the runtime's own goroutines come and go.
	if after > before+3 {
		t.Errorf("goroutines went from %d to %d across five supervised processes; "+
			"every goroutine this package starts must be owned by the WaitGroup "+
			"Stop waits on", before, after)
	}
	t.Logf("goroutines: %d before, %d after five start/stop cycles", before, after)
}

// TestProcess_StopReleasesEverythingUnderConcurrency drives the race Stop is
// most likely to lose.
func TestProcess_StopReleasesEverythingUnderConcurrency(t *testing.T) {
	t.Parallel()

	p, err := New(testConfig(t, "ready"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup

	// A writer, a reader and three stoppers, all at once.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_, _ = p.Write([]byte("x\n"))
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for range p.Lines() {
		}
	}()

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.Stop(context.Background())
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent writers, readers and stoppers deadlocked")
	}

	if p.Running() {
		t.Error("still running after Stop")
	}
}

// TestProcess_NilReadyStartsImmediately covers a program that only speaks when
// spoken to.
func TestProcess_NilReadyStartsImmediately(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t, "echo")
	cfg.Ready = nil
	cfg.StartTimeout = 30 * time.Second

	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Stop(context.Background()) }()

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Start took %s with no readiness probe; a program that produces "+
			"output only in response to input must not be waited on", elapsed)
	}

	if _, err := p.Write([]byte("abc\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-p.Lines():
		if line != "abc" {
			t.Errorf("read %q, want %q", line, "abc")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no output")
	}
}

func TestProcess_RefusesInvalidConfig(t *testing.T) {
	t.Parallel()

	base := testConfig(t, "ready")

	for name, mutate := range map[string]func(*Config){
		"no executable":    func(c *Config) { c.Executable = "" },
		"no start timeout": func(c *Config) { c.StartTimeout = 0 },
		"no stop timeout":  func(c *Config) { c.StopTimeout = 0 },
		"unbounded stderr": func(c *Config) { c.MaxStderrBytes = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := base
			mutate(&cfg)
			if _, err := New(cfg); err == nil {
				t.Error("an invalid configuration was accepted")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The bounded stderr ring
// ---------------------------------------------------------------------------

func TestRing_KeepsTheMostRecentBytes(t *testing.T) {
	t.Parallel()

	r := newRing(8)

	r.write([]byte("abc"))
	if got := r.string(); got != "abc" {
		t.Errorf("partial ring = %q, want %q", got, "abc")
	}

	r.write([]byte("defgh"))
	if got := r.string(); got != "abcdefgh" {
		t.Errorf("exactly full ring = %q, want %q", got, "abcdefgh")
	}

	// Wrapping keeps the END, because that is where a crash explains itself.
	r.write([]byte("ij"))
	if got := r.string(); got != "cdefghij" {
		t.Errorf("wrapped ring = %q, want %q", got, "cdefghij")
	}

	// A single write larger than the ring is trimmed, not wrapped repeatedly.
	r.write([]byte("0123456789ABCDEF"))
	if got := r.string(); got != "89ABCDEF" {
		t.Errorf("oversized write = %q, want %q", got, "89ABCDEF")
	}

	if r.total != 3+5+2+16 {
		t.Errorf("total = %d, want %d", r.total, 3+5+2+16)
	}
}

func TestRing_IsSafeUnderConcurrentWrites(t *testing.T) {
	t.Parallel()

	r := newRing(1024)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				r.write([]byte("0123456789"))
				_ = r.string()
			}
		}()
	}
	wg.Wait()

	if got := len(r.string()); got > 1024 {
		t.Errorf("ring holds %d bytes against a 1024 limit", got)
	}
	if r.total != 8*200*10 {
		t.Errorf("total = %d, want %d", r.total, 8*200*10)
	}
}

// TestProcess_EnvironmentIsAllowlistedNotEmptied is a regression from Task 6.
//
// A non-nil exec.Cmd.Env REPLACES the parent's environment rather than
// extending it. An adapter that set one variable therefore handed its child
// exactly that one variable — no PATH, no SystemRoot — and a Python provider in
// that state died before printing anything. The symptom was an empty transcript
// with no error, on audio that transcribed perfectly by hand.
func TestProcess_EnvironmentIsAllowlistedNotEmptied(t *testing.T) {
	t.Parallel()

	t.Run("nothing configured inherits everything", func(t *testing.T) {
		t.Parallel()
		// nil tells exec to inherit, which is the right default for a caller
		// that has not thought about it.
		if env := (Config{}).buildEnv(); env != nil {
			t.Errorf("buildEnv with no configuration = %v, want nil", env)
		}
	})

	t.Run("inheritance carries the named variables", func(t *testing.T) {
		t.Parallel()

		cfg := Config{InheritEnv: DefaultInheritEnv(), Env: []string{"EXTRA=1"}}
		env := cfg.buildEnv()

		var sawExtra bool
		byKey := map[string]string{}
		for _, e := range env {
			k, v, _ := strings.Cut(e, "=")
			byKey[k] = v
			if e == "EXTRA=1" {
				sawExtra = true
			}
		}

		if !sawExtra {
			t.Error("the explicit entry was dropped")
		}
		if os.Getenv("PATH") != "" && byKey["PATH"] == "" {
			t.Error("PATH was not inherited; a program that cannot find its " +
				"libraries does not start")
		}
	})

	t.Run("a real child receives a working environment", func(t *testing.T) {
		t.Parallel()

		// The helper is a Go program, so it needs far less than Python does —
		// but if the environment were emptied entirely this would still be the
		// place a Windows child failed to start.
		cfg := testConfig(t, "ready")
		cfg.InheritEnv = DefaultInheritEnv()
		cfg.Env = []string{"AEGIS_MARKER=1"}

		p, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Start(context.Background()); err != nil {
			t.Fatalf("a child with an allowlisted environment did not start: %v\n"+
				"stderr: %s", err, p.StderrTail())
		}
		defer func() { _ = p.Stop(context.Background()) }()

		if _, err := p.Write([]byte("ping\n")); err != nil {
			t.Fatal(err)
		}
		select {
		case line := <-p.Lines():
			if line != "PING" {
				t.Errorf("read %q, want PING", line)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no output from a child with an allowlisted environment")
		}
	})
}

// TestProcess_UnlistedVariablesAreNotInherited is the credential-exclusion half
// of the allowlist.
//
// A separate top-level test rather than a subtest: t.Setenv mutates process
// state, and the testing package refuses to combine it with t.Parallel — which
// the parent test uses.
func TestProcess_UnlistedVariablesAreNotInherited(t *testing.T) {
	t.Setenv("AEGIS_PROC_TEST_SECRET", "sk-must-not-be-inherited")

	cfg := Config{InheritEnv: DefaultInheritEnv()}
	for _, e := range cfg.buildEnv() {
		if strings.Contains(e, "sk-must-not-be-inherited") {
			t.Fatalf("an unlisted variable reached the child: %q", e)
		}
	}
}

// ---------------------------------------------------------------------------
// Output that outlives the child
// ---------------------------------------------------------------------------
//
// A child's last bytes are the ones that matter most: the tail of an utterance,
// and the diagnostic a dying provider prints on its way out. Both are still
// sitting in a pipe when the process ends, and both are lost if the supervisor
// reaps the child before its readers have drained.
//
// This is the exact hazard os/exec documents for StdoutPipe: "it is incorrect to
// call Wait before all reads from the pipe have completed", because Wait closes
// the pipe. A supervisor that reaps in the background is doing precisely that,
// and the symptom — audio that stops a few frames early — reads like a bad line
// rather than a bug.

func TestProcess_RawStdoutSurvivesTheChildBeingReaped(t *testing.T) {
	t.Parallel()

	const size = 3000 // fits the pipe buffer, so the child exits without blocking

	cfg := testConfig(t, "burst")
	cfg.Args = []string{"burst", "3000"}
	cfg.RawStdout = true
	cfg.Ready = nil // mutually exclusive with RawStdout

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Stop(context.Background()) }()

	// Wait for the child to be gone AND reaped before reading a single byte.
	// The data is in the pipe; nothing about the child having exited makes it
	// unreadable, and a supervisor that makes it unreadable is dropping audio.
	<-p.Exited()

	got, err := io.ReadAll(p.Stdout())
	if err != nil {
		t.Fatalf("reading a reaped child's output: %v", err)
	}
	if len(got) != size {
		t.Errorf("read %d of %d bytes the child wrote before exiting: the tail of "+
			"the stream was lost when the child was reaped", len(got), size)
	}
}

func TestProcess_StderrSurvivesTheChildBeingReaped(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t, "last-words")
	cfg.Ready = nil // this child never announces readiness; it just dies

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Stop(context.Background()) }()

	<-p.Exited()

	// The drain goroutine may still be finishing; Stop is what joins it.
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if tail := p.StderrTail(); !strings.Contains(tail, "the model file is corrupt") {
		t.Errorf("the child's final diagnostic was lost; StderrTail holds %q. "+
			"That message is the only explanation of why the provider died", tail)
	}
}

// TestProcess_ExitedImpliesStderrIsComplete pins the contract three adapters
// depend on.
//
// # The defect this was written for
//
// Gate 4 of Task 19 caught piper reporting a dead engine with an EMPTY stderr:
//
//	piper: synthesis failed: the engine exited with exit status 7; stderr:
//
// reap closes Exited immediately after cmd.Wait, while drainStderr is a
// separate goroutine unblocked by the same event — the child exiting. A caller
// that waits for Exited and then reads StderrTail is therefore racing the
// drain, and loses often enough to matter under load.
//
// Losing that race destroys exactly the diagnostic that matters most: a
// provider that died mid-utterance, with its own explanation of why. "exit
// status 7" alone sends an operator nowhere.
//
// Every adapter in this module waits on Exited, so the fix belongs here rather
// than being repeated — and forgotten — in each of them.
func TestProcess_ExitedImpliesStderrIsComplete(t *testing.T) {
	t.Parallel()

	// UNDER LOAD, because that is the condition the defect was observed in.
	//
	// In isolation the drain goroutine almost always wins: the data is already
	// in the pipe when the child exits, and cmd.Wait does more work than a
	// couple of reads. Gate 4 runs the whole module ten times over with tests
	// in parallel, and under that pressure the drain gets descheduled and
	// loses. Saturating the scheduler here recreates it.
	stop := make(chan struct{})
	// YIELDING pressure, not starvation.
	//
	// These goroutines make the scheduler busy enough that the drain is not run
	// the instant the child exits — the condition gate 4 produces — while
	// yielding so neighbouring tests still make progress. A previous version
	// spun without yielding and pushed TestProcess_ReadinessTimeoutKillsTheChild
	// from 250ms to 3.4s: a test that breaks its neighbours is a broken test,
	// however good its own assertion.
	for i := 0; i < runtime.NumCPU(); i++ {
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
					runtime.Gosched()
				}
			}
		}()
	}
	defer close(stop)

	for i := 0; i < 30; i++ {
		cfg := testConfig(t, "noisy-death")
		cfg.Ready = nil // this child never announces readiness; it just dies

		p, err := New(cfg)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := p.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}

		// The ONLY synchronisation a caller is given. No Stop, because the
		// adapters do not call Stop before reading the diagnostic — that is
		// precisely the reporting path under test.
		<-p.Exited()

		if tail := p.StderrTail(); !strings.Contains(tail, "THE-LAST-WORD") {
			t.Fatalf("iteration %d: Exited was closed but the child's final line "+
				"is missing; %d bytes captured. Its own explanation of why it "+
				"died was lost", i, len(tail))
		}

		_ = p.Stop(context.Background())
	}
}
