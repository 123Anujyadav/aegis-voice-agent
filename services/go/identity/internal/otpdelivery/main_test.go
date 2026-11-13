package otpdelivery

import (
	"fmt"
	"os"
	"testing"
)

// TestMain makes it impossible for this package's tests to reach a real SMS
// provider, whatever a test or a mutation does.
//
// This package holds the real MSG91 and Twilio adapters, which address
// control.msg91.com and api.twilio.com. Their transport honours the standard
// proxy variables (http.ProxyFromEnvironment), so every outbound HTTPS request
// is routed to a proxy on a closed loopback port and fails locally. Go never
// proxies loopback addresses, so local fakes and the throwaway PostgreSQL are
// unaffected. The environment is set before any test runs, which is before
// ProxyFromEnvironment reads it (it caches the first read).
//
// Why: on 2026-11-12 a bootstrap test probably made one HTTPS request to
// control.msg91.com with a fake key (internal/bootstrap/main_test.go). No
// automated test may contact a provider; TestNoTestCanReachAProvider pins
// this guard.
func TestMain(m *testing.M) {
	for _, v := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if err := os.Setenv(v, deadProxy); err != nil {
			fmt.Fprintln(os.Stderr, "otpdelivery tests: cannot block outbound network:", err)
			os.Exit(2)
		}
	}
	for _, v := range []string{"NO_PROXY", "no_proxy"} {
		if err := os.Unsetenv(v); err != nil {
			fmt.Fprintln(os.Stderr, "otpdelivery tests: cannot block outbound network:", err)
			os.Exit(2)
		}
	}
	os.Exit(m.Run())
}

// deadProxy is a loopback address on the discard port, where nothing listens.
const deadProxy = "http://127.0.0.1:9"
