package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Google identity pseudonym keyring (pseudonym_keyring.go): its file
// format, fail-closed parsing, and the pseudonyms it makes.

// testKeyHex returns a distinct, non-zero 32-byte test key in hex.
func testKeyHex(c byte) string { return strings.Repeat(hex.EncodeToString([]byte{c}), 32) }

func mustKeyring(t *testing.T, text string) *PseudonymKeyring {
	t.Helper()
	k, err := ParsePseudonymKeyring([]byte(text))
	if err != nil {
		t.Fatalf("ParsePseudonymKeyring: %v", err)
	}
	return k
}

func TestPseudonymKeyring_ParsesTheFileFormat(t *testing.T) {
	k := mustKeyring(t, "# Google subject keys; the first is current.\r\n\n"+
		"k2 "+testKeyHex(0x22)+"\r\n   \n# previous\n"+"k1\t"+testKeyHex(0x11)+"\n")
	if k.CurrentKeyID() != "k2" || strings.Join(k.KeyIDs(), ",") != "k2,k1" {
		t.Errorf("keys = %v, current %q", k.KeyIDs(), k.CurrentKeyID())
	}
	if !k.Holds("k1") || k.Holds("k3") {
		t.Error("Holds is wrong")
	}
}

func TestPseudonymKeyring_RefusesMalformedKeyringsWithoutQuotingKeys(t *testing.T) {
	secret := testKeyHex(0x5a)
	for name, text := range map[string]string{
		"empty":             "",
		"comments only":     "# nothing\n\n",
		"one field":         "k1\n",
		"three fields":      "k1 " + secret + " extra\n",
		"bad id":            "K1 " + secret + "\n",
		"long id":           strings.Repeat("a", 33) + " " + secret + "\n",
		"short key":         "k1 " + secret[:62] + "\n",
		"long key":          "k1 " + secret + "aa\n",
		"upper-case hex":    "k1 " + strings.ToUpper(secret) + "\n",
		"not hex":           "k1 " + strings.Repeat("zz", 32) + "\n",
		"all zero":          "k1 " + strings.Repeat("00", 32) + "\n",
		"duplicate id":      "k1 " + secret + "\nk1 " + testKeyHex(0x11) + "\n",
		"duplicate key":     "k1 " + secret + "\nk2 " + secret + "\n",
		"too many keys":     "a " + testKeyHex(1) + "\nb " + testKeyHex(2) + "\nc " + testKeyHex(3) + "\nd " + testKeyHex(4) + "\ne " + testKeyHex(5) + "\n",
		"too large":         "k1 " + secret + "\n" + strings.Repeat("#", maxKeyringFileBytes),
		"byte-order mark":   "\ufeffk1 " + secret + "\n",
		"key without an id": secret + "\n",
	} {
		_, err := ParsePseudonymKeyring([]byte(text))
		if !errors.Is(err, ErrInvalidPseudonymKeyring) {
			t.Errorf("%s: err = %v, want ErrInvalidPseudonymKeyring", name, err)
			continue
		}
		if strings.Contains(strings.ToLower(err.Error()), secret[:16]) {
			t.Errorf("%s: the error quotes key material: %v", name, err)
		}
	}
}

func TestPseudonymKeyring_ReadsOnlyAnAbsoluteRegularFileWithinTheLimit(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "keyring")
	if err := os.WriteFile(good, []byte("k1 "+testKeyHex(0x11)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if k, err := ReadPseudonymKeyringFile(good); err != nil || k.CurrentKeyID() != "k1" {
		t.Errorf("a good file: (%v, %v)", k, err)
	}
	big := filepath.Join(dir, "big")
	if err := os.WriteFile(big, []byte("k1 "+testKeyHex(0x11)+"\n"+strings.Repeat("#", maxKeyringFileBytes)), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"relative":  "keyring",
		"empty":     "",
		"missing":   filepath.Join(dir, "missing"),
		"directory": dir,
		"too large": big,
	} {
		if _, err := ReadPseudonymKeyringFile(path); !errors.Is(err, ErrInvalidPseudonymKeyring) {
			t.Errorf("%s: err = %v, want ErrInvalidPseudonymKeyring", name, err)
		}
	}
}

func TestPseudonymKeyring_GooglePseudonyms(t *testing.T) {
	k := mustKeyring(t, "k2 "+testKeyHex(0x22)+"\nk1 "+testKeyHex(0x11)+"\n")
	const iss, sub = "https://accounts.google.com", "112233445566778899"
	ps, err := k.GoogleSubjectPseudonyms(iss, sub)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 || ps[0].KeyID != "k2" || ps[1].KeyID != "k1" || ps[0].Pseudonym == ps[1].Pseudonym {
		t.Fatalf("pseudonyms = %+v: one per key, current first, different", ps)
	}
	for _, p := range ps {
		if !isLowerHex64(p.Pseudonym) {
			t.Errorf("pseudonym %q is not 64 lower-case hex", p.Pseudonym)
		}
	}
	again, _ := k.GoogleSubjectPseudonyms(iss, sub)
	if again[0] != ps[0] {
		t.Error("pseudonyms are not deterministic")
	}
	// Domain-separated: not the plain HMAC of the subject under the same key.
	key, _ := hex.DecodeString(testKeyHex(0x22))
	plain := hmac.New(sha256.New, key)
	plain.Write([]byte(sub))
	if ps[0].Pseudonym == hex.EncodeToString(plain.Sum(nil)) {
		t.Error("the pseudonym is not domain-separated")
	}
	// The issuer is part of the identity, and the separator is unambiguous.
	other, _ := k.GoogleSubjectPseudonyms("accounts.example", sub)
	shifted1, _ := k.GoogleSubjectPseudonyms("ab", "c")
	shifted2, _ := k.GoogleSubjectPseudonyms("a", "bc")
	if other[0] == ps[0] || shifted1[0] == shifted2[0] {
		t.Error("distinct identities share a pseudonym")
	}
	for name, parts := range map[string][2]string{
		"empty issuer":  {"", sub},
		"empty subject": {iss, ""},
		"NUL":           {iss, "1\x002"},
		"long subject":  {iss, strings.Repeat("1", maxIdentityPartBytes+1)},
	} {
		if _, err := k.GoogleSubjectPseudonyms(parts[0], parts[1]); !errors.Is(err, ErrInvalidExternalIdentity) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// A keyring that reuses another key is detectable, so startup can refuse it.
func TestPseudonymKeyring_SharesMaterialWith(t *testing.T) {
	k := mustKeyring(t, "k2 "+testKeyHex(0x22)+"\nk1 "+testKeyHex(0x11)+"\n")
	reused, _ := hex.DecodeString(testKeyHex(0x11))
	if !k.SharesMaterialWith(reused) {
		t.Error("a reused key was not detected")
	}
	if k.SharesMaterialWith([]byte("test_secret_key")) {
		t.Error("an unrelated key was reported as shared")
	}
}
