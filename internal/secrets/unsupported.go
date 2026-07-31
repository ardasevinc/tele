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
	return nil, unavailableError()
}

func (unsupportedStore) Set(context.Context, string, string, []byte) error {
	return unavailableError()
}

func (unsupportedStore) Delete(context.Context, string, string) error {
	return unavailableError()
}

func (unsupportedStore) BackendInfo() BackendInfo {
	return BackendInfo{Name: "unsupported on " + runtime.GOOS, Supported: false}
}

type UnsupportedError struct {
	GOOS string
}

func (e *UnsupportedError) Error() string {
	return "secret storage is macOS Keychain-only in v1, current GOOS=" + e.GOOS
}

func (e *UnsupportedError) Unwrap() error {
	return ErrBackendUnavailable
}

func unavailableError() error {
	return &UnsupportedError{GOOS: runtime.GOOS}
}
