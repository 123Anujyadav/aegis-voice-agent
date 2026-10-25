package playintegrity

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
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGoogle stands in for Google's STS (token exchange) and IAM Credentials
// (impersonation) endpoints. Every request the real oauth2 library makes is
// sent here by redirectTransport, so nothing reaches Google.
type fakeGoogle struct {
	mu            sync.Mutex
	stsCalls      int
	stsForm       url.Values
	stsHost       string
	impersonation struct {
		calls int
		host  string
		path  string
		auth  string
		scope []string
	}
	stsStatus int
}

func (f *fakeGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	host := r.Header.Get("X-Original-Host")
	switch {
	case strings.HasSuffix(r.URL.Path, "/token"):
		f.stsCalls++
		f.stsHost = host
		_ = r.ParseForm()
		f.stsForm = r.PostForm
		if f.stsStatus != 0 {
			w.WriteHeader(f.stsStatus)
			_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"sts-detail-that-must-not-leak"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"federated-token","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":3600}`)
	case strings.HasSuffix(r.URL.Path, ":generateAccessToken"):
		f.impersonation.calls++
		f.impersonation.host = host
		f.impersonation.path = r.URL.Path
		f.impersonation.auth = r.Header.Get("Authorization")
		var body struct {
			Scope []string `json:"scope"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.impersonation.scope = body.Scope
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"accessToken":"impersonated-token","expireTime":"`+time.Now().Add(time.Hour).UTC().Format(time.RFC3339)+`"}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// redirectTransport sends every request to target, recording the host it was
// meant for, so the configuration can keep Google's real URLs.
type redirectTransport struct{ target *url.URL }

func (t redirectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c := r.Clone(r.Context())
	c.Header.Set("X-Original-Host", r.URL.Host)
	c.URL.Scheme = t.target.Scheme
	c.URL.Host = t.target.Host
	c.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(c)
}

const (
	testAudience = "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/aegis/providers/eks"
	testSA       = "play-integrity@aegis-test.iam.gserviceaccount.com"
)

// credentialConfig is the shape `gcloud iam workload-identity-pools
// create-cred-config` produces for a Kubernetes projected token.
func credentialConfig(t *testing.T, subjectTokenFile string, impersonate bool) []byte {
	t.Helper()
	c := map[string]any{
		"type":               "external_account",
		"audience":           testAudience,
		"subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
		"token_url":          "https://sts.googleapis.com/v1/token",
		"credential_source":  map[string]any{"file": subjectTokenFile, "format": map[string]any{"type": "text"}},
	}
	if impersonate {
		c["service_account_impersonation_url"] = "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/" + testSA + ":generateAccessToken"
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newFakeGoogle(t *testing.T) (*fakeGoogle, *http.Client, string) {
	t.Helper()
	f := &fakeGoogle{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("projected-k8s-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f, &http.Client{Transport: redirectTransport{target}, Timeout: 5 * time.Second}, tokenFile
}

func TestWorkloadIdentity_ExchangesThenImpersonatesWithTheProjectedToken(t *testing.T) {
	f, client, tokenFile := newFakeGoogle(t)
	ts, err := NewWorkloadIdentityTokens(context.Background(), credentialConfig(t, tokenFile, true), client)
	if err != nil {
		t.Fatalf("NewWorkloadIdentityTokens: %v", err)
	}
	got, err := ts.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if got != "impersonated-token" {
		t.Fatalf("token = %q, want the impersonated service account's token", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stsHost != "sts.googleapis.com" || f.impersonation.host != "iamcredentials.googleapis.com" {
		t.Fatalf("hosts = %q, %q; want Google's STS and IAM Credentials", f.stsHost, f.impersonation.host)
	}
	if f.stsForm.Get("subject_token") != "projected-k8s-token" || f.stsForm.Get("audience") != testAudience ||
		f.stsForm.Get("subject_token_type") != "urn:ietf:params:oauth:token-type:jwt" ||
		f.stsForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:token-exchange" {
		t.Fatalf("STS form = %v; want the projected token exchanged for the provider's audience", f.stsForm)
	}
	if f.impersonation.auth != "Bearer federated-token" {
		t.Fatalf("impersonation authorised with %q, want the federated token", f.impersonation.auth)
	}
	if !strings.Contains(f.impersonation.path, testSA) {
		t.Fatalf("impersonation path %q does not name the dedicated service account", f.impersonation.path)
	}
	if len(f.impersonation.scope) != 1 || f.impersonation.scope[0] != Scope {
		t.Fatalf("impersonation scope = %v, want only %s", f.impersonation.scope, Scope)
	}
}

func TestWorkloadIdentity_CachesTheTokenUntilItNearsExpiry(t *testing.T) {
	f, client, tokenFile := newFakeGoogle(t)
	ts, err := NewWorkloadIdentityTokens(context.Background(), credentialConfig(t, tokenFile, true), client)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := ts.AccessToken(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stsCalls != 1 || f.impersonation.calls != 1 {
		t.Fatalf("exchanges = %d, impersonations = %d; want 1 each for a token valid for an hour", f.stsCalls, f.impersonation.calls)
	}
}

func TestWorkloadIdentity_RefusesAServiceAccountKey(t *testing.T) {
	// The type alone is refused. The key field is a placeholder, deliberately
	// not credential-shaped, so secret scanners have nothing to find.
	key := []byte(`{"type":"service_account","project_id":"p","private_key_id":"k","private_key":"placeholder-not-a-key-value","client_email":"sa@p.iam.gserviceaccount.com","token_uri":"https://oauth2.googleapis.com/token"}`)
	_, err := NewWorkloadIdentityTokens(context.Background(), key, nil)
	if !errors.Is(err, ErrInvalidCredentialConfig) {
		t.Fatalf("err = %v, want ErrInvalidCredentialConfig: ADR-0008 allows no static cloud credentials", err)
	}
	if strings.Contains(err.Error(), "placeholder-not-a-key-value") {
		t.Fatalf("the error repeats key material: %v", err)
	}
}

func TestWorkloadIdentity_RequiresImpersonation(t *testing.T) {
	_, _, tokenFile := newFakeGoogle(t)
	_, err := NewWorkloadIdentityTokens(context.Background(), credentialConfig(t, tokenFile, false), nil)
	if !errors.Is(err, ErrInvalidCredentialConfig) {
		t.Fatalf("err = %v, want ErrInvalidCredentialConfig for a configuration without impersonation", err)
	}
}

func TestWorkloadIdentity_RefusesWhatIsNotACredentialConfiguration(t *testing.T) {
	for _, bad := range []string{"", "not json", "{}", `{"type":"authorized_user"}`} {
		if _, err := NewWorkloadIdentityTokens(context.Background(), []byte(bad), nil); !errors.Is(err, ErrInvalidCredentialConfig) {
			t.Errorf("%q: err = %v, want ErrInvalidCredentialConfig", bad, err)
		}
	}
}

func TestWorkloadIdentity_AFailedExchangeIsAnErrorWithoutGooglesDetail(t *testing.T) {
	f, client, tokenFile := newFakeGoogle(t)
	f.stsStatus = http.StatusBadRequest
	ts, err := NewWorkloadIdentityTokens(context.Background(), credentialConfig(t, tokenFile, true), client)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ts.AccessToken(context.Background())
	if !errors.Is(err, ErrAccessToken) {
		t.Fatalf("err = %v, want ErrAccessToken (the decoder treats it as unavailable, never a refusal)", err)
	}
	if strings.Contains(err.Error(), "sts-detail-that-must-not-leak") {
		t.Fatalf("the error repeats Google's response: %v", err)
	}
}

func TestWorkloadIdentity_TheCallersDeadlineBoundsTheWait(t *testing.T) {
	_, client, tokenFile := newFakeGoogle(t)
	ts, err := NewWorkloadIdentityTokens(context.Background(), credentialConfig(t, tokenFile, true), client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ts.AccessToken(ctx); !errors.Is(err, ErrAccessToken) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want ErrAccessToken wrapping context.Canceled", err)
	}
}
