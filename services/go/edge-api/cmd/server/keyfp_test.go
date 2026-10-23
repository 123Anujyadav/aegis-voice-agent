package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
)

// `service trusted-keys` prints one line per trusted key: role, kid and
// public fingerprint.
func TestTrustedKeysCommand(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "issuer.pub.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CS_EDGE_API_ACCESS_TOKEN_PUBLIC_KEY_FILE", path)
	t.Setenv("CS_EDGE_API_ACCESS_TOKEN_KID", "signing-2026-11")
	t.Setenv("CS_EDGE_API_ACCESS_TOKEN_PREVIOUS_PUBLIC_KEY_FILE", "")
	t.Setenv("CS_EDGE_API_ACCESS_TOKEN_PREVIOUS_KID", "")
	var out strings.Builder
	if err := writeTrustedKeys(&out); err != nil {
		t.Fatal(err)
	}
	if want := "active kid=signing-2026-11 sha256=" + accesstoken.Fingerprint(pub) + "\n"; out.String() != want {
		t.Fatalf("output %q, want %q", out.String(), want)
	}
	t.Setenv("CS_EDGE_API_ACCESS_TOKEN_KID", "")
	if err := writeTrustedKeys(&out); err == nil || !strings.Contains(err.Error(), "trusted-keys") {
		t.Fatalf("a bad configuration: %v", err)
	}
}
