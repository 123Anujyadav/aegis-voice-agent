package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/dpop"
)

// Account deletion: the subscriber erases their own account (Google Play
// requires it for an app with accounts; docs/domain/10-identity.md
// "ErasureRequest"). Today identity is the only store holding a subscriber's
// data (docs/phase19/ANDROID-DATA-INVENTORY.md), so the erasure is identity's
// alone and completes in one transaction.
//
// AUTHENTICATION is exactly a proof-bound refresh's: the session id and its
// CURRENT refresh token, and a DPoP proof (htu: the deletion URL) signed by
// the session's enrolled device key. Nothing weaker is accepted, and a
// superseded refresh token is treated as reuse, as on refresh.
//
// WHAT IS ERASED, for the subscriber who owns the session (every one of their
// sessions, on every device):
//   - sessions, and with them their superseded refresh-token hashes;
//   - one-time-code challenges;
//   - device attestations of their enrolled device keys;
//   - guardian delegations in which they are either party.
//
// WHAT SURVIVES, and why (recorded on the erasure as its exceptions):
//   - the security audit log, append-only under LEGAL_HOLD (ADR-0010 §10,
//     ADR-0012 retention table "Audit logs (auth, access, erasure)"). It holds
//     no number and no subscriber id, only random request and session ids,
//     which nothing else links to the subscriber once their sessions are gone;
//   - the erasure record itself, LEGAL_HOLD ("The record of an erasure must
//     outlive the erasure", docs/domain/10-identity.md): the subscriber id and
//     when the erasure was requested and completed;
//   - OTP rate-limit rows (keyed by the number's pseudonym, not by subscriber)
//     and DPoP replay rows: abuse controls that housekeeping deletes within
//     their window plus a margin (at most about 75 minutes);
//   - access tokens already issued: edge-api does not consult sessions, so a
//     token issued before the erasure stays valid until it expires (15
//     minutes, ADR-0029). It names only the subscriber id; edge-api holds no
//     subscriber data.
//
// A REPEATED request (the client did not see the answer and retries) is
// answered as success when it presents the erased session's id with a valid
// proof from the same device key: the erasure leaves a receipt for that until
// the session would have expired, then housekeeping deletes it.
//
// NOT DONE, because nothing exists yet: clearing carrier forwarding before the
// erasure (P-ID-4), and erasure in other contexts' stores.

// EventAccountDeleted records an account erasure, naming the session that
// requested it (ADR-0012: erasure is audited).
const EventAccountDeleted SecurityEventType = "identity.account.deleted"

// ErrAccountDeletionUnavailable means no deletion proof verifier is configured.
var ErrAccountDeletionUnavailable = errors.New("account deletion is not configured")

// ErasureExceptions are the stores an erasure deliberately leaves, stated on
// the erasure record so that what survives is explicit, never silent.
var ErasureExceptions = []string{
	"identity_security_audit: LEGAL_HOLD (ADR-0010 §10, ADR-0012); random request and session ids only",
	"identity_erasures: LEGAL_HOLD (docs/domain/10-identity.md ErasureRequest)",
	"identity_otp_rate_state, identity_otp_requests, identity_otp_failures: number pseudonym, deleted by housekeeping within the rate window plus margin",
	"identity_dpop_replay: proof ids, deleted by housekeeping after expiry",
	"issued access tokens: valid until expiry (at most 15 minutes, ADR-0029)",
}

// SubscriberErasure is one completed erasure: what the repository records and
// what it erases.
type SubscriberErasure struct {
	SubscriberID string
	RequestedAt  time.Time
	// RequestingSessionID and DeviceKeyThumbprint form the receipt that
	// answers a repeated request; ReceiptExpiresAt is when it may go.
	RequestingSessionID string
	DeviceKeyThumbprint string
	ReceiptExpiresAt    time.Time
	Exceptions          []string
}

// ErasureRepository is the storage of account erasure. The pgstore and
// memory repositories implement it alongside IdentityRepository.
type ErasureRepository interface {
	// EraseSubscriber deletes everything identity holds for e.SubscriberID
	// (see the package documentation of erasure above), records the erasure
	// and its receipt, all in one transaction: all or nothing.
	EraseSubscriber(ctx context.Context, e *SubscriberErasure) error
	// ErasureReceipt returns the device key thumbprint of an erased session's
	// receipt, and false if there is none (never erased, or expired).
	ErasureReceipt(ctx context.Context, sessionID string) (thumbprint string, found bool, err error)
}

// SetAccountDeletionProofVerifier configures the verifier for deletion
// proofs. Its htu is the deletion URL. Until it is set, deletion answers
// ErrAccountDeletionUnavailable.
func (s *IdentityService) SetAccountDeletionProofVerifier(v *dpop.Verifier) { s.deletionProof = v }

// DeleteAccountWithDeviceProof erases the account that owns sessionID, after
// the same checks as a proof-bound refresh (see RefreshTokenWithDeviceProof):
//  1. A missing session may be one already erased: with a receipt and a valid
//     proof from the receipt's key, the request succeeds again (idempotent).
//     Anything else is ErrInvalidToken.
//  2. A revoked or expired session is ErrInvalidToken.
//  3. A refresh token that is not the current one is refreshMismatch: a
//     superseded token is reuse and revokes the session.
//  4. An unenrolled session is ErrSessionNotDeviceBound.
//  5. The proof must verify, and its key must be the enrolled key.
//
// Only then is anything erased.
func (s *IdentityService) DeleteAccountWithDeviceProof(ctx context.Context, sessionID, refreshToken, method, proof string) error {
	if s.deletionProof == nil {
		return ErrAccountDeletionUnavailable
	}
	repo, ok := s.repo.(ErasureRepository)
	if !ok {
		return ErrAccountDeletionUnavailable
	}

	session, err := s.repo.GetSession(ctx, sessionID)
	if errors.Is(err, ErrInvalidToken) {
		// Repositories answer a missing session with ErrInvalidToken.
		return s.repeatedDeletion(ctx, repo, sessionID, method, proof)
	}
	if err != nil {
		return err
	}
	if session.IsRevoked || time.Now().After(session.ExpiresAt) {
		return ErrInvalidToken
	}
	presentedHash := s.refreshTokenHash(refreshToken)
	if !equalHash(session.RefreshTokenHash, presentedHash) {
		return s.refreshMismatch(ctx, sessionID, presentedHash)
	}
	if session.DeviceKeyThumbprint == "" {
		return ErrSessionNotDeviceBound
	}
	verified, err := s.deletionProof.Verify(ctx, proof, method)
	switch {
	case errors.Is(err, dpop.ErrInvalidProof):
		return fmt.Errorf("%w: %w", ErrInvalidDeviceProof, err)
	case err != nil:
		return err
	}
	if verified.JKT != session.DeviceKeyThumbprint {
		return fmt.Errorf("%w: proof key is not the enrolled key", ErrInvalidDeviceProof)
	}

	now := time.Now()
	if err := repo.EraseSubscriber(ctx, &SubscriberErasure{
		SubscriberID:        session.SubscriberID,
		RequestedAt:         now,
		RequestingSessionID: session.SessionID,
		DeviceKeyThumbprint: session.DeviceKeyThumbprint,
		ReceiptExpiresAt:    session.ExpiresAt,
		Exceptions:          ErasureExceptions,
	}); err != nil {
		return err
	}
	s.audit(SecurityEvent{Type: EventAccountDeleted, At: now, SessionID: session.SessionID})
	return nil
}

// repeatedDeletion answers a deletion for a session that no longer exists: a
// success only if it was erased (a receipt) and the proof is from the same key.
func (s *IdentityService) repeatedDeletion(ctx context.Context, repo ErasureRepository, sessionID, method, proof string) error {
	thumbprint, found, err := repo.ErasureReceipt(ctx, sessionID)
	if err != nil {
		return err
	}
	if !found {
		return ErrInvalidToken
	}
	verified, err := s.deletionProof.Verify(ctx, proof, method)
	switch {
	case errors.Is(err, dpop.ErrInvalidProof):
		return fmt.Errorf("%w: %w", ErrInvalidDeviceProof, err)
	case err != nil:
		return err
	}
	if verified.JKT != thumbprint {
		return ErrInvalidToken
	}
	return nil
}
