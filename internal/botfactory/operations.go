package botfactory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ardasevinc/tele/internal/botapi"
	"github.com/ardasevinc/tele/internal/botstore"
	"github.com/ardasevinc/tele/internal/secrets"
	"github.com/ardasevinc/tele/internal/telegram"
)

type InventoryReader interface {
	Load() (botstore.Inventory, error)
	Resolve(string) (botstore.Bot, error)
}

type InventoryReadWriter interface {
	InventoryReader
	InventoryStore
}

type TokenStatus struct {
	Stored        bool   `json:"stored"`
	SecretBackend string `json:"secret_backend,omitempty"`
}

type ManagedBotStatus struct {
	Bot   BotReceipt  `json:"bot"`
	Token TokenStatus `json:"token"`
}

type TokenOperationResult struct {
	OK                   bool                     `json:"ok"`
	Action               string                   `json:"action"`
	Outcome              telegram.MutationOutcome `json:"outcome"`
	RetrySafe            bool                     `json:"retry_safe"`
	Bot                  BotReceipt               `json:"bot"`
	Token                TokenStatus              `json:"token"`
	ReconciliationHandle string                   `json:"reconciliation_handle"`
	Timestamp            string                   `json:"timestamp"`
}

func List(
	ctx context.Context,
	inventory InventoryReader,
	secretStore secrets.Store,
	profile, backend string,
) ([]ManagedBotStatus, error) {
	stored, err := inventory.Load()
	if err != nil {
		return nil, err
	}
	statuses := make([]ManagedBotStatus, 0, len(stored.Bots))
	for _, bot := range stored.Bots {
		status, err := managedBotStatus(ctx, secretStore, profile, backend, bot)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func Inspect(
	ctx context.Context,
	inventory InventoryReader,
	secretStore secrets.Store,
	profile, backend, token string,
) (ManagedBotStatus, error) {
	bot, err := inventory.Resolve(token)
	if err != nil {
		return ManagedBotStatus{}, err
	}
	return managedBotStatus(ctx, secretStore, profile, backend, bot)
}

func SyncToken(
	ctx context.Context,
	inventory InventoryReadWriter,
	secretStore secrets.Store,
	api botapi.ManagedTokenAPI,
	profile, backend, botToken string,
) (TokenOperationResult, error) {
	return updateToken(ctx, inventory, secretStore, api, profile, backend, botToken, false)
}

func RotateToken(
	ctx context.Context,
	inventory InventoryReadWriter,
	secretStore secrets.Store,
	api botapi.ManagedTokenAPI,
	profile, backend, botToken string,
) (TokenOperationResult, error) {
	return updateToken(ctx, inventory, secretStore, api, profile, backend, botToken, true)
}

func updateToken(
	ctx context.Context,
	inventory InventoryReadWriter,
	secretStore secrets.Store,
	api botapi.ManagedTokenAPI,
	profile, backend, botToken string,
	rotate bool,
) (TokenOperationResult, error) {
	bot, err := inventory.Resolve(botToken)
	if err != nil {
		return TokenOperationResult{}, err
	}
	manager, err := LoadManager(ctx, secretStore, profile)
	if err != nil {
		return TokenOperationResult{}, fmt.Errorf("managed bot controller is not configured: %w", err)
	}
	if bot.ManagerID != manager.ID ||
		!strings.EqualFold(bot.ManagerUsername, manager.Username) {
		return TokenOperationResult{}, fmt.Errorf(
			"local bot @%s belongs to manager @%s, not the configured manager @%s",
			bot.Username,
			bot.ManagerUsername,
			manager.Username,
		)
	}

	action := "sync"
	handle := "managed-bot-token:@" + bot.Username
	var token string
	if rotate {
		action = "rotate"
		token, err = api.ReplaceManagedBotToken(ctx, manager.Token, bot.ID)
	} else {
		token, err = api.GetManagedBotToken(ctx, manager.Token, bot.ID)
	}
	if err != nil {
		if !rotate {
			return TokenOperationResult{}, err
		}
		var ambiguous botapi.AmbiguousResultError
		if errors.As(err, &ambiguous) {
			return TokenOperationResult{}, telegram.MutationError{
				Outcome:              telegram.MutationOutcomeUnknown,
				RetrySafe:            false,
				ReconciliationHandle: handle,
				Err:                  err,
			}
		}
		return TokenOperationResult{}, telegram.MutationError{
			Outcome:              telegram.MutationRejected,
			RetrySafe:            true,
			ReconciliationHandle: handle,
			Err:                  err,
		}
	}
	if err := StoreManagedBotToken(ctx, secretStore, profile, bot.ID, token); err != nil {
		if rotate {
			return TokenOperationResult{}, confirmedCreationError(handle, fmt.Errorf(
				"telegram rotated the token for @%s, but local token escrow failed: %w",
				bot.Username,
				err,
			))
		}
		return TokenOperationResult{}, err
	}
	bot.TokenSyncedAt = time.Now().UTC()
	bot, err = inventory.Upsert(ctx, bot)
	if err != nil {
		if rotate {
			return TokenOperationResult{}, confirmedCreationError(handle, fmt.Errorf(
				"telegram rotated and tele securely stored the token for @%s, but the sync receipt could not be recorded: %w",
				bot.Username,
				err,
			))
		}
		return TokenOperationResult{}, fmt.Errorf("token stored, but sync receipt could not be recorded: %w", err)
	}
	return TokenOperationResult{
		OK:        true,
		Action:    action,
		Outcome:   telegram.MutationConfirmed,
		RetrySafe: false,
		Bot:       PublicBot(bot),
		Token: TokenStatus{
			Stored:        true,
			SecretBackend: backend,
		},
		ReconciliationHandle: handle,
		Timestamp:            time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func managedBotStatus(
	ctx context.Context,
	secretStore secrets.Store,
	profile, backend string,
	bot botstore.Bot,
) (ManagedBotStatus, error) {
	_, err := LoadManagedBotToken(ctx, secretStore, profile, bot.ID)
	if err != nil && !errors.Is(err, secrets.ErrNotFound) {
		return ManagedBotStatus{}, err
	}
	stored := err == nil
	status := ManagedBotStatus{
		Bot: PublicBot(bot),
		Token: TokenStatus{
			Stored: stored,
		},
	}
	if stored {
		status.Token.SecretBackend = backend
	}
	return status, nil
}
