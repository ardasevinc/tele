//go:build darwin

package secrets

import (
	"context"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

const service = "tele"

type KeychainStore struct {
	dataRoot string
}

func NewStore() Store {
	return KeychainStore{}
}

func Backend() (name string, supported bool) {
	return "macOS Keychain", true
}

func (KeychainStore) BackendInfo() BackendInfo {
	return BackendInfo{ID: BackendKeychainLegacy, Name: "macOS Keychain", Supported: true}
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

func (s KeychainStore) Set(ctx context.Context, profile string, key string, value []byte) error {
	return s.withMutationLock(ctx, profile, func() error {
		return keyring.Set(service, account(profile, key), string(value))
	})
}

func (s KeychainStore) Delete(ctx context.Context, profile string, key string) error {
	return s.withMutationLock(ctx, profile, func() error {
		err := keyring.Delete(service, account(profile, key))
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return err
	})
}

func (s KeychainStore) withMutationLock(ctx context.Context, profile string, mutation func() error) error {
	if s.dataRoot == "" {
		return mutation()
	}
	return WithProfileLock(ctx, s.dataRoot, profile, func(context.Context) error {
		return mutation()
	})
}

func account(profile string, key string) string {
	return fmt.Sprintf("%s:%s", profile, key)
}
