package app

import (
	"context"
	"sort"
	"time"
)

// memoryAccounts is the in-memory account storage (accounts.go): the reference
// the PostgreSQL implementation must match. It lives behind
// MemoryIdentityRepository's lock, so an account id can be checked against the
// sessions and erasure records in the same step, as one transaction does.
type memoryAccounts struct {
	accounts map[string]*Account
	methods  map[string]*memoryAuthMethod // by method id
	// creations counts account creations per hour (the M1 cap).
	creations map[time.Time]int
	// googleTokens maps a redeemed token's hash to its expiry.
	googleTokens map[string]time.Time
}

type memoryAuthMethod struct {
	AuthMethod
	revoked bool
}

// accountsLocked returns the account storage, creating it on first use. The
// caller holds the write lock.
func (m *MemoryIdentityRepository) accountsLocked() *memoryAccounts {
	if m.accts == nil {
		m.accts = &memoryAccounts{
			accounts:     map[string]*Account{},
			methods:      map[string]*memoryAuthMethod{},
			creations:    map[time.Time]int{},
			googleTokens: map[string]time.Time{},
		}
	}
	return m.accts
}

// CreateAccountWithMethod implements AccountRepository.
func (m *MemoryIdentityRepository) CreateAccountWithMethod(_ context.Context, a *Account, method *AuthMethod) error {
	if err := ValidNewAccount(a, method); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.accountsLocked()
	if _, ok := st.accounts[a.ID]; ok || m.subscriberIDSeenLocked(a.ID) {
		return ErrAccountIDInUse
	}
	for _, x := range st.methods {
		if !x.revoked && x.Kind == method.Kind && x.LookupPseudonym == method.LookupPseudonym {
			return ErrAuthMethodInUse
		}
	}
	if _, ok := st.methods[method.MethodID]; ok {
		return ErrInvalidAccountRecord
	}
	acct := *a
	st.accounts[a.ID] = &acct
	st.methods[method.MethodID] = &memoryAuthMethod{AuthMethod: *method}
	return nil
}

// subscriberIDSeenLocked reports whether id is held by any session or erasure
// record. The caller holds the lock.
func (m *MemoryIdentityRepository) subscriberIDSeenLocked(id string) bool {
	for _, s := range m.sessions {
		if s.SubscriberID == id {
			return true
		}
	}
	for i := range m.erasures {
		if m.erasures[i].SubscriberID == id {
			return true
		}
	}
	return false
}

// GetAccount implements AccountRepository.
func (m *MemoryIdentityRepository) GetAccount(_ context.Context, accountID string) (*Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.accountsLocked().accounts[accountID]
	if !ok {
		return nil, ErrAccountNotFound
	}
	cp := *a
	return &cp, nil
}

// FindAuthMethod implements AccountRepository.
func (m *MemoryIdentityRepository) FindAuthMethod(_ context.Context, kind string, lookups []KeyedPseudonym) (*AuthMethod, error) {
	if err := ValidLookups(kind, lookups); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var found []AuthMethod
	for _, x := range m.accountsLocked().methods {
		if x.revoked || x.Kind != kind {
			continue
		}
		for _, l := range lookups {
			if x.KeyID == l.KeyID && x.LookupPseudonym == l.Pseudonym {
				found = append(found, x.AuthMethod)
				break
			}
		}
	}
	switch len(found) {
	case 0:
		return nil, ErrAuthMethodNotFound
	case 1:
		return &found[0], nil
	default:
		return nil, ErrAuthMethodAmbiguous
	}
}

// RekeyAuthMethod implements AccountRepository.
func (m *MemoryIdentityRepository) RekeyAuthMethod(_ context.Context, methodID, fromKeyID string, to KeyedPseudonym) (bool, error) {
	if !ValidKeyedPseudonym(to) {
		return false, ErrInvalidAccountRecord
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.accountsLocked()
	x, ok := st.methods[methodID]
	if !ok || x.revoked || x.KeyID != fromKeyID {
		return false, nil
	}
	for id, y := range st.methods {
		if id != methodID && !y.revoked && y.Kind == x.Kind && y.LookupPseudonym == to.Pseudonym {
			return false, ErrAuthMethodInUse
		}
	}
	x.KeyID, x.LookupPseudonym = to.KeyID, to.Pseudonym
	return true, nil
}

// TouchAuthMethod implements AccountRepository.
func (m *MemoryIdentityRepository) TouchAuthMethod(_ context.Context, methodID string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if x, ok := m.accountsLocked().methods[methodID]; ok && at.After(x.LastUsedAt) {
		x.LastUsedAt = at
	}
	return nil
}

// AuthMethodKeyIDs implements AccountRepository.
func (m *MemoryIdentityRepository) AuthMethodKeyIDs(_ context.Context, kind string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]struct{}{}
	for _, x := range m.accountsLocked().methods {
		if !x.revoked && x.Kind == kind {
			seen[x.KeyID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// ReserveAccountCreation implements AccountRepository under the write lock:
// the check and the increment are one step. Hours before the previous one are
// dropped.
func (m *MemoryIdentityRepository) ReserveAccountCreation(_ context.Context, hourStart time.Time, limit int) (bool, error) {
	if limit < 1 {
		return false, nil
	}
	hour := hourStart.UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.accountsLocked()
	for h := range st.creations {
		if h.Before(hour.Add(-time.Hour)) {
			delete(st.creations, h)
		}
	}
	if st.creations[hour] >= limit {
		return false, nil
	}
	st.creations[hour]++
	return true, nil
}

// ReserveGoogleToken implements AccountRepository. Expired records are dropped
// as it goes.
func (m *MemoryIdentityRepository) ReserveGoogleToken(_ context.Context, tokenHash string, expiresAt time.Time) (bool, error) {
	if !isLowerHex64(tokenHash) || expiresAt.IsZero() {
		return false, ErrInvalidAccountRecord
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.accountsLocked()
	for h, exp := range st.googleTokens {
		if exp.Before(now) && h != tokenHash {
			delete(st.googleTokens, h)
		}
	}
	if _, seen := st.googleTokens[tokenHash]; seen {
		return false, nil
	}
	st.googleTokens[tokenHash] = expiresAt
	return true, nil
}

// eraseAccountLocked tombstones accountID and deletes its methods (INV-ID-7
// option A). A number-derived subscriber has no account row; nothing happens.
// The caller holds the write lock.
func (m *MemoryIdentityRepository) eraseAccountLocked(accountID string, at time.Time) {
	if m.accts == nil {
		return
	}
	for id, x := range m.accts.methods {
		if x.AccountID == accountID {
			delete(m.accts.methods, id)
		}
	}
	if a, ok := m.accts.accounts[accountID]; ok && a.Status == AccountActive {
		a.Status, a.ErasedAt = AccountErased, at
	}
}

var _ AccountRepository = (*MemoryIdentityRepository)(nil)
