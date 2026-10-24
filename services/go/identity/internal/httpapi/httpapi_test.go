package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

// Test-only values; none is, or may become, a production default.
const (
	host       = "https://identity.example.test"
	enrolURL   = host + routes.Enrollment
	refreshURL = host + routes.Refresh
	msisdn     = "+919876543210"
)

// fakeSender is the test-only OtpSender: it records what it was asked to
// deliver. No production code constructs one.
type fakeSender struct {
	mu    sync.Mutex
	codes map[string]string
	fail  bool
}

func (f *fakeSender) SendOtp(_ context.Context, number, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("provider says: cannot reach " + number)
	}
	f.codes[number] = code
	return nil
}

func (f *fakeSender) code(number string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.codes[number]
}

// countingRepo counts challenge writes and deletes; failGet makes GetOtp fail
// as a store would.
type countingRepo struct {
	*app.MemoryIdentityRepository
	mu              sync.Mutex
	saves, deletes  int
	sessionSaves    int
	failGet         bool
	failGetWithText string
}

func (c *countingRepo) SaveOtp(ctx context.Context, ch *app.OtpChallenge) error {
	c.mu.Lock()
	c.saves++
	c.mu.Unlock()
	return c.MemoryIdentityRepository.SaveOtp(ctx, ch)
}

func (c *countingRepo) SaveSession(ctx context.Context, sess *app.UserSession) error {
	c.mu.Lock()
	c.sessionSaves++
	c.mu.Unlock()
	return c.MemoryIdentityRepository.SaveSession(ctx, sess)
}

func (c *countingRepo) sessionCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionSaves
}

func (c *countingRepo) DeleteOtp(ctx context.Context, id string) error {
	c.mu.Lock()
	c.deletes++
	c.mu.Unlock()
	return c.MemoryIdentityRepository.DeleteOtp(ctx, id)
}

func (c *countingRepo) GetOtp(ctx context.Context, id string) (*app.OtpChallenge, error) {
	if c.failGet {
		return nil, errors.New(c.failGetWithText)
	}
	return c.MemoryIdentityRepository.GetOtp(ctx, id)
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type rig struct {
	h        http.Handler
	svc      *app.IdentityService
	repo     *countingRepo
	sender   *fakeSender
	verifier *accesstoken.Verifier
	logs     *syncBuf
	bodies   []string // every response body, for leak checks
	m        *Metrics
	tracer   runtime.Tracer
}

func newRig(t *testing.T, withSender bool) *rig {
	t.Helper()
	repo := &countingRepo{MemoryIdentityRepository: app.NewMemoryIdentityRepository()}
	svc := app.NewIdentityService(repo, []byte("0123456789abcdef0123456789abcdef"))
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := accesstoken.NewSigner(accesstoken.SignerConfig{
		Issuer: "urn:callscreen:identity:test", Audience: "urn:callscreen:edge-api:test", KeyID: "test-key-1", Key: priv,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetAccessTokenSigner(signer)
	replay := dpop.NewMemoryReplayStore()
	for htu, set := range map[string]func(*dpop.Verifier){enrolURL: svc.SetDeviceProofVerifier, refreshURL: svc.SetRefreshProofVerifier} {
		v, err := dpop.NewVerifier(dpop.Config{HTU: htu, MaxAge: 2 * time.Minute, FutureSkew: time.Minute, Replay: replay})
		if err != nil {
			t.Fatal(err)
		}
		set(v)
	}
	r := &rig{svc: svc, repo: repo, logs: &syncBuf{}}
	if withSender {
		r.sender = &fakeSender{codes: map[string]string{}}
		svc.SetOtpSender(r.sender)
	}
	r.verifier, _ = accesstoken.NewVerifier(accesstoken.VerifierConfig{
		Issuer: "urn:callscreen:identity:test", Audience: "urn:callscreen:edge-api:test", Keys: map[string]ed25519.PublicKey{"test-key-1": pub},
	})
	r.m = NewMetrics()
	r.h = New(svc, slog.New(slog.NewTextHandler(r.logs, nil)), r.m, r.tracer)
	return r
}

type resp struct {
	code int
	hdr  http.Header
	body map[string]any
	raw  string
}

func (r *rig) do(t *testing.T, method, path, body string, hdr map[string][]string) resp {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, vs := range hdr {
		req.Header.Del(k)
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

func (r *rig) post(t *testing.T, path, body, proof string) resp {
	t.Helper()
	hdr := map[string][]string{}
	if proof != "" {
		hdr["DPoP"] = []string{proof}
	}
	return r.do(t, "POST", path, body, hdr)
}

func wantProblem(t *testing.T, got resp, status int, code string) {
	t.Helper()
	if got.code != status || got.body["code"] != code {
		t.Fatalf("got %d %v, want %d %s", got.code, got.raw, status, code)
	}
	// FIXED text: exactly the documented title and detail for the code,
	// with nothing request- or error-specific added.
	if want := problems[code]; got.body["detail"] != want.detail || got.body["title"] != want.title {
		t.Errorf("%s: title/detail = %q/%q, want the fixed %q/%q", code, got.body["title"], got.body["detail"], want.title, want.detail)
	}
	if ct := got.hdr.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if got.hdr.Get("Cache-Control") != "no-store" || got.hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing hardening headers: %v", got.hdr)
	}
	if got.body["traceId"] != got.hdr.Get("X-Correlation-ID") || got.hdr.Get("X-Correlation-ID") == "" {
		t.Errorf("problem traceId %v does not match X-Correlation-ID %q", got.body["traceId"], got.hdr.Get("X-Correlation-ID"))
	}
	for _, k := range []string{"accessToken", "refreshToken", "sessionId", "requestId"} {
		if _, ok := got.body[k]; ok {
			t.Errorf("an error response carries %s", k)
		}
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func proof(t *testing.T, signer, named *ecdsa.PrivateKey, jti, htm, htu string, iat time.Time) string {
	t.Helper()
	pt, _ := named.PublicKey.Bytes()
	hdr, _ := json.Marshal(map[string]any{
		"typ": "dpop+jwt", "alg": "ES256",
		"jwk": map[string]any{"kty": "EC", "crv": "P-256", "x": b64(pt[1:33]), "y": b64(pt[33:65])},
	})
	cl, _ := json.Marshal(map[string]any{"jti": jti, "htm": htm, "htu": htu, "iat": iat.Unix()})
	input := b64(hdr) + "." + b64(cl)
	d := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, signer, d[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + b64(sig)
}

func deviceKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// requestCode asks for a code and returns the request id and the code the
// fake sender received.
func (r *rig) requestCode(t *testing.T) (string, string) {
	t.Helper()
	got := r.post(t, routes.OTPRequest, `{"msisdn":"`+msisdn+`"}`, "")
	if got.code != http.StatusAccepted {
		t.Fatalf("OTP request: %d %s", got.code, got.raw)
	}
	return got.body["requestId"].(string), r.sender.code(msisdn)
}

func enrolBody(reqID, code string) string {
	return fmt.Sprintf(`{"requestId":%q,"code":%q}`, reqID, code)
}

func refreshBody(sessionID, token string) string {
	return fmt.Sprintf(`{"sessionId":%q,"refreshToken":%q}`, sessionID, token)
}

// ---------------------------------------------------------------------------
// Routing and request shape
// ---------------------------------------------------------------------------

func TestHTTP_RoutingMethodsAndShape(t *testing.T) {
	r := newRig(t, true)
	wantProblem(t, r.do(t, "POST", "/v1/otp", `{}`, nil), 404, "ERR_NOT_FOUND")
	wantProblem(t, r.do(t, "POST", "/", `{}`, nil), 404, "ERR_NOT_FOUND")
	wantProblem(t, r.do(t, "POST", routes.Enrollment+"/", `{}`, nil), 404, "ERR_NOT_FOUND")
	for _, path := range []string{routes.OTPRequest, routes.Enrollment, routes.Refresh} {
		for _, m := range []string{"GET", "PUT", "DELETE", "PATCH", "OPTIONS"} {
			got := r.do(t, m, path, `{}`, nil)
			wantProblem(t, got, 405, "ERR_METHOD_NOT_ALLOWED")
			if got.hdr.Get("Allow") != "POST" {
				t.Errorf("%s %s: Allow = %q", m, path, got.hdr.Get("Allow"))
			}
			if got.hdr.Get("Access-Control-Allow-Origin") != "" {
				t.Error("a CORS header was sent")
			}
		}
	}

	for name, ct := range map[string][]string{
		"missing": {""}, "text": {"text/plain"}, "form": {"application/x-www-form-urlencoded"},
		"latin1": {"application/json; charset=latin1"}, "extra param": {"application/json; boundary=x"},
	} {
		got := r.do(t, "POST", routes.OTPRequest, `{"msisdn":"`+msisdn+`"}`, map[string][]string{"Content-Type": ct})
		if name == "missing" {
			got = r.do(t, "POST", routes.OTPRequest, `{"msisdn":"`+msisdn+`"}`, map[string][]string{"Content-Type": nil})
		}
		wantProblem(t, got, 415, "ERR_UNSUPPORTED_MEDIA_TYPE")
	}
	ok := r.do(t, "POST", routes.OTPRequest, `{"msisdn":"`+msisdn+`"}`, map[string][]string{"Content-Type": {"application/json; charset=UTF-8"}})
	if ok.code != 202 {
		t.Errorf("charset=UTF-8 refused: %d", ok.code)
	}

	wantProblem(t, r.post(t, routes.OTPRequest, `{"msisdn":"`+strings.Repeat("1", MaxBodyBytes)+`"}`, ""), 413, "ERR_BODY_TOO_LARGE")
	for name, body := range map[string]string{
		"empty": ``, "not json": `msisdn=+91`, "array": `[{"msisdn":"+919876543210"}]`, "null": `null`,
		"unknown field": `{"msisdn":"+919876543210","admin":true}`, "wrong type": `{"msisdn":919876543210}`,
		"trailing": `{"msisdn":"+919876543210"}{}`, "truncated": `{"msisdn":"+9198`,
	} {
		got := r.post(t, routes.OTPRequest, body, "")
		if got.code != 400 || got.body["code"] != "ERR_MALFORMED_BODY" {
			t.Errorf("%s: %d %s", name, got.code, got.raw)
		}
	}
	wantProblem(t, r.post(t, routes.OTPRequest, `{}`, ""), 400, "ERR_INVALID_MSISDN")
	wantProblem(t, r.post(t, routes.OTPRequest, `{"msisdn":"919876543210"}`, ""), 400, "ERR_INVALID_MSISDN")
	wantProblem(t, r.post(t, routes.OTPRequest, `{"msisdn":"+91 98765 43210"}`, ""), 400, "ERR_INVALID_MSISDN")
}

// ---------------------------------------------------------------------------
// OTP request and delivery
// ---------------------------------------------------------------------------

// Without a delivery channel the endpoint refuses before anything happens: no
// challenge, no allowance spent.
func TestHTTP_OTPRequestWithoutDeliveryIsRefusedTruthfully(t *testing.T) {
	r := newRig(t, false)
	for i := 0; i < 5; i++ {
		wantProblem(t, r.post(t, routes.OTPRequest, `{"msisdn":"`+msisdn+`"}`, ""), 503, "ERR_OTP_DELIVERY_UNAVAILABLE")
	}
	if r.repo.saves != 0 {
		t.Errorf("%d challenges created with no way to deliver them", r.repo.saves)
	}
	r.sender = &fakeSender{codes: map[string]string{}}
	r.svc.SetOtpSender(r.sender)
	for i := 0; i < 3; i++ {
		r.requestCode(t) // the full allowance is still there
	}
}

func TestHTTP_OTPRequestDeliversAndLimits(t *testing.T) {
	r := newRig(t, true)
	got := r.post(t, routes.OTPRequest, `{"msisdn":"`+msisdn+`"}`, "")
	if got.code != 202 || !eq(keys(got.body), []string{"expiresInSeconds", "requestId"}) || got.body["expiresInSeconds"] != float64(300) {
		t.Fatalf("got %d %s", got.code, got.raw)
	}
	if c := r.sender.code(msisdn); len(c) != 6 {
		t.Errorf("the sender received %q, want a 6-digit code", c)
	}
	if strings.Contains(got.raw, r.sender.code(msisdn)) {
		t.Error("the response carries the code")
	}
	r.requestCode(t)
	r.requestCode(t)
	limited := r.post(t, routes.OTPRequest, `{"msisdn":"`+msisdn+`"}`, "")
	wantProblem(t, limited, 429, "ERR_RATE_LIMITED")
	ra, err := strconv.Atoi(limited.hdr.Get("Retry-After"))
	if err != nil || ra < 890 || ra > 900 {
		t.Errorf("Retry-After = %q, want about 900 (the oldest request leaves the 15-minute window)", limited.hdr.Get("Retry-After"))
	}
}

func TestHTTP_OTPDeliveryFailureWithdrawsTheChallenge(t *testing.T) {
	r := newRig(t, true)
	r.sender.fail = true
	wantProblem(t, r.post(t, routes.OTPRequest, `{"msisdn":"`+msisdn+`"}`, ""), 503, "ERR_OTP_DELIVERY_FAILED")
	if r.repo.saves != 1 || r.repo.deletes != 1 {
		t.Errorf("saves %d deletes %d: the undelivered challenge was not withdrawn", r.repo.saves, r.repo.deletes)
	}
}

func eq(a, b []string) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

// ---------------------------------------------------------------------------
// Enrolment
// ---------------------------------------------------------------------------

func TestHTTP_EnrolmentIssuesOnlyTheDocumentedFields(t *testing.T) {
	r := newRig(t, true)
	reqID, code := r.requestCode(t)
	k := deviceKey(t)
	got := r.post(t, routes.Enrollment, enrolBody(reqID, code), proof(t, k, k, "e1", "POST", enrolURL, time.Now()))
	if got.code != 200 || !eq(keys(got.body), []string{"accessToken", "expiresIn", "refreshToken", "sessionId", "tokenType"}) {
		t.Fatalf("got %d %s", got.code, got.raw)
	}
	if got.body["tokenType"] != "DPoP" || got.body["expiresIn"] != float64(900) || got.hdr.Get("Cache-Control") != "no-store" {
		t.Errorf("token response: %s %v", got.raw, got.hdr)
	}
	claims, err := r.verifier.Verify(got.body["accessToken"].(string))
	jkt, _ := accesstoken.ThumbprintP256(&k.PublicKey)
	if err != nil || claims.Confirmation.JKT != jkt {
		t.Errorf("the access token: (%+v, %v), want bound to the device", claims, err)
	}
	if strings.Contains(got.raw, msisdn) || strings.Contains(got.raw, code) {
		t.Error("the response carries the number or the code")
	}
}

// Every bad proof is 400, issues nothing, and leaves the challenge untouched:
// the right code with a good proof still works afterwards.
func TestHTTP_EnrolmentProofFailures(t *testing.T) {
	k, other := deviceKey(t), deviceKey(t)
	now := time.Now()
	for name, mk := range map[string]func(t *testing.T) map[string][]string{
		"missing": func(*testing.T) map[string][]string { return nil },
		"empty":   func(*testing.T) map[string][]string { return map[string][]string{"DPoP": {""}} },
		"two headers": func(t *testing.T) map[string][]string {
			p := proof(t, k, k, "x", "POST", enrolURL, now)
			return map[string][]string{"DPoP": {p, p}}
		},
		"oversized": func(*testing.T) map[string][]string {
			return map[string][]string{"DPoP": {strings.Repeat("a", dpop.MaxProofBytes+1)}}
		},
		"garbage": func(*testing.T) map[string][]string { return map[string][]string{"DPoP": {"not.a.proof"}} },
		"invalid signature": func(t *testing.T) map[string][]string {
			return map[string][]string{"DPoP": {proof(t, other, k, "x", "POST", enrolURL, now)}}
		},
		"wrong htm": func(t *testing.T) map[string][]string {
			return map[string][]string{"DPoP": {proof(t, k, k, "x", "GET", enrolURL, now)}}
		},
		"wrong htu (refresh URL)": func(t *testing.T) map[string][]string {
			return map[string][]string{"DPoP": {proof(t, k, k, "x", "POST", refreshURL, now)}}
		},
		"wrong htu (other host)": func(t *testing.T) map[string][]string {
			return map[string][]string{"DPoP": {proof(t, k, k, "x", "POST", "https://evil.example.test"+routes.Enrollment, now)}}
		},
		"stale": func(t *testing.T) map[string][]string {
			return map[string][]string{"DPoP": {proof(t, k, k, "x", "POST", enrolURL, now.Add(-time.Hour))}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, true)
			reqID, code := r.requestCode(t)
			got := r.do(t, "POST", routes.Enrollment, enrolBody(reqID, code), mk(t))
			wantProblem(t, got, 400, "ERR_INVALID_DPOP_PROOF")
			ok := r.post(t, routes.Enrollment, enrolBody(reqID, code), proof(t, k, k, "good", "POST", enrolURL, time.Now()))
			if ok.code != 200 {
				t.Errorf("the challenge did not survive a rejected proof: %d %s", ok.code, ok.raw)
			}
		})
	}

	t.Run("replayed", func(t *testing.T) {
		r := newRig(t, true)
		reqID, code := r.requestCode(t)
		p := proof(t, k, k, "once", "POST", enrolURL, time.Now())
		if got := r.post(t, routes.Enrollment, enrolBody(reqID, code), p); got.code != 200 {
			t.Fatal(got.raw)
		}
		reqID2, code2 := r.requestCode(t)
		wantProblem(t, r.post(t, routes.Enrollment, enrolBody(reqID2, code2), p), 400, "ERR_INVALID_DPOP_PROOF")
	})

	t.Run("a malformed body never spends the proof", func(t *testing.T) {
		r := newRig(t, true)
		reqID, code := r.requestCode(t)
		p := proof(t, k, k, "kept", "POST", enrolURL, time.Now())
		for _, bad := range []string{enrolBody(reqID, "12345"), enrolBody(reqID, "12345a"), enrolBody("req_x", code), `{"requestId":"` + reqID + `"}`} {
			got := r.post(t, routes.Enrollment, bad, p)
			if got.code != 400 {
				t.Fatalf("%s: %d", bad, got.code)
			}
		}
		if got := r.post(t, routes.Enrollment, enrolBody(reqID, code), p); got.code != 200 {
			t.Errorf("the proof was spent by a malformed request: %d %s", got.code, got.raw)
		}
	})
}

func TestHTTP_EnrolmentCodeFailuresAndLockout(t *testing.T) {
	r := newRig(t, true)
	k := deviceKey(t)
	n := 0
	fresh := func() string { n++; return proof(t, k, k, fmt.Sprintf("p%d", n), "POST", enrolURL, time.Now()) }
	reqID, code := r.requestCode(t)
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	wantProblem(t, r.post(t, routes.Enrollment, enrolBody(reqID, wrong), fresh()), 401, "ERR_INVALID_OTP")
	wantProblem(t, r.post(t, routes.Enrollment, enrolBody("req_"+strings.Repeat("0", 64), wrong), fresh()), 401, "ERR_INVALID_OTP")
	// Five failures (across challenges) lock the number; the right code is then refused.
	reqID2, code2 := r.requestCode(t)
	for i := 0; i < 2; i++ {
		_ = r.post(t, routes.Enrollment, enrolBody(reqID, wrong), fresh())
	}
	for i := 0; i < 2; i++ {
		_ = r.post(t, routes.Enrollment, enrolBody(reqID2, wrongFor(code2)), fresh())
	}
	locked := r.post(t, routes.Enrollment, enrolBody(reqID2, code2), fresh())
	wantProblem(t, locked, 429, "ERR_RATE_LIMITED")
	if ra, err := strconv.Atoi(locked.hdr.Get("Retry-After")); err != nil || ra < 890 || ra > 900 {
		t.Errorf("Retry-After = %q, want about 900 (the 15-minute lockout)", locked.hdr.Get("Retry-After"))
	}
}

func wrongFor(code string) string {
	if code == "000000" {
		return "111111"
	}
	return "000000"
}

// ---------------------------------------------------------------------------
// Refresh
// ---------------------------------------------------------------------------

func (r *rig) enrol(t *testing.T, k *ecdsa.PrivateKey) (string, string) {
	t.Helper()
	reqID, code := r.requestCode(t)
	got := r.post(t, routes.Enrollment, enrolBody(reqID, code), proof(t, k, k, "enrol-"+reqID[4:12], "POST", enrolURL, time.Now()))
	if got.code != 200 {
		t.Fatalf("enrol: %d %s", got.code, got.raw)
	}
	return got.body["sessionId"].(string), got.body["refreshToken"].(string)
}

func TestHTTP_RefreshRotatesAndDetectsReuse(t *testing.T) {
	r := newRig(t, true)
	k := deviceKey(t)
	sid, rt := r.enrol(t, k)
	got := r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, "r1", "POST", refreshURL, time.Now()))
	if got.code != 200 || !eq(keys(got.body), []string{"accessToken", "expiresIn", "refreshToken", "tokenType"}) {
		t.Fatalf("got %d %s", got.code, got.raw)
	}
	rt2 := got.body["refreshToken"].(string)
	if rt2 == rt {
		t.Fatal("not rotated")
	}
	claims, err := r.verifier.Verify(got.body["accessToken"].(string))
	jkt, _ := accesstoken.ThumbprintP256(&k.PublicKey)
	if err != nil || claims.Confirmation.JKT != jkt {
		t.Errorf("refreshed token: (%+v, %v)", claims, err)
	}
	// The old token is reuse: refused, and the session is revoked — the
	// current token stops working too.
	wantProblem(t, r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, "r2", "POST", refreshURL, time.Now())), 401, "ERR_INVALID_TOKEN")
	wantProblem(t, r.post(t, routes.Refresh, refreshBody(sid, rt2), proof(t, k, k, "r3", "POST", refreshURL, time.Now())), 401, "ERR_INVALID_TOKEN")
}

func TestHTTP_RefreshProofFailuresRotateNothing(t *testing.T) {
	k, other := deviceKey(t), deviceKey(t)
	now := time.Now()
	for name, mk := range map[string]func(t *testing.T) map[string][]string{
		"missing": func(*testing.T) map[string][]string { return nil },
		"another device key": func(t *testing.T) map[string][]string {
			return map[string][]string{"DPoP": {proof(t, other, other, "x", "POST", refreshURL, now)}}
		},
		"enrolment URL": func(t *testing.T) map[string][]string {
			return map[string][]string{"DPoP": {proof(t, k, k, "x", "POST", enrolURL, now)}}
		},
		"wrong htm": func(t *testing.T) map[string][]string {
			return map[string][]string{"DPoP": {proof(t, k, k, "x", "PUT", refreshURL, now)}}
		},
		"stale": func(t *testing.T) map[string][]string {
			return map[string][]string{"DPoP": {proof(t, k, k, "x", "POST", refreshURL, now.Add(-time.Hour))}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, true)
			sid, rt := r.enrol(t, k)
			wantProblem(t, r.do(t, "POST", routes.Refresh, refreshBody(sid, rt), mk(t)), 400, "ERR_INVALID_DPOP_PROOF")
			if got := r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, "after", "POST", refreshURL, time.Now())); got.code != 200 {
				t.Errorf("the refresh token did not survive a rejected proof: %d %s", got.code, got.raw)
			}
		})
	}
	t.Run("malformed body, proof unspent", func(t *testing.T) {
		r := newRig(t, true)
		sid, rt := r.enrol(t, k)
		p := proof(t, k, k, "kept", "POST", refreshURL, time.Now())
		for _, bad := range []string{refreshBody("sess_x", rt), refreshBody(sid, "short"), refreshBody(sid, strings.ToUpper(rt)), `{"sessionId":"` + sid + `"}`} {
			if got := r.post(t, routes.Refresh, bad, p); got.code != 400 {
				t.Fatalf("%s: %d", bad, got.code)
			}
		}
		if got := r.post(t, routes.Refresh, refreshBody(sid, rt), p); got.code != 200 {
			t.Errorf("the proof was spent by a malformed request: %d", got.code)
		}
	})
}

// An unenrolled session (only creatable through the unmounted legacy path) is
// refused here exactly as by the service: no proof makes it refreshable.
func TestHTTP_UnenrolledSessionsCannotRefresh(t *testing.T) {
	r := newRig(t, true)
	reqID, code := r.requestCode(t)
	sess, tok, err := r.svc.ValidateOtp(context.Background(), reqID, code)
	if err != nil || tok != "" {
		t.Fatalf("(%v, %q)", err, tok)
	}
	k := deviceKey(t)
	wantProblem(t, r.post(t, routes.Refresh, refreshBody(sess.SessionID, sess.RefreshToken), proof(t, k, k, "u1", "POST", refreshURL, time.Now())), 401, "ERR_INVALID_TOKEN")
}

// ---------------------------------------------------------------------------
// Failures, leaks and hardening
// ---------------------------------------------------------------------------

// A store failure is a fixed 500 with nothing from the store in it, and never
// a token.
func TestHTTP_StoreFailureLeaksNothing(t *testing.T) {
	r := newRig(t, true)
	reqID, code := r.requestCode(t)
	r.repo.failGet = true
	r.repo.failGetWithText = "pq: password authentication failed for user identity_owner at db.internal:5432"
	k := deviceKey(t)
	got := r.post(t, routes.Enrollment, enrolBody(reqID, code), proof(t, k, k, "s1", "POST", enrolURL, time.Now()))
	wantProblem(t, got, 500, "ERR_INTERNAL")
	for _, s := range []string{"identity_owner", "db.internal", "password", "pq:"} {
		if strings.Contains(got.raw, s) || strings.Contains(r.logs.String(), s) {
			t.Errorf("%q leaked", s)
		}
	}
}

// The service's error mapping, exhaustively: every sentinel to its fixed
// problem, and nil-without-a-token to a 500 rather than a success.
func TestHTTP_ErrorMapping(t *testing.T) {
	h := &handler{svc: nil, log: slog.New(slog.NewTextHandler(&syncBuf{}, nil)), now: time.Now}
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{&app.RateLimitError{RetryAt: time.Now().Add(time.Minute)}, 429, "ERR_RATE_LIMITED"},
		{app.ErrInvalidMSISDN, 400, "ERR_INVALID_MSISDN"},
		{fmt.Errorf("%w: %w", app.ErrInvalidDeviceProof, dpop.ErrReplayed), 400, "ERR_INVALID_DPOP_PROOF"},
		{app.ErrExpiredOtp, 401, "ERR_OTP_EXPIRED"},
		{app.ErrInvalidOtp, 401, "ERR_INVALID_OTP"},
		{app.ErrTokenReused, 401, "ERR_INVALID_TOKEN"},
		{fmt.Errorf("%w: revoking failed: %w", app.ErrTokenReused, errors.New("store down")), 401, "ERR_INVALID_TOKEN"},
		{app.ErrInvalidToken, 401, "ERR_INVALID_TOKEN"},
		{app.ErrSessionNotDeviceBound, 401, "ERR_INVALID_TOKEN"},
		{app.ErrDeviceProofRequired, 401, "ERR_INVALID_TOKEN"},
		{app.ErrOtpDeliveryFailed, 503, "ERR_OTP_DELIVERY_FAILED"},
		{fmt.Errorf("%w: x", dpop.ErrReplayUnavailable), 503, "ERR_TEMPORARILY_UNAVAILABLE"},
		{app.ErrEnrollmentUnavailable, 503, "ERR_TEMPORARILY_UNAVAILABLE"},
		{app.ErrDeviceRefreshUnavailable, 503, "ERR_TEMPORARILY_UNAVAILABLE"},
		{app.ErrTokenIssuanceUnavailable, 503, "ERR_TEMPORARILY_UNAVAILABLE"},
		{context.DeadlineExceeded, 503, "ERR_TEMPORARILY_UNAVAILABLE"},
		{app.ErrAccessTokenIssuance, 500, "ERR_INTERNAL"},
		{errors.New("anything else"), 500, "ERR_INTERNAL"},
		{nil, 500, "ERR_INTERNAL"},
	} {
		rec := httptest.NewRecorder()
		h.fail(rec, httptest.NewRequest("POST", routes.Refresh, http.NoBody), tc.err)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body["detail"] != problems[tc.code].detail {
			t.Errorf("%v: detail %q is not the fixed text", tc.err, body["detail"])
		}
		if rec.Code != tc.status || body["code"] != tc.code {
			t.Errorf("%v -> %d %v, want %d %s", tc.err, rec.Code, body["code"], tc.status, tc.code)
		}
		if tc.err != nil && strings.Contains(rec.Body.String(), tc.err.Error()) {
			t.Errorf("%v: the error text was echoed", tc.err)
		}
		if (rec.Header().Get("Retry-After") != "") != (tc.status == 429) {
			t.Errorf("%v: Retry-After = %q", tc.err, rec.Header().Get("Retry-After"))
		}
	}
}

func TestHTTP_CorrelationIDs(t *testing.T) {
	r := newRig(t, true)
	got := r.do(t, "POST", "/nowhere", `{}`, map[string][]string{"X-Correlation-ID": {"attacker\r\nvalue"}})
	if c := got.hdr.Get("X-Correlation-ID"); !strings.HasPrefix(c, "corr_") || strings.Contains(c, "attacker") {
		t.Errorf("an invalid inbound correlation id was echoed: %q", c)
	}
	minted := got.hdr.Get("X-Correlation-ID")
	again := r.do(t, "POST", "/nowhere", `{}`, map[string][]string{"X-Correlation-ID": {minted}})
	if again.hdr.Get("X-Correlation-ID") != minted || again.body["traceId"] != minted {
		t.Error("a platform correlation id was not propagated")
	}
}

func TestHTTP_PanicsAreFixed500s(t *testing.T) {
	h := &handler{log: slog.New(slog.NewTextHandler(&syncBuf{}, nil)), now: time.Now}
	wrapped := h.recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("secret 0f1e2d") }))
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest("POST", routes.Refresh, http.NoBody))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "0f1e2d") {
		t.Errorf("panic -> %d %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_RequestsAreBoundedInTime(t *testing.T) {
	var deadline time.Time
	var ok bool
	withDeadlines(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		deadline, ok = r.Context().Deadline()
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", http.NoBody))
	if !ok || time.Until(deadline) > HandlerTimeout || time.Until(deadline) < HandlerTimeout-time.Second {
		t.Errorf("request context deadline = %v (%v), want about %v from now", deadline, ok, HandlerTimeout)
	}
}

// Across a full exchange — request, enrolment, refresh, reuse — and its error
// paths, no response body or log line carries the number, the code, a proof,
// or a token outside the success response that issued it.
func TestHTTP_NothingSensitiveLeaksIntoErrorsOrLogs(t *testing.T) {
	r := newRig(t, true)
	k := deviceKey(t)
	reqID, code := r.requestCode(t)
	p := proof(t, k, k, "leak1", "POST", enrolURL, time.Now())
	_ = r.post(t, routes.Enrollment, enrolBody(reqID, wrongFor(code)), proof(t, k, k, "leak0", "POST", enrolURL, time.Now()))
	ok := r.post(t, routes.Enrollment, enrolBody(reqID, code), p)
	sid, rt := ok.body["sessionId"].(string), ok.body["refreshToken"].(string)
	_ = r.post(t, routes.Enrollment, enrolBody(reqID, code), p) // replay
	_ = r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, other(t), other(t), "z", "POST", refreshURL, time.Now()))
	_ = r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, "leak2", "POST", refreshURL, time.Now()))
	_ = r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, "leak3", "POST", refreshURL, time.Now())) // reuse

	secrets := []string{msisdn, strings.TrimPrefix(msisdn, "+"), code, rt, p[:40]}
	for _, b := range r.bodies {
		if strings.Contains(b, `"accessToken"`) {
			continue // the success responses that legitimately issue tokens
		}
		for _, s := range secrets {
			if strings.Contains(b, s) {
				t.Errorf("an error response carries %q: %s", s, b)
			}
		}
	}
	for _, s := range append(secrets, ok.body["accessToken"].(string)[:30]) {
		if strings.Contains(r.logs.String(), s) {
			t.Errorf("the log carries %q", s)
		}
	}
}

func other(t *testing.T) *ecdsa.PrivateKey { return deviceKey(t) }
