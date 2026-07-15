package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tg"

	"github.com/ardasevinc/tele/internal/peerstore"
)

const (
	cursorVersion     = 1
	telegramPageSize  = 100
	maxMessageResults = 1000
	maxDialogResults  = 500
	maxTelegramPages  = 25
)

type RetrievalReceipt struct {
	RequestedCount int    `json:"requested_count"`
	ReturnedCount  int    `json:"returned_count"`
	Complete       *bool  `json:"complete"`
	Truncated      bool   `json:"truncated"`
	NextCursor     string `json:"next_cursor,omitempty"`
	InputCursor    string `json:"input_cursor,omitempty"`
	ServerTotal    *int   `json:"server_total,omitempty"`
	Pages          int    `json:"pages"`
}

type MessagePage struct {
	Items   []Message
	Receipt RetrievalReceipt
}

type ChatPage struct {
	Items   []Chat
	Receipt RetrievalReceipt
}

type retrievalCursor struct {
	Version       int    `json:"v"`
	Kind          string `json:"kind"`
	Scope         string `json:"scope"`
	OffsetID      int    `json:"offset_id,omitempty"`
	OffsetDate    int    `json:"offset_date,omitempty"`
	OffsetRate    int    `json:"offset_rate,omitempty"`
	OffsetPeerRef string `json:"offset_peer_ref,omitempty"`
}

type historyFetcher func(context.Context, *tg.MessagesGetHistoryRequest) (tg.MessagesMessagesClass, error)
type searchFetcher func(context.Context, retrievalCursor, int) (tg.MessagesMessagesClass, error)
type dialogFetcher func(context.Context, retrievalCursor, int) (tg.MessagesDialogsClass, error)

func dialogPages(ctx context.Context, opts ChatOptions, mode string, fetch dialogFetcher) (ChatPage, error) {
	var page ChatPage
	if err := validateRequestedLimit(opts.Limit, maxDialogResults); err != nil {
		return page, err
	}
	kind := "dialogs"
	scope := scopeFingerprint(kind, mode)
	cursor, err := decodeCursor(opts.Cursor, kind, scope)
	if err != nil {
		return page, err
	}
	page.Receipt.RequestedCount = opts.Limit
	page.Receipt.InputCursor = opts.Cursor
	seen := make(map[string]struct{}, opts.Limit)
	completeKnown := false
	complete := false
	var lastChat Chat
	for page.Receipt.Pages < maxTelegramPages && len(page.Items) < opts.Limit {
		requestLimit := telegramPageSize
		if mode == "" {
			if remaining := opts.Limit - len(page.Items); remaining < requestLimit {
				requestLimit = remaining
			}
		}
		res, fetchErr := fetch(ctx, cursor, requestLimit)
		if fetchErr != nil {
			return ChatPage{}, fetchErr
		}
		page.Receipt.Pages++
		if total := dialogResultTotal(res); total != nil {
			page.Receipt.ServerTotal = total
		}
		chats, peers := chatsFromDialogs(res)
		rawCount := dialogResultLength(res)
		if len(chats) != rawCount {
			return ChatPage{}, fmt.Errorf("could not hydrate all %d Telegram dialogs; hydrated %d", rawCount, len(chats))
		}
		peerByRef := make(map[string]peerstore.Peer, len(peers))
		for _, peer := range peers {
			peerByRef[peer.Ref] = peer
		}
		if len(chats) == 0 {
			completeKnown, complete = true, true
			break
		}
		for _, chat := range chats {
			lastChat = chat
			if _, exists := seen[chat.Ref]; exists {
				continue
			}
			seen[chat.Ref] = struct{}{}
			if mode == "unread" && chat.UnreadCount == 0 {
				continue
			}
			if mode == "mentions" && chat.UnreadMentionsCount == 0 {
				continue
			}
			page.Items = append(page.Items, chat)
			if len(page.Items) == opts.Limit {
				break
			}
		}
		if rawCount < requestLimit {
			completeKnown, complete = true, true
			break
		}
		peer, ok := peerByRef[lastChat.Ref]
		if !ok || lastChat.TopMessageID == 0 {
			break
		}
		cursor.OffsetID = lastChat.TopMessageID
		cursor.OffsetDate = unixTimestamp(lastChat.LastMessageDate)
		cursor.OffsetPeerRef = peer.Ref
	}
	page.Receipt.ReturnedCount = len(page.Items)
	if mode == "" && page.Receipt.ServerTotal != nil && opts.Cursor == "" && len(page.Items) >= *page.Receipt.ServerTotal {
		completeKnown, complete = true, true
	}
	if completeKnown {
		page.Receipt.Complete = &complete
	}
	if len(page.Items) >= opts.Limit && !complete {
		incomplete := false
		page.Receipt.Complete = &incomplete
	}
	page.Receipt.Truncated = page.Receipt.Complete == nil || !*page.Receipt.Complete
	if page.Receipt.Truncated && lastChat.TopMessageID > 0 {
		cursor.Kind = kind
		cursor.Scope = scope
		cursor.OffsetID = lastChat.TopMessageID
		cursor.OffsetDate = unixTimestamp(lastChat.LastMessageDate)
		cursor.OffsetPeerRef = lastChat.Ref
		page.Receipt.NextCursor, err = encodeCursor(cursor)
		if err != nil {
			return ChatPage{}, err
		}
	}
	return page, nil
}

func dialogResultTotal(res tg.MessagesDialogsClass) *int {
	if slice, ok := res.(*tg.MessagesDialogsSlice); ok {
		total := slice.Count
		return &total
	}
	return nil
}

func dialogResultLength(res tg.MessagesDialogsClass) int {
	countDialogs := func(dialogs []tg.DialogClass) int {
		count := 0
		for _, dialog := range dialogs {
			if _, ok := dialog.(*tg.Dialog); ok {
				count++
			}
		}
		return count
	}
	switch value := res.(type) {
	case *tg.MessagesDialogs:
		return countDialogs(value.Dialogs)
	case *tg.MessagesDialogsSlice:
		return countDialogs(value.Dialogs)
	default:
		return 0
	}
}

func unixTimestamp(value string) int {
	date, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return 0
	}
	return int(date.Unix())
}

func searchPages(ctx context.Context, scopePeer string, opts SearchOptions, fetch searchFetcher) (MessagePage, error) {
	var page MessagePage
	if err := validateRequestedLimit(opts.Limit, maxMessageResults); err != nil {
		return page, err
	}
	kind := "search-global"
	if scopePeer != "" {
		kind = "search-peer"
	}
	scope := scopeFingerprint(kind, scopePeer, opts.Query)
	cursor, err := decodeCursor(opts.Cursor, kind, scope)
	if err != nil {
		return page, err
	}
	page.Receipt.RequestedCount = opts.Limit
	page.Receipt.InputCursor = opts.Cursor
	seen := make(map[string]struct{}, opts.Limit)
	completeKnown := false
	complete := false
	serverInexact := false
	var lastMessage Message
	var lastPeer peerstore.Peer
	for page.Receipt.Pages < maxTelegramPages && len(page.Items) < opts.Limit {
		requestLimit := telegramPageSize
		if remaining := opts.Limit - len(page.Items); remaining < requestLimit {
			requestLimit = remaining
		}
		res, fetchErr := fetch(ctx, cursor, requestLimit)
		if fetchErr != nil {
			return MessagePage{}, fetchErr
		}
		page.Receipt.Pages++
		if total, inexact := messageResultTotal(res); total != nil {
			page.Receipt.ServerTotal = total
			serverInexact = serverInexact || inexact
		}
		classes := messageClasses(res)
		if len(classes) == 0 {
			completeKnown, complete = true, true
			break
		}
		converted, peers := convertMessages(scopePeer, res)
		peerByRef := make(map[string]peerstore.Peer, len(peers))
		for _, peer := range peers {
			peerByRef[peer.Ref] = peer
		}
		for _, message := range converted {
			key := message.SourcePeerRef + ":" + fmt.Sprintf("%d", message.ID)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			page.Items = append(page.Items, message)
			lastMessage = message
			lastPeer = peerstore.Peer{}
			if peer, ok := peerByRef[message.SourcePeerRef]; ok {
				lastPeer = peer
			}
			if len(page.Items) == opts.Limit {
				break
			}
		}
		if len(classes) < requestLimit {
			completeKnown, complete = true, true
			break
		}
		if lastMessage.ID == 0 {
			break
		}
		cursor.OffsetID = lastMessage.ID
		if kind == "search-global" {
			cursor.OffsetRate = messageResultNextRate(res, lastMessage.Date)
			cursor.OffsetPeerRef = lastPeer.Ref
		}
	}
	page.Receipt.ReturnedCount = len(page.Items)
	if page.Receipt.ServerTotal != nil && opts.Cursor == "" && len(page.Items) >= *page.Receipt.ServerTotal {
		completeKnown, complete = true, true
	}
	if serverInexact {
		completeKnown = false
	}
	if completeKnown {
		page.Receipt.Complete = &complete
	}
	if len(page.Items) >= opts.Limit && !complete {
		incomplete := false
		page.Receipt.Complete = &incomplete
	}
	page.Receipt.Truncated = page.Receipt.Complete == nil || !*page.Receipt.Complete
	if page.Receipt.Truncated && lastMessage.ID > 0 {
		if kind == "search-global" && lastPeer.Ref == "" {
			return MessagePage{}, fmt.Errorf("cannot construct a stable cursor for global search result %d", lastMessage.ID)
		}
		cursor.Kind = kind
		cursor.Scope = scope
		cursor.OffsetID = lastMessage.ID
		page.Receipt.NextCursor, err = encodeCursor(cursor)
		if err != nil {
			return MessagePage{}, err
		}
	}
	return page, nil
}

func messageResultNextRate(res tg.MessagesMessagesClass, fallbackDate string) int {
	if slice, ok := res.(*tg.MessagesMessagesSlice); ok {
		if rate, exists := slice.GetNextRate(); exists {
			return rate
		}
	}
	date, err := time.Parse(time.RFC3339, fallbackDate)
	if err != nil {
		return 0
	}
	return int(date.Unix())
}

func readPages(ctx context.Context, input tg.InputPeerClass, peerRef string, opts ReadOptions, fetch historyFetcher) (MessagePage, error) {
	var page MessagePage
	if err := validateRequestedLimit(opts.Limit, maxMessageResults); err != nil {
		return page, err
	}
	scope := readScope(peerRef, opts)
	cursor, err := decodeCursor(opts.Cursor, "history", scope)
	if err != nil {
		return page, err
	}
	page.Receipt.RequestedCount = opts.Limit
	page.Receipt.InputCursor = opts.Cursor
	seen := make(map[int]struct{}, opts.Limit)
	offsetID := cursor.OffsetID
	lastRawID := 0
	completeKnown := false
	complete := false
	serverInexact := false
	for page.Receipt.Pages < maxTelegramPages && len(page.Items) < opts.Limit {
		requestLimit := telegramPageSize
		if remaining := opts.Limit - len(page.Items); remaining < requestLimit && opts.Since.IsZero() {
			requestLimit = remaining
		}
		req := &tg.MessagesGetHistoryRequest{
			Peer:     input,
			OffsetID: offsetID,
			Limit:    requestLimit,
			MaxID:    opts.BeforeID,
			MinID:    opts.AfterID,
		}
		if !opts.Until.IsZero() {
			req.OffsetDate = int(opts.Until.Unix())
		}
		if opts.AroundID > 0 {
			if opts.Cursor != "" {
				return MessagePage{}, fmt.Errorf("--around cannot be combined with --cursor")
			}
			req.OffsetID = opts.AroundID
			req.AddOffset = -(opts.Limit / 2)
		}
		res, err := fetch(ctx, req)
		if err != nil {
			return MessagePage{}, err
		}
		page.Receipt.Pages++
		if total, inexact := messageResultTotal(res); total != nil {
			page.Receipt.ServerTotal = total
			serverInexact = serverInexact || inexact
		}
		classes := messageClasses(res)
		if len(classes) == 0 {
			completeKnown, complete = true, true
			break
		}
		converted := messagesFromResult(peerRef, res)
		crossedSince := false
		for _, msg := range converted {
			lastRawID = msg.ID
			if !opts.Since.IsZero() {
				date, parseErr := time.Parse(time.RFC3339, msg.Date)
				if parseErr == nil && date.Before(opts.Since) {
					crossedSince = true
					continue
				}
			}
			if _, exists := seen[msg.ID]; exists {
				continue
			}
			seen[msg.ID] = struct{}{}
			page.Items = append(page.Items, msg)
			if len(page.Items) == opts.Limit {
				break
			}
		}
		if opts.AroundID > 0 {
			completeKnown = false
			break
		}
		if crossedSince || len(classes) < requestLimit {
			completeKnown, complete = true, true
			break
		}
		if lastRawID == 0 || lastRawID == offsetID {
			break
		}
		offsetID = lastRawID
	}
	page.Receipt.ReturnedCount = len(page.Items)
	if page.Receipt.ServerTotal != nil && opts.Cursor == "" && opts.Since.IsZero() && opts.AroundID == 0 && len(page.Items) >= *page.Receipt.ServerTotal {
		completeKnown, complete = true, true
	}
	if serverInexact {
		completeKnown = false
	}
	if completeKnown {
		page.Receipt.Complete = &complete
	}
	if len(page.Items) >= opts.Limit && !complete && opts.AroundID == 0 {
		incomplete := false
		page.Receipt.Complete = &incomplete
	}
	page.Receipt.Truncated = page.Receipt.Complete == nil || !*page.Receipt.Complete
	if page.Receipt.Truncated && lastRawID > 0 && opts.AroundID == 0 {
		page.Receipt.NextCursor, err = encodeCursor(retrievalCursor{
			Kind:     "history",
			Scope:    scope,
			OffsetID: lastRawID,
		})
		if err != nil {
			return MessagePage{}, err
		}
	}
	if opts.Chronological {
		for i, j := 0, len(page.Items)-1; i < j; i, j = i+1, j-1 {
			page.Items[i], page.Items[j] = page.Items[j], page.Items[i]
		}
	}
	return page, nil
}

func readScope(peerRef string, opts ReadOptions) string {
	return scopeFingerprint(
		"history",
		peerRef,
		opts.Since.UTC().Format(time.RFC3339Nano),
		opts.Until.UTC().Format(time.RFC3339Nano),
		fmt.Sprintf("after:%d", opts.AfterID),
		fmt.Sprintf("before:%d", opts.BeforeID),
		fmt.Sprintf("around:%d", opts.AroundID),
	)
}

func messageResultTotal(res tg.MessagesMessagesClass) (*int, bool) {
	switch value := res.(type) {
	case *tg.MessagesMessagesSlice:
		total := value.Count
		return &total, value.Inexact
	case *tg.MessagesChannelMessages:
		total := value.Count
		return &total, value.Inexact
	default:
		return nil, false
	}
}

func encodeCursor(cursor retrievalCursor) (string, error) {
	cursor.Version = cursorVersion
	b, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeCursor(value, kind, scope string) (retrievalCursor, error) {
	if strings.TrimSpace(value) == "" {
		return retrievalCursor{Version: cursorVersion, Kind: kind, Scope: scope}, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return retrievalCursor{}, fmt.Errorf("invalid cursor encoding")
	}
	var cursor retrievalCursor
	if err := json.Unmarshal(b, &cursor); err != nil {
		return retrievalCursor{}, fmt.Errorf("invalid cursor payload")
	}
	if cursor.Version != cursorVersion {
		return retrievalCursor{}, fmt.Errorf("unsupported cursor version %d", cursor.Version)
	}
	if cursor.Kind != kind || cursor.Scope != scope {
		return retrievalCursor{}, fmt.Errorf("cursor does not match this %s scope", kind)
	}
	return cursor, nil
}

func scopeFingerprint(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func validateRequestedLimit(limit, max int) error {
	if limit <= 0 {
		return fmt.Errorf("limit must be positive")
	}
	if limit > max {
		return fmt.Errorf("limit %d exceeds the conservative maximum of %d", limit, max)
	}
	return nil
}
