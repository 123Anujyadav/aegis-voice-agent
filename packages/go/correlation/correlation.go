// Package correlation holds the cross-subsystem correlation conformance suite.
//
// It defines no correlation mechanism. [runtime.CorrelationID] is the one
// canonical identity and the subsystems carry it in their own fields; anything
// added here would be the second convention this phase exists to remove.
//
// What it does define is compile-time proof that the aliases are real. See
// ADR-0014.
package correlation

import (
	gov "github.com/callscreen/callscreen-platform/packages/go/governance"
	"github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	tel "github.com/callscreen/callscreen-platform/packages/go/telephony"
	tr "github.com/callscreen/callscreen-platform/packages/go/toolruntime"
)

// Compile-time proof that all five names are ONE type.
//
// These are plain assignments, not conversions. Go permits assignment between
// two named types only when they are identical, so if any subsystem went back
// to declaring its own `type CorrelationID string` this file would stop
// compiling — and it would stop compiling in this module rather than at some
// distant call site that happens to bridge two subsystems.
//
// That is the property worth pinning. Before ADR-0014 each of these lines
// required an explicit string conversion, and a conversion is precisely where
// one identity can be silently replaced by another with the compiler's
// blessing.
var (
	_ rt.CorrelationID = tel.CorrelationID("")
	_ rt.CorrelationID = media.CorrelationID("")
	_ rt.CorrelationID = gov.CorrelationID("")
	_ rt.CorrelationID = tr.CorrelationID("")

	// And the reverse direction, so the proof is symmetric rather than relying
	// on runtime happening to be the assignment target everywhere.
	_ tel.CorrelationID   = rt.CorrelationID("")
	_ media.CorrelationID = rt.CorrelationID("")
	_ gov.CorrelationID   = rt.CorrelationID("")
	_ tr.CorrelationID    = rt.CorrelationID("")

	// And directly between two subsystems that do not import each other, which
	// is the assembly a trace actually performs.
	_ tel.CorrelationID = gov.CorrelationID("")
	_ tr.CorrelationID  = media.CorrelationID("")
)
