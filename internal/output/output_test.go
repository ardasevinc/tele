package output

import (
	"errors"
	"testing"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tgerr"
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
