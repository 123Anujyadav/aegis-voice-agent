package googleauth

import (
	"net/http"
	"testing"
)

// TestNoTestCanReachGoogle pins TestMain's guard without making a request.
func TestNoTestCanReachGoogle(t *testing.T) {
	for _, host := range []string{keysHost, "accounts.google.com", "oauth2.googleapis.com"} {
		req, err := http.NewRequest(http.MethodGet, "https://"+host+"/", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		proxy, err := http.ProxyFromEnvironment(req)
		if err != nil {
			t.Fatal(err)
		}
		if proxy == nil || proxy.String() != deadProxy {
			t.Fatalf("a request to %s would go to %v, not the dead loopback proxy", host, proxy)
		}
	}
}
