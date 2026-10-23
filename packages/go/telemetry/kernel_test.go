package telemetry

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// The tracer runs inside the frozen runtime kernel unchanged: injected through
// the existing runtime.WithTracer option, the kernel's own runtime.generate
// span site records into it, as a child of the caller's span. This is the
// first implemented path for ADR-0011 hop 6; no frozen code is edited.
func TestRecordingTracer_RecordsTheKernelsGenerateSpan(t *testing.T) {
	t.Parallel()
	col := &collector{}
	tr := NewRecordingTracer(col)

	cfg := runtime.DefaultConfig("trace-test", "0.0.0-test")
	cfg.Scheduler.MaxConcurrent, cfg.Scheduler.MaxQueued, cfg.Session.Shards = 4, 4, 2
	k, err := runtime.New(cfg,
		runtime.WithClock(runtime.NewFakeClock(time.Time{})),
		runtime.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		runtime.WithTracer(tr),
	)
	if err != nil {
		t.Fatal(err)
	}
	provider := runtime.NewFakeProvider("fake")
	if err = k.Providers().Register(provider); err != nil {
		t.Fatal(err)
	}
	if err = k.Models().Register(runtime.ModelSpec{
		ID: "fake-fast", Provider: provider.ID(), Tier: runtime.TierFast,
		MaxContextTokens: 8192, MaxOutputTokens: 1024, DefaultMaxOutputTokens: 256,
		TypicalLatency: 100 * time.Millisecond, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err = k.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Stop(context.Background()) })
	session, err := k.Sessions().Create(0)
	if err != nil {
		t.Fatal(err)
	}
	if err = session.Activate(); err != nil {
		t.Fatal(err)
	}
	provider.Script(
		runtime.Chunk{Kind: runtime.ChunkText, Text: "hello"},
		runtime.Chunk{Kind: runtime.ChunkDone},
	)

	turnCtx, turn := tr.Start(context.Background(), "turn")
	d, err := k.Generate(turnCtx, runtime.GenerateSpec{
		SessionID: session.ID(),
		Tier:      runtime.TierFast,
		Messages:  []runtime.Message{{Role: runtime.RoleUser, Content: "hi"}},
		Class:     runtime.ClassInteractive,
	}, runtime.NewRecordingSink(false))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if res := d.Result(); res.Err != nil {
		t.Fatalf("stream: %v", res.Err)
	}
	turn.End()

	var gen *SpanRecord
	deadline := time.Now().Add(5 * time.Second)
	for gen == nil && time.Now().Before(deadline) {
		for _, r := range col.all() {
			if r.Name == "runtime.generate" {
				r := r
				gen = &r
			}
		}
		if gen == nil {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if gen == nil {
		t.Fatalf("no runtime.generate span exported; got %+v", col.all())
	}
	turnSC, _ := SpanContextFromContext(turnCtx)
	if gen.Context.TraceID != turnSC.TraceID || gen.Parent != turnSC.SpanID {
		t.Errorf("runtime.generate is not a child of the turn: %+v vs %+v", gen.Context, turnSC)
	}
	got := map[string]any{}
	for _, a := range gen.Attributes {
		got[a.Key] = a.Value
	}
	for key, want := range map[string]any{"tier": runtime.TierFast.String(), "class": runtime.ClassInteractive.String(), "model": "fake-fast", "provider": "fake"} {
		if got[key] != want {
			t.Errorf("attribute %s = %v, want %v (all: %v)", key, got[key], want, got)
		}
	}
	if gen.Err != "" {
		t.Errorf("unexpected error on a successful generation: %q", gen.Err)
	}
}
