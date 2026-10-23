package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/bootstrap"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/httpapi"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

const (
	testEnrolURL   = "https://identity.example.test/v1/otp/validate"
	testRefreshURL = "https://identity.example.test/v1/token/refresh"
	testNumber     = "+919876543210"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitListening(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond); err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("nothing listening on %d", port)
}

func lifecycleConfig(t *testing.T) platform.ServiceConfig {
	t.Helper()
	return platform.ServiceConfig{
		Name: serviceName, Version: "test", HTTPPort: freePort(t), HealthPort: freePort(t),
		ShutdownTimeout: 3 * time.Second, ReadHeaderTimeout: 5 * time.Second,
	}
}

// Inactive identity: a real service runs, answers health, and binds NO
// application port.
func TestLifecycle_InactiveIdentityBindsNoApplicationPort(t *testing.T) {
	clearIdentityEnv(t)
	cfg := lifecycleConfig(t)
	health := platform.NewHealth(cfg)
	svc := platform.NewService(cfg, quiet, health)
	if err := configureIdentity(context.Background(), cfg, svc, health, quiet); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	waitListening(t, cfg.HealthPort)
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.HTTPPort), 300*time.Millisecond); err == nil {
		c.Close()
		t.Error("an inactive identity bound the application port")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("service did not stop")
	}
}

// ---------------------------------------------------------------------------
// Real PostgreSQL
// ---------------------------------------------------------------------------

type codeSender struct {
	mu    sync.Mutex
	codes map[string]string
}

func (c *codeSender) SendOtp(_ context.Context, number, code string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.codes[number] = code
	return nil
}

func (c *codeSender) get(number string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.codes[number]
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func dpopProof(t *testing.T, k *ecdsa.PrivateKey, jti, htu string) string {
	t.Helper()
	pt, _ := k.PublicKey.Bytes()
	hdr, _ := json.Marshal(map[string]any{
		"typ": "dpop+jwt", "alg": "ES256",
		"jwk": map[string]any{"kty": "EC", "crv": "P-256", "x": b64(pt[1:33]), "y": b64(pt[33:65])},
	})
	cl, _ := json.Marshal(map[string]any{"jti": jti, "htm": "POST", "htu": htu, "iat": time.Now().Unix()})
	input := b64(hdr) + "." + b64(cl)
	d := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, k, d[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + b64(sig)
}

func call(t *testing.T, base, path, body, proof string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", base+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if proof != "" {
		req.Header.Set("DPoP", proof)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// activate migrates a fresh schema, sets the full configuration and returns
// the captured runtime alongside registrations into the given service.
func activate(t *testing.T, cfg platform.ServiceConfig, svc *platform.Service, health *platform.Health) *bootstrap.Runtime {
	t.Helper()
	dsn := freshSchemaDSN(t)
	clearIdentityEnv(t)
	t.Setenv("CS_IDENTITY_DATABASE_DSN_FILE", writeFile(t, "dsn", []byte(dsn)))
	setEnvironment(t, ptr("development")) // the disposable test database is plaintext
	if err := runMigrate(context.Background(), &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	setFullIdentityEnv(t, dsn)
	var rt *bootstrap.Runtime
	orig := openIdentity
	openIdentity = func(ctx context.Context, c bootstrap.ActivationConfig, now func() time.Time) (*bootstrap.Runtime, error) {
		r, err := orig(ctx, c, now)
		rt = r
		return r, err
	}
	t.Cleanup(func() { openIdentity = orig })
	if err := configureIdentity(context.Background(), cfg, svc, health, quiet); err != nil {
		t.Fatalf("activation: %v", err)
	}
	return rt
}

// The activated service end to end over real TCP: readiness, request,
// enrolment, refresh, reuse — then shutdown with a request IN FLIGHT, which
// must still be answered from the database: the listener drains before the
// pool closes. Afterwards the pool is closed and failures leak nothing.
func TestLifecycle_ActivatedHTTPEndToEndAndOrderlyShutdown(t *testing.T) {
	cfg := lifecycleConfig(t)
	health := platform.NewHealth(cfg)
	svc := platform.NewService(cfg, quiet, health)
	rt := activate(t, cfg, svc, health)
	sender := &codeSender{codes: map[string]string{}}
	rt.Service.SetOtpSender(sender) // test-only: production configures none

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	waitListening(t, cfg.HTTPPort)
	waitListening(t, cfg.HealthPort)
	base := fmt.Sprintf("http://127.0.0.1:%d", cfg.HTTPPort)

	ready, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/readyz", cfg.HealthPort))
	if err != nil {
		t.Fatal(err)
	}
	var rb struct {
		Status string `json:"status"`
		Checks []struct{ Name, Status string }
	}
	_ = json.NewDecoder(ready.Body).Decode(&rb)
	ready.Body.Close()
	if ready.StatusCode != 200 || rb.Status != "healthy" || len(rb.Checks) != 2 {
		t.Errorf("readiness = %d %+v", ready.StatusCode, rb)
	}

	code, body := call(t, base, routes.OTPRequest, `{"msisdn":"`+testNumber+`"}`, "")
	if code != 202 {
		t.Fatalf("OTP request: %d %v", code, body)
	}
	reqID := body["requestId"].(string)
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	code, body = call(t, base, routes.Enrollment, fmt.Sprintf(`{"requestId":%q,"code":%q}`, reqID, sender.get(testNumber)), dpopProof(t, k, "e1", testEnrolURL))
	if code != 200 || body["tokenType"] != "DPoP" {
		t.Fatalf("enrol: %d %v", code, body)
	}
	sid, rtok := body["sessionId"].(string), body["refreshToken"].(string)
	code, body = call(t, base, routes.Refresh, fmt.Sprintf(`{"sessionId":%q,"refreshToken":%q}`, sid, rtok), dpopProof(t, k, "r1", testRefreshURL))
	if code != 200 || body["refreshToken"] == rtok {
		t.Fatalf("refresh: %d %v", code, body)
	}
	if code, body = call(t, base, routes.Refresh, fmt.Sprintf(`{"sessionId":%q,"refreshToken":%q}`, sid, rtok), dpopProof(t, k, "r2", testRefreshURL)); code != 401 {
		t.Errorf("reuse over HTTP: %d %v", code, body)
	}

	// GET /metrics on the HEALTH port shows the reuse as a security event and
	// as a refused refresh, through the real lifecycle.
	scrape, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", cfg.HealthPort))
	if err != nil {
		t.Fatal(err)
	}
	exposition, _ := io.ReadAll(scrape.Body)
	scrape.Body.Close()
	for _, want := range []string{
		`identity_security_events_total{type="identity.refresh.token_reused"} 1`,
		`identity_auth_requests_total{endpoint="token_refresh",outcome="invalid_token"} 1`,
		`identity_auth_requests_total{endpoint="otp_validate",outcome="success"} 1`,
	} {
		if scrape.StatusCode != http.StatusOK || !strings.Contains(string(exposition), want) {
			t.Errorf("metrics scrape (%d) lacks %q:\n%s", scrape.StatusCode, want, exposition)
		}
	}

	// A request in flight across shutdown: headers and half the body now, the
	// rest after shutdown has begun.
	reqID2 := ""
	if code, body = call(t, base, routes.OTPRequest, `{"msisdn":"`+testNumber+`"}`, ""); code == 202 {
		reqID2 = body["requestId"].(string)
	} else {
		t.Fatalf("second OTP request: %d %v", code, body)
	}
	wrong := "000000"
	if sender.get(testNumber) == wrong {
		wrong = "111111"
	}
	payload := fmt.Sprintf(`{"requestId":%q,"code":%q}`, reqID2, wrong)
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.HTTPPort))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nDPoP: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		routes.Enrollment, dpopProof(t, k, "inflight", testEnrolURL), len(payload), payload[:10])
	time.Sleep(100 * time.Millisecond)

	cancel() // begin shutdown: readiness off, drain, then runners in reverse
	time.Sleep(cfg.ShutdownTimeout/5 + 500*time.Millisecond)
	if c, dialTimeoutErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.HTTPPort), 300*time.Millisecond); dialTimeoutErr == nil {
		c.Close()
		t.Error("the listener still accepts new connections during shutdown")
	}
	if _, writeErr := conn.Write([]byte(payload[10:])); writeErr != nil {
		t.Fatalf("finishing the in-flight request: %v", writeErr)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("the in-flight request got no response: %v", err)
	}
	var inflight map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&inflight)
	resp.Body.Close()
	if resp.StatusCode != 401 || inflight["code"] != "ERR_INVALID_OTP" {
		t.Errorf("in-flight request = %d %v; want 401 ERR_INVALID_OTP, answered from the still-open database", resp.StatusCode, inflight)
	}

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("service did not stop")
	}
	if err := rt.CheckDatabase(context.Background()); err == nil {
		t.Error("the pool is still open after shutdown")
	}

	// With the pool closed, the API fails closed and says nothing about why.
	h := httpapi.New(rt.Service, quiet, nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", routes.Enrollment, strings.NewReader(fmt.Sprintf(`{"requestId":%q,"code":"123456"}`, reqID2)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("DPoP", dpopProof(t, k, "after", testEnrolURL))
	h.ServeHTTP(rec, req)
	if rec.Code < 500 || strings.Contains(rec.Body.String(), "accessToken") || strings.Contains(strings.ToLower(rec.Body.String()), "pool") {
		t.Errorf("after the pool closed: %d %s", rec.Code, rec.Body.String())
	}
}

// Two instances on one database, over HTTP: enrol on A; the same proof is a
// replay on B; B refreshes A's session; A sees the rotated-away token as reuse.
func TestLifecycle_TwoInstancesOverHTTP(t *testing.T) {
	dsn := freshSchemaDSN(t)
	clearIdentityEnv(t)
	t.Setenv("CS_IDENTITY_DATABASE_DSN_FILE", writeFile(t, "dsn", []byte(dsn)))
	setEnvironment(t, ptr("development")) // the disposable test database is plaintext
	if err := runMigrate(context.Background(), &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	setFullIdentityEnv(t, dsn)
	c, _, err := bootstrap.LoadActivation()
	if err != nil {
		t.Fatal(err)
	}
	a, err := bootstrap.Open(context.Background(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := bootstrap.Open(context.Background(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	sender := &codeSender{codes: map[string]string{}}
	a.Service.SetOtpSender(sender)
	sa := httptest.NewServer(httpapi.New(a.Service, quiet, nil, nil))
	defer sa.Close()
	sb := httptest.NewServer(httpapi.New(b.Service, quiet, nil, nil))
	defer sb.Close()

	code, body := call(t, sa.URL, routes.OTPRequest, `{"msisdn":"`+testNumber+`"}`, "")
	if code != 202 {
		t.Fatalf("%d %v", code, body)
	}
	reqID := body["requestId"].(string)
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	p := dpopProof(t, k, "e1", testEnrolURL)
	code, body = call(t, sa.URL, routes.Enrollment, fmt.Sprintf(`{"requestId":%q,"code":%q}`, reqID, sender.get(testNumber)), p)
	if code != 200 {
		t.Fatalf("enrol on A: %d %v", code, body)
	}
	sid, rtok := body["sessionId"].(string), body["refreshToken"].(string)
	if code, body = call(t, sb.URL, routes.Enrollment, fmt.Sprintf(`{"requestId":%q,"code":"123456"}`, reqID), p); code != 400 || body["code"] != "ERR_INVALID_DPOP_PROOF" {
		t.Errorf("the proof replayed on B: %d %v", code, body)
	}
	code, body = call(t, sb.URL, routes.Refresh, fmt.Sprintf(`{"sessionId":%q,"refreshToken":%q}`, sid, rtok), dpopProof(t, k, "r1", testRefreshURL))
	if code != 200 {
		t.Fatalf("refresh on B: %d %v", code, body)
	}
	if code, _ = call(t, sa.URL, routes.Refresh, fmt.Sprintf(`{"sessionId":%q,"refreshToken":%q}`, sid, rtok), dpopProof(t, k, "r2", testRefreshURL)); code != 401 {
		t.Errorf("the rotated-away token on A: %d", code)
	}
	_ = strconv.Itoa // keep imports stable if assertions change
}
