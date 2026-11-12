package bootstrap

import (
	"net/http"
	"testing"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/otpdelivery"
)

// TestNoTestCanReachAProvider pins TestMain's guard without making a request:
// it asks the transport's proxy function where a request to each provider
// host would go, and requires the dead loopback proxy. If the guard were ever
// removed, this test fails offline instead of a test reaching a provider.
func TestNoTestCanReachAProvider(t *testing.T) {
	for _, host := range []string{otpdelivery.MSG91APIHost, otpdelivery.TwilioAPIHost} {
		req, err := http.NewRequest(http.MethodPost, "https://"+host+"/", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		proxy, err := http.ProxyFromEnvironment(req)
		if err != nil {
			t.Fatal(err)
		}
		if proxy == nil || proxy.String() != deadProxy {
			t.Fatalf("a request to %s would go to %v, not the dead loopback proxy: a test could reach the provider", host, proxy)
		}
	}
}
