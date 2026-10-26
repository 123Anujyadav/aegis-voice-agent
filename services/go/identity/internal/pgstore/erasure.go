package pgstore

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

var _ app.ErasureRepository = (*Repository)(nil)

// EraseSubscriber implements app.ErasureRepository in one transaction: the
// requesting session is locked and re-checked, then every row identity holds
// for the subscriber is deleted, and the erasure record (LEGAL_HOLD) and its
// receipt are written; or nothing changes.
//
// Deleted: identity_sessions (superseded refresh-token hashes follow by
// ON DELETE CASCADE), identity_otp_challenges, the device attestations of the
// subscriber's enrolled keys, and guardian delegations in which the subscriber
// is either party. Left, as the record's exceptions say: the append-only audit
// log, rate-limit and replay rows that expire on their own (app.ErasureExceptions).
func (r *Repository) EraseSubscriber(ctx context.Context, e *app.SubscriberErasure) error {
	return inTx(ctx, r.pool, func(tx pgx.Tx) error {
		// The requesting session is locked and re-checked first: a concurrent
		// deletion, rotation or revocation since the caller read it means this
		// request erases nothing (app.ErrSessionConflict).
		var subscriber, tokenHash string
		var revoked bool
		var expiresAt time.Time
		err := tx.QueryRow(ctx, `
			SELECT subscriber_id, refresh_token_hash, is_revoked, expires_at FROM identity_sessions
			WHERE session_id = $1 FOR UPDATE`, e.RequestingSessionID).Scan(&subscriber, &tokenHash, &revoked, &expiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrSessionConflict
		}
		if err != nil {
			return fmt.Errorf("pgstore: locking the requesting session: %w", err)
		}
		if revoked || subscriber != e.SubscriberID || !time.Now().Before(expiresAt) ||
			subtle.ConstantTimeCompare([]byte(tokenHash), []byte(e.RequestingRefreshTokenHash)) != 1 {
			return app.ErrSessionConflict
		}
		// A sign-in in flight for this subscriber either commits its session
		// before this point (and the DELETE below removes it) or waits and
		// then sees this erasure (SaveSession's ErasureFence).
		if err := lockSubscriberErasure(ctx, tx, e.SubscriberID); err != nil {
			return err
		}
		// The device ids must be read before the sessions that name them go.
		if _, err := tx.Exec(ctx, `
			DELETE FROM identity_device_attestations WHERE device_id IN (
				SELECT 'dev_' || device_key_thumbprint FROM identity_sessions
				WHERE subscriber_id = $1 AND device_key_thumbprint <> '')`, e.SubscriberID); err != nil {
			return fmt.Errorf("pgstore: erasing device attestations: %w", err)
		}
		steps := []struct{ what, sql string }{
			{"sessions", `DELETE FROM identity_sessions WHERE subscriber_id = $1`},
			{"challenges", `DELETE FROM identity_otp_challenges WHERE subscriber_id = $1`},
			{"delegations", `DELETE FROM identity_guardian_delegations WHERE subscriber_id = $1 OR guardian_id = $1`},
		}
		for _, s := range steps {
			if _, err := tx.Exec(ctx, s.sql, e.SubscriberID); err != nil {
				return fmt.Errorf("pgstore: erasing %s: %w", s.what, err)
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO identity_erasures (subscriber_id, requested_at, exceptions) VALUES ($1, $2, $3)`,
			e.SubscriberID, us(e.RequestedAt), e.Exceptions); err != nil {
			return fmt.Errorf("pgstore: recording the erasure: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO identity_erasure_receipts (session_id, device_key_thumbprint, expires_at) VALUES ($1, $2, $3)
			ON CONFLICT (session_id) DO NOTHING`,
			e.RequestingSessionID, e.DeviceKeyThumbprint, us(e.ReceiptExpiresAt)); err != nil {
			return fmt.Errorf("pgstore: writing the erasure receipt: %w", err)
		}
		return nil
	})
}

// lockSubscriberErasure takes the subscriber's transaction-scoped erasure
// lock: a PostgreSQL advisory lock on a 64-bit hash of the subscriber id
// (a collision only serialises two subscribers' sign-in and erasure).
func lockSubscriberErasure(ctx context.Context, tx pgx.Tx, subscriberID string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "identity_erasure:"+subscriberID); err != nil {
		return fmt.Errorf("pgstore: taking the subscriber erasure lock: %w", err)
	}
	return nil
}

// ErasureReceipt implements app.ErasureRepository. An expired receipt is not
// found, whether or not housekeeping has deleted it yet.
func (r *Repository) ErasureReceipt(ctx context.Context, sessionID string) (string, bool, error) {
	var thumbprint string
	var expiresAt time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT device_key_thumbprint, expires_at FROM identity_erasure_receipts WHERE session_id = $1`,
		sessionID).Scan(&thumbprint, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("pgstore: reading an erasure receipt: %w", err)
	}
	if time.Now().After(expiresAt) {
		return "", false, nil
	}
	return thumbprint, true, nil
}
