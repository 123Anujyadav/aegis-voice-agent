// Package googleauth verifies Google ID tokens for "Sign in with Google"
// (ADR-0035 D35.4; ADR-0036: go-oidc with go-jose, contained to this package).
//
// What is checked, all of it, failing closed:
//
//   - the signature, RS256 only, against Google's published keys (keyset.go:
//     a bounded cache that honours Cache-Control);
//   - iss is exactly "https://accounts.google.com" or "accounts.google.com";
//   - aud contains the configured server (web) client id;
//   - azp, when present, is one of the configured Android client ids or the
//     server client id (Google does not document azp for Android, so it is
//     checked when present and counted by kind; aud is the binding);
//   - exp is after now minus MaxClockSkew; iat is not after now plus
//     MaxClockSkew; exp is after iat; and the token lives at most
//     MaxTokenLifetime;
//   - sub is present, printable ASCII, at most 255 bytes;
//   - nonce equals the expected nonce (constant time);
//   - email_verified is the JSON boolean true for EmailVerified to be true;
//     anything else (missing, false, null, a string) is false.
//
// Errors are app.ErrGoogleSignInRefused or app.ErrGoogleSignInUnavailable
// (no usable key), never token text. Nothing here logs.
package googleauth

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// Google's endpoints and accepted issuers.
const (
	// KeysURL is Google's JWK set for ID tokens.
	KeysURL   = "https://www.googleapis.com/oauth2/v3/certs"
	keysHost  = "www.googleapis.com"
	issuerAlt = "accounts.google.com"
)

// Bounds of the claim checks.
const (
	// MaxClockSkew is the allowance on exp and iat. The single-use record of a
	// token outlives its exp by a much larger margin (one hour), so a token
	// accepted at the edge of its expiry is still recorded.
	MaxClockSkew = 60 * time.Second
	// MaxTokenLifetime bounds exp - iat. Google's tokens last about an hour.
	MaxTokenLifetime = 2 * time.Hour
	// MaxIDTokenBytes bounds a token before it is parsed.
	MaxIDTokenBytes = 6 << 10
	maxSubjectBytes = 255
)

// AuthorizedPartyKind is how a verified token's azp matched.
type AuthorizedPartyKind string

// Authorized-party kinds.
const (
	AuthorizedPartyAndroid AuthorizedPartyKind = "android"
	AuthorizedPartyServer  AuthorizedPartyKind = "server"
	AuthorizedPartyAbsent  AuthorizedPartyKind = "absent"
)

// Config configures a Verifier.
type Config struct {
	// ServerClientID is the web client id the app requests tokens for: the
	// required audience.
	ServerClientID string
	// AndroidClientIDs are the Android client ids azp may name.
	AndroidClientIDs []string
	// ObserveAuthorizedParty, if set, is told the kind of every accepted
	// token's azp (a fixed label, never the id). It must not block.
	ObserveAuthorizedParty func(AuthorizedPartyKind)
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// KeysURL overrides the key set's address; tests only (loopback over
	// TLS). Empty means Google's.
	KeysURL string
	// HTTPClient overrides the key fetch client; tests only.
	HTTPClient *http.Client
}

// Verifier verifies Google ID tokens. Safe for concurrent use.
type Verifier struct {
	serverID string
	android  map[string]struct{}
	observe  func(AuthorizedPartyKind)
	now      func() time.Time
	keys     *keySet
}

// ErrInvalidConfig is a Verifier configuration New refuses.
var ErrInvalidConfig = errors.New("googleauth: invalid configuration")

// New builds a Verifier. It contacts nothing.
func New(cfg Config) (*Verifier, error) {
	if !ValidClientID(cfg.ServerClientID) {
		return nil, fmt.Errorf("%w: the server client id must be a Google OAuth client id", ErrInvalidConfig)
	}
	if len(cfg.AndroidClientIDs) == 0 {
		return nil, fmt.Errorf("%w: at least one Android client id is required", ErrInvalidConfig)
	}
	android := map[string]struct{}{}
	for _, id := range cfg.AndroidClientIDs {
		if !ValidClientID(id) {
			return nil, fmt.Errorf("%w: an Android client id is not a Google OAuth client id", ErrInvalidConfig)
		}
		android[id] = struct{}{}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	keysURL, err := keysEndpoint(cfg.KeysURL)
	if err != nil {
		return nil, err
	}
	return &Verifier{
		serverID: cfg.ServerClientID, android: android, observe: cfg.ObserveAuthorizedParty, now: now,
		keys: &keySet{url: keysURL, client: guardedClient(cfg.HTTPClient), now: now},
	}, nil
}

// ValidClientID reports whether id has the shape of a Google OAuth client id.
func ValidClientID(id string) bool {
	prefix, ok := strings.CutSuffix(id, ".apps.googleusercontent.com")
	if !ok || prefix == "" || len(prefix) > 128 {
		return false
	}
	for _, c := range prefix {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// keysEndpoint is Google's key URL, or a test override on loopback over TLS.
func keysEndpoint(override string) (string, error) {
	if override == "" {
		return KeysURL, nil
	}
	u, err := url.Parse(override)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return "", fmt.Errorf("%w: the key URL must be https", ErrInvalidConfig)
	}
	switch u.Hostname() {
	case keysHost, "localhost", "127.0.0.1", "::1":
		return override, nil
	}
	return "", fmt.Errorf("%w: keys are fetched only from %s", ErrInvalidConfig, keysHost)
}

// guardedClient never follows a redirect and bounds every fetch.
func guardedClient(supplied *http.Client) *http.Client {
	c := http.Client{Timeout: FetchTimeout, Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: FetchTimeout,
		ResponseHeaderTimeout: FetchTimeout, MaxIdleConnsPerHost: 2,
	}}
	if supplied != nil {
		c = *supplied
		if c.Timeout == 0 || c.Timeout > FetchTimeout {
			c.Timeout = FetchTimeout
		}
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("googleauth: refusing to follow a redirect")
	}
	return &c
}

// preverified is an oidc.KeySet answering only for the one token whose
// signature this package already verified, so go-oidc's claim checks run on
// exactly that token.
type preverified struct {
	raw     string
	payload []byte
}

// VerifySignature implements oidc.KeySet for the one verified token.
func (p preverified) VerifySignature(_ context.Context, jwt string) ([]byte, error) {
	if jwt != p.raw {
		return nil, errors.New("googleauth: not the verified token")
	}
	return p.payload, nil
}

// googleClaims are the claims go-oidc does not expose.
type googleClaims struct {
	AuthorizedParty string          `json:"azp"`
	Email           string          `json:"email"`
	EmailVerified   json.RawMessage `json:"email_verified"`
}

// VerifyGoogleIDToken implements app.GoogleTokenVerifier.
func (v *Verifier) VerifyGoogleIDToken(ctx context.Context, raw, expectedNonce string) (*app.GoogleIdentity, error) {
	if raw == "" || len(raw) > MaxIDTokenBytes {
		return nil, app.ErrGoogleSignInRefused
	}
	payload, err := v.verifySignature(ctx, raw)
	if err != nil {
		return nil, err
	}
	tok, err := oidc.NewVerifier(app.GoogleIssuer, preverified{raw: raw, payload: payload}, &oidc.Config{
		ClientID:             v.serverID,
		SupportedSigningAlgs: []string{oidc.RS256},
		// Issuer and time are checked below, explicitly: both spellings of
		// the issuer, and the skew on exp and iat.
		SkipIssuerCheck: true,
		SkipExpiryCheck: true,
		Now:             v.now,
	}).Verify(ctx, raw)
	if err != nil {
		return nil, app.ErrGoogleSignInRefused
	}
	var extra googleClaims
	if err := tok.Claims(&extra); err != nil {
		return nil, app.ErrGoogleSignInRefused
	}
	kind, ok := v.authorizedParty(extra.AuthorizedParty)
	if !ok || !v.claimsAcceptable(tok, expectedNonce) {
		return nil, app.ErrGoogleSignInRefused
	}
	if v.observe != nil {
		v.observe(kind)
	}
	return &app.GoogleIdentity{
		Issuer:        app.GoogleIssuer,
		Subject:       tok.Subject,
		Email:         extra.Email,
		EmailVerified: bytes.Equal(bytes.TrimSpace(extra.EmailVerified), []byte("true")),
		ExpiresAt:     tok.Expiry,
	}, nil
}

// verifySignature checks the compact JWS against Google's keys, RS256 only.
func (v *Verifier) verifySignature(ctx context.Context, raw string) ([]byte, error) {
	jws, err := jose.ParseSignedCompact(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil || len(jws.Signatures) != 1 {
		return nil, app.ErrGoogleSignInRefused
	}
	key, err := v.keys.lookup(ctx, jws.Signatures[0].Header.KeyID)
	switch {
	case errors.Is(err, errUnavailable):
		return nil, app.ErrGoogleSignInUnavailable
	case err != nil:
		return nil, app.ErrGoogleSignInRefused
	}
	payload, err := jws.Verify(key)
	if err != nil {
		return nil, app.ErrGoogleSignInRefused
	}
	return payload, nil
}

// authorizedParty classifies azp: absent, the server client, or an Android
// client; anything else is refused.
func (v *Verifier) authorizedParty(azp string) (AuthorizedPartyKind, bool) {
	switch {
	case azp == "":
		return AuthorizedPartyAbsent, true
	case azp == v.serverID:
		return AuthorizedPartyServer, true
	}
	if _, ok := v.android[azp]; ok {
		return AuthorizedPartyAndroid, true
	}
	return "", false
}

// claimsAcceptable checks iss, the times, sub and the nonce.
func (v *Verifier) claimsAcceptable(tok *oidc.IDToken, expectedNonce string) bool {
	if !slices.Contains([]string{app.GoogleIssuer, issuerAlt}, tok.Issuer) {
		return false
	}
	now := v.now()
	switch {
	case tok.Expiry.IsZero() || tok.IssuedAt.IsZero():
		return false
	case !tok.Expiry.After(now.Add(-MaxClockSkew)):
		return false
	case tok.IssuedAt.After(now.Add(MaxClockSkew)):
		return false
	case !tok.Expiry.After(tok.IssuedAt) || tok.Expiry.Sub(tok.IssuedAt) > MaxTokenLifetime:
		return false
	}
	if !validSubject(tok.Subject) || expectedNonce == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(tok.Nonce), []byte(expectedNonce)) == 1
}

func validSubject(sub string) bool {
	if sub == "" || len(sub) > maxSubjectBytes {
		return false
	}
	for i := 0; i < len(sub); i++ {
		if c := sub[i]; c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

var _ app.GoogleTokenVerifier = (*Verifier)(nil)
