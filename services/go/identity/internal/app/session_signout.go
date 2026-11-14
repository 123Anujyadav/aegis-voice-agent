package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/dpop"
)

// Sign-out (OD-14 option B; ADR-0035 D35.9): a device signs itself out
// (POST /v1/session/logout), or signs out every device of its account
// (POST /v1/session/revoke-all). Both present what a proof-bound refresh
// does: the session id, its refresh token, and a DPoP proof whose htu is the
// endpoint's own URL.
//
// PROOF FIRST. Unlike refresh and account deletion, which treat a superseded
// refresh token as reuse before they look at the proof (refreshMismatch;
// docs/phase19/REFRESH-TOKEN-THREAT-ANALYSIS.md §3, kept as it is: decision
// R1 of 2026-11-14), these endpoints verify the proof and require the
// session's own enrolled key BEFORE anything else can change. A missing,
// invalid, replayed, wrong-key or wrong-endpoint proof revokes nothing,
// whatever refresh token comes with it. Only an authenticated request meets
// refreshMismatch, so reuse found here revokes the one session, as refresh
// would, and never the account's other sessions.
//
// Order (nothing changes before step 4):
//  1. The session exists, has not expired (expiry is not evidence of theft:
//     nothing is revoked), and is bound to a device key.
//  2. The proof verifies for this endpoint's URL (its jti is then spent),
//     and its key is the session's key.
//  3. A revoked session is a retry (see each endpoint).
//  4. A refresh token that is not the current one goes to refreshMismatch;
//     the current one signs out.
//
// SESSIONS ONLY. Neither endpoint deletes or changes a sign-in method (a
// Google identity and any other): the account signs in again as before.
//
// ACCESS TOKENS ALREADY ISSUED stay valid until they expire, at most 15
// minutes (ADR-0029; O17): edge-api does not consult sessions. Refresh is
// refused at once, so no new access token is issued.

// Sign-out audit events. Each names only the session that asked (SessionID).
const (
	// EventSessionLoggedOut records a session signed out by its own device.
	EventSessionLoggedOut SecurityEventType = "identity.session.logged_out"
	// EventSessionsRevokedAll records a revoke-all, naming the session that
	// asked; it is not repeated for a retry.
	EventSessionsRevokedAll SecurityEventType = "identity.session.revoked_all"
)

var (
	// ErrSignOutUnavailable means the endpoint's proof verifier, or for
	// revoke-all the operation's storage, is not configured.
	ErrSignOutUnavailable = errors.New("sign-out is not configured")
	// ErrSignOutContended means the store could not complete a revoke-all
	// after a bounded number of attempts (a deadlock or serialisation
	// failure): nothing changed, and the client may retry.
	ErrSignOutContended = errors.New("sign-out could not complete; nothing changed")
)

// SessionRevocation is one revoke-all operation, as the authenticated request
// describes it.
type SessionRevocation struct {
	// SessionID is the session that asked; SubscriberID its account.
	SessionID    string
	SubscriberID string
	// RefreshTokenHash is the hash of the refresh token presented, and
	// DeviceKeyThumbprint the key that signed the proof. Together with
	// SessionID they identify the operation: revoke-all revokes its own
	// session, and a revoked session's token never rotates again, so they
	// match at most one completed operation.
	RefreshTokenHash    string
	DeviceKeyThumbprint string
	RevokedAt           time.Time
}

// SessionRevocationRepository stores revoke-all. The pgstore and memory
// repositories implement it alongside IdentityRepository.
type SessionRevocationRepository interface {
	// RevokeAllSessions performs op in one step, all or nothing, serialised
	// with sign-ins and erasures of the same account. It locks the session
	// that asked and checks again that it belongs to op.SubscriberID, holds
	// op.RefreshTokenHash and op.DeviceKeyThumbprint, then:
	//   - live and unexpired: it records the operation and revokes every
	//     session of the account that is not yet revoked (the one that asked
	//     included), and returns how many, with repeated false;
	//   - revoked, with this operation recorded: it changes nothing and
	//     returns repeated true (a retry, answered as done);
	//   - anything else: ErrSessionConflict, changing nothing.
	// It never revokes a session of another account. A store that gives up
	// after bounded retries returns ErrSignOutContended, changing nothing.
	RevokeAllSessions(ctx context.Context, op *SessionRevocation) (revoked int, repeated bool, err error)
}

// SetLogoutProofVerifier configures the verifier for logout proofs (htu: the
// logout URL). Until it is set, logout answers ErrSignOutUnavailable.
func (s *IdentityService) SetLogoutProofVerifier(v *dpop.Verifier) { s.signOut.logout = v }

// SetRevokeAllProofVerifier configures the verifier for revoke-all proofs
// (htu: the revoke-all URL). Until it is set, revoke-all answers
// ErrSignOutUnavailable.
func (s *IdentityService) SetRevokeAllProofVerifier(v *dpop.Verifier) { s.signOut.revokeAll = v }

// LogoutWithDeviceProof revokes the session sessionID, on its current refresh
// token and a proof by its enrolled key (see the order above). A retry for a
// session already revoked, with its current refresh token and a fresh proof
// by its key, succeeds again and changes nothing; with any other token it is
// ErrInvalidToken.
func (s *IdentityService) LogoutWithDeviceProof(ctx context.Context, sessionID, refreshToken, method, proof string) error {
	if s.signOut.logout == nil {
		return ErrSignOutUnavailable
	}
	session, err := s.authenticateSignOut(ctx, s.signOut.logout, sessionID, method, proof)
	if err != nil {
		return err
	}
	presented := s.refreshTokenHash(refreshToken)
	if !equalHash(session.RefreshTokenHash, presented) {
		if session.IsRevoked {
			return ErrInvalidToken
		}
		return s.refreshMismatch(ctx, sessionID, presented)
	}
	if session.IsRevoked {
		return nil
	}
	if err := s.repo.RevokeSession(ctx, sessionID); err != nil {
		return err
	}
	s.audit(SecurityEvent{Type: EventSessionLoggedOut, At: s.now(), SessionID: sessionID})
	return nil
}

// RevokeAllSessionsWithDeviceProof revokes every session of the account that
// owns sessionID, that one included, on its current refresh token and a proof
// by its enrolled key (see the order above). The repository makes the
// revocation and its record one atomic step (SessionRevocationRepository).
//
// A retry of a completed revoke-all (the same session and refresh token, a
// fresh proof by the same key) succeeds again and revokes nothing more, so a
// session signed in since is untouched. A revoked session with no such record
// (signed out some other way) is ErrInvalidToken and revokes nothing.
func (s *IdentityService) RevokeAllSessionsWithDeviceProof(ctx context.Context, sessionID, refreshToken, method, proof string) error {
	if s.signOut.revokeAll == nil {
		return ErrSignOutUnavailable
	}
	repo, ok := s.repo.(SessionRevocationRepository)
	if !ok {
		return ErrSignOutUnavailable
	}
	session, err := s.authenticateSignOut(ctx, s.signOut.revokeAll, sessionID, method, proof)
	if err != nil {
		return err
	}
	presented := s.refreshTokenHash(refreshToken)
	if !session.IsRevoked && !equalHash(session.RefreshTokenHash, presented) {
		return s.refreshMismatch(ctx, sessionID, presented)
	}
	now := s.now()
	_, repeated, err := repo.RevokeAllSessions(ctx, &SessionRevocation{
		SessionID: sessionID, SubscriberID: session.SubscriberID, RefreshTokenHash: presented,
		DeviceKeyThumbprint: session.DeviceKeyThumbprint, RevokedAt: now,
	})
	switch {
	case errors.Is(err, ErrSessionConflict):
		// Revoked some other way, rotated or expired since it was read: the
		// request is refused and nothing changes.
		return ErrInvalidToken
	case err != nil:
		return err
	}
	if !repeated {
		s.audit(SecurityEvent{Type: EventSessionsRevokedAll, At: now, SessionID: sessionID})
	}
	return nil
}

// authenticateSignOut is steps 1 and 2: it returns the session (revoked or
// not) only when it is unexpired, bound to a device key, and the proof
// verifies for verifier's URL with that key. It changes nothing but the
// proof's replay record.
func (s *IdentityService) authenticateSignOut(ctx context.Context, verifier *dpop.Verifier, sessionID, method, proof string) (*UserSession, error) {
	session, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		// Repositories answer a missing session with ErrInvalidToken; any
		// other error is the store's own.
		return nil, err
	}
	if time.Now().After(session.ExpiresAt) {
		return nil, ErrInvalidToken
	}
	if session.DeviceKeyThumbprint == "" {
		return nil, ErrSessionNotDeviceBound
	}
	verified, err := verifier.Verify(ctx, proof, method)
	switch {
	case errors.Is(err, dpop.ErrInvalidProof):
		return nil, fmt.Errorf("%w: %w", ErrInvalidDeviceProof, err)
	case err != nil:
		// The replay store could not answer: fail closed.
		return nil, err
	}
	if verified.JKT != session.DeviceKeyThumbprint {
		return nil, fmt.Errorf("%w: proof key is not the session's key", ErrInvalidDeviceProof)
	}
	return session, nil
}

// signOutVerifiers are the sign-out endpoints' proof verifiers; nil means
// that endpoint is not configured.
type signOutVerifiers struct {
	logout, revokeAll *dpop.Verifier
}
