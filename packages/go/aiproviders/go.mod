// =============================================================================
// packages/go/aiproviders: adapters from the frozen provider ports to hosted
// AI vendors chosen by Accepted ADRs.
//
// WHY THIS MODULE EXISTS
//
// ADR-0006 (Accepted) puts the four LLM tiers on Claude; the runtime codes
// against runtime.Provider and names no vendor. This module holds the vendor
// side of that seam, so the frozen runtime, speech and media modules stay
// vendor-free.
//
// WHAT IT DOES NOT DO
//
// It activates nothing. An adapter here is built only when a service is
// configured with a credential file, and no service is. Using one is the LLM
// (or speech) spending category's approval, plus the DPDP review for caller
// transcripts (E9, L1): docs/phase19/INTEGRATION-READINESS-REQUIREMENTS.md
// §2.5. Standard library only: no vendor SDK.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/aiproviders

go 1.25.0

require (
	github.com/callscreen/callscreen-platform/packages/go/providerconformance v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0
)

require (
	github.com/callscreen/callscreen-platform/packages/go/media v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/speech v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/telephony v0.0.0 // indirect
)

// The module paths are not fetchable remotes; the relative replaces keep this
// module buildable standalone with GOWORK=off, as CI checks.
replace github.com/callscreen/callscreen-platform/packages/go/providerconformance => ../providerconformance

replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../runtime

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics

replace github.com/callscreen/callscreen-platform/packages/go/media => ../media

replace github.com/callscreen/callscreen-platform/packages/go/speech => ../speech

replace github.com/callscreen/callscreen-platform/packages/go/telephony => ../telephony
