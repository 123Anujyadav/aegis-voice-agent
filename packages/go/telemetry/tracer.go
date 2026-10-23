package telemetry

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// ADR-0026 (Proposed) step 1: a standard-library implementation of the
// existing frozen runtime.Tracer / runtime.Span port, with W3C Trace Context
// identity and an exporter interface whose first implementation writes
// structured log records. It adds no backend, no collector and no credential.
// The stub declarations in telemetry.go are left as they are: replacing them
// is part of accepting the ADR.

// Compile-time proof that the implementation satisfies the frozen port, so a
// kernel can take it through runtime.WithTracer unchanged.
var (
	_ runtime.Tracer = (*RecordingTracer)(nil)
	_ runtime.Span   = (*recordingSpan)(nil)
)

// Attribute is one key/value recorded on a span.
type Attribute struct {
	Key   string
	Value any
}

// SpanRecord is a finished span, as handed to an Exporter.
type SpanRecord struct {
	Name    string
	Context SpanContext
	// Parent is the parent span's id; invalid for a root span.
	Parent SpanID
	// Remote reports that Parent came from another process (a traceparent).
	Remote     bool
	Start, End time.Time
	Attributes []Attribute
	// DroppedAttributes counts attributes refused by the allowlist.
	DroppedAttributes int
	// Err describes the last error recorded, empty when none was. It is the
	// allowlisted description from spanErrorText, never raw err.Error().
	Err string
}

// Exporter receives every finished span exactly once. Implementations must be
// safe for concurrent use and must not block for long: Export runs on the
// goroutine that ends the span.
type Exporter interface {
	Export(SpanRecord)
}

// ExporterFunc adapts a function to Exporter.
type ExporterFunc func(SpanRecord)

// Export calls f.
func (f ExporterFunc) Export(r SpanRecord) { f(r) }

// DefaultAllowedAttributes are the keys the runtime kernel's own span sites
// set (kernel.go, runtime.generate): identifiers and enumerations only, as the
// runtime.Span contract requires. Anything else is dropped unless a caller
// allows it explicitly.
var DefaultAllowedAttributes = []string{
	"request_id", "session_id", "stream_id", "correlation_id",
	"tier", "class", "model", "provider", "shed_reason",
	// Edge request spans (middleware.EdgeTraceMiddleware): the request
	// method, normalised to a closed set, and the numeric response status.
	"http_method", "http_status_code",
}

// RecordingTracer is a runtime.Tracer that records spans and hands each
// finished one to its Exporter.
//
// Parentage: a span started from a context that carries a span from this
// tracer is that span's child; otherwise one that carries a remote
// SpanContext (ContextWithRemoteSpanContext) continues the remote trace;
// otherwise it starts a new trace. Every span is sampled: ADR-0011 traces
// every turn, and export-side sampling is a backend decision (ADR-0026).
type RecordingTracer struct {
	exporter Exporter
	allowed  map[string]struct{}
	now      func() time.Time
}

// TracerOption customises a RecordingTracer.
type TracerOption func(*RecordingTracer)

// WithAllowedAttributes replaces the attribute allowlist. Keys are exact.
func WithAllowedAttributes(keys ...string) TracerOption {
	return func(t *RecordingTracer) {
		t.allowed = make(map[string]struct{}, len(keys))
		for _, k := range keys {
			t.allowed[k] = struct{}{}
		}
	}
}

// WithClock replaces the clock used for span start and end times.
func WithClock(now func() time.Time) TracerOption {
	return func(t *RecordingTracer) { t.now = now }
}

// NewRecordingTracer builds a tracer exporting to exporter. A nil exporter
// discards spans (they are still created and propagated).
func NewRecordingTracer(exporter Exporter, opts ...TracerOption) *RecordingTracer {
	if exporter == nil {
		exporter = ExporterFunc(func(SpanRecord) {})
	}
	t := &RecordingTracer{exporter: exporter, now: time.Now}
	WithAllowedAttributes(DefaultAllowedAttributes...)(t)
	for _, opt := range opts {
		opt(t)
	}
	return t
}

type spanKey struct{}

type remoteKey struct{}

// ContextWithRemoteSpanContext returns ctx carrying sc as the remote parent of
// the next span started from it, typically parsed from an inbound traceparent.
// An invalid sc is ignored.
func ContextWithRemoteSpanContext(ctx context.Context, sc SpanContext) context.Context {
	if !sc.IsValid() {
		return ctx
	}
	return context.WithValue(ctx, remoteKey{}, sc)
}

// SpanContextFromContext returns the span context to propagate from ctx: the
// current span's if one was started here, otherwise the remote one.
func SpanContextFromContext(ctx context.Context) (SpanContext, bool) {
	if s, ok := ctx.Value(spanKey{}).(*recordingSpan); ok {
		return s.rec.Context, true
	}
	if sc, ok := ctx.Value(remoteKey{}).(SpanContext); ok {
		return sc, true
	}
	return SpanContext{}, false
}

// Start implements runtime.Tracer.
func (t *RecordingTracer) Start(ctx context.Context, name string) (context.Context, runtime.Span) {
	rec := SpanRecord{Name: name, Start: t.now()}
	rec.Context.Sampled = true
	switch {
	case spanFrom(ctx) != nil:
		parent := spanFrom(ctx).rec.Context
		rec.Context.TraceID, rec.Parent, rec.Context.Sampled = parent.TraceID, parent.SpanID, parent.Sampled
	case remoteFrom(ctx).IsValid():
		remote := remoteFrom(ctx)
		rec.Context.TraceID, rec.Parent, rec.Context.Sampled, rec.Remote = remote.TraceID, remote.SpanID, remote.Sampled, true
	default:
		rec.Context.TraceID = newTraceID()
	}
	rec.Context.SpanID = newSpanID()
	s := &recordingSpan{tracer: t, rec: rec}
	return context.WithValue(ctx, spanKey{}, s), s
}

func spanFrom(ctx context.Context) *recordingSpan {
	if s, ok := ctx.Value(spanKey{}).(*recordingSpan); ok {
		return s
	}
	return nil
}

func remoteFrom(ctx context.Context) SpanContext {
	if sc, ok := ctx.Value(remoteKey{}).(SpanContext); ok {
		return sc
	}
	return SpanContext{}
}

// recordingSpan is a runtime.Span. Safe for concurrent use; every method after
// End is a no-op, and End exports exactly once.
type recordingSpan struct {
	tracer *RecordingTracer
	mu     sync.Mutex
	rec    SpanRecord
	ended  bool
}

// SetAttribute implements runtime.Span. Keys outside the allowlist are dropped
// and counted, never recorded: runtime.Span forbids PERSONAL and SENSITIVE
// values, and an allowlist is the only enforcement that does not depend on
// every call site remembering that.
func (s *recordingSpan) SetAttribute(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	if _, ok := s.tracer.allowed[key]; !ok {
		s.rec.DroppedAttributes++
		return
	}
	for i := range s.rec.Attributes {
		if s.rec.Attributes[i].Key == key {
			s.rec.Attributes[i].Value = value
			return
		}
	}
	s.rec.Attributes = append(s.rec.Attributes, Attribute{Key: key, Value: value})
}

// RecordError implements runtime.Span. A nil error is ignored. The error's
// text is not recorded as-is; see spanErrorText.
func (s *recordingSpan) RecordError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ended {
		s.rec.Err = spanErrorText(err)
	}
}

// End implements runtime.Span.
func (s *recordingSpan) End() {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	s.rec.End = s.tracer.now()
	rec := s.rec
	rec.Attributes = append([]Attribute(nil), s.rec.Attributes...)
	s.mu.Unlock()
	s.tracer.exporter.Export(rec)
}

// newTraceID returns a random, valid trace-id.
func newTraceID() TraceID {
	for {
		var id TraceID
		readRandom(id[:])
		if id.IsValid() {
			return id
		}
	}
}

// newSpanID returns a random, valid span id.
func newSpanID() SpanID {
	for {
		var id SpanID
		readRandom(id[:])
		if id.IsValid() {
			return id
		}
	}
}

// readRandom fills b from crypto/rand. As in runtime.newID, an error means the
// OS entropy source has failed, and every id minted afterwards would be
// predictable, so the process must not continue.
func readRandom(b []byte) {
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("telemetry: entropy source unavailable: %v", err))
	}
}

// LogExporter writes each finished span as one structured log record.
//
// Pass the platform logger (platform.NewLogger): its redacting handler then
// applies to every span exactly as to every log line (ADR-0012, ADR-0026 D4).
// The handler's allow list keeps the trace_id and span_id keys intact.
type LogExporter struct {
	log *slog.Logger
}

// NewLogExporter builds a LogExporter over log.
func NewLogExporter(log *slog.Logger) *LogExporter { return &LogExporter{log: log} }

// Export implements Exporter.
func (e *LogExporter) Export(r SpanRecord) {
	attrs := make([]any, 0, len(r.Attributes))
	for _, a := range r.Attributes {
		attrs = append(attrs, slog.Any(a.Key, a.Value))
	}
	fields := []any{
		slog.String("span_name", r.Name),
		slog.String("trace_id", r.Context.TraceID.String()),
		slog.String("span_id", r.Context.SpanID.String()),
		slog.Int64("duration_ms", r.End.Sub(r.Start).Milliseconds()),
		slog.Group("span_attributes", attrs...),
	}
	if r.Parent.IsValid() {
		fields = append(fields, slog.String("parent_span_id", r.Parent.String()), slog.Bool("parent_remote", r.Remote))
	}
	if r.DroppedAttributes > 0 {
		fields = append(fields, slog.Int("dropped_attributes", r.DroppedAttributes))
	}
	if r.Err != "" {
		fields = append(fields, slog.String("error", r.Err))
	}
	e.log.Info("span", fields...)
}
