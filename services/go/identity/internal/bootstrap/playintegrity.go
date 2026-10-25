package bootstrap

// PLAY INTEGRITY (ADR-0031): the verifier's configuration contract, and the
// builder that turns it into a verifier. DORMANT.
//
// Nothing in cmd/server calls LoadPlayIntegrityConfig or
// NewPlayIntegrityVerifier, and a test keeps it that way. Wiring them is what
// would switch on ADR-0031 D31.3 (a token Google rejects is refused), and that
// waits for:
//   - owner ratification of ADR-0031 and of the policy values
//     (docs/phase19/PLAY-INTEGRITY-DECISION-MEMO.md §11–§12);
//   - the external setup in docs/phase19/PLAY-INTEGRITY-IMPLEMENTATION-HANDOFF.md.
//
// The contract (CS_IDENTITY_PLAY_INTEGRITY_*):
//
//	PACKAGE_NAME            the Android application id
//	CERT_DIGESTS            accepted signing-certificate SHA-256 digests,
//	                        base64url (43 characters), comma-separated
//	MAX_AGE                 how old a verdict may be, in (0, 10m]
//	FUTURE_SKEW             how far ahead a verdict timestamp may be, in [0, 1m]
//	DECODE_TIMEOUT          one decode call, in (0, 5s]
//	CREDENTIAL_CONFIG_FILE  Google's Workload Identity Federation credential
//	                        configuration (no private key; not a secret)
//
// No variable has a default. The values are owner policy, so code never
// chooses them. All six are set, or none: a partial configuration fails
// startup, so a half-configured verifier cannot exist.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/playintegrity"
)

// MaxCredentialConfigBytes bounds the credential configuration file, which is
// a few hundred bytes of JSON.
const MaxCredentialConfigBytes = 64 << 10

// PlayIntegrityConfig is the contract above.
type PlayIntegrityConfig struct {
	PackageName          string        `env:"PACKAGE_NAME"`
	CertificateDigests   []string      `env:"CERT_DIGESTS"`
	MaxAge               time.Duration `env:"MAX_AGE"`
	FutureSkew           time.Duration `env:"FUTURE_SKEW"`
	DecodeTimeout        time.Duration `env:"DECODE_TIMEOUT"`
	CredentialConfigFile string        `env:"CREDENTIAL_CONFIG_FILE"`
}

// PlayIntegrityEnvPrefix namespaces the contract's variables.
const PlayIntegrityEnvPrefix = EnvPrefix + "_PLAY_INTEGRITY"

// PlayIntegrityVariables are the contract's variable names, in field order.
var PlayIntegrityVariables = []string{
	PlayIntegrityEnvPrefix + "_PACKAGE_NAME",
	PlayIntegrityEnvPrefix + "_CERT_DIGESTS",
	PlayIntegrityEnvPrefix + "_MAX_AGE",
	PlayIntegrityEnvPrefix + "_FUTURE_SKEW",
	PlayIntegrityEnvPrefix + "_DECODE_TIMEOUT",
	PlayIntegrityEnvPrefix + "_CREDENTIAL_CONFIG_FILE",
}

// LoadPlayIntegrityConfig reads the contract. With no variable set it reports
// not configured, and no error. Every variable set and valid is configured.
// Anything in between is an error that names variables, never values.
func LoadPlayIntegrityConfig() (cfg PlayIntegrityConfig, configured bool, err error) {
	var missing []string
	for _, name := range PlayIntegrityVariables {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	switch len(missing) {
	case len(PlayIntegrityVariables):
		return PlayIntegrityConfig{}, false, nil
	case 0:
	default:
		return PlayIntegrityConfig{}, false, fmt.Errorf("%w: Play Integrity is partly configured; set all of %s or none; missing: %s",
			ErrInvalidConfig, strings.Join(PlayIntegrityVariables, ", "), strings.Join(missing, ", "))
	}
	if err := platform.LoadConfig(&cfg, PlayIntegrityEnvPrefix); err != nil {
		return PlayIntegrityConfig{}, false, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	if err := cfg.Policy().Validate(); err != nil {
		return PlayIntegrityConfig{}, false, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	return cfg, true, nil
}

// Policy is the verdict policy the configuration describes.
func (c PlayIntegrityConfig) Policy() app.IntegrityPolicy {
	return app.IntegrityPolicy{
		PackageName:        c.PackageName,
		CertificateDigests: append([]string(nil), c.CertificateDigests...),
		MaxAge:             c.MaxAge,
		FutureSkew:         c.FutureSkew,
		DecodeTimeout:      c.DecodeTimeout,
	}
}

// NewPlayIntegrityVerifier builds the Google verifier for a loaded
// configuration: a Workload Identity Federation token source (a
// service-account key is refused) behind the decodeIntegrityToken adapter.
// The context must outlive the verifier, because later token exchanges use
// it. A nil client gives both calls a bounded client that never follows
// redirects.
func NewPlayIntegrityVerifier(ctx context.Context, c PlayIntegrityConfig, client *http.Client) (app.IntegrityVerifier, error) {
	data, err := readSecretFile(PlayIntegrityEnvPrefix+"_CREDENTIAL_CONFIG_FILE", c.CredentialConfigFile, MaxCredentialConfigBytes)
	if err != nil {
		return nil, err
	}
	tokenClient := client
	if tokenClient == nil {
		tokenClient = &http.Client{
			Timeout:       playintegrity.DefaultTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	// data is not cleared: it is not secret, and Google's Credentials keeps a
	// reference to it.
	tokens, err := playintegrity.NewWorkloadIdentityTokens(ctx, data, tokenClient)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalidConfig, PlayIntegrityEnvPrefix+"_CREDENTIAL_CONFIG_FILE", err)
	}
	decoder, err := playintegrity.New(playintegrity.Config{PackageName: c.PackageName, Tokens: tokens, HTTPClient: client})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	return decoder, nil
}
