package peerstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gotd/td/tg"

	"github.com/ardasevinc/tele/internal/privatefs"
)

type Store struct {
	path string
}

type Cache struct {
	Peers []Peer `json:"peers"`
}

type Peer struct {
	Ref        string    `json:"ref"`
	Kind       string    `json:"kind"`
	ID         int64     `json:"id"`
	AccessHash int64     `json:"access_hash,omitempty"`
	Title      string    `json:"title"`
	Username   string    `json:"username,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func New(dataDir, profile string) Store {
	return Store{path: filepath.Join(dataDir, profile, "peers.json")}
}

func (s Store) Path() string {
	return s.path
}

func (s Store) Load() (Cache, error) {
	var cache Cache
	if err := privatefs.RepairFile(s.path); err != nil {
		return cache, err
	}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return cache, nil
	}
	if err != nil {
		return cache, err
	}
	return cache, json.Unmarshal(b, &cache)
}

func (s Store) Save(cache Cache) error {
	sort.Slice(cache.Peers, func(i, j int) bool { return cache.Peers[i].Ref < cache.Peers[j].Ref })
	b, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return privatefs.AtomicWriteFile(s.path, b)
}

func (s Store) Upsert(peers []Peer) error {
	cache, err := s.Load()
	if err != nil {
		return err
	}
	byRef := map[string]Peer{}
	for _, p := range cache.Peers {
		byRef[p.Ref] = p
	}
	for _, p := range peers {
		p.UpdatedAt = time.Now().UTC()
		byRef[p.Ref] = p
	}
	cache.Peers = cache.Peers[:0]
	for _, p := range byRef {
		cache.Peers = append(cache.Peers, p)
	}
	return s.Save(cache)
}

func (s Store) Resolve(token string) (tg.InputPeerClass, Peer, error) {
	cache, err := s.Load()
	if err != nil {
		return nil, Peer{}, err
	}
	token = strings.TrimPrefix(strings.TrimSpace(token), "@")
	for _, p := range cache.Peers {
		if p.Ref == token || strings.EqualFold(p.Username, token) || strings.EqualFold(p.Title, token) {
			input, err := p.Input()
			return input, p, err
		}
	}
	return nil, Peer{}, fmt.Errorf("peer %q not in cache; run tele chats first or use a username", token)
}

func FromUser(u *tg.User) (Peer, bool) {
	if u == nil || u.ID == 0 {
		return Peer{}, false
	}
	accessHash, ok := u.GetAccessHash()
	if !ok {
		return Peer{}, false
	}
	username, _ := u.GetUsername()
	title := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if title == "" {
		title = username
	}
	return Peer{
		Ref:        fmt.Sprintf("user:%d", u.ID),
		Kind:       "user",
		ID:         u.ID,
		AccessHash: accessHash,
		Title:      title,
		Username:   username,
	}, true
}

func FromChat(c *tg.Chat) (Peer, bool) {
	if c == nil || c.ID == 0 {
		return Peer{}, false
	}
	return Peer{
		Ref:   fmt.Sprintf("chat:%d", c.ID),
		Kind:  "chat",
		ID:    c.ID,
		Title: c.Title,
	}, true
}

func FromChannel(c *tg.Channel) (Peer, bool) {
	if c == nil || c.ID == 0 {
		return Peer{}, false
	}
	accessHash, ok := c.GetAccessHash()
	if !ok {
		return Peer{}, false
	}
	username, _ := c.GetUsername()
	kind := "channel"
	if c.Megagroup {
		kind = "supergroup"
	}
	return Peer{
		Ref:        fmt.Sprintf("%s:%d", kind, c.ID),
		Kind:       kind,
		ID:         c.ID,
		AccessHash: accessHash,
		Title:      c.Title,
		Username:   username,
	}, true
}

func (p Peer) Input() (tg.InputPeerClass, error) {
	switch p.Kind {
	case "user":
		return &tg.InputPeerUser{UserID: p.ID, AccessHash: p.AccessHash}, nil
	case "chat":
		return &tg.InputPeerChat{ChatID: p.ID}, nil
	case "channel", "supergroup":
		return &tg.InputPeerChannel{ChannelID: p.ID, AccessHash: p.AccessHash}, nil
	default:
		return nil, fmt.Errorf("unsupported peer kind %q", p.Kind)
	}
}
