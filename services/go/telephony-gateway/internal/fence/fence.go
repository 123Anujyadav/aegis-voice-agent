// Package fence is the telephony-gateway's single-active-process fence
// (Phase 19 P1-30A, decision D6: "evaluate a PostgreSQL session advisory-lock
// fence, with fail-closed readiness").
//
// # What it does
//
// One process at a time holds a PostgreSQL SESSION advisory lock, on a
// DEDICATED connection opened for nothing else, and reports itself owner only
// while that ownership is freshly confirmed:
//
//   - Acquire: pg_try_advisory_lock on a new connection (never a pooled one: a
//     session lock belongs to one backend, and a pool hands queries to whichever
//     connection is free, so a "lock held" on one pooled connection says nothing
//     about the next query's — TestPostgresFence_APooledConnectionDoesNotCarryTheLock).
//   - Heartbeat: every HeartbeatInterval, under HeartbeatTimeout, ask pg_locks
//     whether THIS backend still holds the lock. Any error, timeout or "no"
//     ends ownership at once, and the connection is closed.
//   - Self-fencing by time: Owned() is true only until
//     lastConfirmation + HeartbeatInterval + HeartbeatTimeout, judged on the
//     process's own monotonic clock, so a stuck or paused heartbeat cannot keep
//     a stale "owned".
//   - Acquisition grace: a NEW holder waits that same window after acquiring
//     before it reports Owned. The server can release a lock while its old
//     holder is cut off and still within its window; the grace covers exactly
//     that window, so the old holder has self-fenced before the new one starts.
//
// # What it does NOT guarantee
//
// This is a fence on the platform's own decision to admit work. It is NOT a
// proof that only one process acts at the carrier:
//
//   - work admitted while Owned (a carrier request already sent) is not
//     recalled when ownership ends;
//   - the grace argument assumes both processes use the same timing, their
//     clocks advance at a similar rate, and a process paused longer than the
//     window re-checks Owned() before acting (it reads false after resuming);
//   - availability after a partition depends on how fast the SERVER notices the
//     old session is gone (TCP keepalive / idle-session settings) — until then
//     the lock stays held and no process is owner (safe, but down);
//   - a connection pooler in transaction mode between gateway and database
//     breaks session locks entirely and must not be used for this connection;
//   - PostgreSQL failover: session locks do not survive it; both processes
//     reconnect to the new primary and the grace applies again.
//
// No timing value is defaulted here (decision D10 is unresolved): every
// duration and the lock key are required configuration.
package fence

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Name is the fence's lifecycle and health-check name.
const Name = "telephony-gateway-fence"

// ErrNotOwner is the readiness error while the process does not own the fence.
var ErrNotOwner = errors.New("fence: this process is not the active owner")

// ErrInvalidConfig is returned for a missing or inconsistent configuration.
var ErrInvalidConfig = errors.New("fence: invalid configuration")

// Config configures the fence. Every field is required; there are no defaults.
type Config struct {
	// LockKey is the advisory-lock key shared by every gateway process of one
	// deployment. Non-zero.
	LockKey int64
	// HeartbeatInterval is how often ownership is re-confirmed.
	HeartbeatInterval time.Duration
	// HeartbeatTimeout bounds one acquisition or confirmation round trip
	// (and the dial). Shorter than HeartbeatInterval.
	HeartbeatTimeout time.Duration
}

// Validate reports every problem.
func (c Config) Validate() error {
	var p []string
	if c.LockKey == 0 {
		p = append(p, "LockKey must be set (non-zero)")
	}
	if c.HeartbeatInterval <= 0 {
		p = append(p, "HeartbeatInterval must be positive")
	}
	if c.HeartbeatTimeout <= 0 {
		p = append(p, "HeartbeatTimeout must be positive")
	} else if c.HeartbeatInterval > 0 && c.HeartbeatTimeout >= c.HeartbeatInterval {
		p = append(p, "HeartbeatTimeout must be shorter than HeartbeatInterval")
	}
	if len(p) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, strings.Join(p, "; "))
	}
	return nil
}

// Validity is how long one confirmation keeps the process owner, and how long
// a new holder waits before claiming ownership.
func (c Config) Validity() time.Duration { return c.HeartbeatInterval + c.HeartbeatTimeout }

// Session is one dedicated database session that can hold the lock.
type Session interface {
	// Primary reports whether the session's server is a writable primary
	// (not in recovery). Advisory locks are per server: two processes
	// connected to DIFFERENT servers (a replica endpoint, a split-brain old
	// primary) could each hold "the" lock. The fence takes the lock only on a
	// primary. (P1-31)
	Primary(ctx context.Context) (bool, error)
	// TryLock attempts the session lock without waiting.
	TryLock(ctx context.Context, key int64) (bool, error)
	// Holds reports whether THIS session's backend holds the lock now, on a
	// server that is still a writable primary.
	Holds(ctx context.Context, key int64) (bool, error)
	// Unlock releases the lock if held.
	Unlock(ctx context.Context, key int64) error
	// Close ends the session (which also releases any lock it holds, once the
	// server sees it).
	Close(ctx context.Context) error
}

// Dialer opens a NEW dedicated session. It must never return a pooled one.
type Dialer func(ctx context.Context) (Session, error)

// Fence is a platform.Runner (Name/Run/Shutdown) and a platform.Checker.
type Fence struct {
	cfg   Config
	dial  Dialer
	log   *slog.Logger
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error

	mu        sync.Mutex
	held      bool      // the lock is believed held by our current session
	acquired  time.Time // when the current hold began
	confirmed time.Time // last successful confirmation

	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
	started  bool
}

// New builds a fence. log is required.
func New(cfg Config, dial Dialer, log *slog.Logger) (*Fence, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if dial == nil || log == nil {
		return nil, fmt.Errorf("%w: a dialer and a logger are required", ErrInvalidConfig)
	}
	return &Fence{
		cfg: cfg, dial: dial, log: log,
		now:   time.Now,
		sleep: sleepCtx,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Name implements platform.Runner.
func (f *Fence) Name() string { return Name }

// Owned reports whether this process is the active owner NOW: the lock was
// acquired at least Validity ago (grace) and confirmed within the last
// Validity. It turns false by the clock alone if confirmations stop.
func (f *Fence) Owned() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ownedLocked(f.now())
}

func (f *Fence) ownedLocked(now time.Time) bool {
	v := f.cfg.Validity()
	return f.held && now.Sub(f.acquired) >= v && now.Sub(f.confirmed) < v
}

// Check implements platform.Checker: readiness fails unless Owned.
func (f *Fence) Check(context.Context) error {
	if !f.Owned() {
		return ErrNotOwner
	}
	return nil
}

// Run implements platform.Runner: acquire, hold, re-acquire after a loss, until
// ctx is cancelled or Shutdown is called. Database failures never end Run
// (they leave the process not-owner, which readiness reports); it returns nil.
func (f *Fence) Run(ctx context.Context) error {
	f.mu.Lock()
	if f.started {
		f.mu.Unlock()
		return errors.New("fence: Run called twice")
	}
	f.started = true
	f.mu.Unlock()
	defer close(f.done)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-f.stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	for ctx.Err() == nil {
		f.holdOnce(ctx)
		if ctx.Err() != nil {
			break
		}
		_ = f.sleep(ctx, f.cfg.HeartbeatInterval)
	}
	return nil
}

// holdOnce opens a session, tries the lock and, if it got it, confirms it
// until confirmation fails or ctx ends. It always leaves the fence not-held.
func (f *Fence) holdOnce(ctx context.Context) {
	t := NewTimings() // P1-41 §8: measurement only; see timing.go
	dctx, dcancel := context.WithTimeout(WithTimings(ctx, t), f.cfg.HeartbeatTimeout)
	sess, err := f.dial(dctx)
	dcancel()
	if err != nil {
		f.log.WarnContext(ctx, "fence: cannot open a database session", slog.String("error", redact(err).Error()), t.attr())
		return
	}
	defer f.release(sess)

	pctx, pcancel := context.WithTimeout(ctx, f.cfg.HeartbeatTimeout)
	var primary bool
	t.time(PhasePrimary, func() { primary, err = sess.Primary(pctx) })
	pcancel()
	if err != nil || !primary {
		f.log.ErrorContext(ctx, "fence: the database is not a writable primary; refusing to take the lock",
			slog.String("error", errText(err)), t.attr())
		return
	}

	// Freshness is measured from when a query was SENT (P1-31): the server
	// evaluated it at some instant between send and receipt, so the send time
	// is the latest instant we can vouch for. The acquisition grace, by
	// contrast, runs from receipt — the later, conservative end for a new owner.
	sent := f.now()
	tctx, tcancel := context.WithTimeout(ctx, f.cfg.HeartbeatTimeout)
	var ok bool
	t.time(PhaseLock, func() { ok, err = sess.TryLock(tctx, f.cfg.LockKey) })
	tcancel()
	if err != nil {
		f.log.WarnContext(ctx, "fence: lock attempt failed", slog.String("error", redact(err).Error()), t.attr())
		return
	}
	if !ok {
		// Another process holds it. Debug: a standby repeats this every
		// interval.
		f.log.DebugContext(ctx, "fence: lock held elsewhere; standing by", t.attr())
		return
	}
	received := f.now()
	f.mu.Lock()
	f.held, f.acquired, f.confirmed = true, received, sent
	f.mu.Unlock()
	f.log.InfoContext(ctx, "fence: lock acquired; owner after the grace period",
		slog.Duration("grace", f.cfg.Validity()), t.attr())

	for {
		if err := f.sleep(ctx, f.cfg.HeartbeatInterval); err != nil {
			return // stopping: release() drops ownership first
		}
		sent := f.now()
		hctx, hcancel := context.WithTimeout(ctx, f.cfg.HeartbeatTimeout)
		ht := NewTimings()
		var holds bool
		var err error
		ht.time(PhaseHeartbeat, func() { holds, err = sess.Holds(hctx, f.cfg.LockKey) })
		hcancel()
		if err != nil || !holds {
			f.lose()
			f.log.ErrorContext(ctx, "fence: ownership LOST; no longer accepting work",
				slog.Bool("lock_missing", err == nil && !holds), slog.String("error", errText(err)), ht.attr())
			return
		}
		f.mu.Lock()
		// A confirmation sent after the validity window had already lapsed
		// does not revive ownership: the window is what the next holder's
		// grace is measured against.
		if sent.Sub(f.confirmed) >= f.cfg.Validity() {
			f.mu.Unlock()
			f.lose()
			f.log.ErrorContext(ctx, "fence: confirmation arrived after the validity window; ownership dropped", ht.attr())
			return
		}
		f.confirmed = sent
		f.mu.Unlock()
		// Debug: one line per heartbeat.
		f.log.DebugContext(ctx, "fence: ownership confirmed", ht.attr())
	}
}

// lose ends ownership locally (fail closed).
func (f *Fence) lose() {
	f.mu.Lock()
	f.held = false
	f.mu.Unlock()
}

// release ends ownership FIRST, then unlocks and closes the session, each
// bounded, ignoring failures (a session the server cannot reach is released by
// the server when it notices).
func (f *Fence) release(sess Session) {
	f.lose()
	ctx, cancel := context.WithTimeout(context.Background(), f.cfg.HeartbeatTimeout)
	defer cancel()
	_ = sess.Unlock(ctx, f.cfg.LockKey)
	_ = sess.Close(ctx)
}

// Shutdown implements platform.Runner: stop, drop ownership, release the lock,
// within ctx.
func (f *Fence) Shutdown(ctx context.Context) error {
	f.stopOnce.Do(func() { close(f.stop) })
	f.lose()
	f.mu.Lock()
	started := f.started
	f.mu.Unlock()
	if !started {
		return nil
	}
	select {
	case <-f.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("fence: shutdown: %w", ctx.Err())
	}
}

// ---------------------------------------------------------------------------
// PostgreSQL session
// ---------------------------------------------------------------------------

// PgxDialer opens a dedicated pgx connection (not a pool) for each session.
// cfg is copied per dial.
func PgxDialer(cfg *pgx.ConnConfig) Dialer {
	return func(ctx context.Context) (Session, error) {
		conn, err := instrumentedConnect(ctx, cfg.Copy())
		if err != nil {
			return nil, err
		}
		return pgSession{conn: conn}, nil
	}
}

// ErrCredentialsUnavailable is returned by a credential dialer when its
// Credentials source fails. The source's own error is deliberately dropped: it
// can carry account identifiers, role ARNs or token fragments.
var ErrCredentialsUnavailable = errors.New("fence: database credentials unavailable (details withheld)")

// CredentialsReason may be implemented by a Credentials source's errors to
// name a fixed, non-secret failure category (for example
// "sts_access_denied"). The fence keeps the category — so operators can tell
// an identity failure from a TLS or database failure — and still drops the
// error text. A category is kept only if it is 1–40 characters of [a-z0-9_].
type CredentialsReason interface {
	CredentialsReason() string
}

// credentialsError is ErrCredentialsUnavailable with a vetted category.
type credentialsError struct{ reason string }

func (e credentialsError) Error() string {
	return "fence: database credentials unavailable: " + e.reason + " (details withheld)"
}

func (e credentialsError) Is(target error) bool { return target == ErrCredentialsUnavailable }

func safeReason(r string) bool {
	if r == "" || len(r) > 40 {
		return false
	}
	for _, c := range r {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

// Credentials supplies the password for ONE new connection to host:port as
// user (for RDS IAM authentication: a freshly generated token). It is called
// on every dial — initial acquisition and every re-dial after a loss — under
// the dial's context, so it is bounded by the same HeartbeatTimeout as the
// connection itself. Implementations must never log the value. (P1-A1, C1)
//
// This package defines the interface only and imports no cloud SDK
// (ADR-0008 §14); a provider adapter lives in its own package.
type Credentials interface {
	Password(ctx context.Context, host string, port uint16, user string) (string, error)
}

// connectFunc opens a session from a fully prepared config.
type connectFunc func(ctx context.Context, cfg *pgx.ConnConfig) (Session, error)

func pgxConnect(ctx context.Context, cfg *pgx.ConnConfig) (Session, error) {
	conn, err := instrumentedConnect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return pgSession{conn: conn}, nil
}

// PgxCredentialDialer opens a dedicated pgx connection whose password is
// obtained from creds immediately before each connection. The password is set
// only on a per-dial copy of cfg; cfg itself is never modified, and nothing
// retains the password after the dial returns.
func PgxCredentialDialer(cfg *pgx.ConnConfig, creds Credentials) (Dialer, error) {
	if cfg == nil || creds == nil {
		return nil, fmt.Errorf("%w: a connection config and a credentials source are required", ErrInvalidConfig)
	}
	return credentialDialer(cfg, creds, pgxConnect), nil
}

func credentialDialer(cfg *pgx.ConnConfig, creds Credentials, connect connectFunc) Dialer {
	return func(ctx context.Context) (Session, error) {
		c := cfg.Copy()
		var password string
		var err error
		timingsFrom(ctx).time(PhaseCredentials, func() { password, err = creds.Password(ctx, c.Host, c.Port, c.User) })
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			var r CredentialsReason
			if errors.As(err, &r) && safeReason(r.CredentialsReason()) {
				return nil, credentialsError{reason: r.CredentialsReason()}
			}
			return nil, ErrCredentialsUnavailable
		}
		c.Password = password
		return connect(ctx, c)
	}
}

// ParseDSN parses a DSN without ever echoing it in an error.
func ParseDSN(dsn string) (*pgx.ConnConfig, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: database DSN does not parse (value not shown)", ErrInvalidConfig)
	}
	return cfg, nil
}

type pgSession struct{ conn *pgx.Conn }

func (s pgSession) Primary(ctx context.Context) (bool, error) {
	var primary bool
	err := s.conn.QueryRow(ctx, `SELECT NOT pg_is_in_recovery()`).Scan(&primary)
	return primary, err
}

func (s pgSession) TryLock(ctx context.Context, key int64) (bool, error) {
	var ok bool
	err := s.conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::bigint)`, key).Scan(&ok)
	return ok, err
}

// Holds asks pg_locks about THIS backend. A bigint advisory key is stored as
// classid = high 32 bits, objid = low 32 bits, objsubid = 1.
func (s pgSession) Holds(ctx context.Context, key int64) (bool, error) {
	hi := int64(uint64(key) >> 32)
	lo := int64(uint64(key) & 0xffffffff)
	var held bool
	err := s.conn.QueryRow(ctx, `SELECT NOT pg_is_in_recovery() AND EXISTS (
    SELECT 1 FROM pg_locks
    WHERE locktype = 'advisory' AND granted AND pid = pg_backend_pid()
      AND classid = $1::bigint::oid AND objid = $2::bigint::oid AND objsubid = 1)`, hi, lo).Scan(&held)
	return held, err
}

func (s pgSession) Unlock(ctx context.Context, key int64) error {
	_, err := s.conn.Exec(ctx, `SELECT pg_advisory_unlock($1::bigint)`, key)
	return err
}

func (s pgSession) Close(ctx context.Context) error { return s.conn.Close(ctx) }

// Fixed, detail-free categories for failures redact cannot otherwise name.
var (
	errTLSVerification = errors.New("TLS certificate verification failed (details withheld)")
	errNetwork         = errors.New("network error reaching the database (details withheld)")
)

// redact keeps context errors, SQLSTATE codes and fixed failure categories
// (credentials with a vetted reason, TLS verification, network); anything
// else (which can name a host, user or database) becomes a fixed message.
// Categories let staging separate identity, TLS, network and database
// failures (D10 evidence) without logging any of their details.
func redact(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		return context.Canceled
	}
	var ce credentialsError
	switch {
	case errors.As(err, &ce):
		return ce
	case errors.Is(err, ErrCredentialsUnavailable):
		return ErrCredentialsUnavailable
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return fmt.Errorf("postgres error %s", pg.Code)
	}
	var cv *tls.CertificateVerificationError
	if errors.As(err, &cv) {
		return errTLSVerification
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return errNetwork
	}
	return errors.New("database operation failed (details withheld)")
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return redact(err).Error()
}
