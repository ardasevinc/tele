//go:build linux && secretserviceintegration

package secrets

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSecretServiceRealProviderLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dataRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instance, err := NewVaultInstance()
	if err != nil {
		t.Fatal(err)
	}
	store, err := InitSecretService(ctx, dataRoot, "integration", instance)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, "integration", "api-hash", []byte("integration-secret")); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, "integration", "bot:42", []byte{0, 1, 255}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil || len(snapshot) != 2 || string(snapshot["api-hash"]) != "integration-secret" {
		t.Fatalf("Snapshot = %#v, %v", snapshot, err)
	}
	diagnostics, err := store.CatalogDiagnostics(ctx)
	if err != nil || diagnostics.Mappings != 2 || diagnostics.PhysicalItems != 2 || diagnostics.Orphans != 0 {
		t.Fatalf("CatalogDiagnostics = %+v, %v", diagnostics, err)
	}
	store.Close()

	reopened, err := OpenSecretService(ctx, dataRoot, "integration", instance)
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
	deleted, err := PurgeSecretService(ctx, dataRoot, "integration", instance)
	if err != nil || deleted != 2 {
		t.Fatalf("PurgeSecretService deleted %d items: %v", deleted, err)
	}
	if _, err := OpenSecretService(ctx, dataRoot, "integration", instance); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open after purge = %v", err)
	}
}
