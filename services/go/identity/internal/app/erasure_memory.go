package app

import (
	"context"
	"time"
)

// erasureReceipt is what answers a repeated deletion of an erased session.
type erasureReceipt struct {
	thumbprint string
	expiresAt  time.Time
}

var _ ErasureRepository = (*MemoryIdentityRepository)(nil)

// EraseSubscriber implements ErasureRepository: the same erasure as pgstore's,
// under the repository lock (all or nothing).
func (m *MemoryIdentityRepository) EraseSubscriber(_ context.Context, e *SubscriberErasure) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	devices := map[string]struct{}{}
	for id, s := range m.sessions {
		if s.SubscriberID != e.SubscriberID {
			continue
		}
		if s.DeviceKeyThumbprint != "" {
			devices["dev_"+s.DeviceKeyThumbprint] = struct{}{}
		}
		delete(m.sessions, id)
		delete(m.superseded, id)
	}
	for id, c := range m.otps {
		if c.SubscriberID == e.SubscriberID {
			delete(m.otps, id)
		}
	}
	for id := range devices {
		delete(m.devices, id)
	}
	for key, d := range m.delegations {
		if d.SubscriberID == e.SubscriberID || d.GuardianID == e.SubscriberID {
			delete(m.delegations, key)
		}
	}
	record := *e
	record.Exceptions = append([]string(nil), e.Exceptions...)
	m.erasures = append(m.erasures, record)
	m.erasureReceipts[e.RequestingSessionID] = erasureReceipt{thumbprint: e.DeviceKeyThumbprint, expiresAt: e.ReceiptExpiresAt}
	return nil
}

// ErasureReceipt implements ErasureRepository. An expired receipt is gone.
func (m *MemoryIdentityRepository) ErasureReceipt(_ context.Context, sessionID string) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.erasureReceipts[sessionID]
	if !ok || time.Now().After(r.expiresAt) {
		return "", false, nil
	}
	return r.thumbprint, true, nil
}

// Erasures returns the recorded erasures, for tests.
func (m *MemoryIdentityRepository) Erasures() []SubscriberErasure {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]SubscriberErasure(nil), m.erasures...)
}
