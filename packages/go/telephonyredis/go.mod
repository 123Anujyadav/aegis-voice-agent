// =============================================================================
// packages/go/telephonyredis — the Redis adapter for telephony callback
// deduplication (ADR-0009 §5: idempotency keys are Redis ephemeral state).
//
// NO THIRD-PARTY DEPENDENCY. No Redis client was available offline (none is in
// the workspace or the module cache, and GOPROXY=off), so this module speaks
// the five Redis commands it needs over RESP2 using only the standard library.
// It is deliberately NOT a general client; see resp.go.
//
// A separate module, not part of `telephony`: ADR-0015 keeps adapters out of
// the hand-written modules, and this one follows the same boundary even though
// its closure is first-party plus the standard library.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/telephonyredis

go 1.25.0

require github.com/callscreen/callscreen-platform/packages/go/telephony v0.0.0

require (
	github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0 // indirect
)

replace github.com/callscreen/callscreen-platform/packages/go/telephony => ../telephony

replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../runtime

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics
