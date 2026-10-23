package accesstoken

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
)

// Fingerprint identifies an Ed25519 public key for operators: the unpadded
// base64url SHA-256 of its 32 raw bytes. It is public information (a hash of a
// public key) and safe to print, log or put in a ticket. The identity service
// computes it from the public half of its signing key and edge-api from each
// key it trusts, so a rotation step can confirm both sides hold the same key
// pair before anything is rolled out.
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
