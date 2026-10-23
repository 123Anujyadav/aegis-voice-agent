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

