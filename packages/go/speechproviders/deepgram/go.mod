// =============================================================================
// packages/go/speechproviders/deepgram: the Deepgram adapter for the frozen
// speech.STTProvider port.
//
// WHY A MODULE OF ITS OWN
//
// ADR-0005 (Accepted) makes Deepgram Nova-3 the secondary recogniser, and
// Deepgram streams only over WebSocket, which the standard library does not
// speak. ADR-0034 (approved by the owner 2026-11-12) admits one small WebSocket
// library for it, contained to this module so that no other module gains it:
// `go list -deps` in any other module shows no github.com/coder/websocket.
//
// WHAT IT DOES NOT DO
//
// It activates nothing. A provider is built only from a mounted key file, and
// no service has one. Deepgram processes outside India, so every use is
// consent-gated (ADR-0012), and activation needs the speech spending category
// (OD-17) and the DPDP review (E9).
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/speechproviders/deepgram

go 1.25.0

require (
	github.com/callscreen/callscreen-platform/packages/go/media v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/providerconformance v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/speech v0.0.0
	github.com/coder/websocket v1.8.15
)

require (
	github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/telephony v0.0.0 // indirect
)

// The module paths are not fetchable remotes; the relative replaces keep this
// module buildable standalone with GOWORK=off, as CI checks.
replace github.com/callscreen/callscreen-platform/packages/go/media => ../../media

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../../metrics

replace github.com/callscreen/callscreen-platform/packages/go/providerconformance => ../../providerconformance

replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../../runtime

replace github.com/callscreen/callscreen-platform/packages/go/speech => ../../speech

replace github.com/callscreen/callscreen-platform/packages/go/telephony => ../../telephony
