package app

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrAccountNotActive is a session requested for an account that is not
// stored or not active.
var ErrAccountNotActive = errors.New("identity: the account is not active")

// createAccountSession creates a device-bound session for an account that a
// non-SMS sign-in method (ADR-0035) has authenticated, and issues its access
// token: the same session, token and audit as SMS sign-in, for an account id
// from identity_accounts.
//
// IT DELIBERATELY REPEATS, rather than shares, the tail of SMS sign-in
// (validateOtp from Play Integrity on, and
// ValidateOtpWithDeviceProofAndIntegrity from token issuance on): the owner
// required that the OTP path is not refactored (2026-11-13). Keep the two in
// step; account_sessions_test.go pins the shared properties.
//
// The caller has verified the device proof (deviceKeyThumbprint is the
// verified jkt) and the sign-in method, and passes the time the sign-in began
// as the erasure fence. The assess function is the Play Integrity assessment,
// or nil.
// Nothing is created for an account that is not active, or one erased since
// began.
func (s *IdentityService) createAccountSession(ctx context.Context, accountID, deviceKeyThumbprint string, began time.Time, assess integrityAssessor) (*UserSession, string, error) {
	if s.accessTokens == nil {
		return nil, "", ErrTokenIssuanceUnavailable
	}
	if deviceKeyThumbprint == "" || began.IsZero() {
		return nil, "", fmt.Errorf("%w: no verified device key", ErrInvalidDeviceProof)
	}
	accounts, err := s.accounts()
	if err != nil {
		return nil, "", err
	}
	acct, err := accounts.GetAccount(ctx, accountID)
	switch {
	case errors.Is(err, ErrAccountNotFound):
		return nil, "", ErrAccountNotActive
	case err != nil:
		return nil, "", err
	case acct.Status != AccountActive:
		return nil, "", ErrAccountNotActive
	}

	// Play Integrity, after the method and the key are proved and before the
	// session exists (ADR-0031 D31.3), as at SMS sign-in.
	tier := SessionUnverified
	if assess != nil {
		assessed, aerr := assess(ctx)
		if aerr != nil {
			return nil, "", aerr
		}
		tier = assessed
	}

	refreshToken := s.generateSecureToken()
	session := &UserSession{
		SessionID:           "sess_" + s.generateSecureToken(),
		SubscriberID:        acct.ID,
		RefreshTokenHash:    s.refreshTokenHash(refreshToken),
		Roles:               []string{"subscriber"},
		CreatedAt:           time.Now(),
		ExpiresAt:           time.Now().Add(24 * time.Hour),
		DeviceKeyThumbprint: deviceKeyThumbprint,
		IntegrityTier:       tier,
		// An erasure of this account since the sign-in began means it was
		// deleted while the sign-in was in flight: no session.
		ErasureFence: began,
	}
	if saveErr := s.repo.SaveSession(ctx, session); saveErr != nil {
		return nil, "", saveErr
	}
	token, err := s.issueBoundAccessToken(ctx, session.SessionID)
	if err != nil {
		// As at SMS sign-in: revoke rather than hand back a refresh token for
		// a sign-in that did not complete.
		if rerr := s.repo.RevokeSession(ctx, session.SessionID); rerr != nil {
			return nil, "", fmt.Errorf("%w: %w; revoking the session also failed: %w", ErrAccessTokenIssuance, err, rerr)
		}
		return nil, "", fmt.Errorf("%w: %w", ErrAccessTokenIssuance, err)
	}
	at := s.now()
	s.audit(SecurityEvent{Type: EventDeviceEnrolled, At: at, SessionID: session.SessionID})
	s.auditReenrolment(ctx, session, at)
	issued := cloneSession(session)
	issued.RefreshToken = refreshToken
	return issued, token, nil
}
