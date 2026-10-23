package bootstrap

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"strings"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
)

// SigningKeyFingerprint reads the access-token signing key and its kid from
// the environment, exactly the two variables the service signs with, and
// returns the kid and accesstoken.Fingerprint of the key's PUBLIC half. Only
// these two variables are read: no database, no other secret. The key bytes
// are cleared after use and never returned; errors name the variable, never
// the path or the content.
//
// It is the operator check the signing-key rotation runbook uses: edge-api's
// `service trusted-keys` must show the same kid and fingerprint.
func SigningKeyFingerprint() (kid, fingerprint string, err error) {
	const fileVar, kidVar = EnvPrefix + "_ACCESS_TOKEN_PRIVATE_KEY_FILE", EnvPrefix + "_ACCESS_TOKEN_KID"
	kid = os.Getenv(kidVar)
	path := os.Getenv(fileVar)
	switch {
	case strings.TrimSpace(kid) == "" || strings.TrimSpace(kid) != kid:
		return "", "", fmt.Errorf("%w: %s is blank or padded", ErrInvalidConfig, kidVar)
	case strings.TrimSpace(path) == "" || strings.TrimSpace(path) != path:
		return "", "", fmt.Errorf("%w: %s is blank or padded", ErrInvalidConfig, fileVar)
	}
	data, err := readSecretFile(fileVar, path, MaxKeyFileBytes)
	if err != nil {
		return "", "", err
	}
	defer clear(data)
	key, err := accesstoken.ParsePrivateKeyPEM(data)
	if err != nil {
		return "", "", fmt.Errorf("%w: %s: %w", ErrInvalidConfig, fileVar, err)
	}
	defer clear(key)
	pub, ok := key.Public().(ed25519.PublicKey)
	if !ok {
		return "", "", fmt.Errorf("%w: %s: not an Ed25519 key", ErrInvalidConfig, fileVar)
	}
	return kid, accesstoken.Fingerprint(pub), nil
}
