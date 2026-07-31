//go:build !linux

package secrets

import (
	"context"
)

func InitSecretService(context.Context, string, string, string) (*SecretServiceStore, error) {
	return nil, &BackendError{Kind: ErrBackendUnavailable, Backend: BackendSecretService}
}

func OpenSecretService(context.Context, string, string, string) (*SecretServiceStore, error) {
	return nil, &BackendError{Kind: ErrBackendUnavailable, Backend: BackendSecretService}
}

func InspectSecretService(context.Context, string, string, string) error {
	return &BackendError{Kind: ErrBackendUnavailable, Backend: BackendSecretService}
}

func PurgeSecretService(context.Context, string, string, string) (int, error) {
	return 0, &BackendError{Kind: ErrBackendUnavailable, Backend: BackendSecretService}
}

type SecretServiceStore struct{}

func (*SecretServiceStore) Get(context.Context, string, string) ([]byte, error) {
	return nil, &BackendError{Kind: ErrBackendUnavailable, Backend: BackendSecretService}
}
func (*SecretServiceStore) Set(context.Context, string, string, []byte) error {
	return &BackendError{Kind: ErrBackendUnavailable, Backend: BackendSecretService}
}
func (*SecretServiceStore) Delete(context.Context, string, string) error {
	return &BackendError{Kind: ErrBackendUnavailable, Backend: BackendSecretService}
}
func (*SecretServiceStore) Snapshot(context.Context) (map[string][]byte, error) {
	return nil, &BackendError{Kind: ErrBackendUnavailable, Backend: BackendSecretService}
}
func (*SecretServiceStore) Close() {}
