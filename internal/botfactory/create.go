package botfactory

import (
	"context"
	"fmt"
	"time"

	"github.com/ardasevinc/tele/internal/botapi"
	"github.com/ardasevinc/tele/internal/botstore"
	"github.com/ardasevinc/tele/internal/secrets"
	"github.com/ardasevinc/tele/internal/telegram"
)

type ManagedBotCreator interface {
	CreateManagedBot(context.Context, telegram.ManagedBotCreateOptions) (telegram.ManagedBot, error)
}

type InventoryStore interface {
	Upsert(context.Context, botstore.Bot) (botstore.Bot, error)
}

type BotReceipt struct {
	Ref             string    `json:"ref"`
	ID              int64     `json:"id"`
	Username        string    `json:"username"`
	Name            string    `json:"name"`
	ManagerID       int64     `json:"manager_id"`
	ManagerUsername string    `json:"manager_username"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	TokenSyncedAt   time.Time `json:"token_synced_at,omitempty"`
}

type TokenReceipt struct {
	Stored        bool   `json:"stored"`
	SecretBackend string `json:"secret_backend"`
}

type CreationResult struct {
	OK                   bool                     `json:"ok"`
	Outcome              telegram.MutationOutcome `json:"outcome"`
	RetrySafe            bool                     `json:"retry_safe"`
	Bot                  BotReceipt               `json:"bot"`
	Token                TokenReceipt             `json:"token"`
	ReconciliationHandle string                   `json:"reconciliation_handle"`
	Timestamp            string                   `json:"timestamp"`
}

func Create(
	ctx context.Context,
	secretStore secrets.Store,
	tokenAPI botapi.ManagedTokenAPI,
	creator ManagedBotCreator,
	inventory InventoryStore,
	profile, username, name, secretBackend string,
) (CreationResult, error) {
	manager, err := LoadManager(ctx, secretStore, profile)
	if err != nil {
		return CreationResult{}, fmt.Errorf("managed bot controller is not configured: %w", err)
	}
	created, err := creator.CreateManagedBot(ctx, telegram.ManagedBotCreateOptions{
		Username:        username,
		Name:            name,
		ManagerID:       manager.ID,
		ManagerUsername: manager.Username,
	})
	if err != nil {
		return CreationResult{}, err
	}

	handle := "managed-bot:@" + created.Username
	stored, err := inventory.Upsert(ctx, botstore.Bot{
		ID:              created.ID,
		AccessHash:      created.AccessHash,
		Username:        created.Username,
		Name:            created.Name,
		ManagerID:       created.ManagerID,
		ManagerUsername: created.ManagerUsername,
	})
	if err != nil {
		return CreationResult{}, confirmedCreationError(
			handle,
			fmt.Errorf("Telegram created @%s, but its local inventory receipt could not be stored: %w", created.Username, err),
		)
	}

	token, err := tokenAPI.GetManagedBotToken(ctx, manager.Token, created.ID)
	if err != nil {
		return CreationResult{}, confirmedCreationError(
			handle,
			fmt.Errorf("Telegram created @%s and stored its inventory receipt, but its token could not be retrieved: %w", created.Username, err),
		)
	}
	if err := StoreManagedBotToken(ctx, secretStore, profile, created.ID, token); err != nil {
		return CreationResult{}, confirmedCreationError(
			handle,
			fmt.Errorf("Telegram created @%s and stored its inventory receipt, but its token escrow failed: %w", created.Username, err),
		)
	}

	stored.TokenSyncedAt = time.Now().UTC()
	stored, err = inventory.Upsert(ctx, stored)
	if err != nil {
		return CreationResult{}, confirmedCreationError(
			handle,
			fmt.Errorf("Telegram created @%s and securely stored its token, but the token-sync receipt could not be recorded: %w", created.Username, err),
		)
	}
	return CreationResult{
		OK:        true,
		Outcome:   telegram.MutationConfirmed,
		RetrySafe: false,
		Bot:       PublicBot(stored),
		Token: TokenReceipt{
			Stored:        true,
			SecretBackend: secretBackend,
		},
		ReconciliationHandle: handle,
		Timestamp:            time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func PublicBot(bot botstore.Bot) BotReceipt {
	return BotReceipt{
		Ref:             bot.Ref,
		ID:              bot.ID,
		Username:        bot.Username,
		Name:            bot.Name,
		ManagerID:       bot.ManagerID,
		ManagerUsername: bot.ManagerUsername,
		CreatedAt:       bot.CreatedAt,
		UpdatedAt:       bot.UpdatedAt,
		TokenSyncedAt:   bot.TokenSyncedAt,
	}
}

func confirmedCreationError(handle string, err error) error {
	return telegram.MutationError{
		Outcome:              telegram.MutationConfirmed,
		RetrySafe:            false,
		ReconciliationHandle: handle,
		Err:                  err,
	}
}
