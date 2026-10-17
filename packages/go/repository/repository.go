// Package repository defines generic persistence interfaces for entities of
// type T keyed by ID, and the errors their implementations return.
package repository

import (
	"context"
	"errors"
)

var (
	// ErrNotFound reports that no entity has the requested ID.
	ErrNotFound = errors.New("repository: entity not found")
	// ErrAlreadyExists reports that an entity with the same ID already exists.
	ErrAlreadyExists = errors.New("repository: entity already exists")
	// ErrOptimisticLock reports that the entity changed after it was read, so
	// the write was not applied.
	ErrOptimisticLock = errors.New("repository: optimistic lock failure")
)

// Reader finds entities by ID and lists them a page at a time.
type Reader[T any, ID any] interface {
	FindByID(ctx context.Context, id ID) (*T, error)
	List(ctx context.Context, limit int, offset int) ([]*T, error)
}

// Writer saves and deletes entities.
type Writer[T any, ID any] interface {
	Save(ctx context.Context, entity *T) error
	Delete(ctx context.Context, id ID) error
}

// Repository combines Reader and Writer.
type Repository[T any, ID any] interface {
	Reader[T, ID]
	Writer[T, ID]
}
