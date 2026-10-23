package bootstrap

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
)

// The operator check prints the kid and the fingerprint of the signing key's
// public half, reading only the two signing variables.
func TestSigningKeyFingerprint_MatchesThePublicKey(t *testing.T) {
	path, pub, _ := signingKeyFile(t)
	t.Setenv(EnvPrefix+"_ACCESS_TOKEN_PRIVATE_KEY_FILE", path)
	t.Setenv(EnvPrefix+"_ACCESS_TOKEN_KID", "signing-2026-11")
	kid, fp, err := SigningKeyFingerprint()
	if err != nil || kid != "signing-2026-11" || fp != accesstoken.Fingerprint(pub) {
		t.Fatalf("(%q, %q, %v); want the kid and the public key's fingerprint %q", kid, fp, err, accesstoken.Fingerprint(pub))
	}
}

// Missing, padded or unreadable inputs are refused naming the variable, never
// the path or any key content.
func TestSigningKeyFingerprint_RefusesBadInput(t *testing.T) {
	good, _, _ := signingKeyFile(t)
	notAKey := writeFile(t, "plain.txt", []byte("not a key"))
	dir := t.TempDir()
	for name, tc := range map[string]struct{ file, kid, mention string }{
		"no kid":       {good, "", "_KID"},
		"padded kid":   {good, " k1", "_KID"},
		"no file":      {"", "k1", "_PRIVATE_KEY_FILE"},
		"missing file": {filepath.Join(dir, "absent.pem"), "k1", "does not exist"},
		"a directory":  {dir, "k1", "not a regular file"},
		"not a key":    {notAKey, "k1", "_PRIVATE_KEY_FILE"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(EnvPrefix+"_ACCESS_TOKEN_PRIVATE_KEY_FILE", tc.file)
			t.Setenv(EnvPrefix+"_ACCESS_TOKEN_KID", tc.kid)
			kid, fp, err := SigningKeyFingerprint()
			if !errors.Is(err, ErrInvalidConfig) || kid != "" || fp != "" || !strings.Contains(err.Error(), tc.mention) {
				t.Fatalf("(%q, %q, %v); want ErrInvalidConfig mentioning %q", kid, fp, err, tc.mention)
			}
			if tc.file != "" && strings.Contains(err.Error(), tc.file) {
				t.Errorf("the error names the path: %v", err)
			}
		})
	}
}
