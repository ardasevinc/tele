package botapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	defaultBaseURL = "https://api.telegram.org"
	maxResponse    = 1 << 20
)

type Bot struct {
	ID            int64  `json:"id"`
	IsBot         bool   `json:"is_bot"`
	Username      string `json:"username"`
	CanManageBots bool   `json:"can_manage_bots"`
}

type ManagerAPI interface {
	GetMe(context.Context, string) (Bot, error)
}

type ManagedTokenAPI interface {
	GetManagedBotToken(context.Context, string, int64) (string, error)
	ReplaceManagedBotToken(context.Context, string, int64) (string, error)
}

type AmbiguousResultError struct {
	Err error
}

func (e AmbiguousResultError) Error() string { return e.Err.Error() }
func (e AmbiguousResultError) Unwrap() error { return e.Err }

type Client struct {
	HTTP    *http.Client
	BaseURL string
}

func NewClient(httpClient *http.Client) Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return Client{HTTP: httpClient, BaseURL: defaultBaseURL}
}

func (c Client) GetMe(ctx context.Context, token string) (Bot, error) {
	var bot Bot
	if strings.TrimSpace(token) == "" {
		return bot, errors.New("manager token is required")
	}
	if err := c.call(ctx, token, "getMe", nil, &bot); err != nil {
		return bot, err
	}
	return bot, nil
}

func (c Client) GetManagedBotToken(ctx context.Context, managerToken string, botID int64) (string, error) {
	return c.managedBotToken(ctx, managerToken, botID, "getManagedBotToken")
}

func (c Client) ReplaceManagedBotToken(ctx context.Context, managerToken string, botID int64) (string, error) {
	return c.managedBotToken(ctx, managerToken, botID, "replaceManagedBotToken")
}

func (c Client) managedBotToken(
	ctx context.Context,
	managerToken string,
	botID int64,
	method string,
) (string, error) {
	if strings.TrimSpace(managerToken) == "" {
		return "", errors.New("manager token is required")
	}
	if botID == 0 {
		return "", errors.New("managed bot ID is required")
	}
	var token string
	params := url.Values{"user_id": {fmt.Sprintf("%d", botID)}}
	if err := c.call(ctx, managerToken, method, params, &token); err != nil {
		return "", err
	}
	if strings.TrimSpace(token) == "" {
		return "", errors.New("Telegram Bot API returned an empty managed bot token")
	}
	return token, nil
}

type responseEnvelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
}

func (c Client) call(ctx context.Context, token, method string, params url.Values, result any) error {
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	endpoint, err := url.Parse(baseURL)
	if err != nil {
		return errors.New("telegram Bot API base URL is invalid")
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/bot" + token + "/" + method
	endpoint.RawQuery = params.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return errors.New("telegram Bot API request could not be created")
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return AmbiguousResultError{Err: ctx.Err()}
		}
		return AmbiguousResultError{Err: errors.New("telegram Bot API request failed")}
	}
	defer func() { _ = response.Body.Close() }()

	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponse))
	var envelope responseEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return AmbiguousResultError{Err: errors.New("telegram Bot API returned an invalid response")}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices || !envelope.OK {
		description := strings.TrimSpace(strings.ReplaceAll(envelope.Description, token, "[redacted]"))
		if description == "" {
			description = http.StatusText(response.StatusCode)
		}
		return fmt.Errorf("telegram Bot API error %d: %s", envelope.ErrorCode, description)
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return AmbiguousResultError{Err: errors.New("telegram Bot API response is missing a result")}
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return AmbiguousResultError{Err: errors.New("telegram Bot API returned an invalid result")}
	}
	return nil
}
