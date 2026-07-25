package telegram

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
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

type fakeBotCreationAPI struct {
	available bool
	checkErr  error
	createErr error
	request   *tg.BotsCreateBotRequest
	created   tg.UserClass
}

func (f *fakeBotCreationAPI) BotsCheckUsername(_ context.Context, _ string) (bool, error) {
	return f.available, f.checkErr
}

func (f *fakeBotCreationAPI) BotsCreateBot(
	_ context.Context,
	request *tg.BotsCreateBotRequest,
) (tg.UserClass, error) {
	f.request = request
	return f.created, f.createErr
}

func TestCreateManagedBotAssignsVerifiedManager(t *testing.T) {
	user := &tg.User{ID: 42, Bot: true}
	user.SetAccessHash(99)
	user.SetUsername("FactoryChildBot")
	api := &fakeBotCreationAPI{available: true, created: user}
	manager := &tg.InputPeerUser{UserID: 7, AccessHash: 8}

	attempt := createManagedBot(
		context.Background(),
		api,
		"FactoryChildBot",
		"Factory Child",
		manager,
		"@ManagerBot",
	)
	if attempt.err != nil {
		t.Fatal(attempt.err)
	}
	if !attempt.dispatched || !attempt.confirmed {
		t.Fatalf("attempt = %+v", attempt)
	}
	if attempt.bot.ID != 42 || attempt.bot.AccessHash != 99 ||
		attempt.bot.ManagerID != 7 || attempt.bot.ManagerUsername != "ManagerBot" {
		t.Fatalf("bot = %+v", attempt.bot)
	}
	input, ok := api.request.ManagerID.(*tg.InputUser)
	if !ok || input.UserID != 7 || input.AccessHash != 8 {
		t.Fatalf("manager input = %#v", api.request.ManagerID)
	}
	if api.request.Username != "FactoryChildBot" || api.request.Name != "Factory Child" {
		t.Fatalf("request = %+v", api.request)
	}
}

func TestCreateManagedBotDoesNotDispatchUnavailableUsername(t *testing.T) {
	api := &fakeBotCreationAPI{}
	attempt := createManagedBot(
		context.Background(),
		api,
		"UnavailableBot",
		"Unavailable",
		&tg.InputPeerUser{UserID: 7, AccessHash: 8},
		"ManagerBot",
	)
	if attempt.err == nil || attempt.dispatched || api.request != nil {
		t.Fatalf("attempt = %+v request = %+v", attempt, api.request)
	}
}

func TestCreateManagedBotMarksInvalidSuccessAsConfirmed(t *testing.T) {
	api := &fakeBotCreationAPI{
		available: true,
		created:   &tg.User{ID: 42},
	}
	attempt := createManagedBot(
		context.Background(),
		api,
		"FactoryChildBot",
		"Factory Child",
		&tg.InputPeerUser{UserID: 7, AccessHash: 8},
		"ManagerBot",
	)
	if attempt.err == nil || !attempt.confirmed {
		t.Fatalf("attempt = %+v", attempt)
	}
}

func TestBotCreationFailureContract(t *testing.T) {
	transportErr := errors.New("connection lost")
	err := mutationFailure(transportErr, true, "managed-bot:@FactoryChildBot")
	var mutationErr MutationError
	if !errors.As(err, &mutationErr) {
		t.Fatalf("error = %T, want MutationError", err)
	}
	if mutationErr.Outcome != MutationOutcomeUnknown || mutationErr.RetrySafe {
		t.Fatalf("mutation error = %+v", mutationErr)
	}
}
