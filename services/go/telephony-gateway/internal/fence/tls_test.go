package fence

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// P1-A1 — R1 on the wire, offline: a loopback fake PostgreSQL server accepts
// the SSLRequest and completes a TLS handshake with a certificate that is
// either for the wrong host or from an unknown CA. With sslmode=verify-full
// the dial must FAIL, after exactly ONE connection attempt (no plaintext
// fallback).

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T, cn string) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// leaf issues a server certificate for dnsName (and, if ip is set, that IP).
func (ca testCA) leaf(t *testing.T, dnsName string, ip ...net.IP) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: dnsName},
		DNSNames: []string{dnsName}, IPAddresses: ip,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// fakeTLSPostgres answers the 8-byte SSLRequest with 'S' and then performs a
// TLS handshake with cert. It counts connections.
func fakeTLSPostgres(t *testing.T, cert tls.Certificate) (port int, conns, handshakes *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	conns, handshakes = &atomic.Int64{}, &atomic.Int64{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				buf := make([]byte, 8)
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				if _, err := c.Write([]byte("S")); err != nil {
					return
				}
				srv := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
				if srv.Handshake() == nil {
					handshakes.Add(1)
				}
				_, _ = io.Copy(io.Discard, srv)
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, conns, handshakes
}

func TestVerifyFullRefusesABadServerCertificateWithoutFallback(t *testing.T) {
	trusted := newTestCA(t, "trusted-ca")
	untrusted := newTestCA(t, "untrusted-ca")
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, trusted.pem, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, cert := range map[string]tls.Certificate{
		"certificate for another host":   trusted.leaf(t, "not-the-writer.example.internal"),
		"certificate from an unknown CA": untrusted.leaf(t, "127.0.0.1"),
	} {
		port, conns, handshakes := fakeTLSPostgres(t, cert)
		cfg, err := ParseDSN("postgres://fence_iam@127.0.0.1:" + strconv.Itoa(port) +
			"/telephony?sslmode=verify-full&sslrootcert=" + url.QueryEscape(caFile))
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Fallbacks) != 0 {
			t.Fatalf("%s: verify-full produced fallbacks", name)
		}
		dial, err := PgxCredentialDialer(cfg, &fakeCreds{})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // TEST bound, not D10
		_, err = dial(ctx)
		cancel()
		if err == nil {
			t.Errorf("%s: connected despite a bad certificate", name)
		} else if !sameError(redact(err), errTLSVerification) {
			t.Errorf("%s: redacted as %q, want the TLS verification category", name, redact(err))
		}
		if got := conns.Load(); got != 1 {
			t.Errorf("%s: %d connection attempts; want exactly 1 (no plaintext or other fallback)", name, got)
		}
		time.Sleep(50 * time.Millisecond) // let the server record its handshake outcome
		if handshakes.Load() != 0 {
			t.Errorf("%s: the TLS handshake completed; the client should have rejected the certificate", name)
		}
	}

	// Positive control: a certificate from the trusted CA for the dialled
	// host completes the handshake, so the refusals above are certificate
	// verification, not a broken fake server.
	port, _, handshakes := fakeTLSPostgres(t, trusted.leaf(t, "writer.example.internal", net.ParseIP("127.0.0.1")))
	cfg, err := ParseDSN("postgres://fence_iam@127.0.0.1:" + strconv.Itoa(port) +
		"/telephony?sslmode=verify-full&sslrootcert=" + url.QueryEscape(caFile))
	if err != nil {
		t.Fatal(err)
	}
	dial, _ := PgxCredentialDialer(cfg, &fakeCreds{})
	// The fake server never answers the startup message, so the dial ends only
	// with its context. It is cancelled once the server records the handshake;
	// a fixed 300ms dial could end first under -race on a loaded host.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // TEST bound, not D10
	defer cancel()
	dialled := make(chan struct{})
	go func() { defer close(dialled); _, _ = dial(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for handshakes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-dialled
	if handshakes.Load() != 1 {
		t.Error("positive control: the trusted certificate did not complete a handshake")
	}
}
