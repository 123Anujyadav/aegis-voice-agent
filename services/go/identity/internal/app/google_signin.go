package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/dpop"
)

// # Sign in with Google (ADR-0035 D35.4, D35.8; G2)
//
// POST /v1/auth/google carries a Google ID token and a nonce seed, with a DPoP
// proof. The order is fixed, so that nothing is spent on a request that will
// be refused:
//
//  1. Google sign-in is configured, else ErrGoogleSignInUnavailable.
//  2. The nonce seed has its shape (ErrMalformedGoogleSignIn otherwise).
//  3. The DPoP proof verifies for this endpoint, spending its jti.
//  4. The ID token verifies (GoogleTokenVerifier: signature, iss, aud, azp,
//     exp and iat with skew, sub) and carries the nonce bound to the proof's
//     key and the seed (GoogleNonce).
//  5. The token is used once: its SHA-256 is reserved until the token's own
//     expiry (the record outlives it by the housekeeping margin).
//  6. THE M1 GATE, on every sign-in, existing accounts included: the token's
//     email_verified is true and its email is on the allowlist.
//  7. The account is found by the Google identity; if there is none, it is
//     created with one unit of the hour's creation budget in one atomic step
//     (existing accounts and refused creations spend nothing).
//  8. Play Integrity, then the session (createAccountSession).
//
// Every refusal in steps 4-7 is ErrGoogleSignInRefused: the same answer
// whatever the reason. Google not configured, or its keys unavailable, is
// ErrGoogleSignInUnavailable. No email, token, subject, nonce, seed or
// pseudonym is ever logged, audited or returned.
//
// Removing an address from the allowlist stops that account's NEXT Google
// sign-in; it does not revoke sessions already issued (they refresh until
// their 24-hour expiry).

// GoogleIssuer is the normalised issuer of Google ID tokens: both spellings
// Google uses are mapped to it before pseudonymisation.
const GoogleIssuer = "https://accounts.google.com"

var (
	// ErrGoogleSignInRefused is any refused Google sign-in: one answer for
	// every reason.
	ErrGoogleSignInRefused = errors.New("identity: Google sign-in refused")
	// ErrGoogleSignInUnavailable is Google sign-in not configured, or Google's
	// signing keys not available.
	ErrGoogleSignInUnavailable = errors.New("identity: Google sign-in is unavailable")
	// ErrMalformedGoogleSignIn is a request whose fields do not have their
	// shape: answered as malformed input, before anything is spent.
	ErrMalformedGoogleSignIn = errors.New("identity: malformed Google sign-in request")
)

// Audit events of Google sign-in. They carry no identifier.
const (
	EventAccountCreated      SecurityEventType = "identity.account.created"
	EventGoogleSignedIn      SecurityEventType = "identity.google.signed_in"
	EventGoogleSignInRefused SecurityEventType = "identity.auth.refused"
)

// GoogleIdentity is what a verified ID token says. Email is used only for the
// M1 gate and is never stored.
type GoogleIdentity struct {
	// Issuer is GoogleIssuer.
	Issuer string
	// Subject is the Google account's stable identifier.
	Subject       string
	Email         string
	EmailVerified bool
	// ExpiresAt is the token's own expiry.
	ExpiresAt time.Time
}

// GoogleTokenVerifier verifies a Google ID token and its nonce. Errors are
// ErrGoogleSignInRefused or ErrGoogleSignInUnavailable, never token text.
type GoogleTokenVerifier interface {
	VerifyGoogleIDToken(ctx context.Context, rawIDToken, expectedNonce string) (*GoogleIdentity, error)
}

// GoogleSignInConfig is everything Google sign-in needs.
type GoogleSignInConfig struct {
	Verifier      GoogleTokenVerifier
	ProofVerifier *dpop.Verifier
	Directory     *GoogleAccountDirectory
	Allowlist     *EmailAllowlist
	// HourlyCap is the most accounts Google sign-in creates per clock hour.
	HourlyCap int
}

// MaxAccountCreationHourlyCap is the largest accepted hourly cap.
const MaxAccountCreationHourlyCap = 10000

// googleNonceDomain separates the nonce from every other hash.
const googleNonceDomain = "lily.identity.google-nonce.v1\x00"

var nonceSeedPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// ValidNonceSeed reports whether seed is 32 bytes as unpadded base64url.
func ValidNonceSeed(seed string) bool { return nonceSeedPattern.MatchString(seed) }

// GoogleNonce is the nonce a client must request from Google for a sign-in
// whose DPoP proof is made with the key of thumbprint jkt:
// base64url_nopad(SHA-256("lily.identity.google-nonce.v1" || 0x00 || jkt ||
// 0x00 || seed)). Both inputs are base64url, so neither contains the NUL
// separator.
func GoogleNonce(jkt, seed string) string {
	h := sha256.New()
	h.Write([]byte(googleNonceDomain))
	h.Write([]byte(jkt))
	h.Write([]byte{0})
	h.Write([]byte(seed))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// SetGoogleSignIn configures Google sign-in. Every part is required and the
// cap must be within [1, MaxAccountCreationHourlyCap]; the repository must
// store accounts. Construction-time configuration: not safe to change while
// serving.
func (s *IdentityService) SetGoogleSignIn(cfg GoogleSignInConfig) error {
	if cfg.Verifier == nil || cfg.ProofVerifier == nil || cfg.Directory == nil || cfg.Allowlist == nil {
		return fmt.Errorf("%w: Google sign-in needs a token verifier, a proof verifier, a directory and an allowlist", ErrAccountsUnavailable)
	}
	if cfg.HourlyCap < 1 || cfg.HourlyCap > MaxAccountCreationHourlyCap {
		return fmt.Errorf("%w: the hourly account-creation cap must be between 1 and %d", ErrAccountsUnavailable, MaxAccountCreationHourlyCap)
	}
	if _, err := s.accounts(); err != nil {
		return err
	}
	c := cfg
	s.googleSignIn = &c
	return nil
}

// GoogleSignInEnabled reports whether Google sign-in is configured.
func (s *IdentityService) GoogleSignInEnabled() bool { return s.googleSignIn != nil }

// SignInWithGoogle signs in, or signs up, with a Google ID token: see the
// order above. It returns the session (with its refresh token) and the access
// token, exactly as SMS sign-in does.
func (s *IdentityService) SignInWithGoogle(ctx context.Context, idToken, nonceSeed, method, proof, integrityToken string) (*UserSession, string, error) {
	cfg := s.googleSignIn
	if cfg == nil {
		return nil, "", ErrGoogleSignInUnavailable
	}
	if !ValidNonceSeed(nonceSeed) || idToken == "" {
		return nil, "", ErrMalformedGoogleSignIn
	}
	began := time.Now()
	jkt, identity, err := s.verifyGoogleCredential(ctx, cfg, idToken, nonceSeed, method, proof)
	if err != nil {
		return nil, "", err
	}
	accountID, err := s.googleAccount(ctx, cfg, identity)
	if err != nil {
		return nil, "", err
	}
	session, token, err := s.createAccountSession(ctx, accountID, jkt, began, func(ctx context.Context) (SessionIntegrityTier, error) {
		return s.assessIntegrity(ctx, integrityToken, proof, jkt, SecurityEvent{})
	})
	if err != nil {
		return nil, "", err
	}
	s.audit(SecurityEvent{Type: EventGoogleSignedIn, At: s.now(), SessionID: session.SessionID})
	return session, token, nil
}

// verifyGoogleCredential is steps 3-6: the proof, the token bound to the
// proof's key, single use, and the M1 gate. It returns the proof's key
// thumbprint and the verified identity.
func (s *IdentityService) verifyGoogleCredential(ctx context.Context, cfg *GoogleSignInConfig, idToken, nonceSeed, method, proof string) (string, *GoogleIdentity, error) {
	verified, err := cfg.ProofVerifier.Verify(ctx, proof, method)
	switch {
	case errors.Is(err, dpop.ErrInvalidProof):
		return "", nil, fmt.Errorf("%w: %w", ErrInvalidDeviceProof, err)
	case err != nil:
		return "", nil, err
	}
	identity, err := cfg.Verifier.VerifyGoogleIDToken(ctx, idToken, GoogleNonce(verified.JKT, nonceSeed))
	switch {
	case errors.Is(err, ErrGoogleSignInUnavailable):
		return "", nil, ErrGoogleSignInUnavailable
	case err != nil:
		return "", nil, s.refuseGoogleSignIn()
	}
	accounts, err := s.accounts()
	if err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256([]byte(idToken))
	first, err := accounts.ReserveGoogleToken(ctx, hex.EncodeToString(digest[:]), identity.ExpiresAt)
	if err != nil {
		return "", nil, err
	}
	if !first || !identity.EmailVerified || !cfg.Allowlist.Allows(identity.Email) {
		return "", nil, s.refuseGoogleSignIn()
	}
	return verified.JKT, identity, nil
}

// googleAccount is step 7: the identity's account, created within the hour's
// budget when there is none.
func (s *IdentityService) googleAccount(ctx context.Context, cfg *GoogleSignInConfig, identity *GoogleIdentity) (string, error) {
	accountID, found, err := cfg.Directory.Lookup(ctx, identity.Issuer, identity.Subject)
	if err != nil || found {
		return accountID, err
	}
	accountID, created, err := cfg.Directory.CreateWithinBudget(ctx, identity.Issuer, identity.Subject,
		s.now().UTC().Truncate(time.Hour), cfg.HourlyCap)
	switch {
	case errors.Is(err, ErrAccountCreationCapReached):
		return "", s.refuseGoogleSignIn()
	case err != nil:
		return "", err
	}
	if created {
		s.audit(SecurityEvent{Type: EventAccountCreated, At: s.now()})
	}
	return accountID, nil
}

// refuseGoogleSignIn audits a refusal (with no identifier) and returns the
// one refusal error.
func (s *IdentityService) refuseGoogleSignIn() error {
	s.audit(SecurityEvent{Type: EventGoogleSignInRefused, At: s.now()})
	return ErrGoogleSignInRefused
}
