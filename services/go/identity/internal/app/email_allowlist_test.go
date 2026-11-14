package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The M1 Google tester allowlist (email_allowlist.go): trim and lower-case
// only, ASCII only, duplicates refused, and nothing quoted in errors.

func TestEmailAllowlist_NormalisesOnlyByTrimAndCase(t *testing.T) {
	a, err := ParseEmailAllowlist([]byte("# testers\r\n\n  Tester@Example.COM \r\nother@example.com\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Len() != 2 {
		t.Errorf("Len = %d", a.Len())
	}
	for email, want := range map[string]bool{
		"tester@example.com":     true,
		" TESTER@EXAMPLE.COM\t":  true,
		"other@example.com":      true,
		"tes.ter@example.com":    false, // dots are not removed
		"tester+x@example.com":   false, // +tags are not removed
		"tester@example.co":      false,
		"":                       false,
		"tester @example.com":    false,
		"tëster@example.com":     false,
		"tester@example.com\x00": false,
	} {
		if got := a.Allows(email); got != want {
			t.Errorf("Allows(%q) = %v, want %v", email, got, want)
		}
	}
	var none *EmailAllowlist
	if none.Allows("tester@example.com") {
		t.Error("a nil list allowed an address")
	}
}

func TestEmailAllowlist_RefusesBadListsWithoutQuotingAddresses(t *testing.T) {
	for name, text := range map[string]string{
		"empty":                   "",
		"comments only":           "# nobody\n",
		"no @":                    "tester.example.com\n",
		"two @":                   "a@b@example.com\n",
		"empty local part":        "@example.com\n",
		"empty domain":            "tester@\n",
		"inner space":             "tes ter@example.com\n",
		"non-ASCII":               "tëster@example.com\n",
		"too long":                strings.Repeat("a", 250) + "@example.com\n",
		"duplicate by case":       "secret-one@example.com\nSECRET-ONE@example.com\n",
		"duplicate by whitespace": "secret-one@example.com\n  secret-one@example.com\n",
		"too large":               strings.Repeat("#", maxAllowlistFileBytes+1),
	} {
		_, err := ParseEmailAllowlist([]byte(text))
		if !errors.Is(err, ErrInvalidEmailAllowlist) {
			t.Errorf("%s: err = %v", name, err)
			continue
		}
		if strings.Contains(strings.ToLower(err.Error()), "secret-one") || strings.Contains(err.Error(), "example.com") {
			t.Errorf("%s: the error quotes an address: %v", name, err)
		}
	}
}

func TestEmailAllowlist_ReadsOnlyAnAbsoluteRegularFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "allowlist")
	if err := os.WriteFile(good, []byte("tester@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if a, err := ReadEmailAllowlistFile(good); err != nil || !a.Allows("tester@example.com") {
		t.Errorf("good file: (%v, %v)", a, err)
	}
	for name, path := range map[string]string{"relative": "allowlist", "missing": filepath.Join(dir, "none"), "directory": dir} {
		if _, err := ReadEmailAllowlistFile(path); !errors.Is(err, ErrInvalidEmailAllowlist) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// Two lists from the same text hold different digests: the in-memory key is
// random per list, so a dump of one reveals nothing reusable.
func TestEmailAllowlist_DigestsAreKeyedPerList(t *testing.T) {
	a, _ := ParseEmailAllowlist([]byte("tester@example.com\n")) //nolint:errcheck // constant
	b, _ := ParseEmailAllowlist([]byte("tester@example.com\n")) //nolint:errcheck // constant
	for d := range a.set {
		if _, same := b.set[d]; same {
			t.Error("two lists share a digest")
		}
		if strings.Contains(d, "tester") {
			t.Error("a digest contains the address")
		}
	}
}
