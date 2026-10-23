package pgstore

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

var (
	auditReq  = "req_" + strings.Repeat("a", 64)
	auditSess = "sess_" + strings.Repeat("b", 64)
)

// Every event is appended with its type, time and random identifier only.
func TestSecurityAuditLog_Postgres_AppendsEvents(t *testing.T) {
	env := requirePostgres(t)
	log := NewSecurityAuditLog(env.poolA)
	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for _, e := range []app.SecurityEvent{
		{Type: app.EventOtpVerificationFailed, At: at, RequestID: auditReq},
		{Type: app.EventOtpLockout, At: at, RequestID: auditReq, LockedUntil: at.Add(15 * time.Minute)},
		{Type: app.EventRefreshTokenReused, At: at, SessionID: auditSess},
		{Type: app.EventDeviceEnrolled, At: at, SessionID: auditSess},
	} {
		if err := log.Record(e); err != nil {
			t.Fatalf("%s: %v", e.Type, err)
		}
	}
	var n int
	var lockedUntil time.Time
	if err := env.poolB.QueryRow(ctx, `SELECT count(*) FROM identity_security_audit`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("rows = (%d, %v), want 4", n, err)
	}
	if err := env.poolB.QueryRow(ctx, `SELECT locked_until FROM identity_security_audit WHERE event_type = $1`,
		string(app.EventOtpLockout)).Scan(&lockedUntil); err != nil || !lockedUntil.Equal(at.Add(15*time.Minute)) {
		t.Fatalf("lockout row: (%v, %v)", lockedUntil, err)
	}
}

// The log is append-only: no UPDATE, DELETE or TRUNCATE succeeds, for any
// caller, including housekeeping.
func TestSecurityAuditLog_Postgres_IsAppendOnly(t *testing.T) {
	env := requirePostgres(t)
	if err := NewSecurityAuditLog(env.poolA).Record(app.SecurityEvent{Type: app.EventDeviceEnrolled, At: time.Now(), SessionID: auditSess}); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE identity_security_audit SET event_type = 'identity.tampered'`,
		`DELETE FROM identity_security_audit`,
		`TRUNCATE identity_security_audit`,
	} {
		_, err := env.poolA.Exec(ctx, stmt)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "CSA01" {
			t.Errorf("%s: err = %v, want SQLSTATE CSA01", stmt, err)
		}
	}
	if _, err := housekeeper(t, env.poolA, time.Now().Add(365*24*time.Hour), 100).RunOnce(ctx); err != nil {
		t.Fatalf("housekeeping with an audit row present: %v", err)
	}
	var n int
	if err := env.poolA.QueryRow(ctx, `SELECT count(*) FROM identity_security_audit`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("after the attempts and housekeeping: (%d, %v), want the 1 row intact", n, err)
	}
}

// The constraints refuse anything but the closed shapes: a phone number, a
// code or free text cannot be stored, whatever a caller sends.
func TestSecurityAuditLog_Postgres_RefusesPersonalData(t *testing.T) {
	env := requirePostgres(t)
	log := NewSecurityAuditLog(env.poolA)
	for name, e := range map[string]app.SecurityEvent{
		"number as request id": {Type: app.EventOtpVerificationFailed, At: time.Now(), RequestID: "+919876543210"},
		"short request id":     {Type: app.EventOtpVerificationFailed, At: time.Now(), RequestID: "req_a"},
		"token as session id":  {Type: app.EventRefreshTokenReused, At: time.Now(), SessionID: strings.Repeat("f", 64)},
		"free-text type":       {Type: "identity.user +919876543210 locked", At: time.Now()},
		"foreign type":         {Type: "billing.charge", At: time.Now()},
	} {
		if err := log.Record(e); err == nil {
			t.Errorf("%s: stored", name)
		} else if strings.Contains(err.Error(), "9876543210") {
			t.Errorf("%s: the error echoes the value: %v", name, err)
		}
	}
	var n int
	if err := env.poolA.QueryRow(ctx, `SELECT count(*) FROM identity_security_audit`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows = (%d, %v), want none", n, err)
	}
}

// Offline: 0005 is append-only by construction and keeps its columns closed.
func TestMigration0005_IsAppendOnlyAndClosed(t *testing.T) {
	chain, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	sql := chain[4].SQL
	for _, want := range []string{
		"CREATE TABLE identity_security_audit",
		"BEFORE UPDATE OR DELETE ON identity_security_audit",
		"BEFORE TRUNCATE ON identity_security_audit",
		"ERRCODE = 'CSA01'",
		`'^req_[0-9a-f]{64}$'`,
		`'^sess_[0-9a-f]{64}$'`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("0005 lacks %q", want)
		}
	}
	for _, forbidden := range []string{"msisdn", "subscriber_id", "code_hash", "token"} {
		if strings.Contains(strings.ToLower(strings.SplitN(sql, "CREATE TABLE", 2)[1]), forbidden+" ") {
			t.Errorf("0005 has a %q column", forbidden)
		}
	}
}

// latestVersion is the newest migration in the embedded chain: tests that
// migrate from an older version assert they reach it, rather than a number
// that the next migration would make stale.
func latestVersion(t *testing.T) int {
	t.Helper()
	chain, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	return chain[len(chain)-1].Version
}
