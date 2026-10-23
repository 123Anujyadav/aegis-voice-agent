package pgshared

import (
	"context"
	"crypto/x509"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// ADR-0029 "Database TLS", against a REAL TLS-serving PostgreSQL. The other
// TLS tests prove what is refused before connecting; these prove that the
// accepted configuration actually establishes verified TLS, and that a server
// the CA bundle does not vouch for, or a host name its certificate does not
// name, is refused at the handshake.
//
// They run only with AEGIS_PG_TLS_DSN: a verify-full DSN, with an absolute
// sslrootcert, for a server whose certificate is issued by that CA for the
// DSN's host (an IP address). AEGIS_PG_TLS_OTHER_CA names an unrelated CA
// bundle. tools/scripts/identity-postgres-tests.sh provides both. The tests
// only read, so the database need not be disposable.

func tlsServer(t *testing.T) (dsn, otherCA string) {
	t.Helper()
	dsn = strings.TrimSpace(os.Getenv("AEGIS_PG_TLS_DSN"))
	if dsn == "" {
		t.Skip("AEGIS_PG_TLS_DSN is not set: real TLS PostgreSQL test SKIPPED (not passed)")
	}
	otherCA = strings.TrimSpace(os.Getenv("AEGIS_PG_TLS_OTHER_CA"))
	if otherCA == "" {
		t.Fatal("AEGIS_PG_TLS_DSN is set but AEGIS_PG_TLS_OTHER_CA is not")
	}
	return dsn, otherCA
}

// withParam returns dsn (URL form) with one query parameter replaced.
func withParam(t *testing.T, dsn, key, value string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("parsing AEGIS_PG_TLS_DSN (value not shown)")
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String()
}

// withHost returns dsn (URL form) with its host name replaced, port kept.
func withHost(t *testing.T, dsn, host string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("parsing AEGIS_PG_TLS_DSN (value not shown)")
	}
	u.Host = host + ":" + u.Port()
	return u.String()
}

func tlsCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestTLS_Postgres_VerifyFullEstablishesVerifiedTLS(t *testing.T) {
	dsn, _ := tlsServer(t)
	ctx := tlsCtx(t)
	pool, err := OpenPool(ctx, dsn, RequireVerifiedTLS)
	if err != nil {
		t.Fatalf("the accepted verify-full configuration did not connect: %v", err)
	}
	defer pool.Close()
	// The server's own view of this connection: encrypted, with TLS 1.2+.
	var ssl bool
	var version string
	if err := pool.QueryRow(ctx, `SELECT ssl, version FROM pg_stat_ssl WHERE pid = pg_backend_pid()`).Scan(&ssl, &version); err != nil {
		t.Fatal(err)
	}
	if !ssl || (version != "TLSv1.2" && version != "TLSv1.3") {
		t.Fatalf("pg_stat_ssl: ssl=%v version=%q; want an encrypted TLS 1.2+ connection", ssl, version)
	}
}

func TestTLS_Postgres_UntrustedServerIsRefusedAtTheHandshake(t *testing.T) {
	dsn, otherCA := tlsServer(t)
	ctx := tlsCtx(t)
	wrong := withParam(t, dsn, "sslrootcert", otherCA)

	// The cause, through the unredacted path: the certificate chain does not
	// lead to the configured CA.
	pool, err := newPool(ctx, wrong, RequireVerifiedTLS)
	if err != nil {
		t.Fatalf("the policy refused a well-formed verify-full DSN: %v", err)
	}
	defer pool.Close()
	pingErr := pool.Ping(ctx)
	var unknown x509.UnknownAuthorityError
	if !errors.As(pingErr, &unknown) {
		t.Fatalf("ping with an unrelated CA = %v; want x509.UnknownAuthorityError", pingErr)
	}

	// The public path: refused, and redacted.
	if p, openErr := OpenPool(ctx, wrong, RequireVerifiedTLS); p != nil || !errors.Is(openErr, ErrConnect) {
		t.Fatalf("OpenPool with an unrelated CA = (%v, %v); want ErrConnect", p, openErr)
	}
}

func TestTLS_Postgres_HostNameMismatchIsRefusedAtTheHandshake(t *testing.T) {
	dsn, _ := tlsServer(t)
	ctx := tlsCtx(t)
	// The certificate names the IP address only. "localhost" reaches the same
	// server but is not a name the certificate carries.
	mismatch := withHost(t, dsn, "localhost")
	pool, err := newPool(ctx, mismatch, RequireVerifiedTLS)
	if err != nil {
		t.Fatalf("the policy refused a well-formed verify-full DSN: %v", err)
	}
	defer pool.Close()
	pingErr := pool.Ping(ctx)
	var host x509.HostnameError
	if !errors.As(pingErr, &host) {
		t.Fatalf("ping by a name the certificate does not carry = %v; want x509.HostnameError", pingErr)
	}
}
