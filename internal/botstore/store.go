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
	Bots []Bot `json:"bots"`
}

type Bot struct {
	Ref             string    `json:"ref"`
	ID              int64     `json:"id"`
	AccessHash      int64     `json:"access_hash,omitempty"`
	Username        string    `json:"username"`
	Name            string    `json:"name"`
	ManagerID       int64     `json:"manager_id"`
	ManagerUsername string    `json:"manager_username"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	TokenSyncedAt   time.Time `json:"token_synced_at,omitempty"`
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
	sortBots(inventory.Bots)
	return inventory, nil
}

func (s Store) Save(inventory Inventory) error {
	sortBots(inventory.Bots)
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
		if err := s.Save(inventory); err != nil {
			return err
		}
		stored = bot
		return nil
	})
	return stored, err
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

func normalizeToken(token string) string {
	token = strings.TrimSpace(token)
	for strings.HasPrefix(token, "@") {
		token = strings.TrimSpace(strings.TrimPrefix(token, "@"))
	}
	return token
}
