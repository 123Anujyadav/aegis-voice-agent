package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// # The Google identity pseudonym keyring (ADR-0035 D35.2; owner, 2026-11-13)
//
// A Google identity is stored only as a lookup pseudonym: HMAC-SHA-256 over a
// domain-separated issuer and subject, under a key USED FOR NOTHING ELSE
// (ADR-0030 D30.1). It is not the service's HMAC key, and nothing here reads
// or changes that key. The keys come from their own mounted file, so they can
// be rotated independently.
//
// # File format
//
// One key per line: a key id, whitespace, and 64 lowercase hex characters
// (32 bytes). Blank lines and lines starting with "#" are ignored. The FIRST
// key is the current key; the others are previous keys, kept for lookups
// during a rotation. 1 to MaxPseudonymKeys keys; ids and key material unique;
// no all-zero key. At most 4 KiB.
//
//	# Google subject pseudonym keys; the first is current.
//	k2026b <64 lower-case hex characters from a secure random source>
//	k2026a <64 lower-case hex characters: the previous key>
//
// # Versioning and rotation
//
// Every stored method records the id of the key its pseudonym was made with.
// A lookup computes the pseudonym under EVERY key and matches (key id,
// pseudonym) pairs, so a method stored under a previous key is still found;
// it is then re-keyed to the current key (a compare-and-swap). To rotate:
//  1. Add a new key at the top of the file and deploy: new methods use it, old
//     ones are found under their old key and moved on use.
//  2. A previous key may be removed only when no method not revoked is stored
//     under it (AuthMethodKeyIDs). Methods of people who never sign in again
//     need a re-keying job before then; that job is not built yet.
//
// # Fail closed
//
//   - A missing, unreadable, oversized or malformed file, a wrong key length,
//     a duplicate id or key, or an all-zero key is an error: Google Sign-In
//     must not start (the bootstrap wiring is G2). Errors name a line, never
//     key material.
//   - Creating an account REFUSES while any stored method uses a key id the
//     keyring does not hold (ErrPseudonymKeyringIncomplete): that method could
//     not be found, so the same person could otherwise get a second account.
//   - SharesMaterialWith lets startup refuse a keyring that reuses another key
//     (G2 checks it against the service's HMAC key).

// MaxPseudonymKeys is the most keys a keyring holds: the current key and up
// to three previous ones.
const MaxPseudonymKeys = 4

// maxKeyringFileBytes bounds a keyring file.
const maxKeyringFileBytes = 4096

// pseudonymKeyBytes is the length of every key.
const pseudonymKeyBytes = 32

// googleSubjectDomain separates Google identity pseudonyms from every other
// keyed value.
const googleSubjectDomain = "lily.identity.google-subject.v1\x00"

// maxIdentityPartBytes bounds an issuer or a subject.
const maxIdentityPartBytes = 255

var (
	// ErrInvalidPseudonymKeyring is a keyring file or text refused.
	ErrInvalidPseudonymKeyring = errors.New("identity: invalid pseudonym keyring")
	// ErrPseudonymKeyringIncomplete is a stored method whose key the keyring
	// does not hold: creating accounts is refused until it does.
	ErrPseudonymKeyringIncomplete = errors.New("identity: the pseudonym keyring lacks a key that stored methods use")
	// ErrInvalidExternalIdentity is an issuer or subject refused.
	ErrInvalidExternalIdentity = errors.New("identity: invalid external identity")
)

// PseudonymKeyring is an ordered set of HMAC keys for one purpose, the current
// key first. Its zero value is not usable; build one with
// ParsePseudonymKeyring or ReadPseudonymKeyringFile.
type PseudonymKeyring struct {
	keys []pseudonymKey
}

type pseudonymKey struct {
	id  string
	key []byte
}

// ReadPseudonymKeyringFile reads and parses a keyring from an absolute path to
// a regular file of at most 4 KiB.
func ReadPseudonymKeyringFile(path string) (*PseudonymKeyring, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: the keyring path must be absolute", ErrInvalidPseudonymKeyring)
	}
	f, err := os.Open(path) //nolint:gosec // an operator-configured path to a mounted Secret, checked absolute above
	if err != nil {
		return nil, fmt.Errorf("%w: the keyring file cannot be opened", ErrInvalidPseudonymKeyring)
	}
	defer f.Close() //nolint:errcheck // read-only
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: the keyring is not a regular file", ErrInvalidPseudonymKeyring)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxKeyringFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: the keyring file cannot be read", ErrInvalidPseudonymKeyring)
	}
	if len(data) > maxKeyringFileBytes {
		return nil, fmt.Errorf("%w: the keyring file is larger than %d bytes", ErrInvalidPseudonymKeyring, maxKeyringFileBytes)
	}
	return ParsePseudonymKeyring(data)
}

// ParsePseudonymKeyring parses keyring text (see the file format above).
// Errors name a line number, never key material.
func ParsePseudonymKeyring(data []byte) (*PseudonymKeyring, error) {
	if len(data) > maxKeyringFileBytes {
		return nil, fmt.Errorf("%w: larger than %d bytes", ErrInvalidPseudonymKeyring, maxKeyringFileBytes)
	}
	var ring PseudonymKeyring
	ids := map[string]int{}
	for i, line := range strings.Split(string(data), "\n") {
		n := i + 1
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id, key, err := parseKeyringLine(n, line)
		if err != nil {
			return nil, err
		}
		if at, dup := ids[id]; dup {
			return nil, fmt.Errorf("%w: lines %d and %d use the same key id", ErrInvalidPseudonymKeyring, at, n)
		}
		for _, k := range ring.keys {
			if hmac.Equal(k.key, key) {
				return nil, fmt.Errorf("%w: line %d repeats key material of key %q", ErrInvalidPseudonymKeyring, n, k.id)
			}
		}
		ids[id] = n
		ring.keys = append(ring.keys, pseudonymKey{id: id, key: key})
		if len(ring.keys) > MaxPseudonymKeys {
			return nil, fmt.Errorf("%w: more than %d keys", ErrInvalidPseudonymKeyring, MaxPseudonymKeys)
		}
	}
	if len(ring.keys) == 0 {
		return nil, fmt.Errorf("%w: no key", ErrInvalidPseudonymKeyring)
	}
	return &ring, nil
}

// parseKeyringLine parses one key line: a key id and its key.
func parseKeyringLine(n int, line string) (string, []byte, error) {
	fields := strings.Fields(line)
	if len(fields) != 2 {
		return "", nil, fmt.Errorf("%w: line %d must be a key id and a key", ErrInvalidPseudonymKeyring, n)
	}
	id, hexKey := fields[0], fields[1]
	if !authKeyIDPattern.MatchString(id) {
		return "", nil, fmt.Errorf("%w: line %d: the key id must be 1-32 lower-case letters, digits, '-' or '_'", ErrInvalidPseudonymKeyring, n)
	}
	key, err := hex.DecodeString(hexKey)
	if err != nil || len(key) != pseudonymKeyBytes || hexKey != strings.ToLower(hexKey) {
		return "", nil, fmt.Errorf("%w: line %d: the key must be %d lower-case hex characters", ErrInvalidPseudonymKeyring, n, 2*pseudonymKeyBytes)
	}
	if allZero(key) {
		return "", nil, fmt.Errorf("%w: line %d: the key is all zeros", ErrInvalidPseudonymKeyring, n)
	}
	return id, key, nil
}

func allZero(b []byte) bool {
	var acc byte
	for _, x := range b {
		acc |= x
	}
	return acc == 0
}

// CurrentKeyID is the id of the key new pseudonyms are made with.
func (k *PseudonymKeyring) CurrentKeyID() string { return k.keys[0].id }

// KeyIDs returns every key id, the current one first.
func (k *PseudonymKeyring) KeyIDs() []string {
	ids := make([]string, len(k.keys))
	for i, key := range k.keys {
		ids[i] = key.id
	}
	return ids
}

// Holds reports whether the keyring has a key with this id.
func (k *PseudonymKeyring) Holds(keyID string) bool {
	for _, key := range k.keys {
		if key.id == keyID {
			return true
		}
	}
	return false
}

// SharesMaterialWith reports, in constant time per key, whether any key equals
// other: a keyring must not reuse another purpose's key.
func (k *PseudonymKeyring) SharesMaterialWith(other []byte) bool {
	shared := false
	for _, key := range k.keys {
		if hmac.Equal(key.key, other) {
			shared = true
		}
	}
	return shared
}

// GoogleSubjectPseudonyms returns the lookup pseudonym of a Google identity
// (issuer and subject together) under every key, the current key first. The
// issuer must already be normalised by the caller; neither part may be empty,
// longer than 255 bytes, or contain NUL.
func (k *PseudonymKeyring) GoogleSubjectPseudonyms(issuer, subject string) ([]KeyedPseudonym, error) {
	for _, part := range []string{issuer, subject} {
		if part == "" || len(part) > maxIdentityPartBytes || strings.ContainsRune(part, 0) {
			return nil, ErrInvalidExternalIdentity
		}
	}
	out := make([]KeyedPseudonym, len(k.keys))
	for i, key := range k.keys {
		h := hmac.New(sha256.New, key.key)
		h.Write([]byte(googleSubjectDomain))
		h.Write([]byte(issuer))
		h.Write([]byte{0})
		h.Write([]byte(subject))
		out[i] = KeyedPseudonym{KeyID: key.id, Pseudonym: hex.EncodeToString(h.Sum(nil))}
	}
	return out, nil
}
