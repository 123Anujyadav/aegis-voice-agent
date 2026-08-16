// =============================================================================
// packages/go/evaluation — the Enterprise AI Evaluation Platform.
//
// DELIBERATE CONSTRAINT: THE PLATFORM CORE DEPENDS ON NOTHING IT EVALUATES.
//
// This module requires exactly one first-party package — the Phase 10A runtime,
// for its Clock — and nothing else. It does NOT import the conversation engine,
// the memory engine, the tool runtime or the governance engine, and it never
// will.
//
// That is the difference between an evaluation platform and a test suite.
//
// A test suite imports what it tests, and its expectations live in code
// alongside the thing under test, which is why a refactor can update the
// expectation and the test in one commit and nobody notices. This platform
// addresses a [Subject] through an interface, so it cannot see the internals of
// what it evaluates, cannot be updated in the same commit by accident, and can
// evaluate a subsystem written in another language behind a gRPC adapter without
// a line changing here.
//
// The adapters that DO import the five frozen phases live in a separate module,
// packages/go/evalsubjects, so the boundary is checkable with one command:
//
//	go list -deps ./... | grep callscreen
//
// NO EVALUATION FRAMEWORK. There is no OpenAI Evals, no Anthropic Evaluation
// SDK, no DeepEval, no Ragas, no Promptfoo, no LangSmith, no LangFuse, no
// Phoenix, no TruLens and no DSPy evaluation. The scenario engine, the golden
// framework, the replay engine, the regression engine, the determinism engine
// and the benchmark framework are written here.
//
// NO ASSERTIONS. There is not one assertion in this module. A scenario produces
// an [Observation]; a [Golden] is an approved observation; a [Verdict] is a
// comparison between them. The expectation is DATA — versioned, attributed and
// reviewable — rather than a line of code somebody can edit until it passes.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/evaluation

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
