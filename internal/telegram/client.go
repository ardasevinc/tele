package telegram

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	gotdpeer "github.com/gotd/td/telegram/message/peer"
	gotdmessages "github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"
	"golang.org/x/term"

	"github.com/ardasevinc/tele/internal/buildinfo"
	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/peerstore"
	"github.com/ardasevinc/tele/internal/privatefs"
	"github.com/ardasevinc/tele/internal/secrets"
	telesession "github.com/ardasevinc/tele/internal/session"
)

const apiHashKey = "api-hash"
const authPendingKey = "auth-pending"
const pendingAuthTTL = 15 * time.Minute

var (
	ErrPendingAuthExpired = errors.New("pending auth expired")
	ErrPendingAuthInvalid = errors.New("pending auth state is invalid")
)

var (
	loginCodeLineRE = regexp.MustCompile(`(?i)(login code:\s*)[A-Za-z0-9_-]+`)
	webLoginCodeRE  = regexp.MustCompile(`(?i)(this is your login code:\s*)\n[A-Za-z0-9_-]+`)
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

type Account struct {
	ID        int64  `json:"id"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Phone     string `json:"phone,omitempty"`
}

type AuthStatus struct {
	Profile    string   `json:"profile"`
	Authorized bool     `json:"authorized"`
	Account    *Account `json:"account,omitempty"`
}

type LoginOptions struct {
	Phone          string
	Code           string
	Password       string
	NonInteractive bool
}

type AuthStartStatus struct {
	Profile           string `json:"profile"`
	Phone             string `json:"phone"`
	CodeSent          bool   `json:"code_sent"`
	CodeType          string `json:"code_type,omitempty"`
	TimeoutSeconds    int    `json:"timeout_seconds,omitempty"`
	AlreadyAuthorized bool   `json:"already_authorized,omitempty"`
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

type MediaDownloadOptions struct {
	Peer      string
	MessageID int
	OutDir    string
}

type MediaDownloadResult struct {
	OK           bool   `json:"ok"`
	PeerRef      string `json:"peer_ref"`
	MessageID    int    `json:"message_id"`
	Path         string `json:"path"`
	Bytes        int64  `json:"bytes"`
	MediaType    string `json:"media_type"`
	MimeType     string `json:"mime_type,omitempty"`
	FileName     string `json:"file_name"`
	StorageType  string `json:"storage_type,omitempty"`
	DownloadedAt string `json:"downloaded_at"`
}

type authPending struct {
	Phone         string `json:"phone"`
	PhoneCodeHash string `json:"phone_code_hash"`
	CreatedAt     string `json:"created_at"`
}

func (a App) SetAPIHash(ctx context.Context, hash string) error {
	return a.Secrets.Set(ctx, a.Profile, apiHashKey, []byte(strings.TrimSpace(hash)))
}

func (a App) ResetLocalAuth(ctx context.Context) error {
	sessionErr := telesession.KeychainStorage{
		Profile: a.Profile,
		Store:   a.Secrets,
		Path:    a.sessionPath(),
	}.Delete(ctx)
	pendingErr := a.Secrets.Delete(ctx, a.Profile, authPendingKey)
	return errors.Join(sessionErr, pendingErr)
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
	if err := client.Run(ctx, func(ctx context.Context) error {
		called = true
		callbackErr = fn(ctx, client)
		return callbackErr
	}); err != nil {
		return err
	}
	if callbackErr != nil {
		return callbackErr
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

func (a App) Login(ctx context.Context, opts LoginOptions) (AuthStatus, error) {
	status := AuthStatus{Profile: a.Profile}
	err := a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		flow := auth.NewFlow(newInteractiveAuth(a.In, a.Err, opts), auth.SendCodeOptions{})
		if err := flow.Run(ctx, c.Auth()); err != nil {
			return err
		}
		self, err := c.Self(ctx)
		if err != nil {
			return err
		}
		status.Authorized = true
		status.Account = userToAccount(self)
		return a.Secrets.Delete(ctx, a.Profile, authPendingKey)
	})
	return status, err
}

func (a App) AuthStart(ctx context.Context, phone string) (AuthStartStatus, error) {
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return AuthStartStatus{}, fmt.Errorf("phone is required")
	}
	status := AuthStartStatus{Profile: a.Profile, Phone: phone}
	err := a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		authStatus, err := c.Auth().Status(ctx)
		if err != nil {
			return err
		}
		if authStatus != nil && authStatus.Authorized {
			status.AlreadyAuthorized = true
			return a.Secrets.Delete(ctx, a.Profile, authPendingKey)
		}
		sent, err := c.Auth().SendCode(ctx, phone, auth.SendCodeOptions{})
		if err != nil {
			return err
		}
		code, ok := sent.(*tg.AuthSentCode)
		if !ok {
			return fmt.Errorf("unsupported auth sent code response %T", sent)
		}
		pending := authPending{
			Phone:         phone,
			PhoneCodeHash: code.PhoneCodeHash,
			CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		}
		if err := a.savePendingAuth(ctx, pending); err != nil {
			return err
		}
		status.CodeSent = true
		if code.Type != nil {
			status.CodeType = code.Type.TypeName()
		}
		if timeout, ok := code.GetTimeout(); ok {
			status.TimeoutSeconds = timeout
		}
		return nil
	})
	return status, err
}

func (a App) AuthComplete(ctx context.Context, code, password string) (AuthStatus, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return AuthStatus{}, fmt.Errorf("code is required")
	}
	pending, err := a.pendingAuth(ctx)
	if err != nil {
		return AuthStatus{}, err
	}
	status := AuthStatus{Profile: a.Profile}
	err = a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		if _, err := c.Auth().SignIn(ctx, pending.Phone, code, pending.PhoneCodeHash); err != nil {
			if !errors.Is(err, auth.ErrPasswordAuthNeeded) {
				return err
			}
			password = strings.TrimSpace(password)
			if password == "" {
				return auth.ErrPasswordNotProvided
			}
			if _, err := c.Auth().Password(ctx, password); err != nil {
				return err
			}
		}
		s, err := c.Auth().Status(ctx)
		if err != nil {
			return err
		}
		status = statusFromGotd(a.Profile, s)
		return a.Secrets.Delete(ctx, a.Profile, authPendingKey)
	})
	return status, err
}

func (a App) Status(ctx context.Context) (AuthStatus, error) {
	status := AuthStatus{Profile: a.Profile}
	if _, err := os.Stat(a.sessionPath()); errors.Is(err, os.ErrNotExist) {
		return status, nil
	} else if err != nil {
		return status, err
	}
	err := a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		s, err := c.Auth().Status(ctx)
		if err != nil {
			return err
		}
		status = statusFromGotd(a.Profile, s)
		return nil
	})
	return status, err
}

func (a App) LogoutRemote(ctx context.Context) error {
	err := a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		_, err := c.API().AuthLogOut(ctx)
		return err
	})
	if err != nil && !auth.IsUnauthorized(err) {
		return err
	}
	return a.Secrets.Delete(ctx, a.Profile, authPendingKey)
}

type Chat struct {
	Ref                   string   `json:"ref"`
	Kind                  string   `json:"kind"`
	ID                    int64    `json:"id"`
	Title                 string   `json:"title"`
	Username              string   `json:"username,omitempty"`
	UnreadCount           int      `json:"unread_count"`
	UnreadMentionsCount   int      `json:"unread_mentions_count,omitempty"`
	TopMessageID          int      `json:"top_message_id,omitempty"`
	LastMessageDate       string   `json:"last_message_date,omitempty"`
	LastMessagePreview    string   `json:"last_message_preview,omitempty"`
	LastMessageRedactions []string `json:"last_message_redactions,omitempty"`
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
	Redactions           []string        `json:"redactions,omitempty"`
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

func (a App) DownloadMedia(ctx context.Context, opts MediaDownloadOptions) (MediaDownloadResult, error) {
	if strings.TrimSpace(opts.Peer) == "" {
		return MediaDownloadResult{}, fmt.Errorf("peer is required")
	}
	if opts.MessageID <= 0 {
		return MediaDownloadResult{}, fmt.Errorf("msg-id must be positive")
	}
	var out MediaDownloadResult
	err := a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		input, peerRef, err := a.resolvePeer(ctx, c, opts.Peer)
		if err != nil {
			return err
		}
		msg, err := fetchMessage(ctx, c, input, peerRef, opts.MessageID)
		if err != nil {
			return err
		}
		file, ok := (gotdmessages.Elem{Msg: msg, Peer: input}).File()
		if !ok {
			return fmt.Errorf("message %d has no downloadable media", opts.MessageID)
		}
		dir := strings.TrimSpace(opts.OutDir)
		if dir == "" {
			dir, err = os.MkdirTemp("", "tele-media-*")
			if err != nil {
				return err
			}
		}
		if err := privatefs.EnsureDir(dir); err != nil {
			return err
		}
		name := safeDownloadFileName(opts.MessageID, file.Name)
		path := filepath.Join(dir, name)
		storageType, err := downloadToPath(ctx, c, file.Location, path)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		peer := peerRef.Ref
		if peer == "" {
			peer = opts.Peer
		}
		out = MediaDownloadResult{
			OK:           true,
			PeerRef:      peer,
			MessageID:    opts.MessageID,
			Path:         path,
			Bytes:        info.Size(),
			MediaType:    mediaTypeName(msg),
			MimeType:     file.MIMEType,
			FileName:     name,
			DownloadedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if storageType != nil {
			out.StorageType = storageType.TypeName()
		}
		return nil
	})
	return out, err
}

func downloadToPath(ctx context.Context, c *telegram.Client, location tg.InputFileLocationClass, path string) (_ tg.StorageFileTypeClass, err error) {
	return atomicDownload(filepath.Clean(path), func(w io.WriterAt) (tg.StorageFileTypeClass, error) {
		return c.Download(location).Parallel(ctx, w)
	})
}

func atomicDownload(path string, download func(io.WriterAt) (tg.StorageFileTypeClass, error)) (storage tg.StorageFileTypeClass, err error) {
	err = privatefs.AtomicReplaceFile(path, func(file *os.File) error {
		var downloadErr error
		storage, downloadErr = download(file)
		return downloadErr
	})
	return storage, err
}

func (a App) pendingAuth(ctx context.Context) (authPending, error) {
	b, err := a.Secrets.Get(ctx, a.Profile, authPendingKey)
	if errors.Is(err, secrets.ErrNotFound) {
		return authPending{}, fmt.Errorf("no pending auth; run tele auth start first")
	}
	if err != nil {
		return authPending{}, err
	}
	pending, err := parsePendingAuth(b, time.Now())
	if err != nil {
		if deleteErr := a.Secrets.Delete(ctx, a.Profile, authPendingKey); deleteErr != nil {
			return authPending{}, errors.Join(err, deleteErr)
		}
		return authPending{}, err
	}
	return pending, nil
}

func parsePendingAuth(data []byte, now time.Time) (authPending, error) {
	var pending authPending
	if err := json.Unmarshal(data, &pending); err != nil {
		return authPending{}, fmt.Errorf("%w; run tele auth start again", ErrPendingAuthInvalid)
	}
	createdAt, err := time.Parse(time.RFC3339, pending.CreatedAt)
	if err != nil || strings.TrimSpace(pending.Phone) == "" || strings.TrimSpace(pending.PhoneCodeHash) == "" {
		return authPending{}, fmt.Errorf("%w; run tele auth start again", ErrPendingAuthInvalid)
	}
	if now.After(createdAt.Add(pendingAuthTTL)) {
		return authPending{}, fmt.Errorf("%w; run tele auth start again", ErrPendingAuthExpired)
	}
	return pending, nil
}

func (a App) savePendingAuth(ctx context.Context, pending authPending) error {
	b, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	return a.Secrets.Set(ctx, a.Profile, authPendingKey, b)
}

func (a App) resolvePeer(ctx context.Context, c *telegram.Client, token string) (tg.InputPeerClass, peerstore.Peer, error) {
	if input, p, err := peerstore.New(a.Paths.Data, a.Profile).Resolve(token); err == nil {
		return input, p, nil
	}
	input, err := gotdpeer.Resolve(gotdpeer.DefaultResolver(c.API()), token)(ctx)
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

func statusFromGotd(profile string, s *auth.Status) AuthStatus {
	status := AuthStatus{Profile: profile}
	if s == nil || !s.Authorized {
		return status
	}
	status.Authorized = true
	status.Account = userToAccount(s.User)
	return status
}

func userToAccount(u *tg.User) *Account {
	if u == nil {
		return nil
	}
	username, _ := u.GetUsername()
	first, _ := u.GetFirstName()
	last, _ := u.GetLastName()
	phone, _ := u.GetPhone()
	return &Account{
		ID:        u.ID,
		Username:  username,
		FirstName: first,
		LastName:  last,
		Phone:     phone,
	}
}

func chatsFromDialogs(dialogs tg.MessagesDialogsClass) ([]Chat, []peerstore.Peer) {
	var items []Chat
	var peers []peerstore.Peer
	addPeer := func(p peerstore.Peer, unread, mentions, top int, preview messagePreview) {
		peers = append(peers, p)
		items = append(items, Chat{
			Ref:                   p.Ref,
			Kind:                  p.Kind,
			ID:                    p.ID,
			Title:                 p.Title,
			Username:              p.Username,
			UnreadCount:           unread,
			UnreadMentionsCount:   mentions,
			TopMessageID:          top,
			LastMessageDate:       preview.Date,
			LastMessagePreview:    preview.Text,
			LastMessageRedactions: preview.Redactions,
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
	Date       string
	Text       string
	Redactions []string
}

func messagePreviews(messages []tg.MessageClass) map[string]messagePreview {
	out := map[string]messagePreview{}
	for _, cls := range messages {
		switch msg := cls.(type) {
		case *tg.Message:
			text, redactions := redactMessageText(peerKey(msg.PeerID), msg.Message)
			text = strings.TrimSpace(text)
			if text == "" {
				if media, ok := msg.GetMedia(); ok {
					text = "[" + media.TypeName() + "]"
				}
			}
			out[fmt.Sprintf("%s:%d", peerKey(msg.PeerID), msg.ID)] = messagePreview{Date: unixDate(msg.Date), Text: text, Redactions: redactions}
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

func fetchMessage(ctx context.Context, c *telegram.Client, input tg.InputPeerClass, peerRef peerstore.Peer, msgID int) (*tg.Message, error) {
	id := []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}}
	var (
		res tg.MessagesMessagesClass
		err error
	)
	if channel, ok := inputChannel(input, peerRef); ok {
		res, err = c.API().ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: channel,
			ID:      id,
		})
	} else {
		res, err = c.API().MessagesGetMessages(ctx, id)
	}
	if err != nil {
		return nil, err
	}
	for _, cls := range messageClasses(res) {
		msg, ok := cls.(*tg.Message)
		if ok && msg.ID == msgID {
			return msg, nil
		}
	}
	return nil, fmt.Errorf("message %d not found", msgID)
}

func inputChannel(input tg.InputPeerClass, peerRef peerstore.Peer) (*tg.InputChannel, bool) {
	if channel, ok := input.(*tg.InputPeerChannel); ok {
		return &tg.InputChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash}, true
	}
	if peerRef.Kind == "channel" || peerRef.Kind == "supergroup" {
		return &tg.InputChannel{ChannelID: peerRef.ID, AccessHash: peerRef.AccessHash}, true
	}
	return nil, false
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

func mediaTypeName(msg *tg.Message) string {
	if msg == nil {
		return ""
	}
	media, ok := msg.GetMedia()
	if !ok || media == nil {
		return ""
	}
	return media.TypeName()
}

func safeDownloadFileName(msgID int, name string) string {
	name = strings.TrimSpace(filepath.Base(name))
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "media"
	}
	name = strings.Map(func(r rune) rune {
		switch r {
		case 0, '/', '\\', ':':
			return '-'
		default:
			return r
		}
	}, name)
	return fmt.Sprintf("%d-%s", msgID, name)
}

func unixDate(ts int) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(int64(ts), 0).UTC().Format(time.RFC3339)
}

func redactSensitiveText(text string) string {
	text = loginCodeLineRE.ReplaceAllString(text, "${1}[redacted]")
	text = webLoginCodeRE.ReplaceAllString(text, "${1}\n[redacted]")
	return text
}

func redactMessageText(sourcePeerRef, text string) (string, []string) {
	if sourcePeerRef != "user:777000" {
		return text, nil
	}
	redacted := redactSensitiveText(text)
	if redacted == text {
		return text, nil
	}
	return redacted, []string{"telegram_login_code"}
}

type interactiveAuth struct {
	reader         *bufio.Reader
	terminal       *os.File
	err            io.Writer
	phone          string
	code           string
	password       string
	nonInteractive bool
}

func newInteractiveAuth(in io.Reader, errOut io.Writer, opts LoginOptions) *interactiveAuth {
	a := &interactiveAuth{
		reader:         bufio.NewReader(in),
		err:            errOut,
		phone:          opts.Phone,
		code:           opts.Code,
		password:       opts.Password,
		nonInteractive: opts.NonInteractive,
	}
	if file, ok := in.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		a.terminal = file
	}
	return a
}

func (a *interactiveAuth) Phone(ctx context.Context) (string, error) {
	if a.phone != "" {
		return a.phone, nil
	}
	return a.prompt(ctx, "phone: ", false)
}

func (a *interactiveAuth) Password(ctx context.Context) (string, error) {
	if a.password != "" {
		return a.password, nil
	}
	return a.prompt(ctx, "2fa password: ", true)
}

func (a *interactiveAuth) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	if a.code != "" {
		return a.code, nil
	}
	return a.prompt(ctx, "login code: ", false)
}

func (a *interactiveAuth) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	_, _ = fmt.Fprintln(a.err, "Telegram returned terms of service; accept them in the official app before continuing.")
	return fmt.Errorf("terms of service acceptance is not implemented in tele alpha")
}

func (a *interactiveAuth) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, fmt.Errorf("sign-up is intentionally unsupported; use an established account")
}

func (a *interactiveAuth) prompt(ctx context.Context, label string, hidden bool) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	if a.nonInteractive {
		return "", fmt.Errorf("missing %s for non-interactive login", strings.TrimSuffix(label, ": "))
	}
	_, _ = fmt.Fprint(a.err, label)
	type result struct {
		value string
		err   error
	}
	resultCh := make(chan result, 1)
	go func() {
		if hidden && a.terminal != nil {
			b, err := term.ReadPassword(int(a.terminal.Fd()))
			_, _ = fmt.Fprintln(a.err)
			resultCh <- result{value: string(b), err: err}
			return
		}
		value, err := a.reader.ReadString('\n')
		if errors.Is(err, io.EOF) {
			err = nil
		}
		resultCh <- result{value: value, err: err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-resultCh:
		return strings.TrimSpace(result.value), result.err
	}
}

func ParseAPIID(value string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("api_id must be a positive integer")
	}
	return id, nil
}
