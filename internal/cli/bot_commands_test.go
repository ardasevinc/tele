package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ardasevinc/tele/internal/botapi"
	"github.com/ardasevinc/tele/internal/secrets"
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
