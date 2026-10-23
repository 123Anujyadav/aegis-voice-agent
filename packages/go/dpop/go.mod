// dpop - DPoP proofs of possession (RFC 9449), ES256.
//
// Shared by identity (enrolment and refresh proofs) and edge-api (per-request
// proofs bound to an access token).
module github.com/callscreen/callscreen-platform/packages/go/dpop

go 1.25.0

require github.com/callscreen/callscreen-platform/packages/go/accesstoken v0.0.0

// Not yet a fetchable remote; see the identical directive in every module of
// this workspace.
replace github.com/callscreen/callscreen-platform/packages/go/accesstoken => ../accesstoken
