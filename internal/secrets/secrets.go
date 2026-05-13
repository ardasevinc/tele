package secrets

import (
	"context"
	"errors"
)

var ErrNotFound = errors.New("secret not found")

type Store interface {
	Get(ctx context.Context, profile string, key string) ([]byte, error)
	Set(ctx context.Context, profile string, key string, value []byte) error
	Delete(ctx context.Context, profile string, key string) error
}
