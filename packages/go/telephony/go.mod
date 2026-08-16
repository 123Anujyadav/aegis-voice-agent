// =============================================================================
// packages/go/telephony — the Enterprise Telephony Runtime.
//
// TWO FIRST-PARTY DEPENDENCIES, NO THIRD-PARTY ONES.
//
// This module requires packages/go/runtime (Phase 10A, frozen) and
// packages/go/metrics (Phase 10.5). Both are dependency-free, so the transitive
// closure of this module is the Go standard library — the same property every
// module in the AI plane has held since Phase 10A, and the reason the platform
// has no supply-chain surface.
//
// THERE IS NO TELEPHONY LIBRARY HERE, AND THAT IS THE POINT.
//
// No Twilio, Exotel, Plivo, Vonage, Asterisk, FreeSWITCH or Linphone SDK. No
// SIP stack, no RTP stack, no media library. This module manages the LIFECYCLE
// of a call — its states, its session, its recovery — and knows nothing about
// how the call is carried. A provider adapter implements [Provider] and lives
// in a sibling module that a service opts into.
//
// That boundary is what makes "provider agnostic" checkable rather than
// aspirational: a carrier SDK cannot leak into this module, because this
// module's go.mod does not require one and never will.
//
// WHAT THE RUNTIME DEPENDENCY BUYS. Clock (so every timeout is testable without
// sleeping), FSM (so the call state machine is declared and validated rather
// than assembled from booleans), and the identifier conventions. Reimplementing
// any of those here would produce a second, subtly different version of a
// solved problem — which is exactly the mistake Phase 10.5 spent a phase
// undoing for metrics.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/telephony

go 1.25.0

require (
	github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0
)

// The module paths above are not fetchable remotes: this monorepo is private
// and unpublished. The relative replaces also keep this module buildable
// standalone with GOWORK=off, which CI relies on to prove the go.mod is
// self-sufficient rather than leaning on the workspace.
replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../runtime

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics
