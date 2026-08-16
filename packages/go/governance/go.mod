// =============================================================================
// packages/go/governance — the Enterprise Safety, Policy & Governance Engine.
//
// DELIBERATE CONSTRAINT: ONE DEPENDENCY, FIRST-PARTY. NOTHING EXTERNAL.
//
// The same rule as every phase since 10A, and here it is not merely prudent —
// it is what makes the module's central claim checkable. This engine is the
// final authority on whether the platform may act. If its supply chain were
// wide, "the safety engine decided" would mean "the safety engine and whatever
// forty transitive packages it happens to trust decided".
//
// NO EXTERNAL SAFETY FRAMEWORK. There is no OpenAI Moderation, no Anthropic
// Safety API, no Gemini safety endpoint, no LangChain Guardrails, no Guardrails
// AI, no NeMo Guardrails, no Llama Guard, no Prompt Shields. The policy model,
// the evaluator, the conflict resolution, the consent registry, the risk
// aggregator and the audit trail are written here.
//
// That is not framework avoidance for its own sake. A safety decision made by a
// remote service is a safety decision with an availability dependency and a
// latency budget, and the honest failure mode of "the moderation endpoint timed
// out" is either shipping unreviewed output or dropping a call. This engine
// decides locally, in microseconds, from policies the operator can read.
//
// WHY THIS DEPENDS ON NOTHING IT GOVERNS:
//
// The architectural rule for Phase 10E is that every conversation decision,
// tool execution, memory write and external action passes through this engine.
// That means conversation (10B), memory (10C) and toolruntime (10D) call INTO
// this module. It therefore cannot import any of them — not merely because the
// import cycle would not compile, but because a governance engine that knows
// the shape of its callers ends up with a special case per caller, and a
// special case per caller is how something eventually gets a bypass.
//
// It evaluates an [Action]: a generic description of something a subsystem
// wants to do. There is ONE entry point. A subsystem that bypasses this engine
// does so visibly, because there is exactly one door and it is not optional.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/governance

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
