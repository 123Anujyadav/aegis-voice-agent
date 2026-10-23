// edge-api - Mobile-facing backend for frontend. The only publicly routable ingress.
//
// Each service is its own module so it declares only the dependencies it
// actually uses. An unrelated upgrade in another service cannot force a rebuild
// or a security review here, and the container image carries only this
// service's dependency closure (Phase 1 SS13).
module github.com/callscreen/callscreen-platform/services/go/edge-api

go 1.25.0

require (
	github.com/callscreen/callscreen-platform/packages/go/accesstoken v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/dpop v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/metricsexport v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/middleware v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/platform v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/telemetry v0.0.0
	github.com/jackc/pgx/v5 v5.10.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)

require (
	github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/pgshared v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/redis v0.0.0 // indirect
)

// The module path above is not yet a fetchable remote: this monorepo is
// private and unpublished. Without a replace directive Go must resolve
// platform@v0.0.0 over the network to compute a version for the workspace
// checksum database, which fails, and takes unrelated modules down with it.
//
// The relative path also keeps each module buildable standalone with
// GOWORK=off, which CI relies on to prove that a module's go.mod is genuinely
// self-sufficient rather than quietly leaning on the workspace to resolve
// something (Phase 1 §16).
//
// This directive is removed once the repository is published and the module
// path resolves on its own.
replace github.com/callscreen/callscreen-platform/packages/go/platform => ../../../packages/go/platform

// accesstoken and dpop are the token and proof verifiers identity issues and
// checks with (internal/auth). middleware supplies problem details and the
// correlation context key; redis, runtime and metrics are its dependencies and,
// like platform above, need a path here because a replace directive applies
// only in the main module. Go 1.25 is dpop's requirement
// (ecdsa.ParseUncompressedPublicKey).
replace github.com/callscreen/callscreen-platform/packages/go/accesstoken => ../../../packages/go/accesstoken

replace github.com/callscreen/callscreen-platform/packages/go/dpop => ../../../packages/go/dpop

replace github.com/callscreen/callscreen-platform/packages/go/middleware => ../../../packages/go/middleware

replace github.com/callscreen/callscreen-platform/packages/go/redis => ../../../packages/go/redis

replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../../../packages/go/runtime

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../../../packages/go/metrics

replace github.com/callscreen/callscreen-platform/packages/go/pgshared => ../../../packages/go/pgshared

replace github.com/callscreen/callscreen-platform/packages/go/telemetry => ../../../packages/go/telemetry

// metricsexport renders the authenticator's outcome counter at GET /metrics.
replace github.com/callscreen/callscreen-platform/packages/go/metricsexport => ../../../packages/go/metricsexport
