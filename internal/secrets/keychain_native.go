package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"time"
)

const keychainManifestSchema = "tele/keychain-manifest/v1"

type nativeKeychainAPI interface {
	Create(context.Context, string, []byte) error
	Replace(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
}

type NativeKeychainStore struct {
	api      nativeKeychainAPI
	profile  string
	instance string
	lockPath string
}

type keychainManifest struct {
	Schema     string            `json:"schema"`
	Instance   string            `json:"instance"`
	Profile    string            `json:"profile"`
	Generation uint64            `json:"generation"`
	Mappings   map[string]string `json:"mappings"`
	Garbage    []string          `json:"garbage"`
}

func InitKeychain(ctx context.Context, dataRoot, profile, instance string) (*NativeKeychainStore, error) {
	if err := validateVaultIdentity(profile, instance); err != nil {
		return nil, err
	}
	api, err := connectNativeKeychain()
	if err != nil {
		return nil, err
	}
	store := newNativeKeychainStore(api, dataRoot, profile, instance)
	if err := store.initialize(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

func OpenKeychain(ctx context.Context, dataRoot, profile, instance string) (*NativeKeychainStore, error) {
	if err := validateVaultIdentity(profile, instance); err != nil {
		return nil, err
	}
	api, err := connectNativeKeychain()
	if err != nil {
		return nil, err
	}
	store := newNativeKeychainStore(api, dataRoot, profile, instance)
	if err := withProfileLockPath(ctx, store.lockPath, func(ctx context.Context) error {
		_, err := store.loadManifest(ctx)
		return err
	}); err != nil {
		return nil, err
	}
	return store, nil
}

func InspectKeychain(ctx context.Context, dataRoot, profile, instance string) error {
	_, err := OpenKeychain(ctx, dataRoot, profile, instance)
	return err
}

func PurgeKeychain(ctx context.Context, dataRoot, profile, instance string) (int, error) {
	store, err := OpenKeychain(ctx, dataRoot, profile, instance)
	if err != nil {
		return 0, err
	}
	return store.purge(ctx)
}

func newNativeKeychainStore(api nativeKeychainAPI, dataRoot, profile, instance string) *NativeKeychainStore {
	return &NativeKeychainStore{
		api: api, profile: profile, instance: instance,
		lockPath: filepath.Join(dataRoot, profile, "secrets", "profile.lock"),
	}
}

func (s *NativeKeychainStore) Close() {}

func (s *NativeKeychainStore) BackendInfo() BackendInfo {
	return BackendInfo{ID: BackendKeychain, Instance: s.instance, Name: "macOS Keychain", Supported: true}
}

func (s *NativeKeychainStore) Get(ctx context.Context, profile, key string) ([]byte, error) {
	if err := s.validate(profile, key, nil); err != nil {
		return nil, err
	}
	var value []byte
	err := withProfileLockPath(ctx, s.lockPath, func(ctx context.Context) error {
		manifest, err := s.loadManifest(ctx)
		if err != nil {
			return err
		}
		physicalID, ok := manifest.Mappings[key]
		if !ok {
			return ErrNotFound
		}
		value, err = s.readPhysical(ctx, physicalID)
		return err
	})
	return value, err
}

func (s *NativeKeychainStore) Set(ctx context.Context, profile, key string, value []byte) error {
	if err := s.validate(profile, key, value); err != nil {
		return err
	}
	return withProfileLockPath(ctx, s.lockPath, func(ctx context.Context) error {
		manifest, err := s.loadManifest(ctx)
		if err != nil {
			return err
		}
		if _, exists := manifest.Mappings[key]; !exists && len(manifest.Mappings) >= vaultMaxRecords {
			return fmt.Errorf("keychain manifest record limit exceeded")
		}
		physicalID, err := NewVaultInstance()
		if err != nil {
			return err
		}
		if manifest.Generation == math.MaxUint64 {
			return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "manifest generation overflow"}
		}
		manifest.Generation++
		manifest.Garbage = append(manifest.Garbage, physicalID)
		if err := s.writeManifest(ctx, manifest, false); err != nil {
			return err
		}
		if err := s.api.Create(ctx, s.physicalAccount(physicalID), value); err != nil {
			return s.backendError(err)
		}
		readback, err := s.readPhysical(ctx, physicalID)
		if err != nil || !bytes.Equal(readback, value) {
			zeroBytes(readback)
			return &BackendError{Kind: ErrMigrationIncomplete, Backend: BackendKeychain, Detail: "physical item readback failed"}
		}
		zeroBytes(readback)
		previous := manifest.Mappings[key]
		if manifest.Generation == math.MaxUint64 {
			return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "manifest generation overflow"}
		}
		manifest.Generation++
		manifest.Mappings[key] = physicalID
		manifest.Garbage = removePhysicalID(manifest.Garbage, physicalID)
		if previous != "" {
			manifest.Garbage = append(manifest.Garbage, previous)
		}
		if err := s.writeManifest(ctx, manifest, false); err != nil {
			return err
		}
		if previous == "" {
			return nil
		}
		if err := s.deletePhysical(ctx, previous); err != nil {
			return err
		}
		if manifest.Generation == math.MaxUint64 {
			return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "manifest generation overflow"}
		}
		manifest.Generation++
		manifest.Garbage = removePhysicalID(manifest.Garbage, previous)
		return s.writeManifest(ctx, manifest, false)
	})
}

func (s *NativeKeychainStore) Delete(ctx context.Context, profile, key string) error {
	if err := s.validate(profile, key, nil); err != nil {
		return err
	}
	return withProfileLockPath(ctx, s.lockPath, func(ctx context.Context) error {
		manifest, err := s.loadManifest(ctx)
		if err != nil {
			return err
		}
		physicalID, exists := manifest.Mappings[key]
		if !exists {
			return nil
		}
		delete(manifest.Mappings, key)
		if manifest.Generation == math.MaxUint64 {
			return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "manifest generation overflow"}
		}
		manifest.Generation++
		manifest.Garbage = append(manifest.Garbage, physicalID)
		if err := s.writeManifest(ctx, manifest, false); err != nil {
			return err
		}
		if err := s.deletePhysical(ctx, physicalID); err != nil {
			return err
		}
		if manifest.Generation == math.MaxUint64 {
			return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "manifest generation overflow"}
		}
		manifest.Generation++
		manifest.Garbage = removePhysicalID(manifest.Garbage, physicalID)
		return s.writeManifest(ctx, manifest, false)
	})
}

func (s *NativeKeychainStore) Snapshot(ctx context.Context) (map[string][]byte, error) {
	var snapshot map[string][]byte
	err := withProfileLockPath(ctx, s.lockPath, func(ctx context.Context) error {
		manifest, err := s.loadManifest(ctx)
		if err != nil {
			return err
		}
		snapshot = make(map[string][]byte, len(manifest.Mappings))
		keys := make([]string, 0, len(manifest.Mappings))
		for key := range manifest.Mappings {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, err := s.readPhysical(ctx, manifest.Mappings[key])
			if err != nil {
				zeroSnapshotValues(snapshot)
				return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "manifest item is unreadable"}
			}
			snapshot[key] = value
		}
		return nil
	})
	return snapshot, err
}

func (s *NativeKeychainStore) CatalogDiagnostics(ctx context.Context) (CatalogDiagnostics, error) {
	var diagnostics CatalogDiagnostics
	err := withProfileLockPath(ctx, s.lockPath, func(ctx context.Context) error {
		manifest, err := s.loadManifest(ctx)
		if err != nil {
			return err
		}
		physicalItems := 0
		for _, physicalID := range manifest.Mappings {
			value, err := s.api.Get(ctx, s.physicalAccount(physicalID))
			if err != nil {
				return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "mapped physical item is missing or unreadable"}
			}
			zeroBytes(value)
			physicalItems++
		}
		orphans := 0
		for _, physicalID := range manifest.Garbage {
			value, err := s.api.Get(ctx, s.physicalAccount(physicalID))
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return s.backendError(err)
			}
			zeroBytes(value)
			orphans++
			physicalItems++
		}
		diagnostics = CatalogDiagnostics{
			Generation: manifest.Generation, Mappings: len(manifest.Mappings),
			PhysicalItems: physicalItems, Orphans: orphans,
		}
		return nil
	})
	return diagnostics, err
}

func (s *NativeKeychainStore) initialize(ctx context.Context) error {
	return withProfileLockPath(ctx, s.lockPath, func(ctx context.Context) error {
		if _, err := s.loadManifest(ctx); err == nil {
			return fmt.Errorf("keychain instance already exists")
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		sentinelID, err := NewVaultInstance()
		if err != nil {
			return err
		}
		account := s.prefix() + "sentinel:" + sentinelID
		first := []byte("tele-keychain-probe-v1")
		if err := s.api.Create(ctx, account, first); err != nil {
			return s.backendError(err)
		}
		probePresent := true
		defer func() {
			if probePresent {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = s.api.Delete(cleanupCtx, account)
			}
		}()
		value, err := s.api.Get(ctx, account)
		if err != nil || !bytes.Equal(value, first) {
			zeroBytes(value)
			return &BackendError{Kind: ErrBackendUnavailable, Backend: BackendKeychain, Detail: "sentinel read failed"}
		}
		zeroBytes(value)
		second := []byte("tele-keychain-probe-v1-replaced")
		if err := s.api.Replace(ctx, account, second); err != nil {
			return s.backendError(err)
		}
		value, err = s.api.Get(ctx, account)
		if err != nil || !bytes.Equal(value, second) {
			zeroBytes(value)
			return &BackendError{Kind: ErrBackendUnavailable, Backend: BackendKeychain, Detail: "sentinel replace readback failed"}
		}
		zeroBytes(value)
		if err := s.api.Delete(ctx, account); err != nil {
			return s.backendError(err)
		}
		probePresent = false
		return s.writeManifest(ctx, keychainManifest{
			Schema: keychainManifestSchema, Instance: s.instance, Profile: s.profile,
			Generation: 1, Mappings: map[string]string{}, Garbage: []string{},
		}, true)
	})
}

func (s *NativeKeychainStore) loadManifest(ctx context.Context) (keychainManifest, error) {
	encoded, err := s.api.Get(ctx, s.manifestAccount())
	if err != nil {
		return keychainManifest{}, s.backendError(err)
	}
	defer zeroBytes(encoded)
	if len(encoded) > vaultMaxSize || rejectDuplicateJSONKeys(encoded) != nil {
		return keychainManifest{}, &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "manifest is invalid"}
	}
	var manifest keychainManifest
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || ensureJSONEOF(decoder) != nil || s.validateManifest(manifest) != nil {
		return keychainManifest{}, &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "manifest is invalid"}
	}
	return manifest, nil
}

func (s *NativeKeychainStore) writeManifest(ctx context.Context, manifest keychainManifest, create bool) error {
	if err := s.validateManifest(manifest); err != nil {
		return err
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	defer zeroBytes(encoded)
	if create {
		err = s.api.Create(ctx, s.manifestAccount(), encoded)
	} else {
		err = s.api.Replace(ctx, s.manifestAccount(), encoded)
	}
	if err != nil {
		return s.backendError(err)
	}
	readback, err := s.loadManifest(ctx)
	if err != nil || readback.Generation != manifest.Generation || !equalStringMap(readback.Mappings, manifest.Mappings) || !equalStrings(readback.Garbage, manifest.Garbage) {
		return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "manifest readback verification failed"}
	}
	return nil
}

func (s *NativeKeychainStore) readPhysical(ctx context.Context, physicalID string) ([]byte, error) {
	value, err := s.api.Get(ctx, s.physicalAccount(physicalID))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "physical item is missing"}
		}
		return nil, s.backendError(err)
	}
	if len(value) > vaultMaxValueSize {
		zeroBytes(value)
		return nil, &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "physical item exceeds size limit"}
	}
	return value, nil
}

func (s *NativeKeychainStore) deletePhysical(ctx context.Context, physicalID string) error {
	err := s.api.Delete(ctx, s.physicalAccount(physicalID))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return s.backendError(err)
}

func (s *NativeKeychainStore) purge(ctx context.Context) (int, error) {
	deleted := 0
	err := withProfileLockPath(ctx, s.lockPath, func(ctx context.Context) error {
		manifest, err := s.loadManifest(ctx)
		if err != nil {
			return err
		}
		physicalIDs := make(map[string]struct{}, len(manifest.Mappings)+len(manifest.Garbage))
		for _, physicalID := range manifest.Mappings {
			physicalIDs[physicalID] = struct{}{}
		}
		for _, physicalID := range manifest.Garbage {
			physicalIDs[physicalID] = struct{}{}
		}
		ordered := make([]string, 0, len(physicalIDs))
		for physicalID := range physicalIDs {
			ordered = append(ordered, physicalID)
		}
		sort.Strings(ordered)
		for _, physicalID := range ordered {
			if err := s.api.Delete(ctx, s.physicalAccount(physicalID)); err != nil {
				if errors.Is(err, ErrNotFound) {
					continue
				}
				return s.backendError(err)
			}
			deleted++
		}
		if err := s.api.Delete(ctx, s.manifestAccount()); err != nil {
			return s.backendError(err)
		}
		deleted++
		return nil
	})
	return deleted, err
}

func (s *NativeKeychainStore) validate(profile, key string, value []byte) error {
	if profile != s.profile {
		return fmt.Errorf("keychain profile mismatch: got %q, want %q", profile, s.profile)
	}
	return validateSecretKeyValue(key, value)
}

func (s *NativeKeychainStore) validateManifest(manifest keychainManifest) error {
	if manifest.Schema != keychainManifestSchema || manifest.Instance != s.instance || manifest.Profile != s.profile || manifest.Generation == 0 || manifest.Mappings == nil || manifest.Garbage == nil {
		return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "manifest identity is invalid"}
	}
	if len(manifest.Mappings) > vaultMaxRecords {
		return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "manifest record limit exceeded"}
	}
	seen := make(map[string]struct{}, len(manifest.Garbage)+len(manifest.Mappings))
	for key, physicalID := range manifest.Mappings {
		if validateSecretKeyValue(key, nil) != nil || ValidateVaultInstance(physicalID) != nil {
			return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "manifest mapping is invalid"}
		}
		if _, exists := seen[physicalID]; exists {
			return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "manifest physical identity is duplicated"}
		}
		seen[physicalID] = struct{}{}
	}
	if len(manifest.Garbage) > vaultMaxRecords {
		return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "manifest garbage limit exceeded"}
	}
	for _, physicalID := range manifest.Garbage {
		if ValidateVaultInstance(physicalID) != nil {
			return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "manifest garbage identity is invalid"}
		}
		if _, exists := seen[physicalID]; exists {
			return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "manifest physical identity is duplicated"}
		}
		seen[physicalID] = struct{}{}
	}
	return nil
}

func removePhysicalID(values []string, target string) []string {
	result := values[:0]
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *NativeKeychainStore) backendError(err error) error {
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrBackendLocked) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &BackendError{Kind: ErrBackendUnavailable, Backend: BackendKeychain, Detail: err.Error()}
}

func (s *NativeKeychainStore) prefix() string {
	return "v1:" + s.profile + ":" + s.instance + ":"
}

func (s *NativeKeychainStore) manifestAccount() string {
	return s.prefix() + "manifest"
}

func (s *NativeKeychainStore) physicalAccount(physicalID string) string {
	return s.prefix() + "physical:" + physicalID
}
