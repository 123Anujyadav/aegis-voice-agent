// Command server is the entrypoint for the telephony-gateway service.
//
// SIP/SIPS signalling, carrier trunk management, DID pool and call admission control.
//
// PHASE 2 SCOPE: this wires configuration, logging, health and the lifecycle.
// The domain behaviour described above is implemented in a later phase; nothing
// here encodes it yet. What is here is production code, not scaffolding: the
// startup contract, the failure semantics and the shutdown ordering are final.
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"

	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/services/go/telephony-gateway/internal/app"
)

// serviceName identifies this service in configuration, logs, metrics and
// traces. It matches the directory name under services/go, which is what makes
// telemetry attributable without a lookup table.
const serviceName = "telephony-gateway"

// envPrefix namespaces this service's environment variables, following the
// CS_<SERVICE>_<KEY> convention from Phase 1 SS5.
const envPrefix = "CS_TELEPHONY_GATEWAY"

// main is a thin wrapper around run so that deferred cleanup executes before
// the process exits. Because os.Exit skips deferred functions, calling it
// directly from a function that owns resources is a resource leak on every
// shutdown.
func main() {
	// `service version` prints the build metadata and exits, reading no
	// configuration. Every other invocation starts the service as before.
	if len(os.Args) == 2 && os.Args[1] == "version" {
		if err := writeVersion(os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		// Reported to stderr rather than through the logger because a failure
		// here may well BE a failure to construct the logger.
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}
}

// run boots the service and blocks until it shuts down.
//
// It returns an error rather than exiting so that main owns the exit decision
// and every deferred cleanup runs first.
func run() error {
	var cfg platform.ServiceConfig
	if err := platform.LoadConfig(&cfg, envPrefix); err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	// The service name is fixed by the binary, not by the environment. Allowing
	// it to be overridden would let a misconfigured deployment report itself as
	// a different service and corrupt every dashboard it appears in.
	cfg.Name = serviceName
	// The version is fixed by the binary too: it is the build's own metadata
	// (buildinfo.go), so what /readyz and every log line report is what was
	// built, not what an environment variable claims. CS_*_VERSION is ignored.
	build := buildInfo()
	cfg.Version = build.Version

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate configuration: %w", err)
	}

	// Per-process key for log pseudonymisation. Deployed environments supply a
	// stable key from the secret manager so pseudonyms correlate across
	// replicas; the random fallback confines correlation to one process, which
	// is the safe default rather than the useful one.
	hashKey := make([]byte, 32)
	if _, err := rand.Read(hashKey); err != nil {
		return fmt.Errorf("generate log hash key: %w", err)
	}

	logger := platform.NewLogger(cfg, os.Stdout, hashKey)
	platform.SetDefaultLogger(logger)
	logBuild(logger, build)

	health := platform.NewHealth(cfg)
	svc := platform.NewService(cfg, logger, health)

	// Runners are registered here in dependency order, lowest level first,
	// because shutdown runs in reverse.
	//
	// The single-active-process fence (P1-30B, decision D6) is the lowest
	// level: registered first, it shuts down last. It is a critical readiness
	// check — this instance is ready only while it owns the fence. Without the
	// fence configuration the service still starts and serves health, but is
	// never ready; a present-but-invalid configuration stops startup here.
	// Nothing else runs yet: no carrier adapter, no call path.
	if _, err := app.WireFence(svc, health, envPrefix, os.LookupEnv, logger); err != nil {
		return fmt.Errorf("configure fence: %w", err)
	}

	return svc.Run(context.Background())
}
