package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

var _ app.WebErasureRepository = (*Repository)(nil)

// HasAccount implements app.WebErasureRepository: any session row of the
// subscriber, whatever its state, is something an erasure would delete.
func (r *Repository) HasAccount(ctx context.Context, subscriberID string) (bool, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity_sessions WHERE subscriber_id = $1)`,
		subscriberID).Scan(&exists); err != nil {
		return false, fmt.Errorf("pgstore: checking for an account: %w", err)
	}
	return exists, nil
}

// ReserveDeletionCodeRequest implements app.WebErasureRepository: one upsert,
// so concurrent requests on every replica count against the same hour.
func (r *Repository) ReserveDeletionCodeRequest(ctx context.Context, window time.Time, limit int) (bool, error) {
	var n int
	if err := r.pool.QueryRow(ctx, `
		INSERT INTO identity_deletion_code_budget (hour_start, requests) VALUES ($1, 1)
		ON CONFLICT (hour_start) DO UPDATE SET requests = identity_deletion_code_budget.requests + 1
		RETURNING requests`, us(window)).Scan(&n); err != nil {
		return false, fmt.Errorf("pgstore: counting a deletion-code request: %w", err)
	}
	return n <= limit, nil
}

// EraseSubscriberByDeletionCode implements app.WebErasureRepository. The
// challenge is consumed by the transaction's first statement: of concurrent
// confirms one deletes the row, and the others wait for it and then find it
// gone (app.ErrSessionConflict), by which time its receipt is committed.
func (r *Repository) EraseSubscriberByDeletionCode(ctx context.Context, e *app.SubscriberErasure, requestID, codeHash string) error {
	return inTx(ctx, r.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			DELETE FROM identity_otp_challenges
			WHERE request_id = $1 AND purpose = 'account_deletion' AND subscriber_id = $2`, requestID, e.SubscriberID)
		if err != nil {
			return fmt.Errorf("pgstore: consuming the deletion code: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return app.ErrSessionConflict
		}
		if err := lockSubscriberErasure(ctx, tx, e.SubscriberID); err != nil {
			return err
		}
		if err := eraseRows(ctx, tx, e); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO identity_deletion_code_receipts (request_id, code_hash, expires_at) VALUES ($1, $2, $3)`,
			requestID, codeHash, us(e.ReceiptExpiresAt)); err != nil {
			return fmt.Errorf("pgstore: writing the deletion receipt: %w", err)
		}
		return nil
	})
}

// DeletionCodeReceipt implements app.WebErasureRepository. An expired receipt
// is not found, whether or not housekeeping has deleted it yet.
func (r *Repository) DeletionCodeReceipt(ctx context.Context, requestID string) (string, bool, error) {
	var hash string
	var expiresAt time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT code_hash, expires_at FROM identity_deletion_code_receipts WHERE request_id = $1`,
		requestID).Scan(&hash, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("pgstore: reading a deletion receipt: %w", err)
	}
	if time.Now().After(expiresAt) {
		return "", false, nil
	}
	return hash, true, nil
}
