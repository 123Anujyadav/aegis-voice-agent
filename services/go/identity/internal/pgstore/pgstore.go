// Package pgstore is the PostgreSQL backend for the identity service's security
// state: app.IdentityRepository (OTP challenges, per-MSISDN rate limits and
// lockouts, sessions) and dpop.ReplayStore (proof reservations).
//
// It is an adapter. Every rule is the one app.MemoryIdentityRepository and
// dpop.MemoryReplayStore already implement; what changes is where atomicity
// comes from. There it is one process's mutex; here it is the database — a
// conditional statement, a row lock held for a transaction, or a uniqueness
// constraint — so it holds between independent processes sharing the database.
//
// Nothing here migrates on its own (see Migrate), logs, or reads configuration.
// Values are passed only as query parameters.
//
// # Refresh tokens are stored only as keyed hashes
//
// identity_sessions.refresh_token_hash holds app.HashRefreshToken values
// (migration 0002). SaveSession and ReplaceSession refuse a session carrying a
// raw token, and a CHECK constraint refuses any value that is not 64 lowercase
// hex characters. MSISDNs are still stored as given: GetOtp and GetSession
// return them.
//
// # Time
//
// PostgreSQL stores microseconds. Every time is truncated to the microsecond
// on the way in, so a value read back compares equal to the one written.
package pgstore

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/pgshared"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SQLSTATEs raised by the identity_sessions_guard trigger.
const (
	sqlstateKeyChange = "CSI01"
	sqlstateUnrevoke  = "CSI02"
)

// The connection helpers and the replay store moved to packages/go/pgshared so
// edge-api can use them; these names are aliases, so there is one
// implementation and errors.Is matches either name.
var (
	ErrInvalidDSN = pgshared.ErrInvalidDSN
	ErrConnect    = pgshared.ErrConnect
)

// Redact is pgshared.Redact.
func Redact(err error) error { return pgshared.Redact(err) }

// NewPool is pgshared.NewPool: policy is the TLS requirement (ADR-0029).
func NewPool(dsn string, policy pgshared.TLSPolicy) (*pgxpool.Pool, error) {
	return pgshared.NewPool(dsn, policy)
}

// Ping is pgshared.Ping.
func Ping(ctx context.Context, pool *pgxpool.Pool) error { return pgshared.Ping(ctx, pool) }

// OpenPool is pgshared.OpenPool: policy is the TLS requirement (ADR-0029).
func OpenPool(ctx context.Context, dsn string, policy pgshared.TLSPolicy) (*pgxpool.Pool, error) {
	return pgshared.OpenPool(ctx, dsn, policy)
}

func us(t time.Time) time.Time { return t.Truncate(time.Microsecond) }

// inTx runs fn in one transaction, committing only if fn succeeds. Any error
// rolls everything back, so no step of a multi-statement operation survives
// without the others.
func inTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }() //nolint:errcheck // after Commit this is a no-op; after a failure the original error is returned
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pgstore: commit: %w", err)
	}
	return nil
}

// mapGuard turns the session trigger's errors into the app's sentinels.
func mapGuard(err error) (bool, error) {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case sqlstateKeyChange:
			return true, app.ErrDeviceKeyAlreadyEnrolled
		case sqlstateUnrevoke:
			return true, app.ErrSessionConflict
		}
	}
	return false, err
}

// reservationFromDB converts a database reservation id (a positive BIGSERIAL)
// to the repository's uint64 form, refusing a value that could not be one.
func reservationFromDB(id int64) (uint64, error) {
	if id <= 0 {
		return 0, fmt.Errorf("pgstore: invalid OTP reservation id %d", id)
	}
	return uint64(id), nil
}

// reservationToDB converts a reservation id back to its BIGSERIAL form. An id
// beyond int64 cannot have come from the database, so it matches no row.
func reservationToDB(id uint64) (int64, bool) {
	if id > math.MaxInt64 {
		return 0, false
	}
	return int64(id), true
}

// ---------------------------------------------------------------------------
// IdentityRepository
// ---------------------------------------------------------------------------

// Repository is a PostgreSQL app.IdentityRepository.
type Repository struct{ pool *pgxpool.Pool }

var _ app.IdentityRepository = (*Repository)(nil)

// NewRepository returns a repository on pool, which must already be migrated.
func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// SaveOtp stores or replaces a challenge: one upsert.
func (r *Repository) SaveOtp(ctx context.Context, c *app.OtpChallenge) error {
	if !app.ValidMSISDNPseudonym(c.MSISDNPseudonym) {
		return app.ErrMSISDNNotPseudonymised
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO identity_otp_challenges (request_id, msisdn_pseudonym, subscriber_id, code_hash, attempts, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (request_id) DO UPDATE SET
			msisdn_pseudonym = EXCLUDED.msisdn_pseudonym, subscriber_id = EXCLUDED.subscriber_id,
			code_hash = EXCLUDED.code_hash, attempts = EXCLUDED.attempts, expires_at = EXCLUDED.expires_at`,
		c.RequestID, c.MSISDNPseudonym, c.SubscriberID, c.CodeHash, c.Attempts, us(c.ExpiresAt))
	if err != nil {
		return fmt.Errorf("pgstore: saving OTP challenge: %w", err)
	}
	return nil
}

// GetOtp returns a fresh copy, or app.ErrInvalidOtp when there is none.
func (r *Repository) GetOtp(ctx context.Context, requestID string) (*app.OtpChallenge, error) {
	c := app.OtpChallenge{RequestID: requestID}
	err := r.pool.QueryRow(ctx, `
		SELECT msisdn_pseudonym, subscriber_id, code_hash, attempts, expires_at FROM identity_otp_challenges WHERE request_id = $1`,
		requestID).Scan(&c.MSISDNPseudonym, &c.SubscriberID, &c.CodeHash, &c.Attempts, &c.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrInvalidOtp
	}
	if err != nil {
		return nil, fmt.Errorf("pgstore: reading OTP challenge: %w", err)
	}
	return &c, nil
}

// DeleteOtp removes a challenge if present.
func (r *Repository) DeleteOtp(ctx context.Context, requestID string) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM identity_otp_challenges WHERE request_id = $1`, requestID); err != nil {
		return fmt.Errorf("pgstore: deleting OTP challenge: %w", err)
	}
	return nil
}

// ConsumeOtp is one DELETE: of concurrent callers, only the one whose DELETE
// removed the row sees a row affected.
func (r *Repository) ConsumeOtp(ctx context.Context, requestID string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM identity_otp_challenges WHERE request_id = $1`, requestID)
	if err != nil {
		return false, fmt.Errorf("pgstore: consuming OTP challenge: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// RecordFailedOtpAttempt is one UPDATE ... RETURNING: it increments the stored
// counter and can never insert, so a consumed challenge stays gone.
func (r *Repository) RecordFailedOtpAttempt(ctx context.Context, requestID string) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `
		UPDATE identity_otp_challenges SET attempts = attempts + 1
		WHERE request_id = $1 RETURNING attempts`, requestID).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, app.ErrInvalidOtp
	}
	if err != nil {
		return 0, fmt.Errorf("pgstore: recording failed OTP attempt: %w", err)
	}
	return n, nil
}

// rateState is one MSISDN's lock row, read under FOR UPDATE.
type rateState struct {
	lockedUntil time.Time
	lockedBy    int64
}

// lockRateState creates msisdn's state row if needed and locks it for the
// rest of tx. Every rate-limit operation on one number therefore runs one at a
// time, across every process.
func lockRateState(ctx context.Context, tx pgx.Tx, pseudonym string, create bool) (*rateState, error) {
	var until *time.Time
	var by *int64
	var err error
	if create {
		// ONE statement creates-or-locks the row and reads it. It used to be an
		// INSERT ... DO NOTHING followed by SELECT ... FOR UPDATE; housekeeping
		// deleting an idle row between the two left the SELECT with nothing to
		// lock. ON CONFLICT DO UPDATE takes the row lock as part of the upsert,
		// and if the row was deleted meanwhile it inserts a fresh one.
		err = tx.QueryRow(ctx, `
			INSERT INTO identity_otp_rate_state (msisdn_pseudonym) VALUES ($1)
			ON CONFLICT (msisdn_pseudonym) DO UPDATE SET msisdn_pseudonym = EXCLUDED.msisdn_pseudonym
			RETURNING locked_until, locked_by`, pseudonym).Scan(&until, &by)
		if err != nil {
			return nil, fmt.Errorf("pgstore: creating rate state: %w", err)
		}
	} else {
		err = tx.QueryRow(ctx, `
			SELECT locked_until, locked_by FROM identity_otp_rate_state WHERE msisdn_pseudonym = $1 FOR UPDATE`,
			pseudonym).Scan(&until, &by)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pgstore: locking rate state: %w", err)
	}
	st := &rateState{}
	if until != nil {
		st.lockedUntil = *until
	}
	if by != nil {
		st.lockedBy = *by
	}
	return st, nil
}

// windowCount prunes table's rows for msisdn to the window and returns how
// many remain and the oldest. The table and column arguments are constants of
// this file.
func windowCount(ctx context.Context, tx pgx.Tx, table, column, pseudonym string, cutoff time.Time) (int, time.Time, error) {
	if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE msisdn_pseudonym = $1 AND `+column+` <= $2`, pseudonym, cutoff); err != nil {
		return 0, time.Time{}, fmt.Errorf("pgstore: pruning %s: %w", table, err)
	}
	var n int
	var oldest *time.Time
	if err := tx.QueryRow(ctx, `SELECT count(*), min(`+column+`) FROM `+table+` WHERE msisdn_pseudonym = $1`, pseudonym).Scan(&n, &oldest); err != nil {
		return 0, time.Time{}, fmt.Errorf("pgstore: counting %s: %w", table, err)
	}
	if oldest == nil {
		return n, time.Time{}, nil
	}
	return n, *oldest, nil
}

// refusal is the memory repository's limitedUntil: the later of the lockout
// end and, if the window is full, when it next frees a slot.
func refusal(st *rateState, now time.Time, count, limit int, oldest time.Time, window time.Duration) error {
	var retryAt time.Time
	if now.Before(st.lockedUntil) {
		retryAt = st.lockedUntil
	}
	if count >= limit {
		if at := oldest.Add(window); at.After(retryAt) {
			retryAt = at
		}
	}
	return &app.RateLimitError{RetryAt: retryAt}
}

// ReserveOtpRequest runs in one transaction holding msisdn's row lock: prune,
// count, then refuse or record. A refusal still commits the pruning.
func (r *Repository) ReserveOtpRequest(ctx context.Context, pseudonym string, now time.Time, p app.OtpRatePolicy) error {
	if !app.ValidMSISDNPseudonym(pseudonym) {
		return app.ErrMSISDNNotPseudonymised
	}
	now = us(now)
	var refused error
	err := inTx(ctx, r.pool, func(tx pgx.Tx) error {
		st, err := lockRateState(ctx, tx, pseudonym, true)
		if err != nil {
			return err
		}
		n, oldest, err := windowCount(ctx, tx, "identity_otp_requests", "requested_at", pseudonym, now.Add(-p.Window))
		if err != nil {
			return err
		}
		if now.Before(st.lockedUntil) || n >= p.MaxRequests {
			refused = refusal(st, now, n, p.MaxRequests, oldest, p.Window)
			return nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO identity_otp_requests (msisdn_pseudonym, requested_at) VALUES ($1, $2)`, pseudonym, now); err != nil {
			return fmt.Errorf("pgstore: recording OTP request: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return refused
}

// ReserveOtpVerification runs in one transaction holding msisdn's row lock:
// prune, count, refuse or record a provisional failure under a sequence id,
// and set the lockout if this reservation fills the window.
func (r *Repository) ReserveOtpVerification(ctx context.Context, pseudonym string, now time.Time, p app.OtpRatePolicy) (uint64, error) {
	if !app.ValidMSISDNPseudonym(pseudonym) {
		return 0, app.ErrMSISDNNotPseudonymised
	}
	now = us(now)
	var id int64
	var refused error
	err := inTx(ctx, r.pool, func(tx pgx.Tx) error {
		st, err := lockRateState(ctx, tx, pseudonym, true)
		if err != nil {
			return err
		}
		n, oldest, err := windowCount(ctx, tx, "identity_otp_failures", "failed_at", pseudonym, now.Add(-p.Window))
		if err != nil {
			return err
		}
		if now.Before(st.lockedUntil) || n >= p.MaxFailures {
			refused = refusal(st, now, n, p.MaxFailures, oldest, p.Window)
			return nil
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO identity_otp_failures (reservation_id, msisdn_pseudonym, failed_at)
			VALUES (nextval('identity_otp_reservation_seq'), $1, $2) RETURNING reservation_id`,
			pseudonym, now).Scan(&id); err != nil {
			return fmt.Errorf("pgstore: reserving OTP verification: %w", err)
		}
		if n+1 >= p.MaxFailures {
			if _, err := tx.Exec(ctx, `UPDATE identity_otp_rate_state SET locked_until = $2, locked_by = $3 WHERE msisdn_pseudonym = $1`,
				pseudonym, us(now.Add(p.Lockout)), id); err != nil {
				return fmt.Errorf("pgstore: recording lockout: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if refused != nil {
		return 0, refused
	}
	return reservationFromDB(id)
}

// ReleaseOtpVerification deletes the reservation and, if it set the lockout,
// the lockout — in one transaction under the row lock.
func (r *Repository) ReleaseOtpVerification(ctx context.Context, pseudonym string, reservationID uint64) error {
	if !app.ValidMSISDNPseudonym(pseudonym) {
		return app.ErrMSISDNNotPseudonymised
	}
	dbID, ok := reservationToDB(reservationID)
	if !ok {
		return nil // no database reservation has this id: nothing to release
	}
	return inTx(ctx, r.pool, func(tx pgx.Tx) error {
		st, err := lockRateState(ctx, tx, pseudonym, false)
		if err != nil || st == nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM identity_otp_failures WHERE msisdn_pseudonym = $1 AND reservation_id = $2`, pseudonym, dbID)
		if err != nil {
			return fmt.Errorf("pgstore: releasing OTP verification: %w", err)
		}
		if tag.RowsAffected() == 1 && st.lockedBy == dbID {
			if _, err := tx.Exec(ctx, `UPDATE identity_otp_rate_state SET locked_until = NULL, locked_by = NULL WHERE msisdn_pseudonym = $1`, pseudonym); err != nil {
				return fmt.Errorf("pgstore: withdrawing lockout: %w", err)
			}
		}
		return nil
	})
}

// ConfirmOtpFailure is one read: the lockout end if this reservation set the
// lockout and it has not been cleared. Only the reservation that set it can
// match, so one lockout is reported once.
func (r *Repository) ConfirmOtpFailure(ctx context.Context, pseudonym string, reservationID uint64) (time.Time, error) {
	if !app.ValidMSISDNPseudonym(pseudonym) {
		return time.Time{}, app.ErrMSISDNNotPseudonymised
	}
	dbID, ok := reservationToDB(reservationID)
	if reservationID == 0 || !ok {
		return time.Time{}, nil
	}
	var until *time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT locked_until FROM identity_otp_rate_state
		WHERE msisdn_pseudonym = $1 AND locked_by = $2 AND locked_until IS NOT NULL`,
		pseudonym, dbID).Scan(&until)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("pgstore: reading lockout: %w", err)
	}
	return *until, nil
}

// ResetOtpFailures clears the number's failures and lockout together.
func (r *Repository) ResetOtpFailures(ctx context.Context, pseudonym string) error {
	if !app.ValidMSISDNPseudonym(pseudonym) {
		return app.ErrMSISDNNotPseudonymised
	}
	return inTx(ctx, r.pool, func(tx pgx.Tx) error {
		st, err := lockRateState(ctx, tx, pseudonym, false)
		if err != nil || st == nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM identity_otp_failures WHERE msisdn_pseudonym = $1`, pseudonym); err != nil {
			return fmt.Errorf("pgstore: clearing OTP failures: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE identity_otp_rate_state SET locked_until = NULL, locked_by = NULL WHERE msisdn_pseudonym = $1`, pseudonym); err != nil {
			return fmt.Errorf("pgstore: clearing lockout: %w", err)
		}
		return nil
	})
}

// SaveSession is one upsert. On conflict it updates only if the stored key is
// unenrolled or unchanged; otherwise no row is affected and the write is
// refused. A revocation already stored is kept (is_revoked = old OR new).
func (r *Repository) SaveSession(ctx context.Context, s *app.UserSession) error {
	if err := app.CheckStorableSession(s); err != nil {
		return err
	}
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO identity_sessions (session_id, subscriber_id, refresh_token_hash, device_id, roles,
			created_at, expires_at, is_revoked, device_key_thumbprint, integrity_tier)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (session_id) DO UPDATE SET
			subscriber_id = EXCLUDED.subscriber_id,
			refresh_token_hash = EXCLUDED.refresh_token_hash, device_id = EXCLUDED.device_id,
			roles = EXCLUDED.roles, created_at = EXCLUDED.created_at, expires_at = EXCLUDED.expires_at,
			is_revoked = identity_sessions.is_revoked OR EXCLUDED.is_revoked,
			device_key_thumbprint = EXCLUDED.device_key_thumbprint,
			integrity_tier = EXCLUDED.integrity_tier
		WHERE identity_sessions.device_key_thumbprint = ''
		   OR identity_sessions.device_key_thumbprint = EXCLUDED.device_key_thumbprint`,
		s.SessionID, s.SubscriberID, s.RefreshTokenHash, s.DeviceID, s.Roles,
		us(s.CreatedAt), us(s.ExpiresAt), s.IsRevoked, s.DeviceKeyThumbprint, storedTier(s.IntegrityTier))
	if err != nil {
		if ok, g := mapGuard(err); ok {
			return g
		}
		return fmt.Errorf("pgstore: saving session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return app.ErrDeviceKeyAlreadyEnrolled
	}
	return nil
}

func scanSession(row pgx.Row, s *app.UserSession) error {
	var tier string
	if err := row.Scan(&s.SubscriberID, &s.RefreshTokenHash, &s.DeviceID, &s.Roles,
		&s.CreatedAt, &s.ExpiresAt, &s.IsRevoked, &s.DeviceKeyThumbprint, &tier); err != nil {
		return err
	}
	s.IntegrityTier = app.SessionIntegrityTier(tier)
	return nil
}

// storedTier is the integrity tier as stored: the zero value is 'unverified',
// as the memory repository stores it (ADR-0031 D31.4).
func storedTier(t app.SessionIntegrityTier) string {
	if t == "" {
		return string(app.SessionUnverified)
	}
	return string(t)
}

const sessionColumns = `subscriber_id, refresh_token_hash, device_id, roles, created_at, expires_at, is_revoked, device_key_thumbprint, integrity_tier`

// GetSession returns a fresh copy, or app.ErrInvalidToken when there is none.
func (r *Repository) GetSession(ctx context.Context, sessionID string) (*app.UserSession, error) {
	s := app.UserSession{SessionID: sessionID}
	err := scanSession(r.pool.QueryRow(ctx, `SELECT `+sessionColumns+` FROM identity_sessions WHERE session_id = $1`, sessionID), &s)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrInvalidToken
	}
	if err != nil {
		return nil, fmt.Errorf("pgstore: reading session: %w", err)
	}
	return &s, nil
}

// ReplaceSession is the compare-and-swap, in one transaction: the stored row is
// locked FOR UPDATE, compared, and only then written. A concurrent swap or
// revocation waits for the lock and then sees this one's result.
func (r *Repository) ReplaceSession(ctx context.Context, s *app.UserSession, expectedRefreshTokenHash string) error {
	if err := app.CheckStorableSession(s); err != nil {
		return err
	}
	return inTx(ctx, r.pool, func(tx pgx.Tx) error {
		var revoked bool
		var tokenHash, key string
		err := tx.QueryRow(ctx, `
			SELECT is_revoked, refresh_token_hash, device_key_thumbprint FROM identity_sessions
			WHERE session_id = $1 FOR UPDATE`, s.SessionID).Scan(&revoked, &tokenHash, &key)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrSessionConflict
		}
		if err != nil {
			return fmt.Errorf("pgstore: locking session: %w", err)
		}
		if revoked || subtle.ConstantTimeCompare([]byte(tokenHash), []byte(expectedRefreshTokenHash)) != 1 {
			return app.ErrSessionConflict
		}
		if key != "" && s.DeviceKeyThumbprint != key {
			return app.ErrDeviceKeyAlreadyEnrolled
		}
		if _, err := tx.Exec(ctx, `
			UPDATE identity_sessions SET subscriber_id = $2, refresh_token_hash = $3, device_id = $4,
				roles = $5, created_at = $6, expires_at = $7, is_revoked = $8, device_key_thumbprint = $9,
				integrity_tier = $10
			WHERE session_id = $1`,
			s.SessionID, s.SubscriberID, s.RefreshTokenHash, s.DeviceID, s.Roles,
			us(s.CreatedAt), us(s.ExpiresAt), s.IsRevoked, s.DeviceKeyThumbprint, storedTier(s.IntegrityTier)); err != nil {
			if ok, g := mapGuard(err); ok {
				return g
			}
			return fmt.Errorf("pgstore: replacing session: %w", err)
		}
		// The replaced token is recorded in the same transaction, so a
		// rotation that commits always leaves its predecessor recognisable as
		// reuse (SupersededRefreshToken), and one that rolls back leaves none.
		if s.RefreshTokenHash != expectedRefreshTokenHash {
			if _, err := tx.Exec(ctx, `
				INSERT INTO identity_refresh_tokens_superseded (session_id, refresh_token_hash, superseded_at)
				VALUES ($1, $2, now()) ON CONFLICT DO NOTHING`,
				s.SessionID, expectedRefreshTokenHash); err != nil {
				return fmt.Errorf("pgstore: recording the superseded refresh token: %w", err)
			}
		}
		return nil
	})
}

// SupersededRefreshToken is one primary-key lookup.
func (r *Repository) SupersededRefreshToken(ctx context.Context, sessionID, refreshTokenHash string) (bool, error) {
	var superseded bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM identity_refresh_tokens_superseded
			WHERE session_id = $1 AND refresh_token_hash = $2)`,
		sessionID, refreshTokenHash).Scan(&superseded)
	if err != nil {
		return false, fmt.Errorf("pgstore: checking for a superseded refresh token: %w", Redact(err))
	}
	return superseded, nil
}

// OtherDeviceKeyEnrolled is one indexed read (identity_sessions_subscriber).
func (r *Repository) OtherDeviceKeyEnrolled(ctx context.Context, subscriberID, thumbprint, exceptSessionID string) (bool, error) {
	var other bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM identity_sessions
			WHERE subscriber_id = $1 AND session_id <> $3
			  AND device_key_thumbprint <> '' AND device_key_thumbprint <> $2)`,
		subscriberID, thumbprint, exceptSessionID).Scan(&other)
	if err != nil {
		return false, fmt.Errorf("pgstore: checking for another enrolled device: %w", Redact(err))
	}
	return other, nil
}

// RevokeSession is one UPDATE; a missing session is not an error.
func (r *Repository) RevokeSession(ctx context.Context, sessionID string) error {
	if _, err := r.pool.Exec(ctx, `UPDATE identity_sessions SET is_revoked = TRUE WHERE session_id = $1`, sessionID); err != nil {
		return fmt.Errorf("pgstore: revoking session: %w", err)
	}
	return nil
}

// SaveDevice stores or replaces an attestation.
func (r *Repository) SaveDevice(ctx context.Context, d *app.DeviceAttestation) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO identity_device_attestations (device_id, model, os_version, integrity_token, attested_at, is_play_certified)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (device_id) DO UPDATE SET model = EXCLUDED.model, os_version = EXCLUDED.os_version,
			integrity_token = EXCLUDED.integrity_token, attested_at = EXCLUDED.attested_at,
			is_play_certified = EXCLUDED.is_play_certified`,
		d.DeviceID, d.Model, d.OsVersion, d.IntegrityToken, us(d.AttestedAt), d.IsPlayCertified)
	if err != nil {
		return fmt.Errorf("pgstore: saving device: %w", err)
	}
	return nil
}

// GetGuardianDelegation returns a delegation, or app.ErrGuardianForbidden.
func (r *Repository) GetGuardianDelegation(ctx context.Context, guardianID, subscriberID string) (*app.GuardianDelegation, error) {
	g := app.GuardianDelegation{GuardianID: guardianID, SubscriberID: subscriberID}
	err := r.pool.QueryRow(ctx, `
		SELECT permissions, granted_at FROM identity_guardian_delegations
		WHERE guardian_id = $1 AND subscriber_id = $2`, guardianID, subscriberID).Scan(&g.Permissions, &g.GrantedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrGuardianForbidden
	}
	if err != nil {
		return nil, fmt.Errorf("pgstore: reading guardian delegation: %w", err)
	}
	return &g, nil
}

// ---------------------------------------------------------------------------
// ReplayStore
// ---------------------------------------------------------------------------

// ReplayStore is pgshared.ReplayStore: the PostgreSQL dpop.ReplayStore shared by
// every process — identity's and edge-api's — using this database.
type ReplayStore = pgshared.ReplayStore

// NewReplayStore returns a replay store on pool, which must already be migrated.
func NewReplayStore(pool *pgxpool.Pool) *ReplayStore { return pgshared.NewReplayStore(pool) }
