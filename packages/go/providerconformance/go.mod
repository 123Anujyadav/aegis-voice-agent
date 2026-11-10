// =============================================================================
// packages/go/providerconformance: behavioural conformance suites for the
// provider ports.
//
// WHY THIS MODULE EXISTS
//
// The product codes against four provider-neutral ports: speech.STTProvider
// and speech.TTSProvider (ADR-0005, ADR-0007), runtime.Provider (ADR-0006)
// and telephony.Provider (ADR-0003).
// Their contracts are written in prose ("must not block indefinitely", "the
// provider closes it when the stream ends", "Close is idempotent", "Generate
// must return promptly"), and nothing checked them for an arbitrary
// implementation. These suites do, so that every future adapter, whichever
// vendor or local model it wraps, has a ready acceptance test.
//
// It imports the frozen speech, runtime, telephony and media modules and edits
// none of them. It names no vendor and calls no network.
// docs/phase19/INTEGRATION-DEPENDENCY-MATRIX.md §2, item 3.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/providerconformance

go 1.25.0

require (
	github.com/callscreen/callscreen-platform/packages/go/media v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/speech v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/telephony v0.0.0
)

require github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0 // indirect

// The module paths are not fetchable remotes; the relative replaces keep this
// module buildable standalone with GOWORK=off, as CI checks.
replace github.com/callscreen/callscreen-platform/packages/go/media => ../media

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics

replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../runtime

replace github.com/callscreen/callscreen-platform/packages/go/speech => ../speech

replace github.com/callscreen/callscreen-platform/packages/go/telephony => ../telephony
