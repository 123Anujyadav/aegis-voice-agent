package playintegrity

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Scope is the OAuth scope the Play Integrity API requires.
const Scope = "https://www.googleapis.com/auth/playintegrity"

// ErrInvalidCredentialConfig is a credential configuration this package will
// not use: not valid JSON, not a workload identity federation configuration,
// or one without service-account impersonation.
var ErrInvalidCredentialConfig = errors.New("playintegrity: invalid credential configuration")

// ErrAccessToken is a failure to obtain a Google access token. It carries no
// detail from Google's response, which the decoder treats as unavailability:
// the session is unverified, never refused.
var ErrAccessToken = errors.New("playintegrity: obtaining a Google access token failed")

// WorkloadIdentityTokens is a TokenSource backed by Workload Identity
// Federation: a short-lived token from the workload's own identity (on EKS, a
// projected Kubernetes service-account token) is exchanged at Google's STS
// for a federated token, which impersonates a dedicated service account in
// the Play-linked project. No private key exists anywhere.
//
// It accepts only an external_account (federation) configuration, so a
// service-account key file is refused: ADR-0008 allows no static cloud
// credentials. See docs/phase19/PLAY-INTEGRITY-DECISION-MEMO.md §7.
//
// DORMANT: nothing in cmd/server constructs this until ADR-0031 is ratified
// and the setup in docs/phase19/PLAY-INTEGRITY-IMPLEMENTATION-HANDOFF.md
// exists.
type WorkloadIdentityTokens struct {
	ts oauth2.TokenSource
}

// NewWorkloadIdentityTokens builds the token source from Google's credential
// configuration file (gcloud iam workload-identity-pools create-cred-config),
// which holds no secret. The context must outlive the token source: it
// carries the HTTP client used for every later token exchange. A nil client
// uses net/http's default.
func NewWorkloadIdentityTokens(ctx context.Context, credentialConfig []byte, client *http.Client) (*WorkloadIdentityTokens, error) {
	if client != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, client)
	}
	// The typed loader refuses any other credential type, including a
	// service_account key; CredentialsFromJSON is deprecated for exactly the
	// risk of loading a credential of an unexpected type.
	creds, err := google.CredentialsFromJSONWithTypeAndParams(ctx, credentialConfig, google.ExternalAccount,
		google.CredentialsParams{Scopes: []string{Scope}})
	if err != nil {
		// The library's error can quote the configuration; it is not secret,
		// but it is not repeated either.
		return nil, fmt.Errorf("%w: not a workload identity federation (external_account) configuration Google accepts", ErrInvalidCredentialConfig)
	}
	if !impersonates(credentialConfig) {
		return nil, fmt.Errorf("%w: service_account_impersonation_url is required (the federated identity impersonates a dedicated service account)", ErrInvalidCredentialConfig)
	}
	return &WorkloadIdentityTokens{ts: creds.TokenSource}, nil
}

// AccessToken returns a current access token, refreshed by the underlying
// source when it nears expiry. The context bounds the wait; the exchange
// itself is bounded by the HTTP client's own timeout.
func (w *WorkloadIdentityTokens) AccessToken(ctx context.Context) (string, error) {
	type result struct {
		token *oauth2.Token
		err   error
	}
	done := make(chan result, 1)
	go func() {
		t, err := w.ts.Token()
		done <- result{t, err}
	}()
	select {
	case <-ctx.Done():
		return "", fmt.Errorf("%w: %w", ErrAccessToken, ctx.Err())
	case r := <-done:
		if r.err != nil || r.token == nil || r.token.AccessToken == "" {
			return "", ErrAccessToken
		}
		return r.token.AccessToken, nil
	}
}
