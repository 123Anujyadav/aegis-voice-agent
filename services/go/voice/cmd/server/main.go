// Command server is the entrypoint for the voice service.
//
// PHASE 14 SCOPE. This wires configuration, logging, health, the lifecycle —
// and the deterministic voice intelligence path:
//
//	service run()
//	  -> voiceintel.Bridge
//	    -> conversation.Engine (constructed WITH the real classifier)
//	      -> intent.Classifier
//
// It does NOT construct a voice.Pipeline. Every shipped provider (ollama,
// piper, process, whispercli, whispercpp) shells out to an external binary or
// model, and this phase remains no-model/no-network, so the audio-bearing
// stages are deferred. See docs/adr/0017-service-wiring.md.
//
// Before this, conversation.NewEngine existed in production in exactly one
// place — voiceintel's own bridge — reachable from no service. This file is
// what makes it reachable.
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"os"

	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/packages/go/voiceintel"
)

// serviceName identifies this service in configuration, logs, metrics and
// traces. It matches the directory name under services/go, which is what makes
// telemetry attributable without a lookup table.
const serviceName = "voice"

// envPrefix namespaces this service's environment variables, following the
// CS_<SERVICE>_<KEY> convention from Phase 1 SS5.
const envPrefix = "CS_VOICE"

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
func run() error {
	var cfg platform.ServiceConfig
	if err := platform.LoadConfig(&cfg, envPrefix); err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	// The service name is fixed by the binary, not by the environment.
	cfg.Name = serviceName

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate configuration: %w", err)
	}

	hashKey := make([]byte, 32)
	if _, err := rand.Read(hashKey); err != nil {
		return fmt.Errorf("generate log hash key: %w", err)
	}

	logger := platform.NewLogger(cfg, os.Stdout, hashKey)
	platform.SetDefaultLogger(logger)

	health := platform.NewHealth(cfg)

	svc, _, err := buildService(cfg, logger, health)
	if err != nil {
		return err
	}

	return svc.Run(context.Background())
}

// buildService assembles the service and its registered components.
//
// Separated from run so that a test can exercise THIS path rather than
// instantiating the bridge itself. It returns the intelligence runner as well
// as the service so a caller can assert on what was wired; run ignores it,
// because nothing in the request path reaches for it through a global.
func buildService(
	cfg platform.ServiceConfig,
	logger *slog.Logger,
	health *platform.Health,
) (*platform.Service, *voiceIntelligence, error) {
	svc := platform.NewService(cfg, logger, health)

	intelligence, err := newVoiceIntelligence(logger)
	if err != nil {
		// Fail startup. A voice service that cannot build its classifier must
		// not come up pretending to have one — every utterance would silently
		// resolve to the fallback intent, which is precisely the pre-Phase-13
		// behaviour this wiring exists to end.
		return nil, nil, fmt.Errorf("construct voice intelligence: %w", err)
	}
	svc.Register(intelligence)

	return svc, intelligence, nil
}

// voiceIntelligence owns the deterministic intelligence bridge for the process.
//
// It exists to give the bridge a lifecycle under platform.Runner. The bridge is
// held by this struct and injected at construction — there is deliberately no
// package-level bridge, no session registry and no init(), so the only way to
// reach a conversation is through the instance the service registered.
type voiceIntelligence struct {
	bridge *voiceintel.Bridge
	log    *slog.Logger
}

// Compile-time proof that this satisfies the lifecycle contract the service
// expects. voiceintel.Bridge deliberately does NOT implement Runner itself:
// lifecycle is the service's concern, not the bridge's, and keeping them apart
// is what stops the bridge growing process-level responsibilities.
var _ platform.Runner = (*voiceIntelligence)(nil)

// newVoiceIntelligence constructs the bridge with the real deterministic
// classifier.
//
// voiceintel.New with no options builds intent.New(intent.DefaultConfig()) and
// passes it to conversation.NewEngine through WithClassifier. No test double,
// no nil classifier, no fallback-only engine.
func newVoiceIntelligence(log *slog.Logger) (*voiceIntelligence, error) {
	bridge, err := voiceintel.New()
	if err != nil {
		return nil, err
	}
	return &voiceIntelligence{bridge: bridge, log: log}, nil
}

// Name identifies the component in lifecycle logs.
func (v *voiceIntelligence) Name() string { return "voice-intelligence" }

// Run blocks until the service's context is cancelled.
//
// The bridge owns no goroutine and no listener: it is a constructor for
// per-session planners, so there is no loop to run. Returning early would be
// wrong rather than efficient — platform.Service treats any runner exit as a
// reason to shut the process down, logging "runner exited unexpectedly without
// error". Blocking here is what makes the component's lifetime the service's
// lifetime.
func (v *voiceIntelligence) Run(ctx context.Context) error {
	v.log.Info("voice intelligence ready",
		slog.String("component", v.Name()))
	<-ctx.Done()
	return nil
}

// Shutdown releases the component.
//
// There is nothing to close. Per-session conversations are owned by the
// frozen conversation.Engine, which removes each one from its active map when
// it reaches a terminal state; they die with the engine, which dies with this
// struct. Inventing a cleanup policy here would be inventing one for frozen
// code, and calling a close that does not exist is how double-close bugs start.
func (v *voiceIntelligence) Shutdown(_ context.Context) error {
	v.log.Info("voice intelligence stopped",
		slog.String("component", v.Name()))
	return nil
}

// Bridge exposes the wired bridge to whatever drives calls.
//
// Session identity stays conversation.ConversationID, supplied by the caller to
// Bridge.Planner(id, persona); this service stores no conversation state and
// keeps no session map of its own.
func (v *voiceIntelligence) Bridge() *voiceintel.Bridge { return v.bridge }
