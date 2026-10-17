package main

import (
	"log/slog"
	"strings"
	"testing"
)

// Phase 14 T13 — run() COVERAGE.
//
// T2–T12 exercise buildService, the same function run() calls, so run() itself
// stayed at 0% and held services/go/voice at 41.7% — below hardening's 60%
// floor. These tests drive run() through its REAL inputs: the CS_VOICE_*
// environment and, in run_unix_test.go, SIGTERM. They need no change to
// production code.
//
// None of them is parallel. t.Setenv changes the whole process's environment,
// and run() replaces the default logger.

// keepDefaultLogger restores the process-wide default logger after a test,
// because run() installs its own through platform.SetDefaultLogger.
func keepDefaultLogger(t *testing.T) {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
}

// TestT13_RunFailsWhenConfigurationCannotBeLoaded: a value that cannot be
// parsed into its field stops the service before anything is constructed, and
// the error says which stage failed.
func TestT13_RunFailsWhenConfigurationCannotBeLoaded(t *testing.T) {
	keepDefaultLogger(t)
	t.Setenv("CS_VOICE_HEALTH_PORT", "not-a-port")

	err := run()
	if err == nil || !strings.Contains(err.Error(), "load configuration") {
		t.Fatalf("run() = %v, want a load-configuration error", err)
	}
}

// TestT13_RunFailsWhenConfigurationIsInvalid: configuration that loads but
// violates ServiceConfig's rules is refused before the logger, the health
// endpoint or the intelligence path are built.
func TestT13_RunFailsWhenConfigurationIsInvalid(t *testing.T) {
	keepDefaultLogger(t)
	t.Setenv("CS_VOICE_ENVIRONMENT", "nowhere")

	err := run()
	if err == nil || !strings.Contains(err.Error(), "validate configuration") {
		t.Fatalf("run() = %v, want a validate-configuration error", err)
	}
}
