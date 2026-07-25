package telegram

import (
	"context"
	"testing"
	"time"
)

type fakeBotUsernameAPI struct {
	username  string
	available bool
	err       error
}

func (f *fakeBotUsernameAPI) BotsCheckUsername(_ context.Context, username string) (bool, error) {
	f.username = username
	return f.available, f.err
}

func TestNormalizeBotUsername(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  string
	}{
		{input: "@GermanShepherdBot", want: "GermanShepherdBot"},
		{input: "agent_bot", want: "agent_bot"},
		{input: "12345bot", want: "12345bot"},
	} {
		got, err := NormalizeBotUsername(tt.input)
		if err != nil {
			t.Fatalf("NormalizeBotUsername(%q): %v", tt.input, err)
		}
		if got != tt.want {
			t.Fatalf("NormalizeBotUsername(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeBotUsernameRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"bot", "not-a-bot", "missing_suffix", "this_username_is_much_too_long_for_a_telegram_bot"} {
		if _, err := NormalizeBotUsername(value); err == nil {
			t.Fatalf("NormalizeBotUsername accepted %q", value)
		}
	}
}

func TestCheckBotUsername(t *testing.T) {
	api := &fakeBotUsernameAPI{available: true}
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	got, err := checkBotUsername(context.Background(), api, "AgentBot", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if api.username != "AgentBot" || got.Username != "AgentBot" || !got.Available || got.CheckedAt != now.Format(time.RFC3339) {
		t.Fatalf("request=%q result=%+v", api.username, got)
	}
}
