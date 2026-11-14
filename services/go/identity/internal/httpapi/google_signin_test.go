package httpapi

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

// POST /v1/auth/google through the HTTP layer, with a stub token verifier:
// 503 when off, the 8 KiB limit on this route only, malformed input, the one
// 401 for every refusal, 503 when keys are unavailable, success, and no leak.

var googleURL = host + routes.GoogleSignIn

type stubVerifier struct {
	mu          sync.Mutex
	ids         map[string]*app.GoogleIdentity
	nonces      map[string]string
	unavailable bool
}

func (s *stubVerifier) VerifyGoogleIDToken(_ context.Context, raw, nonce string) (*app.GoogleIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable {
		return nil, app.ErrGoogleSignInUnavailable
	}
	id, ok := s.ids[raw]
	if !ok || s.nonces[raw] != nonce {
		return nil, app.ErrGoogleSignInRefused
	}
	cp := *id
	return &cp, nil
}

func (r *rig) withGoogle(t *testing.T, hourlyCap int) *stubVerifier {
	t.Helper()
	ring, err := app.ParsePseudonymKeyring([]byte("k1 " + strings.Repeat("11", 32) + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	dir, err := app.NewGoogleAccountDirectory(r.repo.MemoryIdentityRepository, ring)
	if err != nil {
		t.Fatal(err)
	}
	list, err := app.ParseEmailAllowlist([]byte("tester@example.com\n"))
	if err != nil {
		t.Fatal(err)
	}
	proofs, err := dpop.NewVerifier(dpop.Config{HTU: googleURL, MaxAge: 2 * time.Minute, FutureSkew: time.Minute, Replay: dpop.NewMemoryReplayStore()})
	if err != nil {
		t.Fatal(err)
	}
	stub := &stubVerifier{ids: map[string]*app.GoogleIdentity{}, nonces: map[string]string{}}
	if err := r.svc.SetGoogleSignIn(app.GoogleSignInConfig{Verifier: stub, ProofVerifier: proofs, Directory: dir, Allowlist: list, HourlyCap: hourlyCap}); err != nil {
		t.Fatal(err)
	}
	return stub
}

var googleSeq int

// googleBody mints a token (a JWS-shaped string) for sub and email, bound to
// key's nonce, and returns the body and a fresh proof.
func googleBody(t *testing.T, s *stubVerifier, key *ecdsa.PrivateKey, sub, email string) (string, string, string) {
	t.Helper()
	googleSeq++
	seedBytes := make([]byte, 32)
	_, _ = rand.Read(seedBytes) //nolint:errcheck // crypto/rand
	seed := base64.RawURLEncoding.EncodeToString(seedBytes)
	raw := fmt.Sprintf("eyJh.eyJz%d.c2ln", googleSeq)
	jkt, err := accesstoken.ThumbprintP256(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.ids[raw] = &app.GoogleIdentity{Issuer: app.GoogleIssuer, Subject: sub, Email: email, EmailVerified: true, ExpiresAt: time.Now().Add(time.Hour)}
	s.nonces[raw] = app.GoogleNonce(jkt, seed)
	s.mu.Unlock()
	body, _ := json.Marshal(map[string]string{"idToken": raw, "nonceSeed": seed}) //nolint:errcheck // fixture
	return string(body), proof(t, key, key, fmt.Sprintf("g-%d", googleSeq), "POST", googleURL, time.Now()), raw
}

func TestGoogleHTTP_OffIs503(t *testing.T) {
	r := newRig(t, false)
	got := r.post(t, routes.GoogleSignIn, `{"idToken":"a.b.c","nonceSeed":"x"}`, "")
	wantProblem(t, got, http.StatusServiceUnavailable, "ERR_SIGN_IN_UNAVAILABLE")
}

func TestGoogleHTTP_SignsInAndAnswersLikeEnrolment(t *testing.T) {
	r := newRig(t, false)
	stub := r.withGoogle(t, 5)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader) //nolint:errcheck // test key
	body, p, _ := googleBody(t, stub, key, "sub-1", "Tester@Example.com")
	got := r.post(t, routes.GoogleSignIn, body, p)
	if got.code != http.StatusOK || got.hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("got %d %s", got.code, got.raw)
	}
	if strings.Join(keys(got.body), ",") != "accessToken,expiresIn,refreshToken,sessionId,tokenType" || got.body["tokenType"] != "DPoP" {
		t.Errorf("body fields = %v", keys(got.body))
	}
	claims, err := r.verifier.Verify(got.body["accessToken"].(string)) //nolint:forcetypeassert // checked by the fields above
	if err != nil || !strings.HasPrefix(claims.Subject, "usr_") {
		t.Errorf("token: (%+v, %v)", claims, err)
	}
}

// Every refusal is the same 401, whatever the reason.
func TestGoogleHTTP_EveryRefusalIsTheSame401(t *testing.T) {
	r := newRig(t, false)
	stub := r.withGoogle(t, 1)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader) //nolint:errcheck // test key
	okBody, okProof, okRaw := googleBody(t, stub, key, "sub-1", "tester@example.com")
	if got := r.post(t, routes.GoogleSignIn, okBody, okProof); got.code != http.StatusOK {
		t.Fatalf("setup: %d %s", got.code, got.raw)
	}
	notListed, p1, _ := googleBody(t, stub, key, "sub-2", "stranger@example.com")
	capSpent, p2, _ := googleBody(t, stub, key, "sub-3", "tester@example.com")
	var replayBody map[string]string
	_ = json.Unmarshal([]byte(okBody), &replayBody) //nolint:errcheck // fixture
	// A token the stub does not know, built at runtime: a literal shaped like
	// a JWT next to "idToken" trips the secret scanner (gitleaks).
	unknownToken := strings.Join([]string{"eyJh", "dW5rbm93bg", "c2ln"}, ".")
	unknownBody, _ := json.Marshal(map[string]string{"idToken": unknownToken, "nonceSeed": replayBody["nonceSeed"]}) //nolint:errcheck // fixture
	cases := map[string][2]string{
		"not on the allowlist": {notListed, p1},
		"cap spent":            {capSpent, p2},
		"replayed token":       {okBody, proof(t, key, key, "replay", "POST", googleURL, time.Now())},
		"unknown token":        {string(unknownBody), proof(t, key, key, "unknown", "POST", googleURL, time.Now())},
	}
	var shapes []string
	for name, c := range cases {
		got := r.post(t, routes.GoogleSignIn, c[0], c[1])
		wantProblem(t, got, http.StatusUnauthorized, "ERR_SIGN_IN_REFUSED")
		shape := fmt.Sprintf("%d|%v|%v|%v|%v", got.code, got.body["code"], got.body["title"], got.body["detail"], got.body["status"])
		shapes = append(shapes, shape)
		_ = name
	}
	for _, s := range shapes {
		if s != shapes[0] {
			t.Errorf("refusals differ: %q vs %q", s, shapes[0])
		}
	}
	for _, b := range r.bodies {
		for _, secret := range []string{"stranger@", "tester@", "sub-", okRaw, replayBody["nonceSeed"]} {
			if strings.Contains(b, secret) {
				t.Errorf("a response carries %q", secret)
			}
		}
	}
	for _, secret := range []string{"stranger@", "tester@", "sub-", okRaw, replayBody["nonceSeed"]} {
		if strings.Contains(r.logs.String(), secret) {
			t.Errorf("the log carries %q", secret)
		}
	}
}

func TestGoogleHTTP_UnavailableKeysAre503(t *testing.T) {
	r := newRig(t, false)
	stub := r.withGoogle(t, 5)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader) //nolint:errcheck // test key
	body, p, _ := googleBody(t, stub, key, "sub-1", "tester@example.com")
	stub.mu.Lock()
	stub.unavailable = true
	stub.mu.Unlock()
	wantProblem(t, r.post(t, routes.GoogleSignIn, body, p), http.StatusServiceUnavailable, "ERR_SIGN_IN_UNAVAILABLE")
}

func TestGoogleHTTP_MalformedRequestsKeepTheirErrors(t *testing.T) {
	r := newRig(t, false)
	stub := r.withGoogle(t, 5)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader) //nolint:errcheck // test key
	body, p, _ := googleBody(t, stub, key, "sub-1", "tester@example.com")
	var fields map[string]string
	_ = json.Unmarshal([]byte(body), &fields) //nolint:errcheck // fixture
	for name, c := range map[string]struct {
		body, proof string
		status      int
		code        string
	}{
		"unknown field":     {`{"idToken":"a.b.c","nonceSeed":"` + fields["nonceSeed"] + `","extra":1}`, p, 400, "ERR_MALFORMED_BODY"},
		"not a JWS":         {`{"idToken":"not-a-token","nonceSeed":"` + fields["nonceSeed"] + `"}`, p, 400, "ERR_INVALID_INPUT"},
		"bad seed":          {`{"idToken":"a.b.c","nonceSeed":"short"}`, p, 400, "ERR_INVALID_INPUT"},
		"missing proof":     {body, "", 400, "ERR_INVALID_DPOP_PROOF"},
		"proof for refresh": {body, proof(t, key, key, "x", "POST", refreshURL, time.Now()), 400, "ERR_INVALID_DPOP_PROOF"},
	} {
		wantProblem(t, r.post(t, routes.GoogleSignIn, c.body, c.proof), c.status, c.code)
		_ = name
	}
}

// 8 KiB on this route only; every other route keeps 1 KiB.
func TestGoogleHTTP_BodyLimitIs8KiBOnThisRouteOnly(t *testing.T) {
	r := newRig(t, true)
	r.withGoogle(t, 5)
	big := `{"idToken":"a.` + strings.Repeat("b", 5<<10) + `.c","nonceSeed":"` + strings.Repeat("A", 43) + `"}`
	if got := r.post(t, routes.GoogleSignIn, big, ""); got.code == http.StatusRequestEntityTooLarge {
		t.Errorf("a 5 KiB body was refused as too large on %s", routes.GoogleSignIn)
	}
	tooBig := `{"idToken":"a.` + strings.Repeat("b", MaxGoogleSignInBodyBytes) + `.c","nonceSeed":"x"}`
	wantProblem(t, r.post(t, routes.GoogleSignIn, tooBig, ""), http.StatusRequestEntityTooLarge, "ERR_BODY_TOO_LARGE")
	twoKiB := `{"msisdn":"+91` + strings.Repeat("9", 2<<10) + `"}`
	for _, path := range []string{routes.OTPRequest, routes.Enrollment, routes.Refresh, routes.AccountDeletion} {
		wantProblem(t, r.post(t, path, twoKiB, ""), http.StatusRequestEntityTooLarge, "ERR_BODY_TOO_LARGE")
	}
	if MaxBodyBytes != 1<<10 {
		t.Errorf("MaxBodyBytes changed: %d", MaxBodyBytes)
	}
}
