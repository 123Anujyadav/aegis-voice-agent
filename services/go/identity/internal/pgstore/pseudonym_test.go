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

// Phone numbers at rest in PostgreSQL. Real-database tests, gated like the
// rest of this package's.

// dumpAll serialises every row of every identity_ table.
func dumpAll(t *testing.T, p *pgxpool.Pool) string {
	t.Helper()
	tables := ids(t, p, `SELECT table_name::text FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name LIKE 'identity\_%'`)
	var b strings.Builder
	for _, table := range tables {
		for _, row := range ids(t, p, `SELECT row_to_json(x)::text FROM `+table+` x`) {
			b.WriteString(table + " " + row + "\n")
		}
	}
	return b.String()
}

// Two service instances: requests, a wrong code, enrolment, refresh and a
// pending challenge. No row of any table holds the number in any form; the
// stored keys are its pseudonym, the same on both instances.
func TestPostgres_NoPhoneNumberInAnyTable(t *testing.T) {
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
	if _, _, validateOtpErr := b.ValidateOtp(ctx, req, "000000"); !errors.Is(validateOtpErr, app.ErrInvalidOtp) {
		t.Fatalf("wrong code on B: %v", validateOtpErr)
	}
	sess, _, err := a.ValidateOtpWithDeviceProof(ctx, req, replicaCode, "POST", replicaProof(t, device, "e1", replicaEnrolURL, clock))
	if err != nil {
		t.Fatalf("enrol on A: %v", err)
	}
	if _, _, err := b.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", replicaProof(t, device, "r1", replicaRefreshURL, clock)); err != nil {
		t.Fatalf("refresh on B: %v", err)
	}
	if _, err := b.RequestOtp(ctx, replicaMSISDN); err != nil { // left pending
		t.Fatal(err)
	}

	dump := dumpAll(t, env.poolA)
	for _, raw := range []string{replicaMSISDN, strings.TrimPrefix(replicaMSISDN, "+"), "4155552671"} {
		if strings.Contains(dump, raw) {
			t.Errorf("a table holds %q", raw)
		}
	}
	want, _ := app.MSISDNPseudonym(replicaSecret, replicaMSISDN)
	for _, sql := range []string{
		`SELECT msisdn_pseudonym FROM identity_otp_rate_state`,
		`SELECT DISTINCT msisdn_pseudonym FROM identity_otp_requests`,
		`SELECT DISTINCT msisdn_pseudonym FROM identity_otp_challenges`,
	} {
		if got := ids(t, env.poolB, sql); !eq(got, []string{want}) {
			t.Errorf("%s = %v, want only the number's pseudonym", sql, got)
		}
	}
	if got := ids(t, env.poolB, `SELECT DISTINCT subscriber_id FROM identity_otp_challenges`); !eq(got, []string{sess.SubscriberID}) {
		t.Errorf("challenge subscriber ids %v, want the session's %q", got, sess.SubscriberID)
	}
}

// The database refuses a raw number in every pseudonym column, and sessions
// have no number column at all.
func TestPostgres_PseudonymColumnsRejectRawNumbers(t *testing.T) {
	env := requirePostgres(t)
	p := env.poolA
	good := pk("+14155550301")
	mustExec(t, p, `INSERT INTO identity_otp_rate_state (msisdn_pseudonym) VALUES ($1)`, good)
	for name, stmt := range map[string]string{
		"challenge":  `INSERT INTO identity_otp_challenges (request_id, msisdn_pseudonym, subscriber_id, code_hash, expires_at) VALUES ('r', '+14155550301', 'u', 'h', now())`,
		"rate state": `INSERT INTO identity_otp_rate_state (msisdn_pseudonym) VALUES ('+14155550302')`,
		"request":    `INSERT INTO identity_otp_requests (msisdn_pseudonym, requested_at) VALUES ('+14155550301', now())`,
		"failure":    `INSERT INTO identity_otp_failures (reservation_id, msisdn_pseudonym, failed_at) VALUES (99, '+14155550301', now())`,
		"upper hex":  `INSERT INTO identity_otp_rate_state (msisdn_pseudonym) VALUES ('` + strings.ToUpper(good[:64]) + `')`,
	} {
		_, err := env.poolB.Exec(ctx, stmt)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || (pg.Code != "23514" && pg.Code != "23503") {
			t.Errorf("%s: err = %v, want a CHECK violation", name, err)
		} else if pg.Code == "23503" {
			t.Errorf("%s: refused by the foreign key, not the CHECK: %v", name, err)
		}
	}
	_, err := env.poolB.Exec(ctx, `SELECT msisdn FROM identity_sessions`)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "42703" {
		t.Errorf("identity_sessions still has an msisdn column: %v", err)
	}
}

// One number's allowance and lockout are shared by every instance, because
// every instance derives the same pseudonym.
func TestPostgres_RateLimitsAndLockoutsSpanInstances(t *testing.T) {
	env := requirePostgres(t)
	clock := time.Now().Truncate(time.Second)
	now := func() time.Time { return clock }
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	a, b := replica(t, env.poolA, key, now), replica(t, env.poolB, key, now)

	const n = "+14155550311"
	for i, svc := range []*app.IdentityService{a, b, a} {
		if _, err := svc.RequestOtp(ctx, n); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if _, err := b.RequestOtp(ctx, n); !errors.Is(err, app.ErrRateLimited) {
		t.Errorf("fourth request on the other instance: %v, want ErrRateLimited", err)
	}

	const l = "+14155550312"
	r1, err := a.RequestOtp(ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := b.RequestOtp(ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	reqs := []string{r1, r1, r1, r2, r2}
	for i, req := range reqs {
		svc := []*app.IdentityService{a, b}[i%2]
		if _, _, err := svc.ValidateOtp(ctx, req, "000000"); !errors.Is(err, app.ErrInvalidOtp) {
			t.Fatalf("wrong code %d: %v", i, err)
		}
	}
	for _, svc := range []*app.IdentityService{a, b} {
		if _, _, err := svc.ValidateOtp(ctx, r2, replicaCode); !errors.Is(err, app.ErrRateLimited) {
			t.Errorf("the right code during the lockout: %v, want ErrRateLimited on both instances", err)
		}
	}
}

// schemaAt builds a schema at exactly version n of the chain, as a build that
// knew only those migrations left it.
func schemaAt(t *testing.T, n int) *pgxpool.Pool {
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
	mustExec(t, p, ledgerDDL)
	for _, m := range chain[:n] {
		mustExec(t, p, m.SQL)
		mustExec(t, p, `INSERT INTO identity_schema_migrations (version, name) VALUES ($1, $2)`, m.Version, m.Name)
	}
	return p
}

func ledgerVersion(t *testing.T, p *pgxpool.Pool) int {
	t.Helper()
	var v int
	if err := p.QueryRow(ctx, `SELECT max(version) FROM identity_schema_migrations`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func hasColumn(t *testing.T, p *pgxpool.Pool, table, column string) bool {
	t.Helper()
	var n int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`, table, column).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// 0004 against a v3 database: a raw number in any OTP or rate-limit table is
// refused with CSI04 and nothing changes; sessions alone are migrated, keep
// working and lose their number column.
func TestPostgres_Migration0004(t *testing.T) {
	for name, seed := range map[string]string{
		"pending challenge": `INSERT INTO identity_otp_challenges (request_id, msisdn, code_hash, expires_at)
			VALUES ('r', '+14155550321', 'h', now() + interval '5 minutes')`,
		"rate state": `INSERT INTO identity_otp_rate_state (msisdn) VALUES ('+14155550321')`,
		"request and its state": `INSERT INTO identity_otp_rate_state (msisdn) VALUES ('+14155550321');
			INSERT INTO identity_otp_requests (msisdn, requested_at) VALUES ('+14155550321', now())`,
		"failure and its state": `INSERT INTO identity_otp_rate_state (msisdn) VALUES ('+14155550321');
			INSERT INTO identity_otp_failures (reservation_id, msisdn, failed_at) VALUES (1, '+14155550321', now())`,
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			p := schemaAt(t, 3)
			mustExec(t, p, seed)
			before := dumpAll(t, p)
			res, err := Migrate(ctx, p)
			var pg *pgconn.PgError
			if !errors.As(err, &pg) || pg.Code != "CSI04" || len(res.Applied) != 0 {
				t.Fatalf("(%+v, %v), want SQLSTATE CSI04 and nothing applied", res, err)
			}
			if ledgerVersion(t, p) != 3 {
				t.Error("the ledger moved")
			}
			for _, table := range []string{"identity_otp_challenges", "identity_otp_rate_state", "identity_otp_requests", "identity_otp_failures", "identity_sessions"} {
				if !hasColumn(t, p, table, "msisdn") || hasColumn(t, p, table, "msisdn_pseudonym") {
					t.Errorf("%s was altered by a refused migration", table)
				}
			}
			if after := dumpAll(t, p); after != before {
				t.Error("rows changed in a refused migration")
			}
		})
	}

	t.Run("sessions only: migrated, kept, number dropped", func(t *testing.T) {
		p := schemaAt(t, 3)
		mustExec(t, p, `INSERT INTO identity_sessions (session_id, subscriber_id, msisdn, refresh_token_hash, created_at, expires_at)
			VALUES ('s1', 'usr_1', '+14155550321', $1, now(), now() + interval '1 day')`, th("rt_s1"))
		res, err := Migrate(ctx, p)
		if err != nil || res.Version != 4 || len(res.Applied) != 1 || res.Applied[0] != "0004_msisdn_pseudonyms.sql" {
			t.Fatalf("(%+v, %v)", res, err)
		}
		if hasColumn(t, p, "identity_sessions", "msisdn") {
			t.Error("the session number column survived")
		}
		if strings.Contains(dumpAll(t, p), "4155550321") {
			t.Error("the number is still stored")
		}
		got, err := NewRepository(p).GetSession(ctx, "s1")
		if err != nil || got.SubscriberID != "usr_1" || got.RefreshTokenHash != th("rt_s1") {
			t.Errorf("the session did not survive intact: (%+v, %v)", got, err)
		}
		if res, err := Migrate(ctx, p); err != nil || len(res.Applied) != 0 || res.Version != 4 {
			t.Errorf("re-run: (%+v, %v)", res, err)
		}
	})

	t.Run("empty: migrated", func(t *testing.T) {
		p := schemaAt(t, 3)
		if res, err := Migrate(ctx, p); err != nil || res.Version != 4 {
			t.Fatalf("(%+v, %v)", res, err)
		}
		if !hasColumn(t, p, "identity_otp_challenges", "subscriber_id") || !hasColumn(t, p, "identity_otp_rate_state", "msisdn_pseudonym") {
			t.Error("the new columns are missing")
		}
	})
}
