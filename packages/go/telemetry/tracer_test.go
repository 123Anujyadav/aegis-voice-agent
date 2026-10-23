package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// collector is an Exporter that keeps every record.
type collector struct {
	mu   sync.Mutex
	recs []SpanRecord
}

func (c *collector) Export(r SpanRecord) { c.mu.Lock(); c.recs = append(c.recs, r); c.mu.Unlock() }

func (c *collector) all() []SpanRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]SpanRecord(nil), c.recs...)
}

func TestRecordingTracer_RootChildAndRemoteParentage(t *testing.T) {
	t.Parallel()
	col := &collector{}
	tr := NewRecordingTracer(col)

	rootCtx, root := tr.Start(context.Background(), "turn")
	_, child := tr.Start(rootCtx, "runtime.generate")
	child.End()
	root.End()

	remote, err := ParseTraceparent(specExample)
	if err != nil {
		t.Fatal(err)
	}
	_, inbound := tr.Start(ContextWithRemoteSpanContext(context.Background(), remote), "inbound")
	inbound.End()

	recs := col.all()
	if len(recs) != 3 {
		t.Fatalf("%d records, want 3", len(recs))
	}
	c, r, in := recs[0], recs[1], recs[2]
	if r.Parent.IsValid() || !r.Context.IsValid() || !r.Context.Sampled {
		t.Errorf("root = %+v, want a valid sampled root with no parent", r)
	}
	if c.Context.TraceID != r.Context.TraceID || c.Parent != r.Context.SpanID || c.Remote {
		t.Errorf("child = %+v, want the root's trace and the root as parent", c)
	}
	if c.Context.SpanID == r.Context.SpanID {
		t.Error("child reused the parent's span id")
	}
	if in.Context.TraceID != remote.TraceID || in.Parent != remote.SpanID || !in.Remote {
		t.Errorf("inbound = %+v, want a continuation of %s", in, specExample)
	}
}

func TestSpanContextFromContext_PrefersTheLocalSpan(t *testing.T) {
	t.Parallel()
	tr := NewRecordingTracer(nil)
	remote, _ := ParseTraceparent(specExample)
	ctx := ContextWithRemoteSpanContext(context.Background(), remote)
	if sc, ok := SpanContextFromContext(ctx); !ok || sc != remote {
		t.Errorf("remote only = %+v, %v", sc, ok)
	}
	ctx, span := tr.Start(ctx, "s")
	defer span.End()
	sc, ok := SpanContextFromContext(ctx)
	if !ok || sc.TraceID != remote.TraceID || sc.SpanID == remote.SpanID {
		t.Errorf("with a local span = %+v, %v; want the local span's context", sc, ok)
	}
	// What a client would inject outbound parses back to the same context.
	if back, err := ParseTraceparent(FormatTraceparent(sc)); err != nil || back != sc {
		t.Errorf("propagation round trip = %+v, %v", back, err)
	}
	if _, ok := SpanContextFromContext(context.Background()); ok {
		t.Error("an empty context reported a span context")
	}
	if got := ContextWithRemoteSpanContext(context.Background(), SpanContext{}); got != context.Background() {
		t.Error("an invalid remote context was stored")
	}
}

func TestRecordingTracer_IDsAreUniqueUnderConcurrency(t *testing.T) {
	t.Parallel()
	const workers, each = 32, 200
	col := &collector{}
	tr := NewRecordingTracer(col)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				_, s := tr.Start(context.Background(), "s")
				s.End()
			}
		}()
	}
	wg.Wait()
	recs := col.all()
	if len(recs) != workers*each {
		t.Fatalf("%d records, want %d", len(recs), workers*each)
	}
	traces, spans := map[TraceID]bool{}, map[SpanID]bool{}
	for _, r := range recs {
		if traces[r.Context.TraceID] || spans[r.Context.SpanID] {
			t.Fatalf("duplicate id in %+v", r.Context)
		}
		traces[r.Context.TraceID], spans[r.Context.SpanID] = true, true
	}
}

func TestRecordingSpan_AllowlistErrorsAndEndSemantics(t *testing.T) {
	t.Parallel()
	col := &collector{}
	start := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	clock := start
	tr := NewRecordingTracer(col, WithClock(func() time.Time { return clock }))

	_, s := tr.Start(context.Background(), "runtime.generate")
	s.SetAttribute("tier", "fast")
	s.SetAttribute("tier", "deep") // the last value wins, without a duplicate
	s.SetAttribute("caller_number", "+919800000000")
	s.SetAttribute("transcript", "my account number is ...")
	s.RecordError(nil)
	s.RecordError(fmt.Errorf("calling provider for %q: %w", "my account number is 1234", runtime.ErrProviderUnavailable))
	clock = start.Add(42 * time.Millisecond)
	s.End()
	s.End() // idempotent
	s.SetAttribute("model", "late")
	s.RecordError(errors.New("late"))

	recs := col.all()
	if len(recs) != 1 {
		t.Fatalf("%d exports, want exactly 1", len(recs))
	}
	r := recs[0]
	if len(r.Attributes) != 1 || r.Attributes[0] != (Attribute{Key: "tier", Value: "deep"}) {
		t.Errorf("attributes = %+v, want only tier=deep", r.Attributes)
	}
	if r.DroppedAttributes != 2 {
		t.Errorf("dropped = %d, want 2 (caller_number, transcript)", r.DroppedAttributes)
	}
	if r.Err != "runtime: provider unavailable" {
		t.Errorf("err = %q, want the sentinel's category and none of the wrapping text", r.Err)
	}
	if got := r.End.Sub(r.Start); got != 42*time.Millisecond {
		t.Errorf("duration = %s, want 42ms", got)
	}

	// An explicit allowlist replaces the default.
	_, s2 := NewRecordingTracer(col, WithAllowedAttributes("custom")).Start(context.Background(), "s")
	s2.SetAttribute("custom", 1)
	s2.SetAttribute("tier", "fast")
	s2.End()
	if r2 := col.all()[1]; len(r2.Attributes) != 1 || r2.Attributes[0].Key != "custom" || r2.DroppedAttributes != 1 {
		t.Errorf("custom allowlist = %+v", r2)
	}
}

func TestRecordingSpan_ConcurrentUseIsSafe(t *testing.T) {
	t.Parallel()
	col := &collector{}
	_, s := NewRecordingTracer(col).Start(context.Background(), "s")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.SetAttribute("tier", "fast")
				s.RecordError(errors.New("e"))
			}
			s.End()
		}()
	}
	wg.Wait()
	if n := len(col.all()); n != 1 {
		t.Fatalf("%d exports from concurrent End, want 1", n)
	}
}

// The log exporter through the REAL platform logger: span identity is kept
// (trace_id and span_id are on its allow list) while its redaction still
// applies inside span attributes.
func TestLogExporter_WritesThroughThePlatformRedactingLogger(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := platform.NewLogger(platform.ServiceConfig{
		Name: "voice", Version: "test", Environment: "development", Region: "in-south-1",
		LogLevel: "info", LogFormat: "json",
	}, &buf, []byte("test-hash-key-at-least-16-bytes"))
	tr := NewRecordingTracer(NewLogExporter(log))

	parentCtx, parent := tr.Start(context.Background(), "turn")
	_, s := tr.Start(parentCtx, "runtime.generate")
	s.SetAttribute("tier", "fast")
	s.SetAttribute("session_id", "ses_01J")
	s.SetAttribute("not_allowed", "x")
	s.RecordError(fmt.Errorf("admission for caller +919800000000: %w", runtime.ErrShed))
	s.End()
	parent.End()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d log lines, want 2:\n%s", len(lines), buf.String())
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatal(err)
	}
	sc, _ := SpanContextFromContext(parentCtx)
	if rec["msg"] != "span" || rec["span_name"] != "runtime.generate" {
		t.Errorf("record = %v", rec)
	}
	if rec["trace_id"] != sc.TraceID.String() || rec["parent_span_id"] != sc.SpanID.String() {
		t.Errorf("identity not kept: trace_id=%v parent_span_id=%v, want %s / %s", rec["trace_id"], rec["parent_span_id"], sc.TraceID, sc.SpanID)
	}
	if id, _ := rec["span_id"].(string); len(id) != 16 {
		t.Errorf("span_id = %v", rec["span_id"])
	}
	attrs, _ := rec["span_attributes"].(map[string]any)
	if attrs["tier"] != "fast" {
		t.Errorf("span_attributes = %v", attrs)
	}
	// platform redacts session_id everywhere except its exact-match allow
	// list; span attributes get the same policy, not a weaker one.
	if attrs["session_id"] != "[REDACTED]" {
		t.Errorf("session_id = %v, want the platform redaction to apply", attrs["session_id"])
	}
	if _, ok := attrs["not_allowed"]; ok || rec["dropped_attributes"] != float64(1) {
		t.Errorf("allowlist not applied: attrs=%v dropped=%v", attrs, rec["dropped_attributes"])
	}
	if rec["error"] != "runtime: shed at admission" {
		t.Errorf("error = %v", rec["error"])
	}
}
