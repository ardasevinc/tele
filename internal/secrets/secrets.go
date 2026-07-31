package secrets

import (
	"context"
	"errors"
	"fmt"
	"runtime"
)

var (
	ErrNotFound            = errors.New("secret not found")
	ErrBackendUnavailable  = errors.New("secret backend unavailable")
	ErrBackendUnconfigured = errors.New("secret backend unconfigured")
	ErrBackendLocked       = errors.New("secret backend locked")
	ErrCatalogIncomplete   = errors.New("secret catalog incomplete")
	ErrMigrationIncomplete = errors.New("secret migration incomplete")
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
	if e == nil || e.Kind == nil {
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

type Snapshotter interface {
	Snapshot(context.Context) (map[string][]byte, error)
}

type VaultDiagnostics struct {
	Path             string
	FormatVersion    uint16
	PayloadSchema    uint16
	Generation       uint64
	ArgonMemoryKiB   uint32
	ArgonIterations  uint32
	ArgonParallelism uint8
	Records          int
}

type VaultDiagnoser interface {
	VaultDiagnostics(context.Context) (VaultDiagnostics, error)
}

type Selection struct {
	Backend  BackendID
	Instance string
}

type OpenOptions struct {
	DataRoot   string
	Profile    string
	Passphrase []byte
}

func Open(selection Selection, opts OpenOptions) (Store, error) {
	if selection.Backend == "" {
		if runtime.GOOS == "darwin" {
			return openLegacyKeychain()
		}
		return nil, &BackendError{Kind: ErrBackendUnconfigured, Detail: "run tele secrets init --backend vault-v1"}
	}
	switch selection.Backend {
	case BackendVault:
		if selection.Instance == "" {
			return nil, &BackendError{Kind: ErrBackendUnconfigured, Backend: BackendVault, Detail: "missing instance UUID"}
		}
		return OpenVault(VaultPath(opts.DataRoot, opts.Profile, selection.Instance), opts.Profile, selection.Instance, opts.Passphrase)
	case BackendKeychainLegacy:
		return openLegacyKeychain()
	case BackendKeychain, BackendSecretService:
		return nil, &BackendError{Kind: ErrBackendUnavailable, Backend: selection.Backend, Detail: "backend is not implemented in this build"}
	default:
		return nil, &BackendError{Kind: ErrBackendUnavailable, Backend: selection.Backend, Detail: "unknown backend ID"}
	}
}

func BackendDisplayName(id BackendID) string {
	switch id {
	case BackendVault:
		return "portable vault"
	case BackendSecretService:
		return "Secret Service"
	case BackendKeychain, BackendKeychainLegacy:
		return "macOS Keychain"
	default:
		return string(id)
	}
}
