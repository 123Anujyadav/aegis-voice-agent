package app

import (
	"context"
	"testing"
)

func TestIdentityService_OtpLifecycle(t *testing.T) {
	repo := NewMemoryIdentityRepository()
	service := NewIdentityService(repo, []byte("test_secret_key"))

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
	session, token, err := service.ValidateOtp(ctx, reqID2, "482910")
	if err != nil {
		t.Fatalf("ValidateOtp failed: %v", err)
	}
	if session.MSISDN != msisdn {
		t.Errorf("expected MSISDN %s, got %s", msisdn, session.MSISDN)
	}
	if token == "" {
		t.Errorf("expected non-empty access token")
	}
}

func TestIdentityService_TokenRotationAndReuse(t *testing.T) {
	repo := NewMemoryIdentityRepository()
	service := NewIdentityService(repo, []byte("test_secret_key"))

	ctx := context.Background()
	reqID, _ := service.RequestOtp(ctx, "+919876543210")
	session, _, _ := service.ValidateOtp(ctx, reqID, "482910")

	oldRefreshToken := session.RefreshToken

	// 1. Rotate refresh token successfully
	newToken, newRefreshToken, err := service.RefreshToken(ctx, session.SessionID, oldRefreshToken)
	if err != nil {
		t.Fatalf("RefreshToken failed: %v", err)
	}
	if newToken == "" || newRefreshToken == "" {
		t.Fatalf("expected new tokens on rotation")
	}

	// 2. Attempt reuse of old refresh token (Security violation)
	_, _, err = service.RefreshToken(ctx, session.SessionID, oldRefreshToken)
	if err != ErrTokenReused {
		t.Fatalf("expected ErrTokenReused, got: %v", err)
	}

}

func TestIdentityService_PlayIntegrityVerification(t *testing.T) {
	repo := NewMemoryIdentityRepository()
	service := NewIdentityService(repo, []byte("test_secret_key"))

	ctx := context.Background()
	attestation, err := service.VerifyPlayIntegrity(ctx, "valid_play_integrity_token_string")
	if err != nil {
		t.Fatalf("VerifyPlayIntegrity failed: %v", err)
	}

	if !attestation.IsPlayCertified {
		t.Errorf("expected device to be Play certified")
	}
}
