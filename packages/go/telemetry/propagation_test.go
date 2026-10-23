package telemetry

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// FuzzParseTraceparent: whatever arrives in a traceparent header, the parser
// never panics, accepts only valid ids, and anything it accepts survives
// FormatTraceparent and a second parse unchanged.
func FuzzParseTraceparent(f *testing.F) {
	for _, seed := range []string{
		specExample,
		"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00",
		"cc-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01-future",
		"ff-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"00-00000000000000000000000000000000-b7ad6b7169203331-01",
		"", "00", strings.Repeat("-", 55),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		sc, err := ParseTraceparent(s)
		if err != nil {
			if !errors.Is(err, ErrInvalidTraceparent) {
				t.Fatalf("%q: error %v does not wrap ErrInvalidTraceparent", s, err)
			}
			return
		}
		if !sc.IsValid() {
			t.Fatalf("%q accepted with an invalid context %+v", s, sc)
		}
		out := FormatTraceparent(sc)
		back, err := ParseTraceparent(out)
		if err != nil || back != sc {
			t.Fatalf("%q -> %q -> %+v, %v; want %+v", s, out, back, err, sc)
		}
	})
}

// An inbound "not sampled" flag is propagated to children, and outbound, but
// the span is still recorded and exported: ADR-0011 traces every turn, so
// in-process head sampling is always on, and the flag is only carried for the
// next hop and a future backend to honour (ADR-0026).
func TestRecordingTracer_UnsampledInboundIsPropagatedButStillRecorded(t *testing.T) {
	t.Parallel()
	remote, err := ParseTraceparent("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00")
	if err != nil || remote.Sampled {
		t.Fatalf("fixture = %+v, %v", remote, err)
	}
	col := &collector{}
	tr := NewRecordingTracer(col)
	ctx, parent := tr.Start(ContextWithRemoteSpanContext(context.Background(), remote), "inbound")
	childCtx, child := tr.Start(ctx, "child")
	child.End()
	parent.End()

	if n := len(col.all()); n != 2 {
		t.Fatalf("%d spans exported, want 2: unsampled spans are still recorded", n)
	}
	sc, _ := SpanContextFromContext(childCtx)
	if sc.Sampled {
		t.Error("child turned the remote's not-sampled flag on")
	}
	if got := FormatTraceparent(sc); !strings.HasSuffix(got, "-00") || !strings.HasPrefix(got, "00-"+remote.TraceID.String()+"-") {
		t.Errorf("outbound traceparent = %q, want the remote trace with flags 00", got)
	}
}

// A context with no parent starts a sampled root: the platform's own traces
// are always sampled.
func TestRecordingTracer_LocalRootIsSampled(t *testing.T) {
	t.Parallel()
	ctx, s := NewRecordingTracer(nil).Start(context.Background(), "root")
	defer s.End()
	if sc, ok := SpanContextFromContext(ctx); !ok || !sc.Sampled || !sc.IsValid() {
		t.Errorf("root = %+v, %v", sc, ok)
	}
}
