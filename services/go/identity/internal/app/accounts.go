package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// # Accounts and authentication methods (ADR-0035 D35.1, D35.2; G1)
//
// ADR-0035 (accepted for M1 on internal assurance, 2026-11-13) gives every
// account created by Google Sign-In, and later by a passkey, a STORED, RANDOM
// account id (O2): "usr_" and 32 lowercase hex characters from 128 bits of
// crypto/rand, recorded in identity_accounts and never derived from any
// identifier. It has the same shape as a number-derived subscriber id, so a
// session, an access token's subject and every consumer treat both alike. The
// SMS route's number-derived ids are not stored here and are unchanged.
//
// An authentication method attaches an external identity to an account. It is
// found by its LOOKUP PSEUDONYM, an HMAC under a keyring key used for nothing
// else (pseudonym_keyring.go); the method records which key, so the keyring
// can rotate. Among methods not revoked, a pseudonym belongs to one account.
// No email, name, token or credential is stored.
//
// Erasure (INV-ID-7 option A) deletes an account's methods and TOMBSTONES its
// id: the row stays, status erased, and the id is never issued again.
//
// G1 is storage only: nothing in a running process creates an account yet.
// Google token verification and the endpoint are G2.

// Account statuses.
const (
	AccountActive = "active"
	AccountErased = "erased"
)

// Authentication method kinds. Passkeys are accepted as ADR-0035's direction;
// nothing creates one until ADR-0037 is accepted.
const (
	AuthMethodGoogle  = "google"
	AuthMethodPasskey = "passkey"
)

// Account is one stored account (identity_accounts).
type Account struct {
	ID        string
	Status    string
	CreatedAt time.Time
	// ErasedAt is set exactly when Status is AccountErased.
	ErasedAt time.Time
}

// AuthMethod is one authentication method attached to an account
// (identity_auth_methods).
type AuthMethod struct {
	MethodID  string
	AccountID string
	Kind      string
	// KeyID names the keyring key LookupPseudonym was computed under.
	KeyID           string
	LookupPseudonym string
	CreatedAt       time.Time
	LastUsedAt      time.Time
}

// KeyedPseudonym is a lookup pseudonym and the id of the key that made it.
type KeyedPseudonym struct {
	KeyID     string
	Pseudonym string
}

var (
	// ErrAccountNotFound is an account id with no stored account.
	ErrAccountNotFound = errors.New("identity: account not found")
	// ErrAccountIDInUse is an account id already stored, tombstoned, or held
	// by a number-derived subscriber's sessions or erasure record: it is
	// never issued twice.
	ErrAccountIDInUse = errors.New("identity: the account id is in use or tombstoned")
	// ErrAuthMethodInUse is a lookup pseudonym already attached to an account.
	ErrAuthMethodInUse = errors.New("identity: the authentication method is already attached to an account")
	// ErrAuthMethodNotFound is a lookup that matches no method.
	ErrAuthMethodNotFound = errors.New("identity: authentication method not found")
	// ErrAuthMethodAmbiguous is a lookup matching more than one method: an
	// integrity failure (one identity on two accounts) that fails closed.
	ErrAuthMethodAmbiguous = errors.New("identity: authentication method lookup is ambiguous")
	// ErrInvalidAccountRecord is an account or method a repository refuses to
	// store or return.
	ErrInvalidAccountRecord = errors.New("identity: invalid account record")
	// ErrAccountsUnavailable is a repository that does not store accounts.
	ErrAccountsUnavailable = errors.New("identity: account storage is not available")
	// ErrAccountCreationCapReached is a creation refused because this hour's
	// account-creation budget is spent.
	ErrAccountCreationCapReached = errors.New("identity: the hourly account-creation cap is reached")
)

// AccountRepository stores accounts, their authentication methods, the hourly
// account-creation budget and the single-use record of Google ID tokens. Both
// backends implement it (accounts_memory.go; pgstore/accounts.go); the service
// reaches it through the IdentityRepository it was built with.
type AccountRepository interface {
	// CreateAccountWithMethod stores a new active account and its first method
	// in one step. ErrAccountIDInUse if the id is stored (active or
	// tombstoned) or appears in any session or erasure record;
	// ErrAuthMethodInUse if the method's (kind, pseudonym) is attached to a
	// method not revoked. On error nothing is stored.
	CreateAccountWithMethod(ctx context.Context, account *Account, method *AuthMethod) error
	// CreateAccountWithMethodWithinBudget is CreateAccountWithMethod and one
	// unit of the hour's account-creation budget (ReserveAccountCreation) in
	// ONE atomic step: the unit is spent only if the account is created. A
	// refused creation (ErrAccountIDInUse, ErrAuthMethodInUse, an invalid
	// record) spends nothing; a spent hour is ErrAccountCreationCapReached
	// and creates nothing.
	CreateAccountWithMethodWithinBudget(ctx context.Context, account *Account, method *AuthMethod, hourStart time.Time, limit int) error
	// GetAccount returns the stored account, active or tombstoned.
	GetAccount(ctx context.Context, accountID string) (*Account, error)
	// FindAuthMethod returns the method of kind, not revoked, whose key id and
	// pseudonym equal one of lookups. ErrAuthMethodNotFound if none;
	// ErrAuthMethodAmbiguous if more than one.
	FindAuthMethod(ctx context.Context, kind string, lookups []KeyedPseudonym) (*AuthMethod, error)
	// RekeyAuthMethod moves a method to another key's pseudonym, only while it
	// is still under fromKeyID (a compare-and-swap). It reports whether it did.
	// ErrAuthMethodInUse if the new pseudonym is attached to another method.
	RekeyAuthMethod(ctx context.Context, methodID, fromKeyID string, to KeyedPseudonym) (bool, error)
	// TouchAuthMethod records the method's last use.
	TouchAuthMethod(ctx context.Context, methodID string, at time.Time) error
	// AuthMethodKeyIDs returns the distinct key ids of kind's methods not
	// revoked: a keyring must hold every one of them (pseudonym_keyring.go).
	AuthMethodKeyIDs(ctx context.Context, kind string) ([]string, error)
	// ReserveAccountCreation counts one account creation in the hour starting
	// at hourStart and reports whether the hour's count is within limit; a
	// refused call counts nothing. Atomic across replicas.
	ReserveAccountCreation(ctx context.Context, hourStart time.Time, limit int) (bool, error)
	// ReserveGoogleToken records tokenHash and reports whether this is the
	// first time it was seen: the single-use check. expiresAt is the token's
	// own expiry; the record is kept at least that long plus a margin (one
	// hour) that must exceed any clock skew the verifier allows.
	ReserveGoogleToken(ctx context.Context, tokenHash string, expiresAt time.Time) (bool, error)
}

var (
	authKeyIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	methodIDPattern  = regexp.MustCompile(`^am_[0-9a-f]{32}$`)
)

// NewAuthMethodID returns "am_" and 32 hex characters of crypto/rand.
func NewAuthMethodID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("identity: generating a method id: %w", err)
	}
	return "am_" + hex.EncodeToString(b), nil
}

func validAuthKind(kind string) bool { return kind == AuthMethodGoogle || kind == AuthMethodPasskey }

// ValidTokenHash reports whether h is a token hash as stored: 64 lowercase hex
// characters (SHA-256).
func ValidTokenHash(h string) bool { return isLowerHex64(h) }

// ValidKeyedPseudonym reports whether p has a valid key id and pseudonym.
func ValidKeyedPseudonym(p KeyedPseudonym) bool {
	return authKeyIDPattern.MatchString(p.KeyID) && isLowerHex64(p.Pseudonym)
}

// ValidNewAccount checks an account and its first method before storing them.
func ValidNewAccount(a *Account, m *AuthMethod) error {
	switch {
	case a == nil || m == nil:
		return ErrInvalidAccountRecord
	case !validSubscriberID(a.ID) || a.Status != AccountActive || a.CreatedAt.IsZero() || !a.ErasedAt.IsZero():
		return fmt.Errorf("%w: the account", ErrInvalidAccountRecord)
	case !methodIDPattern.MatchString(m.MethodID) || m.AccountID != a.ID || !validAuthKind(m.Kind) ||
		!ValidKeyedPseudonym(KeyedPseudonym{KeyID: m.KeyID, Pseudonym: m.LookupPseudonym}) ||
		m.CreatedAt.IsZero() || m.LastUsedAt.IsZero():
		return fmt.Errorf("%w: the method", ErrInvalidAccountRecord)
	}
	return nil
}

// ValidLookups checks a FindAuthMethod request.
func ValidLookups(kind string, lookups []KeyedPseudonym) error {
	if !validAuthKind(kind) || len(lookups) == 0 || len(lookups) > MaxPseudonymKeys {
		return ErrInvalidAccountRecord
	}
	for _, l := range lookups {
		if !ValidKeyedPseudonym(l) {
			return ErrInvalidAccountRecord
		}
	}
	return nil
}

// AccountRepository returns the service's account storage, and false when its
// repository has none.
func (s *IdentityService) AccountRepository() (AccountRepository, bool) {
	repo, ok := s.repo.(AccountRepository)
	return repo, ok
}

// accounts returns the service's account storage, when its repository has it.
func (s *IdentityService) accounts() (AccountRepository, error) {
	repo, ok := s.repo.(AccountRepository)
	if !ok {
		return nil, ErrAccountsUnavailable
	}
	return repo, nil
}
