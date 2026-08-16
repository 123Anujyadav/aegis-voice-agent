// Package evalsubjects implements evaluation.Subject for the five frozen
// phases.
//
// # It modifies nothing
//
// Phases 10A to 10E are approved and frozen. Every adapter here uses only the
// exported API of the phase it evaluates — in most cases the test harness that
// each phase deliberately exported, saying at the time that services embedding
// the engine would need it. This is that need arriving.
//
// There is no build tag, no reflection, no linkname and no unexported access
// anywhere in this module. If an adapter cannot observe something through the
// public API, the correct answer is that the phase does not expose it, and that
// is a finding rather than a thing to work around.
//
// # Every adapter is deterministic or says so
//
// Each subject opens a session with a FakeClock, so a scenario's clock advances
// are reproducible and no adapter depends on wall time. Any adapter that cannot
// make its subject deterministic declares it, and the determinism engine reports
// the divergence rather than the platform hiding it.
//
// # Outcome codes are the adapter's contract
//
// A step's outcome is a short code — "ok", "denied", "not_found" — and it enters
// the behaviour fingerprint. Changing an outcome code changes every golden for
// that subject, which is correct: the platform's view of what the subsystem did
// really has changed.
package evalsubjects

import (
	ev "github.com/callscreen/callscreen-platform/packages/go/evaluation"
)

// All returns every adapter, ready to register with an evaluation runtime.
//
// The convenience the final platform verification uses. Building the set by
// hand is equally valid and is what a deployment evaluating a subset would do.
func All() []ev.Subject {
	return []ev.Subject{
		NewRuntimeSubject(),
		NewConversationSubject(),
		NewMemorySubject(),
		NewToolSubject(),
		NewGovernanceSubject(),
	}
}

// Names returns the subject names the adapters register under.
//
// Exported so a scenario library can reference them without a string literal
// per scenario — a typo in a subject name produces a scenario that is never
// executed and never reported, which is the quietest failure this platform has.
const (
	SubjectRuntime      ev.SubjectName = "runtime"
	SubjectConversation ev.SubjectName = "conversation"
	SubjectMemory       ev.SubjectName = "memory"
	SubjectTool         ev.SubjectName = "toolruntime"
	SubjectGovernance   ev.SubjectName = "governance"
)

// ok is the conventional success outcome, spelled once so a typo cannot make
// one adapter's success look different from another's in a heatmap.
const ok = "ok"

// failed builds a step result for an adapter-level failure.
//
// An adapter failure means THE ADAPTER could not perform the step — not that
// the subject refused. A subject that correctly refuses reports an outcome code
// and Failed=false, which is the distinction the whole platform rests on.
func failed(outcome, detail string) ev.StepResult {
	return ev.StepResult{Failed: true, Outcome: outcome, Detail: detail}
}

// result builds a successful step result.
func result(outcome string, out ev.Values) ev.StepResult {
	return ev.StepResult{Outcome: outcome, Output: out}
}
