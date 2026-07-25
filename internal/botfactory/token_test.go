package botfactory

import (
	"context"
	"testing"

	"github.com/ardasevinc/tele/internal/secrets"
)

func TestManagedBotTokenRoundTrip(t *testing.T) {
	store := &memoryStore{values: map[string][]byte{}}
	if err := StoreManagedBotToken(context.Background(), store, "factory", 42, "42:secret"); err != nil {
		t.Fatal(err)
	}
	got, err := LoadManagedBotToken(context.Background(), store, "factory", 42)
	if err != nil {
		t.Fatal(err)
	}
	if got != "42:secret" {
		t.Fatal("stored token mismatch")
	}
	if _, ok := store.values["factory:"+ManagedBotTokenSecretKey(42)]; !ok {
		t.Fatal("token was not stored under the bot-scoped key")
	}
}

func TestManagedBotTokenRejectsInvalidValues(t *testing.T) {
	store := &memoryStore{values: map[string][]byte{}}
	if err := StoreManagedBotToken(context.Background(), store, "factory", 0, "42:secret"); err == nil {
		t.Fatal("accepted an empty bot ID")
	}
	if err := StoreManagedBotToken(context.Background(), store, "factory", 42, " "); err == nil {
		t.Fatal("accepted an empty token")
	}
	if _, err := LoadManagedBotToken(context.Background(), store, "factory", 42); err != secrets.ErrNotFound {
		t.Fatalf("load error = %v, want ErrNotFound", err)
	}
}
