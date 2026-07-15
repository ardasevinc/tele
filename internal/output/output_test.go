package output

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tgerr"

	tgapp "github.com/ardasevinc/tele/internal/telegram"
)

type testMutationError struct {
	outcome   string
	retrySafe bool
}

func (e testMutationError) Error() string                        { return "mutation failed" }
func (e testMutationError) MutationOutcomeCode() string          { return e.outcome }
func (e testMutationError) MutationRetrySafe() bool              { return e.retrySafe }
func (e testMutationError) MutationReconciliationHandle() string { return "random_id:42" }

func TestErrorFromClassifiesFloodWait(t *testing.T) {
	got := ErrorFrom(tgerr.New(420, "FLOOD_WAIT_42")).Error
	if got.Code != "telegram_flood_wait" {
		t.Fatalf("Code = %q, want telegram_flood_wait", got.Code)
	}
	if got.RetryAfterSeconds != 42 {
		t.Fatalf("RetryAfterSeconds = %d, want 42", got.RetryAfterSeconds)
	}
	if got.TelegramType != "FLOOD_WAIT" {
		t.Fatalf("TelegramType = %q, want FLOOD_WAIT", got.TelegramType)
	}
}

func TestErrorFromClassifiesPasswordRequired(t *testing.T) {
	got := ErrorFrom(auth.ErrPasswordNotProvided).Error
	if got.Code != "password_required" {
		t.Fatalf("Code = %q, want password_required", got.Code)
	}
}

func TestErrorFromKeepsGenericErrorUseful(t *testing.T) {
	got := ErrorFrom(errors.New("peer \"x\" not in cache")).Error
	if got.Code != "peer_not_found" {
		t.Fatalf("Code = %q, want peer_not_found", got.Code)
	}
}

func TestErrorFromClassifiesUnknownMutationOutcome(t *testing.T) {
	got := ErrorFrom(testMutationError{outcome: "outcome_unknown"}).Error
	if got.Code != "mutation_outcome_unknown" || got.Outcome != "outcome_unknown" {
		t.Fatalf("ErrorFrom = %+v", got)
	}
	if got.RetrySafe == nil || *got.RetrySafe {
		t.Fatalf("RetrySafe = %v, want false", got.RetrySafe)
	}
	if got.ReconciliationHandle != "random_id:42" || got.Guidance == "" {
		t.Fatalf("ErrorFrom missing reconciliation contract: %+v", got)
	}
}

func TestErrorFromClassifiesConfirmedOutputFailure(t *testing.T) {
	got := ErrorFrom(testMutationError{outcome: "confirmed"}).Error
	if got.Code != "mutation_confirmed_output_failed" || got.Outcome != "confirmed" {
		t.Fatalf("ErrorFrom = %+v", got)
	}
}

func TestRetrievalMetaEncodesUnknownCompletenessExplicitly(t *testing.T) {
	var out bytes.Buffer
	err := json.NewEncoder(&out).Encode(Envelope{Meta: Meta{
		Profile:   "test",
		Retrieval: &RetrievalMeta{RequestedCount: 10, ReturnedCount: 3, Complete: nil, Truncated: true, Pages: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"complete":null`)) {
		t.Fatalf("encoded retrieval metadata = %s", out.String())
	}
}

func TestJSONLWriterEmitsOneCompactObjectPerLine(t *testing.T) {
	var out bytes.Buffer
	w := Writer{Out: &out, Format: JSONL}
	if err := w.JSONL([]any{
		MetaRecord(NewMeta("test")),
		DataRecord(map[string]any{"text": "line one\nline two"}),
		ErrorRecordFrom(errors.New("boom")),
	}); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
	if len(lines) != 3 {
		t.Fatalf("JSONL emitted %d lines:\n%s", len(lines), out.String())
	}
	for i, line := range lines {
		var value map[string]any
		if err := json.Unmarshal(line, &value); err != nil {
			t.Fatalf("line %d is not one JSON object: %q: %v", i, line, err)
		}
		if value["schema_version"] != SchemaVersion {
			t.Fatalf("line %d schema_version = %v", i, value["schema_version"])
		}
	}
}

func TestErrorRecordIsTyped(t *testing.T) {
	record := ErrorRecordFrom(errors.New("boom"))
	if record.Type != "error" || record.Error == nil || record.Data != nil || record.SchemaVersion != SchemaVersion {
		t.Fatalf("ErrorRecordFrom = %+v", record)
	}
}

func TestErrorFromAssignsStableExitFamilies(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
		exit int
	}{
		{name: "invalid input", err: errors.New("unknown flag: --wat"), code: "invalid_input", exit: ExitInvalidInput},
		{name: "invalid negative bound", err: errors.New("--timeout must not be negative"), code: "invalid_input", exit: ExitInvalidInput},
		{name: "auth", err: errors.New("not authorized"), code: "not_authorized", exit: ExitAuthOrConfig},
		{name: "expired pending auth", err: tgapp.ErrPendingAuthExpired, code: "pending_auth_expired", exit: ExitAuthOrConfig},
		{name: "invalid pending auth", err: tgapp.ErrPendingAuthInvalid, code: "pending_auth_invalid", exit: ExitAuthOrConfig},
		{name: "timeout", err: context.DeadlineExceeded, code: "timeout", exit: ExitGeneral},
		{name: "canceled", err: context.Canceled, code: "canceled", exit: ExitGeneral},
		{name: "peer", err: errors.New("peer x not in cache"), code: "peer_not_found", exit: ExitNotFound},
		{name: "telegram", err: tgerr.New(400, "USERNAME_NOT_OCCUPIED"), code: "telegram_username_not_occupied", exit: ExitTelegram},
		{name: "output", err: errors.New("write stdout: broken pipe"), code: "output_failed", exit: ExitLocalIO},
		{name: "mutation", err: testMutationError{outcome: "outcome_unknown"}, code: "mutation_outcome_unknown", exit: ExitMutationReconcile},
		{name: "general", err: errors.New("boom"), code: "command_failed", exit: ExitGeneral},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ErrorFrom(tt.err).Error
			if got.Code != tt.code || got.ExitCode != tt.exit {
				t.Fatalf("ErrorFrom = code %q exit %d, want code %q exit %d", got.Code, got.ExitCode, tt.code, tt.exit)
			}
		})
	}
}
