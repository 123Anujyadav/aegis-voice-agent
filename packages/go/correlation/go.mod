// =============================================================================
// packages/go/correlation — cross-subsystem correlation conformance.
//
// WHY THIS MODULE EXISTS
//
// packages/go/runtime now owns CorrelationID, and telephony, media, governance
// and toolruntime alias it (ADR-0014). An alias makes the compiler enforce that
// they are the SAME type, but it does not prove that a correlation identity
// actually survives a trip through the real engines — a subsystem can still
// accept one and record a different one, or drop it, and nothing about the type
// system objects.
//
// This module holds the test that drives the real engines end to end and
// asserts the identity that comes out is the one that went in. It contains no
// mechanism of its own: correlation is runtime's type and the subsystems' field,
// and a helper here would be exactly the parallel convention this phase removed.
//
// It is a separate module for the same reason packages/go/evalsubjects is: it is
// the only place allowed to import several frozen subsystems at once, and that
// coupling is deliberate and quarantined here rather than spread across them.
//
// DEPENDENCIES: five first-party modules, no third-party.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/correlation

go 1.25.0

require (
	github.com/callscreen/callscreen-platform/packages/go/governance v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/media v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/telephony v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/toolruntime v0.0.0
)

require github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0 // indirect

replace github.com/callscreen/callscreen-platform/packages/go/governance => ../governance

replace github.com/callscreen/callscreen-platform/packages/go/media => ../media

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics

replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../runtime

replace github.com/callscreen/callscreen-platform/packages/go/telephony => ../telephony

replace github.com/callscreen/callscreen-platform/packages/go/toolruntime => ../toolruntime
