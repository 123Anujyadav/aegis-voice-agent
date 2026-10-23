package httpapi

import (
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/middleware"
	"github.com/callscreen/callscreen-platform/packages/go/telemetry"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

// ADR-0026 step 2 on identity's public listener: one edge span per request,
// a new root (an inbound traceparent is never adopted), carrying the
// correlation ID and the status, and nothing from the request body or path.

type spanCollector struct {
	mu   sync.Mutex
	recs []telemetry.SpanRecord
}

func (c *spanCollector) Export(r telemetry.SpanRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recs = append(c.recs, r)
}

func (c *spanCollector) snapshot() []telemetry.SpanRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]telemetry.SpanRecord(nil), c.recs...)
}

func attrs(r telemetry.SpanRecord) map[string]any {
	m := map[string]any{}
	for _, a := range r.Attributes {
		m[a.Key] = a.Value
	}
	return m
}

// tracedRig is newRig with the API built on a recording tracer.
func tracedRig(t *testing.T) (*rig, *spanCollector) {
	t.Helper()
	r := newRig(t, true)
	c := &spanCollector{}
	r.tracer = telemetry.NewRecordingTracer(c)
	r.h = New(r.svc, slog.New(slog.NewTextHandler(r.logs, nil)), r.m, r.tracer)
	return r, c
}

func TestTrace_EveryRequestIsOneEdgeRootSpan(t *testing.T) {
	r, c := tracedRig(t)
	ok := r.post(t, routes.OTPRequest, `{"msisdn":"`+msisdn+`"}`, "")
	refused := r.post(t, routes.Enrollment, `{"requestId":"nope","code":"123456"}`, "")
	missing := r.do(t, "POST", "/v1/otp/request/"+msisdn, `{}`, nil)
	spans := c.snapshot()
	if len(spans) != 3 {
		t.Fatalf("%d spans for 3 requests: %+v", len(spans), spans)
	}
	for i, want := range []resp{ok, refused, missing} {
		s := spans[i]
		a := attrs(s)
		if s.Name != middleware.EdgeSpanName || s.Parent.IsValid() || s.Remote {
			t.Errorf("span %d: %+v; want a new edge root", i, s)
		}
		if a["correlation_id"] != want.hdr.Get("X-Correlation-ID") || a["http_status_code"] != want.code {
			t.Errorf("span %d attributes %v; want correlation %q and status %d", i, a, want.hdr.Get("X-Correlation-ID"), want.code)
		}
		for k, v := range a {
			if strings.Contains(strings.ToLower(k), "path") || strings.Contains(stringOf(v), "9876543210") {
				t.Errorf("span %d carries request data: %s=%v", i, k, v)
			}
		}
	}
}

// An inbound traceparent is discarded: the edge span starts a new trace.
func TestTrace_InboundTraceparentIsNeverAdopted(t *testing.T) {
	r, c := tracedRig(t)
	inbound := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	r.do(t, "POST", routes.OTPRequest, `{"msisdn":"`+msisdn+`"}`, map[string][]string{"traceparent": {inbound}})
	spans := c.snapshot()
	if len(spans) != 1 || spans[0].Remote || spans[0].Parent.IsValid() ||
		spans[0].Context.TraceID.String() == "0af7651916cd43dd8448eb211c80319c" {
		t.Fatalf("the inbound trace was adopted: %+v", spans)
	}
}

// Without a tracer the API is unchanged: no spans, same responses.
func TestTrace_NilTracerLeavesTheChainUnchanged(t *testing.T) {
	r := newRig(t, true)
	if got := r.post(t, routes.OTPRequest, `{"msisdn":"`+msisdn+`"}`, ""); got.code != 202 || got.hdr.Get("X-Correlation-ID") == "" {
		t.Fatalf("without a tracer: %d %v", got.code, got.hdr)
	}
}

func stringOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
