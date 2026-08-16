package telemetry

import (
	"context"
	"fmt"
)

type Tracer interface {
	StartSpan(ctx context.Context, spanName string) (context.Context, func())
}

type MetricsExporter interface {
	RecordGauge(name string, value float64, labels map[string]string)
	IncrementCounter(name string, value int64, labels map[string]string)
}

type NoopTracer struct{}

func (n *NoopTracer) StartSpan(ctx context.Context, spanName string) (context.Context, func()) {
	return ctx, func() {}
}

type NoopMetricsExporter struct{}

func (n *NoopMetricsExporter) RecordGauge(name string, value float64, labels map[string]string) {}
func (n *NoopMetricsExporter) IncrementCounter(name string, value int64, labels map[string]string) {}

func InitializeOpenTelemetry(serviceName string) (Tracer, MetricsExporter, error) {
	fmt.Printf("Initializing OpenTelemetry for service: %s\n", serviceName)
	return &NoopTracer{}, &NoopMetricsExporter{}, nil
}
