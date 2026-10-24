package pgstore

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// identity_refresh_tokens_superseded against real PostgreSQL, gated like the
// rest of this package's real-database tests.

// Superseded tokens are deleted with their session, and the column refuses
// anything that is not a canonical hash, whatever the writer.
func TestPostgres_SupersededTokensLiveExactlyAsLongAsTheSession(t *testing.T) {
	env := requirePostgres(t)
	repo := NewRepository(env.poolA)
	if err := repo.SaveSession(ctx, session("supdel")); err != nil {
		t.Fatal(err)
	}
	n := session("supdel")
	n.RefreshTokenHash = th("rt_1")
	if err := repo.ReplaceSession(ctx, n, th("rt_0")); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.SupersededRefreshToken(ctx, "supdel", th("rt_0")); err != nil || !ok {
		t.Fatalf("(%v, %v), want superseded", ok, err)
	}
	if _, err := env.poolB.Exec(ctx, `DELETE FROM identity_sessions WHERE session_id = 'supdel'`); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := env.poolB.QueryRow(ctx, `SELECT count(*) FROM identity_refresh_tokens_superseded WHERE session_id = 'supdel'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("(%d rows, %v) outlived their session, want 0", left, err)
	}

	_ = repo.SaveSession(ctx, session("supchk"))
	for name, sql := range map[string]string{
		"a raw token": `INSERT INTO identity_refresh_tokens_superseded VALUES ('supchk', 'a-raw-refresh-token', now())`,
		"upper case":  `INSERT INTO identity_refresh_tokens_superseded VALUES ('supchk', upper('` + th("x") + `'), now())`,
	} {
		_, err := env.poolB.Exec(ctx, sql)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "23514" {
			t.Errorf("%s: err = %v, want a CHECK violation (23514)", name, err)
		}
	}
	_, err := env.poolB.Exec(ctx, `INSERT INTO identity_refresh_tokens_superseded VALUES ('no_such_session', '`+th("x")+`', now())`)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23503" {
		t.Errorf("an orphan row: err = %v, want a foreign-key violation (23503)", err)
	}
}
