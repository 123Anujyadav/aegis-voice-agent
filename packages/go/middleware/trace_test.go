package middleware

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/callscreen/callscreen-platform/packages/go/telemetry"
)

// A caller-supplied trace the edge must never adopt.
const forgedTraceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

type spanSink struct {
	mu   sync.Mutex
	recs []telemetry.SpanRecord
}

func (s *spanSink) Export(r telemetry.SpanRecord) {
	s.mu.Lock()
	s.recs = append(s.recs, r)
	s.mu.Unlock()
}

func (s *spanSink) all() []telemetry.SpanRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]telemetry.SpanRecord(nil), s.recs...)
}

func (s *spanSink) named(t *testing.T, name string) telemetry.SpanRecord {
	t.Helper()
	all := s.all()
	for i := range all {
		if all[i].Name == name {
			return all[i]
		}
	}
	t.Fatalf("no %q span in %+v", name, all)
	return telemetry.SpanRecord{}
}

// gate stands in for edge authentication: it refuses a request without an
// Authorization header, as the real middleware does, so the chain under test
// has the production shape.
func gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// edgeChain is the edge-api application chain with the edge span in place:
// security headers and correlation outermost, then the edge span, then
// authentication, then the routes.
func edgeChain(tracer runtime.Tracer, routes http.Handler) http.Handler {
	return SecurityHeadersMiddleware(CorrelationIDMiddleware(EdgeTraceMiddleware(tracer)(gate(routes))))
}

// await is named for a span that may be exported asynchronously (the kernel
// ends runtime.generate when its dispatcher finishes), bounded by a deadline.
func (s *spanSink) await(t *testing.T, name string) telemetry.SpanRecord {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		all := s.all()
		for i := range all {
			if all[i].Name == name {
				return all[i]
			}
		}
	}
	return s.named(t, name)
}

func attrs(r telemetry.SpanRecord) map[string]any {
	m := map[string]any{}
	for _, a := range r.Attributes {
		m[a.Key] = a.Value
	}
	return m
}

func TestEdgeTrace_AuthenticatedRequestOpensANewRootSpan(t *testing.T) {
	t.Parallel()
	sink := &spanSink{}
	var sawTraceparent, sawTracestate string
	routes := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawTraceparent, sawTracestate = r.Header.Get("traceparent"), r.Header.Get("tracestate")
		w.WriteHeader(http.StatusNoContent)
	})
	h := edgeChain(telemetry.NewRecordingTracer(sink), routes)

	req := httptest.NewRequest(http.MethodPost, "/v1/screening?msisdn=%2B919800000000", strings.NewReader(`{"transcript":"my account number is 1234"}`))
	req.Header.Set("Authorization", "DPoP secret-access-token")
	req.Header.Set("traceparent", forgedTraceparent)
	req.Header.Set("tracestate", "vendor=forged")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	span := sink.named(t, EdgeSpanName)

	// Rule 2: never parented by, or stitched into, the caller's trace.
	if span.Parent.IsValid() || span.Remote {
		t.Errorf("edge span has a parent %s (remote %v); want a new root", span.Parent, span.Remote)
	}
	if span.Context.TraceID.String() == "0af7651916cd43dd8448eb211c80319c" {
		t.Error("edge span adopted the forged trace id")
	}
	if sawTraceparent != "" || sawTracestate != "" {
		t.Errorf("downstream saw traceparent %q / tracestate %q; both must be removed", sawTraceparent, sawTracestate)
	}

	// Correlation and security headers are intact, and the span carries the
	// same correlation id the response does.
	if got := attrs(span)["correlation_id"]; got == nil || got != rec.Header().Get("X-Correlation-ID") {
		t.Errorf("correlation_id = %v, response header %q", got, rec.Header().Get("X-Correlation-ID"))
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("security headers no longer applied")
	}

	want := map[string]any{"correlation_id": rec.Header().Get("X-Correlation-ID"), "http_method": "POST", "http_status_code": http.StatusNoContent}
	if got := attrs(span); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("attributes = %v, want exactly %v", got, want)
	}
	if span.Err != "" {
		t.Errorf("error recorded on a success: %q", span.Err)
	}
}

// Requests authentication refuses are still traced (the span sits outside
// auth), and the refusal is unchanged.
func TestEdgeTrace_RefusedRequestIsTracedAndStillRefused(t *testing.T) {
	t.Parallel()
	sink := &spanSink{}
	reached := false
	h := edgeChain(telemetry.NewRecordingTracer(sink), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me", http.NoBody))
	if rec.Code != http.StatusUnauthorized || reached {
		t.Fatalf("status = %d, route reached = %v; want 401 and not reached", rec.Code, reached)
	}
	span := sink.named(t, EdgeSpanName)
	if attrs(span)["http_status_code"] != http.StatusUnauthorized || span.Err != "" {
		t.Errorf("refusal span = %+v; want status 401 and no error (4xx is not a server error)", span)
	}
}

// Downstream work runs under the edge span: a child span, a real kernel
// generation, the platform log trace_id, and the traceparent for an internal
// peer all carry the edge trace.
func TestEdgeTrace_PropagatesToDownstreamWork(t *testing.T) {
	t.Parallel()
	sink := &spanSink{}
	tracer := telemetry.NewRecordingTracer(sink)
	kernel := startedKernel(t, tracer)

	var logTraceID string
	outbound := http.Header{}
	routes := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logTraceID, _ = platform.TraceIDFrom(ctx)
		telemetry.InjectTraceparent(ctx, outbound)
		_, child := tracer.Start(ctx, "edge.route")
		child.End()
		generate(ctx, t, kernel)
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/me", http.NoBody)
	req.Header.Set("Authorization", "DPoP x")
	edgeChain(tracer, routes).ServeHTTP(httptest.NewRecorder(), req)

	edge := sink.named(t, EdgeSpanName)
	trace := edge.Context.TraceID
	if child := sink.named(t, "edge.route"); child.Context.TraceID != trace || child.Parent != edge.Context.SpanID {
		t.Errorf("route span %+v is not a child of the edge span %+v", child.Context, edge.Context)
	}
	if gen := sink.await(t, "runtime.generate"); gen.Context.TraceID != trace || gen.Parent != edge.Context.SpanID {
		t.Errorf("kernel span %+v is not a child of the edge span %+v", gen.Context, edge.Context)
	}
	if logTraceID != trace.String() {
		t.Errorf("platform trace_id = %q, want %s", logTraceID, trace)
	}
	if sc, err := telemetry.ParseTraceparent(outbound.Get("traceparent")); err != nil || sc.TraceID != trace || sc.SpanID != edge.Context.SpanID {
		t.Errorf("outbound traceparent %q = %+v, %v", outbound.Get("traceparent"), sc, err)
	}
}

// A 5xx records a fixed category; nothing from the request or from the
// handler's own error text reaches the span.
func TestEdgeTrace_ServerErrorRecordsOnlyAFixedCategory(t *testing.T) {
	t.Parallel()
	sink := &spanSink{}
	routes := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "provider echoed: my account number is 1234", http.StatusBadGateway)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/x?token=abc", strings.NewReader("transcript text"))
	req.Header.Set("Authorization", "DPoP secret-access-token")
	edgeChain(telemetry.NewRecordingTracer(sink), routes).ServeHTTP(httptest.NewRecorder(), req)

	span := sink.named(t, EdgeSpanName)
	if span.Err != "http status 502" {
		t.Errorf("err = %q, want the fixed category", span.Err)
	}
	dump := fmt.Sprintf("%+v", span)
	for _, secret := range []string{"1234", "secret-access-token", "token=abc", "transcript", "/v1/x", "DPoP"} {
		if strings.Contains(dump, secret) {
			t.Errorf("span contains %q: %s", secret, dump)
		}
	}
}

func TestEdgeTrace_UnknownMethodsAreNormalised(t *testing.T) {
	t.Parallel()
	sink := &spanSink{}
	req := httptest.NewRequest("BREW", "/", http.NoBody)
	req.Header.Set("Authorization", "DPoP x")
	edgeChain(telemetry.NewRecordingTracer(sink), http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), req)
	if got := attrs(sink.named(t, EdgeSpanName))["http_method"]; got != "OTHER" {
		t.Errorf("http_method = %v, want OTHER", got)
	}
}

// Unconfigured tracing changes nothing: a nil tracer returns the handler
// itself, and the runtime's NoopTracer serves normally with no trace id.
func TestEdgeTrace_NilAndNoopTracersAreSafe(t *testing.T) {
	t.Parallel()
	next := http.NotFoundHandler()
	if got := EdgeTraceMiddleware(nil)(next); fmt.Sprintf("%p", got) != fmt.Sprintf("%p", next) {
		t.Error("a nil tracer wrapped the handler")
	}
	var hadTraceID bool
	routes := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadTraceID = platform.TraceIDFrom(r.Context())
		w.WriteHeader(http.StatusTeapot)
	})
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Header.Set("Authorization", "DPoP x")
	rec := httptest.NewRecorder()
	edgeChain(runtime.NoopTracer{}, routes).ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot || hadTraceID {
		t.Errorf("noop tracer: status %d, trace id present %v", rec.Code, hadTraceID)
	}
}

// A panicking handler still ends the span, and the panic is not swallowed.
func TestEdgeTrace_PanicEndsTheSpanAndPropagates(t *testing.T) {
	t.Parallel()
	sink := &spanSink{}
	h := EdgeTraceMiddleware(telemetry.NewRecordingTracer(sink))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic swallowed")
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	}()
	if n := len(sink.all()); n != 1 {
		t.Fatalf("%d spans exported after a panic, want 1", n)
	}
}

func TestEdgeTrace_ConcurrentRequestsGetDistinctTraces(t *testing.T) {
	t.Parallel()
	sink := &spanSink{}
	h := edgeChain(telemetry.NewRecordingTracer(sink), http.NotFoundHandler())
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			req.Header.Set("Authorization", "DPoP x")
			req.Header.Set("traceparent", forgedTraceparent)
			h.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	wg.Wait()
	seen := map[telemetry.TraceID]bool{}
	for _, r := range sink.all() {
		if seen[r.Context.TraceID] || r.Parent.IsValid() {
			t.Fatalf("shared or parented trace: %+v", r.Context)
		}
		seen[r.Context.TraceID] = true
	}
	if len(seen) != 64 {
		t.Errorf("%d traces, want 64", len(seen))
	}
}

// startedKernel builds a real runtime kernel that records into tracer, with
// one fake model, as the telemetry kernel test does.
func startedKernel(t *testing.T, tracer runtime.Tracer) *runtime.Kernel {
	t.Helper()
	cfg := runtime.DefaultConfig("edge-trace-test", "0.0.0-test")
	cfg.Scheduler.MaxConcurrent, cfg.Scheduler.MaxQueued, cfg.Session.Shards = 4, 4, 2
	k, err := runtime.New(cfg,
		runtime.WithClock(runtime.NewFakeClock(time.Time{})),
		runtime.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		runtime.WithTracer(tracer),
	)
	if err != nil {
		t.Fatal(err)
	}
	p := runtime.NewFakeProvider("fake")
	if err = k.Providers().Register(p); err != nil {
		t.Fatal(err)
	}
	if err = k.Models().Register(runtime.ModelSpec{
		ID: "fake-fast", Provider: p.ID(), Tier: runtime.TierFast,
		MaxContextTokens: 8192, MaxOutputTokens: 1024, DefaultMaxOutputTokens: 256,
		TypicalLatency: 100 * time.Millisecond, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err = k.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Stop(context.Background()) })
	p.Script(runtime.Chunk{Kind: runtime.ChunkText, Text: "ok"}, runtime.Chunk{Kind: runtime.ChunkDone})
	return k
}

// generate runs one generation under ctx and waits for it to finish.
func generate(ctx context.Context, t *testing.T, k *runtime.Kernel) {
	t.Helper()
	s, err := k.Sessions().Create(0)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Activate(); err != nil {
		t.Fatal(err)
	}
	d, err := k.Generate(ctx, runtime.GenerateSpec{
		SessionID: s.ID(), Tier: runtime.TierFast, Class: runtime.ClassInteractive,
		Messages: []runtime.Message{{Role: runtime.RoleUser, Content: "hi"}},
	}, runtime.NewRecordingSink(false))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if res := d.Result(); res.Err != nil {
		t.Fatalf("stream: %v", res.Err)
	}
}
