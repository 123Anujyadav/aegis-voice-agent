package main

// End to end: identity issues a real device-bound access token, and edge-api —
// in its real platform.Service lifecycle, on the shared PostgreSQL replay
// store — admits it to a TEST-ONLY protected route.
//
// identity's package is `main` in another module, so it cannot be imported. It
// runs as a child process: this test builds identity's cmd/server TEST binary
// and starts its TestE2EIdentityProcess helper (identity's e2e_process_test.go),
// which is the configured service through configureIdentity and
// platform.Service with one test-only addition — an OtpSender that hands codes
// back over a pipe, since production has no OTP delivery at all. The migration
// runs through identity's explicit `migrate` command in a child of its own.
//
// edge-api runs in this process through configureAuth, which already takes
// the route table as a parameter: the test passes the production table
// (protectedRoutes: GET /v1/me) plus two routes of its own.
//
// Gated like every PostgreSQL test here: AEGIS_IDENTITY_PG_DSN names a
// disposable database and AEGIS_IDENTITY_PG_DISPOSABLE=1 declares it so.

import (
	"bufio"
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
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/packages/go/pgshared"
	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/services/go/edge-api/internal/auth"
	"github.com/callscreen/callscreen-platform/services/go/edge-api/internal/httpapi"
)

// Test-only values; none is, or may become, a production default.
const (
	e2eIssuer     = "urn:callscreen:identity:e2e"
	e2eAudience   = "urn:callscreen:edge-api:e2e"
	e2eKID        = "e2e-key-1"
	e2eEnrolURL   = "https://identity.e2e.test/v1/otp/validate"
	e2eRefreshURL = "https://identity.e2e.test/v1/token/refresh"
	e2eOrigin     = "https://edge.e2e.test"
	e2eNumber     = "+919876500042"
	whoamiPath    = "/v1/e2e-test-only/whoami"
	slowPath      = "/v1/e2e-test-only/slow"
)

func e2eFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// waitFor polls cond — a condition, not a delay — until it holds or 20s pass.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func dials(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err == nil {
		c.Close()
	}
	return err == nil
}

func statusOf(u string) int {
	resp, err := http.Get(u)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

// e2eSchema creates a schema of its own in the disposable database and returns
// a DSN scoped to it (search_path), dropped at cleanup.
func e2eSchema(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("AEGIS_IDENTITY_PG_DSN"))
	if dsn == "" {
		t.Skip("AEGIS_IDENTITY_PG_DSN is not set: end-to-end test SKIPPED (not passed)")
	}
	if os.Getenv("AEGIS_IDENTITY_PG_DISPOSABLE") != "1" {
		t.Fatal("AEGIS_IDENTITY_PG_DSN is set but AEGIS_IDENTITY_PG_DISPOSABLE is not 1: refusing")
	}
	admin, err := pgshared.OpenPool(ctx, dsn, pgshared.AllowPlaintextForDevelopment)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "e2e_test_" + hex.EncodeToString(b)
	if _, execErr := admin.Exec(ctx, `CREATE SCHEMA `+schema); execErr != nil {
		admin.Close()
		t.Fatalf("creating schema: %v", execErr)
	}
	t.Cleanup(func() {
		if _, execErr := admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`); execErr != nil {
			t.Errorf("dropping %s: %v", schema, execErr)
		}
		admin.Close()
	})
	if !strings.Contains(dsn, "://") {
		return dsn + " search_path=" + schema
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("parsing DSN (value not shown)")
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func e2eWrite(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// buildIdentity compiles identity's cmd/server test binary.
func buildIdentity(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("the go command is needed to build identity: %v", err)
	}
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "identity"))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "identity-e2e.test")
	if os.PathSeparator == '\\' {
		out += ".exe"
	}
	cmd := exec.Command(goBin, "test", "-c", "-o", out, "./cmd/server")
	cmd.Dir = dir
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building identity's test binary: %v\n%s", err, b)
	}
	return out
}

// identityChild is identity running as a child process.
type identityChild struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	codes  chan string
	stderr *syncBuffer
	exited chan error
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func identityCmd(bin, mode string, env []string) *exec.Cmd {
	cmd := exec.Command(bin, "-test.run=^TestE2EIdentityProcess$", "-test.count=1", "-test.timeout=5m")
	// CS_IDENTITY_ENVIRONMENT=development: the disposable test database is
	// plaintext, which identity otherwise refuses (ADR-0029).
	cmd.Env = append(append(os.Environ(), "AEGIS_E2E_IDENTITY_PROCESS="+mode, "CS_IDENTITY_ENVIRONMENT=development"), env...)
	return cmd
}

func startIdentity(t *testing.T, bin string, env []string) *identityChild {
	t.Helper()
	cmd := identityCmd(bin, "serve", env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	c := &identityChild{cmd: cmd, stdin: stdin, codes: make(chan string, 4), stderr: &syncBuffer{}, exited: make(chan error, 1)}
	cmd.Stderr = c.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// stdout carries only the helper's OTP lines and the test framework's.
	// The code is passed on and never logged.
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if code, ok := strings.CutPrefix(sc.Text(), "E2E-OTP "); ok {
				c.codes <- code
			}
		}
		c.exited <- cmd.Wait()
	}()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
		}
	})
	return c
}

// stop closes stdin (the helper's shutdown signal) and waits for a clean exit.
func (c *identityChild) stop(t *testing.T) {
	t.Helper()
	_ = c.stdin.Close()
	select {
	case err := <-c.exited:
		if err != nil {
			t.Errorf("identity exited with %v\n%s", err, c.stderr.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("identity did not stop")
	}
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// proof is an ES256 DPoP proof by k; token, when set, is bound by ath.
func proof(t *testing.T, k *ecdsa.PrivateKey, method, htu, token string) string {
	t.Helper()
	pt, _ := k.PublicKey.Bytes()
	hdr, _ := json.Marshal(map[string]any{
		"typ": "dpop+jwt", "alg": "ES256",
		"jwk": map[string]any{"kty": "EC", "crv": "P-256", "x": b64url(pt[1:33]), "y": b64url(pt[33:65])},
	})
	jti := make([]byte, 16)
	_, _ = rand.Read(jti)
	claims := map[string]any{"jti": hex.EncodeToString(jti), "htm": method, "htu": htu, "iat": time.Now().Unix()}
	if token != "" {
		claims["ath"] = dpop.AccessTokenHash(token)
	}
	cl, _ := json.Marshal(claims)
	input := b64url(hdr) + "." + b64url(cl)
	d := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, k, d[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + b64url(sig)
}

func postJSON(t *testing.T, u, body, dpopProof string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", u, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if dpopProof != "" {
		req.Header.Set("DPoP", dpopProof)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

type edgeResp struct {
	code int
	hdr  http.Header
	body map[string]any
	raw  string
}

func edgeGet(t *testing.T, base, path string, hdr map[string]string) edgeResp {
	t.Helper()
	req, _ := http.NewRequest("GET", base+path, http.NoBody)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := edgeResp{code: resp.StatusCode, hdr: resp.Header, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

// recordingRuntime wraps the REAL auth.Open runtime to record when its pool
// is closed, relative to the in-flight handler.
type recordingRuntime struct {
	authRuntime
	events *eventLog
}

func (r *recordingRuntime) Close() {
	r.events.add("replay-store pool closed")
	r.authRuntime.Close()
}

type eventLog struct {
	mu sync.Mutex
	ev []string
}

func (e *eventLog) add(s string) { e.mu.Lock(); e.ev = append(e.ev, s); e.mu.Unlock() }
func (e *eventLog) list() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.ev...)
}

func TestE2E_IdentityTokenAdmittedByEdgeThroughTheRealLifecycle(t *testing.T) {
	dsn := e2eSchema(t)
	bin := buildIdentity(t)
	dir := t.TempDir()

	// Generated test-only keys: identity signs with the private half, edge
	// trusts only the public half.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	privDER, _ := x509.MarshalPKCS8PrivateKey(priv)
	pubDER, _ := x509.MarshalPKIXPublicKey(pub)
	hmacKey := make([]byte, 48)
	_, _ = rand.Read(hmacKey)
	dsnFile := e2eWrite(t, dir, "dsn", []byte(dsn+"\n"))

	// 1. The explicit migration path, in a child of its own.
	mig := identityCmd(bin, "migrate", []string{"CS_IDENTITY_DATABASE_DSN_FILE=" + dsnFile})
	if out, err := mig.CombinedOutput(); err != nil || !strings.Contains(string(out), "PASS") {
		t.Fatalf("identity migrate: %v\n%s", err, out)
	}

	// 2. identity, configured entirely from its environment.
	idHTTP, idHealth := e2eFreePort(t), e2eFreePort(t)
	id := startIdentity(t, bin, []string{
		fmt.Sprintf("CS_IDENTITY_HTTP_PORT=%d", idHTTP),
		fmt.Sprintf("CS_IDENTITY_HEALTH_PORT=%d", idHealth),
		"CS_IDENTITY_SHUTDOWN_TIMEOUT=3s",
		"CS_IDENTITY_ACCESS_TOKEN_PRIVATE_KEY_FILE=" + e2eWrite(t, dir, "signing.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})),
		"CS_IDENTITY_ACCESS_TOKEN_ISSUER=" + e2eIssuer,
		"CS_IDENTITY_ACCESS_TOKEN_AUDIENCE=" + e2eAudience,
		"CS_IDENTITY_ACCESS_TOKEN_KID=" + e2eKID,
		"CS_IDENTITY_ENROLLMENT_CANONICAL_URL=" + e2eEnrolURL,
		"CS_IDENTITY_REFRESH_CANONICAL_URL=" + e2eRefreshURL,
		"CS_IDENTITY_HMAC_SECRET_KEY_FILE=" + e2eWrite(t, dir, "hmac.key", hmacKey),
		"CS_IDENTITY_DATABASE_DSN_FILE=" + dsnFile,
	})
	idBase := fmt.Sprintf("http://127.0.0.1:%d", idHTTP)
	waitFor(t, "identity readiness", func() bool { return statusOf(fmt.Sprintf("http://127.0.0.1:%d/readyz", idHealth)) == 200 })

	// 3. edge-api, in this process, through configureAuth and platform.Service,
	// on the same database. The routes are this test's.
	clearAuthEnv(t)
	for k, v := range map[string]string{
		"CS_EDGE_API_ACCESS_TOKEN_PUBLIC_KEY_FILE": e2eWrite(t, dir, "issuer.pub.pem", pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})),
		"CS_EDGE_API_ACCESS_TOKEN_KID":             e2eKID,
		"CS_EDGE_API_ACCESS_TOKEN_ISSUER":          e2eIssuer,
		"CS_EDGE_API_ACCESS_TOKEN_AUDIENCE":        e2eAudience,
		"CS_EDGE_API_PUBLIC_ORIGIN":                e2eOrigin,
		"CS_EDGE_API_DATABASE_DSN_FILE":            dsnFile,
		"CS_EDGE_API_ENVIRONMENT":                  "development", // the disposable test database is plaintext
	} {
		t.Setenv(k, v)
	}
	events := &eventLog{}
	var realRT authRuntime
	orig := openAuth
	openAuth = func(ctx context.Context, cfg auth.Config, log *slog.Logger) (authRuntime, error) {
		rt, err := orig(ctx, cfg, log)
		if err != nil {
			return nil, err
		}
		realRT = rt
		return &recordingRuntime{authRuntime: rt, events: events}, nil
	}
	t.Cleanup(func() { openAuth = orig })

	var reached atomic.Int32
	var mu sync.Mutex
	var seen auth.Principal
	entered, release := make(chan struct{}), make(chan struct{})
	routes := map[string]http.Handler{
		"GET " + whoamiPath: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached.Add(1)
			p, ok := auth.PrincipalFrom(r.Context())
			mu.Lock()
			seen = p
			mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		}),
		"GET " + slowPath: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached.Add(1)
			close(entered)
			<-release
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("finished during shutdown"))
			events.add("in-flight handler returned")
		}),
	}
	edgeLogs := &syncBuffer{}
	edgeLog := slog.New(slog.NewJSONHandler(edgeLogs, nil))
	for pattern, h := range protectedRoutes(edgeLog) {
		routes[pattern] = h
	}
	cfg := platform.ServiceConfig{
		Name: serviceName, Version: "e2e", HTTPPort: e2eFreePort(t), HealthPort: e2eFreePort(t),
		ShutdownTimeout: 3 * time.Second, ReadHeaderTimeout: 5 * time.Second,
	}
	health := platform.NewHealth(cfg)
	svc := platform.NewService(cfg, edgeLog, health)
	if err := configureAuth(context.Background(), cfg, svc, health, routes, edgeLog); err != nil {
		t.Fatalf("edge configureAuth: %v", err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- svc.Run(runCtx) }()
	edgeBase := fmt.Sprintf("http://127.0.0.1:%d", cfg.HTTPPort)
	edgeReady := fmt.Sprintf("http://127.0.0.1:%d/readyz", cfg.HealthPort)
	waitFor(t, "edge readiness", func() bool { return statusOf(edgeReady) == 200 })
	waitFor(t, "edge listener", func() bool { return dials(cfg.HTTPPort) })
	if code := statusOf(fmt.Sprintf("http://127.0.0.1:%d/healthz", cfg.HealthPort)); code != 200 {
		t.Errorf("edge /healthz without credentials = %d", code)
	}

	// 4. Enrolment through identity's HTTP API: OTP request, the code from the
	// stand-in sender, then validation with a DPoP proof by the device key.
	code, body := postJSON(t, idBase+"/v1/otp/request", `{"msisdn":"`+e2eNumber+`"}`, "")
	if code != 202 {
		t.Fatalf("OTP request: %d %v", code, body)
	}
	reqID, _ := body["requestId"].(string)
	var otp string
	select {
	case otp = <-id.codes:
	case <-time.After(20 * time.Second):
		t.Fatal("no OTP was delivered")
	}
	device, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	code, body = postJSON(t, idBase+"/v1/otp/validate", fmt.Sprintf(`{"requestId":%q,"code":%q}`, reqID, otp), proof(t, device, "POST", e2eEnrolURL, ""))
	token, _ := body["accessToken"].(string)
	if code != 200 || body["tokenType"] != "DPoP" || token == "" {
		t.Fatalf("enrolment: %d (token issued: %v)", code, token != "")
	}
	jkt, _ := accesstoken.ThumbprintP256(&device.PublicKey)
	var tokClaims struct {
		Sub string `json:"sub"`
		JTI string `json:"jti"`
		Cnf struct {
			JKT string `json:"jkt"`
		} `json:"cnf"`
	}
	if parts := strings.Split(token, "."); len(parts) == 3 {
		raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
		_ = json.Unmarshal(raw, &tokClaims)
	}
	if tokClaims.Sub == "" || tokClaims.Cnf.JKT != jkt {
		t.Fatalf("identity's token is not bound to the device key (sub set: %v)", tokClaims.Sub != "")
	}

	// 5. The protected route, with a proof bound to the token.
	whoami := func(tok, p string) edgeResp {
		return edgeGet(t, edgeBase, whoamiPath, map[string]string{"Authorization": "DPoP " + tok, "DPoP": p})
	}
	p1 := proof(t, device, "GET", e2eOrigin+whoamiPath, token)
	if got := whoami(token, p1); got.code != 200 || reached.Load() != 1 {
		t.Fatalf("protected route: %d %s (handler ran %d)", got.code, got.raw, reached.Load())
	}
	mu.Lock()
	got := seen
	mu.Unlock()
	if got.Subject != tokClaims.Sub || got.DeviceKeyThumbprint != jkt || got.TokenID != tokClaims.JTI || got.TokenID == "" {
		t.Errorf("principal = %+v, want sub/jkt/jti from identity's token", got)
	}

	// 6. Refusals: none reaches the handler.
	wantRefused := func(name string, r edgeResp, status int, codeWant, dpopErr string) {
		t.Helper()
		wa := r.hdr.Get("WWW-Authenticate")
		if r.code != status || r.body["code"] != codeWant || r.hdr.Get("Cache-Control") != "no-store" ||
			!strings.HasPrefix(wa, `DPoP algs="ES256"`) || (dpopErr != "" && !strings.Contains(wa, `error="`+dpopErr+`"`)) {
			t.Errorf("%s: %d %s WWW-Authenticate=%q", name, r.code, r.raw, wa)
		}
		if strings.Contains(r.raw, token) {
			t.Errorf("%s: the response echoes the token", name)
		}
	}
	wantRefused("replayed proof", whoami(token, p1), 401, "ERR_INVALID_DPOP_PROOF", "invalid_dpop_proof")
	sig := token[strings.LastIndex(token, ".")+1:]
	flip := "A"
	if sig[0] == 'A' {
		flip = "B"
	}
	forged := token[:strings.LastIndex(token, ".")+1] + flip + sig[1:]
	wantRefused("forged token signature", whoami(forged, proof(t, device, "GET", e2eOrigin+whoamiPath, forged)), 401, "ERR_INVALID_TOKEN", "invalid_token")
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	wantRefused("proof by another key", whoami(token, proof(t, other, "GET", e2eOrigin+whoamiPath, token)), 401, "ERR_INVALID_DPOP_PROOF", "invalid_dpop_proof")
	wantRefused("proof for another URL", whoami(token, proof(t, device, "GET", e2eOrigin+slowPath, token)), 401, "ERR_INVALID_DPOP_PROOF", "invalid_dpop_proof")
	wantRefused("proof for another method", whoami(token, proof(t, device, "POST", e2eOrigin+whoamiPath, token)), 401, "ERR_INVALID_DPOP_PROOF", "invalid_dpop_proof")
	wantRefused("proof without ath", whoami(token, proof(t, device, "GET", e2eOrigin+whoamiPath, "")), 401, "ERR_INVALID_DPOP_PROOF", "invalid_dpop_proof")
	wantRefused("no proof", edgeGet(t, edgeBase, whoamiPath, map[string]string{"Authorization": "DPoP " + token}), 401, "ERR_INVALID_DPOP_PROOF", "invalid_dpop_proof")
	wantRefused("bearer scheme", edgeGet(t, edgeBase, whoamiPath, map[string]string{"Authorization": "Bearer " + token, "DPoP": proof(t, device, "GET", e2eOrigin+whoamiPath, token)}), 401, "ERR_UNAUTHENTICATED", "")
	if n := reached.Load(); n != 1 {
		t.Fatalf("the protected handler ran %d times; want 1 (the accepted request only)", n)
	}
	// A fresh proof is accepted again.
	if r := whoami(token, proof(t, device, "GET", e2eOrigin+whoamiPath, token)); r.code != 200 || reached.Load() != 2 {
		t.Fatalf("fresh proof: %d %s", r.code, r.raw)
	}

	// 6b. The production route, GET /v1/me, with identity's token: the verified
	// subject and device binding, and nothing else; its proof replayed is
	// refused.
	meProof := proof(t, device, "GET", e2eOrigin+httpapi.MePath, token)
	meHdr := map[string]string{"Authorization": "DPoP " + token, "DPoP": meProof, "X-User-ID": "attacker"}
	if r := edgeGet(t, edgeBase, httpapi.MePath+"?subject=attacker", meHdr); r.code != 200 || r.body["subject"] != tokClaims.Sub ||
		r.body["deviceKeyThumbprint"] != jkt || len(r.body) != 2 || r.hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("GET /v1/me: %d %s", r.code, r.raw)
	}
	wantRefused("/v1/me replayed proof", edgeGet(t, edgeBase, httpapi.MePath, meHdr), 401, "ERR_INVALID_DPOP_PROOF", "invalid_dpop_proof")
	wantRefused("/v1/me without credentials", edgeGet(t, edgeBase, httpapi.MePath, nil), 401, "ERR_UNAUTHENTICATED", "")

	// 7. Shutdown with an authenticated request IN FLIGHT.
	type result struct {
		code int
		body string
		err  error
	}
	inflight := make(chan result, 1)
	slowProof := proof(t, device, "GET", e2eOrigin+slowPath, token)
	go func() {
		req, _ := http.NewRequest("GET", edgeBase+slowPath, http.NoBody)
		req.Header.Set("Authorization", "DPoP "+token)
		req.Header.Set("DPoP", slowProof)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			inflight <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		inflight <- result{code: resp.StatusCode, body: string(b)}
	}()
	select {
	case <-entered: // authenticated (replay-checked in PostgreSQL) and now blocked
	case r := <-inflight:
		t.Fatalf("the slow request finished before blocking: %+v", r)
	case <-time.After(20 * time.Second):
		t.Fatal("the slow request never reached its handler")
	}
	cancel()
	waitFor(t, "readiness to fail", func() bool { return statusOf(edgeReady) == http.StatusServiceUnavailable })
	// The listener stops accepting only when HTTPRunner.Shutdown begins — after
	// the drain delay — and Shutdown then waits for the blocked handler.
	waitFor(t, "the edge listener to close", func() bool { return !dials(cfg.HTTPPort) })
	if ev := events.list(); len(ev) != 0 {
		t.Fatalf("events before the handler was released: %v", ev)
	}
	close(release)
	select {
	case r := <-inflight:
		if r.err != nil || r.code != 200 || r.body != "finished during shutdown" {
			t.Errorf("in-flight request = %+v; want 200, drained", r)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the in-flight request got no response")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("edge service: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("edge did not stop")
	}
	if ev := events.list(); len(ev) != 2 || ev[0] != "in-flight handler returned" || ev[1] != "replay-store pool closed" {
		t.Errorf("shutdown order = %v", ev)
	}
	if err := realRT.CheckDatabase(context.Background()); err == nil {
		t.Error("the replay-store pool is still open after shutdown")
	}
	if dials(cfg.HTTPPort) || dials(cfg.HealthPort) {
		t.Error("an edge listener is still open after shutdown")
	}

	// 8. The closed store fails closed: 503, handler not run.
	before := reached.Load()
	rec := &recorder{hdr: http.Header{}, code: 200}
	req, _ := http.NewRequest("GET", whoamiPath, http.NoBody)
	req.Header.Set("Authorization", "DPoP "+token)
	req.Header.Set("DPoP", proof(t, device, "GET", e2eOrigin+whoamiPath, token))
	applicationHandler(realRT.Authenticator(), routes).ServeHTTP(rec, req)
	if rec.code != http.StatusServiceUnavailable || reached.Load() != before {
		t.Errorf("after the pool closed: %d, handler ran %d more", rec.code, reached.Load()-before)
	}

	// 9. identity stops cleanly, and nothing secret was logged by either side.
	id.stop(t)
	logs := edgeLogs.String() + id.stderr.String()
	for name, s := range map[string]string{"access token": token, "proof": p1, "OTP code": otp, "DSN": dsn} {
		if strings.Contains(logs, s) {
			t.Errorf("the %s appears in a log", name)
		}
	}
}

type recorder struct {
	hdr  http.Header
	code int
	body bytes.Buffer
}

func (r *recorder) Header() http.Header         { return r.hdr }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *recorder) WriteHeader(code int)        { r.code = code }
