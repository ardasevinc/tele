package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ardasevinc/tele/internal/botapi"
	"github.com/ardasevinc/tele/internal/botfactory"
	"github.com/ardasevinc/tele/internal/botstore"
	"github.com/ardasevinc/tele/internal/secrets"
	"github.com/ardasevinc/tele/internal/telegram"
)

type botCommandStore struct {
	values map[string][]byte
}

func (s *botCommandStore) Get(_ context.Context, profile, key string) ([]byte, error) {
	value, ok := s.values[profile+":"+key]
	if !ok {
		return nil, secrets.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (s *botCommandStore) Set(_ context.Context, profile, key string, value []byte) error {
	s.values[profile+":"+key] = append([]byte(nil), value...)
	return nil
}

func (s *botCommandStore) Delete(_ context.Context, profile, key string) error {
	delete(s.values, profile+":"+key)
	return nil
}

type botCommandAPI struct {
	bot   botapi.Bot
	token string
	calls int
}

func (a *botCommandAPI) GetMe(_ context.Context, token string) (botapi.Bot, error) {
	a.token = token
	a.calls++
	return a.bot, nil
}

func TestBotManagerConfigureStoresTokenWithoutEmittingIt(t *testing.T) {
	const token = "123:command-secret"
	var stdout, stderr bytes.Buffer
	store := &botCommandStore{values: map[string][]byte{}}
	api := &botCommandAPI{bot: botapi.Bot{
		ID: 123, IsBot: true, Username: "ManagerBot", CanManageBots: true,
	}}
	state := &appState{
		in:                      strings.NewReader(token + "\n"),
		out:                     &stdout,
		err:                     &stderr,
		secretStore:             store,
		secretBackend:           "test keychain",
		secretBackendSupported:  true,
		secretBackendConfigured: true,
		managerAPI:              api,
	}
	err := executeWithState(context.Background(), []string{
		"--json",
		"--config", t.TempDir() + "/config.toml",
		"--profile", "factory",
		"bots", "manager", "configure", "@managerbot", "--token-stdin",
	}, state)
	if err != nil {
		t.Fatal(err)
	}
	if api.calls != 1 || api.token != token {
		t.Fatalf("manager API calls=%d token_match=%t", api.calls, api.token == token)
	}
	if strings.Contains(stdout.String(), token) || strings.Contains(stderr.String(), token) {
		t.Fatalf("command leaked token: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("--token-stdin emitted a prompt: %q", stderr.String())
	}
	if !strings.Contains(string(store.values["factory:bot-manager"]), token) {
		t.Fatal("manager credential was not stored in the selected profile")
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data["configured"] != true || envelope.Data["token_stored"] != true {
		t.Fatalf("data = %#v", envelope.Data)
	}
}

func TestBotManagerConfigureRejectsAmbiguousAndLiteralTokens(t *testing.T) {
	tests := [][]string{
		{"bots", "manager", "configure", "@ManagerBot", "literal-token"},
		{"bots", "manager", "configure", "@ManagerBot", "--token-env", "TOKEN", "--token-stdin"},
	}
	for _, args := range tests {
		state := &appState{in: strings.NewReader(""), out: &bytes.Buffer{}, err: &bytes.Buffer{}}
		if err := executeWithState(context.Background(), args, state); err == nil {
			t.Fatalf("accepted args %v", args)
		}
	}
}

func TestBotManagerStatusReportsMissingConfigurationWithoutCallingTelegram(t *testing.T) {
	var stdout bytes.Buffer
	store := &botCommandStore{values: map[string][]byte{}}
	api := &botCommandAPI{}
	state := &appState{
		in:                      strings.NewReader(""),
		out:                     &stdout,
		err:                     &bytes.Buffer{},
		secretStore:             store,
		secretBackend:           "test keychain",
		secretBackendSupported:  true,
		secretBackendConfigured: true,
		managerAPI:              api,
	}
	if err := executeWithState(context.Background(), []string{
		"--json", "--config", t.TempDir() + "/config.toml", "bots", "manager", "status",
	}, state); err != nil {
		t.Fatal(err)
	}
	if api.calls != 0 {
		t.Fatalf("manager API called %d times", api.calls)
	}
	if !strings.Contains(stdout.String(), `"configured": false`) ||
		!strings.Contains(stdout.String(), `"token_stored": false`) {
		t.Fatalf("status output = %s", stdout.String())
	}
}

type botCommandCreator struct {
	bot     telegram.ManagedBot
	options telegram.ManagedBotCreateOptions
}

func (c *botCommandCreator) CreateManagedBot(
	_ context.Context,
	options telegram.ManagedBotCreateOptions,
) (telegram.ManagedBot, error) {
	c.options = options
	return c.bot, nil
}

type botCommandTokenAPI struct {
	managerToken string
	token        string
	getCalls     int
	replaceCalls int
}

func (a *botCommandTokenAPI) GetManagedBotToken(
	_ context.Context,
	managerToken string,
	_ int64,
) (string, error) {
	a.managerToken = managerToken
	a.getCalls++
	return a.token, nil
}

func (a *botCommandTokenAPI) ReplaceManagedBotToken(
	_ context.Context,
	managerToken string,
	_ int64,
) (string, error) {
	a.managerToken = managerToken
	a.replaceCalls++
	return a.token, nil
}

func TestBotsCreateEscrowsTokenWithoutEmittingIt(t *testing.T) {
	const managerToken = "7:manager-secret"
	const childToken = "42:child-secret"
	var stdout, stderr bytes.Buffer
	store := &botCommandStore{values: map[string][]byte{}}
	managerAPI := &botCommandAPI{bot: botapi.Bot{
		ID: 7, IsBot: true, Username: "ManagerBot", CanManageBots: true,
	}}
	if _, err := botfactory.ConfigureManager(
		context.Background(),
		store,
		managerAPI,
		"default",
		"ManagerBot",
		managerToken,
		"test keychain",
	); err != nil {
		t.Fatal(err)
	}
	creator := &botCommandCreator{bot: telegram.ManagedBot{
		ID: 42, AccessHash: 99, Username: "FactoryChildBot", Name: "Factory Child",
		ManagerID: 7, ManagerUsername: "ManagerBot",
	}}
	tokenAPI := &botCommandTokenAPI{token: childToken}
	inventory := botstore.New(t.TempDir(), "default")
	state := &appState{
		in:                      strings.NewReader(""),
		out:                     &stdout,
		err:                     &stderr,
		secretStore:             store,
		secretBackend:           "test keychain",
		secretBackendSupported:  true,
		secretBackendConfigured: true,
		managedBotCreator:       creator,
		managedTokenAPI:         tokenAPI,
		botInventory:            &inventory,
	}
	if err := executeWithState(context.Background(), []string{
		"--json",
		"--config", t.TempDir() + "/config.toml",
		"bots", "create", "@FactoryChildBot",
		"--name", "Factory Child",
	}, state); err != nil {
		t.Fatal(err)
	}
	output := stdout.String() + stderr.String()
	if strings.Contains(output, managerToken) || strings.Contains(output, childToken) {
		t.Fatalf("command leaked a token: %s", output)
	}
	if tokenAPI.managerToken != managerToken {
		t.Fatal("manager token was not used for child token retrieval")
	}
	if _, ok := store.values["default:"+botfactory.ManagedBotTokenSecretKey(42)]; !ok {
		t.Fatal("child token was not escrowed")
	}
	if got, err := inventory.Resolve("@FactoryChildBot"); err != nil || got.TokenSyncedAt.IsZero() {
		t.Fatalf("inventory bot=%+v error=%v", got, err)
	}
	if !strings.Contains(stdout.String(), `"reconciliation_handle": "managed-bot:@FactoryChildBot"`) {
		t.Fatalf("output = %s", stdout.String())
	}
}

func TestBotsCreateRejectsSafetyModes(t *testing.T) {
	for _, args := range [][]string{
		{"--read-only", "bots", "create", "FactoryChildBot", "--name", "Factory Child"},
		{"--dry-run", "bots", "create", "FactoryChildBot", "--name", "Factory Child"},
	} {
		state := &appState{in: strings.NewReader(""), out: &bytes.Buffer{}, err: &bytes.Buffer{}}
		if err := executeWithState(context.Background(), args, state); err == nil {
			t.Fatalf("accepted args %v", args)
		}
	}
}

func TestBotInventoryAndTokenCommandsNeverEmitTokens(t *testing.T) {
	const managerToken = "7:manager-secret"
	const childToken = "42:child-secret"
	var stdout, stderr bytes.Buffer
	store := &botCommandStore{values: map[string][]byte{}}
	if _, err := botfactory.ConfigureManager(
		context.Background(),
		store,
		&botCommandAPI{bot: botapi.Bot{
			ID: 7, IsBot: true, Username: "ManagerBot", CanManageBots: true,
		}},
		"default",
		"ManagerBot",
		managerToken,
		"test keychain",
	); err != nil {
		t.Fatal(err)
	}
	if err := botfactory.StoreManagedBotToken(
		context.Background(),
		store,
		"default",
		42,
		childToken,
	); err != nil {
		t.Fatal(err)
	}
	inventory := botstore.New(t.TempDir(), "default")
	if _, err := inventory.Upsert(context.Background(), botstore.Bot{
		ID: 42, Username: "FactoryChildBot", Name: "Factory Child",
		ManagerID: 7, ManagerUsername: "ManagerBot",
	}); err != nil {
		t.Fatal(err)
	}
	tokenAPI := &botCommandTokenAPI{token: "42:replacement-secret"}
	state := &appState{
		in:                      strings.NewReader(""),
		out:                     &stdout,
		err:                     &stderr,
		secretStore:             store,
		secretBackend:           "test keychain",
		secretBackendSupported:  true,
		secretBackendConfigured: true,
		managedTokenAPI:         tokenAPI,
		botInventory:            &inventory,
	}
	configPath := t.TempDir() + "/config.toml"
	for _, args := range [][]string{
		{"--json", "--config", configPath, "bots", "list"},
		{"--json", "--config", configPath, "bots", "inspect", "@FactoryChildBot"},
		{"--json", "--config", configPath, "bots", "token", "sync", "@FactoryChildBot"},
		{"--json", "--config", configPath, "bots", "token", "rotate", "@FactoryChildBot", "--yes"},
	} {
		if err := executeWithState(context.Background(), args, state); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	output := stdout.String() + stderr.String()
	for _, token := range []string{managerToken, childToken, tokenAPI.token} {
		if strings.Contains(output, token) {
			t.Fatalf("commands leaked a token: %s", output)
		}
	}
	if tokenAPI.getCalls != 1 || tokenAPI.replaceCalls != 1 {
		t.Fatalf("token calls: get=%d replace=%d", tokenAPI.getCalls, tokenAPI.replaceCalls)
	}
}

func TestBotTokenRotateRequiresExplicitConfirmation(t *testing.T) {
	for _, args := range [][]string{
		{"bots", "token", "rotate", "FactoryChildBot"},
		{"--read-only", "bots", "token", "rotate", "FactoryChildBot", "--yes"},
		{"--dry-run", "bots", "token", "rotate", "FactoryChildBot", "--yes"},
	} {
		state := &appState{in: strings.NewReader(""), out: &bytes.Buffer{}, err: &bytes.Buffer{}}
		if err := executeWithState(context.Background(), args, state); err == nil {
			t.Fatalf("accepted args %v", args)
		}
	}
}
