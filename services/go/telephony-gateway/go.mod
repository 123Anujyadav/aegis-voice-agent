// telephony-gateway - SIP/SIPS signalling, carrier trunk management, DID pool and call admission control.
//
// Each service is its own module so it declares only the dependencies it
// actually uses. An unrelated upgrade in another service cannot force a rebuild
// or a security review here, and the container image carries only this
// service's dependency closure (Phase 1 SS13).
module github.com/callscreen/callscreen-platform/services/go/telephony-gateway

go 1.25.0

require (
	github.com/aws/aws-sdk-go-v2 v1.47.1
	github.com/aws/aws-sdk-go-v2/credentials v1.20.6
	github.com/aws/aws-sdk-go-v2/feature/rds/auth v1.7.4
	github.com/aws/aws-sdk-go-v2/service/sts v1.51.1
	github.com/callscreen/callscreen-platform/packages/go/platform v0.0.0
	github.com/jackc/pgx/v5 v5.10.0
)

require (
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.4 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.4 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.19 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.4 // indirect
	github.com/aws/smithy-go v1.28.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.41.0 // indirect
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
