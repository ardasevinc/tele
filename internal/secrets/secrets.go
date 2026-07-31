package secrets

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrNotFound            = errors.New("secret not found")
	ErrBackendUnavailable  = errors.New("secret backend unavailable")
	ErrBackendUnconfigured = errors.New("secret backend unconfigured")
)

type BackendID string

const (
	BackendKeychainLegacy BackendID = "keychain-legacy-v1"
	BackendKeychain       BackendID = "keychain-v1"
	BackendSecretService  BackendID = "secret-service-v1"
	BackendVault          BackendID = "vault-v1"
)

type BackendInfo struct {
	ID        BackendID
	Instance  string
	Name      string
	Supported bool
}

type BackendError struct {
	Kind    error
	Backend BackendID
	Detail  string
}

func (e *BackendError) Error() string {
	if e == nil {
		return "secret backend error"
	}
	message := e.Kind.Error()
	if e.Backend != "" {
		message += fmt.Sprintf(" (%s)", e.Backend)
	}
	if e.Detail != "" {
		message += ": " + e.Detail
	}
	return message
}

func (e *BackendError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Kind
}

type Store interface {
	Get(ctx context.Context, profile string, key string) ([]byte, error)
	Set(ctx context.Context, profile string, key string, value []byte) error
	Delete(ctx context.Context, profile string, key string) error
}

type Describer interface {
	BackendInfo() BackendInfo
}
