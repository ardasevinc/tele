package botfactory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ardasevinc/tele/internal/botapi"
	"github.com/ardasevinc/tele/internal/secrets"
)

type memoryStore struct {
	values map[string][]byte
	err    error
}

func (s *memoryStore) Get(_ context.Context, profile, key string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	value, ok := s.values[profile+":"+key]
	if !ok {
		return nil, secrets.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (s *memoryStore) Set(_ context.Context, profile, key string, value []byte) error {
	if s.err != nil {
		return s.err
	}
	s.values[profile+":"+key] = append([]byte(nil), value...)
	return nil
}

func (s *memoryStore) Delete(context.Context, string, string) error { return nil }

type fakeManagerAPI struct {
	bot botapi.Bot
	err error
}

func (f fakeManagerAPI) GetMe(context.Context, string) (botapi.Bot, error) {
	return f.bot, f.err
}

func TestConfigureAndVerifyManager(t *testing.T) {
	store := &memoryStore{values: map[string][]byte{}}
	api := fakeManagerAPI{bot: botapi.Bot{
		ID: 42, IsBot: true, Username: "ManagerBot", CanManageBots: true,
	}}
	status, err := ConfigureManager(context.Background(), store, api, "main", "@managerbot", "secret", "test keychain")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Configured || !status.Verified || !status.TokenStored || status.Username != "ManagerBot" {
		t.Fatalf("status = %+v", status)
	}
	encoded := string(store.values["main:"+ManagerSecretKey])
	if !strings.Contains(encoded, "secret") {
		t.Fatal("credential was not stored")
	}

	verified, err := VerifyManager(context.Background(), store, api, "main", "test keychain")
	if err != nil {
		t.Fatal(err)
	}
	if verified != status {
		t.Fatalf("verified = %+v, want %+v", verified, status)
	}
}

func TestConfigureManagerRejectsWrongIdentityAndCapability(t *testing.T) {
	tests := []struct {
		name string
		bot  botapi.Bot
		want string
	}{
		{name: "not bot", bot: botapi.Bot{ID: 1, Username: "ManagerBot"}, want: "does not belong to a bot"},
		{name: "wrong identity", bot: botapi.Bot{ID: 1, IsBot: true, Username: "OtherBot", CanManageBots: true}, want: "belongs to @OtherBot"},
		{name: "capability", bot: botapi.Bot{ID: 1, IsBot: true, Username: "ManagerBot"}, want: "Bot Management Mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memoryStore{values: map[string][]byte{}}
			_, err := ConfigureManager(context.Background(), store, fakeManagerAPI{bot: tt.bot}, "main", "ManagerBot", "secret", "test")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v", err)
			}
			if len(store.values) != 0 {
				t.Fatal("rejected credential was stored")
			}
		})
	}
}

func TestVerifyManagerReportsMissingAndInvalidCredentials(t *testing.T) {
	store := &memoryStore{values: map[string][]byte{}}
	status, err := VerifyManager(context.Background(), store, fakeManagerAPI{}, "main", "test")
	if err != nil {
		t.Fatal(err)
	}
	if status.Configured || status.TokenStored {
		t.Fatalf("missing status = %+v", status)
	}

	store.values["main:"+ManagerSecretKey] = []byte(`{"id":1}`)
	if _, err := VerifyManager(context.Background(), store, fakeManagerAPI{}, "main", "test"); err == nil {
		t.Fatal("invalid credential was accepted")
	}
}

func TestConfigureManagerDoesNotStoreAfterFailures(t *testing.T) {
	store := &memoryStore{values: map[string][]byte{}, err: errors.New("store failed")}
	_, err := ConfigureManager(context.Background(), store, fakeManagerAPI{bot: botapi.Bot{
		ID: 1, IsBot: true, Username: "ManagerBot", CanManageBots: true,
	}}, "main", "ManagerBot", "secret", "test")
	if err == nil || !strings.Contains(err.Error(), "could not be stored") {
		t.Fatalf("error = %v", err)
	}
}
