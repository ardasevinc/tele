package botfactory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ardasevinc/tele/internal/botapi"
	"github.com/ardasevinc/tele/internal/botstore"
	"github.com/ardasevinc/tele/internal/telegram"
)

type fakeManagedBotCreator struct {
	bot     telegram.ManagedBot
	err     error
	options telegram.ManagedBotCreateOptions
}

func (f *fakeManagedBotCreator) CreateManagedBot(
	_ context.Context,
	options telegram.ManagedBotCreateOptions,
) (telegram.ManagedBot, error) {
	f.options = options
	return f.bot, f.err
}

type fakeManagedTokenAPI struct {
	managerToken string
	botID        int64
	token        string
	err          error
}

func (f *fakeManagedTokenAPI) GetManagedBotToken(
	_ context.Context,
	managerToken string,
	botID int64,
) (string, error) {
	f.managerToken = managerToken
	f.botID = botID
	return f.token, f.err
}

type fakeInventory struct {
	bot    botstore.Bot
	upsert int
	errAt  int
}

func (f *fakeInventory) Upsert(_ context.Context, bot botstore.Bot) (botstore.Bot, error) {
	f.upsert++
	if f.errAt == f.upsert {
		return botstore.Bot{}, errors.New("inventory unavailable")
	}
	now := time.Date(2026, 7, 25, 12, 0, f.upsert, 0, time.UTC)
	if bot.Ref == "" {
		bot.Ref = botstore.Ref(bot.ID)
	}
	if bot.CreatedAt.IsZero() {
		bot.CreatedAt = now
	}
	bot.UpdatedAt = now
	f.bot = bot
	return bot, nil
}

func configuredManagerStore(t *testing.T) *memoryStore {
	t.Helper()
	store := &memoryStore{values: map[string][]byte{}}
	api := fakeManagerAPI{bot: botapi.Bot{
		ID: 7, IsBot: true, Username: "ManagerBot", CanManageBots: true,
	}}
	if _, err := ConfigureManager(
		context.Background(),
		store,
		api,
		"factory",
		"ManagerBot",
		"7:manager-secret",
		"test keychain",
	); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestCreateStoresReceiptBeforeEscrowingToken(t *testing.T) {
	store := configuredManagerStore(t)
	creator := &fakeManagedBotCreator{bot: telegram.ManagedBot{
		ID: 42, AccessHash: 99, Username: "FactoryChildBot", Name: "Factory Child",
		ManagerID: 7, ManagerUsername: "ManagerBot",
	}}
	tokenAPI := &fakeManagedTokenAPI{token: "42:child-secret"}
	inventory := &fakeInventory{}

	result, err := Create(
		context.Background(),
		store,
		tokenAPI,
		creator,
		inventory,
		"factory",
		"FactoryChildBot",
		"Factory Child",
		"test keychain",
	)
	if err != nil {
		t.Fatal(err)
	}
	if creator.options.ManagerID != 7 || creator.options.ManagerUsername != "ManagerBot" {
		t.Fatalf("create options = %+v", creator.options)
	}
	if tokenAPI.managerToken != "7:manager-secret" || tokenAPI.botID != 42 {
		t.Fatalf("token request manager_match=%t bot_id=%d", tokenAPI.managerToken == "7:manager-secret", tokenAPI.botID)
	}
	if inventory.upsert != 2 || inventory.bot.TokenSyncedAt.IsZero() {
		t.Fatalf("inventory = %+v upserts=%d", inventory.bot, inventory.upsert)
	}
	if result.Bot.Ref != "bot:42" || !result.Token.Stored ||
		result.Token.SecretBackend != "test keychain" ||
		result.Outcome != telegram.MutationConfirmed {
		t.Fatalf("result = %+v", result)
	}
	if _, ok := store.values["factory:"+ManagedBotTokenSecretKey(42)]; !ok {
		t.Fatal("child token was not escrowed")
	}
}

func TestCreateStopsBeforeTokenRetrievalWhenInventoryFails(t *testing.T) {
	store := configuredManagerStore(t)
	creator := &fakeManagedBotCreator{bot: telegram.ManagedBot{
		ID: 42, Username: "FactoryChildBot", Name: "Factory Child",
		ManagerID: 7, ManagerUsername: "ManagerBot",
	}}
	tokenAPI := &fakeManagedTokenAPI{token: "42:child-secret"}
	inventory := &fakeInventory{errAt: 1}

	_, err := Create(
		context.Background(),
		store,
		tokenAPI,
		creator,
		inventory,
		"factory",
		"FactoryChildBot",
		"Factory Child",
		"test keychain",
	)
	if err == nil {
		t.Fatal("Create succeeded")
	}
	if tokenAPI.botID != 0 {
		t.Fatal("token retrieval ran before a durable inventory receipt existed")
	}
	var mutationErr telegram.MutationError
	if !errors.As(err, &mutationErr) ||
		mutationErr.Outcome != telegram.MutationConfirmed ||
		mutationErr.RetrySafe ||
		mutationErr.ReconciliationHandle != "managed-bot:@FactoryChildBot" {
		t.Fatalf("error = %+v", err)
	}
}

func TestCreateKeepsInventoryWhenTokenRetrievalFails(t *testing.T) {
	store := configuredManagerStore(t)
	creator := &fakeManagedBotCreator{bot: telegram.ManagedBot{
		ID: 42, Username: "FactoryChildBot", Name: "Factory Child",
		ManagerID: 7, ManagerUsername: "ManagerBot",
	}}
	tokenAPI := &fakeManagedTokenAPI{err: errors.New("token endpoint unavailable")}
	inventory := &fakeInventory{}

	_, err := Create(
		context.Background(),
		store,
		tokenAPI,
		creator,
		inventory,
		"factory",
		"FactoryChildBot",
		"Factory Child",
		"test keychain",
	)
	if err == nil || inventory.upsert != 1 {
		t.Fatalf("error=%v upserts=%d", err, inventory.upsert)
	}
	if !strings.Contains(err.Error(), "stored its inventory receipt") {
		t.Fatalf("error = %q", err)
	}
	if _, ok := store.values["factory:"+ManagedBotTokenSecretKey(42)]; ok {
		t.Fatal("stored a missing token")
	}
}
