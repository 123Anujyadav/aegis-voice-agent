package otpdelivery

import (
	"net/http"
	"testing"
)

// TestNoTestCanReachAProvider pins TestMain's guard without making a request:
// it asks the adapters' proxy function where a request to each provider host
// would go, and requires the dead loopback proxy.
func TestNoTestCanReachAProvider(t *testing.T) {
	for _, host := range []string{MSG91APIHost, TwilioAPIHost} {
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
