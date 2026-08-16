package process

import (
	"context"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Supervision overhead
// ---------------------------------------------------------------------------
//
// # What is being measured, and what would be dishonest to measure
//
// Supervision is: build the argv vector and the allowlisted environment, create
// three pipes, fork and exec, start the reader goroutines, wait for readiness,
// then signal, reap and join. Every one of those is this package's cost.
//
// What a supervised program DOES is not. A benchmark that spawned whisper and
// reported the total would be reporting transcription time labelled as
// supervision overhead, and the number would move whenever the model changed
// while this code stayed identical.
//
// So the child here is the smallest program that can participate in the
// protocol: it announces readiness, echoes a line, and exits. Its own work is
// a few microseconds of I/O, which is the floor of what any child can cost —
// and it is reported separately below so a reader can subtract it.

// BenchmarkProcess_StartStop measures one full supervision cycle against a real
// operating-system process.
//
// The dominant term is fork/exec, which belongs to the OS rather than to this
// package. It is included because it is unavoidable: a provider adapter cannot
// have a supervised process without paying it, and knowing the figure is what
// decides whether a provider can be started per turn or must be long-lived.
func BenchmarkProcess_StartStop(b *testing.B) {
	bin := benchHelper(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p, err := New(benchConfig(bin, "ready"))
		if err != nil {
			b.Fatal(err)
		}
		if err := p.Start(context.Background()); err != nil {
			b.Fatalf("Start: %v\nstderr: %s", err, p.StderrTail())
		}
		if err := p.Stop(context.Background()); err != nil {
			b.Fatalf("Stop: %v", err)
		}
	}
}

// BenchmarkProcess_Configure measures everything BEFORE the operating system
// is involved: validation, the argv vector and the environment allowlist.
//
// This is the part of supervision this package can actually make faster, and
// separating it is what makes the previous number interpretable.
func BenchmarkProcess_Configure(b *testing.B) {
	cfg := benchConfig(benchHelper(b), "ready")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := cfg.validate(); err != nil {
			b.Fatal(err)
		}
		if env := cfg.buildEnv(); len(env) == 0 {
			b.Fatal("empty environment")
		}
		if _, err := New(cfg); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkProcess_WriteRead measures one request/response exchange with a
// live child: a line in on stdin, a line out on stdout.
//
// The child uppercases the line, so its own work is negligible and what
// remains is the pipe handover — the per-utterance cost an adapter pays.
func BenchmarkProcess_WriteRead(b *testing.B) {
	p, err := New(benchConfig(benchHelper(b), "ready"))
	if err != nil {
		b.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		b.Fatalf("Start: %v\nstderr: %s", err, p.StderrTail())
	}
	defer func() { _ = p.Stop(context.Background()) }()

	line := []byte("the quick brown fox jumps over the lazy dog\n")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := p.Write(line); err != nil {
			b.Fatal(err)
		}
		select {
		case out, ok := <-p.Lines():
			if !ok {
				b.Fatal("the child's output ended")
			}
			if len(out) == 0 {
				b.Fatal("empty line")
			}
		case <-time.After(10 * time.Second):
			b.Fatal("no response from the child")
		}
	}
}

// BenchmarkProcess_StderrRing measures the bounded diagnostic buffer, which
// absorbs whatever a chatty provider writes and must stay cheap under flood.
func BenchmarkProcess_StderrRing(b *testing.B) {
	r := newRing(8 << 10)
	chunk := make([]byte, 512)
	for i := range chunk {
		chunk[i] = 'x'
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.write(chunk)
	}
}

func benchConfig(bin, mode string) Config {
	return Config{
		Executable:     bin,
		Args:           []string{mode},
		InheritEnv:     DefaultInheritEnv(),
		StartTimeout:   10 * time.Second,
		StopTimeout:    2 * time.Second,
		MaxStderrBytes: 8 << 10,
		Ready: func(line string) (bool, error) {
			return line == "READY", nil
		},
	}
}

// benchHelper reuses the test helper program, compiled once per binary.
func benchHelper(b *testing.B) string {
	b.Helper()

	helperOnce.Do(func() {
		buildHelper()
	})
	if helperErr != nil {
		b.Skipf("cannot build the helper: %v", helperErr)
	}
	return helperPath
}
