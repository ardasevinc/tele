//go:build !darwin

package secrets

import (
	"context"
	"runtime"
)

type unsupportedStore struct{}

func NewStore() Store {
	return unsupportedStore{}
}

func Backend() (name string, supported bool) {
	return "unsupported on " + runtime.GOOS, false
}

func (unsupportedStore) Get(context.Context, string, string) ([]byte, error) {
	return nil, ErrNotFound
}

func (unsupportedStore) Set(context.Context, string, string, []byte) error {
	return &UnsupportedError{GOOS: runtime.GOOS}
}

func (unsupportedStore) Delete(context.Context, string, string) error {
	return nil
}

type UnsupportedError struct {
	GOOS string
}

func (e *UnsupportedError) Error() string {
	return "secret storage is macOS Keychain-only in v1, current GOOS=" + e.GOOS
}
