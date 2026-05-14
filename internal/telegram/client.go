package telegram

import (
	"bufio"
	"context"
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
	"github.com/gotd/td/tg"
	"golang.org/x/term"

	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/peerstore"
	"github.com/ardasevinc/tele/internal/secrets"
	telesession "github.com/ardasevinc/tele/internal/session"
)

const apiHashKey = "api-hash"

var (
	loginCodeLineRE = regexp.MustCompile(`(?i)(login code:\s*)[A-Za-z0-9_-]+`)
	webLoginCodeRE  = regexp.MustCompile(`(?i)(this is your login code:\s*)\n[A-Za-z0-9_-]+`)
)

type App struct {
	Config  config.Config
	Profile string
	Paths   config.Paths
	Secrets secrets.Store
	In      io.Reader
	Out     io.Writer
	Err     io.Writer
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

func (a App) SetAPIHash(ctx context.Context, hash string) error {
	return a.Secrets.Set(ctx, a.Profile, apiHashKey, []byte(strings.TrimSpace(hash)))
}

func (a App) DeleteAuth(ctx context.Context) error {
	return telesession.KeychainStorage{
		Profile: a.Profile,
		Store:   a.Secrets,
		Path:    a.sessionPath(),
	}.Delete(ctx)
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
			AppVersion:     "0.1.0-alpha",
			SystemLangCode: "en",
			LangCode:       "en",
		},
	})
	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	called := false
	var callbackErr error
	if err := client.Run(runCtx, func(ctx context.Context) error {
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

func (a App) Login(ctx context.Context, opts LoginOptions) (AuthStatus, error) {
	status := AuthStatus{Profile: a.Profile}
	err := a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		flow := auth.NewFlow(interactiveAuth{
			in:             a.In,
			err:            a.Err,
			phone:          opts.Phone,
			code:           opts.Code,
			password:       opts.Password,
			nonInteractive: opts.NonInteractive,
		}, auth.SendCodeOptions{})
		if err := flow.Run(ctx, c.Auth()); err != nil {
			return err
		}
		status.Authorized = true
		return nil
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

func (a App) Logout(ctx context.Context) error {
	err := a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		_, err := c.API().AuthLogOut(ctx)
		return err
	})
	if err != nil && !auth.IsUnauthorized(err) {
		return err
	}
	return a.DeleteAuth(ctx)
}

type Chat struct {
	Ref          string `json:"ref"`
	Kind         string `json:"kind"`
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	Username     string `json:"username,omitempty"`
	UnreadCount  int    `json:"unread_count"`
	TopMessageID int    `json:"top_message_id,omitempty"`
}

func (a App) Chats(ctx context.Context, limit int) ([]Chat, error) {
	var out []Chat
	err := a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		dialogs, err := c.API().MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
			Limit:      limit,
			OffsetPeer: &tg.InputPeerEmpty{},
		})
		if err != nil {
			return err
		}
		items, peers := chatsFromDialogs(dialogs)
		out = items
		return peerstore.New(a.Paths.Data, a.Profile).Upsert(peers)
	})
	return out, err
}

type Message struct {
	ID            int      `json:"id"`
	Date          string   `json:"date,omitempty"`
	Text          string   `json:"text,omitempty"`
	Outgoing      bool     `json:"outgoing"`
	Post          bool     `json:"post,omitempty"`
	Media         string   `json:"media,omitempty"`
	Service       string   `json:"service,omitempty"`
	SideEffects   []string `json:"side_effects,omitempty"`
	SourcePeerRef string   `json:"source_peer_ref,omitempty"`
}

func (a App) History(ctx context.Context, peerToken string, limit int) ([]Message, error) {
	var out []Message
	err := a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		input, peerRef, err := a.resolvePeer(ctx, c, peerToken)
		if err != nil {
			return err
		}
		res, err := c.API().MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:  input,
			Limit: limit,
		})
		if err != nil {
			return err
		}
		out = messagesFromResult(peerRef.Ref, res)
		return nil
	})
	return out, err
}

func (a App) Search(ctx context.Context, query, peerToken string, limit int) ([]Message, error) {
	var out []Message
	err := a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		input := tg.InputPeerClass(&tg.InputPeerEmpty{})
		peerRef := peerstore.Peer{}
		if peerToken != "" {
			var err error
			input, peerRef, err = a.resolvePeer(ctx, c, peerToken)
			if err != nil {
				return err
			}
		}
		res, err := c.API().MessagesSearch(ctx, &tg.MessagesSearchRequest{
			Peer:   input,
			Q:      query,
			Filter: &tg.InputMessagesFilterEmpty{},
			Limit:  limit,
		})
		if err != nil {
			return err
		}
		out = messagesFromResult(peerRef.Ref, res)
		return nil
	})
	return out, err
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
	addPeer := func(p peerstore.Peer, unread, top int) {
		peers = append(peers, p)
		items = append(items, Chat{
			Ref:          p.Ref,
			Kind:         p.Kind,
			ID:           p.ID,
			Title:        p.Title,
			Username:     p.Username,
			UnreadCount:  unread,
			TopMessageID: top,
		})
	}
	handle := func(dialogs []tg.DialogClass, users []tg.UserClass, chats []tg.ChatClass) {
		peerByID := map[string]peerstore.Peer{}
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
				addPeer(p, dialog.UnreadCount, dialog.TopMessage)
			}
		}
	}
	switch v := dialogs.(type) {
	case *tg.MessagesDialogs:
		handle(v.Dialogs, v.Users, v.Chats)
	case *tg.MessagesDialogsSlice:
		handle(v.Dialogs, v.Users, v.Chats)
	}
	return items, peers
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

func messagesFromResult(sourcePeer string, res tg.MessagesMessagesClass) []Message {
	var classes []tg.MessageClass
	switch v := res.(type) {
	case *tg.MessagesMessages:
		classes = v.Messages
	case *tg.MessagesMessagesSlice:
		classes = v.Messages
	case *tg.MessagesChannelMessages:
		classes = v.Messages
	}
	out := make([]Message, 0, len(classes))
	for _, cls := range classes {
		switch msg := cls.(type) {
		case *tg.Message:
			item := Message{
				ID:            msg.ID,
				Date:          unixDate(msg.Date),
				Text:          redactSensitiveText(msg.Message),
				Outgoing:      msg.Out,
				Post:          msg.Post,
				SideEffects:   []string{"may_mark_read"},
				SourcePeerRef: sourcePeer,
			}
			if media, ok := msg.GetMedia(); ok {
				item.Media = media.TypeName()
			}
			out = append(out, item)
		case *tg.MessageService:
			out = append(out, Message{
				ID:            msg.ID,
				Date:          unixDate(msg.Date),
				Outgoing:      msg.Out,
				Post:          msg.Post,
				Service:       msg.Action.TypeName(),
				SideEffects:   []string{"may_mark_read"},
				SourcePeerRef: sourcePeer,
			})
		}
	}
	return out
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

type interactiveAuth struct {
	in             io.Reader
	err            io.Writer
	phone          string
	code           string
	password       string
	nonInteractive bool
}

func (a interactiveAuth) Phone(ctx context.Context) (string, error) {
	if a.phone != "" {
		return a.phone, nil
	}
	return a.prompt(ctx, "phone: ", false)
}

func (a interactiveAuth) Password(ctx context.Context) (string, error) {
	if a.password != "" {
		return a.password, nil
	}
	return a.prompt(ctx, "2fa password: ", true)
}

func (a interactiveAuth) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	if a.code != "" {
		return a.code, nil
	}
	return a.prompt(ctx, "login code: ", false)
}

func (a interactiveAuth) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	_, _ = fmt.Fprintln(a.err, "Telegram returned terms of service; accept them in the official app before continuing.")
	return fmt.Errorf("terms of service acceptance is not implemented in tele alpha")
}

func (a interactiveAuth) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, fmt.Errorf("sign-up is intentionally unsupported; use an established account")
}

func (a interactiveAuth) prompt(ctx context.Context, label string, hidden bool) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	if a.nonInteractive {
		return "", fmt.Errorf("missing %s for non-interactive login", strings.TrimSuffix(label, ": "))
	}
	_, _ = fmt.Fprint(a.err, label)
	if hidden {
		if term.IsTerminal(int(os.Stdin.Fd())) {
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			_, _ = fmt.Fprintln(a.err)
			return strings.TrimSpace(string(b)), err
		}
	}
	reader := bufio.NewReader(a.in)
	value, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func ParseAPIID(value string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("api_id must be a positive integer")
	}
	return id, nil
}
