// =============================================================================
// packages/go/metrics — the platform metric instruments.
//
// WHY THIS MODULE EXISTS
//
// Phases 10A through 10F each grew their own Counter, Gauge and Histogram. Six
// copies, roughly 3,000 lines, semantically near-identical and observably NOT
// identical: three different `Sample` shapes, and three of the six could not
// export histogram bucket data from Snapshot() at all. A cross-subsystem latency
// dashboard was therefore not buildable, which is the concrete cost the "A1"
// finding had been describing abstractly for four consecutive phase audits.
//
// This module is the single implementation. It carries the same deliberate
// constraint as packages/go/runtime and packages/go/platform:
//
//	THIS MODULE HAS NO DEPENDENCIES. NOT EVEN FIRST-PARTY ONES.
//
// That is what lets every subsystem adopt it without acquiring anything else,
// and what lets a service outside the AI plane — billing, analytics — use the
// same instruments without importing the AI runtime. The alternative considered
// and rejected was exporting constructors from packages/go/runtime: every AI
// phase already depends on the runtime so it would have cost nothing there, but
// it would have forced unrelated services to import a conversation kernel to
// count HTTP requests.
//
// WHAT IT DELIBERATELY DOES NOT DO
//
// It does not export to Prometheus, OpenTelemetry, or anything else. Snapshot()
// returns plain values and an adapter converts them. Instrument code must not
// acquire a network dependency; that is the whole reason the AI phases were able
// to keep their supply chain at zero.
//
// It does not define bucket boundaries. Those are a DOMAIN decision and they
// correctly differ per subsystem: governance decides in hundreds of nanoseconds,
// a conversation turn takes seconds. A shared bucket set would be actively wrong
// — a fact worth recording, because the earlier audits mistakenly listed
// divergent buckets as the defect. The defect was the duplicated machinery and
// the inconsistent scrape surface, not the boundaries.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/metrics

go 1.25.0
