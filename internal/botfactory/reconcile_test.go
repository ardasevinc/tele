package botfactory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ardasevinc/tele/internal/botapi"
	"github.com/ardasevinc/tele/internal/botstore"
	"github.com/ardasevinc/tele/internal/secrets"
	"github.com/ardasevinc/tele/internal/telegram"
)

type fakeOwnedBotDiscoverer struct {
	bots  []telegram.OwnedBot
	err   error
	calls int
}

func (f *fakeOwnedBotDiscoverer) ListOwnedBots(context.Context) ([]telegram.OwnedBot, error) {
	f.calls++
	return append([]telegram.OwnedBot(nil), f.bots...), f.err
}

func reconciliationManagerAPI() fakeManagerAPI {
	return fakeManagerAPI{bot: botapi.Bot{
		ID: 7, IsBot: true, Username: "ManagerBot", CanManageBots: true,
	}}
}

func TestReconcileProposesImportsAndTombstonesLocalOnlyBots(t *testing.T) {
	ctx := context.Background()
	secretStore := configuredManagerStore(t)
	inventory := botstore.New(t.TempDir(), "factory")
	if _, err := inventory.Upsert(ctx, botstore.Bot{
		ID: 77, Username: "GoneBot", Name: "Gone", ManagerID: 7, ManagerUsername: "ManagerBot",
	}); err != nil {
		t.Fatal(err)
	}
	discoverer := &fakeOwnedBotDiscoverer{bots: []telegram.OwnedBot{
		{ID: 42, AccessHash: 420, Username: "ManagedBot", Name: "Managed", ManagerID: 7},
		{ID: 99, AccessHash: 990, Username: "OtherBot", Name: "Other", ManagerID: 8},
	}}
	tokenAPI := &fakeManagedTokenAPI{token: "42:secret"}
	result, err := Reconcile(
		ctx, secretStore, reconciliationManagerAPI(), tokenAPI, discoverer, inventory,
		"factory", "test keychain", ReconcileOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || len(result.Proposed) != 2 || len(result.PendingImportIDs) != 1 ||
		result.PendingImportIDs[0] != 42 || len(result.Tombstoned) != 1 || result.Tombstoned[0].ID != 77 {
		t.Fatalf("result = %+v", result)
	}
	if tokenAPI.botID != 0 {
		t.Fatal("reconciliation retrieved a token without importing the remote bot")
	}
	if _, err := inventory.Resolve("ManagedBot"); err == nil {
		t.Fatal("proposed remote bot was silently added")
	}
	stored, err := inventory.Load()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Reconciliation == nil || stored.Reconciliation.Complete || stored.Bots[0].TombstonedAt == nil {
		t.Fatalf("stored inventory = %+v", stored)
	}
}

func TestReconcileExplicitImportEscrowsTokenAndCompletesCatalog(t *testing.T) {
	ctx := context.Background()
	secretStore := configuredManagerStore(t)
	inventory := botstore.New(t.TempDir(), "factory")
	discoverer := &fakeOwnedBotDiscoverer{bots: []telegram.OwnedBot{
		{ID: 42, AccessHash: 420, Username: "ManagedBot", Name: "Managed", ManagerID: 7},
		{ID: 99, AccessHash: 990, Username: "OtherBot", Name: "Other", ManagerID: 8},
	}}
	tokenAPI := &fakeManagedTokenAPI{token: "42:child-secret"}
	result, err := Reconcile(
		ctx, secretStore, reconciliationManagerAPI(), tokenAPI, discoverer, inventory,
		"factory", "test keychain", ReconcileOptions{Imports: []string{"@managedbot"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || len(result.Imported) != 1 || result.Imported[0].ID != 42 ||
		len(result.TokensSynchronized) != 1 || len(result.PendingImportIDs) != 0 || len(result.MissingTokenIDs) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if tokenAPI.managerToken != "7:manager-secret" || tokenAPI.botID != 42 {
		t.Fatalf("token request manager_match=%t bot=%d", tokenAPI.managerToken == "7:manager-secret", tokenAPI.botID)
	}
	value, err := LoadManagedBotToken(ctx, secretStore, "factory", 42)
	if err != nil || value != "42:child-secret" {
		t.Fatalf("stored token match=%t error=%v", value == "42:child-secret", err)
	}
	if _, err := inventory.Resolve("OtherBot"); err == nil {
		t.Fatal("bot controlled by another manager was fabricated locally")
	}
}

func TestReconcileRepairsMissingTokenForExistingManagedBot(t *testing.T) {
	ctx := context.Background()
	secretStore := configuredManagerStore(t)
	inventory := botstore.New(t.TempDir(), "factory")
	if _, err := inventory.Upsert(ctx, botstore.Bot{
		ID: 42, Username: "ManagedBot", Name: "Old name", ManagerID: 7, ManagerUsername: "ManagerBot",
	}); err != nil {
		t.Fatal(err)
	}
	discoverer := &fakeOwnedBotDiscoverer{bots: []telegram.OwnedBot{
		{ID: 42, AccessHash: 420, Username: "ManagedBot", Name: "Current name", ManagerID: 7},
	}}
	result, err := Reconcile(
		ctx, secretStore, reconciliationManagerAPI(), &fakeManagedTokenAPI{token: "42:current"}, discoverer, inventory,
		"factory", "test keychain", ReconcileOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || len(result.TokensSynchronized) != 1 || result.Matched[0].Name != "Current name" {
		t.Fatalf("result = %+v", result)
	}
}

func TestReconcileRejectsUnmanagedImportBeforeMutation(t *testing.T) {
	ctx := context.Background()
	secretStore := configuredManagerStore(t)
	inventory := botstore.New(t.TempDir(), "factory")
	discoverer := &fakeOwnedBotDiscoverer{bots: []telegram.OwnedBot{
		{ID: 99, AccessHash: 990, Username: "OtherBot", Name: "Other", ManagerID: 8},
	}}
	_, err := Reconcile(
		ctx, secretStore, reconciliationManagerAPI(), &fakeManagedTokenAPI{token: "secret"}, discoverer, inventory,
		"factory", "test keychain", ReconcileOptions{Imports: []string{"OtherBot"}},
	)
	if err == nil || !strings.Contains(err.Error(), "not the configured manager") {
		t.Fatalf("error = %v", err)
	}
	stored, loadErr := inventory.Load()
	if loadErr != nil || len(stored.Bots) != 0 || stored.Reconciliation != nil {
		t.Fatalf("inventory=%+v error=%v", stored, loadErr)
	}
}

func TestReconcileKeepsImportReceiptWhenTokenRetrievalFails(t *testing.T) {
	ctx := context.Background()
	secretStore := configuredManagerStore(t)
	inventory := botstore.New(t.TempDir(), "factory")
	discoverer := &fakeOwnedBotDiscoverer{bots: []telegram.OwnedBot{
		{ID: 42, AccessHash: 420, Username: "ManagedBot", Name: "Managed", ManagerID: 7},
	}}
	_, err := Reconcile(
		ctx, secretStore, reconciliationManagerAPI(), &fakeManagedTokenAPI{err: errors.New("offline")}, discoverer, inventory,
		"factory", "test keychain", ReconcileOptions{Imports: []string{"ManagedBot"}},
	)
	if err == nil || !strings.Contains(err.Error(), "retrieve token") {
		t.Fatalf("error = %v", err)
	}
	bot, resolveErr := inventory.Resolve("ManagedBot")
	if resolveErr != nil || bot.ID != 42 || !bot.TokenSyncedAt.IsZero() {
		t.Fatalf("bot=%+v error=%v", bot, resolveErr)
	}
	if _, getErr := secretStore.Get(ctx, "factory", ManagedBotTokenSecretKey(42)); !errors.Is(getErr, secrets.ErrNotFound) {
		t.Fatalf("token error = %v", getErr)
	}
}
