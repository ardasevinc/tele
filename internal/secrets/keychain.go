//go:build darwin

package secrets

import (
	"context"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

const service = "tele"

type KeychainStore struct{}

func NewStore() Store {
	return KeychainStore{}
}

func Backend() (name string, supported bool) {
	return "macOS Keychain", true
}

func (KeychainStore) Get(_ context.Context, profile string, key string) ([]byte, error) {
	value, err := keyring.Get(service, account(profile, key))
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return []byte(value), nil
}

func (KeychainStore) Set(_ context.Context, profile string, key string, value []byte) error {
	return keyring.Set(service, account(profile, key), string(value))
}

func (KeychainStore) Delete(_ context.Context, profile string, key string) error {
	err := keyring.Delete(service, account(profile, key))
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

func account(profile string, key string) string {
	return fmt.Sprintf("%s:%s", profile, key)
}
