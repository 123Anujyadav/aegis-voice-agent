package pgstore

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Refresh tokens at rest in PostgreSQL. Real-database tests, gated like the
// rest of this package's.

// The CHECK constraint refuses, whatever the writer, a value that is not a
// canonical hash.
func TestPostgres_HashColumnRejectsNonCanonicalValues(t *testing.T) {
	env := requirePostgres(t)
	_ = NewRepository(env.poolA).SaveSession(ctx, session("chk"))
	for name, sql := range map[string]string{
		"insert raw": `INSERT INTO identity_sessions (session_id, subscriber_id, refresh_token_hash, created_at, expires_at)
			VALUES ('raw', 'u', 'a-raw-refresh-token', now(), now())`,
		"update to upper case": `UPDATE identity_sessions SET refresh_token_hash = upper(refresh_token_hash) WHERE session_id = 'chk'`,
		"update to short":      `UPDATE identity_sessions SET refresh_token_hash = 'abc' WHERE session_id = 'chk'`,
	} {
		_, err := env.poolB.Exec(ctx, sql)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "23514" {
			t.Errorf("%s: err = %v, want a CHECK violation (23514)", name, err)
		}
	}
}

// v1 schema: apply 0001 by hand and record it, as an older build left it.
func schemaAtV1(t *testing.T) *pgxpool.Pool {
	t.Helper()
	env := requirePostgres(t)
	if _, err := env.poolA.Exec(ctx, `DROP SCHEMA `+env.schema+` CASCADE; CREATE SCHEMA `+env.schema); err != nil {
		t.Fatal(err)
	}
	p := env.openSchema(t)
	chain, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{ledgerDDL, chain[0].SQL} {
		if _, err := p.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Exec(ctx, `INSERT INTO identity_schema_migrations (version, name) VALUES (1, $1)`, chain[0].Name); err != nil {
		t.Fatal(err)
	}
	return p
}

func columnExists(t *testing.T, p *pgxpool.Pool, column string) bool {
	t.Helper()
	var n int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'identity_sessions' AND column_name = $1`, column).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// 0002 against a v1 database holding a raw token: refused with CSI03, and
// NOTHING changes — version, column, row. With the rows removed (the
// documented operator step) it applies, and re-running is a no-op.
func TestPostgres_Migration0002RefusesExistingRowsAtomically(t *testing.T) {
	p := schemaAtV1(t)
	const raw = "4f1c8a0e9b2d7c6f5a4e3d2c1b0a99887766554433221100ffeeddccbbaa9988"
	if _, err := p.Exec(ctx, `INSERT INTO identity_sessions (session_id, subscriber_id, msisdn, refresh_token, created_at, expires_at)
		VALUES ('legacy', 'u', '+14155552671', $1, now(), now() + interval '1 day')`, raw); err != nil {
		t.Fatal(err)
	}

	res, err := Migrate(ctx, p)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "CSI03" {
		t.Fatalf("migrating with a raw-token row: (%+v, %v), want SQLSTATE CSI03", res, err)
	}
	if len(res.Applied) != 0 {
		t.Errorf("reported applied: %v", res.Applied)
	}
	var version int
	_ = p.QueryRow(ctx, `SELECT max(version) FROM identity_schema_migrations`).Scan(&version)
	if version != 1 {
		t.Errorf("ledger at v%d after the refusal, want 1", version)
	}
	if !columnExists(t, p, "refresh_token") || columnExists(t, p, "refresh_token_hash") {
		t.Error("the refused migration changed the column")
	}
	var still string
	if queryRowErr := p.QueryRow(ctx, `SELECT refresh_token FROM identity_sessions WHERE session_id = 'legacy'`).Scan(&still); queryRowErr != nil || still != raw {
		t.Errorf("the row changed: (%q, %v)", still, queryRowErr)
	}

	// The operator's documented step, then the migration again.
	if _, execErr := p.Exec(ctx, `DELETE FROM identity_sessions`); execErr != nil {
		t.Fatal(execErr)
	}
	res, err = Migrate(ctx, p)
	if latest := latestVersion(t); err != nil || len(res.Applied) != latest-1 || res.Applied[0] != "0002_refresh_token_hash.sql" || res.Version != latest {
		t.Fatalf("after clearing: (%+v, %v)", res, err)
	}
	if columnExists(t, p, "refresh_token") || !columnExists(t, p, "refresh_token_hash") {
		t.Error("0002 did not rename the column")
	}
	res, err = Migrate(ctx, p)
	if err != nil || len(res.Applied) != 0 || res.Version != latestVersion(t) {
		t.Errorf("re-run: (%+v, %v), want nothing applied", res, err)
	}
	// And the upgraded table works through the repository.
	if err := NewRepository(p).SaveSession(ctx, session("after")); err != nil {
		t.Errorf("saving after the upgrade: %v", err)
	}
}

// Two service instances on one database: every refresh token they issue is
// stored only as its keyed hash, and no raw token appears anywhere in the
// table's rows.
func TestPostgres_RawRefreshTokensNeverReachTheDatabase(t *testing.T) {
	env := requirePostgres(t)
	clock := time.Now().Truncate(time.Second)
	now := func() time.Time { return clock }
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	a, b := replica(t, env.poolA, key, now), replica(t, env.poolB, key, now)
	device, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	req, err := a.RequestOtp(ctx, replicaMSISDN)
	if err != nil {
		t.Fatal(err)
	}
	sess, _, err := a.ValidateOtpWithDeviceProof(ctx, req, replicaCode, "POST", replicaProof(t, device, "e1", replicaEnrolURL, clock))
	if err != nil {
		t.Fatal(err)
	}
	_, r1, err := b.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", replicaProof(t, device, "r1", replicaRefreshURL, clock))
	if err != nil {
		t.Fatal(err)
	}
	_, r2, err := a.RefreshTokenWithDeviceProof(ctx, sess.SessionID, r1, "POST", replicaProof(t, device, "r2", replicaRefreshURL, clock))
	if err != nil {
		t.Fatal(err)
	}

	var stored string
	if queryRowErr := env.poolB.QueryRow(ctx, `SELECT refresh_token_hash FROM identity_sessions WHERE session_id = $1`, sess.SessionID).Scan(&stored); queryRowErr != nil {
		t.Fatal(queryRowErr)
	}
	if stored != app.HashRefreshToken(replicaSecret, r2) {
		t.Errorf("stored %q, want the keyed hash of the current token", stored)
	}
	rows, err := env.poolB.Query(ctx, `SELECT row_to_json(s)::text FROM identity_sessions s`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var dump strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		dump.WriteString(line)
	}
	for _, raw := range []string{sess.RefreshToken, r1, r2} {
		if strings.Contains(dump.String(), raw) {
			t.Error("a raw refresh token is stored in identity_sessions")
		}
	}
	// The superseded tokens are refused across instances as reuse.
	if _, _, err := b.RefreshTokenWithDeviceProof(ctx, sess.SessionID, r1, "POST", replicaProof(t, device, "r3", replicaRefreshURL, clock)); !errors.Is(err, app.ErrTokenReused) {
		t.Errorf("a superseded token on the other instance: err = %v, want ErrTokenReused", err)
	}
}
