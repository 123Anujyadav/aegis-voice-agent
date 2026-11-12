package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ReserveSignInDelivery implements app.IdentityRepository: the global hourly
// sign-in delivery cap (F-1, app/signin_controls.go), one row per hour in
// identity_signin_delivery_budget (migration 0010).
//
// ONE statement, the deletion-code budget's upsert with a guard: a new hour is
// inserted at 1, and an existing hour is incremented only WHERE it is still
// below the limit. INSERT ... ON CONFLICT DO UPDATE locks the hour's row, and a
// concurrent call (on any replica) waits for it and re-evaluates the guard
// against the committed count, so exactly limit calls in an hour return a row.
// A refused call matches no row (pgx.ErrNoRows) and changes nothing.
func (r *Repository) ReserveSignInDelivery(ctx context.Context, hourStart time.Time, limit int) (bool, error) {
	if limit < 1 {
		return false, nil
	}
	var n int
	err := r.pool.QueryRow(ctx, `
		INSERT INTO identity_signin_delivery_budget (hour_start, deliveries) VALUES ($1, 1)
		ON CONFLICT (hour_start) DO UPDATE SET deliveries = identity_signin_delivery_budget.deliveries + 1
		WHERE identity_signin_delivery_budget.deliveries < $2
		RETURNING deliveries`, us(hourStart.UTC()), limit).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("pgstore: counting a sign-in delivery: %w", err)
	}
	return n <= limit, nil
}
