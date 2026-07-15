package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	gotdpeer "github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"

	"github.com/ardasevinc/tele/internal/buildinfo"
	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/peerstore"
	"github.com/ardasevinc/tele/internal/secrets"
	telesession "github.com/ardasevinc/tele/internal/session"
)

type App struct {
	Config         config.Config
	Profile        string
	Paths          config.Paths
	Secrets        secrets.Store
	FloodWaitLimit time.Duration
	In             io.Reader
	Out            io.Writer
	Err            io.Writer
}

type ReadOptions struct {
	Peer          string
	Limit         int
	Since         time.Time
	Until         time.Time
	AfterID       int
	BeforeID      int
	AroundID      int
	Chronological bool
	Cursor        string
}

type SearchOptions struct {
	Query  string
	Peer   string
	Limit  int
	Cursor string
}

type ChatOptions struct {
	Limit  int
	Cursor string
}

func (a App) Run(ctx context.Context, fn func(ctx context.Context, c *telegram.Client) error) error {
	profile, err := a.profile()
	if err != nil {
		return err
	}
	hash, err := a.Secrets.Get(ctx, a.Profile, apiHashKey)
	if errors.Is(err, secrets.ErrNotFound) {
		return fmt.Errorf("missing api_hash for profile %q; run tele config set api-hash", a.Profile)
	}
	if err != nil {
		return err
	}
	if profile.APIID == 0 {
		return fmt.Errorf("missing api_id for profile %q; run tele config set api-id <id>", a.Profile)
	}
	client := telegram.NewClient(int(profile.APIID), string(hash), telegram.Options{
		SessionStorage: telesession.KeychainStorage{
			Profile: a.Profile,
			Store:   a.Secrets,
			Path:    a.sessionPath(),
		},
		Device: telegram.DeviceConfig{
			DeviceModel:    "tele",
			SystemVersion:  "macOS",
			AppVersion:     buildinfo.Version,
			SystemLangCode: "en",
			LangCode:       "en",
		},
		Middlewares: floodWaitMiddlewares(a.FloodWaitLimit),
	})
	called := false
	var callbackErr error
	runErr := client.Run(ctx, func(ctx context.Context) error {
		called = true
		callbackErr = fn(ctx, client)
		return callbackErr
	})
	return clientRunError(runErr, callbackErr, called)
}

func clientRunError(runErr, callbackErr error, called bool) error {
	if callbackErr != nil {
		return callbackErr
	}
	if runErr != nil {
		return runErr
	}
	if !called {
		return fmt.Errorf("telegram client closed before ready")
	}
	return nil
}

func floodWaitMiddlewares(limit time.Duration) []telegram.Middleware {
	if limit <= 0 {
		return nil
	}
	return []telegram.Middleware{floodWaitMiddleware(limit)}
}

type Chat struct {
	Ref                 string `json:"ref"`
	Kind                string `json:"kind"`
	ID                  int64  `json:"id"`
	Title               string `json:"title"`
	Username            string `json:"username,omitempty"`
	UnreadCount         int    `json:"unread_count"`
	UnreadMentionsCount int    `json:"unread_mentions_count,omitempty"`
	TopMessageID        int    `json:"top_message_id,omitempty"`
	LastMessageDate     string `json:"last_message_date,omitempty"`
	LastMessagePreview  string `json:"last_message_preview,omitempty"`
}

type PeerInfo struct {
	Ref      string `json:"ref"`
	Kind     string `json:"kind,omitempty"`
	Title    string `json:"title,omitempty"`
	Username string `json:"username,omitempty"`
}

func (a App) Chats(ctx context.Context, opts ChatOptions) (ChatPage, error) {
	return a.dialogs(ctx, opts, "")
}

func (a App) Inbox(ctx context.Context, opts ChatOptions, mode string) (ChatPage, error) {
	return a.dialogs(ctx, opts, mode)
}

func (a App) dialogs(ctx context.Context, opts ChatOptions, mode string) (ChatPage, error) {
	var out ChatPage
	err := a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		store := peerstore.New(a.Paths.Data, a.Profile)
		var pageErr error
		out, pageErr = dialogPages(ctx, opts, mode, func(ctx context.Context, cursor retrievalCursor, limit int) (tg.MessagesDialogsClass, error) {
			offsetPeer := tg.InputPeerClass(&tg.InputPeerEmpty{})
			if cursor.OffsetID > 0 {
				var resolveErr error
				offsetPeer, _, resolveErr = store.Resolve(cursor.OffsetPeerRef)
				if resolveErr != nil {
					return nil, fmt.Errorf("resolve dialog cursor peer: %w", resolveErr)
				}
			}
			res, fetchErr := c.API().MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
				ExcludePinned: cursor.OffsetID > 0,
				OffsetDate:    cursor.OffsetDate,
				OffsetID:      cursor.OffsetID,
				OffsetPeer:    offsetPeer,
				Limit:         limit,
			})
			if fetchErr != nil {
				return nil, fetchErr
			}
			_, peers := chatsFromDialogs(res)
			if len(peers) > 0 {
				if saveErr := store.Upsert(ctx, peers); saveErr != nil {
					return nil, saveErr
				}
			}
			return res, nil
		})
		return pageErr
	})
	return out, err
}

func (a App) PeerInfo(token string) PeerInfo {
	_, p, err := peerstore.New(a.Paths.Data, a.Profile).Resolve(token)
	if err != nil {
		return PeerInfo{Ref: token}
	}
	return PeerInfo{
		Ref:      p.Ref,
		Kind:     p.Kind,
		Title:    p.Title,
		Username: p.Username,
	}
}

type Message struct {
	ID                   int             `json:"id"`
	Date                 string          `json:"date,omitempty"`
	Text                 string          `json:"text,omitempty"`
	Outgoing             bool            `json:"outgoing"`
	Post                 bool            `json:"post,omitempty"`
	Media                string          `json:"media,omitempty"`
	Service              string          `json:"service,omitempty"`
	SourcePeerRef        string          `json:"source_peer_ref,omitempty"`
	SourcePeerLabel      string          `json:"source_peer_label,omitempty"`
	SenderPeerRef        string          `json:"sender_peer_ref,omitempty"`
	SenderLabel          string          `json:"sender_label,omitempty"`
	ReplyToMessageID     int             `json:"reply_to_message_id,omitempty"`
	ThreadID             int             `json:"thread_id,omitempty"`
	ForumTopic           bool            `json:"forum_topic,omitempty"`
	ForwardedFromPeerRef string          `json:"forwarded_from_peer_ref,omitempty"`
	ForwardedFromLabel   string          `json:"forwarded_from_label,omitempty"`
	ForwardedDate        string          `json:"forwarded_date,omitempty"`
	EditDate             string          `json:"edit_date,omitempty"`
	GroupedID            int64           `json:"grouped_id,omitempty"`
	Entities             []MessageEntity `json:"entities,omitempty"`
}

type MessageEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

func (a App) History(ctx context.Context, peerToken string, limit int) (MessagePage, error) {
	return a.Read(ctx, ReadOptions{Peer: peerToken, Limit: limit})
}

func (a App) Read(ctx context.Context, opts ReadOptions) (MessagePage, error) {
	var out MessagePage
	err := a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		input, peerRef, err := a.resolvePeer(ctx, c, opts.Peer)
		if err != nil {
			return err
		}
		store := peerstore.New(a.Paths.Data, a.Profile)
		out, err = readPages(ctx, input, peerRef.Ref, opts, func(ctx context.Context, req *tg.MessagesGetHistoryRequest) (tg.MessagesMessagesClass, error) {
			res, fetchErr := c.API().MessagesGetHistory(ctx, req)
			if fetchErr != nil {
				return nil, fetchErr
			}
			_, peers := messagePeerCatalog(res)
			if len(peers) > 0 {
				if saveErr := store.Upsert(ctx, peers); saveErr != nil {
					return nil, saveErr
				}
			}
			return res, nil
		})
		return err
	})
	return out, err
}

func (a App) Search(ctx context.Context, opts SearchOptions) (MessagePage, error) {
	var out MessagePage
	err := a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		var input tg.InputPeerClass
		peerRef := peerstore.Peer{}
		if opts.Peer != "" {
			var err error
			input, peerRef, err = a.resolvePeer(ctx, c, opts.Peer)
			if err != nil {
				return err
			}
		}
		store := peerstore.New(a.Paths.Data, a.Profile)
		var searchErr error
		out, searchErr = searchPages(ctx, peerRef.Ref, opts, func(ctx context.Context, cursor retrievalCursor, limit int) (tg.MessagesMessagesClass, error) {
			var res tg.MessagesMessagesClass
			var fetchErr error
			if opts.Peer != "" {
				res, fetchErr = c.API().MessagesSearch(ctx, &tg.MessagesSearchRequest{
					Peer:     input,
					Q:        opts.Query,
					Filter:   &tg.InputMessagesFilterEmpty{},
					OffsetID: cursor.OffsetID,
					Limit:    limit,
				})
			} else {
				offsetPeer := tg.InputPeerClass(&tg.InputPeerEmpty{})
				if cursor.OffsetID > 0 {
					var cursorErr error
					offsetPeer, _, cursorErr = store.Resolve(cursor.OffsetPeerRef)
					if cursorErr != nil {
						return nil, fmt.Errorf("resolve global search cursor peer: %w", cursorErr)
					}
				}
				res, fetchErr = c.API().MessagesSearchGlobal(ctx, &tg.MessagesSearchGlobalRequest{
					Q:          opts.Query,
					Filter:     &tg.InputMessagesFilterEmpty{},
					OffsetRate: cursor.OffsetRate,
					OffsetPeer: offsetPeer,
					OffsetID:   cursor.OffsetID,
					Limit:      limit,
				})
			}
			if fetchErr != nil {
				return nil, fetchErr
			}
			_, peers := messagePeerCatalog(res)
			if len(peers) > 0 {
				if saveErr := store.Upsert(ctx, peers); saveErr != nil {
					return nil, saveErr
				}
			}
			return res, nil
		})
		return searchErr
	})
	return out, err
}

func (a App) resolvePeer(ctx context.Context, c *telegram.Client, token string) (tg.InputPeerClass, peerstore.Peer, error) {
	if input, p, err := peerstore.New(a.Paths.Data, a.Profile).Resolve(token); err == nil {
		return input, p, nil
	}
	input, err := gotdpeer.Resolve(token).Bind(gotdpeer.DefaultResolver(c.API()))(ctx)
	if err != nil {
		return nil, peerstore.Peer{}, err
	}
	return input, peerstore.Peer{Ref: token, Title: token}, nil
}

func (a App) profile() (config.Profile, error) {
	_, profile, err := a.Config.ResolveProfile(a.Profile)
	return profile, err
}

func (a App) sessionPath() string {
	return filepath.Join(a.Paths.Data, a.Profile, "session.enc")
}

func chatsFromDialogs(dialogs tg.MessagesDialogsClass) ([]Chat, []peerstore.Peer) {
	var items []Chat
	var peers []peerstore.Peer
	addPeer := func(p peerstore.Peer, unread, mentions, top int, preview messagePreview) {
		peers = append(peers, p)
		items = append(items, Chat{
			Ref:                 p.Ref,
			Kind:                p.Kind,
			ID:                  p.ID,
			Title:               p.Title,
			Username:            p.Username,
			UnreadCount:         unread,
			UnreadMentionsCount: mentions,
			TopMessageID:        top,
			LastMessageDate:     preview.Date,
			LastMessagePreview:  preview.Text,
		})
	}
	handle := func(dialogs []tg.DialogClass, messages []tg.MessageClass, users []tg.UserClass, chats []tg.ChatClass) {
		peerByID := map[string]peerstore.Peer{}
		previewByID := messagePreviews(messages)
		for _, u := range users {
			if user, ok := u.(*tg.User); ok {
				if p, ok := peerstore.FromUser(user); ok {
					peerByID[fmt.Sprintf("user:%d", p.ID)] = p
				}
			}
		}
		for _, c := range chats {
			switch v := c.(type) {
			case *tg.Chat:
				if p, ok := peerstore.FromChat(v); ok {
					peerByID[fmt.Sprintf("chat:%d", p.ID)] = p
				}
			case *tg.Channel:
				if p, ok := peerstore.FromChannel(v); ok {
					peerByID[fmt.Sprintf("channel:%d", p.ID)] = p
					peerByID[fmt.Sprintf("supergroup:%d", p.ID)] = p
				}
			}
		}
		for _, d := range dialogs {
			dialog, ok := d.(*tg.Dialog)
			if !ok {
				continue
			}
			key := peerKey(dialog.Peer)
			if p, ok := peerByID[key]; ok {
				previewKey := fmt.Sprintf("%s:%d", key, dialog.TopMessage)
				addPeer(p, dialog.UnreadCount, dialog.UnreadMentionsCount, dialog.TopMessage, previewByID[previewKey])
			}
		}
	}
	switch v := dialogs.(type) {
	case *tg.MessagesDialogs:
		handle(v.Dialogs, v.Messages, v.Users, v.Chats)
	case *tg.MessagesDialogsSlice:
		handle(v.Dialogs, v.Messages, v.Users, v.Chats)
	}
	return items, peers
}

type messagePreview struct {
	Date string
	Text string
}

func messagePreviews(messages []tg.MessageClass) map[string]messagePreview {
	out := map[string]messagePreview{}
	for _, cls := range messages {
		switch msg := cls.(type) {
		case *tg.Message:
			text := strings.TrimSpace(msg.Message)
			if text == "" {
				if media, ok := msg.GetMedia(); ok {
					text = "[" + media.TypeName() + "]"
				}
			}
			out[fmt.Sprintf("%s:%d", peerKey(msg.PeerID), msg.ID)] = messagePreview{Date: unixDate(msg.Date), Text: text}
		case *tg.MessageService:
			out[fmt.Sprintf("%s:%d", peerKey(msg.PeerID), msg.ID)] = messagePreview{Date: unixDate(msg.Date), Text: "[" + msg.Action.TypeName() + "]"}
		}
	}
	return out
}

func peerKey(p tg.PeerClass) string {
	switch v := p.(type) {
	case *tg.PeerUser:
		return fmt.Sprintf("user:%d", v.UserID)
	case *tg.PeerChat:
		return fmt.Sprintf("chat:%d", v.ChatID)
	case *tg.PeerChannel:
		return fmt.Sprintf("channel:%d", v.ChannelID)
	default:
		return ""
	}
}

func messageClasses(res tg.MessagesMessagesClass) []tg.MessageClass {
	switch v := res.(type) {
	case *tg.MessagesMessages:
		return v.Messages
	case *tg.MessagesMessagesSlice:
		return v.Messages
	case *tg.MessagesChannelMessages:
		return v.Messages
	default:
		return nil
	}
}

func messagesFromResult(sourcePeer string, res tg.MessagesMessagesClass) []Message {
	messages, _ := convertMessages(sourcePeer, res)
	return messages
}

func unixDate(ts int) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(int64(ts), 0).UTC().Format(time.RFC3339)
}
