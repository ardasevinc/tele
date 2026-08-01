package botfactory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ardasevinc/tele/internal/botstore"
	"github.com/ardasevinc/tele/internal/secrets"
	"github.com/ardasevinc/tele/internal/telegram"
)

func TestVerifyLegacyBotCatalogNeedsNoManagerForEmptyInventory(t *testing.T) {
	keys, err := VerifyLegacyBotCatalog(
		context.Background(),
		&memoryStore{values: map[string][]byte{}},
		reconciliationManagerAPI(),
		&fakeOwnedBotDiscoverer{},
		botstore.New(t.TempDir(), "factory"),
		"factory",
		"macOS Keychain",
	)
	if err != nil || len(keys) != 0 {
		t.Fatalf("keys=%v error=%v", keys, err)
	}
}

func TestVerifyLegacyBotCatalogRejectsInventoryWithoutManager(t *testing.T) {
	inventory := botstore.New(t.TempDir(), "factory")
	if _, err := inventory.Upsert(context.Background(), botstore.Bot{ID: 42, Username: "ManagedBot"}); err != nil {
		t.Fatal(err)
	}
	_, err := VerifyLegacyBotCatalog(
		context.Background(),
		&memoryStore{values: map[string][]byte{}},
		reconciliationManagerAPI(),
		&fakeOwnedBotDiscoverer{},
		inventory,
		"factory",
		"macOS Keychain",
	)
	if !errors.Is(err, secrets.ErrCatalogIncomplete) || !strings.Contains(err.Error(), "without a manager") {
		t.Fatalf("error = %v", err)
	}
}

func TestVerifyLegacyBotCatalogChecksLiveRemoteAgreementAndTokenPresence(t *testing.T) {
	ctx := context.Background()
	secretStore := configuredManagerStore(t)
	if err := StoreManagedBotToken(ctx, secretStore, "factory", 42, "42:secret"); err != nil {
		t.Fatal(err)
	}
	inventory := botstore.New(t.TempDir(), "factory")
	if _, err := inventory.Upsert(ctx, botstore.Bot{
		ID: 42, Username: "ManagedBot", Name: "Managed", ManagerID: 7, ManagerUsername: "ManagerBot",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := inventory.RecordReconciliation(ctx, 7, []int64{42}, nil, nil); err != nil {
		t.Fatal(err)
	}
	discoverer := &fakeOwnedBotDiscoverer{bots: []telegram.OwnedBot{
		{ID: 42, AccessHash: 420, Username: "ManagedBot", Name: "Managed", ManagerID: 7},
	}}
	keys, err := VerifyLegacyBotCatalog(
		ctx, secretStore, reconciliationManagerAPI(), discoverer, inventory,
		"factory", "macOS Keychain",
	)
	if err != nil || len(keys) != 1 || keys[0] != "managed-bot-token:42" {
		t.Fatalf("keys=%v error=%v", keys, err)
	}

	discoverer.bots = append(discoverer.bots, telegram.OwnedBot{
		ID: 99, AccessHash: 990, Username: "NewBot", Name: "New", ManagerID: 7,
	})
	_, err = VerifyLegacyBotCatalog(
		ctx, secretStore, reconciliationManagerAPI(), discoverer, inventory,
		"factory", "macOS Keychain",
	)
	if !errors.Is(err, secrets.ErrCatalogIncomplete) || !strings.Contains(err.Error(), "changed after reconciliation") {
		t.Fatalf("drift error = %v", err)
	}
}

func TestVerifyLegacyBotCatalogRejectsMissingManagedToken(t *testing.T) {
	ctx := context.Background()
	secretStore := configuredManagerStore(t)
	inventory := botstore.New(t.TempDir(), "factory")
	if _, err := inventory.Upsert(ctx, botstore.Bot{
		ID: 42, Username: "ManagedBot", Name: "Managed", ManagerID: 7, ManagerUsername: "ManagerBot",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := inventory.RecordReconciliation(ctx, 7, []int64{42}, nil, nil); err != nil {
		t.Fatal(err)
	}
	_, err := VerifyLegacyBotCatalog(
		ctx,
		secretStore,
		reconciliationManagerAPI(),
		&fakeOwnedBotDiscoverer{bots: []telegram.OwnedBot{{
			ID: 42, AccessHash: 420, Username: "ManagedBot", Name: "Managed", ManagerID: 7,
		}}},
		inventory,
		"factory",
		"macOS Keychain",
	)
	if !errors.Is(err, secrets.ErrCatalogIncomplete) || !strings.Contains(err.Error(), "token") {
		t.Fatalf("error = %v", err)
	}
}
