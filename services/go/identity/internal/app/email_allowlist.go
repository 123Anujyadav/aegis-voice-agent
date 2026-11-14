package app

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// # The M1 Google tester allowlist (ADR-0035 D35.8; owner, 2026-11-13)
//
// At M1 only the owner's testers may sign in with Google. The gate is the
// verified email of the Google account. THE EMAIL IS A GATE, NEVER AN
// IDENTIFIER: accounts are keyed on the Google subject (pseudonym_keyring.go),
// and nothing here is stored or logged.
//
// File format: one email per line; blank lines and lines starting with "#" are
// ignored; at most 64 KiB.
//
// Normalisation (owner decision): trim surrounding whitespace and lower-case,
// nothing else. ASCII only at M1. Gmail dots and "+tags" are NOT removed: two
// addresses that differ only so may be two people. An address must have
// exactly one "@" with something on each side, no space or control character,
// and at most 254 characters. Two lines that normalise to the same address are
// refused.
//
// The list is held in memory only as HMACs under a key drawn at random for
// this process, so a memory dump does not list the testers' addresses.

// maxAllowlistFileBytes bounds an allowlist file.
const maxAllowlistFileBytes = 64 << 10

// maxEmailBytes is the longest accepted address (RFC 5321 path limit less the
// angle brackets).
const maxEmailBytes = 254

// ErrInvalidEmailAllowlist is an allowlist file or text refused. Its messages
// name a line, never an address.
var ErrInvalidEmailAllowlist = errors.New("identity: invalid email allowlist")

// EmailAllowlist is a set of normalised email addresses held as HMACs.
type EmailAllowlist struct {
	key []byte
	set map[string]struct{}
}

// NormalizeEmail trims and lower-cases an address, and reports whether the
// result is an acceptable M1 address (see above).
func NormalizeEmail(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > maxEmailBytes {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c <= ' ' || c >= 0x7f {
			return "", false
		}
	}
	local, domain, ok := strings.Cut(s, "@")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") {
		return "", false
	}
	return s, true
}

// ReadEmailAllowlistFile reads and parses an allowlist from an absolute path
// to a regular file of at most 64 KiB.
func ReadEmailAllowlistFile(path string) (*EmailAllowlist, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: the allowlist path must be absolute", ErrInvalidEmailAllowlist)
	}
	f, err := os.Open(path) //nolint:gosec // an operator-configured path to a mounted file, checked absolute above
	if err != nil {
		return nil, fmt.Errorf("%w: the allowlist file cannot be opened", ErrInvalidEmailAllowlist)
	}
	defer f.Close() //nolint:errcheck // read-only
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: the allowlist is not a regular file", ErrInvalidEmailAllowlist)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxAllowlistFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: the allowlist file cannot be read", ErrInvalidEmailAllowlist)
	}
	return ParseEmailAllowlist(data)
}

// ParseEmailAllowlist parses allowlist text. At least one address is required.
func ParseEmailAllowlist(data []byte) (*EmailAllowlist, error) {
	if len(data) > maxAllowlistFileBytes {
		return nil, fmt.Errorf("%w: larger than %d bytes", ErrInvalidEmailAllowlist, maxAllowlistFileBytes)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("%w: no randomness for the in-memory key", ErrInvalidEmailAllowlist)
	}
	a := &EmailAllowlist{key: key, set: map[string]struct{}{}}
	lineOf := map[string]int{}
	for i, line := range strings.Split(string(data), "\n") {
		n := i + 1
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		email, ok := NormalizeEmail(line)
		if !ok {
			return nil, fmt.Errorf("%w: line %d is not an acceptable address", ErrInvalidEmailAllowlist, n)
		}
		h := a.digest(email)
		if at, dup := lineOf[h]; dup {
			return nil, fmt.Errorf("%w: lines %d and %d are the same address", ErrInvalidEmailAllowlist, at, n)
		}
		lineOf[h] = n
		a.set[h] = struct{}{}
	}
	if len(a.set) == 0 {
		return nil, fmt.Errorf("%w: no address", ErrInvalidEmailAllowlist)
	}
	return a, nil
}

func (a *EmailAllowlist) digest(normalized string) string {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(normalized))
	return hex.EncodeToString(m.Sum(nil))
}

// Allows reports whether email, normalised, is on the list. An address that
// does not normalise is not.
func (a *EmailAllowlist) Allows(email string) bool {
	if a == nil {
		return false
	}
	normalized, ok := NormalizeEmail(email)
	if !ok {
		return false
	}
	_, found := a.set[a.digest(normalized)]
	return found
}

// Len is the number of addresses on the list. It reveals none of them.
func (a *EmailAllowlist) Len() int { return len(a.set) }
