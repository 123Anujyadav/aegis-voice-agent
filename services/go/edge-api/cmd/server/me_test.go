package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/services/go/edge-api/internal/auth"
	"github.com/callscreen/callscreen-platform/services/go/edge-api/internal/httpapi"
)

const (
	meIssuer   = "urn:callscreen:identity:me-test"
	meAudience = "urn:callscreen:edge-api:me-test"
	meKID      = "me-test-key-1"
	meOrigin   = "https://edge.me.test"
	meSubject  = "sub_me_test_0001"
)

// meRig is the production composition — applicationHandler over
// protectedRoutes, behind a real auth.Authenticator — with a counter around the
// production /v1/me handler and the edge logs captured.
type meRig struct {
	h       http.Handler
	signer  *accesstoken.Signer
	priv    ed25519.PrivateKey
	device  *ecdsa.PrivateKey
	jkt     string
	reached *atomic.Int32
	logs    *syncBuffer
}

// setMeEnv writes a generated (test-only) issuer key pair, sets the six edge
// variables to trust its public half, and returns the private half.
func setMeEnv(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(pub)
	dir := t.TempDir()
	key := filepath.Join(dir, "issuer.pub.pem")
	dsn := filepath.Join(dir, "dsn")
	if err := os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dsn, []byte("postgres://edge@127.0.0.1:1/edge?connect_timeout=2"), 0o600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"CS_EDGE_API_ACCESS_TOKEN_PUBLIC_KEY_FILE": key,
		"CS_EDGE_API_ACCESS_TOKEN_KID":             meKID,
		"CS_EDGE_API_ACCESS_TOKEN_ISSUER":          meIssuer,
		"CS_EDGE_API_ACCESS_TOKEN_AUDIENCE":        meAudience,
		"CS_EDGE_API_PUBLIC_ORIGIN":                meOrigin,
		"CS_EDGE_API_DATABASE_DSN_FILE":            dsn,
	} {
		t.Setenv(k, v)
	}
	return priv
}

func newSigner(t *testing.T, priv ed25519.PrivateKey, iss, aud, kid string, now func() time.Time) *accesstoken.Signer {
	t.Helper()
	s, err := accesstoken.NewSigner(accesstoken.SignerConfig{Issuer: iss, Audience: aud, KeyID: kid, Key: priv, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newMeRig(t *testing.T) *meRig {
	t.Helper()
	priv := setMeEnv(t)
	ac, active, err := auth.LoadActivation()
	if err != nil || !active {
		t.Fatalf("(%v, %v)", active, err)
	}
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	a, err := auth.New(ac, dpop.NewMemoryReplayStore(), nil, log)
	if err != nil {
		t.Fatal(err)
	}
	r := &meRig{priv: priv, reached: &atomic.Int32{}, logs: logs}
	r.device, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	r.jkt, _ = accesstoken.ThumbprintP256(&r.device.PublicKey)
	r.signer = newSigner(t, priv, meIssuer, meAudience, meKID, time.Now)
	routes := protectedRoutes(log)
	prod := routes[httpapi.MePath]
	routes[httpapi.MePath] = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.reached.Add(1)
		prod.ServeHTTP(w, req)
	})
	r.h = applicationHandler(a, routes)
	return r
}

func (r *meRig) token(t *testing.T) string {
	t.Helper()
	tok, err := r.signer.Issue(meSubject, []string{"subscriber"}, r.jkt)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

type meResult struct {
	code int
	hdr  http.Header
	body map[string]any
	raw  string
}

func (r *meRig) do(t *testing.T, method, target string, hdr http.Header) meResult {
	t.Helper()
	req := httptest.NewRequest(method, target, http.NoBody)
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	out := meResult{code: rec.Code, hdr: rec.Header(), raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

func creds(token, p string) http.Header {
	h := http.Header{}
	if token != "" {
		h.Set("Authorization", "DPoP "+token)
	}
	if p != "" {
		h.Set("DPoP", p)
	}
	return h
}

func (r *meRig) good(t *testing.T, token string) http.Header {
	return creds(token, proof(t, r.device, "GET", meOrigin+httpapi.MePath, token))
}

func wantCommonHeaders(t *testing.T, name string, got meResult) {
	t.Helper()
	if got.hdr.Get("Cache-Control") != "no-store" || got.hdr.Get("X-Correlation-ID") == "" ||
		got.hdr.Get("X-Content-Type-Options") != "nosniff" || got.hdr.Get("Strict-Transport-Security") == "" {
		t.Errorf("%s: headers %v", name, got.hdr)
	}
}

func TestMe_ReturnsTheVerifiedPrincipal(t *testing.T) {
	r := newMeRig(t)
	tok := r.token(t)
	got := r.do(t, "GET", httpapi.MePath, r.good(t, tok))
	if got.code != 200 || r.reached.Load() != 1 {
		t.Fatalf("%d %s", got.code, got.raw)
	}
	wantCommonHeaders(t, "200", got)
	if ct := got.hdr.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	keys := make([]string, 0, len(got.body))
	for k := range got.body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "deviceKeyThumbprint,subject" {
		t.Errorf("response members = %v; want exactly subject and deviceKeyThumbprint", keys)
	}
	if got.body["subject"] != meSubject || got.body["deviceKeyThumbprint"] != r.jkt {
		t.Errorf("body = %s", got.raw)
	}
	if strings.Contains(got.raw, tok) || strings.Contains(got.raw, "subscriber") {
		t.Errorf("the response carries the token or roles: %s", got.raw)
	}
}

// Identity is taken from the verified token only: spoofing headers and query
// parameters change nothing.
func TestMe_IgnoresClientSuppliedIdentity(t *testing.T) {
	r := newMeRig(t)
	tok := r.token(t)
	h := r.good(t, tok)
	for _, name := range []string{"X-User-ID", "X-Subject", "X-Forwarded-User", "X-Remote-User", "X-Device-Thumbprint", "X-Roles"} {
		h.Set(name, "attacker")
	}
	got := r.do(t, "GET", httpapi.MePath+"?subject=attacker&sub=attacker&deviceKeyThumbprint=attacker&roles=admin", h)
	if got.code != 200 || got.body["subject"] != meSubject || got.body["deviceKeyThumbprint"] != r.jkt || strings.Contains(got.raw, "attacker") {
		t.Fatalf("%d %s", got.code, got.raw)
	}
	// A token for a different subject yields that subject — from the token.
	other, _ := r.signer.Issue("sub_someone_else", nil, r.jkt)
	got = r.do(t, "GET", httpapi.MePath, r.good(t, other))
	if got.code != 200 || got.body["subject"] != "sub_someone_else" {
		t.Fatalf("%d %s", got.code, got.raw)
	}
}

// Every authentication failure is refused BEFORE the handler: the counter
// around the production handler never moves, and nothing leaks.
func TestMe_AuthenticationFailuresNeverReachTheHandler(t *testing.T) {
	r := newMeRig(t)
	tok := r.token(t)
	path := meOrigin + httpapi.MePath
	otherDevice, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, otherIssuerKey, _ := ed25519.GenerateKey(rand.Reader)
	sig := tok[strings.LastIndex(tok, ".")+1:]
	flip := "A"
	if sig[0] == 'A' {
		flip = "B"
	}
	tampered := tok[:strings.LastIndex(tok, ".")+1] + flip + sig[1:]
	payloadSwap := func() string { // header.payload of another subject, original signature
		o, _ := r.signer.Issue("sub_attacker", nil, r.jkt)
		parts := strings.Split(o, ".")
		return parts[0] + "." + parts[1] + "." + sig
	}()
	foreign, _ := newSigner(t, otherIssuerKey, meIssuer, meAudience, meKID, time.Now).Issue(meSubject, nil, r.jkt)
	wrongAud, _ := newSigner(t, r.priv, meIssuer, "urn:other", meKID, time.Now).Issue(meSubject, nil, r.jkt)
	wrongIss, _ := newSigner(t, r.priv, "urn:other", meAudience, meKID, time.Now).Issue(meSubject, nil, r.jkt)
	wrongKID, _ := newSigner(t, r.priv, meIssuer, meAudience, "other-kid", time.Now).Issue(meSubject, nil, r.jkt)
	expired, _ := newSigner(t, r.priv, meIssuer, meAudience, meKID, func() time.Time { return time.Now().Add(-2 * accesstoken.Lifetime) }).Issue(meSubject, nil, r.jkt)
	unbound, _ := r.signer.Issue(meSubject, nil, mustJKT(t, otherDevice))

	replayed := r.good(t, tok)
	if got := r.do(t, "GET", httpapi.MePath, replayed); got.code != 200 {
		t.Fatalf("first use: %d %s", got.code, got.raw)
	}
	r.reached.Store(0)

	twoProofs := r.good(t, tok)
	twoProofs.Add("DPoP", proof(t, r.device, "GET", path, tok))
	twoAuth := r.good(t, tok)
	twoAuth.Add("Authorization", "DPoP "+tok)

	cases := []struct {
		name   string
		hdr    http.Header
		status int
		code   string
	}{
		{"no credentials", http.Header{}, 401, "ERR_UNAUTHENTICATED"},
		{"proof only", creds("", proof(t, r.device, "GET", path, tok)), 401, "ERR_UNAUTHENTICATED"},
		{"bearer scheme", http.Header{"Authorization": {"Bearer " + tok}, "Dpop": {proof(t, r.device, "GET", path, tok)}}, 401, "ERR_UNAUTHENTICATED"},
		{"two Authorization headers", twoAuth, 401, "ERR_UNAUTHENTICATED"},
		{"garbage token", creds("not-a-jwt", proof(t, r.device, "GET", path, "not-a-jwt")), 401, "ERR_INVALID_TOKEN"},
		{"tampered signature", creds(tampered, proof(t, r.device, "GET", path, tampered)), 401, "ERR_INVALID_TOKEN"},
		{"swapped payload", creds(payloadSwap, proof(t, r.device, "GET", path, payloadSwap)), 401, "ERR_INVALID_TOKEN"},
		{"another issuer's key", creds(foreign, proof(t, r.device, "GET", path, foreign)), 401, "ERR_INVALID_TOKEN"},
		{"wrong audience", creds(wrongAud, proof(t, r.device, "GET", path, wrongAud)), 401, "ERR_INVALID_TOKEN"},
		{"wrong issuer", creds(wrongIss, proof(t, r.device, "GET", path, wrongIss)), 401, "ERR_INVALID_TOKEN"},
		{"wrong kid", creds(wrongKID, proof(t, r.device, "GET", path, wrongKID)), 401, "ERR_INVALID_TOKEN"},
		{"expired token", creds(expired, proof(t, r.device, "GET", path, expired)), 401, "ERR_INVALID_TOKEN"},
		{"no proof", creds(tok, ""), 401, "ERR_INVALID_DPOP_PROOF"},
		{"malformed proof", creds(tok, "abc.def.ghi"), 401, "ERR_INVALID_DPOP_PROOF"},
		{"two proofs", twoProofs, 401, "ERR_INVALID_DPOP_PROOF"},
		{"proof by another key", creds(tok, proof(t, otherDevice, "GET", path, tok)), 401, "ERR_INVALID_DPOP_PROOF"},
		{"token bound to another key", creds(unbound, proof(t, r.device, "GET", path, unbound)), 401, "ERR_INVALID_DPOP_PROOF"},
		{"proof for another URL", creds(tok, proof(t, r.device, "GET", meOrigin+"/v1/other", tok)), 401, "ERR_INVALID_DPOP_PROOF"},
		{"proof for another origin", creds(tok, proof(t, r.device, "GET", "https://evil.test"+httpapi.MePath, tok)), 401, "ERR_INVALID_DPOP_PROOF"},
		{"proof for another method", creds(tok, proof(t, r.device, "POST", path, tok)), 401, "ERR_INVALID_DPOP_PROOF"},
		{"proof without ath", creds(tok, proof(t, r.device, "GET", path, "")), 401, "ERR_INVALID_DPOP_PROOF"},
		{"proof bound to another token", creds(tok, proof(t, r.device, "GET", path, r.token(t))), 401, "ERR_INVALID_DPOP_PROOF"},
		{"replayed proof", replayed, 401, "ERR_INVALID_DPOP_PROOF"},
	}
	for _, c := range cases {
		got := r.do(t, "GET", httpapi.MePath, c.hdr)
		if got.code != c.status || got.body["code"] != c.code || !strings.HasPrefix(got.hdr.Get("WWW-Authenticate"), `DPoP algs="ES256"`) {
			t.Errorf("%s: %d %s (WWW-Authenticate %q)", c.name, got.code, got.raw, got.hdr.Get("WWW-Authenticate"))
		}
		wantCommonHeaders(t, c.name, got)
		if strings.Contains(got.raw, meSubject) || strings.Contains(got.raw, tok) || strings.Contains(got.raw, r.jkt) {
			t.Errorf("%s: the refusal discloses protected data: %s", c.name, got.raw)
		}
	}
	if n := r.reached.Load(); n != 0 {
		t.Fatalf("the /v1/me handler ran %d times for refused requests", n)
	}
	logs := r.logs.String()
	if strings.Contains(logs, tok) || strings.Contains(logs, sig) || strings.Contains(logs, ".eyJ") {
		t.Error("a token or proof appears in the logs")
	}
}

func mustJKT(t *testing.T, k *ecdsa.PrivateKey) string {
	t.Helper()
	j, err := accesstoken.ThumbprintP256(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// Other methods: unauthenticated, 401 before anything; authenticated (a proof
// for that method), 405 with Allow: GET and nothing about the caller.
func TestMe_UnsupportedMethods(t *testing.T) {
	r := newMeRig(t)
	tok := r.token(t)
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		if got := r.do(t, m, httpapi.MePath, http.Header{}); got.code != 401 {
			t.Errorf("%s without credentials: %d", m, got.code)
		}
		got := r.do(t, m, httpapi.MePath, creds(tok, proof(t, r.device, m, meOrigin+httpapi.MePath, tok)))
		if got.code != http.StatusMethodNotAllowed || got.hdr.Get("Allow") != "GET" {
			t.Errorf("%s: %d Allow=%q %s", m, got.code, got.hdr.Get("Allow"), got.raw)
		}
		if m != "HEAD" && (got.body["code"] != "ERR_METHOD_NOT_ALLOWED" || got.hdr.Get("Content-Type") != "application/problem+json") {
			t.Errorf("%s: %s", m, got.raw)
		}
		wantCommonHeaders(t, m, got)
		if strings.Contains(got.raw, meSubject) || strings.Contains(got.raw, r.jkt) {
			t.Errorf("%s discloses the principal: %s", m, got.raw)
		}
	}
	// Paths other than exactly /v1/me are not this route.
	if got := r.do(t, "GET", httpapi.MePath+"/", creds(tok, proof(t, r.device, "GET", meOrigin+httpapi.MePath+"/", tok))); got.code != 404 || strings.Contains(got.raw, meSubject) {
		t.Errorf("/v1/me/: %d %s", got.code, got.raw)
	}
}

// The health listener is unchanged with the production route mounted: probes
// need no credentials, the application port serves /v1/me only behind
// authentication, and nothing leaks into either.
func TestMe_ServedBehindAuthenticationInTheRealLifecycle(t *testing.T) {
	priv := setMeEnv(t)
	fakeOpen(t) // the real authenticator on an in-memory store; PostgreSQL is the E2E test's
	cfg := platform.ServiceConfig{Name: serviceName, Version: "test", HTTPPort: e2eFreePort(t), HealthPort: e2eFreePort(t), ShutdownTimeout: time.Second, ReadHeaderTimeout: 5 * time.Second}
	health := platform.NewHealth(cfg)
	svc := platform.NewService(cfg, quiet, health)
	if err := configureAuth(ctx, cfg, svc, health, protectedRoutes(quiet), quiet); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- svc.Run(runCtx) }()
	hp := fmt.Sprintf("http://127.0.0.1:%d", cfg.HealthPort)
	waitFor(t, "readiness", func() bool { return statusOf(hp+"/readyz") == 200 })
	waitFor(t, "listener", func() bool { return dials(cfg.HTTPPort) })
	resp, err := http.Get(hp + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	var rb struct {
		Status string `json:"status"`
		Checks []struct {
			Name     string `json:"name"`
			Critical bool   `json:"critical"`
		} `json:"checks"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&rb)
	resp.Body.Close()
	if rb.Status != "healthy" || len(rb.Checks) != 1 || rb.Checks[0].Name != "edge-replay-store" || !rb.Checks[0].Critical {
		t.Errorf("readiness = %+v", rb)
	}
	if code := statusOf(hp + "/healthz"); code != 200 {
		t.Errorf("/healthz = %d", code)
	}
	if code := statusOf(hp + httpapi.MePath); code != 404 {
		t.Errorf("/v1/me on the health port = %d; want 404", code)
	}

	base := fmt.Sprintf("http://127.0.0.1:%d", cfg.HTTPPort)
	if got := edgeGet(t, base, httpapi.MePath, nil); got.code != 401 || got.body["code"] != "ERR_UNAUTHENTICATED" {
		t.Errorf("unauthenticated /v1/me = %d %s", got.code, got.raw)
	}
	device, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tok, _ := newSigner(t, priv, meIssuer, meAudience, meKID, time.Now).Issue(meSubject, nil, mustJKT(t, device))
	got := edgeGet(t, base, httpapi.MePath, map[string]string{"Authorization": "DPoP " + tok, "DPoP": proof(t, device, "GET", meOrigin+httpapi.MePath, tok)})
	if got.code != 200 || got.body["subject"] != meSubject {
		t.Errorf("authenticated /v1/me = %d %s", got.code, got.raw)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("service did not stop")
	}
}
