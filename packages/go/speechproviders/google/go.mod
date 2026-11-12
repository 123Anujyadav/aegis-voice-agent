// =============================================================================
// packages/go/speechproviders/google: the Google Cloud Speech-to-Text v2
// streaming adapter for speech.STTProvider.
//
// WHY A SEPARATE MODULE
//
// ADR-0005 (Accepted) makes Google STT v2 the primary recogniser, and v2
// streams only over gRPC. ADR-0034 (approved by the owner 2026-11-12) admits
// gRPC and Google's generated Speech v2 client, contained to the adapter's own
// module: their transitive set (protobuf, Google auth, OpenTelemetry) is large,
// and keeping it here means no other module, including packages/go/aiproviders,
// gains any of it. `go list -deps` on every other module is unchanged.
//
// WHY go 1.26.0
//
// cloud.google.com/go/speech v1.38.1, google.golang.org/grpc v1.85.0 and
// google.golang.org/api v0.310.0 all declare go 1.26.0. CI builds with
// go1.26.11 under GOTOOLCHAIN=local, which satisfies it.
//
// WHAT IT DOES NOT DO
//
// It activates nothing: no service builds the provider. See README.md.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/speechproviders/google

go 1.26.0

require (
	cloud.google.com/go/speech v1.38.1
	github.com/callscreen/callscreen-platform/packages/go/media v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/providerconformance v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/speech v0.0.0
	google.golang.org/api v0.310.0
	google.golang.org/grpc v1.85.0
	google.golang.org/protobuf v1.36.12
)

require (
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/auth v0.24.2 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.3.0 // indirect
	cloud.google.com/go/compute/metadata v0.10.0 // indirect
	cloud.google.com/go/longrunning v1.3.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/telephony v0.0.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/s2a-go v0.1.11 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.24 // indirect
	github.com/googleapis/gax-go/v2 v2.27.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.72.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.72.0 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/oauth2 v0.37.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	google.golang.org/genproto v0.0.0-20261012220534-4341edf3f941 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20261012220534-4341edf3f941 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261019180522-ce908d29299b // indirect
)

replace github.com/callscreen/callscreen-platform/packages/go/media => ../../media

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../../metrics

replace github.com/callscreen/callscreen-platform/packages/go/providerconformance => ../../providerconformance

replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../../runtime

replace github.com/callscreen/callscreen-platform/packages/go/speech => ../../speech

replace github.com/callscreen/callscreen-platform/packages/go/telephony => ../../telephony
