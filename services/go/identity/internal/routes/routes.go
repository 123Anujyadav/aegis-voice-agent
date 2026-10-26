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
	// AccountDeletion erases the account that owns a device-bound session, on
	// the session's current refresh token and a DPoP proof (app/erasure.go).
	// Its canonical URL is the refresh canonical URL's origin and this path.
	AccountDeletion = "/v1/account/delete"
	// AccountDeletionCode starts a web account deletion: a code to the
	// number, if it has an account; the same answer either way
	// (app/web_erasure.go).
	AccountDeletionCode = "/v1/account/deletion/code"
	// AccountDeletionConfirm erases the account with that code.
	AccountDeletionConfirm = "/v1/account/deletion/confirm"
)
