package pgstore

import (
	"errors"
	"testing"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/jackc/pgx/v5/pgconn"
)

// ADR-0031 D31.4 against real PostgreSQL: the session's integrity tier, gated
// like the rest of this package's real-database tests.

// The tier round-trips through save, read and compare-and-swap; the zero
// value is stored as unverified, exactly as the memory repository stores it.
func TestPostgres_IntegrityTierRoundTrips(t *testing.T) {
	env := requirePostgres(t)
	repo := NewRepository(env.poolA)
	s := session("tier")
	if err := repo.SaveSession(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetSession(ctx, "tier")
	if err != nil || got.IntegrityTier != app.SessionUnverified {
		t.Fatalf("a session saved with no tier reads back (%q, %v), want unverified", got.IntegrityTier, err)
	}
	next := *got
	next.IntegrityTier = app.SessionCertified
	next.RefreshTokenHash = app.HashRefreshToken([]byte("k"), "rotated")
	if err := repo.ReplaceSession(ctx, &next, got.RefreshTokenHash); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.GetSession(ctx, "tier"); got.IntegrityTier != app.SessionCertified {
		t.Fatalf("after the swap: %q, want certified", got.IntegrityTier)
	}
	bad := next
	bad.IntegrityTier = "trusted"
	if err := repo.SaveSession(ctx, &bad); !errors.Is(err, app.ErrInvalidIntegrityTier) {
		t.Fatalf("an unknown tier was written: %v", err)
	}
}

// The CHECK constraint refuses any other tier, whatever the writer.
func TestPostgres_IntegrityTierColumnRejectsUnknownValues(t *testing.T) {
	env := requirePostgres(t)
	_ = NewRepository(env.poolA).SaveSession(ctx, session("tierchk"))
	for name, sql := range map[string]string{
		"update to unknown": `UPDATE identity_sessions SET integrity_tier = 'trusted' WHERE session_id = 'tierchk'`,
		"update to empty":   `UPDATE identity_sessions SET integrity_tier = '' WHERE session_id = 'tierchk'`,
	} {
		_, err := env.poolB.Exec(ctx, sql)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "23514" {
			t.Errorf("%s: err = %v, want a CHECK violation (23514)", name, err)
		}
	}
}
