// Package pgshared is the PostgreSQL code identity and edge-api share:
// connection helpers that never leak credentials, and the shared DPoP replay
// store. It moved here from services/go/identity/internal/pgstore, which
// edge-api cannot import (Go's internal-package rule); pgstore keeps its old
// names as aliases of these, so there is one implementation.
//
// # The replay table
//
// ReplayStore uses identity_dpop_replay, created by identity's migration 0001
// (ReplayTableDDL is that migration's text, pinned by a test in pgstore). This
// package never applies DDL: the table is migrated by identity's explicit
// `service migrate`, and CheckReplaySchema lets another service refuse to start
// against a database that lacks it. Every process — identity's and edge-api's
// replicas alike — that reserves against the same table shares one replay
// space; (jkt, jti) keys cannot collide across purposes, since a proof's key
// and jti are the client's.
package pgshared

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrInvalidDSN is a connection string that does not parse. The value is never
// included: it may carry a password.
var ErrInvalidDSN = errors.New("postgres: invalid connection configuration (value not shown)")

// ErrConnect is a failure to reach or authenticate to the database. It stands
// in for pgconn.ConnectError, whose text names the user, database and host.
var ErrConnect = errors.New("postgres: connecting to the database failed (details not shown)")

// ErrReplaySchemaMissing is a reachable database without the replay table (or
// without access to it): identity's migrations have not been applied, or the
// role lacks privileges.
var ErrReplaySchemaMissing = errors.New("postgres: the identity_dpop_replay table is not available (run identity's `service migrate`, and grant this role access)")

// Redact returns err with any connection-attempt detail removed: an error
// carrying a *pgconn.ConnectError — user, database, host, address — becomes
// ErrConnect, keeping only the server's SQLSTATE when there is one (for
// example 28P01, authentication failed), which names no credential. Anything
// else is returned unchanged. Use it on every error that may reach a log, a
// probe body or a terminal.
func Redact(err error) error {
	var ce *pgconn.ConnectError
	if !errors.As(err, &ce) {
		return err
	}
	var pg *pgconn.PgError
	switch {
	case errors.As(err, &pg):
		return fmt.Errorf("%w: SQLSTATE %s", ErrConnect, pg.Code)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: timed out", ErrConnect)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("%w: cancelled", ErrConnect)
	}
	return ErrConnect
}

// NewPool parses dsn, enforces policy (ADR-0029: verify-full TLS with an
// explicit CA unless ENVIRONMENT is explicitly development, see
// PolicyForEnvironment), and creates a pool WITHOUT connecting; pgxpool
// connects lazily. Ping establishes and verifies a connection.
func NewPool(dsn string, policy TLSPolicy) (*pgxpool.Pool, error) {
	return newPool(context.Background(), dsn, policy)
}

// newPool is NewPool with the context pgxpool is built under.
func newPool(ctx context.Context, dsn string, policy TLSPolicy) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// ParseConfig's error can echo the DSN, which may carry a password.
		return nil, ErrInvalidDSN
	}
	if tlsErr := checkTLS(dsn, cfg, policy); tlsErr != nil {
		return nil, tlsErr
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, Redact(fmt.Errorf("postgres: creating connection pool: %w", err))
	}
	return pool, nil
}

// Ping verifies the pool can reach the database, with connection details
// redacted from any error.
func Ping(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pool.Ping(ctx); err != nil {
		return Redact(fmt.Errorf("postgres: ping: %w", err))
	}
	return nil
}

// OpenPool connects and verifies the connection, closing the pool if it
// cannot. It does not migrate. The TLS policy is as for NewPool.
func OpenPool(ctx context.Context, dsn string, policy TLSPolicy) (*pgxpool.Pool, error) {
	pool, err := newPool(ctx, dsn, policy)
	if err != nil {
		return nil, err
	}
	if err := Ping(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func us(t time.Time) time.Time { return t.Truncate(time.Microsecond) }

// ---------------------------------------------------------------------------
// ReplayStore
// ---------------------------------------------------------------------------

// ReplayTableDDL is the replay table exactly as identity's migration 0001
// creates it. It is not applied by this package; it exists so tests can build
// the table and so a test can prove the migration still says the same.
const ReplayTableDDL = `CREATE TABLE identity_dpop_replay (
    jkt         TEXT        NOT NULL CHECK (jkt <> ''),
    jti         TEXT        NOT NULL CHECK (jti <> ''),
    expires_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (jkt, jti)
);

CREATE INDEX identity_dpop_replay_expires_at ON identity_dpop_replay (expires_at);`

// CheckReplaySchema verifies, without changing anything, that the replay table
// exists with the columns ReplayStore uses and that this role can read it.
func CheckReplaySchema(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, `SELECT jkt, jti, expires_at FROM identity_dpop_replay LIMIT 0`)
	if err == nil {
		rows.Close()
		err = rows.Err()
	}
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) {
			return fmt.Errorf("%w (SQLSTATE %s)", ErrReplaySchemaMissing, pg.Code)
		}
		return Redact(fmt.Errorf("postgres: checking the replay table: %w", err))
	}
	return nil
}

// ReserveTimeout bounds one replay reservation. A reservation is a single
// indexed INSERT; two seconds is far beyond its normal latency and well inside
// a client's request timeout.
const ReserveTimeout = 2 * time.Second

// ReplayStore is a PostgreSQL dpop.ReplayStore shared by every process using
// the database.
type ReplayStore struct{ pool *pgxpool.Pool }

var _ dpop.ReplayStore = (*ReplayStore)(nil)

// NewReplayStore returns a replay store on pool, which must already be migrated.
func NewReplayStore(pool *pgxpool.Pool) *ReplayStore { return &ReplayStore{pool: pool} }

// Reserve is one statement. The primary key on (jkt, jti) makes a second insert
// conflict; the conflict takes the row over only if the held reservation has
// expired (expires_at <= now, as MemoryReplayStore), and otherwise affects no
// row, which is a replay. Any database error is returned as itself, and the
// verifier refuses the proof (dpop.ErrReplayUnavailable).
//
// The statement runs under ReserveTimeout at most: a stalled database must
// fail the request closed quickly, not queue it behind the pool without bound.
func (s *ReplayStore) Reserve(ctx context.Context, jkt, jti string, expiresAt, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, ReserveTimeout)
	defer cancel()
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO identity_dpop_replay (jkt, jti, expires_at) VALUES ($1, $2, $3)
		ON CONFLICT (jkt, jti) DO UPDATE SET expires_at = EXCLUDED.expires_at
		WHERE identity_dpop_replay.expires_at <= $4`,
		jkt, jti, us(expiresAt), us(now))
	if err != nil {
		return fmt.Errorf("postgres: reserving proof: %w", Redact(err))
	}
	if tag.RowsAffected() == 0 {
		return dpop.ErrReplayed
	}
	return nil
}

// Prune deletes at most limit expired reservations and reports how many. It is
// housekeeping only — Reserve is correct whether or not it ever runs, because
// an expired reservation is taken over on conflict. The identity service's housekeeping
// prunes this table (with its safety margin) when identity runs.
func (s *ReplayStore) Prune(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, errors.New("postgres: prune limit must be positive")
	}
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM identity_dpop_replay WHERE (jkt, jti) IN (
			SELECT jkt, jti FROM identity_dpop_replay WHERE expires_at <= $1
			ORDER BY expires_at LIMIT $2)`, us(now), limit)
	if err != nil {
		return 0, fmt.Errorf("postgres: pruning proof reservations: %w", Redact(err))
	}
	return tag.RowsAffected(), nil
}
