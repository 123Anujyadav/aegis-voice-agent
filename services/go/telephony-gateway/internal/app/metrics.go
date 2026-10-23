package app

import (
	"context"
	"errors"

	"github.com/callscreen/callscreen-platform/packages/go/metrics"
	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/services/go/telephony-gateway/internal/fence"
)

// Fence readiness metric names. They are subsystem-prefixed, as metricsexport
// expects (ADR-0013), because the exporter adds no subsystem label.
const (
	// MetricFenceChecks counts readiness evaluations of the fence, by result.
	MetricFenceChecks = "telephony_gateway_fence_readiness_checks_total"
	// MetricFenceOwned is 1 while the most recent readiness evaluation found
	// this process the fence owner, and 0 otherwise.
	MetricFenceOwned = "telephony_gateway_fence_owned"
)

// Results recorded under the "result" label. The vocabulary is closed, so the
// label cannot grow without bound.
const (
	ResultOwned    = "owned"     // the check passed: this process owns the fence
	ResultNotOwner = "not_owner" // fence.ErrNotOwner, including "not configured"
	ResultError    = "error"     // any other failure
)

// Metrics is the telephony-gateway's instrument set.
//
// WHY READINESS EVALUATIONS: they are the fence's real, recurring observable
// work in production. Every /readyz probe evaluates the fence, and its answer
// decides whether this instance receives traffic. Counting the outcomes needs
// no change to the fence itself: they are recorded by wrapping the checker the
// fence registers, so the fence's acquisition and heartbeat logic is untouched.
type Metrics struct {
	reg    *metrics.Registry
	checks *metrics.Counter
	owned  *metrics.Gauge
}

// NewMetrics registers the gateway's instruments in a fresh registry.
func NewMetrics() *Metrics {
	reg := metrics.NewRegistry()
	return &Metrics{
		reg:    reg,
		checks: reg.Counter(MetricFenceChecks, "result"),
		owned:  reg.Gauge(MetricFenceOwned),
	}
}

// Registry returns the registry to expose, for example through
// metricsexport.Source.
func (m *Metrics) Registry() *metrics.Registry { return m.reg }

// observeFenceCheck records one readiness evaluation's outcome.
func (m *Metrics) observeFenceCheck(err error) {
	switch {
	case err == nil:
		m.checks.Inc(ResultOwned)
		m.owned.Set(1)
	case errors.Is(err, fence.ErrNotOwner):
		m.checks.Inc(ResultNotOwner)
		m.owned.Set(0)
	default:
		m.checks.Inc(ResultError)
		m.owned.Set(0)
	}
}

// observedChecker is a platform.Checker that records each evaluation of the
// checker it wraps, then returns that checker's result unchanged.
type observedChecker struct {
	inner platform.Checker
	m     *Metrics
}

// Name implements platform.Checker: the wrapped checker's name, so /readyz
// reports the fence exactly as before.
func (c observedChecker) Name() string { return c.inner.Name() }

// Check implements platform.Checker.
func (c observedChecker) Check(ctx context.Context) error {
	err := c.inner.Check(ctx)
	c.m.observeFenceCheck(err)
	return err
}

// observe wraps c when metrics are enabled, and returns it unchanged otherwise.
func observe(c platform.Checker, m *Metrics) platform.Checker {
	if m == nil {
		return c
	}
	return observedChecker{inner: c, m: m}
}
