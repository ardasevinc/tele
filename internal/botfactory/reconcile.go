package botfactory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ardasevinc/tele/internal/botapi"
	"github.com/ardasevinc/tele/internal/botstore"
	"github.com/ardasevinc/tele/internal/secrets"
	"github.com/ardasevinc/tele/internal/telegram"
)

type OwnedBotDiscoverer interface {
	ListOwnedBots(context.Context) ([]telegram.OwnedBot, error)
}

type ReconciliationInventory interface {
	InventoryReadWriter
	RecordReconciliation(context.Context, int64, []int64, []int64, []int64) (botstore.Inventory, error)
}

type ReconcileOptions struct {
	Imports []string
}

type DiscoveredBot struct {
	Ref                        string `json:"ref"`
	ID                         int64  `json:"id"`
	Username                   string `json:"username"`
	Name                       string `json:"name"`
	ManagerID                  int64  `json:"manager_id,omitempty"`
	ManagedByConfiguredManager bool   `json:"managed_by_configured_manager"`
	Importable                 bool   `json:"importable"`
}

type ReconciliationResult struct {
	OK                 bool            `json:"ok"`
	Complete           bool            `json:"complete"`
	Manager            ManagerStatus   `json:"manager"`
	CheckedAt          time.Time       `json:"checked_at"`
	RemoteOwned        []DiscoveredBot `json:"remote_owned"`
	Matched            []BotReceipt    `json:"matched"`
	Tombstoned         []BotReceipt    `json:"tombstoned"`
	Proposed           []DiscoveredBot `json:"proposed"`
	Imported           []BotReceipt    `json:"imported"`
	TokensSynchronized []BotReceipt    `json:"tokens_synchronized"`
	PendingImportIDs   []int64         `json:"pending_import_ids"`
	MissingTokenIDs    []int64         `json:"missing_token_ids"`
}

func Reconcile(
	ctx context.Context,
	secretStore secrets.Store,
	managerAPI botapi.ManagerAPI,
	tokenAPI botapi.ManagedTokenAPI,
	discoverer OwnedBotDiscoverer,
	inventory ReconciliationInventory,
	profile, backend string,
	options ReconcileOptions,
) (ReconciliationResult, error) {
	managerStatus, err := VerifyManager(ctx, secretStore, managerAPI, profile, backend)
	if err != nil {
		return ReconciliationResult{}, err
	}
	if !managerStatus.Configured {
		return ReconciliationResult{}, errors.New("managed bot controller is not configured")
	}
	manager, err := LoadManager(ctx, secretStore, profile)
	if err != nil {
		return ReconciliationResult{}, err
	}
	remoteBots, err := discoverer.ListOwnedBots(ctx)
	if err != nil {
		return ReconciliationResult{}, fmt.Errorf("discover owned bots: %w", err)
	}
	remoteByID, remoteByToken, err := indexRemoteBots(remoteBots)
	if err != nil {
		return ReconciliationResult{}, err
	}
	local, err := inventory.Load()
	if err != nil {
		return ReconciliationResult{}, err
	}
	localByID := make(map[int64]botstore.Bot, len(local.Bots))
	localUsernames := make(map[string]int64, len(local.Bots))
	for _, bot := range local.Bots {
		localByID[bot.ID] = bot
		localUsernames[strings.ToLower(bot.Username)] = bot.ID
		if remote, exists := remoteByID[bot.ID]; exists && bot.ManagerID != 0 && bot.ManagerID != remote.ManagerID {
			return ReconciliationResult{}, fmt.Errorf(
				"local bot @%s records manager %d, but Telegram reports manager %d",
				bot.Username, bot.ManagerID, remote.ManagerID,
			)
		}
	}

	selected, err := selectedImports(options.Imports, remoteByToken, localByID, manager.ID)
	if err != nil {
		return ReconciliationResult{}, err
	}
	for _, remote := range remoteBots {
		if localID, exists := localUsernames[strings.ToLower(remote.Username)]; exists && localID != remote.ID {
			return ReconciliationResult{}, fmt.Errorf(
				"remote bot @%s conflicts with local bot %d using the same username",
				remote.Username, localID,
			)
		}
	}

	result := ReconciliationResult{
		OK:                 true,
		Manager:            managerStatus,
		RemoteOwned:        []DiscoveredBot{},
		Matched:            []BotReceipt{},
		Tombstoned:         []BotReceipt{},
		Proposed:           []DiscoveredBot{},
		Imported:           []BotReceipt{},
		TokensSynchronized: []BotReceipt{},
		PendingImportIDs:   []int64{},
		MissingTokenIDs:    []int64{},
	}
	for _, remote := range remoteBots {
		discovered := publicDiscoveredBot(remote, manager.ID)
		result.RemoteOwned = append(result.RemoteOwned, discovered)
		if _, exists := localByID[remote.ID]; !exists {
			result.Proposed = append(result.Proposed, discovered)
		}
	}

	// Refresh identities already in the inventory. Remote manager data wins only
	// after the conflict preflight above proves it does not contradict custody.
	for _, remote := range remoteBots {
		localBot, exists := localByID[remote.ID]
		if !exists {
			continue
		}
		localBot.AccessHash = remote.AccessHash
		localBot.Username = remote.Username
		localBot.Name = remote.Name
		localBot.ManagerID = remote.ManagerID
		switch remote.ManagerID {
		case manager.ID:
			localBot.ManagerUsername = manager.Username
		case 0:
			localBot.ManagerUsername = ""
		}
		stored, err := inventory.Upsert(ctx, localBot)
		if err != nil {
			return ReconciliationResult{}, fmt.Errorf("refresh local bot @%s: %w", remote.Username, err)
		}
		localByID[remote.ID] = stored
	}

	for _, remote := range selected {
		if _, exists := localByID[remote.ID]; exists {
			continue
		}
		stored, err := inventory.Upsert(ctx, botstore.Bot{
			ID:              remote.ID,
			AccessHash:      remote.AccessHash,
			Username:        remote.Username,
			Name:            remote.Name,
			ManagerID:       manager.ID,
			ManagerUsername: manager.Username,
		})
		if err != nil {
			return ReconciliationResult{}, fmt.Errorf("import @%s inventory receipt: %w", remote.Username, err)
		}
		localByID[remote.ID] = stored
		result.Imported = append(result.Imported, PublicBot(stored))
	}

	// A manager-controlled bot with a durable local receipt can have its current
	// token retrieved without rotating it. The receipt is deliberately written
	// before the secret, mirroring bot creation's crash-safe ordering.
	for _, remote := range remoteBots {
		localBot, exists := localByID[remote.ID]
		if !exists || remote.ManagerID != manager.ID {
			continue
		}
		_, err := LoadManagedBotToken(ctx, secretStore, profile, remote.ID)
		if err == nil {
			continue
		}
		if !errors.Is(err, secrets.ErrNotFound) {
			return ReconciliationResult{}, fmt.Errorf("inspect token for @%s: %w", remote.Username, err)
		}
		token, err := tokenAPI.GetManagedBotToken(ctx, manager.Token, remote.ID)
		if err != nil {
			return ReconciliationResult{}, fmt.Errorf("retrieve token for @%s: %w", remote.Username, err)
		}
		if err := StoreManagedBotToken(ctx, secretStore, profile, remote.ID, token); err != nil {
			return ReconciliationResult{}, fmt.Errorf("store token for @%s: %w", remote.Username, err)
		}
		localBot.TokenSyncedAt = time.Now().UTC()
		stored, err := inventory.Upsert(ctx, localBot)
		if err != nil {
			return ReconciliationResult{}, fmt.Errorf("token stored for @%s, but sync receipt could not be recorded: %w", remote.Username, err)
		}
		localByID[remote.ID] = stored
		result.TokensSynchronized = append(result.TokensSynchronized, PublicBot(stored))
	}

	remoteIDs := make([]int64, 0, len(remoteBots))
	for _, remote := range remoteBots {
		remoteIDs = append(remoteIDs, remote.ID)
		if remote.ManagerID != manager.ID {
			continue
		}
		if _, exists := localByID[remote.ID]; !exists {
			result.PendingImportIDs = append(result.PendingImportIDs, remote.ID)
			continue
		}
		if _, err := LoadManagedBotToken(ctx, secretStore, profile, remote.ID); errors.Is(err, secrets.ErrNotFound) {
			result.MissingTokenIDs = append(result.MissingTokenIDs, remote.ID)
		} else if err != nil {
			return ReconciliationResult{}, fmt.Errorf("verify token for @%s: %w", remote.Username, err)
		}
	}
	storedInventory, err := inventory.RecordReconciliation(
		ctx, manager.ID, remoteIDs, result.PendingImportIDs, result.MissingTokenIDs,
	)
	if err != nil {
		return ReconciliationResult{}, fmt.Errorf("record bot reconciliation: %w", err)
	}
	result.CheckedAt = storedInventory.Reconciliation.CheckedAt
	result.Complete = storedInventory.Reconciliation.Complete
	for _, bot := range storedInventory.Bots {
		if bot.TombstonedAt != nil {
			result.Tombstoned = append(result.Tombstoned, PublicBot(bot))
		} else if _, exists := remoteByID[bot.ID]; exists {
			result.Matched = append(result.Matched, PublicBot(bot))
		}
	}
	sortBotReceipts(result.Imported)
	sortBotReceipts(result.TokensSynchronized)
	return result, nil
}

func indexRemoteBots(bots []telegram.OwnedBot) (map[int64]telegram.OwnedBot, map[string]telegram.OwnedBot, error) {
	byID := make(map[int64]telegram.OwnedBot, len(bots))
	byToken := make(map[string]telegram.OwnedBot, len(bots)*3)
	for _, bot := range bots {
		if bot.ID == 0 || strings.TrimSpace(bot.Username) == "" {
			return nil, nil, errors.New("remote owned-bot catalog contains an invalid identity")
		}
		if _, exists := byID[bot.ID]; exists {
			return nil, nil, fmt.Errorf("remote owned-bot catalog contains duplicate ID %d", bot.ID)
		}
		username := strings.ToLower(strings.TrimSpace(bot.Username))
		if _, exists := byToken[username]; exists {
			return nil, nil, fmt.Errorf("remote owned-bot catalog contains duplicate username @%s", bot.Username)
		}
		byID[bot.ID] = bot
		byToken[username] = bot
		byToken[strconv.FormatInt(bot.ID, 10)] = bot
		byToken[botstore.Ref(bot.ID)] = bot
	}
	return byID, byToken, nil
}

func selectedImports(
	tokens []string,
	remoteByToken map[string]telegram.OwnedBot,
	localByID map[int64]botstore.Bot,
	managerID int64,
) ([]telegram.OwnedBot, error) {
	selected := make([]telegram.OwnedBot, 0, len(tokens))
	seen := make(map[int64]struct{}, len(tokens))
	for _, token := range tokens {
		normalized := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(token), "@"))
		remote, exists := remoteByToken[normalized]
		if !exists {
			return nil, fmt.Errorf("owned bot %q was not found remotely", token)
		}
		if remote.ManagerID != managerID {
			return nil, fmt.Errorf(
				"owned bot @%s is managed by %d, not the configured manager %d",
				remote.Username, remote.ManagerID, managerID,
			)
		}
		if _, duplicate := seen[remote.ID]; duplicate {
			return nil, fmt.Errorf("owned bot @%s was selected for import more than once", remote.Username)
		}
		seen[remote.ID] = struct{}{}
		if _, exists := localByID[remote.ID]; !exists {
			selected = append(selected, remote)
		}
	}
	return selected, nil
}

func publicDiscoveredBot(bot telegram.OwnedBot, managerID int64) DiscoveredBot {
	managed := bot.ManagerID == managerID
	return DiscoveredBot{
		Ref:                        botstore.Ref(bot.ID),
		ID:                         bot.ID,
		Username:                   bot.Username,
		Name:                       bot.Name,
		ManagerID:                  bot.ManagerID,
		ManagedByConfiguredManager: managed,
		Importable:                 managed,
	}
}

func sortBotReceipts(bots []BotReceipt) {
	sort.Slice(bots, func(i, j int) bool {
		return strings.ToLower(bots[i].Username) < strings.ToLower(bots[j].Username)
	})
}
