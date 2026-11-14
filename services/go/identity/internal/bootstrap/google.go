package bootstrap

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/googleauth"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

// # Sign in with Google (ADR-0035, ADR-0036; G2)
//
// Five variables, ALL OR NONE. None set: Google sign-in is off and
// POST /v1/auth/google answers 503 ERR_SIGN_IN_UNAVAILABLE. Some but not all
// set, or any invalid: startup is refused. They are independent of the OTP
// variables: nothing here changes SMS sign-in.
//
//	CS_IDENTITY_GOOGLE_SERVER_CLIENT_ID      the web client id the app
//	                                         requests tokens for (aud)
//	CS_IDENTITY_GOOGLE_ANDROID_CLIENT_IDS    comma-separated Android client
//	                                         ids azp may name (at least one)
//	CS_IDENTITY_GOOGLE_SUBJECT_KEYRING_FILE  the Google identity pseudonym
//	                                         keyring (app/pseudonym_keyring.go);
//	                                         refused if any key is the
//	                                         service's HMAC key
//	CS_IDENTITY_GOOGLE_ALLOWED_EMAILS_FILE   the M1 tester allowlist
//	                                         (app/email_allowlist.go)
//	CS_IDENTITY_ACCOUNT_CREATION_HOURLY_CAP  1-10000 new accounts per hour
//
// Startup contacts nothing: Google's keys are fetched on the first sign-in.
// Identity then needs egress to www.googleapis.com, a network-policy change
// that is not part of this code.

// Google sign-in variables.
const (
	GoogleServerClientIDVariable   = EnvPrefix + "_GOOGLE_SERVER_CLIENT_ID"
	GoogleAndroidClientIDsVariable = EnvPrefix + "_GOOGLE_ANDROID_CLIENT_IDS"
	GoogleSubjectKeyringVariable   = EnvPrefix + "_GOOGLE_SUBJECT_KEYRING_FILE"
	GoogleAllowedEmailsVariable    = EnvPrefix + "_GOOGLE_ALLOWED_EMAILS_FILE"
	AccountCreationCapVariable     = EnvPrefix + "_ACCOUNT_CREATION_HOURLY_CAP"
)

// googleVariables lists them, for the all-or-none rule.
var googleVariables = []string{
	GoogleServerClientIDVariable, GoogleAndroidClientIDsVariable, GoogleSubjectKeyringVariable,
	GoogleAllowedEmailsVariable, AccountCreationCapVariable,
}

// GoogleSignInSettings is the resolved configuration; the zero value is off.
type GoogleSignInSettings struct {
	ServerClientID   string
	AndroidClientIDs []string
	KeyringFile      string
	AllowlistFile    string
	HourlyCap        int
}

// Enabled reports whether Google sign-in is configured.
func (g GoogleSignInSettings) Enabled() bool { return g.ServerClientID != "" }

// resolveGoogleSignIn applies the all-or-none rule and validates each value.
// Errors name variables, never values (a misplaced secret is not echoed).
func resolveGoogleSignIn(lookup func(string) (string, bool)) (GoogleSignInSettings, error) {
	values := map[string]string{}
	for _, name := range googleVariables {
		if v, ok := lookup(name); ok {
			values[name] = v
		}
	}
	if len(values) == 0 {
		return GoogleSignInSettings{}, nil
	}
	for _, name := range googleVariables {
		v, ok := values[name]
		switch {
		case !ok:
			return GoogleSignInSettings{}, fmt.Errorf("%w: %s is required when any Google sign-in variable is set", ErrInvalidConfig, name)
		case strings.TrimSpace(v) == "" || strings.TrimSpace(v) != v:
			return GoogleSignInSettings{}, fmt.Errorf("%w: %s is blank or padded", ErrInvalidConfig, name)
		}
	}
	g := GoogleSignInSettings{
		ServerClientID: values[GoogleServerClientIDVariable],
		KeyringFile:    values[GoogleSubjectKeyringVariable],
		AllowlistFile:  values[GoogleAllowedEmailsVariable],
	}
	if !googleauth.ValidClientID(g.ServerClientID) {
		return GoogleSignInSettings{}, fmt.Errorf("%w: %s is not a Google OAuth client id", ErrInvalidConfig, GoogleServerClientIDVariable)
	}
	android, err := parseAndroidClientIDs(values[GoogleAndroidClientIDsVariable])
	if err != nil {
		return GoogleSignInSettings{}, err
	}
	g.AndroidClientIDs = android
	if fileErr := checkGoogleFiles(g.KeyringFile, g.AllowlistFile); fileErr != nil {
		return GoogleSignInSettings{}, fileErr
	}
	hourlyCap, err := strconv.Atoi(values[AccountCreationCapVariable])
	if err != nil || hourlyCap < 1 || hourlyCap > app.MaxAccountCreationHourlyCap {
		return GoogleSignInSettings{}, fmt.Errorf("%w: %s must be a whole number between 1 and %d", ErrInvalidConfig, AccountCreationCapVariable, app.MaxAccountCreationHourlyCap)
	}
	g.HourlyCap = hourlyCap
	return g, nil
}

// parseAndroidClientIDs reads the comma-separated list: distinct client ids.
func parseAndroidClientIDs(list string) ([]string, error) {
	var ids []string
	seen := map[string]bool{}
	for _, id := range strings.Split(list, ",") {
		if !googleauth.ValidClientID(id) || seen[id] {
			return nil, fmt.Errorf("%w: %s must list distinct Google OAuth client ids, comma-separated", ErrInvalidConfig, GoogleAndroidClientIDsVariable)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

// checkGoogleFiles requires two absolute paths naming different files.
func checkGoogleFiles(keyring, allowlist string) error {
	for _, f := range []struct{ name, path string }{
		{GoogleSubjectKeyringVariable, keyring}, {GoogleAllowedEmailsVariable, allowlist},
	} {
		if !filepath.IsAbs(f.path) {
			return fmt.Errorf("%w: %s must be an absolute path", ErrInvalidConfig, f.name)
		}
	}
	if filepath.Clean(keyring) == filepath.Clean(allowlist) {
		return fmt.Errorf("%w: %s and %s name the same file", ErrInvalidConfig, GoogleSubjectKeyringVariable, GoogleAllowedEmailsVariable)
	}
	return nil
}

// GoogleSignInCanonicalURL is the canonical URL a Google sign-in proof must
// name: refreshCanonicalURL's scheme and host with routes.GoogleSignIn.
func GoogleSignInCanonicalURL(refreshCanonicalURL string) string {
	u, err := url.Parse(refreshCanonicalURL)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + routes.GoogleSignIn
}

// googleSignInDeps are what installGoogleSignIn needs from Open.
type googleSignInDeps struct {
	secretKey           []byte
	refreshCanonicalURL string
	replay              dpop.ReplayStore
	now                 func() time.Time
	observe             func(googleauth.AuthorizedPartyKind)
	// keysURL and httpClient are test overrides for the Google key fetch.
	keysURL    string
	httpClient *http.Client
}

// authorizedPartyObserver forwards azp kinds to a hook installed after the
// runtime is built (the metrics registry is created later). A nil observer
// observes nothing.
type authorizedPartyObserver struct {
	mu   sync.RWMutex
	hook func(string)
}

func (o *authorizedPartyObserver) observe(kind googleauth.AuthorizedPartyKind) {
	if o == nil {
		return
	}
	o.mu.RLock()
	hook := o.hook
	o.mu.RUnlock()
	if hook != nil {
		hook(string(kind))
	}
}

func (o *authorizedPartyObserver) set(hook func(string)) {
	o.mu.Lock()
	o.hook = hook
	o.mu.Unlock()
}

// installGoogleSignIn builds and installs Google sign-in from g, or does
// nothing when it is off. Every failure stops startup: it fails closed.
func installGoogleSignIn(svc *app.IdentityService, g *GoogleSignInSettings, deps googleSignInDeps) error {
	if g == nil || !g.Enabled() {
		return nil
	}
	keyring, err := app.ReadPseudonymKeyringFile(g.KeyringFile)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalidConfig, GoogleSubjectKeyringVariable, err)
	}
	if keyring.SharesMaterialWith(deps.secretKey) {
		return fmt.Errorf("%w: %s reuses the service's HMAC key; the keyring must hold its own keys", ErrInvalidConfig, GoogleSubjectKeyringVariable)
	}
	allowlist, err := app.ReadEmailAllowlistFile(g.AllowlistFile)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalidConfig, GoogleAllowedEmailsVariable, err)
	}
	accounts, ok := svc.AccountRepository()
	if !ok {
		return fmt.Errorf("%w: the repository does not store accounts", ErrInvalidConfig)
	}
	directory, err := app.NewGoogleAccountDirectory(accounts, keyring)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	verifier, err := googleauth.New(googleauth.Config{
		ServerClientID: g.ServerClientID, AndroidClientIDs: g.AndroidClientIDs,
		ObserveAuthorizedParty: deps.observe, Now: deps.now,
		KeysURL: deps.keysURL, HTTPClient: deps.httpClient,
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	proofs, err := dpop.NewVerifier(dpop.Config{
		HTU: GoogleSignInCanonicalURL(deps.refreshCanonicalURL), MaxAge: ProofMaxAge, FutureSkew: ProofFutureSkew,
		Replay: deps.replay, Now: deps.now,
	})
	if err != nil {
		return fmt.Errorf("%w: Google sign-in URL from %s_REFRESH_CANONICAL_URL: %w", ErrInvalidConfig, EnvPrefix, err)
	}
	if err := svc.SetGoogleSignIn(app.GoogleSignInConfig{
		Verifier: verifier, ProofVerifier: proofs, Directory: directory, Allowlist: allowlist, HourlyCap: g.HourlyCap,
	}); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	return nil
}
