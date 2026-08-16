// =============================================================================
// packages/go/memory — the Enterprise Memory Engine.
//
// ONE DEPENDENCY, AND IT IS THE RUNTIME.
//
// This module depends on packages/go/runtime (Phase 10A, frozen) and nothing
// else. The runtime has zero external dependencies, so the transitive closure
// here is the Go standard library plus one first-party package.
//
// IT DELIBERATELY DOES NOT DEPEND ON packages/go/conversation.
//
// Memory is a PEER of the conversation engine, not a child of it. The brief
// requires this layer to serve the consumer assistant, the business
// receptionist, fraud intelligence, telephony intelligence and a future
// multi-agent runtime. A dependency on the conversation engine would make
// every one of those consumers drag in dialogue machinery they do not use, and
// would quietly make "conversation memory" the privileged case rather than one
// kind among eight.
//
// The arrow points the other way: an orchestration layer above both wires them
// together. Neither imports the other.
//
// WHAT THE RUNTIME DEPENDENCY BUYS. Clock (so every TTL, promotion window and
// expiry sweep is testable without sleeping), FSM (so the record lifecycle is
// declared rather than assembled from booleans), and the identifier
// conventions. Reimplementing any of them here would produce a second, subtly
// different version of a solved problem.
//
// NO MEMORY FRAMEWORK, NO VECTOR STORE. There is no LangChain Memory, no
// LlamaIndex, no Mem0, no Zep, no Chroma, Pinecone, Weaviate or Milvus client.
// The index layer is built here, from maps and sorted slices, because the
// access patterns this platform actually has are exact-match, prefix and time
// range — none of which needs a vector.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/memory

go 1.25.0

require github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0

// The module path above is not a fetchable remote: this monorepo is private and
// unpublished. The relative replace also keeps this module buildable standalone
// with GOWORK=off, which CI relies on to prove the go.mod is self-sufficient.
replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../runtime

// The shared metric instruments (Counter, Gauge, Histogram). First-party and
// dependency-free, so adopting it does not widen this module's supply chain:
// its transitive closure remains the Go standard library.
//
// It replaces a ~490-line private copy of the same primitives that lived in
// this package. See docs/hardening/METRICS_MIGRATION_REPORT.md.
require github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics
