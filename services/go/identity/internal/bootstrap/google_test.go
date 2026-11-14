package bootstrap

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// Google sign-in configuration (google.go): all or none, every value checked,
// no value echoed, and installation failing closed. Nothing here contacts
// Google (TestMain's dead proxy stands guard; startup fetches nothing).

const (
	gServer  = "123-server.apps.googleusercontent.com"
	gAndroid = "123-android.apps.googleusercontent.com"
)

func googleEnv(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	keyring := filepath.Join(dir, "keyring")
	allowlist := filepath.Join(dir, "allowlist")
	if err := os.WriteFile(keyring, []byte("k1 "+strings.Repeat("ab", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(allowlist, []byte("tester@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		GoogleServerClientIDVariable:   gServer,
		GoogleAndroidClientIDsVariable: gAndroid + ",456-android.apps.googleusercontent.com",
		GoogleSubjectKeyringVariable:   keyring,
		GoogleAllowedEmailsVariable:    allowlist,
		AccountCreationCapVariable:     "5",
	}
}

func TestGoogleSettings_AllOrNone(t *testing.T) {
	off, err := resolveGoogleSignIn(lookupFrom(map[string]string{}))
	if err != nil || off.Enabled() {
		t.Fatalf("none set: (%+v, %v), want off", off, err)
	}
	env := googleEnv(t)
	on, err := resolveGoogleSignIn(lookupFrom(env))
	if err != nil || !on.Enabled() || on.HourlyCap != 5 || len(on.AndroidClientIDs) != 2 {
		t.Fatalf("all set: (%+v, %v)", on, err)
	}
	for _, missing := range googleVariables {
		partial := merge(env)
		delete(partial, missing)
		if _, err := resolveGoogleSignIn(lookupFrom(partial)); !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), missing) {
			t.Errorf("without %s: err = %v", missing, err)
		}
	}
}

func TestGoogleSettings_RefusesBadValuesWithoutEchoingThem(t *testing.T) {
	env := googleEnv(t)
	for name, change := range map[string]map[string]string{
		"bad server id":       {GoogleServerClientIDVariable: "s3cr3t-value"},
		"padded server id":    {GoogleServerClientIDVariable: " " + gServer},
		"blank":               {AccountCreationCapVariable: ""},
		"bad android id":      {GoogleAndroidClientIDsVariable: "s3cr3t-value"},
		"duplicate android":   {GoogleAndroidClientIDsVariable: gAndroid + "," + gAndroid},
		"empty android entry": {GoogleAndroidClientIDsVariable: gAndroid + ","},
		"relative keyring":    {GoogleSubjectKeyringVariable: "keyring"},
		"relative allowlist":  {GoogleAllowedEmailsVariable: "allowlist"},
		"same file":           {GoogleAllowedEmailsVariable: env[GoogleSubjectKeyringVariable]},
		"cap zero":            {AccountCreationCapVariable: "0"},
		"cap too big":         {AccountCreationCapVariable: "10001"},
		"cap not a number":    {AccountCreationCapVariable: "s3cr3t-value"},
	} {
		_, err := resolveGoogleSignIn(lookupFrom(merge(env, change)))
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: err = %v", name, err)
			continue
		}
		if strings.Contains(err.Error(), "s3cr3t") {
			t.Errorf("%s: the error echoes the value: %v", name, err)
		}
	}
}

func installDeps(secret []byte) googleSignInDeps {
	return googleSignInDeps{
		secretKey: secret, refreshCanonicalURL: "https://identity.example/v1/token/refresh",
		replay: dpop.NewMemoryReplayStore(),
	}
}

func TestInstallGoogleSignIn_EnablesOrFailsClosed(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	env := googleEnv(t)
	settings, err := resolveGoogleSignIn(lookupFrom(env))
	if err != nil {
		t.Fatal(err)
	}
	svc := app.NewIdentityService(app.NewMemoryIdentityRepository(), secret)
	if err := installGoogleSignIn(svc, nil, installDeps(secret)); err != nil || svc.GoogleSignInEnabled() {
		t.Fatalf("off: (%v, %v)", svc.GoogleSignInEnabled(), err)
	}
	if err := installGoogleSignIn(svc, &settings, installDeps(secret)); err != nil || !svc.GoogleSignInEnabled() {
		t.Fatalf("on: (%v, %v)", svc.GoogleSignInEnabled(), err)
	}
	if got := GoogleSignInCanonicalURL("https://identity.example/v1/token/refresh"); got != "https://identity.example/v1/auth/google" {
		t.Errorf("canonical URL = %q", got)
	}

	dir := t.TempDir()
	reused := filepath.Join(dir, "reused")
	if err := os.WriteFile(reused, []byte("k1 "+hex.EncodeToString(secret)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	badList := filepath.Join(dir, "badlist")
	if err := os.WriteFile(badList, []byte("not-an-address\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(g *GoogleSignInSettings){
		"keyring reuses the HMAC key": func(g *GoogleSignInSettings) { g.KeyringFile = reused },
		"missing keyring":             func(g *GoogleSignInSettings) { g.KeyringFile = filepath.Join(dir, "none") },
		"bad allowlist":               func(g *GoogleSignInSettings) { g.AllowlistFile = badList },
		"missing allowlist":           func(g *GoogleSignInSettings) { g.AllowlistFile = filepath.Join(dir, "none") },
	} {
		s := settings
		mutate(&s)
		fresh := app.NewIdentityService(app.NewMemoryIdentityRepository(), secret)
		if err := installGoogleSignIn(fresh, &s, installDeps(secret)); !errors.Is(err, ErrInvalidConfig) || fresh.GoogleSignInEnabled() {
			t.Errorf("%s: (%v, enabled %v), want refused and off", name, err, fresh.GoogleSignInEnabled())
		}
	}
	// A repository without account storage is refused.
	noAccounts := app.NewIdentityService(withoutAccountStore{app.NewMemoryIdentityRepository()}, secret)
	if err := installGoogleSignIn(noAccounts, &settings, installDeps(secret)); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("no account storage: %v", err)
	}
}

type withoutAccountStore struct{ app.IdentityRepository }

// Google sign-in and SMS sign-in are configured independently.
func TestGoogleSettings_AreIndependentOfOTP(t *testing.T) {
	env := merge(map[string]string{environmentVariable: "staging", OtpProviderVariable: OtpProviderNone}, googleEnv(t))
	d, err := resolveOtpDelivery(lookupFrom(env))
	if err != nil || d.Provider != "" {
		t.Errorf("OTP with Google set: (%+v, %v)", d, err)
	}
	g, err := resolveGoogleSignIn(lookupFrom(map[string]string{environmentVariable: "staging", OtpProviderVariable: OtpProviderNone}))
	if err != nil || g.Enabled() {
		t.Errorf("Google with only OTP set: (%+v, %v)", g, err)
	}
}
