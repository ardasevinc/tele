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
	SchemaVersion = "tele/v1alpha1"

	Human Format = "human"
	JSON  Format = "json"
	JSONL Format = "jsonl"
)

const (
	ExitGeneral           = 1
	ExitInvalidInput      = 2
	ExitAuthOrConfig      = 3
	ExitNotFound          = 4
	ExitTelegram          = 5
	ExitLocalIO           = 6
	ExitMutationReconcile = 7
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
	if w.Format != JSONL {
		enc.SetIndent("", "  ")
	}
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
	_, _ = fmt.Fprintln(w.Err, "info: "+SanitizeTerminal(fmt.Sprintf(format, args...)))
}

func (w Writer) Warn(format string, args ...any) {
	if w.Quiet || w.Format != Human {
		return
	}
	_, _ = fmt.Fprintln(w.Err, "warn: "+SanitizeTerminal(fmt.Sprintf(format, args...)))
}

type ErrorResponse struct {
	SchemaVersion string    `json:"schema_version"`
	Error         ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code                 string `json:"code"`
	Message              string `json:"message"`
	ExitCode             int    `json:"exit_code"`
	Outcome              string `json:"outcome,omitempty"`
	RetrySafe            *bool  `json:"retry_safe,omitempty"`
	ReconciliationHandle string `json:"reconciliation_handle,omitempty"`
	Guidance             string `json:"guidance,omitempty"`
	RetryAfterSeconds    int    `json:"retry_after_seconds,omitempty"`
	TelegramCode         int    `json:"telegram_code,omitempty"`
	TelegramType         string `json:"telegram_type,omitempty"`
}

type mutationFailure interface {
	MutationOutcomeCode() string
	MutationRetrySafe() bool
	MutationReconciliationHandle() string
}

type Meta struct {
	Profile     string         `json:"profile"`
	AccountID   int64          `json:"account_id,omitempty"`
	PeerRef     string         `json:"peer_ref,omitempty"`
	FetchedAt   string         `json:"fetched_at"`
	Limit       int            `json:"limit,omitempty"`
	SideEffects []string       `json:"side_effects,omitempty"`
	Retrieval   *RetrievalMeta `json:"retrieval,omitempty"`
}

type RetrievalMeta struct {
	RequestedCount int    `json:"requested_count"`
	ReturnedCount  int    `json:"returned_count"`
	Complete       *bool  `json:"complete"`
	Truncated      bool   `json:"truncated"`
	NextCursor     string `json:"next_cursor,omitempty"`
	InputCursor    string `json:"input_cursor,omitempty"`
	ServerTotal    *int   `json:"server_total,omitempty"`
	Pages          int    `json:"pages"`
}

type Envelope struct {
	SchemaVersion string `json:"schema_version"`
	Meta          Meta   `json:"meta"`
	Data          any    `json:"data"`
}

type Record struct {
	SchemaVersion string     `json:"schema_version"`
	Type          string     `json:"type"`
	Meta          *Meta      `json:"meta,omitempty"`
	Data          any        `json:"data,omitempty"`
	Error         *ErrorBody `json:"error,omitempty"`
}

func NewEnvelope(meta Meta, data any) Envelope {
	return Envelope{SchemaVersion: SchemaVersion, Meta: meta, Data: data}
}

func MetaRecord(meta Meta) Record {
	return Record{SchemaVersion: SchemaVersion, Type: "meta", Meta: &meta}
}

func DataRecord(data any) Record {
	return Record{SchemaVersion: SchemaVersion, Type: "data", Data: data}
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
	var mutationErr mutationFailure
	if errors.As(err, &mutationErr) {
		retrySafe := mutationErr.MutationRetrySafe()
		body.Outcome = mutationErr.MutationOutcomeCode()
		body.RetrySafe = &retrySafe
		body.ReconciliationHandle = mutationErr.MutationReconciliationHandle()
		switch body.Outcome {
		case "outcome_unknown":
			body.Code = "mutation_outcome_unknown"
			body.Guidance = "do not retry blindly; reconcile the operation first"
		case "confirmed":
			body.Code = "mutation_confirmed_output_failed"
			body.Guidance = "the mutation was confirmed; do not retry"
		case "rejected":
			body.Code = "mutation_rejected"
		}
	}
	if d, ok := tgerr.AsFloodWait(err); ok {
		body.Code = "telegram_flood_wait"
		body.RetryAfterSeconds = int(d / time.Second)
	}
	if rpcErr, ok := tgerr.As(err); ok {
		body.TelegramCode = rpcErr.Code
		body.TelegramType = rpcErr.Type
		if body.Code == "command_failed" || body.Code == "mutation_rejected" {
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
	case strings.Contains(msg, "unknown config key") || strings.Contains(msg, "mutually exclusive"):
		if body.Code == "command_failed" {
			body.Code = "invalid_input"
		}
	case strings.Contains(msg, "unknown command") || strings.Contains(msg, "unknown flag") || strings.Contains(msg, "required flag") || strings.Contains(msg, "arg(s)") || strings.Contains(msg, "invalid argument"):
		if body.Code == "command_failed" {
			body.Code = "invalid_input"
		}
	case strings.Contains(msg, "broken pipe") || strings.Contains(msg, "closed pipe") || strings.Contains(msg, "short write"):
		if body.Code == "command_failed" {
			body.Code = "output_failed"
		}
	}
	body.ExitCode = ExitCodeFor(body.Code)
	return ErrorResponse{SchemaVersion: SchemaVersion, Error: body}
}

func ExitCodeFor(code string) int {
	switch code {
	case "invalid_input":
		return ExitInvalidInput
	case "not_authorized", "password_required", "missing_api_hash", "missing_api_id":
		return ExitAuthOrConfig
	case "peer_not_found":
		return ExitNotFound
	case "output_failed":
		return ExitLocalIO
	case "mutation_outcome_unknown", "mutation_confirmed_output_failed":
		return ExitMutationReconcile
	default:
		if strings.HasPrefix(code, "telegram_") {
			return ExitTelegram
		}
		return ExitGeneral
	}
}

func ErrorRecordFrom(err error) Record {
	response := ErrorFrom(err)
	return Record{SchemaVersion: SchemaVersion, Type: "error", Error: &response.Error}
}
