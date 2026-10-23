package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// edge-api cannot start without its authentication configuration and
// database; with neither, normal startup must get PAST the build metadata and
// stop where it always does - at authentication - rather than on the metadata.
func startupEnv() []string {
	return []string{"PATH=" + os.Getenv("PATH"), "SYSTEMROOT=" + os.Getenv("SYSTEMROOT")}
}

func checkStartup(t *testing.T, _ int, logs *lockedBuffer) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(logs.String(), "FATAL") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	out := logs.String()
	if !strings.Contains(out, "FATAL: authentication: edge-api: protected routes are registered but authentication is not configured") ||
		strings.Contains(out, "panic") {
		t.Fatalf("startup did not reach the authentication stage:\n%s", out)
	}
	if !strings.Contains(out, `"version":"dev"`) {
		t.Errorf("the log lines do not carry the honest default version:\n%s", out)
	}
	wantBuildLogs(t, out)
}
