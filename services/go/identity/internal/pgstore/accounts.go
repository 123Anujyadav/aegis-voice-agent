package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// Accounts and authentication methods (migration 0011; app/accounts.go).

const (
	sqlstateUniqueViolation = "23505"
	constraintAccountsPK    = "identity_accounts_pkey"
	constraintMethodLookup  = "identity_auth_methods_lookup"
)

// uniqueViolation reports which unique constraint err violated, or "".
func uniqueViolation(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == sqlstateUniqueViolation {
		return pg.ConstraintName
	}
	return ""
}

// CreateAccountWithMethod implements app.AccountRepository, in one
// transaction. The account row is inserted only if the id appears in no
// session and no erasure record (a number-derived subscriber's id is never
// issued as an account id, and an erased id is never reused); the primary key
// refuses a stored or tombstoned account id, and the partial unique index
// refuses a method already attached.
func (r *Repository) CreateAccountWithMethod(ctx context.Context, a *app.Account, m *app.AuthMethod) error {
	if err := app.ValidNewAccount(a, m); err != nil {
		return err
	}
	return mapCreateError(inTx(ctx, r.pool, func(tx pgx.Tx) error {
		return insertAccountAndMethod(ctx, tx, a, m)
	}))
}

// CreateAccountWithMethodWithinBudget implements app.AccountRepository, in one
// transaction: the hour's budget row is counted with the 0010-style guard,
// then the account and its method are inserted exactly as in
// CreateAccountWithMethod. Any refusal rolls the whole transaction back, so
// the budget unit is spent only when the account is created. The budget row
// is locked until the transaction ends, which serialises the hour's
// creations: at an hourly cap of a few accounts, that is the point.
func (r *Repository) CreateAccountWithMethodWithinBudget(ctx context.Context, a *app.Account, m *app.AuthMethod, hourStart time.Time, limit int) error {
	if err := app.ValidNewAccount(a, m); err != nil {
		return err
	}
	if limit < 1 {
		return app.ErrAccountCreationCapReached
	}
	err := inTx(ctx, r.pool, func(tx pgx.Tx) error {
		var n int
		err := tx.QueryRow(ctx, `
			INSERT INTO identity_account_creation_budget (hour_start, created) VALUES ($1, 1)
			ON CONFLICT (hour_start) DO UPDATE SET created = identity_account_creation_budget.created + 1
			WHERE identity_account_creation_budget.created < $2
			RETURNING created`, us(hourStart.UTC()), limit).Scan(&n)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrAccountCreationCapReached
		}
		if err != nil {
			return err
		}
		return insertAccountAndMethod(ctx, tx, a, m)
	})
	return mapCreateError(err)
}

// insertAccountAndMethod is CreateAccountWithMethod's two inserts, inside the
// caller's transaction.
func insertAccountAndMethod(ctx context.Context, tx pgx.Tx, a *app.Account, m *app.AuthMethod) error {
	tag, err := tx.Exec(ctx, `
		INSERT INTO identity_accounts (account_id, status, created_at)
		SELECT $1, 'active', $2
		WHERE NOT EXISTS (SELECT 1 FROM identity_sessions WHERE subscriber_id = $1)
		  AND NOT EXISTS (SELECT 1 FROM identity_erasures WHERE subscriber_id = $1)`,
		a.ID, us(a.CreatedAt))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return app.ErrAccountIDInUse
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO identity_auth_methods
			(method_id, account_id, kind, key_id, lookup_pseudonym, created_at, last_used_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		m.MethodID, m.AccountID, m.Kind, m.KeyID, m.LookupPseudonym, us(m.CreatedAt), us(m.LastUsedAt))
	return err
}

// mapCreateError turns a creation's database error into the app's sentinels.
func mapCreateError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, app.ErrAccountIDInUse), errors.Is(err, app.ErrAccountCreationCapReached):
		return err
	}
	switch uniqueViolation(err) {
	case constraintAccountsPK:
		return app.ErrAccountIDInUse
	case constraintMethodLookup:
		return app.ErrAuthMethodInUse
	}
	return fmt.Errorf("pgstore: creating an account: %w", err)
}

// GetAccount implements app.AccountRepository.
func (r *Repository) GetAccount(ctx context.Context, accountID string) (*app.Account, error) {
	var a app.Account
	var erased *time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT account_id, status, created_at, erased_at FROM identity_accounts WHERE account_id = $1`,
		accountID).Scan(&a.ID, &a.Status, &a.CreatedAt, &erased)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pgstore: reading an account: %w", err)
	}
	if erased != nil {
		a.ErasedAt = *erased
	}
	return &a, nil
}

// FindAuthMethod implements app.AccountRepository: one query over the
// (key id, pseudonym) pairs.
func (r *Repository) FindAuthMethod(ctx context.Context, kind string, lookups []app.KeyedPseudonym) (*app.AuthMethod, error) {
	if err := app.ValidLookups(kind, lookups); err != nil {
		return nil, err
	}
	keyIDs := make([]string, len(lookups))
	pseudonyms := make([]string, len(lookups))
	for i, l := range lookups {
		keyIDs[i], pseudonyms[i] = l.KeyID, l.Pseudonym
	}
	rows, err := r.pool.Query(ctx, `
		SELECT m.method_id, m.account_id, m.kind, m.key_id, m.lookup_pseudonym, m.created_at, m.last_used_at
		FROM identity_auth_methods m
		JOIN unnest($2::text[], $3::text[]) AS l(key_id, pseudonym)
		  ON m.key_id = l.key_id AND m.lookup_pseudonym = l.pseudonym
		WHERE m.kind = $1 AND m.revoked_at IS NULL
		LIMIT 2`, kind, keyIDs, pseudonyms)
	if err != nil {
		return nil, fmt.Errorf("pgstore: finding an authentication method: %w", err)
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (app.AuthMethod, error) {
		var m app.AuthMethod
		scanErr := row.Scan(&m.MethodID, &m.AccountID, &m.Kind, &m.KeyID, &m.LookupPseudonym, &m.CreatedAt, &m.LastUsedAt)
		return m, scanErr
	})
	if err != nil {
		return nil, fmt.Errorf("pgstore: reading an authentication method: %w", err)
	}
	switch len(found) {
	case 0:
		return nil, app.ErrAuthMethodNotFound
	case 1:
		return &found[0], nil
	default:
		return nil, app.ErrAuthMethodAmbiguous
	}
}

// RekeyAuthMethod implements app.AccountRepository: a compare-and-swap on the
// method's key id.
func (r *Repository) RekeyAuthMethod(ctx context.Context, methodID, fromKeyID string, to app.KeyedPseudonym) (bool, error) {
	if !app.ValidKeyedPseudonym(to) {
		return false, app.ErrInvalidAccountRecord
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE identity_auth_methods SET key_id = $3, lookup_pseudonym = $4
		WHERE method_id = $1 AND key_id = $2 AND revoked_at IS NULL`,
		methodID, fromKeyID, to.KeyID, to.Pseudonym)
	if uniqueViolation(err) == constraintMethodLookup {
		return false, app.ErrAuthMethodInUse
	}
	if err != nil {
		return false, fmt.Errorf("pgstore: re-keying an authentication method: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// TouchAuthMethod implements app.AccountRepository. The time only moves
// forward.
func (r *Repository) TouchAuthMethod(ctx context.Context, methodID string, at time.Time) error {
	if _, err := r.pool.Exec(ctx, `
		UPDATE identity_auth_methods SET last_used_at = $2 WHERE method_id = $1 AND last_used_at < $2`,
		methodID, us(at)); err != nil {
		return fmt.Errorf("pgstore: recording an authentication method's use: %w", err)
	}
	return nil
}

// AuthMethodKeyIDs implements app.AccountRepository.
func (r *Repository) AuthMethodKeyIDs(ctx context.Context, kind string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT key_id FROM identity_auth_methods
		WHERE kind = $1 AND revoked_at IS NULL ORDER BY key_id`, kind)
	if err != nil {
		return nil, fmt.Errorf("pgstore: listing method key ids: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("pgstore: reading method key ids: %w", err)
	}
	return ids, nil
}

// ReserveAccountCreation implements app.AccountRepository: the same one-
// statement upsert with a guard as ReserveSignInDelivery (0010), so exactly
// limit calls in an hour succeed across replicas.
func (r *Repository) ReserveAccountCreation(ctx context.Context, hourStart time.Time, limit int) (bool, error) {
	if limit < 1 {
		return false, nil
	}
	var n int
	err := r.pool.QueryRow(ctx, `
		INSERT INTO identity_account_creation_budget (hour_start, created) VALUES ($1, 1)
		ON CONFLICT (hour_start) DO UPDATE SET created = identity_account_creation_budget.created + 1
		WHERE identity_account_creation_budget.created < $2
		RETURNING created`, us(hourStart.UTC()), limit).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("pgstore: counting an account creation: %w", err)
	}
	return n <= limit, nil
}

// ReserveGoogleToken implements app.AccountRepository: the primary key makes
// the first insert the only one that returns a row.
func (r *Repository) ReserveGoogleToken(ctx context.Context, tokenHash string, expiresAt time.Time) (bool, error) {
	if !app.ValidTokenHash(tokenHash) || expiresAt.IsZero() {
		return false, app.ErrInvalidAccountRecord
	}
	var h string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO identity_google_token_replay (token_hash, expires_at) VALUES ($1, $2)
		ON CONFLICT (token_hash) DO NOTHING RETURNING token_hash`, tokenHash, us(expiresAt)).Scan(&h)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("pgstore: recording a Google token: %w", err)
	}
	return true, nil
}

// eraseAccountRows deletes an account's methods and tombstones its id
// (INV-ID-7 option A), inside the erasure transaction. A number-derived
// subscriber has no account row: both statements match nothing.
func eraseAccountRows(ctx context.Context, tx pgx.Tx, e *app.SubscriberErasure) error {
	if _, err := tx.Exec(ctx, `DELETE FROM identity_auth_methods WHERE account_id = $1`, e.SubscriberID); err != nil {
		return fmt.Errorf("pgstore: erasing authentication methods: %w", err)
	}
	erasedAt := e.RequestedAt
	if erasedAt.IsZero() {
		erasedAt = time.Now()
	}
	if _, err := tx.Exec(ctx, `
		UPDATE identity_accounts SET status = 'erased', erased_at = $2
		WHERE account_id = $1 AND status = 'active'`, e.SubscriberID, us(erasedAt)); err != nil {
		return fmt.Errorf("pgstore: tombstoning the account: %w", err)
	}
	return nil
}

var _ app.AccountRepository = (*Repository)(nil)
