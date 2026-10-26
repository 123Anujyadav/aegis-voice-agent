package app

import (
	"context"
	"time"
)

// deletionReceipt answers a repeated web-deletion confirm.
type deletionReceipt struct {
	codeHash  string
	expiresAt time.Time
}

var _ WebErasureRepository = (*MemoryIdentityRepository)(nil)

// HasAccount implements WebErasureRepository.
func (m *MemoryIdentityRepository) HasAccount(_ context.Context, subscriberID string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.sessions {
		if s.SubscriberID == subscriberID {
			return true, nil
		}
	}
	return false, nil
}

// ReserveDeletionCodeRequest implements WebErasureRepository.
func (m *MemoryIdentityRepository) ReserveDeletionCodeRequest(_ context.Context, window time.Time, limit int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletionRequests[window]++
	return m.deletionRequests[window] <= limit, nil
}

// EraseSubscriberByDeletionCode implements WebErasureRepository: the
// challenge is consumed and the subscriber erased under one write lock.
func (m *MemoryIdentityRepository) EraseSubscriberByDeletionCode(_ context.Context, e *SubscriberErasure, requestID, codeHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.otps[requestID]
	if !ok || c.Purpose != OtpPurposeAccountDeletion || c.SubscriberID != e.SubscriberID {
		return ErrSessionConflict
	}
	delete(m.otps, requestID)
	m.eraseLocked(e)
	m.deletionReceipts[requestID] = deletionReceipt{codeHash: codeHash, expiresAt: e.ReceiptExpiresAt}
	return nil
}

// DeletionCodeReceipt implements WebErasureRepository. An expired receipt is gone.
func (m *MemoryIdentityRepository) DeletionCodeReceipt(_ context.Context, requestID string) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.deletionReceipts[requestID]
	if !ok || time.Now().After(r.expiresAt) {
		return "", false, nil
	}
	return r.codeHash, true, nil
}
