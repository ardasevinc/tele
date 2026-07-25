package botfactory

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ardasevinc/tele/internal/secrets"
)

func ManagedBotTokenSecretKey(botID int64) string {
	return "managed-bot-token:" + strconv.FormatInt(botID, 10)
}

func StoreManagedBotToken(
	ctx context.Context,
	store secrets.Store,
	profile string,
	botID int64,
	token string,
) error {
	if store == nil {
		return errors.New("secret store is required")
	}
	if botID == 0 {
		return errors.New("managed bot ID is required")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("managed bot token is required")
	}
	if err := store.Set(ctx, profile, ManagedBotTokenSecretKey(botID), []byte(token)); err != nil {
		return fmt.Errorf("managed bot token could not be stored: %w", err)
	}
	return nil
}

func LoadManagedBotToken(
	ctx context.Context,
	store secrets.Store,
	profile string,
	botID int64,
) (string, error) {
	if store == nil {
		return "", errors.New("secret store is required")
	}
	if botID == 0 {
		return "", errors.New("managed bot ID is required")
	}
	token, err := store.Get(ctx, profile, ManagedBotTokenSecretKey(botID))
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(token))
	if value == "" {
		return "", errors.New("stored managed bot token is invalid")
	}
	return value, nil
}
