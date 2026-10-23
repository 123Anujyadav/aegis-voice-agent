module github.com/callscreen/callscreen-platform/packages/go/middleware

go 1.25.0

require (
	// ADR-0014: one function mints every correlation identity, and it lives in
	// runtime, which depends only on metrics, so no cycle is possible.
	github.com/callscreen/callscreen-platform/packages/go/platform v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/redis v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0
	// EdgeTraceMiddleware (ADR-0026 step 2) opens spans through telemetry.
	github.com/callscreen/callscreen-platform/packages/go/telemetry v0.0.0
)

require github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0 // indirect

replace github.com/callscreen/callscreen-platform/packages/go/platform => ../platform

replace github.com/callscreen/callscreen-platform/packages/go/redis => ../redis

replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../runtime

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics

replace github.com/callscreen/callscreen-platform/packages/go/telemetry => ../telemetry
