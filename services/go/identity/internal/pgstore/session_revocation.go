package pgstore

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// Revoke-all (app/session_signout.go; migration 0012).

var _ app.SessionRevocationRepository = (*Repository)(nil)

const (
	// At most revokeAllAttempts transactions are run by RevokeAllSessions
	// when PostgreSQL aborts one as a deadlock or serialisation victim.
	revokeAllAttempts = 2

	sqlstateDeadlock      = "40P01"
	sqlstateSerialization = "40001"
)

// RevokeSessionOnce implements app.SessionRevocationRepository: one
// conditional update. A concurrent call waits for this one's row lock, then
// (read committed re-checks the condition) finds the row revoked and changes
// nothing, so at most one call is true. It takes no account lock.
func (r *Repository) RevokeSessionOnce(ctx context.Context, sessionID string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `UPDATE identity_sessions SET is_revoked = TRUE WHERE session_id = $1 AND NOT is_revoked`, sessionID)
	if err != nil {
		return false, fmt.Errorf("pgstore: revoking the session: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// RevokeAllSessions implements app.SessionRevocationRepository in one
// transaction, under PostgreSQL's default isolation (read committed):
//
//  1. The account's lock, the one a fenced sign-in (SaveSession) and an
//     erasure take: a sign-in commits its session before this point, and is
//     revoked below, or waits and commits after this operation, untouched.
//  2. The session that asked, locked FOR UPDATE and checked again (account,
//     key, refresh token; then revoked or not, and expiry).
//  3. Every session of the account not yet revoked is revoked, and the
//     operation recorded with how many: both or neither.
//
// A refresh racing this one locks its row first (its swap commits, and the
// rotated row is revoked by step 3, which waits for that lock) or second (its
// swap then sees the row revoked and stores nothing). An erasure takes the
// account's lock first too (erasure.go), so the two queue rather than
// deadlock. Should PostgreSQL still abort an attempt as a deadlock or
// serialisation victim, it rolls back whole and is retried, up to
// revokeAllAttempts in all, and then answered ErrSignOutContended.
func (r *Repository) RevokeAllSessions(ctx context.Context, op *app.SessionRevocation) (int, bool, error) {
	if !app.ValidRefreshTokenHash(op.RefreshTokenHash) {
		return 0, false, app.ErrRefreshTokenNotHashed
	}
	var revoked int
	var repeated bool
	err := retryContended(revokeAllAttempts, func() error {
		var err error
		revoked, repeated, err = r.revokeAllOnce(ctx, op)
		return err
	})
	if err != nil {
		return 0, false, err
	}
	return revoked, repeated, nil
}

// retryContended runs attempt until it succeeds, fails for any reason but a
// deadlock or serialisation abort, or has run attempts times. An abort on the
// last attempt is ErrSignOutContended.
func retryContended(attempts int, attempt func() error) error {
	for i := 1; ; i++ {
		err := attempt()
		if err == nil || !contended(err) {
			return err
		}
		if i >= attempts {
			return fmt.Errorf("%w: %w", app.ErrSignOutContended, Redact(err))
		}
	}
}

// contended reports whether PostgreSQL aborted the transaction as a deadlock
// or serialisation victim: it rolled back, and may be run again.
func contended(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (pg.Code == sqlstateDeadlock || pg.Code == sqlstateSerialization)
}

func (r *Repository) revokeAllOnce(ctx context.Context, op *app.SessionRevocation) (int, bool, error) {
	var revoked int
	var repeated bool
	err := inTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := lockSubscriberErasure(ctx, tx, op.SubscriberID); err != nil {
			return err
		}
		isRevoked, err := lockAskingSession(ctx, tx, op)
		if err != nil {
			return err
		}
		if isRevoked {
			// A retry: done, if this very operation was recorded.
			if rerr := revocationRecorded(ctx, tx, op); rerr != nil {
				return rerr
			}
			repeated = true
			return nil
		}
		tag, err := tx.Exec(ctx, `
			UPDATE identity_sessions SET is_revoked = TRUE WHERE subscriber_id = $1 AND NOT is_revoked`, op.SubscriberID)
		if err != nil {
			return fmt.Errorf("pgstore: revoking the account's sessions: %w", err)
		}
		revoked = int(tag.RowsAffected())
		tag, err = tx.Exec(ctx, `
			INSERT INTO identity_session_revocations (session_id, refresh_token_hash, revoked_at, sessions_revoked)
			VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
			op.SessionID, op.RefreshTokenHash, us(op.RevokedAt), revoked)
		if err != nil {
			return fmt.Errorf("pgstore: recording the revocation: %w", err)
		}
		if tag.RowsAffected() != 1 {
			// The session was live and unrevoked under its lock, so no
			// record can exist; if one does, change nothing.
			return fmt.Errorf("pgstore: a revocation record already exists for a live session")
		}
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return revoked, repeated, nil
}

// lockAskingSession locks the session that asked and checks it again: it
// must exist, belong to op's account, and hold op's refresh token and key;
// a live one must also be unexpired. It reports whether it is revoked.
// Anything else is ErrSessionConflict.
func lockAskingSession(ctx context.Context, tx pgx.Tx, op *app.SessionRevocation) (bool, error) {
	var subscriber, tokenHash, key string
	var isRevoked bool
	var expiresAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT subscriber_id, refresh_token_hash, device_key_thumbprint, is_revoked, expires_at FROM identity_sessions
		WHERE session_id = $1 FOR UPDATE`, op.SessionID).Scan(&subscriber, &tokenHash, &key, &isRevoked, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, app.ErrSessionConflict
	}
	if err != nil {
		return false, fmt.Errorf("pgstore: locking the session that asked: %w", err)
	}
	if subscriber != op.SubscriberID || key == "" || key != op.DeviceKeyThumbprint ||
		subtle.ConstantTimeCompare([]byte(tokenHash), []byte(op.RefreshTokenHash)) != 1 {
		return false, app.ErrSessionConflict
	}
	if !isRevoked && !time.Now().Before(expiresAt) {
		return false, app.ErrSessionConflict
	}
	return isRevoked, nil
}

// revocationRecorded is nil if op itself was recorded (a retry), and
// ErrSessionConflict if not (the session was revoked some other way).
func revocationRecorded(ctx context.Context, tx pgx.Tx, op *app.SessionRevocation) error {
	var recorded bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM identity_session_revocations WHERE session_id = $1 AND refresh_token_hash = $2)`,
		op.SessionID, op.RefreshTokenHash).Scan(&recorded); err != nil {
		return fmt.Errorf("pgstore: reading the revocation record: %w", err)
	}
	if !recorded {
		return app.ErrSessionConflict
	}
	return nil
}
