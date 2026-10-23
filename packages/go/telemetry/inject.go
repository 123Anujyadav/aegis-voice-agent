package telemetry

import (
	"context"
	"net/http"
)

// TraceparentHeader and TracestateHeader are the W3C Trace Context header
// names.
const (
	TraceparentHeader = "traceparent"
	TracestateHeader  = "tracestate"
)

// InjectTraceparent writes the current span context of ctx into h as a
// traceparent header, for a call to an INTERNAL peer, which may honour it
// (ADR-0026 rule 2). It reports whether a header was written. The tracestate
// header is never written (rule 1). Never inject toward a third party: the
// trace id is an internal identifier.
func InjectTraceparent(ctx context.Context, h http.Header) bool {
	sc, ok := SpanContextFromContext(ctx)
	if !ok || !sc.IsValid() {
		return false
	}
	h.Set(TraceparentHeader, FormatTraceparent(sc))
	return true
}
