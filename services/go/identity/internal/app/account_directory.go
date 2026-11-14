package app

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// GoogleAccountDirectory finds and creates accounts by Google identity
// (ADR-0035 D35.1, D35.2, D35.6): the issuer and subject together, held only
// as keyring pseudonyms. It decides nothing about WHETHER an account may be
// created: the caller (G2) verifies the ID token and applies the M1 gate and
// the hourly cap first. A Google identity never joins a second account: a
// concurrent first sign-in of the same identity returns the account the other
// created.
type GoogleAccountDirectory struct {
	repo    AccountRepository
	keyring *PseudonymKeyring
	now     func() time.Time
	// mintAccount and mintMethod are the id sources; tests replace them.
	mintAccount func() (string, error)
	mintMethod  func() (string, error)
}

// NewGoogleAccountDirectory returns a directory over repo using keyring.
func NewGoogleAccountDirectory(repo AccountRepository, keyring *PseudonymKeyring) (*GoogleAccountDirectory, error) {
	if repo == nil || keyring == nil || len(keyring.keys) == 0 {
		return nil, ErrAccountsUnavailable
	}
	return &GoogleAccountDirectory{
		repo: repo, keyring: keyring, now: time.Now,
		mintAccount: NewRandomSubscriberID, mintMethod: NewAuthMethodID,
	}, nil
}

// Lookup returns the active account of a Google identity, and found false if
// there is none. A method stored under a previous key is moved to the current
// key. A method whose account is not active is an integrity failure (erasure
// deletes an account's methods) and fails closed.
func (d *GoogleAccountDirectory) Lookup(ctx context.Context, issuer, subject string) (accountID string, found bool, err error) {
	lookups, err := d.keyring.GoogleSubjectPseudonyms(issuer, subject)
	if err != nil {
		return "", false, err
	}
	m, err := d.repo.FindAuthMethod(ctx, AuthMethodGoogle, lookups)
	if errors.Is(err, ErrAuthMethodNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	acct, err := d.repo.GetAccount(ctx, m.AccountID)
	if err != nil {
		return "", false, err
	}
	if acct.Status != AccountActive {
		return "", false, fmt.Errorf("%w: a method of an account that is not active", ErrInvalidAccountRecord)
	}
	if current := lookups[0]; m.KeyID != current.KeyID {
		if _, err := d.repo.RekeyAuthMethod(ctx, m.MethodID, m.KeyID, current); err != nil {
			return "", false, err
		}
	}
	if err := d.repo.TouchAuthMethod(ctx, m.MethodID, d.now()); err != nil {
		return "", false, err
	}
	return acct.ID, true, nil
}

// Create creates an account for a Google identity, with the identity as its
// first method under the current key, and returns its id with created true.
// If the identity is attached by now (a concurrent first sign-in won), it
// returns that account with created false. It refuses while a stored method
// uses a key the keyring does not hold (ErrPseudonymKeyringIncomplete), and
// after a bounded number of id collisions (ErrSubscriberIDCollision).
func (d *GoogleAccountDirectory) Create(ctx context.Context, issuer, subject string) (accountID string, created bool, err error) {
	return d.create(ctx, issuer, subject, func(a *Account, m *AuthMethod) error {
		return d.repo.CreateAccountWithMethod(ctx, a, m)
	})
}

// CreateWithinBudget is Create with one unit of the hour's account-creation
// budget spent in the same atomic step (ADR-0035 D35.8): the unit is spent
// only if an account is created. An identity attached by now (a concurrent
// first sign-in won) returns that account and spends nothing, even when the
// hour is spent. A spent hour with no such account is
// ErrAccountCreationCapReached.
func (d *GoogleAccountDirectory) CreateWithinBudget(ctx context.Context, issuer, subject string, hourStart time.Time, limit int) (accountID string, created bool, err error) {
	id, created, err := d.create(ctx, issuer, subject, func(a *Account, m *AuthMethod) error {
		return d.repo.CreateAccountWithMethodWithinBudget(ctx, a, m, hourStart, limit)
	})
	if errors.Is(err, ErrAccountCreationCapReached) {
		existing, found, lerr := d.Lookup(ctx, issuer, subject)
		if lerr != nil {
			return "", false, lerr
		}
		if found {
			return existing, false, nil
		}
	}
	return id, created, err
}

// create is Create and CreateWithinBudget, with the store step given.
func (d *GoogleAccountDirectory) create(ctx context.Context, issuer, subject string, store func(*Account, *AuthMethod) error) (accountID string, created bool, err error) {
	lookups, err := d.keyring.GoogleSubjectPseudonyms(issuer, subject)
	if err != nil {
		return "", false, err
	}
	stored, err := d.repo.AuthMethodKeyIDs(ctx, AuthMethodGoogle)
	if err != nil {
		return "", false, err
	}
	for _, id := range stored {
		if !d.keyring.Holds(id) {
			return "", false, ErrPseudonymKeyringIncomplete
		}
	}
	current := lookups[0]
	for attempt := 0; attempt < maxIDAttempts; attempt++ {
		id, err := d.mintAccount()
		if err != nil {
			return "", false, err
		}
		methodID, err := d.mintMethod()
		if err != nil {
			return "", false, err
		}
		at := d.now()
		err = store(
			&Account{ID: id, Status: AccountActive, CreatedAt: at},
			&AuthMethod{
				MethodID: methodID, AccountID: id, Kind: AuthMethodGoogle,
				KeyID: current.KeyID, LookupPseudonym: current.Pseudonym, CreatedAt: at, LastUsedAt: at,
			})
		switch {
		case err == nil:
			return id, true, nil
		case errors.Is(err, ErrAccountIDInUse):
			continue
		case errors.Is(err, ErrAuthMethodInUse):
			existing, found, lerr := d.Lookup(ctx, issuer, subject)
			if lerr != nil {
				return "", false, lerr
			}
			if !found {
				return "", false, fmt.Errorf("%w: the identity is attached but not found", ErrInvalidAccountRecord)
			}
			return existing, false, nil
		default:
			return "", false, err
		}
	}
	return "", false, ErrSubscriberIDCollision
}
