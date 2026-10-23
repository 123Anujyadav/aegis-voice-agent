package telemetry

import (
	"context"
	"net/http"
	"testing"
)

func TestInjectTraceparent_WritesTheCurrentSpanForAnInternalPeer(t *testing.T) {
	t.Parallel()
	ctx, span := NewRecordingTracer(nil).Start(context.Background(), "edge")
	defer span.End()
	h := http.Header{}
	if !InjectTraceparent(ctx, h) {
		t.Fatal("nothing injected from a context with a span")
	}
	sc, _ := SpanContextFromContext(ctx)
	got, err := ParseTraceparent(h.Get(TraceparentHeader))
	if err != nil || got != sc {
		t.Errorf("injected %q = %+v, %v; want %+v", h.Get(TraceparentHeader), got, err, sc)
	}
	if h.Get(TracestateHeader) != "" {
		t.Error("tracestate written; rule 1 drops it")
	}
}

func TestInjectTraceparent_WithoutASpanWritesNothing(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	if InjectTraceparent(context.Background(), h) || len(h) != 0 {
		t.Errorf("injected %v from an empty context", h)
	}
}
