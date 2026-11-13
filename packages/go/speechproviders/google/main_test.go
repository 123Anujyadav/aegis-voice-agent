package google

import (
	"fmt"
	"os"
	"testing"
)

// TestMain makes it impossible for this package's tests to reach the real
// provider, whatever a test or a mutation does.
//
// The adapter addresses Google Speech-to-Text v2 (gRPC) and Google's token endpoint. Its transport honours the standard proxy
// variables, so every outbound request to a non-loopback host is routed to a
// proxy on a closed loopback port and fails locally. Go never proxies
// loopback addresses, so the local stand-ins are unaffected. The environment
// is set before any test runs, which is before it is first read (Go caches
// the first read).
//
// Why: on 2026-11-12 an identity test probably made one HTTPS request to an
// SMS provider with a fake key (services/go/identity/internal/bootstrap/
// main_test.go). No automated test may contact a provider;
// TestNoTestCanReachTheProvider pins this guard.
func TestMain(m *testing.M) {
	for _, v := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if err := os.Setenv(v, deadProxy); err != nil {
			fmt.Fprintln(os.Stderr, "google tests: cannot block outbound network:", err)
			os.Exit(2)
		}
	}
	for _, v := range []string{"NO_PROXY", "no_proxy"} {
		if err := os.Unsetenv(v); err != nil {
			fmt.Fprintln(os.Stderr, "google tests: cannot block outbound network:", err)
			os.Exit(2)
		}
	}
	os.Exit(m.Run())
}

// deadProxy is a loopback address on the discard port, where nothing listens.
const deadProxy = "http://127.0.0.1:9"
