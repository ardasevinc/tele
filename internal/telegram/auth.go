package telegram

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"golang.org/x/term"

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

type authPending struct {
	Phone         string `json:"phone"`
	PhoneCodeHash string `json:"phone_code_hash"`
	CreatedAt     string `json:"created_at"`
}

func (a App) SetAPIHash(ctx context.Context, hash string) error {
	return a.Secrets.Set(ctx, a.Profile, apiHashKey, []byte(strings.TrimSpace(hash)))
}

func (a App) ResetLocalAuth(ctx context.Context) error {
	sessionErr := telesession.KeychainStorage{Profile: a.Profile, Store: a.Secrets, Path: a.sessionPath()}.Delete(ctx)
	pendingErr := a.Secrets.Delete(ctx, a.Profile, authPendingKey)
	return errors.Join(sessionErr, pendingErr)
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
		pending := authPending{Phone: phone, PhoneCodeHash: code.PhoneCodeHash, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
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
	return &Account{ID: u.ID, Username: username, FirstName: first, LastName: last, Phone: phone}
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
	a := &interactiveAuth{reader: bufio.NewReader(in), err: errOut, phone: opts.Phone, code: opts.Code, password: opts.Password, nonInteractive: opts.NonInteractive}
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
	return a.prompt(ctx, "login code: ", true)
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
