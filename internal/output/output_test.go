package output

import (
	"errors"
	"testing"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tgerr"
)

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
