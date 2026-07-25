package telegram

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
)

var botUsernameRE = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

type botUsernameAPI interface {
	BotsCheckUsername(context.Context, string) (bool, error)
}

type BotUsernameCheck struct {
	Username  string `json:"username"`
	Available bool   `json:"available"`
	CheckedAt string `json:"checked_at"`
}

func NormalizeBotUsername(value string) (string, error) {
	username := strings.TrimPrefix(strings.TrimSpace(value), "@")
	if len(username) < 5 || len(username) > 32 {
		return "", fmt.Errorf("bot username must be 5-32 characters")
	}
	if !botUsernameRE.MatchString(username) {
		return "", fmt.Errorf("bot username must contain only letters, numbers, or underscores")
	}
	if !strings.HasSuffix(strings.ToLower(username), "bot") {
		return "", fmt.Errorf("bot username must end in bot")
	}
	return username, nil
}

func (a App) CheckBotUsername(ctx context.Context, value string) (BotUsernameCheck, error) {
	username, err := NormalizeBotUsername(value)
	if err != nil {
		return BotUsernameCheck{}, err
	}
	var result BotUsernameCheck
	err = a.Run(ctx, func(ctx context.Context, client *telegram.Client) error {
		var err error
		result, err = checkBotUsername(ctx, client.API(), username, time.Now)
		return err
	})
	return result, err
}

func checkBotUsername(
	ctx context.Context,
	api botUsernameAPI,
	username string,
	now func() time.Time,
) (BotUsernameCheck, error) {
	available, err := api.BotsCheckUsername(ctx, username)
	if err != nil {
		return BotUsernameCheck{}, err
	}
	return BotUsernameCheck{
		Username:  username,
		Available: available,
		CheckedAt: now().UTC().Format(time.RFC3339),
	}, nil
}
