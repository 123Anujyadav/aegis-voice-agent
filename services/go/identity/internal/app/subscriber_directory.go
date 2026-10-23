package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Subscriber-id directory: OFFLINE PREPARATION for ADR-0030 D30.3/D30.5.
//
// NOT WIRED. IdentityService still derives subscriber ids exactly as before
// (subscriberID, which now calls LegacySubscriberID). Nothing here is reached
// by a running process, and no schema exists for it. It exists so the
// semantics D30.5 specifies (legacy-preserving adoption, one id per number
// under concurrency, bounded collision retry) are pinned by tests before any
// migration is attempted, and so D30.3 can be decided against working code.

// LegacySubscriberID is the subscriber id identity has always derived: "usr_"
// and the first 32 hex characters of HMAC-SHA-256(key, canonical MSISDN),
// with no domain separation. It must never change: every existing subscriber
// id is this value, and D30.5's legacy-preserving adoption stores it.
func LegacySubscriberID(key []byte, msisdn string) string {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msisdn))
	return "usr_" + hex.EncodeToString(h.Sum(nil))[:32]
}

// subscriberDomain separates subscriber pseudonyms from every other keyed
// value (D30.1).
const subscriberDomain = "callscreen.identity.subscriber.v1\x00"

// SubscriberPseudonym is the lookup key of a number in the subscriber
// directory: HMAC-SHA-256 under the subscriber key over a domain-separated
// canonical number, 64 lowercase hex characters. It reveals nothing about the
// number without the key, and nothing about whether it is registered.
func SubscriberPseudonym(key []byte, msisdn string) (string, error) {
	if !isCanonicalMSISDN(msisdn) {
		return "", ErrInvalidMSISDN
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte(subscriberDomain))
	h.Write([]byte(msisdn))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SubscriberIDMode selects how a directory chooses the id of a number it has
// not seen (D30.5's stages 2 and 3).
type SubscriberIDMode int

const (
	// AdoptLegacyIDs stores the legacy derived id for a new mapping, so every
	// existing subscriber keeps their id (stage 2).
	AdoptLegacyIDs SubscriberIDMode = iota + 1
	// MintRandomIDs stores a fresh random id for a new mapping (stage 3, once
	// the legacy key is retired). Existing mappings are never changed.
	MintRandomIDs
)

// ErrSubscriberIDCollision is a random id that collided on every bounded
// attempt: fail closed rather than reuse an id.
var ErrSubscriberIDCollision = errors.New("identity: could not allocate a unique subscriber id")

// SubscriberDirectory maps a verified number's pseudonym to its stored
// subscriber id, creating the mapping on first verification. Resolve must be
// called only AFTER the number is verified (D30.5): it creates state.
type SubscriberDirectory interface {
	// Resolve returns the id for pseudonym, creating it if absent. legacyID is
	// the number's LegacySubscriberID, used by AdoptLegacyIDs. created reports
	// whether this call made the mapping.
	Resolve(ctx context.Context, pseudonym, legacyID string) (id string, created bool, err error)
}

// maxIDAttempts bounds random-id collision retries.
const maxIDAttempts = 3

// MemorySubscriberDirectory is an in-memory SubscriberDirectory: process-local,
// for tests and as the reference the PostgreSQL one must match.
type MemorySubscriberDirectory struct {
	mode SubscriberIDMode
	mint func() (string, error)

	mu    sync.Mutex
	byKey map[string]string   // pseudonym -> id
	ids   map[string]struct{} // every id in use (the UNIQUE constraint)
}

// NewMemorySubscriberDirectory returns an empty directory in mode. The mint
// function is the random-id source for MintRandomIDs; nil means
// NewRandomSubscriberID.
func NewMemorySubscriberDirectory(mode SubscriberIDMode, mint func() (string, error)) (*MemorySubscriberDirectory, error) {
	if mode != AdoptLegacyIDs && mode != MintRandomIDs {
		return nil, fmt.Errorf("identity: unknown subscriber id mode %d", mode)
	}
	if mint == nil {
		mint = NewRandomSubscriberID
	}
	return &MemorySubscriberDirectory{mode: mode, mint: mint, byKey: map[string]string{}, ids: map[string]struct{}{}}, nil
}

// NewRandomSubscriberID returns "usr_" and 32 hex characters of crypto/rand:
// the same shape as a legacy id, so no consumer can tell them apart.
func NewRandomSubscriberID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("identity: generating a subscriber id: %w", err)
	}
	return "usr_" + hex.EncodeToString(b), nil
}

// Resolve implements SubscriberDirectory. Concurrent first resolutions of one
// pseudonym all return the same id: the mapping is created once, under the
// directory's lock, exactly as INSERT … ON CONFLICT DO NOTHING followed by a
// SELECT behaves in the database.
func (d *MemorySubscriberDirectory) Resolve(_ context.Context, pseudonym, legacyID string) (string, bool, error) {
	if !isLowerHex64(pseudonym) || !validSubscriberID(legacyID) {
		return "", false, ErrInvalidToken
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if id, ok := d.byKey[pseudonym]; ok {
		return id, false, nil
	}
	id := legacyID
	if d.mode == MintRandomIDs {
		var err error
		if id, err = d.freshID(); err != nil {
			return "", false, err
		}
	} else if _, taken := d.ids[id]; taken {
		// A legacy id already mapped to a DIFFERENT pseudonym means two numbers
		// derived the same id: impossible for distinct numbers under one key,
		// so it is refused rather than shared.
		return "", false, ErrSubscriberIDCollision
	}
	d.byKey[pseudonym] = id
	d.ids[id] = struct{}{}
	return id, true, nil
}

// freshID mints a random id not already in use, within maxIDAttempts.
func (d *MemorySubscriberDirectory) freshID() (string, error) {
	for i := 0; i < maxIDAttempts; i++ {
		id, err := d.mint()
		if err != nil {
			return "", err
		}
		if !validSubscriberID(id) {
			return "", fmt.Errorf("identity: the subscriber id source returned a malformed id")
		}
		if _, taken := d.ids[id]; !taken {
			return id, nil
		}
	}
	return "", ErrSubscriberIDCollision
}

// validSubscriberID reports whether id has the subscriber-id shape: "usr_"
// and 32 lowercase hex characters.
func validSubscriberID(id string) bool {
	rest, ok := strings.CutPrefix(id, "usr_")
	if !ok || len(rest) != 32 {
		return false
	}
	for i := 0; i < len(rest); i++ {
		if c := rest[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
