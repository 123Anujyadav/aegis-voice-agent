package auth

import (
	"fmt"
	"os"
	"strings"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
)

// TrustedKey is one key edge-api trusts, as an operator sees it.
type TrustedKey struct {
	// Role is "active" or "previous".
	Role string
	// KeyID is the kid tokens signed with this key carry.
	KeyID string
	// Fingerprint is accesstoken.Fingerprint of the public key.
	Fingerprint string
}

// TrustedKeyFingerprints reads the active and (if set) previous public keys
// and kids from the environment, the same four variables the verifier trusts,
// and returns each one's fingerprint. Only these variables are read: no
// database, no DSN. The previous key must be set with its kid or not at all,
// and its kid must differ from the active one, as at startup.
//
// It is the operator check the signing-key rotation runbook uses: identity's
// `service signing-key-fingerprint` must match one of these.
func TrustedKeyFingerprints() ([]TrustedKey, error) {
	active, err := trustedKey("active", "_ACCESS_TOKEN_PUBLIC_KEY_FILE", "_ACCESS_TOKEN_KID")
	if err != nil {
		return nil, err
	}
	keys := []TrustedKey{active}
	prevFile, prevKid := os.Getenv(EnvPrefix+"_ACCESS_TOKEN_PREVIOUS_PUBLIC_KEY_FILE"), os.Getenv(EnvPrefix+"_ACCESS_TOKEN_PREVIOUS_KID")
	switch {
	case prevFile == "" && prevKid == "":
		return keys, nil
	case prevFile == "" || prevKid == "":
		return nil, fmt.Errorf("%w: %s_ACCESS_TOKEN_PREVIOUS_PUBLIC_KEY_FILE and %s_ACCESS_TOKEN_PREVIOUS_KID must be set together", ErrInvalidConfig, EnvPrefix, EnvPrefix)
	}
	previous, err := trustedKey("previous", "_ACCESS_TOKEN_PREVIOUS_PUBLIC_KEY_FILE", "_ACCESS_TOKEN_PREVIOUS_KID")
	if err != nil {
		return nil, err
	}
	if previous.KeyID == active.KeyID {
		return nil, fmt.Errorf("%w: %s_ACCESS_TOKEN_PREVIOUS_KID must differ from %s_ACCESS_TOKEN_KID", ErrInvalidConfig, EnvPrefix, EnvPrefix)
	}
	return append(keys, previous), nil
}

func trustedKey(role, fileSuffix, kidSuffix string) (TrustedKey, error) {
	fileVar, kidVar := EnvPrefix+fileSuffix, EnvPrefix+kidSuffix
	path, kid := os.Getenv(fileVar), os.Getenv(kidVar)
	switch {
	case strings.TrimSpace(kid) == "" || strings.TrimSpace(kid) != kid:
		return TrustedKey{}, fmt.Errorf("%w: %s is blank or padded", ErrInvalidConfig, kidVar)
	case strings.TrimSpace(path) == "" || strings.TrimSpace(path) != path:
		return TrustedKey{}, fmt.Errorf("%w: %s is blank or padded", ErrInvalidConfig, fileVar)
	}
	pub, err := readPublicKeyFile(fileVar, path)
	if err != nil {
		return TrustedKey{}, err
	}
	return TrustedKey{Role: role, KeyID: kid, Fingerprint: accesstoken.Fingerprint(pub)}, nil
}
