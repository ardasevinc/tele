package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tgerr"
)

type Format string

const (
	Human Format = "human"
	JSON  Format = "json"
	JSONL Format = "jsonl"
)

type Writer struct {
	Out    io.Writer
	Err    io.Writer
	Format Format
	Quiet  bool
}

func (w Writer) Print(v any) error {
	if w.Format == JSON {
		enc := json.NewEncoder(w.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	_, err := fmt.Fprintln(w.Out, v)
	return err
}

func (w Writer) JSON(v any) error {
	enc := json.NewEncoder(w.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (w Writer) JSONL(items []any) error {
	enc := json.NewEncoder(w.Out)
	for _, item := range items {
		if err := enc.Encode(item); err != nil {
			return err
		}
	}
	return nil
}

func (w Writer) Info(format string, args ...any) {
	if w.Quiet || w.Format != Human {
		return
	}
	_, _ = fmt.Fprintf(w.Err, "info: "+format+"\n", args...)
}

func (w Writer) Warn(format string, args ...any) {
	if w.Quiet || w.Format != Human {
		return
	}
	_, _ = fmt.Fprintf(w.Err, "warn: "+format+"\n", args...)
}

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code              string `json:"code"`
	Message           string `json:"message"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
	TelegramCode      int    `json:"telegram_code,omitempty"`
	TelegramType      string `json:"telegram_type,omitempty"`
}

type Meta struct {
	Profile     string   `json:"profile"`
	AccountID   int64    `json:"account_id,omitempty"`
	PeerRef     string   `json:"peer_ref,omitempty"`
	FetchedAt   string   `json:"fetched_at"`
	Limit       int      `json:"limit,omitempty"`
	SideEffects []string `json:"side_effects,omitempty"`
}

type Envelope struct {
	Meta Meta `json:"meta"`
	Data any  `json:"data"`
}

func NewMeta(profile string) Meta {
	return Meta{
		Profile:   profile,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

func ErrorFrom(err error) ErrorResponse {
	body := ErrorBody{
		Code:    "command_failed",
		Message: err.Error(),
	}
	if d, ok := tgerr.AsFloodWait(err); ok {
		body.Code = "telegram_flood_wait"
		body.RetryAfterSeconds = int(d / time.Second)
	}
	if rpcErr, ok := tgerr.As(err); ok {
		body.TelegramCode = rpcErr.Code
		body.TelegramType = rpcErr.Type
		if body.Code == "command_failed" {
			body.Code = "telegram_rpc_error"
			if rpcErr.Type != "" {
				body.Code = "telegram_" + strings.ToLower(rpcErr.Type)
			}
		}
	}
	if errors.Is(err, auth.ErrPasswordAuthNeeded) || errors.Is(err, auth.ErrPasswordNotProvided) {
		body.Code = "password_required"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not authorized"):
		body.Code = "not_authorized"
	case strings.Contains(msg, "missing api_hash"):
		body.Code = "missing_api_hash"
	case strings.Contains(msg, "missing api_id"):
		body.Code = "missing_api_id"
	case strings.Contains(msg, "not in cache") || strings.Contains(msg, "peer"):
		if body.Code == "command_failed" {
			body.Code = "peer_not_found"
		}
	case strings.Contains(msg, "requires") || strings.Contains(msg, "must be"):
		if body.Code == "command_failed" {
			body.Code = "invalid_input"
		}
	}
	return ErrorResponse{Error: body}
}
