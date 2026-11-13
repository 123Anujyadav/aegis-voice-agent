package anthropic

import (
	"net/http"
	"testing"
)

// TestNoTestCanReachTheProvider pins TestMain's guard without making a
// request: it asks the proxy function where a request to the provider would
// go, and requires the dead loopback proxy.
func TestNoTestCanReachTheProvider(t *testing.T) {
	for _, host := range []string{"api.anthropic.com"} {
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
