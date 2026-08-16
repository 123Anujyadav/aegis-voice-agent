// =============================================================================
// packages/go/toolruntime — the Enterprise Tool Calling Runtime.
//
// DELIBERATE CONSTRAINT: ONE DEPENDENCY, FIRST-PARTY. NOTHING EXTERNAL.
//
// The same rule as packages/go/runtime and packages/go/memory, for a reason
// specific to this module: this is the component that EXECUTES things. Every
// other module in the platform reads, transforms or remembers; this one acts on
// the outside world. A compromised transitive dependency here does not leak
// data, it takes actions — books, cancels, charges, calls back. That is the
// position in the system where a supply-chain compromise is worth the most to
// an attacker and where the blast radius is widest.
//
// NO ORCHESTRATION FRAMEWORK. There is no LangChain Tools, no CrewAI Tools, no
// Semantic Kernel plugin host, no OpenAI Agents SDK, no Anthropic Tool SDK, no
// LlamaIndex, no AutoGen. The registry, the planner, the executor, the retry
// engine, the idempotency ledger and the compensation journal are written here.
//
// NO VENDOR TOOL PROTOCOL. The runtime has never heard of OpenAI function
// calling, Anthropic tool use or Gemini function declarations. The conversation
// engine emits a [ToolIntent], which names a CAPABILITY and carries typed
// arguments; translating a provider's tool-call wire format into a ToolIntent
// is an adapter's job and happens in a sibling module. Nothing in this package
// changes if the platform changes model vendor, and nothing in this package
// would need to change if the platform stopped using a language model at all.
//
// WHY THIS DOES NOT DEPEND ON packages/go/memory:
//
// The architectural rule for Phase 10D is that the tool runtime never modifies
// memory. Memory learns what happened by subscribing to execution events. A
// dependency on the memory engine would make that rule a matter of discipline —
// the writer would be right there, one call away, and eventually somebody would
// use it during an incident. Without the import, "the tool runtime cannot write
// memory" is a fact about the build, not a promise in a document.
//
// It does not depend on packages/go/conversation either. The conversation
// engine produces intents; this module consumes a struct, not a package. That
// keeps the tool runtime usable by anything that can describe what it wants —
// a scheduled job, an operator console, a future multi-agent planner — rather
// than only by a dialogue.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/toolruntime

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
