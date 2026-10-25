// identity - Device attestation, session tokens and MSISDN verification.
//
// Each service is its own module so it declares only the dependencies it
// actually uses. An unrelated upgrade in another service cannot force a rebuild
// or a security review here, and the container image carries only this
// service's dependency closure (Phase 1 SS13).
module github.com/callscreen/callscreen-platform/services/go/identity

go 1.25.0

require (
	github.com/callscreen/callscreen-platform/packages/go/accesstoken v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/dpop v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/middleware v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/platform v0.0.0
	github.com/jackc/pgx/v5 v5.10.0
)

require github.com/callscreen/callscreen-platform/packages/go/telemetry v0.0.0

require (
	cloud.google.com/go/compute/metadata v0.3.0 // indirect
	golang.org/x/oauth2 v0.36.0
)

require (
	github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/metricsexport v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/pgshared v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/redis v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.29.0 // indirect
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

// middleware supplies UserClaims and WriteProblemDetails to internal/app. The
// import existed before this directive did, so the module built under the
// workspace and failed under GOWORK=off — the exact gap the standalone-build
// rule above exists to catch.
replace github.com/callscreen/callscreen-platform/packages/go/middleware => ../../../packages/go/middleware

// redis is middleware's dependency, not one this service imports — hence the
// `// indirect` marker on its require above.
//
// It needs a path here because a replace directive only takes effect in the
// MAIN module: middleware's own `replace ... => ../redis` is ignored when
// identity is the module being built, so identity must supply a path for every
// first-party module in its transitive closure. Same reason as platform above —
// the paths are not fetchable until the repository is published.
replace github.com/callscreen/callscreen-platform/packages/go/redis => ../../../packages/go/redis

// runtime and metrics entered this closure with ADR-0014: middleware now mints
// correlation identities through runtime.NewCorrelationID rather than its own
// timestamp scheme, and runtime depends on metrics. Neither is imported here,
// hence `// indirect` above — but both need a path for the same reason redis
// does: middleware's own replace directives do not apply when identity is the
// main module.
replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../../../packages/go/runtime

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../../../packages/go/metrics

// accesstoken and dpop moved out of internal/ so edge-api can verify what this
// service issues with the same code. Same reason for the path as platform's.
replace github.com/callscreen/callscreen-platform/packages/go/accesstoken => ../../../packages/go/accesstoken

replace github.com/callscreen/callscreen-platform/packages/go/dpop => ../../../packages/go/dpop

// pgshared holds the connection helpers and replay store that moved out of
// internal/pgstore so edge-api can share them. Same reason for the path.
replace github.com/callscreen/callscreen-platform/packages/go/pgshared => ../../../packages/go/pgshared

replace github.com/callscreen/callscreen-platform/packages/go/telemetry => ../../../packages/go/telemetry

replace github.com/callscreen/callscreen-platform/packages/go/metricsexport => ../../../packages/go/metricsexport
