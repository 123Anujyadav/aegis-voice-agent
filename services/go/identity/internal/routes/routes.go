// Package routes names the identity service's HTTP endpoints. It holds only
// constants, so configuration validation (bootstrap) and the HTTP layer
// (httpapi) share one definition without either importing the other.
//
// The two proof-bearing endpoints are also the paths of the configured
// canonical URLs (CS_IDENTITY_ENROLLMENT_CANONICAL_URL and
// CS_IDENTITY_REFRESH_CANONICAL_URL): a DPoP proof names the canonical URL
// (htu), so a canonical URL whose path is not the path served would make every
// proof unusable. bootstrap refuses such a configuration.
package routes

const (
	// Method is the only method any identity endpoint accepts.
	Method = "POST"

	// OTPRequest starts sign-in for a phone number.
	OTPRequest = "/v1/otp/request"
	// Enrollment validates the code and enrols the device key in the DPoP
	// header, issuing the first access and refresh tokens.
	Enrollment = "/v1/otp/validate"
	// Refresh rotates a device-bound session's refresh token on a DPoP proof.
	Refresh = "/v1/token/refresh"
)
