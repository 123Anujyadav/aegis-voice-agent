package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

// RefreshToken used to discard the SaveSession error and return the rotated
// tokens regardless. The store still held the OLD refresh token, so the client's
// next refresh presented a token the store had never seen, was classified as
// reuse, and revoked a perfectly valid session.
//
// A rotation that was not persisted did not happen. The caller must get an
// error and no tokens, and the credential it already holds must keep working.

// TestRefreshToken_FailedRotationSaveIsFailClosed fails the rotated-session save
// and then proves the session survives it.
func TestRefreshToken_FailedRotationSaveIsFailClosed(t *testing.T) {
	repo := &injectableRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	svc := withFixedOtp(NewIdentityService(repo, []byte("test_secret_key")))
	ctx := context.Background()

	reqID, err := svc.RequestOtp(ctx, "+919876543210")
	if err != nil {
		t.Fatalf("RequestOtp: %v", err)
	}
	sess, _, err := svc.ValidateOtp(ctx, reqID, testOtpCode)
	if err != nil {
		t.Fatalf("ValidateOtp: %v", err)
	}
	oldRefresh := sess.RefreshToken

	// --- the rotation's save fails, and only that save ----------------------
	repo.failSession = true
	access, refresh, err := svc.RefreshToken(ctx, sess.SessionID, oldRefresh)
	repo.failSession = false

	if !errors.Is(err, errInternalStore) {
		t.Fatalf("RefreshToken with a failing save: err = %v, want the store's error", err)
	}
	if access != "" || refresh != "" {
		t.Fatalf("a rotation that was not persisted returned tokens "+
			"(access %d bytes, refresh %d bytes)", len(access), len(refresh))
	}

	// --- nothing about the stored session moved ------------------------------
	stored, err := repo.MemoryIdentityRepository.GetSession(ctx, sess.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if stored.IsRevoked {
		t.Fatal("a failed rotation revoked the session")
	}
	if stored.RefreshTokenHash != rth(oldRefresh) {
		t.Fatal("the stored refresh token changed although its save failed")
	}

	// --- the token the caller still holds is valid, not "reused" -------------
	access2, refresh2, err := svc.RefreshToken(ctx, sess.SessionID, oldRefresh)
	if err != nil {
		t.Fatalf("retrying with the unrotated token: %v (a failed rotation must "+
			"not make the caller's credential look reused)", err)
	}
	if access2 != "" || refresh2 == "" || refresh2 == oldRefresh {
		t.Fatal("the retried rotation did not issue a fresh refresh token, or issued an unenrolled access token")
	}

	// --- the committed rotation behaves as rotation always has ---------------
	if _, _, refreshTokenErr := svc.RefreshToken(ctx, sess.SessionID, refresh2); refreshTokenErr != nil {
		t.Fatalf("the committed refresh token was refused: %v", refreshTokenErr)
	}
	if _, _, refreshTokenErr := svc.RefreshToken(ctx, sess.SessionID, oldRefresh); !errors.Is(refreshTokenErr, ErrTokenReused) {
		t.Fatalf("replaying a rotated-away token: err = %v, want ErrTokenReused", refreshTokenErr)
	}
	stored, err = repo.MemoryIdentityRepository.GetSession(ctx, sess.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !stored.IsRevoked {
		t.Error("genuine reuse no longer revokes the session")
	}
}

// TestRefreshToken_ExpiredSessionIsRefusedNotRevoked proves an expired session
// cannot be refreshed, and that refusing it is not treated as reuse.
//
// RefreshToken checked existence, revocation and the token, but never
// ExpiresAt, so a session past its lifetime rotated forever and each rotation
// carried the old expiry forward unchanged.
func TestRefreshToken_ExpiredSessionIsRefusedNotRevoked(t *testing.T) {
	repo := &injectableRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	svc := NewIdentityService(repo, []byte("test_secret_key"))
	ctx := context.Background()

	seed := func(id string, expires time.Time) {
		t.Helper()
		if err := repo.MemoryIdentityRepository.SaveSession(ctx, &UserSession{
			SessionID:        id,
			SubscriberID:     "usr_test",
			RefreshTokenHash: rth("rt_" + id),
			Roles:            []string{"subscriber"},
			CreatedAt:        expires.Add(-24 * time.Hour),
			ExpiresAt:        expires,
		}); err != nil {
			t.Fatalf("seed SaveSession: %v", err)
		}
	}

	// Control: an otherwise identical session that has NOT expired refreshes,
	// so a refusal below is about expiry and not about how it was seeded.
	seed("sess_live", time.Now().Add(time.Hour))
	if _, _, err := svc.RefreshToken(ctx, "sess_live", "rt_sess_live"); err != nil {
		t.Fatalf("control: an unexpired seeded session was refused: %v", err)
	}

	seed("sess_expired", time.Now().Add(-time.Minute))

	// Any save attempted for the expired session now fails loudly, so a
	// rotation that was even attempted shows up as the store's error.
	repo.failSession = true
	access, refresh, err := svc.RefreshToken(ctx, "sess_expired", "rt_sess_expired")
	repo.failSession = false

	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("refreshing an expired session: err = %v, want ErrInvalidToken "+
			"(the store's error means a rotation was attempted)", err)
	}
	if access != "" || refresh != "" {
		t.Fatal("an expired session was issued tokens")
	}

	stored, err := repo.MemoryIdentityRepository.GetSession(ctx, "sess_expired")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if stored.IsRevoked {
		t.Error("expiry revoked the session; expiry is not evidence of reuse")
	}
	if stored.RefreshTokenHash != rth("rt_sess_expired") {
		t.Error("the expired session's refresh token was rotated")
	}
}

// revokeRepo counts RevokeSession calls and fails them on demand. Everything
// else is the real in-memory repository.
type revokeRepo struct {
	*MemoryIdentityRepository
	revokeErr   error
	revokeCalls int
}

func (r *revokeRepo) RevokeSession(ctx context.Context, sessionID string) error {
	r.revokeCalls++
	if r.revokeErr != nil {
		return r.revokeErr
	}
	return r.MemoryIdentityRepository.RevokeSession(ctx, sessionID)
}

// seedLiveSession writes an unexpired session whose current refresh token is
// "rt_current".
func seedLiveSession(t *testing.T, repo *revokeRepo, id string) {
	t.Helper()
	if err := repo.MemoryIdentityRepository.SaveSession(context.Background(), &UserSession{
		SessionID:        id,
		SubscriberID:     "usr_test",
		RefreshTokenHash: rth("rt_current"),
		Roles:            []string{"subscriber"},
		CreatedAt:        time.Now(),
		ExpiresAt:        time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed SaveSession: %v", err)
	}
}

// TestRefreshToken_ReuseWithFailedRevocationReportsBoth proves a revocation the
// store refused is not reported as one that happened.
//
// The reuse branch discarded RevokeSession's error and returned the bare
// ErrTokenReused — which says the family was revoked — while the session stayed
// live for whoever holds its current token.
func TestRefreshToken_ReuseWithFailedRevocationReportsBoth(t *testing.T) {
	errRevokeStore := errors.New("store: revoke failed: connection refused")
	repo := &revokeRepo{MemoryIdentityRepository: NewMemoryIdentityRepository(), revokeErr: errRevokeStore}
	svc := NewIdentityService(repo, []byte("test_secret_key"))
	seedLiveSession(t, repo, "sess_reuse_fail")

	access, refresh, err := svc.RefreshToken(context.Background(), "sess_reuse_fail", "rt_stolen")

	if repo.revokeCalls != 1 {
		t.Fatalf("RevokeSession was called %d times, want 1: reuse must still attempt revocation",
			repo.revokeCalls)
	}
	if !errors.Is(err, ErrTokenReused) {
		t.Errorf("err = %v, want it to still report the reuse (ErrTokenReused)", err)
	}
	if !errors.Is(err, errRevokeStore) {
		t.Errorf("err = %v, want it to carry the store's revocation error: a failed "+
			"revocation must not read as a completed one", err)
	}
	if access != "" || refresh != "" {
		t.Error("tokens were issued on detected reuse")
	}
}

// TestRefreshToken_ReuseStillRevokes pins the ordinary path: a reused token
// revokes the session and reports exactly ErrTokenReused.
func TestRefreshToken_ReuseStillRevokes(t *testing.T) {
	repo := &revokeRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	svc := NewIdentityService(repo, []byte("test_secret_key"))
	seedLiveSession(t, repo, "sess_reuse")

	access, refresh, err := svc.RefreshToken(context.Background(), "sess_reuse", "rt_stolen")

	if !errors.Is(err, ErrTokenReused) {
		t.Fatalf("err = %v, want ErrTokenReused", err)
	}
	if access != "" || refresh != "" {
		t.Error("tokens were issued on detected reuse")
	}
	if repo.revokeCalls != 1 {
		t.Errorf("RevokeSession was called %d times, want 1", repo.revokeCalls)
	}
	stored, err := repo.MemoryIdentityRepository.GetSession(context.Background(), "sess_reuse")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !stored.IsRevoked {
		t.Error("detected reuse did not revoke the session")
	}
}
