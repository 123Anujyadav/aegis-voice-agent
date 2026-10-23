package auth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/packages/go/middleware"
)

// Test-only values; none is, or may become, a production default.
const (
	issuer   = "urn:callscreen:identity:test"
	audience = "urn:callscreen:edge-api:test"
	kid      = "test-key-1"
	origin   = "https://edge.example.test"
	path     = "/v1/calls"
)

var now0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type rig struct {
	h       http.Handler
	a       *Authenticator
	signer  *accesstoken.Signer
	priv    ed25519.PrivateKey
	device  *ecdsa.PrivateKey
	clock   *clock
	replay  *dpop.MemoryReplayStore
	reached atomic.Int32
	seen    Principal
	logs    *syncBuf
	bodies  []string
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func publicKeyFile(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return writeFile(t, "issuer.pub.pem", pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func validConfig(t *testing.T, pub ed25519.PublicKey) Config {
	t.Helper()
	return Config{
		PublicKeyFile: publicKeyFile(t, pub), KeyID: kid, Issuer: issuer, Audience: audience, PublicOrigin: origin,
		// Parseable, credential-free, and never dialled by New.
		DatabaseDSNFile: writeFile(t, "dsn", []byte("postgres://edge@127.0.0.1:1/edge?connect_timeout=2\n")),
	}
}

func signerFor(t *testing.T, priv ed25519.PrivateKey, kid, iss, aud string, c *clock) *accesstoken.Signer {
	t.Helper()
	s, err := accesstoken.NewSigner(accesstoken.SignerConfig{Issuer: iss, Audience: aud, KeyID: kid, Key: priv, Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newRig(t *testing.T) *rig {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	device, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	r := &rig{priv: priv, device: device, clock: &clock{t: now0}, replay: dpop.NewMemoryReplayStore(), logs: &syncBuf{}}
	r.signer = signerFor(t, priv, kid, issuer, audience, r.clock)
	a, err := New(validConfig(t, pub), r.replay, r.clock.now, slog.New(slog.NewTextHandler(r.logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	protected := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.reached.Add(1)
		r.seen, _ = PrincipalFrom(req.Context())
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("protected business logic ran"))
	})
	r.a = a
	r.h = middleware.CorrelationIDMiddleware(a.Middleware(protected))
	return r
}

func (r *rig) token(t *testing.T) string {
	t.Helper()
	jkt, _ := accesstoken.ThumbprintP256(&r.device.PublicKey)
	tok, err := r.signer.Issue("usr_test", []string{"subscriber"}, jkt)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// proofFor builds an ES256 proof; claims overrides replace or delete ("<nil>")
// members of the standard claim set.
func proofFor(t *testing.T, signer, named *ecdsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	pt, _ := named.PublicKey.Bytes()
	hdr, _ := json.Marshal(map[string]any{
		"typ": "dpop+jwt", "alg": "ES256",
		"jwk": map[string]any{"kty": "EC", "crv": "P-256", "x": b64(pt[1:33]), "y": b64(pt[33:65])},
	})
	cl, _ := json.Marshal(claims)
	input := b64(hdr) + "." + b64(cl)
	d := sha256.Sum256([]byte(input))
	rr, ss, err := ecdsa.Sign(rand.Reader, signer, d[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	rr.FillBytes(sig[:32])
	ss.FillBytes(sig[32:])
	return input + "." + b64(sig)
}

var jtiN atomic.Int64

func claimsFor(r *rig, method, htu, token string) map[string]any {
	return map[string]any{
		"jti": "j" + strconv.FormatInt(jtiN.Add(1), 10),
		"htm": method, "htu": htu, "iat": r.clock.now().Unix(), "ath": dpop.AccessTokenHash(token),
	}
}

func (r *rig) goodProof(t *testing.T, method, token string) string {
	return proofFor(t, r.device, r.device, claimsFor(r, method, origin+path, token))
}

type resp struct {
	code int
	hdr  http.Header
	body map[string]any
	raw  string
}

func (r *rig) do(t *testing.T, method, target string, hdr map[string][]string) resp {
	t.Helper()
	req := httptest.NewRequest(method, target, http.NoBody)
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	out := resp{code: rec.Code, hdr: rec.Header(), raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	r.bodies = append(r.bodies, out.raw)
	return out
}

func auth(token, proof string) map[string][]string {
	h := map[string][]string{"Authorization": {"DPoP " + token}}
	if proof != "" {
		h["DPoP"] = []string{proof}
	}
	return h
}

func wantRefused(t *testing.T, r *rig, got resp, status int, code, dpopErr string) {
	t.Helper()
	if got.code != status || got.body["code"] != code {
		t.Fatalf("got %d %s, want %d %s", got.code, got.raw, status, code)
	}
	if want := problems[code]; got.body["detail"] != want.detail {
		t.Errorf("detail %q is not the fixed text", got.body["detail"])
	}
	if status == http.StatusUnauthorized {
		wa := got.hdr.Get("WWW-Authenticate")
		if !strings.HasPrefix(wa, `DPoP algs="ES256"`) || (dpopErr != "" && !strings.Contains(wa, `error="`+dpopErr+`"`)) || (dpopErr == "" && strings.Contains(wa, "error=")) {
			t.Errorf("WWW-Authenticate = %q, want error %q", wa, dpopErr)
		}
	}
	if got.hdr.Get("Cache-Control") != "no-store" || got.body["traceId"] != got.hdr.Get("X-Correlation-ID") {
		t.Errorf("headers: %v body: %s", got.hdr, got.raw)
	}
	if strings.Contains(got.raw, "protected business logic ran") || r.reached.Load() != 0 {
		t.Fatal("the protected handler ran for a refused request")
	}
}

// ---------------------------------------------------------------------------

func TestAuth_ValidTokenAndBoundProof(t *testing.T) {
	r := newRig(t)
	tok := r.token(t)
	got := r.do(t, "GET", path, auth(tok, r.goodProof(t, "GET", tok)))
	if got.code != 200 || r.reached.Load() != 1 {
		t.Fatalf("%d %s", got.code, got.raw)
	}
	jkt, _ := accesstoken.ThumbprintP256(&r.device.PublicKey)
	if r.seen.Subject != "usr_test" || r.seen.DeviceKeyThumbprint != jkt || len(r.seen.Roles) != 1 || r.seen.TokenID == "" {
		t.Errorf("principal = %+v", r.seen)
	}
	// The scheme is case-insensitive; the query is not part of htu.
	r.reached.Store(0)
	got = r.do(t, "GET", path+"?page=2", map[string][]string{"Authorization": {"dpop " + tok}, "DPoP": {r.goodProof(t, "GET", tok)}})
	if got.code != 200 || r.reached.Load() != 1 {
		t.Errorf("lower-case scheme with a query: %d %s", got.code, got.raw)
	}
}

func TestAuth_TokenFailures(t *testing.T) {
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	for name, mk := range map[string]func(t *testing.T, r *rig) string{
		"signed by another key": func(t *testing.T, r *rig) string {
			jkt, _ := accesstoken.ThumbprintP256(&r.device.PublicKey)
			tok, _ := signerFor(t, otherPriv, kid, issuer, audience, r.clock).Issue("usr_test", nil, jkt)
			return tok
		},
		"unknown key id": func(t *testing.T, r *rig) string {
			jkt, _ := accesstoken.ThumbprintP256(&r.device.PublicKey)
			tok, _ := signerFor(t, r.priv, "test-key-2", issuer, audience, r.clock).Issue("usr_test", nil, jkt)
			return tok
		},
		"wrong issuer": func(t *testing.T, r *rig) string {
			jkt, _ := accesstoken.ThumbprintP256(&r.device.PublicKey)
			tok, _ := signerFor(t, r.priv, kid, "urn:callscreen:identity:other", audience, r.clock).Issue("usr_test", nil, jkt)
			return tok
		},
		"wrong audience": func(t *testing.T, r *rig) string {
			jkt, _ := accesstoken.ThumbprintP256(&r.device.PublicKey)
			tok, _ := signerFor(t, r.priv, kid, issuer, "urn:callscreen:other:test", r.clock).Issue("usr_test", nil, jkt)
			return tok
		},
		"expired": func(t *testing.T, r *rig) string {
			tok := r.token(t)
			r.clock.add(accesstoken.Lifetime + accesstoken.DefaultClockSkew + time.Second)
			return tok
		},
		"tampered payload": func(t *testing.T, r *rig) string {
			parts := strings.Split(r.token(t), ".")
			parts[1] = b64([]byte(`{"iss":"x"}`))
			return strings.Join(parts, ".")
		},
		"unsigned (alg none)": func(t *testing.T, r *rig) string {
			parts := strings.Split(r.token(t), ".")
			return b64([]byte(`{"alg":"none","typ":"at+jwt","kid":"test-key-1"}`)) + "." + parts[1] + "."
		},
		"two segments": func(t *testing.T, r *rig) string { return strings.Join(strings.Split(r.token(t), ".")[:2], ".") },
		"legacy Bearer_JWT format": func(t *testing.T, r *rig) string {
			return "Bearer_JWT." + hex.EncodeToString([]byte(`{"sub":"usr_test","roles":["admin"]}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			tok := mk(t, r)
			wantRefused(t, r, r.do(t, "GET", path, auth(tok, r.goodProof(t, "GET", tok))), 401, "ERR_INVALID_TOKEN", "invalid_token")
			if r.replay.Len() != 0 {
				t.Error("a request with an invalid token reserved its proof's jti")
			}
		})
	}
}

func TestAuth_ProofFailures(t *testing.T) {
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	for name, mk := range map[string]func(t *testing.T, r *rig, tok string) map[string][]string{
		"missing proof": func(_ *testing.T, _ *rig, tok string) map[string][]string { return auth(tok, "") },
		"two proofs": func(t *testing.T, r *rig, tok string) map[string][]string {
			h := auth(tok, r.goodProof(t, "GET", tok))
			h["DPoP"] = append(h["DPoP"], r.goodProof(t, "GET", tok))
			return h
		},
		"oversized proof": func(_ *testing.T, _ *rig, tok string) map[string][]string {
			return auth(tok, strings.Repeat("a", dpop.MaxProofBytes+1))
		},
		"malformed proof": func(_ *testing.T, _ *rig, tok string) map[string][]string { return auth(tok, "not.a.proof") },
		"missing ath": func(t *testing.T, r *rig, tok string) map[string][]string {
			c := claimsFor(r, "GET", origin+path, tok)
			delete(c, "ath")
			return auth(tok, proofFor(t, r.device, r.device, c))
		},
		"ath of another token": func(t *testing.T, r *rig, tok string) map[string][]string {
			c := claimsFor(r, "GET", origin+path, tok)
			c["ath"] = dpop.AccessTokenHash(r.token(t))
			return auth(tok, proofFor(t, r.device, r.device, c))
		},
		"wrong method": func(t *testing.T, r *rig, tok string) map[string][]string {
			return auth(tok, proofFor(t, r.device, r.device, claimsFor(r, "POST", origin+path, tok)))
		},
		"wrong path": func(t *testing.T, r *rig, tok string) map[string][]string {
			return auth(tok, proofFor(t, r.device, r.device, claimsFor(r, "GET", origin+"/v1/other", tok)))
		},
		"another origin (as a Host header would claim)": func(t *testing.T, r *rig, tok string) map[string][]string {
			return auth(tok, proofFor(t, r.device, r.device, claimsFor(r, "GET", "https://evil.example.test"+path, tok)))
		},
		"htu with the query": func(t *testing.T, r *rig, tok string) map[string][]string {
			return auth(tok, proofFor(t, r.device, r.device, claimsFor(r, "GET", origin+path+"?x=1", tok)))
		},
		"another device's key": func(t *testing.T, r *rig, tok string) map[string][]string {
			return auth(tok, proofFor(t, other, other, claimsFor(r, "GET", origin+path, tok)))
		},
		"invalid signature": func(t *testing.T, r *rig, tok string) map[string][]string {
			return auth(tok, proofFor(t, other, r.device, claimsFor(r, "GET", origin+path, tok)))
		},
		"stale": func(t *testing.T, r *rig, tok string) map[string][]string {
			c := claimsFor(r, "GET", origin+path, tok)
			c["iat"] = r.clock.now().Add(-ProofMaxAge - time.Second).Unix()
			return auth(tok, proofFor(t, r.device, r.device, c))
		},
		"from the future": func(t *testing.T, r *rig, tok string) map[string][]string {
			c := claimsFor(r, "GET", origin+path, tok)
			c["iat"] = r.clock.now().Add(ProofFutureSkew + time.Second).Unix()
			return auth(tok, proofFor(t, r.device, r.device, c))
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			tok := r.token(t)
			wantRefused(t, r, r.do(t, "GET", path+"?x=1", mk(t, r, tok)), 401, "ERR_INVALID_DPOP_PROOF", "invalid_dpop_proof")
		})
	}

	t.Run("replayed", func(t *testing.T) {
		r := newRig(t)
		tok := r.token(t)
		p := r.goodProof(t, "GET", tok)
		if got := r.do(t, "GET", path, auth(tok, p)); got.code != 200 {
			t.Fatal(got.raw)
		}
		r.reached.Store(0)
		wantRefused(t, r, r.do(t, "GET", path, auth(tok, p)), 401, "ERR_INVALID_DPOP_PROOF", "invalid_dpop_proof")
	})

	t.Run("another device's key reserves nothing", func(t *testing.T) {
		r := newRig(t)
		tok := r.token(t)
		_ = r.do(t, "GET", path, auth(tok, proofFor(t, other, other, claimsFor(r, "GET", origin+path, tok))))
		if r.replay.Len() != 0 {
			t.Error("a proof by a key the token is not bound to reserved its jti")
		}
	})
}

func TestAuth_AuthorizationHeaderFailures(t *testing.T) {
	for name, hdr := range map[string]func(r *rig, tok, p string) map[string][]string{
		"missing": func(_ *rig, _, p string) map[string][]string { return map[string][]string{"DPoP": {p}} },
		"Bearer scheme": func(_ *rig, tok, p string) map[string][]string {
			return map[string][]string{"Authorization": {"Bearer " + tok}, "DPoP": {p}}
		},
		"no scheme": func(_ *rig, tok, p string) map[string][]string {
			return map[string][]string{"Authorization": {tok}, "DPoP": {p}}
		},
		"empty token": func(_ *rig, _, p string) map[string][]string {
			return map[string][]string{"Authorization": {"DPoP "}, "DPoP": {p}}
		},
		"two schemes": func(_ *rig, tok, p string) map[string][]string {
			return map[string][]string{"Authorization": {"DPoP " + tok, "Bearer " + tok}, "DPoP": {p}}
		},
		"two tokens": func(_ *rig, tok, p string) map[string][]string {
			return map[string][]string{"Authorization": {"DPoP " + tok, "DPoP " + tok}, "DPoP": {p}}
		},
		"trailing junk": func(_ *rig, tok, p string) map[string][]string {
			return map[string][]string{"Authorization": {"DPoP " + tok + " extra"}, "DPoP": {p}}
		},
		"not token68": func(_ *rig, tok, p string) map[string][]string {
			return map[string][]string{"Authorization": {"DPoP " + tok + "\"<>"}, "DPoP": {p}}
		},
		"oversized": func(_ *rig, _, p string) map[string][]string {
			return map[string][]string{"Authorization": {"DPoP " + strings.Repeat("a", accesstoken.MaxTokenBytes+1)}, "DPoP": {p}}
		},
		"Basic": func(_ *rig, _, p string) map[string][]string {
			return map[string][]string{"Authorization": {"Basic dXNlcjpwYXNz"}, "DPoP": {p}}
		},
		"no credentials": func(*rig, string, string) map[string][]string { return nil },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			tok := r.token(t)
			wantRefused(t, r, r.do(t, "GET", path, hdr(r, tok, r.goodProof(t, "GET", tok))), 401, "ERR_UNAUTHENTICATED", "")
		})
	}
}

type failingReplay struct{}

func (failingReplay) Reserve(context.Context, string, string, time.Time, time.Time) error {
	return errors.New("replay store at 10.0.0.5 unreachable")
}

func TestAuth_ReplayStoreFailureFailsClosed(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	device, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	c := &clock{t: now0}
	logs := &syncBuf{}
	a, err := New(validConfig(t, pub), failingReplay{}, c.now, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{device: device, clock: c, logs: logs, replay: dpop.NewMemoryReplayStore()}
	r.signer = signerFor(t, priv, kid, issuer, audience, c)
	r.h = middleware.CorrelationIDMiddleware(a.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { r.reached.Add(1) })))
	tok := r.token(t)
	wantRefused(t, r, r.do(t, "GET", path, auth(tok, r.goodProof(t, "GET", tok))), 503, "ERR_TEMPORARILY_UNAVAILABLE", "")
	if strings.Contains(logs.String(), "10.0.0.5") {
		t.Error("the store's error text was logged")
	}
}

// Nothing from the token, the proof or the key reaches a response or a log.
func TestAuth_NothingSensitiveLeaks(t *testing.T) {
	r := newRig(t)
	tok := r.token(t)
	p := r.goodProof(t, "GET", tok)
	_ = r.do(t, "GET", path, auth(tok, p))
	_ = r.do(t, "GET", path, auth(tok, p))                              // replay
	_ = r.do(t, "GET", path, auth(tok+"x", r.goodProof(t, "GET", tok))) // bad token
	_ = r.do(t, "GET", path, map[string][]string{"Authorization": {"Bearer " + tok}})
	parts := strings.Split(tok, ".")
	secrets := []string{tok, parts[1][:20], parts[2][:20], p[:40], "usr_test"}
	for _, b := range r.bodies {
		for _, s := range secrets {
			if strings.Contains(b, s) {
				t.Errorf("a response carries %q", s)
			}
		}
	}
	for _, s := range secrets {
		if strings.Contains(r.logs.String(), s) {
			t.Errorf("the log carries %q", s)
		}
	}
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

func wantConfigErr(t *testing.T, err error, mention string) {
	t.Helper()
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}
	if mention != "" && !strings.Contains(err.Error(), mention) {
		t.Errorf("err = %q, want it to mention %q", err, mention)
	}
}

func TestConfig_TrustedKeyMissingOrInvalid(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecDER, _ := x509.MarshalPKIXPublicKey(&ec.PublicKey)
	privDER, _ := x509.MarshalPKCS8PrivateKey(priv)
	good := validConfig(t, pub)
	goodPEM, _ := os.ReadFile(good.PublicKeyFile)
	for name, tc := range map[string]struct {
		path    func(t *testing.T) string
		mention string
	}{
		"missing":   {func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent.pem") }, "does not exist"},
		"directory": {func(t *testing.T) string { return t.TempDir() }, "not a regular file"},
		"empty":     {func(t *testing.T) string { return writeFile(t, "k", nil) }, "no PEM block"},
		"private key": {func(t *testing.T) string {
			return writeFile(t, "k", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}))
		}, "PEM type"},
		"P-256 public key": {func(t *testing.T) string {
			return writeFile(t, "k", pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: ecDER}))
		}, "want Ed25519"},
		"trailing data": {func(t *testing.T) string { return writeFile(t, "k", append(append([]byte{}, goodPEM...), "x"...)) }, "trailing data"},
		"oversized":     {func(t *testing.T) string { return writeFile(t, "k", make([]byte, MaxKeyFileBytes+1)) }, "larger than"},
	} {
		t.Run(name, func(t *testing.T) {
			c := good
			c.PublicKeyFile = tc.path(t)
			a, err := New(c, dpop.NewMemoryReplayStore(), nil, nil)
			if a != nil {
				t.Error("an authenticator was built")
			}
			wantConfigErr(t, err, tc.mention)
			if strings.Contains(err.Error(), c.PublicKeyFile) {
				t.Error("the error carries the key path")
			}
		})
	}
	t.Run("no replay store", func(t *testing.T) {
		_, err := New(good, nil, nil, nil)
		wantConfigErr(t, err, "replay store")
	})
	t.Run("invalid key id", func(t *testing.T) {
		c := good
		c.KeyID = "kid\x01"
		_, err := New(c, dpop.NewMemoryReplayStore(), nil, nil)
		wantConfigErr(t, err, "verifier")
	})
}

func TestConfig_PublicOriginMustBeCanonicalHTTPS(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	for _, o := range []string{
		"http://edge.example.test", "https://edge.example.test/", "https://edge.example.test/v1", "https://edge.example.test?x",
		"https://edge.example.test#f", "https://u:p@edge.example.test", "HTTPS://edge.example.test", "edge.example.test", "https://", "",
		" https://edge.example.test",
	} {
		c := validConfig(t, pub)
		c.PublicOrigin = o
		if _, err := New(c, dpop.NewMemoryReplayStore(), nil, nil); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("origin %q accepted", o)
		}
	}
	c := validConfig(t, pub)
	c.PublicOrigin = "https://edge.example.test:8443"
	if _, err := New(c, dpop.NewMemoryReplayStore(), nil, nil); err != nil {
		t.Errorf("an origin with a port was refused: %v", err)
	}
}

func clearAuthEnv(t *testing.T) {
	t.Helper()
	for _, n := range ActivationVariables {
		t.Setenv(n, "")
		if err := os.Unsetenv(n); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfig_Activation(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	t.Run("none set: inactive", func(t *testing.T) {
		clearAuthEnv(t)
		if _, active, err := LoadActivation(); active || err != nil {
			t.Errorf("(%v, %v)", active, err)
		}
	})
	for _, only := range ActivationVariables {
		t.Run("only "+only, func(t *testing.T) {
			clearAuthEnv(t)
			t.Setenv(only, "x")
			_, active, err := LoadActivation()
			if !active {
				t.Error("not active")
			}
			wantConfigErr(t, err, "partially configured")
		})
	}
	t.Run("all set", func(t *testing.T) {
		clearAuthEnv(t)
		c := validConfig(t, pub)
		for name, v := range map[string]string{
			"CS_EDGE_API_ACCESS_TOKEN_PUBLIC_KEY_FILE": c.PublicKeyFile, "CS_EDGE_API_ACCESS_TOKEN_KID": c.KeyID,
			"CS_EDGE_API_ACCESS_TOKEN_ISSUER": c.Issuer, "CS_EDGE_API_ACCESS_TOKEN_AUDIENCE": c.Audience, "CS_EDGE_API_PUBLIC_ORIGIN": c.PublicOrigin,
			"CS_EDGE_API_DATABASE_DSN_FILE": c.DatabaseDSNFile,
		} {
			t.Setenv(name, v)
		}
		got, active, err := LoadActivation()
		if !active || err != nil || got != c {
			t.Errorf("(%+v, %v, %v)", got, active, err)
		}
		t.Setenv("CS_EDGE_API_ACCESS_TOKEN_ISSUER", "  ")
		if _, _, err := LoadActivation(); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("a blank issuer was accepted: %v", err)
		}
	})
}
