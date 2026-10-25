package playintegrity

import "encoding/json"

// impersonates reports whether a federation configuration names a service
// account to impersonate. Google's loader accepts a configuration without one
// (direct resource access); this design requires impersonation (decision memo
// §7), so a configuration without it is refused rather than silently used.
func impersonates(credentialConfig []byte) bool {
	var c struct {
		ServiceAccountImpersonationURL string `json:"service_account_impersonation_url"`
	}
	return json.Unmarshal(credentialConfig, &c) == nil && c.ServiceAccountImpersonationURL != ""
}
