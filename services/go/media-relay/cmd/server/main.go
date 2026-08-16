// Command server is the entrypoint for the media-relay service.
//
// RTP/SRTP to internal audio bus relay, jitter buffer, transcode and VAD frontend.
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
)

// serviceName identifies this service in configuration, logs, metrics and
// traces. It matches the directory name under services/go, which is what makes
// telemetry attributable without a lookup table.
const serviceName = "media-relay"

// envPrefix namespaces this service's environment variables, following the
// CS_<SERVICE>_<KEY> convention from Phase 1 SS5.
const envPrefix = "CS_MEDIA_RELAY"

// main is a thin wrapper around run so that deferred cleanup executes before
// the process exits. os.Exit skips deferred functions, so calling it directly
// from a function that owns resources is a resource leak on every shutdown.
func main() {
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

	health := platform.NewHealth(cfg)
	svc := platform.NewService(cfg, logger, health)

	// Runners are registered here in dependency order, lowest level first,
	// because shutdown runs in reverse. No runners exist at Phase 2; the
	// lifecycle serves health probes and waits for SIGTERM.

	return svc.Run(context.Background())
}