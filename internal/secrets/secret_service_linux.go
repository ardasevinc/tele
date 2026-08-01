//go:build linux

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

	"github.com/godbus/dbus/v5"
)

const (
	secretServiceName                    = "org.freedesktop.secrets"
	secretServicePath    dbus.ObjectPath = "/org/freedesktop/secrets"
	secretServiceAPI                     = "org.freedesktop.Secret.Service"
	secretCollectionAPI                  = "org.freedesktop.Secret.Collection"
	secretItemAPI                        = "org.freedesktop.Secret.Item"
	secretSessionAPI                     = "org.freedesktop.Secret.Session"
	secretPropertiesAPI                  = "org.freedesktop.DBus.Properties"
	secretManifestSchema                 = "tele/secret-manifest/v1"
)

type secretServiceSecret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string `dbus:"content_type"`
}

type secretServiceAPIClient interface {
	CreateItem(context.Context, string, map[string]string, []byte, bool) (string, error)
	SearchItems(context.Context, map[string]string) ([]string, error)
	GetSecret(context.Context, string) ([]byte, error)
	GetAttributes(context.Context, string) (map[string]string, error)
	DeleteItem(context.Context, string) error
	Close() error
}

type SecretServiceStore struct {
	api      secretServiceAPIClient
	profile  string
	instance string
	lockPath string
}

type secretManifest struct {
	Schema     string            `json:"schema"`
	Instance   string            `json:"instance"`
	Profile    string            `json:"profile"`
	Generation uint64            `json:"generation"`
	Mappings   map[string]string `json:"mappings"`
}

func InitSecretService(ctx context.Context, dataRoot, profile, instance string) (*SecretServiceStore, error) {
	if err := validateVaultIdentity(profile, instance); err != nil {
		return nil, err
	}
	api, err := connectSecretService(ctx)
	if err != nil {
		return nil, err
	}
	store := newSecretServiceStore(api, dataRoot, profile, instance)
	if err := store.initialize(ctx); err != nil {
		_ = api.Close()
		return nil, err
	}
	return store, nil
}

func OpenSecretService(ctx context.Context, dataRoot, profile, instance string) (*SecretServiceStore, error) {
	if err := validateVaultIdentity(profile, instance); err != nil {
		return nil, err
	}
	api, err := connectSecretService(ctx)
	if err != nil {
		return nil, err
	}
	store := newSecretServiceStore(api, dataRoot, profile, instance)
	if err := withProfileLockPath(ctx, store.lockPath, func(ctx context.Context) error {
		_, err := store.loadManifest(ctx)
		return err
	}); err != nil {
		_ = api.Close()
		return nil, err
	}
	return store, nil
}

func InspectSecretService(ctx context.Context, dataRoot, profile, instance string) error {
	store, err := OpenSecretService(ctx, dataRoot, profile, instance)
	if err != nil {
		return err
	}
	store.Close()
	return nil
}

func PurgeSecretService(ctx context.Context, dataRoot, profile, instance string) (int, error) {
	store, err := OpenSecretService(ctx, dataRoot, profile, instance)
	if err != nil {
		return 0, err
	}
	defer store.Close()
	return store.purge(ctx)
}

func (s *SecretServiceStore) purge(ctx context.Context) (int, error) {
	deleted := 0
	err := withProfileLockPath(ctx, s.lockPath, func(ctx context.Context) error {
		paths, err := s.api.SearchItems(ctx, s.instanceAttributes())
		if err != nil {
			return s.backendError(err)
		}
		var manifestPath string
		var otherPaths []string
		for _, path := range paths {
			attrs, err := s.api.GetAttributes(ctx, path)
			if err != nil {
				return s.backendError(err)
			}
			if attrs["tele-kind"] == "manifest" {
				if manifestPath != "" {
					return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendSecretService, Detail: "manifest is not unique"}
				}
				manifestPath = path
			} else {
				otherPaths = append(otherPaths, path)
			}
		}
		if manifestPath == "" {
			return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendSecretService, Detail: "manifest is missing"}
		}
		sort.Strings(otherPaths)
		for _, path := range otherPaths {
			if err := s.api.DeleteItem(ctx, path); err != nil {
				return s.backendError(err)
			}
			deleted++
		}
		if err := s.api.DeleteItem(ctx, manifestPath); err != nil {
			return s.backendError(err)
		}
		deleted++
		return nil
	})
	return deleted, err
}

func newSecretServiceStore(api secretServiceAPIClient, dataRoot, profile, instance string) *SecretServiceStore {
	return &SecretServiceStore{
		api: api, profile: profile, instance: instance,
		lockPath: filepath.Join(dataRoot, profile, "secrets", "profile.lock"),
	}
}

func (s *SecretServiceStore) Close() {
	if s != nil && s.api != nil {
		_ = s.api.Close()
	}
}

func (s *SecretServiceStore) BackendInfo() BackendInfo {
	return BackendInfo{ID: BackendSecretService, Instance: s.instance, Name: "Secret Service", Supported: true}
}

func (s *SecretServiceStore) Get(ctx context.Context, profile, key string) ([]byte, error) {
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

func (s *SecretServiceStore) Set(ctx context.Context, profile, key string, value []byte) error {
	if err := s.validate(profile, key, value); err != nil {
		return err
	}
	return withProfileLockPath(ctx, s.lockPath, func(ctx context.Context) error {
		manifest, err := s.loadManifest(ctx)
		if err != nil {
			return err
		}
		if _, exists := manifest.Mappings[key]; !exists && len(manifest.Mappings) >= vaultMaxRecords {
			return fmt.Errorf("secret manifest record limit exceeded")
		}
		physicalID, err := NewVaultInstance()
		if err != nil {
			return err
		}
		if _, err := s.api.CreateItem(ctx, "Tele secret "+physicalID, s.physicalAttributes(physicalID), value, false); err != nil {
			return s.backendError(err)
		}
		readback, err := s.readPhysical(ctx, physicalID)
		if err != nil || !bytes.Equal(readback, value) {
			zeroBytes(readback)
			return &BackendError{Kind: ErrMigrationIncomplete, Backend: BackendSecretService, Detail: "physical item readback failed"}
		}
		zeroBytes(readback)
		previous := manifest.Mappings[key]
		if manifest.Generation == math.MaxUint64 {
			return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendSecretService, Detail: "manifest generation overflow"}
		}
		manifest.Generation++
		manifest.Mappings[key] = physicalID
		if err := s.writeManifest(ctx, manifest); err != nil {
			return err
		}
		if previous != "" {
			_ = s.deletePhysical(ctx, previous)
		}
		return nil
	})
}

func (s *SecretServiceStore) Delete(ctx context.Context, profile, key string) error {
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
			return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendSecretService, Detail: "manifest generation overflow"}
		}
		manifest.Generation++
		if err := s.writeManifest(ctx, manifest); err != nil {
			return err
		}
		return s.deletePhysical(ctx, physicalID)
	})
}

func (s *SecretServiceStore) Snapshot(ctx context.Context) (map[string][]byte, error) {
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
				return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendSecretService, Detail: "manifest item is unreadable"}
			}
			snapshot[key] = value
		}
		return nil
	})
	return snapshot, err
}

func (s *SecretServiceStore) CatalogDiagnostics(ctx context.Context) (CatalogDiagnostics, error) {
	var diagnostics CatalogDiagnostics
	err := withProfileLockPath(ctx, s.lockPath, func(ctx context.Context) error {
		manifest, err := s.loadManifest(ctx)
		if err != nil {
			return err
		}
		paths, err := s.api.SearchItems(ctx, s.baseAttributes("physical"))
		if err != nil {
			return s.backendError(err)
		}
		referenced := make(map[string]struct{}, len(manifest.Mappings))
		for _, physicalID := range manifest.Mappings {
			referenced[physicalID] = struct{}{}
		}
		orphans := 0
		for _, path := range paths {
			attrs, err := s.api.GetAttributes(ctx, path)
			if err != nil {
				return s.backendError(err)
			}
			if _, ok := referenced[attrs["tele-physical-id"]]; !ok {
				orphans++
			}
		}
		diagnostics = CatalogDiagnostics{
			Generation: manifest.Generation, Mappings: len(manifest.Mappings),
			PhysicalItems: len(paths), Orphans: orphans,
		}
		return nil
	})
	return diagnostics, err
}

func (s *SecretServiceStore) initialize(ctx context.Context) error {
	return withProfileLockPath(ctx, s.lockPath, func(ctx context.Context) error {
		if _, err := s.loadManifest(ctx); err == nil {
			return fmt.Errorf("secret service instance already exists")
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		sentinelID, err := NewVaultInstance()
		if err != nil {
			return err
		}
		attrs := s.baseAttributes("sentinel")
		attrs["tele-sentinel"] = sentinelID
		first := []byte("tele-secret-service-probe-v1")
		if _, err := s.api.CreateItem(ctx, "Tele Secret Service probe", attrs, first, false); err != nil {
			return s.backendError(err)
		}
		probePresent := true
		defer func() {
			if !probePresent {
				return
			}
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			paths, err := s.api.SearchItems(cleanupCtx, attrs)
			if err != nil {
				return
			}
			for _, path := range paths {
				_ = s.api.DeleteItem(cleanupCtx, path)
			}
		}()
		paths, err := s.api.SearchItems(ctx, attrs)
		if err != nil || len(paths) != 1 {
			return &BackendError{Kind: ErrBackendUnavailable, Backend: BackendSecretService, Detail: "sentinel create could not be verified"}
		}
		value, err := s.api.GetSecret(ctx, paths[0])
		if err != nil || !bytes.Equal(value, first) {
			zeroBytes(value)
			return &BackendError{Kind: ErrBackendUnavailable, Backend: BackendSecretService, Detail: "sentinel read failed"}
		}
		zeroBytes(value)
		second := []byte("tele-secret-service-probe-v1-replaced")
		if _, err := s.api.CreateItem(ctx, "Tele Secret Service probe", attrs, second, true); err != nil {
			return s.backendError(err)
		}
		paths, err = s.api.SearchItems(ctx, attrs)
		if err != nil || len(paths) != 1 {
			return &BackendError{Kind: ErrBackendUnavailable, Backend: BackendSecretService, Detail: "sentinel replace was not atomic"}
		}
		value, err = s.api.GetSecret(ctx, paths[0])
		if err != nil || !bytes.Equal(value, second) {
			zeroBytes(value)
			return &BackendError{Kind: ErrBackendUnavailable, Backend: BackendSecretService, Detail: "sentinel replace readback failed"}
		}
		zeroBytes(value)
		if err := s.api.DeleteItem(ctx, paths[0]); err != nil {
			return s.backendError(err)
		}
		probePresent = false
		return s.writeManifest(ctx, secretManifest{
			Schema: secretManifestSchema, Instance: s.instance, Profile: s.profile,
			Generation: 1, Mappings: map[string]string{},
		})
	})
}

func (s *SecretServiceStore) loadManifest(ctx context.Context) (secretManifest, error) {
	paths, err := s.api.SearchItems(ctx, s.baseAttributes("manifest"))
	if err != nil {
		return secretManifest{}, s.backendError(err)
	}
	if len(paths) == 0 {
		return secretManifest{}, ErrNotFound
	}
	if len(paths) != 1 {
		return secretManifest{}, &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendSecretService, Detail: "manifest is not unique"}
	}
	encoded, err := s.api.GetSecret(ctx, paths[0])
	if err != nil {
		return secretManifest{}, s.backendError(err)
	}
	defer zeroBytes(encoded)
	if len(encoded) > vaultMaxSize || rejectDuplicateJSONKeys(encoded) != nil {
		return secretManifest{}, &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendSecretService, Detail: "manifest is invalid"}
	}
	var manifest secretManifest
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || ensureJSONEOF(decoder) != nil || s.validateManifest(manifest) != nil {
		return secretManifest{}, &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendSecretService, Detail: "manifest is invalid"}
	}
	return manifest, nil
}

func (s *SecretServiceStore) writeManifest(ctx context.Context, manifest secretManifest) error {
	if err := s.validateManifest(manifest); err != nil {
		return err
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	defer zeroBytes(encoded)
	if _, err := s.api.CreateItem(ctx, "Tele secret manifest "+s.instance, s.baseAttributes("manifest"), encoded, true); err != nil {
		return s.backendError(err)
	}
	readback, err := s.loadManifest(ctx)
	if err != nil || readback.Generation != manifest.Generation || !equalStringMap(readback.Mappings, manifest.Mappings) {
		return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendSecretService, Detail: "manifest readback verification failed"}
	}
	return nil
}

func (s *SecretServiceStore) readPhysical(ctx context.Context, physicalID string) ([]byte, error) {
	paths, err := s.api.SearchItems(ctx, s.physicalAttributes(physicalID))
	if err != nil {
		return nil, s.backendError(err)
	}
	if len(paths) != 1 {
		return nil, &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendSecretService, Detail: "physical item is missing or duplicated"}
	}
	value, err := s.api.GetSecret(ctx, paths[0])
	if err != nil {
		return nil, s.backendError(err)
	}
	if len(value) > vaultMaxValueSize {
		zeroBytes(value)
		return nil, &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendSecretService, Detail: "physical item exceeds size limit"}
	}
	return value, nil
}

func (s *SecretServiceStore) deletePhysical(ctx context.Context, physicalID string) error {
	paths, err := s.api.SearchItems(ctx, s.physicalAttributes(physicalID))
	if err != nil {
		return s.backendError(err)
	}
	for _, path := range paths {
		if err := s.api.DeleteItem(ctx, path); err != nil {
			return s.backendError(err)
		}
	}
	return nil
}

func (s *SecretServiceStore) baseAttributes(kind string) map[string]string {
	attrs := s.instanceAttributes()
	attrs["tele-kind"] = kind
	return attrs
}

func (s *SecretServiceStore) instanceAttributes() map[string]string {
	return map[string]string{
		"application": "tele", "tele-backend": string(BackendSecretService),
		"tele-instance": s.instance, "tele-profile": s.profile,
	}
}

func (s *SecretServiceStore) physicalAttributes(physicalID string) map[string]string {
	attrs := s.baseAttributes("physical")
	attrs["tele-physical-id"] = physicalID
	return attrs
}

func (s *SecretServiceStore) validate(profile, key string, value []byte) error {
	if profile != s.profile {
		return fmt.Errorf("secret service profile mismatch: got %q, want %q", profile, s.profile)
	}
	if err := validateSecretKeyValue(key, value); err != nil {
		return err
	}
	return nil
}

func (s *SecretServiceStore) validateManifest(manifest secretManifest) error {
	if manifest.Schema != secretManifestSchema || manifest.Instance != s.instance || manifest.Profile != s.profile || manifest.Generation == 0 || manifest.Mappings == nil {
		return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendSecretService, Detail: "manifest identity is invalid"}
	}
	if len(manifest.Mappings) > vaultMaxRecords {
		return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendSecretService, Detail: "manifest record limit exceeded"}
	}
	for key, physicalID := range manifest.Mappings {
		if err := validateSecretKeyValue(key, nil); err != nil || ValidateVaultInstance(physicalID) != nil {
			return &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendSecretService, Detail: "manifest mapping is invalid"}
		}
	}
	return nil
}

func (s *SecretServiceStore) backendError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrBackendLocked) {
		return err
	}
	return &BackendError{Kind: ErrBackendUnavailable, Backend: BackendSecretService, Detail: err.Error()}
}

type dbusSecretService struct {
	conn       *dbus.Conn
	service    dbus.BusObject
	collection dbus.BusObject
	session    dbus.ObjectPath
}

func connectSecretService(ctx context.Context) (*dbusSecretService, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, &BackendError{Kind: ErrBackendUnavailable, Backend: BackendSecretService, Detail: "session bus unavailable"}
	}
	client := &dbusSecretService{conn: conn, service: conn.Object(secretServiceName, secretServicePath)}
	fail := func(err error) (*dbusSecretService, error) {
		_ = conn.Close()
		return nil, err
	}
	var owner bool
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, secretServiceName).Store(&owner); err != nil || !owner {
		return fail(&BackendError{Kind: ErrBackendUnavailable, Backend: BackendSecretService, Detail: "service owner unavailable"})
	}
	var output dbus.Variant
	if err := client.service.CallWithContext(ctx, secretServiceAPI+".OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&output, &client.session); err != nil {
		return fail(&BackendError{Kind: ErrBackendUnavailable, Backend: BackendSecretService, Detail: "session open failed"})
	}
	var collectionPath dbus.ObjectPath
	if err := client.service.CallWithContext(ctx, secretServiceAPI+".ReadAlias", 0, "default").Store(&collectionPath); err != nil || collectionPath == "/" {
		return fail(&BackendError{Kind: ErrBackendUnavailable, Backend: BackendSecretService, Detail: "default collection unavailable"})
	}
	client.collection = conn.Object(secretServiceName, collectionPath)
	var unlocked []dbus.ObjectPath
	var prompt dbus.ObjectPath
	if err := client.service.CallWithContext(ctx, secretServiceAPI+".Unlock", 0, []dbus.ObjectPath{collectionPath}).Store(&unlocked, &prompt); err != nil {
		return fail(&BackendError{Kind: ErrBackendUnavailable, Backend: BackendSecretService, Detail: "collection unlock failed"})
	}
	if prompt != "/" {
		return fail(&BackendError{Kind: ErrBackendLocked, Backend: BackendSecretService, Detail: "collection requires interactive unlock"})
	}
	var locked dbus.Variant
	if err := client.collection.CallWithContext(ctx, secretPropertiesAPI+".Get", 0, secretCollectionAPI, "Locked").Store(&locked); err != nil {
		return fail(&BackendError{Kind: ErrBackendUnavailable, Backend: BackendSecretService, Detail: "collection state unavailable"})
	}
	if value, ok := locked.Value().(bool); !ok || value {
		return fail(&BackendError{Kind: ErrBackendLocked, Backend: BackendSecretService})
	}
	return client, nil
}

func (c *dbusSecretService) CreateItem(ctx context.Context, label string, attrs map[string]string, value []byte, replace bool) (string, error) {
	properties := map[string]dbus.Variant{
		secretItemAPI + ".Label":      dbus.MakeVariant(label),
		secretItemAPI + ".Attributes": dbus.MakeVariant(attrs),
	}
	secret := secretServiceSecret{Session: c.session, Parameters: []byte{}, Value: value, ContentType: "application/octet-stream"}
	var item, prompt dbus.ObjectPath
	if err := c.collection.CallWithContext(ctx, secretCollectionAPI+".CreateItem", 0, properties, secret, replace).Store(&item, &prompt); err != nil {
		return "", err
	}
	if prompt != "/" {
		return "", &BackendError{Kind: ErrBackendLocked, Backend: BackendSecretService, Detail: "item creation requires interaction"}
	}
	return string(item), nil
}

func (c *dbusSecretService) SearchItems(ctx context.Context, attrs map[string]string) ([]string, error) {
	var paths []dbus.ObjectPath
	if err := c.collection.CallWithContext(ctx, secretCollectionAPI+".SearchItems", 0, attrs).Store(&paths); err != nil {
		return nil, err
	}
	result := make([]string, len(paths))
	for i, path := range paths {
		result[i] = string(path)
	}
	return result, nil
}

func (c *dbusSecretService) GetSecret(ctx context.Context, path string) ([]byte, error) {
	var secret secretServiceSecret
	if err := c.conn.Object(secretServiceName, dbus.ObjectPath(path)).CallWithContext(ctx, secretItemAPI+".GetSecret", 0, c.session).Store(&secret); err != nil {
		return nil, err
	}
	return append([]byte(nil), secret.Value...), nil
}

func (c *dbusSecretService) GetAttributes(ctx context.Context, path string) (map[string]string, error) {
	var attributes dbus.Variant
	if err := c.conn.Object(secretServiceName, dbus.ObjectPath(path)).CallWithContext(ctx, secretPropertiesAPI+".Get", 0, secretItemAPI, "Attributes").Store(&attributes); err != nil {
		return nil, err
	}
	value, ok := attributes.Value().(map[string]string)
	if !ok {
		return nil, fmt.Errorf("secret service returned malformed item attributes")
	}
	return value, nil
}

func (c *dbusSecretService) DeleteItem(ctx context.Context, path string) error {
	var prompt dbus.ObjectPath
	if err := c.conn.Object(secretServiceName, dbus.ObjectPath(path)).CallWithContext(ctx, secretItemAPI+".Delete", 0).Store(&prompt); err != nil {
		return err
	}
	if prompt != "/" {
		return &BackendError{Kind: ErrBackendLocked, Backend: BackendSecretService, Detail: "item deletion requires interaction"}
	}
	return nil
}

func (c *dbusSecretService) Close() error {
	var sessionErr error
	if c.session != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sessionErr = c.conn.Object(secretServiceName, c.session).CallWithContext(ctx, secretSessionAPI+".Close", 0).Err
	}
	return errors.Join(sessionErr, c.conn.Close())
}
