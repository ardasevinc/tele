package botfactory

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/ardasevinc/tele/internal/botapi"
	"github.com/ardasevinc/tele/internal/botstore"
	"github.com/ardasevinc/tele/internal/secrets"
)

// VerifyLegacyBotCatalog proves that every dynamic managed-bot secret key is
// enumerable before migration from the selectorless legacy Keychain. It does
// not return secret values.
func VerifyLegacyBotCatalog(
	ctx context.Context,
	secretStore secrets.Store,
	managerAPI botapi.ManagerAPI,
	discoverer OwnedBotDiscoverer,
	inventory InventoryReader,
	profile, backend string,
) ([]string, error) {
	stored, err := inventory.Load()
	if err != nil {
		return nil, catalogIncomplete("local bot inventory could not be loaded", err)
	}
	manager, err := LoadManager(ctx, secretStore, profile)
	if errors.Is(err, secrets.ErrNotFound) {
		if len(stored.Bots) != 0 {
			return nil, catalogIncomplete("local bot inventory exists without a manager credential", nil)
		}
		return []string{}, nil
	}
	if err != nil {
		return nil, catalogIncomplete("manager credential is invalid", err)
	}
	status, err := VerifyManager(ctx, secretStore, managerAPI, profile, backend)
	if err != nil || !status.Verified || status.ID != manager.ID {
		return nil, catalogIncomplete("manager identity could not be verified", err)
	}
	if stored.Reconciliation == nil {
		return nil, catalogIncomplete("run tele bots reconcile before migrating the legacy Keychain", nil)
	}
	receipt := stored.Reconciliation
	if !receipt.Complete || receipt.ManagerID != manager.ID {
		return nil, catalogIncomplete("managed-bot reconciliation is incomplete or belongs to another manager", nil)
	}

	remoteBots, err := discoverer.ListOwnedBots(ctx)
	if err != nil {
		return nil, catalogIncomplete("owned-bot catalog could not be verified live", err)
	}
	remoteByID, _, err := indexRemoteBots(remoteBots)
	if err != nil {
		return nil, catalogIncomplete("owned-bot catalog is ambiguous", err)
	}
	remoteIDs := make([]int64, 0, len(remoteBots))
	for _, remote := range remoteBots {
		remoteIDs = append(remoteIDs, remote.ID)
	}
	sort.Slice(remoteIDs, func(i, j int) bool { return remoteIDs[i] < remoteIDs[j] })
	if !equalIDs(remoteIDs, receipt.RemoteBotIDs) {
		return nil, catalogIncomplete("owned-bot catalog changed after reconciliation; run tele bots reconcile again", nil)
	}

	localByID := make(map[int64]botstore.Bot, len(stored.Bots))
	for _, bot := range stored.Bots {
		localByID[bot.ID] = bot
	}
	for _, remote := range remoteBots {
		if remote.ManagerID != manager.ID {
			continue
		}
		local, exists := localByID[remote.ID]
		if !exists || local.TombstonedAt != nil {
			return nil, catalogIncomplete(fmt.Sprintf("manager-controlled bot @%s is not active in the local inventory", remote.Username), nil)
		}
	}

	keys := make([]string, 0, len(stored.Bots))
	for _, bot := range stored.Bots {
		remote, exists := remoteByID[bot.ID]
		if bot.TombstonedAt == nil {
			if !exists {
				return nil, catalogIncomplete(fmt.Sprintf("active local bot @%s is missing remotely", bot.Username), nil)
			}
			if bot.ManagerID != remote.ManagerID {
				return nil, catalogIncomplete(fmt.Sprintf("manager identity for @%s changed", bot.Username), nil)
			}
		}
		key := ManagedBotTokenSecretKey(bot.ID)
		if exists && remote.ManagerID == manager.ID {
			value, err := secretStore.Get(ctx, profile, key)
			if err != nil {
				return nil, catalogIncomplete(fmt.Sprintf("managed token for @%s is unavailable", bot.Username), err)
			}
			zero(value)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func catalogIncomplete(detail string, cause error) error {
	if cause != nil {
		detail += ": " + cause.Error()
	}
	return &secrets.BackendError{
		Kind:    secrets.ErrCatalogIncomplete,
		Backend: secrets.BackendKeychainLegacy,
		Detail:  detail,
	}
}

func zero(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
