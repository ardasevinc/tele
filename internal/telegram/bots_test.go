package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

type fakeBotUsernameAPI struct {
	username  string
	available bool
	err       error
}

type fakeOwnedBotsAPI struct {
	users       []tg.UserClass
	full        map[int64]*tg.UsersUserFull
	listErr     error
	fullErr     error
	fullUserIDs []int64
}

func (f *fakeOwnedBotsAPI) BotsGetAdminedBots(context.Context) ([]tg.UserClass, error) {
	return f.users, f.listErr
}

func (f *fakeOwnedBotsAPI) UsersGetFullUser(_ context.Context, input tg.InputUserClass) (*tg.UsersUserFull, error) {
	user, ok := input.(*tg.InputUser)
	if !ok {
		return nil, errors.New("unexpected input user")
	}
	f.fullUserIDs = append(f.fullUserIDs, user.UserID)
	if f.fullErr != nil {
		return nil, f.fullErr
	}
	return f.full[user.UserID], nil
}

func ownedBotUser(id, accessHash int64, username, firstName, lastName string) *tg.User {
	user := &tg.User{ID: id, Bot: true}
	user.SetAccessHash(accessHash)
	user.SetUsername(username)
	if firstName != "" {
		user.SetFirstName(firstName)
	}
	if lastName != "" {
		user.SetLastName(lastName)
	}
	return user
}

func ownedBotFull(id, managerID int64) *tg.UsersUserFull {
	full := &tg.UsersUserFull{FullUser: tg.UserFull{ID: id}}
	if managerID != 0 {
		full.FullUser.SetBotManagerID(managerID)
	}
	return full
}

func TestListOwnedBotsDiscoversManagerIdentityAndSorts(t *testing.T) {
	api := &fakeOwnedBotsAPI{
		users: []tg.UserClass{
			ownedBotUser(2, 22, "ZuluBot", "Zulu", "Worker"),
			ownedBotUser(1, 11, "AlphaBot", "", ""),
		},
		full: map[int64]*tg.UsersUserFull{
			1: ownedBotFull(1, 7),
			2: ownedBotFull(2, 0),
		},
	}
	bots, err := listOwnedBots(context.Background(), api)
	if err != nil {
		t.Fatal(err)
	}
	if len(bots) != 2 || bots[0].ID != 1 || bots[0].Name != "AlphaBot" || bots[0].ManagerID != 7 ||
		bots[1].ID != 2 || bots[1].Name != "Zulu Worker" || bots[1].ManagerID != 0 {
		t.Fatalf("bots = %+v", bots)
	}
	if len(api.fullUserIDs) != 2 || api.fullUserIDs[0] != 2 || api.fullUserIDs[1] != 1 {
		t.Fatalf("full user calls = %v", api.fullUserIDs)
	}
}

func TestListOwnedBotsRejectsIncompleteAndDuplicateCatalogs(t *testing.T) {
	tests := []struct {
		name  string
		users []tg.UserClass
		full  map[int64]*tg.UsersUserFull
		want  string
	}{
		{name: "empty user", users: []tg.UserClass{&tg.UserEmpty{ID: 1}}, want: "invalid owned-bot identity"},
		{name: "missing username", users: []tg.UserClass{func() *tg.User { u := &tg.User{ID: 1, Bot: true}; u.SetAccessHash(2); return u }()}, want: "has no username"},
		{name: "duplicate ID", users: []tg.UserClass{ownedBotUser(1, 1, "OneBot", "", ""), ownedBotUser(1, 2, "TwoBot", "", "")}, full: map[int64]*tg.UsersUserFull{1: ownedBotFull(1, 0)}, want: "duplicate owned bot ID"},
		{name: "duplicate username", users: []tg.UserClass{ownedBotUser(1, 1, "SameBot", "", ""), ownedBotUser(2, 2, "samebot", "", "")}, full: map[int64]*tg.UsersUserFull{1: ownedBotFull(1, 0)}, want: "duplicate owned bot username"},
		{name: "mismatched full user", users: []tg.UserClass{ownedBotUser(1, 1, "OneBot", "", "")}, full: map[int64]*tg.UsersUserFull{1: ownedBotFull(2, 0)}, want: "mismatched full identity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := listOwnedBots(context.Background(), &fakeOwnedBotsAPI{users: tt.users, full: tt.full})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
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
