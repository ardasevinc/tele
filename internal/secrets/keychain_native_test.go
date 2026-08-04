package secrets

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNativeKeychainCatalogLifecycle(t *testing.T) {
	api := newFakeNativeKeychain()
	store := newNativeKeychainStore(api, secureTempDir(t), "main", testVaultInstance)
	if err := store.initialize(context.Background()); err != nil {
		t.Fatal(err)
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
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{"api-hash": []byte("second"), "bot:42": {0, 1, 255}}
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("Snapshot = %#v", snapshot)
	}
	api.gets, api.exists = 0, 0
	diagnostics, err := store.CatalogDiagnostics(context.Background())
	if err != nil || diagnostics.Mappings != 2 || diagnostics.PhysicalItems != 2 || diagnostics.Orphans != 0 {
		t.Fatalf("CatalogDiagnostics = %+v, %v", diagnostics, err)
	}
	if api.gets != 0 || api.exists != 2 {
		t.Fatalf("catalog API calls: Get=%d Exists=%d, want cached manifest and 2 metadata checks", api.gets, api.exists)
	}
	if err := store.Delete(context.Background(), "main", "api-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "main", "api-hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %v", err)
	}
}

func TestNativeKeychainManifestFailurePreservesMappingAndExposesOrphan(t *testing.T) {
	api := newFakeNativeKeychain()
	store := newNativeKeychainStore(api, secureTempDir(t), "main", testVaultInstance)
	if err := store.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(context.Background(), "main", "key", []byte("old")); err != nil {
		t.Fatal(err)
	}
	api.manifestReplaces = 0
	api.failManifestReplaceAt = 2
	if err := store.Set(context.Background(), "main", "key", []byte("new")); err == nil {
		t.Fatal("manifest failure was ignored")
	}
	api.failManifestReplaceAt = 0
	value, err := store.Get(context.Background(), "main", "key")
	if err != nil || string(value) != "old" {
		t.Fatalf("authoritative value = %q, %v", value, err)
	}
	api.gets, api.exists = 0, 0
	diagnostics, err := store.CatalogDiagnostics(context.Background())
	if err != nil || diagnostics.Mappings != 1 || diagnostics.PhysicalItems != 2 || diagnostics.Orphans != 1 {
		t.Fatalf("CatalogDiagnostics = %+v, %v", diagnostics, err)
	}
	if api.gets != 0 || api.exists != 2 {
		t.Fatalf("catalog API calls: Get=%d Exists=%d, want cached manifest and 2 metadata checks", api.gets, api.exists)
	}
}

func TestNativeKeychainCommandViewLoadsManifestOnce(t *testing.T) {
	ctx := context.Background()
	api := newFakeNativeKeychain()
	seed := newNativeKeychainStore(api, secureTempDir(t), "main", testVaultInstance)
	if err := seed.initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := seed.Set(ctx, "main", "first", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := seed.Set(ctx, "main", "second", []byte("two")); err != nil {
		t.Fatal(err)
	}

	store := newNativeKeychainStore(api, secureTempDir(t), "main", testVaultInstance)
	api.gets, api.exists = 0, 0
	if err := store.open(ctx); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"first", "second"} {
		if _, err := store.Get(ctx, "main", key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CatalogDiagnostics(ctx); err != nil {
		t.Fatal(err)
	}
	if api.gets != 3 || api.exists != 2 {
		t.Fatalf("command view API calls: Get=%d Exists=%d, want 1 manifest Get, 2 value Gets, and 2 metadata checks", api.gets, api.exists)
	}
}

func TestNativeKeychainMutationRefreshesConcurrentManifestAdvance(t *testing.T) {
	ctx := context.Background()
	api := newFakeNativeKeychain()
	dataRoot := secureTempDir(t)
	first := newNativeKeychainStore(api, dataRoot, "main", testVaultInstance)
	if err := first.initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.Set(ctx, "main", "first", []byte("one")); err != nil {
		t.Fatal(err)
	}
	second := newNativeKeychainStore(api, dataRoot, "main", testVaultInstance)
	if err := second.open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.Set(ctx, "main", "advanced", []byte("two")); err != nil {
		t.Fatal(err)
	}
	if err := second.Set(ctx, "main", "later", []byte("three")); err != nil {
		t.Fatal(err)
	}

	verifier := newNativeKeychainStore(api, dataRoot, "main", testVaultInstance)
	if err := verifier.open(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := verifier.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 3 || string(snapshot["first"]) != "one" || string(snapshot["advanced"]) != "two" || string(snapshot["later"]) != "three" {
		t.Fatalf("snapshot after concurrent advance = %#v", snapshot)
	}
}

func TestNativeKeychainPurgeDeletesManifestLast(t *testing.T) {
	api := newFakeNativeKeychain()
	store := newNativeKeychainStore(api, secureTempDir(t), "main", testVaultInstance)
	if err := store.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	api.deleted = nil
	if err := store.Set(context.Background(), "main", "key", []byte("value")); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.purge(context.Background())
	if err != nil || deleted != 2 || len(api.items) != 0 {
		t.Fatalf("purge deleted %d items, %d remain: %v", deleted, len(api.items), err)
	}
	if got := api.deleted; len(got) != 2 || got[1] != store.manifestAccount() {
		t.Fatalf("delete order = %v", got)
	}
}

func TestNativeKeychainProbeFailureDoesNotCreateManifest(t *testing.T) {
	api := newFakeNativeKeychain()
	api.failCreate = true
	store := newNativeKeychainStore(api, secureTempDir(t), "main", testVaultInstance)
	if err := store.initialize(context.Background()); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("initialize error = %v", err)
	}
	if _, ok := api.items[store.manifestAccount()]; ok {
		t.Fatal("failed probe created a manifest")
	}
}

func TestNativeKeychainRejectsDuplicatePhysicalMappings(t *testing.T) {
	store := newNativeKeychainStore(newFakeNativeKeychain(), secureTempDir(t), "main", testVaultInstance)
	physicalID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	manifest := keychainManifest{
		Schema: keychainManifestSchema, Instance: testVaultInstance, Profile: "main", Generation: 1,
		Mappings: map[string]string{"first": physicalID, "second": physicalID}, Garbage: []string{},
	}
	if err := store.validateManifest(manifest); !errors.Is(err, ErrCatalogIncomplete) {
		t.Fatalf("validateManifest error = %v", err)
	}
}

type fakeNativeKeychain struct {
	items                 map[string][]byte
	failCreate            bool
	failManifestReplaceAt int
	manifestReplaces      int
	deleted               []string
	gets                  int
	exists                int
}

func newFakeNativeKeychain() *fakeNativeKeychain {
	return &fakeNativeKeychain{items: map[string][]byte{}}
}

func (f *fakeNativeKeychain) Create(_ context.Context, account string, value []byte) error {
	if f.failCreate {
		return errors.New("injected create failure")
	}
	if _, exists := f.items[account]; exists {
		return errors.New("duplicate item")
	}
	f.items[account] = append([]byte(nil), value...)
	return nil
}

func (f *fakeNativeKeychain) Replace(_ context.Context, account string, value []byte) error {
	if strings.HasSuffix(account, ":manifest") {
		f.manifestReplaces++
		if f.failManifestReplaceAt == f.manifestReplaces {
			return errors.New("injected manifest failure")
		}
	}
	if _, exists := f.items[account]; !exists {
		return ErrNotFound
	}
	f.items[account] = append([]byte(nil), value...)
	return nil
}

func (f *fakeNativeKeychain) Get(_ context.Context, account string) ([]byte, error) {
	f.gets++
	value, exists := f.items[account]
	if !exists {
		return nil, ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (f *fakeNativeKeychain) Exists(_ context.Context, account string) (bool, error) {
	f.exists++
	_, exists := f.items[account]
	return exists, nil
}

func (f *fakeNativeKeychain) Delete(_ context.Context, account string) error {
	if _, exists := f.items[account]; !exists {
		return ErrNotFound
	}
	f.deleted = append(f.deleted, account)
	delete(f.items, account)
	return nil
}
