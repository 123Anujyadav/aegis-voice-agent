module github.com/callscreen/callscreen-platform/packages/go/telemetry

go 1.25.0

// runtime declares the tracing port this module implements (ADR-0026). It is
// frozen and depends only on metrics, so this import cannot form a cycle.
require github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0

// platform is used by tests only, to prove the log exporter's output passes
// the platform's redacting handler.
require github.com/callscreen/callscreen-platform/packages/go/platform v0.0.0

require github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0 // indirect

// Unpublished workspace modules, resolved by relative path so the module also
// builds standalone with GOWORK=off.
replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../runtime

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics

replace github.com/callscreen/callscreen-platform/packages/go/platform => ../platform
