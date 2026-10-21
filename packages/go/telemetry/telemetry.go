// Package telemetry defines the tracing and metrics interfaces services depend
// on, with no-op implementations.
package telemetry

import (
	"context"
	"fmt"
)

// Tracer starts trace spans. StartSpan returns a context for the span and a
// function that ends it.
type Tracer interface {
	StartSpan(ctx context.Context, spanName string) (context.Context, func())
}

// MetricsExporter records gauge and counter metrics.
type MetricsExporter interface {
	RecordGauge(name string, value float64, labels map[string]string)
	IncrementCounter(name string, value int64, labels map[string]string)
}

// NoopTracer is a Tracer that records nothing.
type NoopTracer struct{}

// StartSpan returns ctx unchanged and an end function that does nothing.
func (n *NoopTracer) StartSpan(ctx context.Context, spanName string) (context.Context, func()) {
	return ctx, func() {}
}

// NoopMetricsExporter is a MetricsExporter that discards every value.
type NoopMetricsExporter struct{}

// RecordGauge discards the value.
func (n *NoopMetricsExporter) RecordGauge(name string, value float64, labels map[string]string) {}

// IncrementCounter discards the value.
func (n *NoopMetricsExporter) IncrementCounter(name string, value int64, labels map[string]string) {}

// InitializeOpenTelemetry prints the service name to standard output and
// returns a NoopTracer and a NoopMetricsExporter. Despite its name, it does not
// configure OpenTelemetry, and it never returns an error.
func InitializeOpenTelemetry(serviceName string) (Tracer, MetricsExporter, error) {
	fmt.Printf("Initializing OpenTelemetry for service: %s\n", serviceName)
	return &NoopTracer{}, &NoopMetricsExporter{}, nil
}
