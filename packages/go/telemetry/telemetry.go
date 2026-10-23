// Package telemetry implements distributed tracing for the platform
// (ADR-0026, accepted 2026-10-23).
//
// It provides:
//   - RecordingTracer, the implementation of the frozen runtime.Tracer and
//     runtime.Span port. A kernel takes it through runtime.WithTracer;
//   - W3C Trace Context identity: ParseTraceparent and FormatTraceparent,
//     plus SpanContext propagation through a context;
//   - the Exporter interface, and LogExporter, which writes one structured
//     record per span through the platform's redacting logger.
//
// Attributes and error text are allowlisted (ADR-0026 rules 4 and 6). No
// backend is configured here: the backend adapter is ADR-0028's, behind
// Exporter.
//
// This file previously held a stub: a second, incompatible Tracer interface,
// a no-op MetricsExporter, and an InitializeOpenTelemetry that configured
// nothing. None of it had an importer, and accepting ADR-0026 replaced it.
// Metrics are exported by packages/go/metricsexport (ADR-0013).
package telemetry
