package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/playintegrity"
)

// A syntactically valid base64url SHA-256 digest (43 characters); not a real
// certificate's.
const testDigest = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

// clearPlayIntegrityEnv unsets the whole contract for the test.
func clearPlayIntegrityEnv(t *testing.T) {
	t.Helper()
	for _, name := range PlayIntegrityVariables {
		t.Setenv(name, "")
	}
}

// writeCredentialConfig writes a Workload Identity Federation credential
// configuration (no secret) and returns its path.
func writeCredentialConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credential-config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func federationConfig(t *testing.T) string {
	t.Helper()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("projected-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	return `{"type":"external_account",` +
		`"audience":"//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/eks",` +
		`"subject_token_type":"urn:ietf:params:oauth:token-type:jwt",` +
		`"token_url":"https://sts.googleapis.com/v1/token",` +
		`"service_account_impersonation_url":"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/pi@p.iam.gserviceaccount.com:generateAccessToken",` +
		`"credential_source":{"file":` + quote(tokenFile) + `,"format":{"type":"text"}}}`
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"` }

// setAllPlayIntegrity sets a complete, valid configuration. The durations are
// test values only: the real ones are owner policy and have no default.
func setAllPlayIntegrity(t *testing.T, credentialFile string) {
	t.Helper()
	values := []string{"com.callscreen", testDigest, "90s", "15s", "2s", credentialFile}
	for i, name := range PlayIntegrityVariables {
		t.Setenv(name, values[i])
	}
}

func TestPlayIntegrityConfig_UnsetMeansNoVerifier(t *testing.T) {
	clearPlayIntegrityEnv(t)
	cfg, configured, err := LoadPlayIntegrityConfig()
	if err != nil || configured {
		t.Fatalf("configured = %v, err = %v; want no verifier and no error (today's behaviour)", configured, err)
	}
	if cfg.PackageName != "" || cfg.MaxAge != 0 {
		t.Fatalf("cfg = %+v, want the zero value", cfg)
	}
}

func TestPlayIntegrityConfig_AllSetAndValid(t *testing.T) {
	clearPlayIntegrityEnv(t)
	setAllPlayIntegrity(t, writeCredentialConfig(t, federationConfig(t)))
	cfg, configured, err := LoadPlayIntegrityConfig()
	if err != nil || !configured {
		t.Fatalf("configured = %v, err = %v; want a configured verifier", configured, err)
	}
	p := cfg.Policy()
	if p.PackageName != "com.callscreen" || len(p.CertificateDigests) != 1 || p.CertificateDigests[0] != testDigest ||
		p.MaxAge != 90*time.Second || p.FutureSkew != 15*time.Second || p.DecodeTimeout != 2*time.Second {
		t.Fatalf("policy = %+v, not what the environment says", p)
	}
}

func TestPlayIntegrityConfig_PartialConfigurationRefusesToStart(t *testing.T) {
	credential := writeCredentialConfig(t, federationConfig(t))
	for i, missing := range PlayIntegrityVariables {
		t.Run(missing, func(t *testing.T) {
			clearPlayIntegrityEnv(t)
			setAllPlayIntegrity(t, credential)
			t.Setenv(missing, "")
			_, configured, err := LoadPlayIntegrityConfig()
			if !errors.Is(err, ErrInvalidConfig) || configured {
				t.Fatalf("configured = %v, err = %v; want a refusal", configured, err)
			}
			if !strings.Contains(err.Error(), "missing: "+missing) {
				t.Fatalf("the error does not name %s: %v", missing, err)
			}
			for j, v := range []string{"com.callscreen", testDigest, "90s", "15s", "2s"} {
				if j != i && strings.Contains(err.Error(), v) {
					t.Fatalf("the error repeats a value (%q): %v", v, err)
				}
			}
		})
	}
}

func TestPlayIntegrityConfig_ValuesOutsideTheCodesBoundsAreRefused(t *testing.T) {
	credential := writeCredentialConfig(t, federationConfig(t))
	for name, value := range map[string]string{
		PlayIntegrityVariables[0]: "not an app id",
		PlayIntegrityVariables[1]: "AB:CD:EF:01", // a Play Console hex fingerprint, not base64url
		PlayIntegrityVariables[2]: "11m",         // MaxAge above 10m
		PlayIntegrityVariables[3]: "2m",          // FutureSkew above 1m
		PlayIntegrityVariables[4]: "6s",          // DecodeTimeout above 5s
	} {
		t.Run(name, func(t *testing.T) {
			clearPlayIntegrityEnv(t)
			setAllPlayIntegrity(t, credential)
			t.Setenv(name, value)
			if _, configured, err := LoadPlayIntegrityConfig(); !errors.Is(err, ErrInvalidConfig) || configured {
				t.Fatalf("%s=%q: configured = %v, err = %v; want a refusal", name, value, configured, err)
			}
		})
	}
	t.Run("an unparsable duration", func(t *testing.T) {
		clearPlayIntegrityEnv(t)
		setAllPlayIntegrity(t, credential)
		t.Setenv(PlayIntegrityVariables[2], "two minutes")
		if _, _, err := LoadPlayIntegrityConfig(); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("err = %v, want ErrInvalidConfig", err)
		}
	})
}

func TestNewPlayIntegrityVerifier_BuildsFromAFederationConfiguration(t *testing.T) {
	clearPlayIntegrityEnv(t)
	setAllPlayIntegrity(t, writeCredentialConfig(t, federationConfig(t)))
	cfg, _, err := LoadPlayIntegrityConfig()
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewPlayIntegrityVerifier(context.Background(), cfg, nil)
	if err != nil || v == nil {
		t.Fatalf("verifier = %v, err = %v; want a verifier (no network is touched until a token is decoded)", v, err)
	}
}

func TestNewPlayIntegrityVerifier_RefusesAServiceAccountKey(t *testing.T) {
	clearPlayIntegrityEnv(t)
	// The type alone is refused. The key field is a placeholder, deliberately
	// not credential-shaped.
	key := `{"type":"service_account","private_key":"placeholder-not-a-key-value","client_email":"sa@p.iam.gserviceaccount.com"}`
	setAllPlayIntegrity(t, writeCredentialConfig(t, key))
	cfg, _, err := LoadPlayIntegrityConfig()
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewPlayIntegrityVerifier(context.Background(), cfg, nil)
	if !errors.Is(err, ErrInvalidConfig) || !errors.Is(err, playintegrity.ErrInvalidCredentialConfig) {
		t.Fatalf("err = %v, want a refusal: ADR-0008 allows no static cloud credentials", err)
	}
	if strings.Contains(err.Error(), "placeholder-not-a-key-value") || strings.Contains(err.Error(), cfg.CredentialConfigFile) {
		t.Fatalf("the error repeats key material or the path: %v", err)
	}
}

func TestNewPlayIntegrityVerifier_AMissingCredentialFileIsNamed(t *testing.T) {
	cfg := PlayIntegrityConfig{
		PackageName: "com.callscreen", CertificateDigests: []string{testDigest},
		MaxAge: time.Minute, FutureSkew: time.Second, DecodeTimeout: time.Second,
		CredentialConfigFile: filepath.Join(t.TempDir(), "absent.json"),
	}
	_, err := NewPlayIntegrityVerifier(context.Background(), cfg, nil)
	if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), PlayIntegrityVariables[5]) {
		t.Fatalf("err = %v, want ErrInvalidConfig naming %s", err, PlayIntegrityVariables[5])
	}
}

// fakeGoogleAPIs serves STS, IAM Credentials and Play Integrity for the
// assembled-chain test, recording what the decode call carried.
type fakeGoogleAPIs struct {
	mu         sync.Mutex
	decodeAuth string
	decodePath string
	decodeBody string
	verdictAt  time.Time
}

func (f *fakeGoogleAPIs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/token"):
		_, _ = io.WriteString(w, `{"access_token":"federated","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":3600}`)
	case strings.HasSuffix(r.URL.Path, ":generateAccessToken"):
		_, _ = io.WriteString(w, `{"accessToken":"impersonated","expireTime":"`+time.Now().Add(time.Hour).UTC().Format(time.RFC3339)+`"}`)
	case strings.HasSuffix(r.URL.Path, ":decodeIntegrityToken"):
		f.decodeAuth = r.Header.Get("Authorization")
		f.decodePath = r.Header.Get("X-Original-Host") + r.URL.Path
		b, _ := io.ReadAll(r.Body)
		f.decodeBody = string(b)
		_, _ = io.WriteString(w, `{"tokenPayloadExternal":{
			"requestDetails":{"requestPackageName":"com.callscreen","requestHash":"bound-hash","timestampMillis":"`+strconv.FormatInt(f.verdictAt.UnixMilli(), 10)+`"},
			"appIntegrity":{"appRecognitionVerdict":"PLAY_RECOGNIZED","packageName":"com.callscreen","certificateSha256Digest":["`+testDigest+`"]},
			"deviceIntegrity":{"deviceRecognitionVerdict":["MEETS_DEVICE_INTEGRITY"]},
			"accountDetails":{"appLicensingVerdict":"LICENSED"}}}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type toFake struct{ target *url.URL }

func (t toFake) RoundTrip(r *http.Request) (*http.Response, error) {
	c := r.Clone(r.Context())
	c.Header.Set("X-Original-Host", r.URL.Host)
	c.URL.Scheme, c.URL.Host, c.Host = t.target.Scheme, t.target.Host, t.target.Host
	return http.DefaultTransport.RoundTrip(c)
}

// The whole chain the wiring PR will switch on, against a local fake of
// Google: configuration → Workload Identity Federation (exchange, then
// impersonation) → decodeIntegrityToken with the impersonated token →
// the configured policy certifies a genuine, bound, fresh verdict.
func TestNewPlayIntegrityVerifier_TheAssembledChainAgainstAFakeGoogle(t *testing.T) {
	f := &fakeGoogleAPIs{verdictAt: time.Now()}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: toFake{target}, Timeout: 5 * time.Second}

	clearPlayIntegrityEnv(t)
	setAllPlayIntegrity(t, writeCredentialConfig(t, federationConfig(t)))
	cfg, configured, err := LoadPlayIntegrityConfig()
	if err != nil || !configured {
		t.Fatalf("configured = %v, err = %v", configured, err)
	}
	v, err := NewPlayIntegrityVerifier(context.Background(), cfg, client)
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := v.Decode(context.Background(), "integrity-token")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	f.mu.Lock()
	auth, path, body := f.decodeAuth, f.decodePath, f.decodeBody
	f.mu.Unlock()
	if auth != "Bearer impersonated" {
		t.Fatalf("decode authorised with %q, want the impersonated service account's token", auth)
	}
	if path != "playintegrity.googleapis.com/v1/com.callscreen:decodeIntegrityToken" {
		t.Fatalf("decode called %q", path)
	}
	var req struct {
		IntegrityToken string `json:"integrity_token"`
	}
	if json.Unmarshal([]byte(body), &req) != nil || req.IntegrityToken != "integrity-token" {
		t.Fatalf("decode body = %q", body)
	}
	if a := cfg.Policy().Assess(verdict, "bound-hash", time.Now()); a.Tier != app.TierCertified {
		t.Fatalf("assessment = %+v, want certified for a genuine, bound, fresh verdict", a)
	}
}

// TestPlayIntegrity_IsNotWiredIntoTheServer is a tripwire. Wiring the
// verifier switches on ADR-0031 D31.3 (a token Google rejects is refused),
// which waits for owner ratification and the external setup
// (docs/phase19/PLAY-INTEGRITY-IMPLEMENTATION-HANDOFF.md §3). The wiring PR
// removes this test deliberately; nothing else may make it fail.
func TestPlayIntegrity_IsNotWiredIntoTheServer(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "cmd", "server", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("cmd/server sources not found: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f) //nolint:gosec // G304: a repository source file chosen by this test
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range []string{"NewPlayIntegrityVerifier", "LoadPlayIntegrityConfig", "SetIntegrityVerifier", "playintegrity."} {
			if strings.Contains(string(src), call) {
				t.Errorf("%s mentions %s: the Play Integrity verifier is wired before ADR-0031's ratification", filepath.Base(f), call)
			}
		}
	}
}
