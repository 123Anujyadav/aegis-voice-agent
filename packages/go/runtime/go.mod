// =============================================================================
// packages/go/runtime — the Aegis AI Runtime Core.
//
// DELIBERATE CONSTRAINT: THIS MODULE HAS NO EXTERNAL DEPENDENCIES.
//
// It follows the same rule as packages/go/platform, and for a stronger reason.
// This module is the substrate every AI interaction runs on. A compromised
// transitive dependency here would sit directly on the path between a hostile
// caller's speech and a language model — the single most security-sensitive
// position in the platform.
//
// It is also the reason "no framework lock-in" is achievable rather than
// aspirational. There is no framework to be locked into: the kernel is
// stdlib-only, and every integration point (gRPC transport to Python model
// hosts, Kafka event publication, Redis session persistence, OpenTelemetry
// export) is an INTERFACE defined here and implemented in a sibling module that
// services opt into.
//
// Concretely, that means:
//
//   1. The kernel compiles and its full unit-test suite runs offline, with no
//      network, no broker, no database, and no model provider.
//   2. Swapping Anthropic for a self-hosted model, or gRPC for something else,
//      touches an adapter module and never this one.
//   3. The supply-chain surface of the most sensitive component in the platform
//      is exactly the Go standard library.
//
// GO VERSION. This module declares go 1.25.0, per the Phase 10A requirement,
// and go.work was raised to 1.25.0 to admit it. Sibling modules remain at
// 1.23.0 and continue to build: a workspace directive is a ceiling for the
// modules under it, not a floor they must match, so this is additive rather
// than a forced repository-wide migration.
//
// Note that ARCHITECTURE_FREEZE.md §5 does not pin a Go version — it pins the
// language per plane — so this is not a freeze violation. The Python 3.12 pin
// in that table IS explicit, and Phase 10A's Python 3.13 requirement therefore
// does need a superseding ADR. See docs/runtime/ENGINEERING_AUDIT.md §A1.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/runtime

go 1.25.0

// The shared metric instruments (Counter, Gauge, Histogram). First-party and
// dependency-free, so adopting it does not widen this module's supply chain:
// its transitive closure remains the Go standard library.
//
// It replaces a ~490-line private copy of the same primitives that lived in
// this package. See docs/hardening/METRICS_MIGRATION_REPORT.md.
require github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics
