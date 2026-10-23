package app

import (
	"context"
	"errors"
	"testing"
)

func TestIdentityService_OtpLifecycle(t *testing.T) {
	repo := NewMemoryIdentityRepository()
	service := withFixedOtp(NewIdentityService(repo, []byte("test_secret_key")))

	ctx := context.Background()
	msisdn := "+919876543210"

	// 1. Request OTP
	reqID, err := service.RequestOtp(ctx, msisdn)
	if err != nil {
		t.Fatalf("RequestOtp failed: %v", err)
	}
	if reqID == "" {
		t.Fatalf("expected non-empty request ID")
	}

	// 2. Validate invalid OTP
	_, _, err = service.ValidateOtp(ctx, reqID, "000000")
	if err == nil {
		t.Fatalf("expected error for invalid OTP code")
	}

	// 3. Re-request OTP for valid test
	reqID2, _ := service.RequestOtp(ctx, msisdn)

	// 4. Validate valid OTP
	session, token, err := service.ValidateOtp(ctx, reqID2, testOtpCode)
	if err != nil {
		t.Fatalf("ValidateOtp failed: %v", err)
	}
	// The session carries no number; its link to the number is the keyed
	// subscriber id derived from it.
	if session.SubscriberID != service.subscriberID(msisdn) {
		t.Errorf("subscriber id %s is not the one derived from %s", session.SubscriberID, msisdn)
	}
	if token != "" {
		t.Errorf("an unenrolled session was issued an access token")
	}
}

func TestIdentityService_TokenRotationAndReuse(t *testing.T) {
	repo := NewMemoryIdentityRepository()
	service := withFixedOtp(NewIdentityService(repo, []byte("test_secret_key")))

	ctx := context.Background()
	reqID, _ := service.RequestOtp(ctx, "+919876543210")
	session, _, _ := service.ValidateOtp(ctx, reqID, testOtpCode)

	oldRefreshToken := session.RefreshToken

	// 1. Rotate refresh token successfully
	newToken, newRefreshToken, err := service.RefreshToken(ctx, session.SessionID, oldRefreshToken)
	if err != nil {
		t.Fatalf("RefreshToken failed: %v", err)
	}
	if newToken != "" || newRefreshToken == "" {
		t.Fatalf("expected a new refresh token and no access token on rotation")
	}

	// 2. Attempt reuse of old refresh token (Security violation)
	_, _, err = service.RefreshToken(ctx, session.SessionID, oldRefreshToken)
	if !errors.Is(err, ErrTokenReused) {
		t.Fatalf("expected ErrTokenReused, got: %v", err)
	}
}

// TestIdentityService_PlayIntegrityIsAStubThatVerifiesNothing pins the Play
// Integrity stub's behaviour.
//
// This test previously read as TestIdentityService_PlayIntegrityVerification and
// asserted that a device was Play certified — putting a green tick on a
// fabricated attestation. Phase 18 D11 found that VerifyPlayIntegrity accepts
// ANY token of ten or more characters and invents the result.
//
// The test is retained, inverted in intent: it now pins the stub's behaviour so
// the defect is visible rather than endorsed. It is written to FAIL the day
// real verification is implemented, which is the signal to delete it along with
// this comment.
func TestIdentityService_PlayIntegrityIsAStubThatVerifiesNothing(t *testing.T) {
	repo := NewMemoryIdentityRepository()
	service := withFixedOtp(NewIdentityService(repo, []byte("test_secret_key")))
	ctx := context.Background()

	// Obvious garbage, accepted purely because it is ten characters long.
	attestation, err := service.VerifyPlayIntegrity(ctx, "aaaaaaaaaa")
	if err != nil {
		t.Fatalf("the stub rejected a ten-character string, so its behaviour has "+
			"changed: %v. If real verification has been implemented, delete this "+
			"test and the stub warnings on VerifyPlayIntegrity", err)
	}
	if !attestation.IsPlayCertified {
		t.Fatal("the stub no longer reports IsPlayCertified for arbitrary input; " +
			"if real verification has been implemented, delete this test")
	}

	// The fabrication, pinned explicitly. ADR-0010 §5(b) requires the verdict be
	// verified server-side against Google; none of these values came from a
	// verification.
	if attestation.Model != "Pixel 9 Pro" {
		t.Errorf("Model = %q; the stub hardcodes it", attestation.Model)
	}
	if attestation.IntegrityToken != "aaaaaaaaaa" {
		t.Errorf("IntegrityToken = %q; the stub echoes its input", attestation.IntegrityToken)
	}
}
