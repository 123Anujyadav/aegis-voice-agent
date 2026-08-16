// telephony-gateway - SIP/SIPS signalling, carrier trunk management, DID pool and call admission control.
//
// Each service is its own module so it declares only the dependencies it
// actually uses. An unrelated upgrade in another service cannot force a rebuild
// or a security review here, and the container image carries only this
// service's dependency closure (Phase 1 SS13).
module github.com/callscreen/callscreen-platform/services/go/telephony-gateway

go 1.23.0

require github.com/callscreen/callscreen-platform/packages/go/platform v0.0.0

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
