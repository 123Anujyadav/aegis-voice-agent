package main

import (
	"strings"
	"testing"
)

// TestServiceNameMatchesDirectory pins the service identity.
//
// Telemetry attribution depends on serviceName matching the directory under
// services/go. A mismatch silently splits this service's dashboards, alerts and
// error budget in two, and the split is invisible until someone notices the
// numbers do not add up.
func TestServiceNameMatchesDirectory(t *testing.T) {
	t.Parallel()

	const want = "identity"
	if serviceName != want {
		t.Errorf("serviceName = %q, want %q", serviceName, want)
	}
}

// TestEnvPrefixFollowsConvention verifies the CS_<SERVICE> namespace from
// Phase 1 SS5. A wrong prefix means the service silently ignores its own
// configuration and runs entirely on defaults.
func TestEnvPrefixFollowsConvention(t *testing.T) {
	t.Parallel()

	if !strings.HasPrefix(envPrefix, "CS_") {
		t.Errorf("envPrefix = %q, must start with CS_", envPrefix)
	}
	if envPrefix != strings.ToUpper(envPrefix) {
		t.Errorf("envPrefix = %q, must be upper case", envPrefix)
	}
	if strings.Contains(envPrefix, "-") {
		t.Errorf("envPrefix = %q, must use underscores rather than hyphens", envPrefix)
	}

	want := "CS_" + strings.ToUpper(strings.ReplaceAll(serviceName, "-", "_"))
	if envPrefix != want {
		t.Errorf("envPrefix = %q, want %q derived from serviceName", envPrefix, want)
	}
}