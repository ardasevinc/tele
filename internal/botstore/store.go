package botstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ardasevinc/tele/internal/privatefs"
)

type Store struct {
	path string
	now  func() time.Time
}

type Inventory struct {
	Bots           []Bot           `json:"bots"`
	Reconciliation *Reconciliation `json:"reconciliation,omitempty"`
}

type Reconciliation struct {
	CheckedAt        time.Time `json:"checked_at"`
	ManagerID        int64     `json:"manager_id"`
	RemoteBotIDs     []int64   `json:"remote_bot_ids"`
	LocalBotIDs      []int64   `json:"local_bot_ids"`
	PendingImportIDs []int64   `json:"pending_import_ids"`
	MissingTokenIDs  []int64   `json:"missing_token_ids"`
	Complete         bool      `json:"complete"`
}

type Bot struct {
	Ref             string     `json:"ref"`
	ID              int64      `json:"id"`
	AccessHash      int64      `json:"access_hash,omitempty"`
	Username        string     `json:"username"`
	Name            string     `json:"name"`
	ManagerID       int64      `json:"manager_id"`
	ManagerUsername string     `json:"manager_username"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	TokenSyncedAt   time.Time  `json:"token_synced_at,omitempty"`
	RemoteCheckedAt *time.Time `json:"remote_checked_at,omitempty"`
	TombstonedAt    *time.Time `json:"tombstoned_at,omitempty"`
}

func New(dataDir, profile string) Store {
	return Store{
		path: filepath.Join(dataDir, profile, "bots.json"),
		now:  time.Now,
	}
}

func (s Store) Path() string {
	return s.path
}

func (s Store) Load() (Inventory, error) {
	var inventory Inventory
	if err := privatefs.RepairFile(s.path); err != nil {
		return inventory, err
	}
	encoded, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return inventory, nil
	}
	if err != nil {
		return inventory, err
	}
	if err := json.Unmarshal(encoded, &inventory); err != nil {
		return Inventory{}, fmt.Errorf("bot inventory is invalid: %w", err)
	}
	if err := validateInventory(inventory); err != nil {
		return Inventory{}, fmt.Errorf("bot inventory is invalid: %w", err)
	}
	sortBots(inventory.Bots)
	return inventory, nil
}

func (s Store) Save(inventory Inventory) error {
	if err := validateInventory(inventory); err != nil {
		return fmt.Errorf("bot inventory is invalid: %w", err)
	}
	sortBots(inventory.Bots)
	if inventory.Reconciliation != nil {
		sortInt64s(inventory.Reconciliation.RemoteBotIDs)
		sortInt64s(inventory.Reconciliation.LocalBotIDs)
		sortInt64s(inventory.Reconciliation.PendingImportIDs)
		sortInt64s(inventory.Reconciliation.MissingTokenIDs)
	}
	encoded, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return err
	}
	return privatefs.AtomicWriteFile(s.path, encoded)
}

func (s Store) Upsert(ctx context.Context, bot Bot) (Bot, error) {
	if bot.ID == 0 || strings.TrimSpace(bot.Username) == "" {
		return Bot{}, errors.New("bot ID and username are required")
	}
	if bot.Ref == "" {
		bot.Ref = Ref(bot.ID)
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	var stored Bot
	err := privatefs.WithLock(ctx, s.path+".lock", func() error {
		inventory, err := s.Load()
		if err != nil {
			return err
		}
		currentIndex := -1
		for i := range inventory.Bots {
			if inventory.Bots[i].ID == bot.ID {
				currentIndex = i
				break
			}
		}
		timestamp := now().UTC()
		if currentIndex >= 0 {
			current := inventory.Bots[currentIndex]
			if bot.CreatedAt.IsZero() {
				bot.CreatedAt = current.CreatedAt
			}
			if bot.TokenSyncedAt.IsZero() {
				bot.TokenSyncedAt = current.TokenSyncedAt
			}
			if bot.RemoteCheckedAt == nil {
				bot.RemoteCheckedAt = current.RemoteCheckedAt
			}
			if bot.TombstonedAt == nil {
				bot.TombstonedAt = current.TombstonedAt
			}
		}
		if bot.CreatedAt.IsZero() {
			bot.CreatedAt = timestamp
		}
		bot.UpdatedAt = timestamp
		if currentIndex >= 0 {
			inventory.Bots[currentIndex] = bot
		} else {
			inventory.Bots = append(inventory.Bots, bot)
		}
		// Any independent inventory mutation invalidates the previous remote
		// agreement. A fresh reconciliation will recreate the receipt.
		inventory.Reconciliation = nil
		if err := s.Save(inventory); err != nil {
			return err
		}
		stored = bot
		return nil
	})
	return stored, err
}

func (s Store) RecordReconciliation(
	ctx context.Context,
	managerID int64,
	remoteBotIDs, pendingImportIDs, missingTokenIDs []int64,
) (Inventory, error) {
	if managerID == 0 {
		return Inventory{}, errors.New("manager ID is required")
	}
	remote, err := idSet("remote bot IDs", remoteBotIDs)
	if err != nil {
		return Inventory{}, err
	}
	if _, err := idSet("pending import IDs", pendingImportIDs); err != nil {
		return Inventory{}, err
	}
	if _, err := idSet("missing token IDs", missingTokenIDs); err != nil {
		return Inventory{}, err
	}
	var result Inventory
	err = privatefs.WithLock(ctx, s.path+".lock", func() error {
		inventory, err := s.Load()
		if err != nil {
			return err
		}
		now := s.currentTime()
		localIDs := make([]int64, 0, len(inventory.Bots))
		for i := range inventory.Bots {
			bot := &inventory.Bots[i]
			localIDs = append(localIDs, bot.ID)
			checkedAt := now
			bot.RemoteCheckedAt = &checkedAt
			if _, exists := remote[bot.ID]; exists {
				bot.TombstonedAt = nil
			} else if bot.TombstonedAt == nil {
				tombstonedAt := now
				bot.TombstonedAt = &tombstonedAt
			}
		}
		inventory.Reconciliation = &Reconciliation{
			CheckedAt:        now,
			ManagerID:        managerID,
			RemoteBotIDs:     append([]int64(nil), remoteBotIDs...),
			LocalBotIDs:      localIDs,
			PendingImportIDs: append([]int64(nil), pendingImportIDs...),
			MissingTokenIDs:  append([]int64(nil), missingTokenIDs...),
			Complete:         len(pendingImportIDs) == 0 && len(missingTokenIDs) == 0,
		}
		if err := s.Save(inventory); err != nil {
			return err
		}
		result = inventory
		return nil
	})
	return result, err
}

func (s Store) currentTime() time.Time {
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	return now().UTC()
}

func (s Store) Resolve(token string) (Bot, error) {
	inventory, err := s.Load()
	if err != nil {
		return Bot{}, err
	}
	normalized := normalizeToken(token)
	for _, bot := range inventory.Bots {
		if bot.Ref == normalized ||
			strings.EqualFold(bot.Username, normalized) ||
			strconv.FormatInt(bot.ID, 10) == normalized {
			return bot, nil
		}
	}
	return Bot{}, fmt.Errorf("managed bot %q not in local inventory", token)
}

func Ref(id int64) string {
	return "bot:" + strconv.FormatInt(id, 10)
}

func sortBots(bots []Bot) {
	sort.Slice(bots, func(i, j int) bool {
		return strings.ToLower(bots[i].Username) < strings.ToLower(bots[j].Username)
	})
}

func sortInt64s(values []int64) {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
}

func validateInventory(inventory Inventory) error {
	ids := make(map[int64]struct{}, len(inventory.Bots))
	usernames := make(map[string]struct{}, len(inventory.Bots))
	for _, bot := range inventory.Bots {
		if bot.ID == 0 || strings.TrimSpace(bot.Username) == "" {
			return errors.New("bot ID and username are required")
		}
		if bot.Ref != "" && bot.Ref != Ref(bot.ID) {
			return fmt.Errorf("bot %d has invalid ref %q", bot.ID, bot.Ref)
		}
		if _, exists := ids[bot.ID]; exists {
			return fmt.Errorf("duplicate bot ID %d", bot.ID)
		}
		username := strings.ToLower(strings.TrimSpace(bot.Username))
		if _, exists := usernames[username]; exists {
			return fmt.Errorf("duplicate bot username @%s", bot.Username)
		}
		ids[bot.ID] = struct{}{}
		usernames[username] = struct{}{}
	}
	if inventory.Reconciliation == nil {
		return nil
	}
	reconciliation := inventory.Reconciliation
	if reconciliation.ManagerID == 0 || reconciliation.CheckedAt.IsZero() {
		return errors.New("reconciliation manager ID and checked time are required")
	}
	remote, err := idSet("reconciliation remote bot IDs", reconciliation.RemoteBotIDs)
	if err != nil {
		return err
	}
	local, err := idSet("reconciliation local bot IDs", reconciliation.LocalBotIDs)
	if err != nil {
		return err
	}
	if len(local) != len(ids) {
		return errors.New("reconciliation local bot IDs do not match inventory")
	}
	for id := range ids {
		if _, exists := local[id]; !exists {
			return errors.New("reconciliation local bot IDs do not match inventory")
		}
	}
	pending, err := idSet("reconciliation pending import IDs", reconciliation.PendingImportIDs)
	if err != nil {
		return err
	}
	for id := range pending {
		if _, exists := remote[id]; !exists {
			return fmt.Errorf("pending import bot %d is not in the remote catalog", id)
		}
		if _, exists := local[id]; exists {
			return fmt.Errorf("pending import bot %d is already local", id)
		}
	}
	missing, err := idSet("reconciliation missing token IDs", reconciliation.MissingTokenIDs)
	if err != nil {
		return err
	}
	for id := range missing {
		if _, exists := remote[id]; !exists {
			return fmt.Errorf("missing-token bot %d is not in the remote catalog", id)
		}
		if _, exists := local[id]; !exists {
			return fmt.Errorf("missing-token bot %d is not local", id)
		}
	}
	wantComplete := len(pending) == 0 && len(missing) == 0
	if reconciliation.Complete != wantComplete {
		return errors.New("reconciliation completeness does not match catalog gaps")
	}
	return nil
}

func idSet(label string, values []int64) (map[int64]struct{}, error) {
	result := make(map[int64]struct{}, len(values))
	for _, id := range values {
		if id == 0 {
			return nil, fmt.Errorf("%s contain zero", label)
		}
		if _, exists := result[id]; exists {
			return nil, fmt.Errorf("%s contain duplicate %d", label, id)
		}
		result[id] = struct{}{}
	}
	return result, nil
}

func normalizeToken(token string) string {
	token = strings.TrimSpace(token)
	for strings.HasPrefix(token, "@") {
		token = strings.TrimSpace(strings.TrimPrefix(token, "@"))
	}
	return token
}
