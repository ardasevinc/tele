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
	return "unconfigured on " + runtime.GOOS, false
}

func (unsupportedStore) Get(context.Context, string, string) ([]byte, error) {
	return nil, unconfiguredError()
}

func (unsupportedStore) Set(context.Context, string, string, []byte) error {
	return unconfiguredError()
}

func (unsupportedStore) Delete(context.Context, string, string) error {
	return unconfiguredError()
}

func (unsupportedStore) BackendInfo() BackendInfo {
	return BackendInfo{Name: "unconfigured on " + runtime.GOOS, Supported: false}
}

type UnsupportedError struct {
	GOOS string
}

func (e *UnsupportedError) Error() string {
	return "no default secret backend is configured, current GOOS=" + e.GOOS
}

func (e *UnsupportedError) Unwrap() error {
	return ErrBackendUnconfigured
}

func unconfiguredError() error {
	return &UnsupportedError{GOOS: runtime.GOOS}
}
