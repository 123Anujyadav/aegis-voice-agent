package accesstoken

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

// The fingerprint of RFC 8037 A.4's public key, computed independently
// (Python hashlib), pins the definition.
func TestFingerprint_RFC8037Key(t *testing.T) {
	pub, err := base64.RawURLEncoding.DecodeString(rfc8037X)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("fixture: %v", err)
	}
	if got, want := Fingerprint(pub), "If4x36FUomFia_hUBG_SJxt77UtqvkWqWId-9H-XIbk"; got != want {
		t.Fatalf("Fingerprint = %s, want %s", got, want)
	}
}

// The private key's public half and the public key give the same
// fingerprint; different keys give different ones.
func TestFingerprint_MatchesAcrossTheKeyPair(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if Fingerprint(priv.Public().(ed25519.PublicKey)) != Fingerprint(pub) {
		t.Fatal("the two halves of one key pair fingerprint differently")
	}
	if Fingerprint(pub) == Fingerprint(other) {
		t.Fatal("two keys share a fingerprint")
	}
}
