package httpapi

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

// Token issuance over HTTP fails closed. Each case checks the status, that no
// credential (access token, refresh token, session id) is in the body, and
// that nothing was consumed or rotated on the way.

func noCredentials(t *testing.T, got resp) {
	t.Helper()
	for _, k := range []string{"accessToken", "refreshToken", "sessionId"} {
		if _, ok := got.body[k]; ok {
			t.Errorf("a refused response carries %q: %s", k, got.raw)
		}
	}
}

// testSigner is a working access-token signer, to restore issuance after a
// test removes it.
func testSigner(t *testing.T) *accesstoken.Signer {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := accesstoken.NewSigner(accesstoken.SignerConfig{
		Issuer: "urn:callscreen:identity:test", Audience: "urn:callscreen:edge-api:test", KeyID: "test-key-2", Key: priv,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// An expired session cannot be refreshed, and nothing is rotated.
func TestIssuance_ExpiredSessionCannotRefresh(t *testing.T) {
	r := newRig(t, true)
	k := deviceKey(t)
	sid, rt := r.enrol(t, k)
	ctx := context.Background()
	s, err := r.repo.GetSession(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	s.ExpiresAt = time.Now().Add(-time.Second)
	if err := r.repo.SaveSession(ctx, s); err != nil {
		t.Fatal(err)
	}
	before, _ := r.repo.GetSession(ctx, sid)
	got := r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, "x1", "POST", refreshURL, time.Now()))
	wantProblem(t, got, 401, "ERR_INVALID_TOKEN")
	noCredentials(t, got)
	after, _ := r.repo.GetSession(ctx, sid)
	if after.RefreshTokenHash != before.RefreshTokenHash || after.IsRevoked {
		t.Error("refusing an expired session rotated or revoked it")
	}
}

// A revoked session cannot be refreshed, even with its current token and a
// valid proof by its own key.
func TestIssuance_RevokedSessionCannotRefresh(t *testing.T) {
	r := newRig(t, true)
	k := deviceKey(t)
	sid, rt := r.enrol(t, k)
	if err := r.repo.RevokeSession(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	got := r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, "v1", "POST", refreshURL, time.Now()))
	wantProblem(t, got, 401, "ERR_INVALID_TOKEN")
	noCredentials(t, got)
}

// Without a signer, enrolment is refused before anything happens: the code is
// not consumed and no session exists, so a retry once issuance is restored
// succeeds with the same code.
func TestIssuance_EnrolmentWithoutASignerFailsClosed(t *testing.T) {
	r := newRig(t, true)
	k := deviceKey(t)
	reqID, code := r.requestCode(t)
	signer := testSigner(t)
	r.svc.SetAccessTokenSigner(nil)
	got := r.post(t, routes.Enrollment, enrolBody(reqID, code), proof(t, k, k, "e-ns", "POST", enrolURL, time.Now()))
	wantProblem(t, got, 503, "ERR_TEMPORARILY_UNAVAILABLE")
	noCredentials(t, got)
	if n := r.repo.sessionCount(); n != 0 {
		t.Fatalf("%d sessions were saved by a refused enrolment", n)
	}
	r.svc.SetAccessTokenSigner(signer)
	if again := r.post(t, routes.Enrollment, enrolBody(reqID, code), proof(t, k, k, "e-ok", "POST", enrolURL, time.Now())); again.code != 200 {
		t.Fatalf("the code was consumed by the refused attempt: %d %s", again.code, again.raw)
	}
}

// Without a signer, a refresh is refused before rotation: the refresh token
// still works once issuance is restored.
func TestIssuance_RefreshWithoutASignerRotatesNothing(t *testing.T) {
	r := newRig(t, true)
	k := deviceKey(t)
	sid, rt := r.enrol(t, k)
	signer := testSigner(t)
	r.svc.SetAccessTokenSigner(nil)
	got := r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, "r-ns", "POST", refreshURL, time.Now()))
	wantProblem(t, got, 503, "ERR_TEMPORARILY_UNAVAILABLE")
	noCredentials(t, got)
	r.svc.SetAccessTokenSigner(signer)
	if again := r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, "r-ok", "POST", refreshURL, time.Now())); again.code != 200 {
		t.Fatalf("the refused refresh rotated or revoked the session: %d %s", again.code, again.raw)
	}
}
