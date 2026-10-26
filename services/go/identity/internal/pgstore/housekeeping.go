package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// # Housekeeping: what is deleted, and why it is safe
//
// Every rule below is derived from the code that reads the row, not from the
// table's name. "margin" is HousekeepingConfig.Margin, added beyond every
// expiry so that a replica whose clock lags the housekeeper's by less than the
// margin still sees a row it could act on. cutoff = now - margin.
//
//	identity_otp_challenges  expires_at < cutoff.
//	    validateOtp refuses a challenge once time.Now() is after expires_at,
//	    and deletes it itself when it is next presented; the expiry check runs
//	    before any rate-limit reservation, so an expired challenge never feeds a
//	    security decision. Deleting it only turns ErrExpiredOtp into
//	    ErrInvalidOtp for a caller who comes back more than margin later.
//	identity_otp_requests    requested_at < cutoff - window.
//	identity_otp_failures    failed_at    < cutoff - window.
//	    ReserveOtpRequest / ReserveOtpVerification count a row only while
//	    now - t < window, and prune older rows themselves; RetryAt is computed
//	    from rows inside the window. A row older than window + margin can no
//	    longer change a count, a refusal or a RetryAt. The lockout itself is
//	    not stored in these rows (see below), and a reservation is released or
//	    confirmed within the request that made it — milliseconds, not window +
//	    margin, later.
//	identity_otp_rate_state  no request or failure rows left AND
//	                         (locked_until IS NULL OR locked_until < cutoff).
//	    Such a row is exactly the empty state a never-seen number has: no
//	    counted events and no lockout in force. ConfirmOtpFailure reads
//	    locked_by only within the request that reserved it. Deleting it removes
//	    an MSISDN that no longer affects anything. A concurrent reservation
//	    either holds the row lock (and is skipped) or, if the row was deleted
//	    first, recreates it (lockRateState's upsert); a child row committed
//	    between this statement's snapshot and its delete makes the foreign key
//	    refuse the delete, failing the iteration rather than the request.
//	identity_sessions        expires_at < cutoff — revoked or not.
//	    RefreshToken and RefreshTokenWithDeviceProof refuse a session after
//	    expires_at, before any reuse check, with ErrInvalidToken; a missing
//	    session gets the same answer. Revoked sessions are kept until they would
//	    have expired: until then a revoked row and a missing row answer alike,
//	    and keeping it is the conservative choice. Access tokens never consult
//	    the session, so deleting it changes nothing for them.
//	identity_dpop_replay     expires_at < cutoff.
//	identity_erasure_receipts expires_at < cutoff (the erased session's expiry).
//
// NEVER TOUCHED: identity_erasures and identity_security_audit (append-only,
// LEGAL_HOLD).
//	    expires_at is iat + MaxAge + 1s, after which the verifier refuses the
//	    proof as stale on any replica whose clock is not behind; the margin
//	    covers replicas that are. Reserve already treats such rows as free.
//
// NOT TOUCHED: identity_device_attestations and identity_guardian_delegations
// (no reader or writer defines an expiry for them), and the migration ledger.
//
// # Bounded, coordinated, all-or-nothing
//
// One iteration is one transaction. It first takes a transaction-scoped
// advisory lock with pg_try_advisory_xact_lock: a replica that does not get it
// skips the iteration rather than waiting, and the lock is released by commit
// or rollback, never held across iterations. The lock is scoped to the tables
// being cleaned (housekeepingLockArgs), not to the whole database: every
// runner on the same identity tables contends for one lock, and a runner on
// another schema in the same database — independent tables — does not. Each table is then cleaned by one
// DELETE of at most BatchSize rows, chosen by an indexed predicate in age
// order with FOR UPDATE SKIP LOCKED, so a row a live request holds is left for
// a later iteration. A statement timeout and the context deadline bound the
// whole iteration; any error rolls ALL of it back, and the next iteration
// starts from scratch.

// The advisory lock for one housekeeping iteration is the two-key lock
// (housekeepingLockClass, oid of identity_sessions as this connection resolves
// it). Advisory locks are database-wide, so a single constant key — as this
// once was — made iterations on DIFFERENT schemas of one database exclude each
// other: the cleaned tables are what the lock protects, so they name it. Every
// runner on the same tables resolves the same oid and contends exactly as
// before; a missing table is an error, never a skip. Two-key advisory locks
// never collide with the single-key migration lock ("CSIDMIGR").
const (
	// The lock class, housekeepingLockClass, is ASCII "CSHK".
	housekeepingLockClass int32 = 0x4353484B
	// The housekeepingLockArgs value holds the lock functions' arguments; $1 is
	// housekeepingLockClass. The oid::int4 cast is a lossless reinterpretation.
	housekeepingLockArgs = `$1, 'identity_sessions'::regclass::oid::int4`
)

// Defaults, chosen conservatively. 500 rows per table per iteration bounds an
// iteration to 3,000 deletes; one hour of margin is far above any plausible
// clock skew between replicas (proof freshness itself allows 60s).
const (
	DefaultHousekeepingBatchSize        = 500
	DefaultHousekeepingMargin           = time.Hour
	DefaultHousekeepingIterationTimeout = 30 * time.Second
)

// HousekeepingConfig bounds and parameterises cleanup.
type HousekeepingConfig struct {
	// BatchSize is the most rows deleted from any one table per iteration.
	BatchSize int
	// Margin is kept beyond every expiry; at least 5 minutes.
	Margin time.Duration
	// RateWindow is the OTP rate-limit window rows are counted in
	// (app.OtpRatePolicy.Window).
	RateWindow time.Duration
	// IterationTimeout bounds one iteration, statements included.
	IterationTimeout time.Duration
}

// Validate refuses a configuration that could delete live state or run
// unbounded.
func (c HousekeepingConfig) Validate() error {
	switch {
	case c.BatchSize < 1 || c.BatchSize > 10_000:
		return errors.New("pgstore: housekeeping batch size must be in [1, 10000]")
	case c.Margin < 5*time.Minute:
		return errors.New("pgstore: housekeeping margin must be at least 5 minutes")
	case c.RateWindow <= 0:
		return errors.New("pgstore: housekeeping needs the rate-limit window")
	case c.IterationTimeout <= 0 || c.IterationTimeout > 5*time.Minute:
		return errors.New("pgstore: housekeeping iteration timeout must be in (0, 5m]")
	}
	return nil
}

// HousekeepingResult reports one iteration. Skipped means another replica held
// the lock and nothing was attempted.
type HousekeepingResult struct {
	Skipped                                                      bool
	Challenges, Requests, Failures, RateStates, Sessions, Proofs int64
	// ErasureReceipts are receipts of erased sessions past the session's expiry.
	ErasureReceipts int64
}

// Total is the number of rows the iteration deleted.
func (r HousekeepingResult) Total() int64 {
	return r.Challenges + r.Requests + r.Failures + r.RateStates + r.Sessions + r.Proofs + r.ErasureReceipts
}

// Housekeeper deletes expired identity state in bounded batches.
type Housekeeper struct {
	pool *pgxpool.Pool
	cfg  HousekeepingConfig
	now  func() time.Time
}

// NewHousekeeper validates cfg. A nil now means time.Now.
func NewHousekeeper(pool *pgxpool.Pool, cfg HousekeepingConfig, now func() time.Time) (*Housekeeper, error) {
	if pool == nil {
		return nil, errors.New("pgstore: housekeeping needs a pool")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &Housekeeper{pool: pool, cfg: cfg, now: now}, nil
}

// The bounded deletes, in dependency order: request and failure rows before the
// rate state that owns them. $1 is the cutoff, $2 the batch size.
var housekeepingSteps = []struct {
	name string
	sql  string
	rate bool // cutoff includes the rate-limit window
	set  func(*HousekeepingResult, int64)
}{
	{
		"challenges", `DELETE FROM identity_otp_challenges WHERE request_id IN (
		SELECT request_id FROM identity_otp_challenges WHERE expires_at < $1
		ORDER BY expires_at LIMIT $2 FOR UPDATE SKIP LOCKED)`, false,
		func(r *HousekeepingResult, n int64) { r.Challenges = n },
	},
	{
		"requests", `DELETE FROM identity_otp_requests WHERE id IN (
		SELECT id FROM identity_otp_requests WHERE requested_at < $1
		ORDER BY requested_at LIMIT $2 FOR UPDATE SKIP LOCKED)`, true,
		func(r *HousekeepingResult, n int64) { r.Requests = n },
	},
	{
		"failures", `DELETE FROM identity_otp_failures WHERE reservation_id IN (
		SELECT reservation_id FROM identity_otp_failures WHERE failed_at < $1
		ORDER BY failed_at LIMIT $2 FOR UPDATE SKIP LOCKED)`, true,
		func(r *HousekeepingResult, n int64) { r.Failures = n },
	},
	{
		"rate states", `DELETE FROM identity_otp_rate_state WHERE msisdn_pseudonym IN (
		SELECT s.msisdn_pseudonym FROM identity_otp_rate_state s
		WHERE (s.locked_until IS NULL OR s.locked_until < $1)
		  AND NOT EXISTS (SELECT 1 FROM identity_otp_requests q WHERE q.msisdn_pseudonym = s.msisdn_pseudonym)
		  AND NOT EXISTS (SELECT 1 FROM identity_otp_failures f WHERE f.msisdn_pseudonym = s.msisdn_pseudonym)
		LIMIT $2 FOR UPDATE SKIP LOCKED)`, false,
		func(r *HousekeepingResult, n int64) { r.RateStates = n },
	},
	{
		"sessions", `DELETE FROM identity_sessions WHERE session_id IN (
		SELECT session_id FROM identity_sessions WHERE expires_at < $1
		ORDER BY expires_at LIMIT $2 FOR UPDATE SKIP LOCKED)`, false,
		func(r *HousekeepingResult, n int64) { r.Sessions = n },
	},
	{
		"proofs", `DELETE FROM identity_dpop_replay WHERE (jkt, jti) IN (
		SELECT jkt, jti FROM identity_dpop_replay WHERE expires_at < $1
		ORDER BY expires_at LIMIT $2 FOR UPDATE SKIP LOCKED)`, false,
		func(r *HousekeepingResult, n int64) { r.Proofs = n },
	},
	{
		"erasure receipts", `DELETE FROM identity_erasure_receipts WHERE session_id IN (
		SELECT session_id FROM identity_erasure_receipts WHERE expires_at < $1
		ORDER BY expires_at LIMIT $2 FOR UPDATE SKIP LOCKED)`, false,
		func(r *HousekeepingResult, n int64) { r.ErasureReceipts = n },
	},
}

// RunOnce performs one bounded, coordinated iteration. On error nothing it did
// is kept, and the returned result is zero.
func (h *Housekeeper) RunOnce(ctx context.Context) (HousekeepingResult, error) {
	ctx, cancel := context.WithTimeout(ctx, h.cfg.IterationTimeout)
	defer cancel()
	cutoff := us(h.now().Add(-h.cfg.Margin))
	rateCutoff := us(cutoff.Add(-h.cfg.RateWindow))

	var res HousekeepingResult
	err := inTx(ctx, h.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('statement_timeout', $1, true)`,
			strconv.FormatInt(h.cfg.IterationTimeout.Milliseconds(), 10)); err != nil {
			return fmt.Errorf("pgstore: housekeeping timeout: %w", err)
		}
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(`+housekeepingLockArgs+`)`, housekeepingLockClass).Scan(&locked); err != nil {
			return fmt.Errorf("pgstore: housekeeping lock: %w", err)
		}
		if !locked {
			res.Skipped = true
			return nil
		}
		for _, step := range housekeepingSteps {
			at := cutoff
			if step.rate {
				at = rateCutoff
			}
			tag, err := tx.Exec(ctx, step.sql, at, h.cfg.BatchSize)
			if err != nil {
				return fmt.Errorf("pgstore: housekeeping %s: %w", step.name, err)
			}
			step.set(&res, tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		return HousekeepingResult{}, Redact(err)
	}
	return res, nil
}
