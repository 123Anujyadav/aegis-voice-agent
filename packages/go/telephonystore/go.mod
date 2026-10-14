// =============================================================================
// packages/go/telephonystore — the PostgreSQL adapter for telephony state.
//
// WHY A SEPARATE MODULE. ADR-0015's containment rule: only an adapter module
// depends on pgx, and `telephony` keeps zero third-party dependencies (its own
// go.mod says so). This module is that adapter, exactly as `evalstore` is for
// `evaluation`.
//
// The pgx version and every checksum are the ones `evalstore` already pins, so
// adding this module adds no new third-party module to the workspace.
//
// Data lives in the `telephony` PostgreSQL cluster (ADR-0009 §5).
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/telephonystore

go 1.25.0

require (
	github.com/callscreen/callscreen-platform/packages/go/telephony v0.0.0
	github.com/jackc/pgx/v5 v5.10.0
)

require (
	github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/text v0.39.0 // indirect
)

replace github.com/callscreen/callscreen-platform/packages/go/telephony => ../telephony

replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../runtime

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics
