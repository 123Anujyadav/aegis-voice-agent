package pgshared

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ADR-0029 "Database TLS": verify-full with an explicit CA bundle everywhere
// except an EXPLICIT development environment.

// caBundle writes a self-signed CA certificate and returns its absolute path
// with forward slashes (valid in both DSN syntaxes on every platform).
func caBundle(t *testing.T) string {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(p)
}

func TestPolicyForEnvironment_OnlyExplicitDevelopmentRelaxes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		value string
		set   bool
		want  TLSPolicy
	}{
		{"development", true, AllowPlaintextForDevelopment},
		{"development", false, RequireVerifiedTLS}, // the platform's implicit default must not relax TLS
		{"staging", true, RequireVerifiedTLS},
		{"production", true, RequireVerifiedTLS},
		{"Development", true, RequireVerifiedTLS},
		{"", true, RequireVerifiedTLS},
	}
	for _, c := range cases {
		if got := PolicyForEnvironment(c.value, c.set); got != c.want {
			t.Errorf("PolicyForEnvironment(%q, %v) = %v, want %v", c.value, c.set, got, c.want)
		}
	}
}

func TestNewPool_RequireVerifiedTLS(t *testing.T) {
	t.Parallel()
	ca := caBundle(t)
	q := url.QueryEscape(ca)
	good := []string{
		"postgres://edge@db.example.test:5432/identity?sslmode=verify-full&sslrootcert=" + q,
		"host=db.example.test port=5432 user=edge dbname=identity sslmode=verify-full sslrootcert='" + ca + "'",
	}
	for _, dsn := range good {
		pool, err := NewPool(dsn, RequireVerifiedTLS)
		if err != nil {
			t.Errorf("a verified-TLS DSN was refused: %v", err)
			continue
		}
		pool.Close()
	}
	bad := map[string]string{
		"no sslmode":            "postgres://edge@db.example.test:5432/identity",
		"sslmode=disable":       "postgres://edge@db.example.test:5432/identity?sslmode=disable",
		"sslmode=require":       "postgres://edge@db.example.test:5432/identity?sslmode=require&sslrootcert=" + q,
		"sslmode=verify-ca":     "postgres://edge@db.example.test:5432/identity?sslmode=verify-ca&sslrootcert=" + q,
		"verify-full, no CA":    "postgres://edge@db.example.test:5432/identity?sslmode=verify-full",
		"sslrootcert=system":    "postgres://edge@db.example.test:5432/identity?sslmode=verify-full&sslrootcert=system",
		"multi-host fallback":   "postgres://edge@a.example.test:5432,b.example.test:5432/identity?sslmode=verify-full&sslrootcert=" + q,
		"keyword, prefer":       "host=db.example.test user=edge dbname=identity sslmode=prefer",
		"keyword, relative CA":  "host=db.example.test user=edge dbname=identity sslmode=verify-full sslrootcert=ca.pem",
		"keyword, missing file": "host=db.example.test user=edge dbname=identity sslmode=verify-full sslrootcert='" + filepath.ToSlash(filepath.Join(t.TempDir(), "absent.pem")) + "'",
	}
	for name, dsn := range bad {
		pool, err := NewPool(dsn, RequireVerifiedTLS)
		if pool != nil {
			pool.Close()
		}
		if !errors.Is(err, ErrInsecureDSN) && !errors.Is(err, ErrInvalidDSN) {
			t.Errorf("%s: err = %v, want ErrInsecureDSN or ErrInvalidDSN", name, err)
		}
		if err != nil && (strings.Contains(err.Error(), "db.example.test") || strings.Contains(err.Error(), "edge@")) {
			t.Errorf("%s: the error echoes the connection string: %v", name, err)
		}
	}
}

// Explicit development may use a local database without TLS.
func TestNewPool_DevelopmentAllowsPlaintext(t *testing.T) {
	t.Parallel()
	pool, err := NewPool("postgres://edge@127.0.0.1:5432/identity?sslmode=disable", AllowPlaintextForDevelopment)
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
}

func TestKeywordParams(t *testing.T) {
	t.Parallel()
	got, err := keywordParams(`host=db port=5432  sslrootcert='/a b/c\'d.pem'	sslmode=verify-full`)
	if err != nil {
		t.Fatal(err)
	}
	if got["host"] != "db" || got["port"] != "5432" || got["sslrootcert"] != "/a b/c'd.pem" || got["sslmode"] != "verify-full" {
		t.Errorf("parsed %v", got)
	}
	for _, in := range []string{"host", "=x", "a='unterminated", "sslmode=disable sslmode=verify-full"} {
		if _, err := keywordParams(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}
