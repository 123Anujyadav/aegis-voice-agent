package googleauth

import (
	"fmt"
	"os"
	"testing"
)

// TestMain makes it impossible for this package's tests to reach Google,
// whatever a test or a mutation does: the key fetch honours the standard proxy
// variables, so every outbound request to a non-loopback host is routed to a
// proxy on a closed loopback port and fails locally. The fake Google key
// server is on loopback, which Go never proxies. TestNoTestCanReachGoogle
// pins this guard.
func TestMain(m *testing.M) {
	for _, v := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if err := os.Setenv(v, deadProxy); err != nil {
			fmt.Fprintln(os.Stderr, "googleauth tests: cannot block outbound network:", err)
			os.Exit(2)
		}
	}
	for _, v := range []string{"NO_PROXY", "no_proxy"} {
		if err := os.Unsetenv(v); err != nil {
			fmt.Fprintln(os.Stderr, "googleauth tests: cannot block outbound network:", err)
			os.Exit(2)
		}
	}
	os.Exit(m.Run())
}

// deadProxy is a loopback address on the discard port, where nothing listens.
const deadProxy = "http://127.0.0.1:9"
