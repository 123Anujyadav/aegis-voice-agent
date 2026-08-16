// =============================================================================
// packages/go/evalsubjects — evaluation adapters for the five frozen phases.
//
// THIS IS WHERE THE COUPLING LIVES, AND IT LIVES HERE ON PURPOSE.
//
// The evaluation platform core (packages/go/evaluation) depends on exactly one
// first-party package and never on anything it evaluates. This module is the
// other side of that boundary: it imports all five frozen phases and implements
// evaluation.Subject for each.
//
// Splitting them into two modules rather than two packages is what makes the
// claim checkable rather than aspirational. Run this in either module:
//
//	go list -deps ./... | grep callscreen
//
// The core lists `runtime` and itself. This one lists all seven. A reviewer does
// not have to take the architecture on trust, and a future contributor cannot
// quietly add an import to the core without the module boundary refusing it.
//
// THESE ADAPTERS MODIFY NOTHING. Every phase from 10A to 10E is frozen and each
// adapter uses only its exported API — mostly its test harness, which each phase
// deliberately exported for exactly this reason. There is no build tag, no
// reflection and no unexported access anywhere in this module.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/evalsubjects

go 1.25.0

require (
	github.com/callscreen/callscreen-platform/packages/go/conversation v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/evaluation v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/governance v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/memory v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/toolruntime v0.0.0
)

// The module paths above are not fetchable remotes: this monorepo is private and
// unpublished. The relative replaces also keep this module buildable standalone
// with GOWORK=off.
replace (
	github.com/callscreen/callscreen-platform/packages/go/conversation => ../conversation
	github.com/callscreen/callscreen-platform/packages/go/evaluation => ../evaluation
	github.com/callscreen/callscreen-platform/packages/go/governance => ../governance
	github.com/callscreen/callscreen-platform/packages/go/memory => ../memory
	github.com/callscreen/callscreen-platform/packages/go/runtime => ../runtime
	github.com/callscreen/callscreen-platform/packages/go/toolruntime => ../toolruntime
)

require github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics
