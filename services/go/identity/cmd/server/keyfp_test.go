package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
)

// `service signing-key-fingerprint` prints exactly one line naming the kid
// and the public fingerprint, and nothing of the private key.
func TestSigningKeyFingerprintCommand(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	t.Setenv("CS_IDENTITY_ACCESS_TOKEN_PRIVATE_KEY_FILE", writeFile(t, "signing.pem", pemBytes))
	t.Setenv("CS_IDENTITY_ACCESS_TOKEN_KID", "signing-2026-11")
	var out strings.Builder
	if err := writeSigningKeyFingerprint(&out); err != nil {
		t.Fatal(err)
	}
	if want := "signing kid=signing-2026-11 sha256=" + accesstoken.Fingerprint(pub) + "\n"; out.String() != want {
		t.Fatalf("output %q, want %q", out.String(), want)
	}
	if strings.Contains(out.String(), "PRIVATE") {
		t.Fatal("key material in the output")
	}
	if err := dispatch([]string{"signing-key-fingerprint", "extra"}); err == nil || !strings.Contains(err.Error(), "signing-key-fingerprint") {
		t.Fatalf("an unknown command's usage does not list the new one: %v", err)
	}
}
