package middleware

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/callscreen/callscreen-platform/packages/go/telemetry"
)

// EdgeSpanName is the span every public-edge request opens.
const EdgeSpanName = "edge.request"

// EdgeTraceMiddleware opens one span per request at a PUBLIC edge
// (ADR-0026, accepted; step 2).
//
// Position: inside CorrelationIDMiddleware and outside authentication, for
// example SecurityHeaders(CorrelationID(EdgeTrace(tracer)(auth(mux)))).
// The span then carries the request's correlation ID, and it covers requests
// that authentication refuses.
//
// What it does:
//   - Distrusts the caller (rule 2): every request starts a NEW ROOT. The
//     inbound traceparent and tracestate headers are removed before anything
//     downstream sees them, so no external caller can parent, stitch into or
//     inflate a platform trace.
//   - Propagates: the span travels in the request context, so spans started
//     downstream are its children, and so is a kernel built with
//     runtime.WithTracer. The trace id is also put on the context for the
//     platform logger's trace_id attribute (platform.WithTraceID).
//     telemetry.InjectTraceparent carries the trace to internal peers.
//   - Records only low-cardinality values: correlation_id, http_method (from
//     a closed set) and http_status_code. It never records the path, the
//     query, headers, the body or any credential. A 5xx response records a
//     fixed category through telemetry.SafeError, never a message.
//
// A nil tracer returns next unchanged, so an unconfigured service behaves
// exactly as before.
func EdgeTraceMiddleware(tracer runtime.Tracer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if tracer == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A clone, so the caller's request (and its headers) are not
			// mutated; the clone's headers are a deep copy.
			r = r.Clone(r.Context())
			r.Header.Del(telemetry.TraceparentHeader)
			r.Header.Del(telemetry.TracestateHeader)

			ctx, span := tracer.Start(r.Context(), EdgeSpanName)
			if sc, ok := telemetry.SpanContextFromContext(ctx); ok { //nolint:contextcheck // ctx derives from r.Context() through runtime.Tracer.Start, which the linter cannot see through
				ctx = platform.WithTraceID(ctx, sc.TraceID.String())
			}
			if id, ok := r.Context().Value(CorrelationContextKey).(string); ok && id != "" {
				span.SetAttribute("correlation_id", id)
			}
			span.SetAttribute("http_method", edgeMethod(r.Method))

			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			defer func() {
				// Runs on panic too, so the span is always ended; the panic
				// continues to the server's own recovery unchanged.
				span.SetAttribute("http_status_code", sw.status)
				if sw.status >= http.StatusInternalServerError {
					span.RecordError(edgeStatusError(sw.status))
				}
				span.End()
			}()
			next.ServeHTTP(sw, r.WithContext(ctx)) //nolint:contextcheck // ctx derives from r.Context() through runtime.Tracer.Start, which the linter cannot see through
		})
	}
}

// edgeMethod maps the request method to a closed set, so a client cannot mint
// arbitrary attribute values.
func edgeMethod(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return m
	default:
		return "OTHER"
	}
}

// edgeStatusError is a 5xx outcome as a telemetry.SafeError: its text is a
// fixed vocabulary, so a span records "http status 503", never a message.
type edgeStatusError int

// Error implements error.
func (e edgeStatusError) Error() string { return "http status " + strconv.Itoa(int(e)) }

// SafeErrorText implements telemetry.SafeError: the text is fixed vocabulary.
func (e edgeStatusError) SafeErrorText() string { return e.Error() }

// statusWriter records the status the handler writes.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

// WriteHeader records the first status written, then forwards it.
func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status, w.wroteHeader = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write marks the header written (an implicit 200), then forwards b.
func (w *statusWriter) Write(b []byte) (int, error) {
	w.wroteHeader = true
	n, err := w.ResponseWriter.Write(b)
	if err != nil {
		return n, fmt.Errorf("edge trace: write response: %w", err)
	}
	return n, nil
}

// Unwrap lets http.ResponseController reach the underlying writer (Flush,
// deadlines), so wrapping does not remove capabilities.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
