//go:build darwin && keychainintegration

package secrets

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeKeychainRealLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dataRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instance, err := NewVaultInstance()
	if err != nil {
		t.Fatal(err)
	}
	store, err := InitKeychain(ctx, dataRoot, "integration", instance)
	if err != nil {
		t.Fatal(err)
	}
	largeValue := bytes.Repeat([]byte{0, 1, 2, 255}, 2048)
	if err := store.Set(ctx, "integration", "api-hash", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, "integration", "api-hash", largeValue); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, "integration", "bot:42", []byte{0, 1, 255}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil || len(snapshot) != 2 || !bytes.Equal(snapshot["api-hash"], largeValue) {
		t.Fatalf("Snapshot has %d records and large value match %t: %v", len(snapshot), bytes.Equal(snapshot["api-hash"], largeValue), err)
	}
	diagnostics, err := store.CatalogDiagnostics(ctx)
	if err != nil || diagnostics.Mappings != 2 || diagnostics.PhysicalItems != 2 || diagnostics.Orphans != 0 {
		t.Fatalf("CatalogDiagnostics = %+v, %v", diagnostics, err)
	}
	store.Close()

	reopened, err := OpenKeychain(ctx, dataRoot, "integration", instance)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Delete(ctx, "integration", "api-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Get(ctx, "integration", "api-hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %v", err)
	}
	reopened.Close()
	deleted, err := PurgeKeychain(ctx, dataRoot, "integration", instance)
	if err != nil || deleted != 2 {
		t.Fatalf("PurgeKeychain deleted %d items: %v", deleted, err)
	}
	if _, err := OpenKeychain(ctx, dataRoot, "integration", instance); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open after purge = %v", err)
	}
}
