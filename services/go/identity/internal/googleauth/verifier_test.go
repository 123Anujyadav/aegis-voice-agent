package googleauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/pgstore"
)

// The Google ID-token verifier against a LOCAL fake Google key server. No
// test contacts Google (TestMain's dead proxy stands guard).

const (
	serverID  = "123-server.apps.googleusercontent.com"
	androidID = "123-android.apps.googleusercontent.com"
	nonce     = "expected-nonce-value"
)

var (
	keysOnce sync.Once
	rsaKeys  []*rsa.PrivateKey
)

// testKeys returns three 2048-bit test keys, made once per package run.
func testKeys(t *testing.T) []*rsa.PrivateKey {
	t.Helper()
	keysOnce.Do(func() {
		for i := 0; i < 3; i++ {
			k, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				panic(err)
			}
			rsaKeys = append(rsaKeys, k)
		}
	})
	return rsaKeys
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeGoogle serves a JWK set, with knobs for every failure.
type fakeGoogle struct {
	mu           sync.Mutex
	keys         []jose.JSONWebKey
	status       int
	contentType  string
	cacheControl string
	body         string // overrides the JWK set when set
	redirect     string
	delay        time.Duration
	hits         atomic.Int64
	srv          *httptest.Server
}

func newFakeGoogle(t *testing.T, kids ...string) *fakeGoogle {
	t.Helper()
	f := &fakeGoogle{status: http.StatusOK, contentType: "application/json; charset=UTF-8", cacheControl: "public, max-age=300"}
	for i, kid := range kids {
		f.keys = append(f.keys, jose.JSONWebKey{Key: &testKeys(t)[i].PublicKey, KeyID: kid, Algorithm: "RS256", Use: "sig"})
	}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		f.mu.Lock()
		status, ct, cc, body, redirect, delay := f.status, f.contentType, f.cacheControl, f.body, f.redirect, f.delay
		set := jose.JSONWebKeySet{Keys: append([]jose.JSONWebKey(nil), f.keys...)}
		f.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		if redirect != "" {
			http.Redirect(w, r, redirect, http.StatusFound)
			return
		}
		if body == "" {
			b, _ := json.Marshal(set) //nolint:errcheck // test fixture
			body = string(b)
		}
		w.Header().Set("Content-Type", ct)
		if cc != "" {
			w.Header().Set("Cache-Control", cc)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body)) //nolint:errcheck // test server
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGoogle) set(fn func(f *fakeGoogle)) { f.mu.Lock(); fn(f); f.mu.Unlock() }

type rig struct {
	v     *Verifier
	g     *fakeGoogle
	clock *clock
	seen  []AuthorizedPartyKind
	mu    sync.Mutex
}

func newRig(t *testing.T, kids ...string) *rig {
	t.Helper()
	if len(kids) == 0 {
		kids = []string{"k1"}
	}
	r := &rig{g: newFakeGoogle(t, kids...), clock: &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}}
	v, err := New(Config{
		ServerClientID: serverID, AndroidClientIDs: []string{androidID}, Now: r.clock.now,
		KeysURL: r.g.srv.URL + "/oauth2/v3/certs", HTTPClient: r.g.srv.Client(),
		ObserveAuthorizedParty: func(k AuthorizedPartyKind) { r.mu.Lock(); r.seen = append(r.seen, k); r.mu.Unlock() },
	})
	if err != nil {
		t.Fatal(err)
	}
	r.v = v
	return r
}

// claims are a valid token's claims at the rig's time.
func (r *rig) claims() map[string]any {
	now := r.clock.now()
	return map[string]any{
		"iss": app.GoogleIssuer, "aud": serverID, "azp": androidID, "sub": "110169484474386276334",
		"email": "tester@example.com", "email_verified": true, "nonce": nonce,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
}

// sign makes a compact JWS of claims with test key i under kid.
func sign(t *testing.T, i int, kid string, claims map[string]any) string {
	t.Helper()
	opts := (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid)
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: testKeys(t)[i]}, opts)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(claims) //nolint:errcheck // test fixture
	obj, err := s.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	out, err := obj.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (r *rig) verify(t *testing.T, raw string) (*app.GoogleIdentity, error) {
	t.Helper()
	return r.v.VerifyGoogleIDToken(context.Background(), raw, nonce)
}

func with(c map[string]any, k string, v any) map[string]any {
	out := map[string]any{}
	for key, val := range c {
		out[key] = val
	}
	if v == nil {
		delete(out, k)
	} else {
		out[k] = v
	}
	return out
}

func TestVerify_AValidToken(t *testing.T) {
	r := newRig(t)
	id, err := r.verify(t, sign(t, 0, "k1", r.claims()))
	if err != nil {
		t.Fatalf("a valid token: %v", err)
	}
	if id.Issuer != app.GoogleIssuer || id.Subject != "110169484474386276334" || id.Email != "tester@example.com" ||
		!id.EmailVerified || !id.ExpiresAt.Equal(r.clock.now().Add(time.Hour).Truncate(time.Second)) {
		t.Errorf("identity = %+v", id)
	}
	// The other spelling of the issuer is normalised.
	if id, err := r.verify(t, sign(t, 0, "k1", with(r.claims(), "iss", "accounts.google.com"))); err != nil || id.Issuer != app.GoogleIssuer {
		t.Errorf("accounts.google.com: (%+v, %v)", id, err)
	}
	// aud as an array containing the server client id.
	if _, err := r.verify(t, sign(t, 0, "k1", with(r.claims(), "aud", []string{serverID, "other.apps.googleusercontent.com"}))); err != nil {
		t.Errorf("aud array: %v", err)
	}
}

func TestVerify_AuthorizedParty(t *testing.T) {
	r := newRig(t)
	for name, azp := range map[string]any{"android": androidID, "server": serverID} {
		if _, err := r.verify(t, sign(t, 0, "k1", with(r.claims(), "azp", azp))); err != nil {
			t.Errorf("%s azp: %v", name, err)
		}
	}
	if _, err := r.verify(t, sign(t, 0, "k1", with(r.claims(), "azp", nil))); err != nil {
		t.Errorf("absent azp: %v", err)
	}
	if _, err := r.verify(t, sign(t, 0, "k1", with(r.claims(), "azp", "999-other.apps.googleusercontent.com"))); !errors.Is(err, app.ErrGoogleSignInRefused) {
		t.Errorf("another azp: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	got := map[AuthorizedPartyKind]int{}
	for _, k := range r.seen {
		got[k]++
	}
	if got[AuthorizedPartyAndroid] != 1 || got[AuthorizedPartyServer] != 1 || got[AuthorizedPartyAbsent] != 1 || len(r.seen) != 3 {
		t.Errorf("observed kinds = %v", r.seen)
	}
}

func TestVerify_RefusesBadTokens(t *testing.T) {
	r := newRig(t)
	now := r.clock.now()
	c := r.claims()
	tampered := func() string {
		tok := sign(t, 0, "k1", c)
		parts := strings.Split(tok, ".")
		body, _ := json.Marshal(with(c, "sub", "attacker")) //nolint:errcheck // fixture
		return parts[0] + "." + base64.RawURLEncoding.EncodeToString(body) + "." + parts[2]
	}()
	hdrNone := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","kid":"k1"}`))
	payload, _ := json.Marshal(c) //nolint:errcheck // fixture
	none := hdrNone + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
	hs, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("public-key-used-as-a-secret-xxxx")}, //nolint:errcheck // fixture
		(&jose.SignerOptions{}).WithHeader("kid", "k1"))
	hsObj, _ := hs.Sign(payload)                                                            //nolint:errcheck // fixture
	hsTok, _ := hsObj.CompactSerialize()                                                    //nolint:errcheck // fixture
	rs512, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS512, Key: testKeys(t)[0]}, //nolint:errcheck // fixture
		(&jose.SignerOptions{}).WithHeader("kid", "k1"))
	rsObj, _ := rs512.Sign(payload)      //nolint:errcheck // fixture
	rsTok, _ := rsObj.CompactSerialize() //nolint:errcheck // fixture

	for name, tok := range map[string]string{
		"tampered payload":      tampered,
		"alg none":              none,
		"HS256":                 hsTok,
		"RS512":                 rsTok,
		"signed by another key": sign(t, 1, "k1", c),
		"wrong issuer":          sign(t, 0, "k1", with(c, "iss", "https://evil.example")),
		"wrong audience":        sign(t, 0, "k1", with(c, "aud", "999-other.apps.googleusercontent.com")),
		"no audience":           sign(t, 0, "k1", with(c, "aud", nil)),
		"expired beyond skew":   sign(t, 0, "k1", with(with(c, "exp", now.Add(-MaxClockSkew-time.Second).Unix()), "iat", now.Add(-time.Hour).Unix())),
		"issued in the future":  sign(t, 0, "k1", with(with(c, "iat", now.Add(MaxClockSkew+time.Second).Unix()), "exp", now.Add(2*time.Hour).Unix())),
		"exp before iat":        sign(t, 0, "k1", with(with(c, "iat", now.Unix()), "exp", now.Add(-time.Second).Unix())),
		"lifetime too long":     sign(t, 0, "k1", with(c, "exp", now.Add(MaxTokenLifetime+time.Minute).Unix())),
		"no exp":                sign(t, 0, "k1", with(c, "exp", nil)),
		"no iat":                sign(t, 0, "k1", with(c, "iat", nil)),
		"no subject":            sign(t, 0, "k1", with(c, "sub", nil)),
		"subject too long":      sign(t, 0, "k1", with(c, "sub", strings.Repeat("1", 256))),
		"subject with a space":  sign(t, 0, "k1", with(c, "sub", "1 2")),
		"wrong nonce":           sign(t, 0, "k1", with(c, "nonce", "another")),
		"no nonce":              sign(t, 0, "k1", with(c, "nonce", nil)),
		"not a JWS":             "a.b",
		"empty":                 "",
		"oversized":             strings.Repeat("a", MaxIDTokenBytes+1),
	} {
		if _, err := r.verify(t, tok); !errors.Is(err, app.ErrGoogleSignInRefused) {
			t.Errorf("%s: err = %v, want refused", name, err)
		}
	}
	// Within the skew is accepted.
	if _, err := r.verify(t, sign(t, 0, "k1", with(with(c, "exp", now.Add(-30*time.Second).Unix()), "iat", now.Add(-time.Hour).Unix()))); err != nil {
		t.Errorf("expired within skew: %v", err)
	}
}

// email_verified counts only as the JSON boolean true.
func TestVerify_EmailVerifiedMustBeABooleanTrue(t *testing.T) {
	r := newRig(t)
	for name, v := range map[string]any{"false": false, "string true": "true", "null": json.RawMessage("null"), "number": 1} {
		id, err := r.verify(t, sign(t, 0, "k1", with(r.claims(), "email_verified", v)))
		if err != nil || id.EmailVerified {
			t.Errorf("%s: (%+v, %v), want a token with EmailVerified false", name, id, err)
		}
	}
	if id, err := r.verify(t, sign(t, 0, "k1", with(r.claims(), "email_verified", nil))); err != nil || id.EmailVerified {
		t.Errorf("missing: (%+v, %v)", id, err)
	}
}

func TestKeys_CacheHonoursCacheControlUpTo24Hours(t *testing.T) {
	for _, tc := range []struct {
		cc      string
		reuse   time.Duration // a later call within this is not fetched
		refetch time.Duration // a later call after this is fetched
	}{
		{"public, max-age=300", 299 * time.Second, 301 * time.Second},
		{"max-age=30", 29 * time.Second, 31 * time.Second}, // a short TTL is not raised to 5 minutes
		{"max-age=999999", 23 * time.Hour, 24*time.Hour + time.Second},
	} {
		r := newRig(t)
		r.g.set(func(f *fakeGoogle) { f.cacheControl = tc.cc })
		if _, err := r.verify(t, sign(t, 0, "k1", r.claims())); err != nil {
			t.Fatal(err)
		}
		r.clock.advance(tc.reuse)
		if _, err := r.verify(t, sign(t, 0, "k1", r.claims())); err != nil {
			t.Fatal(err)
		}
		if n := r.g.hits.Load(); n != 1 {
			t.Errorf("%q: %d fetches within the TTL, want 1", tc.cc, n)
		}
		r.clock.advance(tc.refetch - tc.reuse)
		if _, err := r.verify(t, sign(t, 0, "k1", r.claims())); err != nil {
			t.Fatal(err)
		}
		if n := r.g.hits.Load(); n != 2 {
			t.Errorf("%q: %d fetches after the TTL, want 2", tc.cc, n)
		}
	}
	// No caching at all: missing Cache-Control, no-store, no-cache, max-age=0.
	for _, cc := range []string{"", "no-store", "no-cache, max-age=300", "max-age=0", "max-age=abc"} {
		r := newRig(t)
		r.g.set(func(f *fakeGoogle) { f.cacheControl = cc })
		for i := 0; i < 2; i++ {
			if _, err := r.verify(t, sign(t, 0, "k1", r.claims())); err != nil {
				t.Fatal(err)
			}
		}
		if n := r.g.hits.Load(); n != 2 {
			t.Errorf("%q: %d fetches for 2 verifications, want 2 (no caching)", cc, n)
		}
	}
}

func TestKeys_UnknownKeyIDsRefetchAtMostOncePerInterval(t *testing.T) {
	r := newRig(t, "k1", "k2")
	if _, err := r.verify(t, sign(t, 0, "k1", r.claims())); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := r.verify(t, sign(t, 2, "k9", r.claims())); !errors.Is(err, app.ErrGoogleSignInRefused) {
			t.Errorf("unknown kid: %v", err)
		}
	}
	if n := r.g.hits.Load(); n != 1 {
		t.Errorf("%d fetches: unknown key ids within the interval must not refetch", n)
	}
	r.clock.advance(UnknownKeyRefetchInterval + time.Second)
	r.g.set(func(f *fakeGoogle) {
		f.keys = append(f.keys, jose.JSONWebKey{Key: &testKeys(t)[2].PublicKey, KeyID: "k9", Algorithm: "RS256", Use: "sig"})
	})
	if _, err := r.verify(t, sign(t, 2, "k9", r.claims())); err != nil {
		t.Errorf("a rotated-in key after the interval: %v", err)
	}
	if n := r.g.hits.Load(); n != 2 {
		t.Errorf("%d fetches, want 2", n)
	}
}

func TestKeys_ConcurrentColdVerificationsFetchOnce(t *testing.T) {
	r := newRig(t)
	tok := sign(t, 0, "k1", r.claims())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.verify(t, tok); err != nil {
				t.Errorf("verify: %v", err)
			}
		}()
	}
	wg.Wait()
	if n := r.g.hits.Load(); n != 1 {
		t.Errorf("%d fetches for 20 concurrent cold verifications, want 1", n)
	}
}

// No usable key is unavailable (503), never a stale key and never a refusal.
func TestKeys_FailClosedWhenNoUsableKey(t *testing.T) {
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(f *fakeGoogle){
		"server error": func(f *fakeGoogle) { f.status = http.StatusInternalServerError },
		"not JSON":     func(f *fakeGoogle) { f.contentType = "text/html" },
		"malformed":    func(f *fakeGoogle) { f.body = "{not json" },
		"too large":    func(f *fakeGoogle) { f.body = `{"keys":[],"pad":"` + strings.Repeat("a", MaxKeySetBytes) + `"}` },
		"redirect":     func(f *fakeGoogle) { f.redirect = "https://127.0.0.1:1/elsewhere" },
		"no usable keys": func(f *fakeGoogle) {
			f.keys = []jose.JSONWebKey{{Key: &weak.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}
		},
		"encryption key": func(f *fakeGoogle) { f.keys[0].Use = "enc" },
		"duplicate kid":  func(f *fakeGoogle) { f.keys = append(f.keys, f.keys[0]) },
	}
	for name, breakIt := range cases {
		r := newRig(t)
		r.g.set(breakIt)
		if _, err := r.verify(t, sign(t, 0, "k1", r.claims())); !errors.Is(err, app.ErrGoogleSignInUnavailable) {
			t.Errorf("%s: err = %v, want unavailable", name, err)
		}
	}
	// Expired keys are not used when the refetch fails.
	r := newRig(t)
	if _, err := r.verify(t, sign(t, 0, "k1", r.claims())); err != nil {
		t.Fatal(err)
	}
	r.clock.advance(301 * time.Second)
	r.g.set(func(f *fakeGoogle) { f.status = http.StatusServiceUnavailable })
	if _, err := r.verify(t, sign(t, 0, "k1", r.claims())); !errors.Is(err, app.ErrGoogleSignInUnavailable) {
		t.Errorf("expired keys, failed refetch: %v, want unavailable", err)
	}
}

func TestKeys_FetchIsBoundedInTime(t *testing.T) {
	r := newRig(t)
	r.g.set(func(f *fakeGoogle) { f.delay = time.Minute })
	client := r.g.srv.Client()
	client.Timeout = 300 * time.Millisecond
	v, err := New(Config{
		ServerClientID: serverID, AndroidClientIDs: []string{androidID}, Now: r.clock.now,
		KeysURL: r.g.srv.URL, HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := v.VerifyGoogleIDToken(context.Background(), sign(t, 0, "k1", r.claims()), nonce); !errors.Is(err, app.ErrGoogleSignInUnavailable) {
		t.Errorf("a hanging key server: %v, want unavailable", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v", d)
	}
	if supplied := guardedClient(&http.Client{Timeout: time.Hour}); supplied.Timeout != FetchTimeout {
		t.Errorf("a supplied client's timeout is not bounded: %v", supplied.Timeout)
	}
}

func TestNew_RefusesBadConfiguration(t *testing.T) {
	for name, cfg := range map[string]Config{
		"bad server id":      {ServerClientID: "server", AndroidClientIDs: []string{androidID}},
		"no android id":      {ServerClientID: serverID},
		"bad android id":     {ServerClientID: serverID, AndroidClientIDs: []string{"x.apps.googleusercontent.com.evil"}},
		"http key URL":       {ServerClientID: serverID, AndroidClientIDs: []string{androidID}, KeysURL: "http://127.0.0.1/"},
		"foreign key host":   {ServerClientID: serverID, AndroidClientIDs: []string{androidID}, KeysURL: "https://evil.example/certs"},
		"credentials in URL": {ServerClientID: serverID, AndroidClientIDs: []string{androidID}, KeysURL: "https://u:p@127.0.0.1/"},
	} {
		if _, err := New(cfg); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	v, err := New(Config{ServerClientID: serverID, AndroidClientIDs: []string{androidID}})
	if err != nil || v.keys.url != KeysURL {
		t.Errorf("default key URL: (%v, %v)", v, err)
	}
}

// The single-use record of a token outlives its expiry by far more than the
// verifier's clock skew, on both backends.
func TestSkewIsWellWithinTheReplayRetention(t *testing.T) {
	if MaxClockSkew*10 > app.GoogleTokenRecordMargin || MaxClockSkew*10 > pgstore.DefaultHousekeepingMargin {
		t.Errorf("skew %v against retention %v and %v", MaxClockSkew, app.GoogleTokenRecordMargin, pgstore.DefaultHousekeepingMargin)
	}
}
