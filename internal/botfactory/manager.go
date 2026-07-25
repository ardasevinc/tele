package botfactory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ardasevinc/tele/internal/botapi"
	"github.com/ardasevinc/tele/internal/secrets"
)

const ManagerSecretKey = "bot-manager"

type ManagerCredential struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

type ManagerStatus struct {
	Configured    bool   `json:"configured"`
	Verified      bool   `json:"verified"`
	ID            int64  `json:"id,omitempty"`
	Username      string `json:"username,omitempty"`
	CanManageBots bool   `json:"can_manage_bots,omitempty"`
	TokenStored   bool   `json:"token_stored"`
	SecretBackend string `json:"secret_backend,omitempty"`
}

func ConfigureManager(
	ctx context.Context,
	store secrets.Store,
	api botapi.ManagerAPI,
	profile, requestedUsername, token, backend string,
) (ManagerStatus, error) {
	if store == nil {
		return ManagerStatus{}, errors.New("secret store is required")
	}
	requestedUsername = normalizeUsername(requestedUsername)
	if requestedUsername == "" {
		return ManagerStatus{}, errors.New("manager username is required")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return ManagerStatus{}, errors.New("manager token is required")
	}
	bot, err := api.GetMe(ctx, token)
	if err != nil {
		return ManagerStatus{}, err
	}
	if !bot.IsBot {
		return ManagerStatus{}, errors.New("manager credential does not belong to a bot")
	}
	if !strings.EqualFold(normalizeUsername(bot.Username), requestedUsername) {
		return ManagerStatus{}, fmt.Errorf("manager credential belongs to @%s, not @%s", bot.Username, requestedUsername)
	}
	if !bot.CanManageBots {
		return ManagerStatus{}, fmt.Errorf("@%s does not have Bot Management Mode enabled", bot.Username)
	}
	credential := ManagerCredential{ID: bot.ID, Username: bot.Username, Token: token}
	encoded, err := json.Marshal(credential)
	if err != nil {
		return ManagerStatus{}, errors.New("manager credential could not be encoded")
	}
	if err := store.Set(ctx, profile, ManagerSecretKey, encoded); err != nil {
		return ManagerStatus{}, fmt.Errorf("manager credential could not be stored: %w", err)
	}
	return managerStatus(credential, backend, true), nil
}

func VerifyManager(
	ctx context.Context,
	store secrets.Store,
	api botapi.ManagerAPI,
	profile, backend string,
) (ManagerStatus, error) {
	credential, err := LoadManager(ctx, store, profile)
	if errors.Is(err, secrets.ErrNotFound) {
		return ManagerStatus{SecretBackend: backend}, nil
	}
	if err != nil {
		return ManagerStatus{}, err
	}
	bot, err := api.GetMe(ctx, credential.Token)
	if err != nil {
		return ManagerStatus{}, err
	}
	if !bot.IsBot || bot.ID != credential.ID || !strings.EqualFold(bot.Username, credential.Username) {
		return ManagerStatus{}, errors.New("stored manager identity no longer matches Telegram")
	}
	if !bot.CanManageBots {
		return ManagerStatus{}, fmt.Errorf("@%s no longer has Bot Management Mode enabled", credential.Username)
	}
	return managerStatus(credential, backend, true), nil
}

func LoadManager(ctx context.Context, store secrets.Store, profile string) (ManagerCredential, error) {
	var credential ManagerCredential
	if store == nil {
		return credential, errors.New("secret store is required")
	}
	encoded, err := store.Get(ctx, profile, ManagerSecretKey)
	if err != nil {
		return credential, err
	}
	if err := json.Unmarshal(encoded, &credential); err != nil ||
		credential.ID == 0 ||
		normalizeUsername(credential.Username) == "" ||
		strings.TrimSpace(credential.Token) == "" {
		return ManagerCredential{}, errors.New("stored manager credential is invalid")
	}
	return credential, nil
}

func managerStatus(credential ManagerCredential, backend string, verified bool) ManagerStatus {
	return ManagerStatus{
		Configured:    true,
		Verified:      verified,
		ID:            credential.ID,
		Username:      credential.Username,
		CanManageBots: verified,
		TokenStored:   true,
		SecretBackend: backend,
	}
}

func normalizeUsername(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "@")
}
