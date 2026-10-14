// Package telephonystore is the PostgreSQL adapter for durable telephony state.
//
// PHASE 19 P1-10. It holds carrier call-ID bindings — which carrier call is
// which platform call — in the `telephony` cluster (ADR-0009 §5: call
// sessions). It implements the provider-neutral ports in
// packages/go/telephony/callback: [callback.Resolver] and [callback.Binder].
//
// # What is NOT here: durable callback deduplication
//
// The P1-10 brief asked for a PostgreSQL implementation of callback.Store as
// well. ADR-0009 §5 (frozen) assigns "idempotency keys" to Redis as ephemeral
// state, and a callback event key is an idempotency key. Implementing it in
// PostgreSQL would silently depart from an accepted decision, so it is NOT
// implemented; see docs/telephony/P1-10_DURABLE_CALLBACK_STORE_REPORT.md.
//
// # Guarantees
//
// The binding contract is enforced by the database's constraints, so it holds
// across every process and replica sharing the table:
//
//   - (provider, carrier call ID) maps to exactly one platform call — the
//     primary key;
//   - a platform call is bound to at most one carrier call — UNIQUE (call_id);
//   - formats are CHECKed as well as validated in Go.
//
// Concurrent binds of the same carrier call race on the primary key: exactly
// one INSERT wins, and every other caller reads the winner and either succeeds
// (same platform call) or gets [callback.ErrBindingConflict].
//
// # Errors are safe to log
//
// No error from this package carries a DSN, host, user, query parameter or
// PostgreSQL message text. A database failure is reported as
// [ErrStoreUnavailable] with at most the SQLSTATE code and constraint name.
package telephonystore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
	"github.com/callscreen/callscreen-platform/packages/go/telephony/callback"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrStoreUnavailable is returned when the database could not answer: a
// connection, timeout or server failure. It is distinct from a missing
// binding ([callback.ErrUnknownCall]) and a conflict
// ([callback.ErrBindingConflict]), so a caller never mistakes an outage for
// either.
var ErrStoreUnavailable = fmt.Errorf("telephonystore: binding store unavailable: %w", callback.ErrStoreUnavailable)

// SQLSTATE codes this package interprets.
const sqlStateCheckViolation = "23514"

// The statements. Every value is a bind parameter; nothing is interpolated.
const (
	sqlBind = `INSERT INTO telephony_carrier_call_bindings (provider, provider_call_id, call_id)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING`

	sqlLookup = `SELECT call_id FROM telephony_carrier_call_bindings
WHERE provider = $1 AND provider_call_id = $2`

	sqlRemove = `DELETE FROM telephony_carrier_call_bindings
WHERE provider = $1 AND provider_call_id = $2`
)

// querier is the slice of *pgxpool.Pool this package uses.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var _ querier = (*pgxpool.Pool)(nil)

// Config configures [Bindings].
type Config struct {
	// OpTimeout bounds every database operation. Required: [Bindings.Resolve]
	// is called through the context-free callback.Resolver port, and an
	// operation without a bound is an operation that can hang a callback.
	OpTimeout time.Duration
}

// Bindings is a durable, provider-scoped binding registry.
type Bindings struct {
	q       querier
	calls   callback.CallProviders
	timeout time.Duration
}

var (
	_ callback.Resolver = (*Bindings)(nil)
	_ callback.Binder   = (*Bindings)(nil)
)

// NewBindings builds a registry over pool. calls is how a bind is checked
// against the live call it names. The pool is the caller's; this type never
// closes it.
func NewBindings(pool *pgxpool.Pool, calls callback.CallProviders, cfg Config) (*Bindings, error) {
	if pool == nil {
		return nil, errors.New("telephonystore: NewBindings requires a pool")
	}
	return newBindings(pool, calls, cfg)
}

func newBindings(q querier, calls callback.CallProviders, cfg Config) (*Bindings, error) {
	if calls == nil {
		return nil, errors.New("telephonystore: NewBindings requires a CallProviders")
	}
	if cfg.OpTimeout <= 0 {
		return nil, fmt.Errorf("telephonystore: OpTimeout must be positive, got %s", cfg.OpTimeout)
	}
	return &Bindings{q: q, calls: calls, timeout: cfg.OpTimeout}, nil
}

func (s *Bindings) opCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.timeout)
}

// BindCall implements [callback.Binder]: it records that b.ProviderCallID at
// b.Provider is the platform call b.Call.
//
// Binding the same triple again succeeds. Any other overlap — the carrier call
// already bound to another platform call, or the platform call already bound
// to another carrier call — is [callback.ErrBindingConflict], and the existing
// binding is unchanged.
func (s *Bindings) BindCall(ctx context.Context, b callback.Binding) error {
	if err := b.Validate(); err != nil {
		return err
	}
	carrier, err := s.calls.ProviderOf(b.Call)
	if err != nil {
		return fmt.Errorf("telephonystore: bind %s: %w", b.Call, err)
	}
	if carrier != b.Provider {
		return fmt.Errorf("%w: %s is carried by %s, not %s", callback.ErrProviderMismatch, b.Call, carrier, b.Provider)
	}

	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	tag, err := s.q.Exec(ctx, sqlBind, string(b.Provider), b.ProviderCallID, string(b.Call))
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == sqlStateCheckViolation {
			return fmt.Errorf("%w: refused by constraint %s", callback.ErrInvalidBinding, pg.ConstraintName)
		}
		return unavailable("bind", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	// Nothing was inserted: one of the two unique constraints already holds a
	// row. A second statement, with its own snapshot, sees the committed
	// winner. (A single statement could not: its snapshot predates a winner
	// that committed while ON CONFLICT was waiting on it.)
	var existing string
	err = s.q.QueryRow(ctx, sqlLookup, string(b.Provider), b.ProviderCallID).Scan(&existing)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// The carrier call is free, so it was UNIQUE (call_id) that held: the
		// platform call is bound to a different carrier call.
		return fmt.Errorf("%w: %s is already bound to another carrier call", callback.ErrBindingConflict, b.Call)
	case err != nil:
		return unavailable("bind lookup", err)
	case telephony.CallID(existing) == b.Call:
		return nil // the same binding, registered again
	default:
		return fmt.Errorf("%w: the carrier call at %s is already bound to %s", callback.ErrBindingConflict, b.Provider, existing)
	}
}

// Resolve implements [callback.Resolver], bounded by Config.OpTimeout.
func (s *Bindings) Resolve(provider telephony.ProviderID, providerCallID string) (telephony.CallID, error) {
	return s.ResolveContext(context.Background(), provider, providerCallID)
}

// ResolveContext looks a carrier call up. An unbound carrier call — including
// the same ID at another provider — is [callback.ErrUnknownCall]. A database
// failure is [ErrStoreUnavailable], and NOT ErrUnknownCall.
func (s *Bindings) ResolveContext(ctx context.Context, provider telephony.ProviderID, providerCallID string) (telephony.CallID, error) {
	probe := callback.Binding{Provider: provider, ProviderCallID: providerCallID, Call: "call_probe"}
	if err := probe.Validate(); err != nil {
		return "", fmt.Errorf("%w: %w", callback.ErrUnknownCall, err)
	}

	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	var id string
	err := s.q.QueryRow(ctx, sqlLookup, string(provider), providerCallID).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", fmt.Errorf("%w: no binding for this carrier call at %s", callback.ErrUnknownCall, provider)
	case err != nil:
		return "", unavailable("resolve", err)
	}
	return telephony.CallID(id), nil
}

// Remove deletes a binding, reporting whether one existed. Removal is always
// explicit: nothing in this package expires a binding (no retention period is
// established; see the P1-10 report).
func (s *Bindings) Remove(ctx context.Context, provider telephony.ProviderID, providerCallID string) (bool, error) {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	tag, err := s.q.Exec(ctx, sqlRemove, string(provider), providerCallID)
	if err != nil {
		return false, unavailable("remove", err)
	}
	return tag.RowsAffected() > 0, nil
}

func unavailable(op string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrStoreUnavailable, op, redact(err))
}

// pgCodeError keeps only the parts of a PostgreSQL error that cannot carry
// data: the SQLSTATE code and the constraint name. Message and Detail can echo
// row values and are dropped.
type pgCodeError struct{ code, constraint string }

func (e *pgCodeError) Error() string {
	if e.constraint != "" {
		return "postgres error " + e.code + " on constraint " + e.constraint
	}
	return "postgres error " + e.code
}

// redact returns an error safe to log. Context cancellation and deadline
// errors are preserved so callers can match them; everything else is reduced
// to its SQLSTATE or to a fixed message, because driver connection errors
// name the host, user and database.
func redact(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		return context.Canceled
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return &pgCodeError{code: pg.Code, constraint: pg.ConstraintName}
	}
	return errors.New("database operation failed (details withheld)")
}
