// pgshared - PostgreSQL connection helpers that never leak credentials, and
// the shared DPoP replay store (identity_dpop_replay). Shared by identity and
// edge-api.
module github.com/callscreen/callscreen-platform/packages/go/pgshared

go 1.25.0

require (
	github.com/callscreen/callscreen-platform/packages/go/dpop v0.0.0
	github.com/jackc/pgx/v5 v5.10.0
)

require (
	github.com/callscreen/callscreen-platform/packages/go/accesstoken v0.0.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)

// Not yet fetchable remotes; see the identical directives in every module of
// this workspace.
replace github.com/callscreen/callscreen-platform/packages/go/dpop => ../dpop

replace github.com/callscreen/callscreen-platform/packages/go/accesstoken => ../accesstoken
