// Package evaluation is the Enterprise AI Evaluation Platform: permanent
// infrastructure for continuously evaluating every AI subsystem before
// deployment.
//
// # This is not a test suite, and the difference is structural
//
// A test suite asserts. Its expectations live in code, next to the thing under
// test, which means a refactor can change the behaviour and the expectation in
// one commit and nobody notices. It answers one question — pass or fail — and it
// answers it about the code the author was thinking about.
//
// This platform does not assert. There is not one assertion in it.
//
//	Scenario  →  Observation      what actually happened
//	Golden    =  Observation      what happened when somebody approved it
//	Verdict   =  compare(the two) what changed, and by how much
//
// The expectation is DATA: versioned, attributed, carrying a reason and a
// recorded approver. Changing it is a reviewable act rather than an edit to a
// line that was going red.
//
// And the output is not pass/fail. It is a [Verdict] with five values, of which
// the important two are Drift and Fail. **Drift is a behaviour change against an
// approved baseline; Fail is the scenario itself breaking.** A test suite
// conflates them, which is why "the tests are red" is a sentence that carries no
// information. Drift may well be an intended improvement. Failure never is.
//
// # It depends on nothing it evaluates
//
// This module imports the Phase 10A runtime, for its Clock, and nothing else.
// The conversation engine, memory engine, tool runtime and governance engine do
// not appear in its dependency graph.
//
// A [Subject] is addressed through an interface: a name, a set of capabilities,
// and a way to open a [Session] that executes [Step] values. The platform never
// learns what it is evaluating. The adapters that DO import the five frozen
// phases live in packages/go/evalsubjects, so the boundary is one command away
// from being verified rather than being a claim in a document.
//
// That is also what makes it provider agnostic in the sense that matters: a
// subsystem rewritten in Python behind a gRPC adapter is evaluated by the same
// scenarios, against the same goldens, with no change here.
//
// # Behaviour and time are separated everywhere
//
// [Observation.BehaviourPrint] fingerprints outputs, errors, events and state.
// [Observation.Timings] carries durations. They never mix.
//
// Determinism is checked on behaviour, because two runs of a deterministic
// system produce identical outputs and different durations — a system that
// produced identical durations would be a system with no clock. Latency
// regression is checked on timings, because that is the only place a slowdown
// can appear. A single "did the run match" fingerprint would make every
// determinism check flaky and every latency check blind.
//
// # A golden is never recorded automatically
//
// Recording produces a candidate. Promotion to golden requires an explicit
// approval carrying an author and a reason.
//
// The alternative — a platform that updates its own baseline when it sees a
// change — is a platform that reports no drift, ever. That is the classic
// golden-file failure and it is the one thing an evaluation platform must not
// do, because it converts a silent regression into a silent regression with a
// green dashboard.
//
// # What this package does not contain
//
// No prompts, no models, no telephony, no fraud logic, no business rules and no
// user interface. [DashboardModel] is a set of view structs with no renderer,
// because the shape of what an operator needs to see is a design decision worth
// freezing and the pixels are not.
//
// # Invariants
//
// Twelve, listed in docs/evaluation/EVALUATION_ARCHITECTURE.md and each enforced
// at a named place in the code. Most are enforced by absence — a missing import,
// a missing field, a missing constructor — because enforcement by absence cannot
// be forgotten, misconfigured, or switched off the week before a release.
package evaluation
