// =============================================================================
// packages/go/metricsexport — Prometheus text exposition for metrics.Registry.
//
// WHY THIS MODULE EXISTS SEPARATELY
//
// packages/go/metrics is deliberately network-free. Its module header states the
// rule plainly: "It does not export to Prometheus, OpenTelemetry, or anything
// else. Snapshot() returns plain values and an adapter converts them. Instrument
// code must not acquire a network dependency." This module IS that adapter, and
// it lives outside metrics so that the rule keeps holding.
//
// It is also not part of packages/go/platform. platform is the service host and
// every service imports it, including services outside the AI plane; putting the
// exporter there would force a billing service to link an exposition formatter
// it may never serve. Kept separate, a test, a CLI or an admin tool can render an
// exposition without importing a service runtime. This is the same reasoning
// metrics itself records for not living in packages/go/runtime.
//
// DEPENDENCIES: packages/go/metrics, and the standard library. Nothing else.
// The platform's third-party dependency count is zero and ADR-0013 chose this
// design specifically to keep it there.
//
// See docs/adr/0013-metrics-exposition-format.md.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/metricsexport

go 1.25.0

require github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics
