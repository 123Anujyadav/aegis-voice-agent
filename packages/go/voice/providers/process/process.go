// Package process supervises the external programs local voice providers run.
//
// # Why this exists once rather than three times
//
// whisper.cpp, the Python whisper CLI and Piper are all "spawn a program, feed
// it, read from it, stop it". Each adapter writing that itself would produce
// three subtly different versions, and the differences would be in the parts
// nobody exercises until production: what happens when the child ignores a
// termination signal, what happens when it writes to stderr faster than anyone
// reads, what happens to the reader goroutine when the parent gives up.
//
// Every one of those is a leak that presents as a slow crash days later.
//
// # What it guarantees
//
//   - No orphan. Stop always ends with the child reaped, by signal if it goes
//     quietly and by kill if it does not.
//   - No goroutine leak. Every goroutine this package starts is owned by a
//     WaitGroup that Stop waits on.
//   - No unbounded buffer. Stderr is a fixed ring; a chatty child overwrites
//     its own oldest output rather than growing the host's memory.
//   - No indefinite wait. Every blocking operation has a deadline.
//
// # No shell, ever
//
// Processes are started with exec.Command(path, args...), which passes an argv
// vector to the operating system. Nothing in this package builds a command
// string, so text originating from a caller cannot become a command — see
// TestConfig_ArgvIsDataNotCommandLine in the parent package, which proves it
// against a real process.
//
// This package imports only the standard library.
package process

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// Errors this package returns. Callers match with errors.Is.
var (
	// ErrNotStarted is returned by an operation on a process that is not
	// running.
	ErrNotStarted = errors.New("process: not started")

	// ErrAlreadyStarted is returned by a second Start.
	ErrAlreadyStarted = errors.New("process: already started")

	// ErrStartFailed is returned when the program could not be launched.
	ErrStartFailed = errors.New("process: start failed")

	// ErrExited is returned when the child is gone.
	ErrExited = errors.New("process: exited")

	// ErrReadinessTimeout is returned when the child did not become ready.
	ErrReadinessTimeout = errors.New("process: readiness timeout")

	// ErrStopTimeout is returned when a graceful stop did not complete and the
	// child had to be killed.
	//
	// REPORTED, NOT SWALLOWED. The child is dead either way, but "it needed
	// killing" is a fact about the provider worth counting: a program that
	// never exits cleanly will eventually be killed mid-write.
	ErrStopTimeout = errors.New("process: graceful stop timed out, killed")
)

// Config describes one supervised program.
//
// The low-level shape. The parent package's ProcessConfig validates operator
// input and produces one of these; keeping them separate is what lets an
// adapter depend on process supervision without depending on the whole voice
// runtime.
type Config struct {
	// Executable is the absolute path to the program.
	Executable string

	// Args is the argument VECTOR, never a command string.
	Args []string

	// Env is set on the child in addition to InheritEnv.
	//
	// A non-nil exec.Cmd.Env REPLACES the environment rather than extending it,
	// which is a sharp edge: setting one variable here and nothing else leaves
	// the child with exactly that one variable, no PATH and no SystemRoot. On
	// Windows a Python program in that state dies before it prints anything,
	// and the symptom is a provider that returns no results and no error.
	//
	// That happened during development. InheritEnv exists so it cannot happen
	// again — see TestProcess_EnvironmentIsAllowlistedNotEmptied.
	Env []string

	// InheritEnv names variables to copy from the parent's environment.
	//
	// AN ALLOWLIST, NEVER THE WHOLE ENVIRONMENT. A child started with everything
	// the operator exported receives every API key and connection string they
	// happen to have. [DefaultInheritEnv] is the minimum a program needs to
	// start; adding to it is a visible decision.
	InheritEnv []string

	// Dir is the working directory. Empty means the parent's.
	Dir string

	// StartTimeout bounds the wait for readiness.
	StartTimeout time.Duration

	// StopTimeout is the grace period before the child is killed.
	StopTimeout time.Duration

	// MaxStderrBytes bounds retained stderr.
	MaxStderrBytes int

	// RawStdout hands the child's stdout to the caller as a byte stream instead
	// of scanning it into lines.
	//
	// FOR PROVIDERS THAT EMIT AUDIO. Piper writes headerless PCM to stdout, and
	// a line scanner over binary would split on whatever bytes happened to be
	// 0x0A and discard them — silently corrupting the audio rather than failing.
	//
	// Mutually exclusive with Ready: a readiness probe reads lines, and the
	// stream cannot be both consumed here and handed over intact.
	RawStdout bool

	// Ready decides when the child is usable, from its stdout lines.
	//
	// Optional. A nil Ready means the process is considered ready as soon as it
	// has been launched, which is right for a program that produces output only
	// in response to input.
	//
	// Returning an error fails the start — a child that prints a fatal error on
	// line one should not be waited on until the readiness timeout expires.
	Ready func(line string) (ready bool, err error)
}

// DefaultInheritEnv returns the minimum a program needs in order to start.
//
// Deliberately short. Every entry is something a child genuinely requires to
// find its libraries, resolve its interpreter or write a temporary file — not a
// convenience, and certainly not the operator's whole shell.
func DefaultInheritEnv() []string {
	return []string{
		"PATH", "SystemRoot", "windir", "COMSPEC", "PATHEXT",
		"TEMP", "TMP", "TMPDIR", "HOME", "USERPROFILE",
		"LANG", "LC_ALL",
		// Python resolves its standard library through these; a Python-based
		// provider started without them fails before printing anything.
		"PYTHONPATH", "PYTHONHOME", "VIRTUAL_ENV",
	}
}

// buildEnv assembles the child's environment: the allowlisted inheritances
// first, then the explicit entries, which therefore win.
//
// Returns nil when nothing is configured, which lets exec inherit the parent's
// environment — the right behaviour for a caller that has not thought about it,
// and the one every other Go program has.
func (c Config) buildEnv() []string {
	if len(c.InheritEnv) == 0 && len(c.Env) == 0 {
		return nil
	}

	env := make([]string, 0, len(c.InheritEnv)+len(c.Env))
	for _, key := range c.InheritEnv {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return append(env, c.Env...)
}

func (c Config) validate() error {
	switch {
	case c.Executable == "":
		return fmt.Errorf("%w: no executable", ErrStartFailed)
	case c.StartTimeout <= 0:
		return fmt.Errorf("%w: StartTimeout must be positive", ErrStartFailed)
	case c.StopTimeout <= 0:
		return fmt.Errorf("%w: StopTimeout must be positive", ErrStartFailed)
	case c.MaxStderrBytes <= 0:
		return fmt.Errorf("%w: MaxStderrBytes must be positive", ErrStartFailed)
	case c.RawStdout && c.Ready != nil:
		return fmt.Errorf("%w: RawStdout and Ready are mutually exclusive — a "+
			"readiness probe consumes the stdout lines that RawStdout hands over "+
			"intact", ErrStartFailed)
	}
	return nil
}

// Process is one supervised external program.
//
// Safe for concurrent use. An adapter typically writes from one goroutine and
// reads from another, while a supervisor may call Stop from a third.
type Process struct {
	cfg Config

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	started bool
	stopped bool

	// stdout carries lines the child printed, after readiness. Unused when
	// RawStdout is set.
	stdout chan string

	// rawStdout is the child's stdout pipe, handed over intact when RawStdout
	// is set.
	rawStdout io.ReadCloser

	// exited is closed when the child is gone, and exitErr records why.
	exited chan struct{}

	// stderrDone is closed when the drain goroutine has copied everything the
	// child ever wrote. See [Process.Exited] for why the two are sequenced.
	stderrDone chan struct{}
	exitErr    error

	// stderr is a bounded ring of the child's diagnostics.
	stderr *ring

	// wg owns every goroutine this Process starts. Stop waits on it, which is
	// what makes "no goroutine leak" checkable rather than hoped for.
	wg sync.WaitGroup

	// killed records that the child had to be killed rather than exiting.
	killed atomic.Bool
}

// New builds a supervised process. Nothing is launched until Start.
func New(cfg Config) (*Process, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Process{
		cfg:        cfg,
		stdout:     make(chan string, stdoutBuffer),
		exited:     make(chan struct{}),
		stderrDone: make(chan struct{}),
		stderr:     newRing(cfg.MaxStderrBytes),
	}, nil
}

// stdoutBuffer bounds queued stdout lines.
//
// A child that prints faster than the adapter reads will block on its own
// write, which is correct backpressure: the alternative is this package
// buffering an unbounded amount of a program's output.
const stdoutBuffer = 256

// Start launches the program and waits for readiness.
//
// Cancelling ctx during startup terminates the child; it does not leave a
// half-started process behind.
func (p *Process) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return ErrAlreadyStarted
	}

	// exec.CommandContext deliberately NOT used: it kills the child when ctx
	// ends, and ctx here is the START context. A process supervised for the
	// length of a call must outlive the call that started it, and tying its
	// lifetime to a startup context is how a provider dies the moment setup
	// returns.
	cmd := exec.Command(p.cfg.Executable, p.cfg.Args...) //nolint:gosec // argv vector, never a shell string; path validated by the caller
	cmd.Env = p.cfg.buildEnv()
	cmd.Dir = p.cfg.Dir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		p.mu.Unlock()
		return fmt.Errorf("%w: stdin pipe: %v", ErrStartFailed, err)
	}
	// PIPES THIS PACKAGE OWNS, NOT cmd.StdoutPipe.
	//
	// os/exec is explicit that "it is incorrect to call Wait before all reads
	// from the pipe have completed", because Wait CLOSES the pipes it created.
	// A supervisor reaps in the background by design — that is what makes exit
	// status observable without a caller blocking — so a reader draining the
	// child's output is always racing Wait.
	//
	// The child's last bytes are the ones that lose that race, and they are the
	// ones that matter: the tail of an utterance, and the diagnostic a dying
	// provider prints on its way out. Truncated audio reads as a bad line
	// rather than a bug, which is how this survived until a concurrent test
	// caught it.
	//
	// Pipes created here belong to this package. Wait does not touch them, so
	// data already in the pipe stays readable after the child is gone.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		p.mu.Unlock()
		return fmt.Errorf("%w: stdout pipe: %v", ErrStartFailed, err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_, _ = stdoutR.Close(), stdoutW.Close()
		p.mu.Unlock()
		return fmt.Errorf("%w: stderr pipe: %v", ErrStartFailed, err)
	}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	if err := cmd.Start(); err != nil {
		_, _ = stdoutR.Close(), stdoutW.Close()
		_, _ = stderrR.Close(), stderrW.Close()
		p.mu.Unlock()
		return fmt.Errorf("%w: %v", ErrStartFailed, err)
	}

	// The child holds its own handles now. Releasing the parent's copies is
	// what lets a reader see EOF when the child exits — hold them and the
	// stream never ends.
	_, _ = stdoutW.Close(), stderrW.Close()

	stdout, stderr := stdoutR, stderrR

	p.cmd = cmd
	p.stdin = stdin
	p.started = true
	p.mu.Unlock()

	ready := make(chan error, 1)

	if p.cfg.RawStdout {
		// Handed over intact. No scanner runs, so nothing splits the byte
		// stream and nothing consumes it before the caller does.
		p.mu.Lock()
		p.rawStdout = stdout
		p.mu.Unlock()
		close(p.stdout)
	} else {
		p.wg.Add(1)
		go p.pumpStdout(stdout, ready)
	}

	p.wg.Add(1)
	go p.drainStderr(stderr)

	p.wg.Add(1)
	go p.reap()

	return p.awaitReady(ctx, ready)
}

// awaitReady blocks until the child is usable, fails, or the deadline passes.
func (p *Process) awaitReady(ctx context.Context, ready <-chan error) error {
	// A process with no readiness probe is usable as soon as it is launched.
	// Waiting for output from a program that only speaks when spoken to would
	// hang until the timeout on every start.
	if p.cfg.Ready == nil {
		return nil
	}

	timer := time.NewTimer(p.cfg.StartTimeout)
	defer timer.Stop()

	select {
	case err := <-ready:
		if err != nil {
			// Stop joins every goroutine this Process started, including the
			// stderr drain — so once it returns, the ring holds everything the
			// child ever wrote and the tail below is complete.
			_ = p.Stop(context.Background())
			if errors.Is(err, ErrExited) {
				return fmt.Errorf("%w\nstderr: %s", err, p.StderrTail())
			}
			return err
		}
		return nil

	case <-p.exited:
		_ = p.Stop(context.Background())
		return fmt.Errorf("%w during startup: %v\nstderr: %s",
			ErrExited, p.exitError(), p.StderrTail())

	case <-ctx.Done():
		_ = p.Stop(context.Background())
		return fmt.Errorf("%w: %v", ErrStartFailed, ctx.Err())

	case <-timer.C:
		_ = p.Stop(context.Background())
		return fmt.Errorf("%w after %s\nstderr: %s",
			ErrReadinessTimeout, p.cfg.StartTimeout, p.StderrTail())
	}
}

// pumpStdout reads the child's stdout, resolving readiness and forwarding the
// rest.
func (p *Process) pumpStdout(r io.ReadCloser, ready chan<- error) {
	defer p.wg.Done()
	defer close(p.stdout)
	// This pipe is this package's to close now that Wait no longer does it.
	defer func() { _ = r.Close() }()

	resolved := p.cfg.Ready == nil
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, scannerInitial), scannerMax)

	for scanner.Scan() {
		line := scanner.Text()

		if !resolved {
			isReady, err := p.cfg.Ready(line)
			switch {
			case err != nil:
				resolved = true
				ready <- err
				continue
			case isReady:
				resolved = true
				ready <- nil
				continue
			}
			// Not ready yet and no error: readiness banners are not results,
			// so this line is consumed rather than forwarded.
			continue
		}

		select {
		case p.stdout <- line:
		case <-p.exited:
			return
		}
	}

	// The scanner stopped. If readiness was never resolved, unblock the waiter
	// rather than leaving it to time out — the pipe closing means the child is
	// gone, which is a faster and more accurate answer.
	if !resolved {
		// The DIAGNOSIS is deliberately not composed here. This goroutine and
		// the stderr drain are unblocked by the same event — the child exiting
		// — so reading the stderr ring at this point is a race, and the side
		// that loses it produces "exited before readiness" with nothing after
		// it. That is the one message where the child's own explanation is the
		// whole value.
		//
		// awaitReady adds it after Stop has joined the drain goroutine.
		ready <- fmt.Errorf("%w before readiness", ErrExited)
	}
}

const (
	scannerInitial = 4 << 10
	scannerMax     = 1 << 20
)

// drainStderr reads stderr into the bounded ring.
//
// # It must always drain, even when nobody wants the output
//
// A pipe nobody reads fills its operating-system buffer, and then the child
// BLOCKS on write. A provider frozen mid-transcription because its diagnostics
// had nowhere to go is a hang with no obvious cause, and it is the classic way
// process supervision goes wrong.
func (p *Process) drainStderr(r io.ReadCloser) {
	defer p.wg.Done()
	defer close(p.stderrDone)
	defer func() { _ = r.Close() }()

	buf := make([]byte, stderrChunk)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			p.stderr.write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

const stderrChunk = 4 << 10

// reap waits for the child and records why it ended.
func (p *Process) reap() {
	defer p.wg.Done()

	err := p.cmd.Wait()

	p.mu.Lock()
	p.exitErr = err
	p.mu.Unlock()

	// THE DRAIN FINISHES BEFORE THE CHILD IS DECLARED GONE.
	//
	// Wait and the stderr drain are unblocked by the same event — the child
	// exiting — so a caller that waits on Exited and then reads StderrTail is
	// racing a goroutine, and loses it under load. The symptom is the worst
	// possible one: a provider that died mid-utterance reported with an EMPTY
	// stderr, which is exactly the case where its own explanation is the only
	// thing an operator has. Measured: 0 bytes captured, deterministically,
	// with the scheduler saturated.
	//
	// Every adapter in this module waits on Exited, so sequencing it here fixes
	// all of them rather than requiring each to remember.
	//
	// The wait is BOUNDED because it cannot be unconditional: a grandchild that
	// inherited the stderr handle keeps the pipe open after the child is gone,
	// and an unbounded wait would mean Exited never closes for anybody. On
	// expiry the behaviour degrades to what it was before — a possibly empty
	// tail — rather than to a hang.
	timer := time.NewTimer(stderrDrainGrace)
	select {
	case <-p.stderrDone:
	case <-timer.C:
	}
	timer.Stop()

	close(p.exited)
}

// stderrDrainGrace bounds how long reap waits for the stderr drain.
//
// Generous relative to the work: draining a pipe that has already reached EOF
// is microseconds, and this only has to survive the scheduler being busy. Short
// relative to a call: a wedged grandchild delays exit notification by this much
// and no more.
const stderrDrainGrace = 2 * time.Second

// Write sends bytes to the child's stdin.
func (p *Process) Write(b []byte) (int, error) {
	p.mu.Lock()
	stdin, started, stopped := p.stdin, p.started, p.stopped
	p.mu.Unlock()

	switch {
	case !started:
		return 0, ErrNotStarted
	case stopped:
		return 0, ErrExited
	}

	select {
	case <-p.exited:
		return 0, fmt.Errorf("%w: %v", ErrExited, p.exitError())
	default:
	}

	return stdin.Write(b)
}

// CloseStdin signals end of input. Idempotent.
func (p *Process) CloseStdin() error {
	p.mu.Lock()
	stdin := p.stdin
	p.stdin = nil
	p.mu.Unlock()

	if stdin == nil {
		return nil
	}
	return stdin.Close()
}

// Lines yields the child's stdout, one line at a time. Closed when the child's
// stdout ends.
//
// Closed immediately when RawStdout is set: the bytes go to [Process.Stdout]
// instead, and a caller reading both would race for them.
func (p *Process) Lines() <-chan string { return p.stdout }

// Stdout returns the child's raw stdout, or nil unless RawStdout is set.
//
// Reads return io.EOF when the child's stdout closes, which is the signal that
// no further audio is coming.
func (p *Process) Stdout() io.Reader {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rawStdout == nil {
		return nil
	}
	return p.rawStdout
}

// Exited is closed when the child is gone AND everything it wrote to stderr has
// been captured.
//
// The second half of that sentence is load-bearing: adapters wait here and then
// read [Process.StderrTail] to explain why a provider died, and without the
// sequencing in reap that read races the drain goroutine.
func (p *Process) Exited() <-chan struct{} { return p.exited }

// ExitError returns why the child ended, or nil if it ended cleanly or is still
// running.
func (p *Process) ExitError() error { return p.exitError() }

func (p *Process) exitError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitErr
}

// Killed reports whether the child had to be killed rather than exiting.
func (p *Process) Killed() bool { return p.killed.Load() }

// Running reports whether the child is alive.
func (p *Process) Running() bool {
	p.mu.Lock()
	started, stopped := p.started, p.stopped
	p.mu.Unlock()

	if !started || stopped {
		return false
	}
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}

// PID returns the child's process identifier, or 0.
//
// For tests that verify no orphan survives. Not otherwise useful.
func (p *Process) PID() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// StderrTail returns the retained stderr.
//
// Bounded by MaxStderrBytes and holding the MOST RECENT bytes: a program that
// fails after printing a megabyte of progress explains itself at the end.
func (p *Process) StderrTail() string { return p.stderr.string() }

// Stop ends the child and waits for every goroutine this Process started.
//
// # Graceful, then not
//
// Stdin is closed first, which is how most well-behaved programs are asked to
// finish. If the child has not exited within StopTimeout it is killed.
//
// Returns ErrStopTimeout when the kill was needed. The child is dead either
// way, but a provider that never exits cleanly is worth counting: it will
// eventually be killed mid-write, and the resulting truncated output will look
// like a different bug.
//
// Idempotent. Safe to call on a process that never started.
func (p *Process) Stop(ctx context.Context) error {
	p.mu.Lock()
	if !p.started || p.stopped {
		p.mu.Unlock()
		p.wg.Wait()
		return nil
	}
	p.stopped = true
	cmd, stdin := p.cmd, p.stdin
	p.stdin = nil
	p.mu.Unlock()

	// Ask nicely: closing stdin is how a filter program is told to finish.
	if stdin != nil {
		_ = stdin.Close()
	}

	// A caller parked in a raw stdout read would otherwise block until the
	// child chose to exit. Closing the pipe makes that read return promptly,
	// which is what the abort budget needs.
	p.mu.Lock()
	raw := p.rawStdout
	p.mu.Unlock()
	if raw != nil {
		_ = raw.Close()
	}

	timer := time.NewTimer(p.cfg.StopTimeout)
	defer timer.Stop()

	var stopErr error
	select {
	case <-p.exited:
		// Went quietly.

	case <-ctx.Done():
		stopErr = p.kill(cmd)

	case <-timer.C:
		stopErr = p.kill(cmd)
	}

	// EVERY goroutine, before returning. This is what makes "no goroutine leak"
	// a property a test can check by counting.
	p.wg.Wait()
	return stopErr
}

// kill terminates a child that would not stop, and waits for it to be reaped.
func (p *Process) kill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}

	p.killed.Store(true)
	_ = cmd.Process.Kill()

	// Wait for reap to observe the death. Without this, Stop could return while
	// the operating system still holds the process — which is exactly the
	// orphan the test looks for.
	<-p.exited

	return fmt.Errorf("%w after %s", ErrStopTimeout, p.cfg.StopTimeout)
}

// ---------------------------------------------------------------------------
// Bounded stderr
// ---------------------------------------------------------------------------

// ring is a fixed-size byte buffer that overwrites its oldest content.
//
// A child writing progress to stderr on every frame would otherwise grow this
// for the length of a call. Keeping the most recent bytes rather than the first
// keeps the part that explains a crash.
type ring struct {
	mu    sync.Mutex
	buf   []byte
	next  int
	full  bool
	total int64
}

func newRing(size int) *ring { return &ring{buf: make([]byte, size)} }

func (r *ring) write(b []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.total += int64(len(b))

	// Only the last len(buf) bytes can survive, so a huge write is trimmed
	// before it is copied rather than wrapping the ring several times.
	if len(b) >= len(r.buf) {
		copy(r.buf, b[len(b)-len(r.buf):])
		r.next = 0
		r.full = true
		return
	}

	n := copy(r.buf[r.next:], b)
	if n < len(b) {
		copy(r.buf, b[n:])
		r.full = true
	}
	r.next = (r.next + len(b)) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

func (r *ring) string() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.full {
		return string(r.buf[:r.next])
	}
	out := make([]byte, 0, len(r.buf))
	out = append(out, r.buf[r.next:]...)
	out = append(out, r.buf[:r.next]...)
	return string(out)
}

// Written returns how many stderr bytes the child produced in total, including
// any the ring discarded.
func (p *Process) StderrWritten() int64 {
	p.stderr.mu.Lock()
	defer p.stderr.mu.Unlock()
	return p.stderr.total
}
