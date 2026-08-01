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

func TestRecordReconciliationTombstonesAndReactivatesWithoutDeleting(t *testing.T) {
	store := New(t.TempDir(), "main")
	first := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return first }
	for _, bot := range []Bot{{ID: 1, Username: "PresentBot"}, {ID: 2, Username: "MissingBot"}} {
		if _, err := store.Upsert(context.Background(), bot); err != nil {
			t.Fatal(err)
		}
	}
	inventory, err := store.RecordReconciliation(context.Background(), 7, []int64{1, 3}, []int64{3}, []int64{1})
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Reconciliation == nil || inventory.Reconciliation.Complete || len(inventory.Bots) != 2 {
		t.Fatalf("inventory = %+v", inventory)
	}
	if inventory.Bots[0].ID != 2 || inventory.Bots[0].TombstonedAt == nil ||
		inventory.Bots[1].ID != 1 || inventory.Bots[1].TombstonedAt != nil {
		t.Fatalf("bots = %+v", inventory.Bots)
	}

	second := first.Add(time.Hour)
	store.now = func() time.Time { return second }
	inventory, err = store.RecordReconciliation(context.Background(), 7, []int64{1, 2}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !inventory.Reconciliation.Complete || inventory.Bots[0].TombstonedAt != nil ||
		inventory.Bots[1].TombstonedAt != nil || !inventory.Bots[0].RemoteCheckedAt.Equal(second) {
		t.Fatalf("reactivated inventory = %+v", inventory)
	}
}

func TestRecordReconciliationRejectsAmbiguousCatalog(t *testing.T) {
	store := New(t.TempDir(), "main")
	for _, ids := range [][]int64{{0}, {1, 1}} {
		if _, err := store.RecordReconciliation(context.Background(), 7, ids, nil, nil); err == nil {
			t.Fatalf("accepted remote IDs %v", ids)
		}
	}
	if _, err := store.RecordReconciliation(context.Background(), 0, nil, nil, nil); err == nil {
		t.Fatal("accepted zero manager ID")
	}
}

func TestUpsertInvalidatesReconciliationReceipt(t *testing.T) {
	store := New(t.TempDir(), "main")
	if _, err := store.Upsert(context.Background(), Bot{ID: 1, Username: "ManagedBot"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordReconciliation(context.Background(), 7, []int64{1}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upsert(context.Background(), Bot{ID: 2, Username: "NewBot"}); err != nil {
		t.Fatal(err)
	}
	inventory, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Reconciliation != nil {
		t.Fatalf("stale reconciliation survived inventory mutation: %+v", inventory.Reconciliation)
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
