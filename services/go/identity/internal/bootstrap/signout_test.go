package bootstrap

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// Sign-out's canonical URLs are the refresh canonical URL's origin with each
// endpoint's path, so neither can be configured to a different host.
func TestSignOutCanonicalURLs(t *testing.T) {
	for in, want := range map[string][2]string{
		"https://identity.example.test/v1/token/refresh": {
			"https://identity.example.test/v1/session/logout", "https://identity.example.test/v1/session/revoke-all",
		},
		"https://identity.example.test:8443/v1/token/refresh": {
			"https://identity.example.test:8443/v1/session/logout", "https://identity.example.test:8443/v1/session/revoke-all",
		},
	} {
		if got := LogoutCanonicalURL(in); got != want[0] {
			t.Errorf("LogoutCanonicalURL(%q) = %q, want %q", in, got, want[0])
		}
		if got := RevokeAllCanonicalURL(in); got != want[1] {
			t.Errorf("RevokeAllCanonicalURL(%q) = %q, want %q", in, got, want[1])
		}
	}
}

// Build configures both sign-out endpoints, each accepting only proofs for
// its own canonical URL.
func TestBuild_SignOutEndpointsAcceptOnlyTheirOwnURL(t *testing.T) {
	path, _, _ := signingKeyFile(t)
	clock := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	deps := validDeps()
	repo := &knownCodeRepo{MemoryIdentityRepository: app.NewMemoryIdentityRepository(), secret: deps.SecretKey}
	deps.Repository = repo
	deps.Now = func() time.Time { return clock }
	svc, err := Build(validConfig(path), deps)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ctx := context.Background()
	logoutURL, revokeURL := LogoutCanonicalURL(testRefresh), RevokeAllCanonicalURL(testRefresh)
	enrol := func(jti string) (*app.UserSession, *ecdsa.PrivateKey) {
		t.Helper()
		device, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		reqID, err := svc.RequestOtp(ctx, testMSISDN)
		if err != nil {
			t.Fatalf("RequestOtp: %v", err)
		}
		sess, _, err := svc.ValidateOtpWithDeviceProof(ctx, reqID, testCode, "POST", proof(t, device, jti, testEnrolURL, clock))
		if err != nil {
			t.Fatalf("enrolment: %v", err)
		}
		return sess, device
	}

	s1, k1 := enrol("e1")
	for name, htu := range map[string]string{"refresh": testRefresh, "revoke-all": revokeURL, "deletion": AccountDeletionCanonicalURL(testRefresh)} {
		if err := svc.LogoutWithDeviceProof(ctx, s1.SessionID, s1.RefreshToken, "POST", proof(t, k1, "l-"+name, htu, clock)); !errors.Is(err, app.ErrInvalidDeviceProof) {
			t.Errorf("logout with a %s-URL proof: %v, want ErrInvalidDeviceProof", name, err)
		}
	}
	if err := svc.LogoutWithDeviceProof(ctx, s1.SessionID, s1.RefreshToken, "POST", proof(t, k1, "l1", logoutURL, clock)); err != nil {
		t.Fatalf("logout: %v", err)
	}

	s2, k2 := enrol("e2")
	if err := svc.RevokeAllSessionsWithDeviceProof(ctx, s2.SessionID, s2.RefreshToken, "POST", proof(t, k2, "a0", logoutURL, clock)); !errors.Is(err, app.ErrInvalidDeviceProof) {
		t.Errorf("revoke-all with a logout-URL proof: %v, want ErrInvalidDeviceProof", err)
	}
	if err := svc.RevokeAllSessionsWithDeviceProof(ctx, s2.SessionID, s2.RefreshToken, "POST", proof(t, k2, "a1", revokeURL, clock)); err != nil {
		t.Fatalf("revoke-all: %v", err)
	}
}
