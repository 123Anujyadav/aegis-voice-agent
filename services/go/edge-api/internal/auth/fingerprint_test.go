package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
)

func setKeyEnv(t *testing.T, activeFile, activeKid, prevFile, prevKid string) {
	t.Helper()
	t.Setenv(EnvPrefix+"_ACCESS_TOKEN_PUBLIC_KEY_FILE", activeFile)
	t.Setenv(EnvPrefix+"_ACCESS_TOKEN_KID", activeKid)
	t.Setenv(EnvPrefix+"_ACCESS_TOKEN_PREVIOUS_PUBLIC_KEY_FILE", prevFile)
	t.Setenv(EnvPrefix+"_ACCESS_TOKEN_PREVIOUS_KID", prevKid)
}

// The operator check lists each trusted key with its kid and the same
// fingerprint identity prints for the matching private key.
func TestTrustedKeyFingerprints_ListActiveAndPrevious(t *testing.T) {
	newPub, _, _ := ed25519.GenerateKey(rand.Reader)
	oldPub, _, _ := ed25519.GenerateKey(rand.Reader)
	setKeyEnv(t, publicKeyFile(t, newPub), kidNew, "", "")
	keys, err := TrustedKeyFingerprints()
	if err != nil || len(keys) != 1 || keys[0] != (TrustedKey{Role: "active", KeyID: kidNew, Fingerprint: accesstoken.Fingerprint(newPub)}) {
		t.Fatalf("(%+v, %v); want the active key only", keys, err)
	}
	setKeyEnv(t, publicKeyFile(t, newPub), kidNew, publicKeyFile(t, oldPub), kidOld)
	keys, err = TrustedKeyFingerprints()
	want := []TrustedKey{
		{Role: "active", KeyID: kidNew, Fingerprint: accesstoken.Fingerprint(newPub)},
		{Role: "previous", KeyID: kidOld, Fingerprint: accesstoken.Fingerprint(oldPub)},
	}
	if err != nil || len(keys) != 2 || keys[0] != want[0] || keys[1] != want[1] {
		t.Fatalf("(%+v, %v); want %+v", keys, err, want)
	}
}

// The same rules as startup: the previous key needs its kid, a distinct kid,
// and nothing blank or padded.
func TestTrustedKeyFingerprints_RefusesBadConfiguration(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	prev, _, _ := ed25519.GenerateKey(rand.Reader)
	pf, prevf := publicKeyFile(t, pub), publicKeyFile(t, prev)
	for name, tc := range map[string][4]string{
		"no active kid":      {pf, "", "", ""},
		"padded active kid":  {pf, " a", "", ""},
		"no active file":     {"", "a", "", ""},
		"previous file only": {pf, "a", prevf, ""},
		"previous kid only":  {pf, "a", "", "b"},
		"same kid":           {pf, "a", prevf, "a"},
		"not a key":          {writeFile(t, "plain.txt", []byte("hello")), "a", "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			setKeyEnv(t, tc[0], tc[1], tc[2], tc[3])
			keys, err := TrustedKeyFingerprints()
			if !errors.Is(err, ErrInvalidConfig) || keys != nil {
				t.Fatalf("(%+v, %v); want ErrInvalidConfig", keys, err)
			}
			for _, path := range []string{pf, prevf} {
				if strings.Contains(err.Error(), path) {
					t.Errorf("the error names a path: %v", err)
				}
			}
		})
	}
}
