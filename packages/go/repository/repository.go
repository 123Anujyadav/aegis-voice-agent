package repository

import (
	"context"
	"errors"
)

var (
	ErrNotFound      = errors.New("repository: entity not found")
	ErrAlreadyExists = errors.New("repository: entity already exists")
	ErrOptimisticLock = errors.New("repository: optimistic lock failure")
)

type Reader[T any, ID any] interface {
	FindByID(ctx context.Context, id ID) (*T, error)
	List(ctx context.Context, limit int, offset int) ([]*T, error)
}

type Writer[T any, ID any] interface {
	Save(ctx context.Context, entity *T) error
	Delete(ctx context.Context, id ID) error
}

type Repository[T any, ID any] interface {
	Reader[T, ID]
	Writer[T, ID]
}
