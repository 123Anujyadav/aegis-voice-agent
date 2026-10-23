package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/middleware"
	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/callscreen/callscreen-platform/packages/go/telemetry"
	"github.com/callscreen/callscreen-platform/services/go/edge-api/internal/httpapi"
)

// ADR-0026: the edge-api application listener opens an edge span for every
// request. These tests drive the REAL lifecycle (configureAuth, then
// platform.Service.Run, then real HTTP on the bound port) with a deterministic
// tracer injected through edgeTracer.

const forgedTraceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

type spanCollector struct {
	mu   sync.Mutex
	recs []telemetry.SpanRecord
}

func (c *spanCollector) Export(r telemetry.SpanRecord) {
	c.mu.Lock()
	c.recs = append(c.recs, r)
	c.mu.Unlock()
}

func (c *spanCollector) snapshot() []telemetry.SpanRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]telemetry.SpanRecord(nil), c.recs...)
}

// useTracer makes configureAuth build its listener with tracer.
func useTracer(t *testing.T, tracer runtime.Tracer) {
	t.Helper()
	orig := edgeTracer
	edgeTracer = func(*slog.Logger) runtime.Tracer { return tracer }
	t.Cleanup(func() { edgeTracer = orig })
}

func spanAttrs(r *telemetry.SpanRecord) map[string]any {
	m := map[string]any{}
	for _, a := range r.Attributes {
		m[a.Key] = a.Value
	}
	return m
}

// edgeSpanFor returns the edge span whose correlation_id matches the
// response's, waiting briefly: the span ends after the response is written.
func edgeSpanFor(t *testing.T, c *spanCollector, correlationID string) telemetry.SpanRecord {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		recs := c.snapshot()
		for i := range recs {
			if recs[i].Name == middleware.EdgeSpanName && spanAttrs(&recs[i])["correlation_id"] == correlationID {
				return recs[i]
			}
		}
	}
	t.Fatalf("no edge span with correlation_id %q in %+v", correlationID, c.snapshot())
	return telemetry.SpanRecord{}
}

func TestEdgeTrace_RealListenerEmitsEdgeSpans(t *testing.T) {
	priv := setMeEnv(t)
	fakeOpen(t) // the real authenticator on an in-memory replay store
	spans := &spanCollector{}
	tracer := telemetry.NewRecordingTracer(spans)
	useTracer(t, tracer)

	// The production route table, with /v1/me wrapped to observe what the
	// route's own context carries.
	var routeTraceID string
	var routeSpan telemetry.SpanContext
	routes := protectedRoutes(quiet)
	prod := routes[httpapi.MePath]
	routes[httpapi.MePath] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routeTraceID, _ = platform.TraceIDFrom(r.Context())
		_, child := tracer.Start(r.Context(), "edge-api.me")
		child.End()
		routeSpan, _ = telemetry.SpanContextFromContext(r.Context())
		prod.ServeHTTP(w, r)
	})

	cfg := platform.ServiceConfig{Name: serviceName, Version: "test", HTTPPort: e2eFreePort(t), HealthPort: e2eFreePort(t), ShutdownTimeout: time.Second, ReadHeaderTimeout: 5 * time.Second}
	health := platform.NewHealth(cfg)
	svc := platform.NewService(cfg, quiet, health)
	if err := configureAuth(ctx, cfg, svc, health, routes, quiet); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- svc.Run(runCtx) }()
	waitFor(t, "listener", func() bool { return dials(cfg.HTTPPort) })
	base := fmt.Sprintf("http://127.0.0.1:%d", cfg.HTTPPort)

	// 1. Refused by authentication: still refused, still traced, never
	// parented by the forged inbound trace.
	refused := edgeGet(t, base, httpapi.MePath, map[string]string{"traceparent": forgedTraceparent, "tracestate": "vendor=forged"})
	if refused.code != http.StatusUnauthorized || refused.body["code"] != "ERR_UNAUTHENTICATED" {
		t.Fatalf("unauthenticated /v1/me = %d %s", refused.code, refused.raw)
	}
	if refused.hdr.Get("X-Content-Type-Options") != "nosniff" || refused.hdr.Get("X-Correlation-ID") == "" {
		t.Errorf("refusal lost security or correlation headers: %v", refused.hdr)
	}
	rs := edgeSpanFor(t, spans, refused.hdr.Get("X-Correlation-ID"))
	if rs.Parent.IsValid() || rs.Remote || rs.Context.TraceID.String() == "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("refused request span adopted the forged trace: %+v", rs.Context)
	}
	if got := spanAttrs(&rs); got["http_status_code"] != http.StatusUnauthorized || got["http_method"] != "GET" || rs.Err != "" {
		t.Errorf("refused span = attrs %v err %q", got, rs.Err)
	}

	// 2. Authenticated: served, traced, and the route runs under the edge
	// span (child span, platform trace_id).
	device, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tok, _ := newSigner(t, priv, meIssuer, meAudience, meKID, time.Now).Issue(meSubject, nil, mustJKT(t, device))
	dpopProof := proof(t, device, "GET", meOrigin+httpapi.MePath, tok)
	served := edgeGet(t, base, httpapi.MePath, map[string]string{
		"Authorization": "DPoP " + tok, "DPoP": dpopProof, "traceparent": forgedTraceparent,
	})
	if served.code != http.StatusOK || served.body["subject"] != meSubject {
		t.Fatalf("authenticated /v1/me = %d %s", served.code, served.raw)
	}
	es := edgeSpanFor(t, spans, served.hdr.Get("X-Correlation-ID"))
	if es.Parent.IsValid() || es.Remote || es.Context.TraceID == rs.Context.TraceID {
		t.Errorf("served span is not a fresh root: %+v (refused trace %s)", es.Context, rs.Context.TraceID)
	}
	want := map[string]any{"correlation_id": served.hdr.Get("X-Correlation-ID"), "http_method": "GET", "http_status_code": http.StatusOK}
	if got := spanAttrs(&es); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("served span attributes = %v, want exactly %v", got, want)
	}
	if routeTraceID != es.Context.TraceID.String() || routeSpan.SpanID != es.Context.SpanID {
		t.Errorf("route context: trace_id %q span %s; want the edge span %s / %s", routeTraceID, routeSpan.SpanID, es.Context.TraceID, es.Context.SpanID)
	}
	var child *telemetry.SpanRecord
	recs := spans.snapshot()
	for i := range recs {
		if recs[i].Name == "edge-api.me" {
			child = &recs[i]
		}
	}
	if child == nil || child.Context.TraceID != es.Context.TraceID || child.Parent != es.Context.SpanID {
		t.Errorf("route span is not a child of the edge span: %+v", child)
	}

	// 3. Nothing sensitive reaches any span: not the token, the proof, the
	// principal, the forged trace, or the route's response.
	dump := fmt.Sprintf("%+v", spans.snapshot())
	for _, secret := range []string{tok, dpopProof, meSubject, "0af7651916cd43dd8448eb211c80319c", "vendor=forged", httpapi.MePath, "DPoP"} {
		if strings.Contains(dump, secret) {
			t.Errorf("a span contains %q", secret)
		}
	}

	// 4. Shutdown is unchanged: the service stops cleanly and every span
	// started was ended (exported).
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("service did not stop")
	}
	if n := len(spans.snapshot()); n != 3 {
		t.Errorf("%d spans exported, want 3 (refused, served, route child)", n)
	}
}

// A nil tracer leaves the listener exactly as it was, and the production
// default (edgeTracer) is safe to build: it writes spans through the logger
// it is given and needs no exporter, network or credential.
func TestEdgeTrace_NilTracerAndTheProductionDefaultAreSafe(t *testing.T) {
	priv := setMeEnv(t)
	fakeOpen(t)
	device, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tok, _ := newSigner(t, priv, meIssuer, meAudience, meKID, time.Now).Issue(meSubject, nil, mustJKT(t, device))

	var logs bytes.Buffer
	log := platform.NewLogger(platform.ServiceConfig{Name: serviceName, Version: "test", Environment: "development", Region: "in-south-1", LogLevel: "info", LogFormat: "json"}, &logs, []byte("test-hash-key-at-least-16-bytes"))
	for name, tracer := range map[string]runtime.Tracer{"nil": nil, "production default": edgeTracer(log)} {
		t.Run(name, func(t *testing.T) {
			useTracer(t, tracer)
			cfg := platform.ServiceConfig{Name: serviceName, Version: "test", HTTPPort: e2eFreePort(t), HealthPort: e2eFreePort(t), ShutdownTimeout: time.Second, ReadHeaderTimeout: 5 * time.Second}
			health := platform.NewHealth(cfg)
			svc := platform.NewService(cfg, quiet, health)
			if err := configureAuth(ctx, cfg, svc, health, protectedRoutes(quiet), quiet); err != nil {
				t.Fatal(err)
			}
			runCtx, cancel := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { done <- svc.Run(runCtx) }()
			waitFor(t, "listener", func() bool { return dials(cfg.HTTPPort) })
			base := fmt.Sprintf("http://127.0.0.1:%d", cfg.HTTPPort)
			got := edgeGet(t, base, httpapi.MePath, map[string]string{"Authorization": "DPoP " + tok, "DPoP": proof(t, device, "GET", meOrigin+httpapi.MePath, tok)})
			if got.code != http.StatusOK || got.body["subject"] != meSubject {
				t.Errorf("/v1/me = %d %s", got.code, got.raw)
			}
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		})
	}
	// The default tracer wrote its span as one structured log record, with
	// no credential in it.
	out := logs.String()
	if !strings.Contains(out, `"msg":"span"`) || !strings.Contains(out, `"span_name":"edge.request"`) {
		t.Errorf("no edge span log record from the production default:\n%s", out)
	}
	if strings.Contains(out, tok) {
		t.Error("the access token reached the span log record")
	}
}
