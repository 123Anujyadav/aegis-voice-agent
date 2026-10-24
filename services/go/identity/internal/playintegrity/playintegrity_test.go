package playintegrity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// Every test talks to a local fake of decodeIntegrityToken. Nothing here
// reaches Google, and no verdict is real.

const (
	pkg          = "com.example.callscreen"
	digest       = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" // 43 chars, base64url
	accessSecret = "ya29.ACCESS-TOKEN-NEVER-ECHOED"
	tokenSecret  = "INTEGRITY-TOKEN-NEVER-ECHOED"
)

type staticTokens struct {
	tok string
	err error
	n   atomic.Int32
}

func (s *staticTokens) AccessToken(context.Context) (string, error) {
	s.n.Add(1)
	return s.tok, s.err
}

// payload is a decoded verdict in the documented shape: timestampMillis as a
// JSON string, an unknown optional block, and a padded digest.
func payload(hash string, at time.Time) string {
	return `{"tokenPayloadExternal":{
		"requestDetails":{"requestPackageName":"` + pkg + `","requestHash":"` + hash + `","timestampMillis":"` + strconv.FormatInt(at.UnixMilli(), 10) + `"},
		"appIntegrity":{"appRecognitionVerdict":"PLAY_RECOGNIZED","packageName":"` + pkg + `","certificateSha256Digest":["` + digest + `="],"versionCode":"42"},
		"deviceIntegrity":{"deviceRecognitionVerdict":["MEETS_DEVICE_INTEGRITY"]},
		"accountDetails":{"appLicensingVerdict":"LICENSED"},
		"environmentDetails":{"appAccessRiskVerdict":{}}}}`
}

// fakeAPI serves status and body, recording the last request.
type fakeAPI struct {
	status int
	body   string
	delay  time.Duration

	method, path, auth, ctype string
	sent                      map[string]string
	calls                     atomic.Int32
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	f.method, f.path, f.auth, f.ctype = r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"), r.Header.Get("Content-Type")
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &f.sent)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-r.Context().Done():
			return
		}
	}
	w.WriteHeader(f.status)
	_, _ = io.WriteString(w, f.body)
}

func newDecoder(t *testing.T, api *fakeAPI, tokens TokenSource) *Decoder {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	if tokens == nil {
		tokens = &staticTokens{tok: accessSecret}
	}
	d, err := New(Config{PackageName: pkg, Tokens: tokens, Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// The request is the documented one, and the documented response maps onto
// every field Assess judges, with the digest normalised.
func TestDecode_SendsTheDocumentedRequestAndMapsTheVerdict(t *testing.T) {
	at := time.Now().Add(-3 * time.Second).Truncate(time.Millisecond)
	api := &fakeAPI{status: 200, body: payload("hash-1", at)}
	v, err := newDecoder(t, api, nil).Decode(context.Background(), tokenSecret)
	if err != nil {
		t.Fatal(err)
	}
	if api.method != http.MethodPost || api.path != "/v1/"+pkg+":decodeIntegrityToken" ||
		api.auth != "Bearer "+accessSecret || api.ctype != "application/json" || api.sent["integrity_token"] != tokenSecret || len(api.sent) != 1 {
		t.Fatalf("request = %s %s auth=%q type=%q body=%v", api.method, api.path, api.auth, api.ctype, api.sent)
	}
	want := app.IntegrityVerdict{
		RequestPackageName: pkg, RequestHash: "hash-1", Timestamp: at.UTC(),
		AppRecognition: "PLAY_RECOGNIZED", AppPackageName: pkg, CertificateDigests: []string{digest},
		DeviceRecognition: []string{"MEETS_DEVICE_INTEGRITY"}, AppLicensing: "LICENSED",
	}
	if !v.Timestamp.Equal(want.Timestamp) || v.RequestPackageName != want.RequestPackageName || v.RequestHash != want.RequestHash ||
		v.AppRecognition != want.AppRecognition || v.AppPackageName != want.AppPackageName ||
		len(v.CertificateDigests) != 1 || v.CertificateDigests[0] != digest ||
		len(v.DeviceRecognition) != 1 || v.DeviceRecognition[0] != "MEETS_DEVICE_INTEGRITY" || v.AppLicensing != "LICENSED" {
		t.Fatalf("verdict = %+v\nwant      %+v", v, want)
	}
}

// End to end offline: the adapter behind the service's port, judged by the
// real policy, certifies a genuine bound verdict and restricts one bound to
// another request.
func TestDecode_ThroughTheServiceAndPolicy(t *testing.T) {
	ctx := context.Background()
	api := &fakeAPI{status: 200, body: payload("bound-hash", time.Now().Add(-time.Second))}
	svc := app.NewIdentityService(app.NewMemoryIdentityRepository(), []byte("test_secret_key_that_is_long_enough"))
	if err := svc.SetIntegrityVerifier(newDecoder(t, api, nil), app.IntegrityPolicy{
		PackageName: pkg, CertificateDigests: []string{digest}, MaxAge: 2 * time.Minute, FutureSkew: 30 * time.Second, DecodeTimeout: time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	jkt := strings.Repeat("k", 43)
	a, err := svc.VerifyPlayIntegrity(ctx, tokenSecret, app.IntegrityBinding{DeviceKeyThumbprint: jkt, RequestHash: "bound-hash"})
	if err != nil || a.Tier != app.TierCertified {
		t.Fatalf("(%+v, %v), want certified", a, err)
	}
	a, err = svc.VerifyPlayIntegrity(ctx, tokenSecret, app.IntegrityBinding{DeviceKeyThumbprint: jkt, RequestHash: "another-hash"})
	if err != nil || a.Tier != app.TierRestricted || len(a.Reasons) != 1 || a.Reasons[0] != app.ReasonRequestHashMismatch {
		t.Fatalf("(%+v, %v), want restricted for request_hash_mismatch", a, err)
	}
}

// Only HTTP 400 is a rejected token. Our credentials being refused, quota and
// outages are unavailability: never a rejection the client is blamed for.
func TestDecode_StatusMapping(t *testing.T) {
	for status, rejected := range map[int]bool{400: true, 401: false, 403: false, 404: false, 429: false, 500: false, 503: false} {
		api := &fakeAPI{status: status, body: `{"error":{"message":"` + tokenSecret + ` SECRET-ECHO"}}`}
		_, err := newDecoder(t, api, nil).Decode(context.Background(), tokenSecret)
		if err == nil || errors.Is(err, app.ErrIntegrityTokenInvalid) != rejected {
			t.Errorf("HTTP %d: err = %v, want rejected=%v", status, err, rejected)
		}
		if err != nil && (strings.Contains(err.Error(), tokenSecret) || strings.Contains(err.Error(), "SECRET-ECHO") || strings.Contains(err.Error(), accessSecret)) {
			t.Errorf("HTTP %d: the error carries request or response data: %v", status, err)
		}
	}
}

// Without an access token nothing is sent.
func TestDecode_NoAccessTokenSendsNothing(t *testing.T) {
	for name, src := range map[string]*staticTokens{"error": {err: errors.New("metadata server down")}, "empty": {}} {
		api := &fakeAPI{status: 200, body: payload("h", time.Now())}
		_, err := newDecoder(t, api, src).Decode(context.Background(), tokenSecret)
		if err == nil || errors.Is(err, app.ErrIntegrityTokenInvalid) || api.calls.Load() != 0 {
			t.Errorf("%s: (%v, %d calls), want unavailable and no call", name, err, api.calls.Load())
		}
	}
}

// A 200 that is not a verdict is unavailable, never a pass; a numeric
// timestamp is accepted.
func TestDecode_ResponsesThatAreNotVerdicts(t *testing.T) {
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	for name, body := range map[string]string{
		"malformed":          `{"tokenPayloadExternal":`,
		"no payload":         `{}`,
		"no request details": `{"tokenPayloadExternal":{"appIntegrity":{"appRecognitionVerdict":"PLAY_RECOGNIZED"}}}`,
		"no timestamp":       `{"tokenPayloadExternal":{"requestDetails":{"requestHash":"h"}}}`,
		"bad timestamp":      `{"tokenPayloadExternal":{"requestDetails":{"requestHash":"h","timestampMillis":"soon"}}}`,
		"negative timestamp": `{"tokenPayloadExternal":{"requestDetails":{"requestHash":"h","timestampMillis":"-5"}}}`,
		"oversized":          payload("h", time.Now()) + strings.Repeat(" ", DefaultMaxResponseBytes), // valid JSON, but past the limit
	} {
		_, err := newDecoder(t, &fakeAPI{status: 200, body: body}, nil).Decode(context.Background(), tokenSecret)
		if err == nil || errors.Is(err, app.ErrIntegrityTokenInvalid) {
			t.Errorf("%s: err = %v, want unavailable", name, err)
		}
	}
	numeric := `{"tokenPayloadExternal":{"requestDetails":{"requestHash":"h","timestampMillis":` + now + `}}}`
	if _, err := newDecoder(t, &fakeAPI{status: 200, body: numeric}, nil).Decode(context.Background(), tokenSecret); err != nil {
		t.Fatalf("a numeric timestamp was refused: %v", err)
	}
}

// The caller's deadline bounds the call, and running out is unavailability.
func TestDecode_TheCallersDeadlineBoundsTheCall(t *testing.T) {
	api := &fakeAPI{status: 200, body: payload("h", time.Now()), delay: 5 * time.Second}
	d := newDecoder(t, api, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := d.Decode(ctx, tokenSecret)
	if err == nil || errors.Is(err, app.ErrIntegrityTokenInvalid) || time.Since(start) > 2*time.Second {
		t.Fatalf("(%v after %s), want a prompt unavailable", err, time.Since(start))
	}
}

func TestNew_RefusesUnsafeConfiguration(t *testing.T) {
	tokens := &staticTokens{tok: "t"}
	for name, cfg := range map[string]Config{
		"no package":           {Tokens: tokens},
		"bad package":          {PackageName: "callscreen", Tokens: tokens},
		"package with a slash": {PackageName: "com.example/../x", Tokens: tokens},
		"no token source":      {PackageName: pkg},
		"plain http":           {PackageName: pkg, Tokens: tokens, Endpoint: "http://playintegrity.googleapis.com"},
		"query in endpoint":    {PackageName: pkg, Tokens: tokens, Endpoint: "https://example.com/?x=1"},
		"credentials in URL":   {PackageName: pkg, Tokens: tokens, Endpoint: "https://u:p@example.com"},
		"not a URL":            {PackageName: pkg, Tokens: tokens, Endpoint: "::"},
		"negative max":         {PackageName: pkg, Tokens: tokens, MaxResponseBytes: -1},
	} {
		if _, err := New(cfg); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidConfig", name, err)
		}
	}
	d, err := New(Config{PackageName: pkg, Tokens: tokens})
	if err != nil || d.url != DefaultEndpoint+"/v1/"+pkg+":decodeIntegrityToken" {
		t.Fatalf("default endpoint: (%v, %v)", d, err)
	}
}

// A redirect is never followed: the tokens go nowhere but the configured
// endpoint, and a 3xx is unavailability.
func TestDecode_RedirectsAreNeverFollowed(t *testing.T) {
	elsewhere := &fakeAPI{status: 200, body: payload("h", time.Now())}
	other := httptest.NewServer(elsewhere)
	t.Cleanup(other.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	d, err := New(Config{PackageName: pkg, Tokens: &staticTokens{tok: accessSecret}, Endpoint: redirect.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Decode(context.Background(), tokenSecret); err == nil || errors.Is(err, app.ErrIntegrityTokenInvalid) {
		t.Fatalf("err = %v, want unavailable", err)
	}
	if elsewhere.calls.Load() != 0 {
		t.Fatal("the redirect was followed: the tokens reached another server")
	}
}
