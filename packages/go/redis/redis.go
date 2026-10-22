// Package redis defines the Cache contract used for caching, counters and
// locks, and MemoryCache, an in-process placeholder implementation of it.
package redis

import (
	"context"
	"errors"
	"time"
)

// Errors returned by Cache implementations.
var (
	// ErrCacheMiss reports that the key is not present.
	ErrCacheMiss = errors.New("redis: key not found")
	// ErrKeyLocked reports that the lock is held by another owner.
	ErrKeyLocked = errors.New("redis: lock acquired by another process")
)

// Cache is a key-value store with expiring entries, counters and locks.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	Increment(ctx context.Context, key string, ttl time.Duration) (int64, error)
	AcquireLock(ctx context.Context, key string, ttl time.Duration) (bool, error)
	ReleaseLock(ctx context.Context, key string) error
}

// MemoryCache is an in-process placeholder for Cache. It is not Redis and
// provides no distributed guarantee. It is not safe for concurrent use, it
// ignores every TTL, Increment does not count and AcquireLock always
// succeeds, so nothing built on its counters or locks is enforced.
type MemoryCache struct {
	data map[string][]byte
}

// NewMemoryCache returns an empty MemoryCache.
func NewMemoryCache() *MemoryCache {
	return &MemoryCache{
		data: make(map[string][]byte),
	}
}

// Get returns the value stored for key, or ErrCacheMiss.
func (m *MemoryCache) Get(ctx context.Context, key string) ([]byte, error) {
	val, exists := m.data[key]
	if !exists {
		return nil, ErrCacheMiss
	}
	return val, nil
}

// Set stores value for key. The ttl is ignored: entries never expire.
func (m *MemoryCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	m.data[key] = value
	return nil
}

// Delete removes key. Deleting an absent key is not an error.
func (m *MemoryCache) Delete(ctx context.Context, key string) error {
	delete(m.data, key)
	return nil
}

// Increment does not count: it always returns 1, so a limit built on it never
// triggers.
func (m *MemoryCache) Increment(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	return 1, nil
}

// AcquireLock always reports success, so it provides no mutual exclusion.
func (m *MemoryCache) AcquireLock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	return true, nil
}

// ReleaseLock does nothing and always succeeds.
func (m *MemoryCache) ReleaseLock(ctx context.Context, key string) error {
	return nil
}
