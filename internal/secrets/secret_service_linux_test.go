//go:build linux

package secrets

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestSecretServiceFunctionalProbeAndCatalogLifecycle(t *testing.T) {
	api := newFakeSecretService()
	store := newSecretServiceStore(api, secureTempDir(t), "main", testVaultInstance)
	if err := store.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := api.countKind("sentinel"); got != 0 {
		t.Fatalf("sentinel count = %d, want 0", got)
	}
	if got := api.countKind("manifest"); got != 1 {
		t.Fatalf("manifest count = %d, want 1", got)
	}

	if err := store.Set(context.Background(), "main", "api-hash", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(context.Background(), "main", "api-hash", []byte("second")); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(context.Background(), "main", "bot:42", []byte{0, 1, 255}); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get(context.Background(), "main", "api-hash")
	if err != nil || string(value) != "second" {
		t.Fatalf("Get = %q, %v", value, err)
	}
	if got := api.countKind("physical"); got != 2 {
		t.Fatalf("physical count after replace = %d, want 2", got)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{"api-hash": []byte("second"), "bot:42": {0, 1, 255}}
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("Snapshot = %#v", snapshot)
	}
	if err := store.Delete(context.Background(), "main", "api-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "main", "api-hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %v", err)
	}
	manifest, err := store.loadManifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Generation != 5 || len(manifest.Mappings) != 1 {
		t.Fatalf("manifest = %+v", manifest)
	}
}

func TestSecretServiceManifestFlipPreservesOldMappingOnFailure(t *testing.T) {
	api := newFakeSecretService()
	store := newSecretServiceStore(api, secureTempDir(t), "main", testVaultInstance)
	if err := store.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(context.Background(), "main", "api-hash", []byte("old")); err != nil {
		t.Fatal(err)
	}
	api.failManifestWrite = true
	if err := store.Set(context.Background(), "main", "api-hash", []byte("new")); err == nil {
		t.Fatal("manifest failure was ignored")
	}
	api.failManifestWrite = false
	value, err := store.Get(context.Background(), "main", "api-hash")
	if err != nil || string(value) != "old" {
		t.Fatalf("authoritative value = %q, %v; want old", value, err)
	}
	if got := api.countKind("physical"); got != 2 {
		t.Fatalf("orphan was not retained for later GC: %d physical items", got)
	}
	diagnostics, err := store.CatalogDiagnostics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics.Mappings != 1 || diagnostics.PhysicalItems != 2 || diagnostics.Orphans != 1 {
		t.Fatalf("catalog diagnostics = %+v", diagnostics)
	}
}

func TestSecretServiceStoreUsesProfileWideLockReentrantly(t *testing.T) {
	api := newFakeSecretService()
	dataRoot := secureTempDir(t)
	store := newSecretServiceStore(api, dataRoot, "main", testVaultInstance)
	if err := store.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := WithProfileLock(ctx, dataRoot, "main", func(ctx context.Context) error {
		if err := store.Set(ctx, "main", "key", []byte("value")); err != nil {
			return err
		}
		_, err := store.Snapshot(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(store.lockPath) != "profile.lock" {
		t.Fatalf("lock path = %q", store.lockPath)
	}
}

func TestSecretServicePurgeDeletesManifestLast(t *testing.T) {
	api := newFakeSecretService()
	store := newSecretServiceStore(api, secureTempDir(t), "main", testVaultInstance)
	if err := store.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	api.deletedKinds = nil
	if err := store.Set(context.Background(), "main", "key", []byte("value")); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.purge(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 || len(api.items) != 0 {
		t.Fatalf("purge deleted %d items, %d remain", deleted, len(api.items))
	}
	if got := api.deletedKinds; !reflect.DeepEqual(got, []string{"physical", "manifest"}) {
		t.Fatalf("delete order = %v", got)
	}
}

func TestSecretServiceProbeFailureDoesNotCreateManifest(t *testing.T) {
	api := newFakeSecretService()
	api.failCreateKind = "sentinel"
	store := newSecretServiceStore(api, secureTempDir(t), "main", testVaultInstance)
	if err := store.initialize(context.Background()); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("initialize error = %v, want ErrBackendUnavailable", err)
	}
	if api.countKind("manifest") != 0 {
		t.Fatal("failed probe created a manifest")
	}
}

func TestSecretServiceLockedFailureRemainsTyped(t *testing.T) {
	api := newFakeSecretService()
	api.failCreateKind = "sentinel"
	api.createError = &BackendError{Kind: ErrBackendLocked, Backend: BackendSecretService}
	store := newSecretServiceStore(api, secureTempDir(t), "main", testVaultInstance)
	if err := store.initialize(context.Background()); !errors.Is(err, ErrBackendLocked) {
		t.Fatalf("initialize error = %v, want ErrBackendLocked", err)
	}
}

func TestSecretServiceRejectsMissingSessionBus(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "missing-bus"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := connectSecretService(ctx); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("connectSecretService error = %v, want ErrBackendUnavailable", err)
	}
}

type fakeSecretItem struct {
	attrs map[string]string
	value []byte
}

type fakeSecretService struct {
	items             map[string]fakeSecretItem
	next              int
	failManifestWrite bool
	failCreateKind    string
	createError       error
	deletedKinds      []string
}

func newFakeSecretService() *fakeSecretService {
	return &fakeSecretService{items: map[string]fakeSecretItem{}}
}

func (f *fakeSecretService) CreateItem(_ context.Context, _ string, attrs map[string]string, value []byte, replace bool) (string, error) {
	if f.failCreateKind == attrs["tele-kind"] {
		if f.createError != nil {
			return "", f.createError
		}
		return "", errors.New("injected create failure")
	}
	if f.failManifestWrite && attrs["tele-kind"] == "manifest" {
		return "", errors.New("injected manifest failure")
	}
	if replace {
		for path, item := range f.items {
			if reflect.DeepEqual(item.attrs, attrs) {
				f.items[path] = fakeSecretItem{attrs: cloneStringMap(attrs), value: append([]byte(nil), value...)}
				return path, nil
			}
		}
	}
	f.next++
	path := fmt.Sprintf("/item/%d", f.next)
	f.items[path] = fakeSecretItem{attrs: cloneStringMap(attrs), value: append([]byte(nil), value...)}
	return path, nil
}

func (f *fakeSecretService) SearchItems(_ context.Context, attrs map[string]string) ([]string, error) {
	var paths []string
	for path, item := range f.items {
		matches := true
		for key, value := range attrs {
			if item.attrs[key] != value {
				matches = false
				break
			}
		}
		if matches {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func (f *fakeSecretService) GetSecret(_ context.Context, path string) ([]byte, error) {
	item, ok := f.items[path]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]byte(nil), item.value...), nil
}

func (f *fakeSecretService) GetAttributes(_ context.Context, path string) (map[string]string, error) {
	item, ok := f.items[path]
	if !ok {
		return nil, errors.New("not found")
	}
	return cloneStringMap(item.attrs), nil
}

func (f *fakeSecretService) DeleteItem(_ context.Context, path string) error {
	if item, ok := f.items[path]; ok {
		f.deletedKinds = append(f.deletedKinds, item.attrs["tele-kind"])
	}
	delete(f.items, path)
	return nil
}

func (f *fakeSecretService) Close() error { return nil }

func (f *fakeSecretService) countKind(kind string) int {
	count := 0
	for _, item := range f.items {
		if item.attrs["tele-kind"] == kind {
			count++
		}
	}
	return count
}

func cloneStringMap(value map[string]string) map[string]string {
	clone := make(map[string]string, len(value))
	for key, item := range value {
		clone[key] = item
	}
	return clone
}
