package telegram

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

var botUsernameRE = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

type botUsernameAPI interface {
	BotsCheckUsername(context.Context, string) (bool, error)
}

type botCreationAPI interface {
	BotsCheckUsername(context.Context, string) (bool, error)
	BotsCreateBot(context.Context, *tg.BotsCreateBotRequest) (tg.UserClass, error)
}

type BotUsernameCheck struct {
	Username  string `json:"username"`
	Available bool   `json:"available"`
	CheckedAt string `json:"checked_at"`
}

type ManagedBotCreateOptions struct {
	Username        string
	Name            string
	ManagerID       int64
	ManagerUsername string
}

type ManagedBot struct {
	ID              int64  `json:"id"`
	AccessHash      int64  `json:"-"`
	Username        string `json:"username"`
	Name            string `json:"name"`
	ManagerID       int64  `json:"manager_id"`
	ManagerUsername string `json:"manager_username"`
}

type botCreationAttempt struct {
	bot        ManagedBot
	handle     string
	dispatched bool
	confirmed  bool
	err        error
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

func (a App) CreateManagedBot(ctx context.Context, options ManagedBotCreateOptions) (ManagedBot, error) {
	username, err := NormalizeBotUsername(options.Username)
	if err != nil {
		return ManagedBot{}, err
	}
	name := strings.TrimSpace(options.Name)
	if name == "" || len([]rune(name)) > 64 {
		return ManagedBot{}, fmt.Errorf("bot name must be 1-64 characters")
	}
	if options.ManagerID == 0 || strings.TrimSpace(options.ManagerUsername) == "" {
		return ManagedBot{}, fmt.Errorf("manager identity is required")
	}

	attempt := botCreationAttempt{handle: "managed-bot:@" + username}
	err = a.Run(ctx, func(ctx context.Context, client *telegram.Client) error {
		input, _, err := a.resolvePeer(ctx, client, options.ManagerUsername)
		if err != nil {
			return err
		}
		manager, ok := input.(*tg.InputPeerUser)
		if !ok || manager.UserID != options.ManagerID {
			return fmt.Errorf("configured manager identity does not match Telegram")
		}
		attempt = createManagedBot(ctx, client.API(), username, name, manager, options.ManagerUsername)
		return attempt.err
	})
	if err == nil {
		return attempt.bot, nil
	}
	if attempt.confirmed {
		return ManagedBot{}, MutationError{
			Outcome:              MutationConfirmed,
			RetrySafe:            false,
			ReconciliationHandle: attempt.handle,
			Err:                  err,
		}
	}
	return ManagedBot{}, mutationFailure(err, attempt.dispatched, attempt.handle)
}

func createManagedBot(
	ctx context.Context,
	api botCreationAPI,
	username, name string,
	manager *tg.InputPeerUser,
	managerUsername string,
) botCreationAttempt {
	attempt := botCreationAttempt{handle: "managed-bot:@" + username}
	available, err := api.BotsCheckUsername(ctx, username)
	if err != nil {
		attempt.err = err
		return attempt
	}
	if !available {
		attempt.err = fmt.Errorf("bot username @%s is unavailable", username)
		return attempt
	}

	attempt.dispatched = true
	created, err := api.BotsCreateBot(ctx, &tg.BotsCreateBotRequest{
		Name:      name,
		Username:  username,
		ManagerID: &tg.InputUser{UserID: manager.UserID, AccessHash: manager.AccessHash},
	})
	if err != nil {
		attempt.err = err
		return attempt
	}
	attempt.confirmed = true

	user, ok := created.(*tg.User)
	if !ok || user.ID == 0 || !user.Bot {
		attempt.err = fmt.Errorf("Telegram confirmed bot creation but returned an invalid bot identity")
		return attempt
	}
	returnedUsername := username
	if value, ok := user.GetUsername(); ok && strings.TrimSpace(value) != "" {
		returnedUsername = value
	}
	if !strings.EqualFold(returnedUsername, username) {
		attempt.err = fmt.Errorf("Telegram confirmed bot creation but returned @%s instead of @%s", returnedUsername, username)
		return attempt
	}
	accessHash, _ := user.GetAccessHash()
	attempt.bot = ManagedBot{
		ID:              user.ID,
		AccessHash:      accessHash,
		Username:        returnedUsername,
		Name:            name,
		ManagerID:       manager.UserID,
		ManagerUsername: strings.TrimPrefix(strings.TrimSpace(managerUsername), "@"),
	}
	return attempt
}
