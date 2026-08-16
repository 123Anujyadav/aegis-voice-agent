// =============================================================================
// packages/go/conversation — the Conversation Intelligence Engine.
//
// ONE DEPENDENCY, AND IT IS THE RUNTIME.
//
// This module depends on packages/go/runtime (Phase 10A, frozen) and on nothing
// else. The runtime itself has zero external dependencies, so the transitive
// closure of this module is the Go standard library plus one first-party
// package.
//
// That is deliberate and it is the same argument Phase 10A made, one layer up:
// this module decides what an AI says to a hostile stranger on a live phone
// call. Its supply-chain exposure should be as close to zero as the problem
// permits.
//
// WHAT THE DEPENDENCY BUYS. The runtime supplies the substrate this engine
// needs and refuses to reimplement: Clock (so every timeout is testable), FSM
// (so state machines are declared rather than assembled from booleans), Metrics
// (so instruments are kernel-scoped rather than global), and the identifier and
// error conventions. Reimplementing any of those here would produce a second,
// subtly different version of a solved problem.
//
// WHAT IT DOES NOT BUY. The runtime knows nothing about conversation. It moves
// tokens; this module decides what a turn means. The boundary is strict and is
// stated in doc.go.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/conversation

go 1.25.0

require github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0

// The module path above is not a fetchable remote: this monorepo is private and
// unpublished. The relative replace also keeps this module buildable standalone
// with GOWORK=off, which CI relies on to prove the go.mod is self-sufficient
// rather than leaning on the workspace. Removed once the repository publishes.
replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../runtime

// The shared metric instruments (Counter, Gauge, Histogram). First-party and
// dependency-free, so adopting it does not widen this module's supply chain:
// its transitive closure remains the Go standard library.
//
// It replaces a ~490-line private copy of the same primitives that lived in
// this package. See docs/hardening/METRICS_MIGRATION_REPORT.md.
require github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics
