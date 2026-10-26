// Package bootstrap loads the identity service's security configuration and
// builds an IdentityService from it, failing closed on anything missing or
// invalid.
//
// It constructs; it does not serve. Nothing here registers an HTTP route,
// mounts middleware or starts a listener. cmd/server calls Open (runtime.go),
// which validates the activation configuration, opens PostgreSQL and then
// calls Build; cmd/server serves the result.
//
// # Configuration contract
//
// Every variable is required and has no default. A value that is unset, empty,
// whitespace-only or padded with whitespace is refused.
//
//	CS_IDENTITY_ACCESS_TOKEN_PRIVATE_KEY_FILE  path to an Ed25519 PKCS#8 PEM
//	                                           "PRIVATE KEY" file. The key itself
//	                                           is never accepted from the
//	                                           environment.
//	CS_IDENTITY_ACCESS_TOKEN_ISSUER            iss of issued access tokens
//	CS_IDENTITY_ACCESS_TOKEN_AUDIENCE          aud of issued access tokens
//	CS_IDENTITY_ACCESS_TOKEN_KID               kid of the signing key
//	CS_IDENTITY_ENROLLMENT_CANONICAL_URL       htu an enrolment proof must name
//	CS_IDENTITY_REFRESH_CANONICAL_URL          htu a refresh proof must name
//
// Issuer, audience and key id are validated by accesstoken.NewSigner; the URLs
// by dpop.NewVerifier (absolute http(s), no query, fragment or user info, and
// canonical: the URL must re-serialise to exactly itself). The two URLs must
// differ: a proof names one URL, and the same URL for both endpoints would make
// every enrolment proof acceptable for refresh and vice versa.
//
// Each URL's path must also be exactly the path the HTTP API serves for that
// endpoint (internal/routes: /v1/otp/validate and /v1/token/refresh). Scheme
// and host are the deployment's; a proxy that rewrites the path is not
// supported, since the client signs the URL it sees.
//
// No production value for any of these exists in this repository, and none is
// defaulted here.
package bootstrap

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

// EnvPrefix is the identity service's environment namespace, the same one
// cmd/server loads platform.ServiceConfig under.
const EnvPrefix = "CS_IDENTITY"

// Proof freshness for both endpoints. These are this service's proof profile,
// not deployment settings: a proof older than ProofMaxAge, or dated more than
// ProofFutureSkew ahead, is refused. The skew matches the access-token
// verifier's accesstoken.DefaultClockSkew.
const (
	ProofMaxAge     = 2 * time.Minute
	ProofFutureSkew = accesstoken.DefaultClockSkew
)

// MaxKeyFileBytes bounds the key file read. An Ed25519 PKCS#8 PEM is about 120
// bytes; anything near this bound is not a key file.
const MaxKeyFileBytes = 16 << 10

// MinSecretKeyBytes is the shortest HMAC key accepted for Dependencies.SecretKey.
const MinSecretKeyBytes = 32

// ErrInvalidConfig wraps every configuration, key-file and dependency failure.
var ErrInvalidConfig = errors.New("identity bootstrap: invalid configuration")

// SecurityConfig is the configuration contract above. Field order is the
// order problems are reported in.
type SecurityConfig struct {
	AccessTokenPrivateKeyFile string `env:"ACCESS_TOKEN_PRIVATE_KEY_FILE" required:"true"`
	AccessTokenIssuer         string `env:"ACCESS_TOKEN_ISSUER" required:"true"`
	AccessTokenAudience       string `env:"ACCESS_TOKEN_AUDIENCE" required:"true"`
	AccessTokenKeyID          string `env:"ACCESS_TOKEN_KID" required:"true"`
	EnrollmentCanonicalURL    string `env:"ENROLLMENT_CANONICAL_URL" required:"true"`
	RefreshCanonicalURL       string `env:"REFRESH_CANONICAL_URL" required:"true"`
}

// LoadSecurityConfig reads the contract from the environment through the
// platform loader — which reports every missing variable at once — and then
// applies Validate.
func LoadSecurityConfig() (SecurityConfig, error) {
	var c SecurityConfig
	if err := platform.LoadConfig(&c, EnvPrefix); err != nil {
		return SecurityConfig{}, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	if err := c.Validate(); err != nil {
		return SecurityConfig{}, err
	}
	return c, nil
}

// Validate refuses blank or padded values and identical endpoint URLs. The
// platform loader treats an empty value as unset but accepts whitespace, so
// this is what rejects "   ". Everything else is left to the constructors that
// own the rules, so they are not restated here.
func (c SecurityConfig) Validate() error {
	var problems []string
	for _, f := range []struct{ name, value string }{
		{"ACCESS_TOKEN_PRIVATE_KEY_FILE", c.AccessTokenPrivateKeyFile},
		{"ACCESS_TOKEN_ISSUER", c.AccessTokenIssuer},
		{"ACCESS_TOKEN_AUDIENCE", c.AccessTokenAudience},
		{"ACCESS_TOKEN_KID", c.AccessTokenKeyID},
		{"ENROLLMENT_CANONICAL_URL", c.EnrollmentCanonicalURL},
		{"REFRESH_CANONICAL_URL", c.RefreshCanonicalURL},
	} {
		switch {
		case strings.TrimSpace(f.value) == "":
			problems = append(problems, EnvPrefix+"_"+f.name+" is blank")
		case strings.TrimSpace(f.value) != f.value:
			problems = append(problems, EnvPrefix+"_"+f.name+" has leading or trailing whitespace")
		}
	}
	if len(problems) == 0 && c.EnrollmentCanonicalURL == c.RefreshCanonicalURL {
		problems = append(problems, EnvPrefix+"_ENROLLMENT_CANONICAL_URL and "+EnvPrefix+"_REFRESH_CANONICAL_URL must differ")
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, strings.Join(problems, "; "))
	}
	return nil
}

// Dependencies are what the configuration does not describe. Each is required.
type Dependencies struct {
	// Repository holds OTP challenges, rate-limit state and sessions.
	Repository app.IdentityRepository
	// SecretKey is the HMAC key for OTP code hashes and subscriber ids. It must
	// be stable across restarts and replicas, or the same MSISDN maps to a
	// different subscriber. It has no configuration contract yet.
	SecretKey []byte
	// EnrollmentReplay and RefreshReplay reserve proof identifiers. They may be
	// the same store: reservations are keyed by key thumbprint and jti.
	EnrollmentReplay dpop.ReplayStore
	RefreshReplay    dpop.ReplayStore
	// Now is the clock for proof freshness and token timestamps; nil is
	// time.Now.
	Now func() time.Time
}

// Build constructs an IdentityService with access-token signing, device-key
// enrolment and proof-bound refresh all configured, or returns an error and
// nothing. Errors never carry key material.
//
// A running process reaches Build only through Open, which supplies the
// Dependencies safely: SecretKey from CS_IDENTITY_HMAC_SECRET_KEY_FILE, and a
// pgstore repository and replay stores shared by every replica. Tests may call
// Build directly with in-memory dependencies, which are process-local: never
// suitable for more than one replica, or for a process that restarts.
func Build(cfg SecurityConfig, deps Dependencies) (*app.IdentityService, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if deps.Repository == nil {
		return nil, fmt.Errorf("%w: a repository is required", ErrInvalidConfig)
	}
	if len(deps.SecretKey) < MinSecretKeyBytes {
		return nil, fmt.Errorf("%w: the HMAC secret key must be at least %d bytes", ErrInvalidConfig, MinSecretKeyBytes)
	}
	if deps.EnrollmentReplay == nil || deps.RefreshReplay == nil {
		return nil, fmt.Errorf("%w: enrolment and refresh replay stores are required", ErrInvalidConfig)
	}

	signer, err := loadSigner(cfg, deps.Now)
	if err != nil {
		return nil, err
	}
	enrol, err := dpop.NewVerifier(dpop.Config{
		HTU: cfg.EnrollmentCanonicalURL, MaxAge: ProofMaxAge, FutureSkew: ProofFutureSkew,
		Replay: deps.EnrollmentReplay, Now: deps.Now,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %s_ENROLLMENT_CANONICAL_URL: %w", ErrInvalidConfig, EnvPrefix, err)
	}
	refresh, err := dpop.NewVerifier(dpop.Config{
		HTU: cfg.RefreshCanonicalURL, MaxAge: ProofMaxAge, FutureSkew: ProofFutureSkew,
		Replay: deps.RefreshReplay, Now: deps.Now,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %s_REFRESH_CANONICAL_URL: %w", ErrInvalidConfig, EnvPrefix, err)
	}
	// A proof names the canonical URL (htu). If its path is not the path the
	// API serves, no proof for that endpoint could ever be accepted; refuse.
	// The URLs are already known canonical, so they parse.
	for _, c := range []struct{ name, url, path string }{
		{"ENROLLMENT_CANONICAL_URL", cfg.EnrollmentCanonicalURL, routes.Enrollment},
		{"REFRESH_CANONICAL_URL", cfg.RefreshCanonicalURL, routes.Refresh},
	} {
		if u, err := url.Parse(c.url); err != nil || u.Path != c.path {
			return nil, fmt.Errorf("%w: %s_%s must name the served path %s", ErrInvalidConfig, EnvPrefix, c.name, c.path)
		}
	}

	svc := app.NewIdentityService(deps.Repository, append([]byte(nil), deps.SecretKey...))
	svc.SetAccessTokenSigner(signer)
	svc.SetDeviceProofVerifier(enrol)
	svc.SetRefreshProofVerifier(refresh)

	// Account deletion's canonical URL is the refresh canonical URL's origin
	// and the deletion path: identity has one origin, and every canonical
	// path is fixed by routes, so a separate variable could only be
	// misconfigured. Its proofs share refresh's replay store, which is keyed
	// by key and proof id; a proof for one endpoint cannot pass the other (htu).
	deletion, derr := dpop.NewVerifier(dpop.Config{
		HTU: AccountDeletionCanonicalURL(cfg.RefreshCanonicalURL), MaxAge: ProofMaxAge, FutureSkew: ProofFutureSkew,
		Replay: deps.RefreshReplay, Now: deps.Now,
	})
	if derr != nil {
		return nil, fmt.Errorf("%w: account deletion URL from %s_REFRESH_CANONICAL_URL: %w", ErrInvalidConfig, EnvPrefix, derr)
	}
	svc.SetAccountDeletionProofVerifier(deletion)
	return svc, nil
}

// AccountDeletionCanonicalURL is the canonical URL a deletion proof must name:
// refreshCanonicalURL's scheme and host with routes.AccountDeletion. The
// refresh URL is already validated as canonical.
func AccountDeletionCanonicalURL(refreshCanonicalURL string) string {
	u, err := url.Parse(refreshCanonicalURL)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + routes.AccountDeletion
}

// loadSigner reads and parses the key file and builds the signer. Parsing and
// key validation are accesstoken's; neither the file's bytes nor the parsed key
// outlive this call — NewSigner keeps its own copy, and the buffers here are
// zeroed (best effort: the runtime may already have copied them).
func loadSigner(cfg SecurityConfig, now func() time.Time) (*accesstoken.Signer, error) {
	const name = EnvPrefix + "_ACCESS_TOKEN_PRIVATE_KEY_FILE"
	f, err := os.Open(cfg.AccessTokenPrivateKeyFile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%w: %s: the key file does not exist", ErrInvalidConfig, name)
	case errors.Is(err, fs.ErrPermission):
		return nil, fmt.Errorf("%w: %s: permission denied reading the key file", ErrInvalidConfig, name)
	case err != nil:
		return nil, fmt.Errorf("%w: %s: the key file cannot be opened", ErrInvalidConfig, name)
	}
	defer f.Close() //nolint:errcheck // read-only file, already fully read: a Close error cannot lose data
	if info, statErr := f.Stat(); statErr != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s: not a regular file", ErrInvalidConfig, name)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxKeyFileBytes+1))
	defer clear(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: the key file cannot be read", ErrInvalidConfig, name)
	}
	if len(data) > MaxKeyFileBytes {
		return nil, fmt.Errorf("%w: %s: the key file is larger than %d bytes", ErrInvalidConfig, name, MaxKeyFileBytes)
	}

	key, err := accesstoken.ParsePrivateKeyPEM(data)
	defer clear(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalidConfig, name, err)
	}
	signer, err := accesstoken.NewSigner(accesstoken.SignerConfig{
		Issuer: cfg.AccessTokenIssuer, Audience: cfg.AccessTokenAudience,
		KeyID: cfg.AccessTokenKeyID, Key: key, Now: now,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: access-token signer: %w", ErrInvalidConfig, err)
	}
	return signer, nil
}
