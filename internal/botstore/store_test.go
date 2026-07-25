package botstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreRoundTripAndResolve(t *testing.T) {
	store := New(t.TempDir(), "main")
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	stored, err := store.Upsert(context.Background(), Bot{
		ID:              42,
		AccessHash:      99,
		Username:        "ManagedBot",
		Name:            "Managed",
		ManagerID:       7,
		ManagerUsername: "ManagerBot",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Ref != "bot:42" || !stored.CreatedAt.Equal(now) || !stored.UpdatedAt.Equal(now) {
		t.Fatalf("stored = %+v", stored)
	}
	for _, token := range []string{"@managedbot", "bot:42", "42"} {
		got, err := store.Resolve(token)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != 42 {
			t.Fatalf("Resolve(%q) = %+v", token, got)
		}
	}
}

func TestUpsertPreservesCreationAndTokenSyncTimes(t *testing.T) {
	store := New(t.TempDir(), "main")
	created := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	synced := created.Add(time.Minute)
	store.now = func() time.Time { return created }
	if _, err := store.Upsert(context.Background(), Bot{
		ID: 1, Username: "FirstBot", CreatedAt: created, TokenSyncedAt: synced,
	}); err != nil {
		t.Fatal(err)
	}
	updated := created.Add(time.Hour)
	store.now = func() time.Time { return updated }
	got, err := store.Upsert(context.Background(), Bot{ID: 1, Username: "RenamedBot"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.CreatedAt.Equal(created) || !got.TokenSyncedAt.Equal(synced) || !got.UpdatedAt.Equal(updated) {
		t.Fatalf("timestamps = %+v", got)
	}
}

func TestUpsertMergesConcurrentWriters(t *testing.T) {
	store := New(t.TempDir(), "main")
	const workers = 20
	var wait sync.WaitGroup
	errors := make(chan error, workers)
	for i := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.Upsert(context.Background(), Bot{
				ID: int64(i + 1), Username: fmt.Sprintf("Managed%02dBot", i+1),
			})
			errors <- err
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	inventory, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Bots) != workers {
		t.Fatalf("bots = %d, want %d", len(inventory.Bots), workers)
	}
}

func TestStoreIsPrivateDeterministicAndContainsNoTokenField(t *testing.T) {
	store := New(t.TempDir(), "main")
	if err := store.Save(Inventory{Bots: []Bot{
		{ID: 2, Ref: Ref(2), Username: "ZuluBot"},
		{ID: 1, Ref: Ref(1), Username: "AlphaBot"},
	}}); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(string(encoded), "AlphaBot") > strings.Index(string(encoded), "ZuluBot") {
		t.Fatalf("inventory is not sorted: %s", encoded)
	}
	if strings.Contains(strings.ToLower(string(encoded)), `"token"`) {
		t.Fatalf("inventory contains a token field: %s", encoded)
	}
	for target, want := range map[string]os.FileMode{
		filepath.Dir(store.Path()): 0o700,
		store.Path():               0o600,
	} {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %04o, want %04o", target, got, want)
		}
	}
}

func TestStoreRejectsInvalidAndMissingBots(t *testing.T) {
	store := New(t.TempDir(), "main")
	if _, err := store.Upsert(context.Background(), Bot{}); err == nil {
		t.Fatal("Upsert accepted an invalid bot")
	}
	if _, err := store.Resolve("missing"); err == nil {
		t.Fatal("Resolve found a missing bot")
	}
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("Load accepted a corrupt inventory")
	}
}
