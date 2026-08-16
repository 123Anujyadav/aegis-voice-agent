package redis

import (
	"context"
	"errors"
	"time"
)

var (
	ErrCacheMiss = errors.New("redis: key not found")
	ErrKeyLocked = errors.New("redis: lock acquired by another process")
)

type Cache interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	Increment(ctx context.Context, key string, ttl time.Duration) (int64, error)
	AcquireLock(ctx context.Context, key string, ttl time.Duration) (bool, error)
	ReleaseLock(ctx context.Context, key string) error
}

type MemoryCache struct {
	data map[string][]byte
}

func NewMemoryCache() *MemoryCache {
	return &MemoryCache{
		data: make(map[string][]byte),
	}
}

func (m *MemoryCache) Get(ctx context.Context, key string) ([]byte, error) {
	val, exists := m.data[key]
	if !exists {
		return nil, ErrCacheMiss
	}
	return val, nil
}

func (m *MemoryCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	m.data[key] = value
	return nil
}

func (m *MemoryCache) Delete(ctx context.Context, key string) error {
	delete(m.data, key)
	return nil
}

func (m *MemoryCache) Increment(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	return 1, nil
}

func (m *MemoryCache) AcquireLock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	return true, nil
}

func (m *MemoryCache) ReleaseLock(ctx context.Context, key string) error {
	return nil
}
