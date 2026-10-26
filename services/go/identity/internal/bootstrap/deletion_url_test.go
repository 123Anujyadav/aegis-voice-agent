package bootstrap

import "testing"

// Account deletion's canonical URL is the refresh canonical URL's origin with
// the deletion path, so it cannot be configured to a different host.
func TestAccountDeletionCanonicalURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://identity.example.test/v1/token/refresh":      "https://identity.example.test/v1/account/delete",
		"https://identity.example.test:8443/v1/token/refresh": "https://identity.example.test:8443/v1/account/delete",
	} {
		if got := AccountDeletionCanonicalURL(in); got != want {
			t.Errorf("AccountDeletionCanonicalURL(%q) = %q, want %q", in, got, want)
		}
	}
}
