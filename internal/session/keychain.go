package session

import (
	"context"
	"errors"

	"github.com/ardasevinc/tele/internal/secrets"
)

const Key = "mtproto-session"

type KeychainStorage struct {
	Profile string
	Store   secrets.Store
}

func (s KeychainStorage) LoadSession(ctx context.Context) ([]byte, error) {
	data, err := s.Store.Get(ctx, s.Profile, Key)
	if errors.Is(err, secrets.ErrNotFound) {
		return nil, nil
	}
	return data, err
}

func (s KeychainStorage) StoreSession(ctx context.Context, data []byte) error {
	return s.Store.Set(ctx, s.Profile, Key, data)
}
