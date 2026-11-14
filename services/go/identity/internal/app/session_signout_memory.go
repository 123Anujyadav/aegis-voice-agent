package app

import (
	"context"
	"time"
)

// sessionRevocationRecord is one recorded revoke-all, by the session that
// asked: what answers its retry.
type sessionRevocationRecord struct {
	refreshTokenHash string
	revokedAt        time.Time
	sessionsRevoked  int
}

var _ SessionRevocationRepository = (*MemoryIdentityRepository)(nil)

// RevokeSessionOnce implements SessionRevocationRepository: the check and the
// revocation under one lock, so of concurrent calls exactly one is true.
func (m *MemoryIdentityRepository) RevokeSessionOnce(_ context.Context, sessionID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok || s.IsRevoked {
		return false, nil
	}
	s.IsRevoked = true
	return true, nil
}

// RevokeAllSessions implements SessionRevocationRepository: the same checks
// and step as pgstore's, under the repository lock (all or nothing).
func (m *MemoryIdentityRepository) RevokeAllSessions(_ context.Context, op *SessionRevocation) (int, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	asking, ok := m.sessions[op.SessionID]
	if !ok || asking.SubscriberID != op.SubscriberID || asking.DeviceKeyThumbprint == "" ||
		asking.DeviceKeyThumbprint != op.DeviceKeyThumbprint || !equalHash(asking.RefreshTokenHash, op.RefreshTokenHash) {
		return 0, false, ErrSessionConflict
	}
	if asking.IsRevoked {
		if r, recorded := m.revocations[op.SessionID]; recorded && equalHash(r.refreshTokenHash, op.RefreshTokenHash) {
			return 0, true, nil
		}
		return 0, false, ErrSessionConflict
	}
	if !time.Now().Before(asking.ExpiresAt) {
		return 0, false, ErrSessionConflict
	}
	n := 0
	for _, s := range m.sessions {
		if s.SubscriberID == op.SubscriberID && !s.IsRevoked {
			s.IsRevoked = true
			n++
		}
	}
	if m.revocations == nil {
		m.revocations = map[string]sessionRevocationRecord{}
	}
	m.revocations[op.SessionID] = sessionRevocationRecord{
		refreshTokenHash: op.RefreshTokenHash, revokedAt: op.RevokedAt, sessionsRevoked: n,
	}
	return n, false, nil
}
