package botfactory

import (
	"context"
	"errors"
	"testing"

	"github.com/ardasevinc/tele/internal/botapi"
	"github.com/ardasevinc/tele/internal/botstore"
	"github.com/ardasevinc/tele/internal/telegram"
)

type failingSetStore struct {
	*memoryStore
}

func (f failingSetStore) Set(context.Context, string, string, []byte) error {
	return errors.New("keychain unavailable")
}

type operationInventory struct {
	bots   map[string]botstore.Bot
	upsert int
	err    error
}

func (i *operationInventory) Load() (botstore.Inventory, error) {
	bots := make([]botstore.Bot, 0, len(i.bots))
	for _, bot := range i.bots {
		bots = append(bots, bot)
	}
	return botstore.Inventory{Bots: bots}, nil
}

func (i *operationInventory) Resolve(token string) (botstore.Bot, error) {
	bot, ok := i.bots[token]
	if !ok {
		return botstore.Bot{}, errors.New("not found")
	}
	return bot, nil
}

func (i *operationInventory) Upsert(_ context.Context, bot botstore.Bot) (botstore.Bot, error) {
	i.upsert++
	if i.err != nil {
		return botstore.Bot{}, i.err
	}
	i.bots[bot.Username] = bot
	return bot, nil
}

func testOperationInventory() *operationInventory {
	return &operationInventory{bots: map[string]botstore.Bot{
		"FactoryChildBot": {
			Ref: "bot:42", ID: 42, Username: "FactoryChildBot", Name: "Factory Child",
			ManagerID: 7, ManagerUsername: "ManagerBot",
		},
	}}
}

func TestListAndInspectReportTokenPresenceWithoutReturningToken(t *testing.T) {
	store := configuredManagerStore(t)
	if err := StoreManagedBotToken(context.Background(), store, "factory", 42, "42:secret"); err != nil {
		t.Fatal(err)
	}
	inventory := testOperationInventory()
	list, err := List(context.Background(), inventory, store, "factory", "test keychain")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].Token.Stored || list[0].Token.SecretBackend != "test keychain" {
		t.Fatalf("list = %+v", list)
	}
	inspected, err := Inspect(
		context.Background(),
		inventory,
		store,
		"factory",
		"test keychain",
		"FactoryChildBot",
	)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Bot.ID != 42 || !inspected.Token.Stored {
		t.Fatalf("inspect = %+v", inspected)
	}
}

func TestSyncTokenStoresCurrentTokenAndUpdatesReceipt(t *testing.T) {
	store := configuredManagerStore(t)
	inventory := testOperationInventory()
	api := &fakeManagedTokenAPI{token: "42:current-secret"}
	result, err := SyncToken(
		context.Background(),
		inventory,
		store,
		api,
		"factory",
		"test keychain",
		"FactoryChildBot",
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "sync" || !result.Token.Stored ||
		inventory.upsert != 1 || inventory.bots["FactoryChildBot"].TokenSyncedAt.IsZero() {
		t.Fatalf("result=%+v inventory=%+v", result, inventory)
	}
}

func TestRotateTokenClassifiesAmbiguousRemoteResult(t *testing.T) {
	store := configuredManagerStore(t)
	inventory := testOperationInventory()
	api := &fakeManagedTokenAPI{
		err: botapi.AmbiguousResultError{Err: errors.New("connection lost")},
	}
	_, err := RotateToken(
		context.Background(),
		inventory,
		store,
		api,
		"factory",
		"test keychain",
		"FactoryChildBot",
	)
	var mutationErr telegram.MutationError
	if !errors.As(err, &mutationErr) ||
		mutationErr.Outcome != telegram.MutationOutcomeUnknown ||
		mutationErr.RetrySafe {
		t.Fatalf("error = %+v", err)
	}
	if _, ok := store.values["factory:"+ManagedBotTokenSecretKey(42)]; ok {
		t.Fatal("stored a token after an ambiguous rotation")
	}
}

func TestRotateTokenMarksEscrowFailureConfirmed(t *testing.T) {
	baseStore := configuredManagerStore(t)
	store := failingSetStore{memoryStore: baseStore}
	inventory := testOperationInventory()
	api := &fakeManagedTokenAPI{token: "42:replacement-secret"}
	_, err := RotateToken(
		context.Background(),
		inventory,
		store,
		api,
		"factory",
		"test keychain",
		"FactoryChildBot",
	)
	var mutationErr telegram.MutationError
	if !errors.As(err, &mutationErr) ||
		mutationErr.Outcome != telegram.MutationConfirmed ||
		mutationErr.RetrySafe {
		t.Fatalf("error = %+v", err)
	}
}
