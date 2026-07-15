package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestParseAPIID(t *testing.T) {
	id, err := ParseAPIID("123")
	if err != nil {
		t.Fatal(err)
	}
	if id != 123 {
		t.Fatalf("id = %d, want 123", id)
	}
	if _, err := ParseAPIID("0"); err == nil {
		t.Fatal("ParseAPIID accepted zero")
	}
	if _, err := ParseAPIID("nope"); err == nil {
		t.Fatal("ParseAPIID accepted non-number")
	}
}

func TestSafeDownloadFileName(t *testing.T) {
	got := safeDownloadFileName(42, "../weird:name.jpg")
	if got != "42-weird-name.jpg" {
		t.Fatalf("safeDownloadFileName = %q, want %q", got, "42-weird-name.jpg")
	}
}

func TestPlanDelete(t *testing.T) {
	tests := []struct {
		name       string
		input      tg.InputPeerClass
		scope      DeleteScope
		wantRoute  deleteRoute
		wantRevoke bool
		wantErr    string
	}{
		{name: "user for me", input: &tg.InputPeerUser{}, scope: DeleteScopeForMe, wantRoute: deleteRouteMessages},
		{name: "user revoke", input: &tg.InputPeerUser{}, scope: DeleteScopeRevoke, wantRoute: deleteRouteMessages, wantRevoke: true},
		{name: "basic group for me", input: &tg.InputPeerChat{}, scope: DeleteScopeForMe, wantRoute: deleteRouteMessages},
		{name: "basic group revoke", input: &tg.InputPeerChat{}, scope: DeleteScopeRevoke, wantRoute: deleteRouteMessages, wantRevoke: true},
		{name: "cached channel for me rejected", input: &tg.InputPeerChannel{ChannelID: 42, AccessHash: 7}, scope: DeleteScopeForMe, wantErr: "only be deleted with --revoke --yes"},
		{name: "cached supergroup for me rejected", input: &tg.InputPeerChannel{ChannelID: 42, AccessHash: 7}, scope: DeleteScopeForMe, wantErr: "only be deleted with --revoke --yes"},
		{name: "username resolved channel for me rejected", input: &tg.InputPeerChannel{ChannelID: 84, AccessHash: 9}, scope: DeleteScopeForMe, wantErr: "only be deleted with --revoke --yes"},
		{name: "channel revoke", input: &tg.InputPeerChannel{ChannelID: 42, AccessHash: 7}, scope: DeleteScopeRevoke, wantRoute: deleteRouteChannels},
		{name: "unknown scope", input: &tg.InputPeerUser{}, scope: DeleteScope("unknown"), wantErr: "unsupported delete scope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := planDelete(tt.input, tt.scope)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("planDelete error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Route != tt.wantRoute || got.Revoke != tt.wantRevoke {
				t.Fatalf("planDelete = %+v, want route %q revoke %t", got, tt.wantRoute, tt.wantRevoke)
			}
			if tt.wantRoute == deleteRouteChannels {
				if got.Channel == nil || got.Channel.ChannelID != 42 || got.Channel.AccessHash != 7 {
					t.Fatalf("planDelete channel = %+v, want id 42 hash 7", got.Channel)
				}
			}
		})
	}
}

func TestMutationFailureOutcomes(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		dispatched    bool
		wantOutcome   MutationOutcome
		wantRetrySafe bool
	}{
		{name: "pre-dispatch rejected", err: errors.New("resolve failed"), wantOutcome: MutationRejected, wantRetrySafe: true},
		{name: "telegram rejected", err: tgerr.New(400, "MESSAGE_ID_INVALID"), dispatched: true, wantOutcome: MutationRejected, wantRetrySafe: true},
		{name: "post-dispatch timeout unknown", err: context.DeadlineExceeded, dispatched: true, wantOutcome: MutationOutcomeUnknown},
		{name: "post-dispatch transport unknown", err: errors.New("connection reset"), dispatched: true, wantOutcome: MutationOutcomeUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := mutationFailure(tt.err, tt.dispatched, "handle:1")
			var mutationErr MutationError
			if !errors.As(err, &mutationErr) {
				t.Fatalf("mutationFailure returned %T, want MutationError", err)
			}
			if mutationErr.Outcome != tt.wantOutcome || mutationErr.RetrySafe != tt.wantRetrySafe {
				t.Fatalf("mutationFailure = %+v, want outcome %q retry_safe %t", mutationErr, tt.wantOutcome, tt.wantRetrySafe)
			}
		})
	}
}

func TestValidateMutationPreview(t *testing.T) {
	if err := validateMutationPreview("send", &tg.InputPeerUser{}, ""); err != nil {
		t.Fatal(err)
	}
	if err := validateMutationPreview("delete", &tg.InputPeerChannel{}, DeleteScopeForMe); err == nil {
		t.Fatal("validateMutationPreview accepted channel --for-me")
	}
	if err := validateMutationPreview("unknown", &tg.InputPeerUser{}, ""); err == nil {
		t.Fatal("validateMutationPreview accepted unknown action")
	}
}

func TestMutationResultConfirmedWithoutMessageID(t *testing.T) {
	got := mutationResult("send", "user:1", 0, "random_id:42")
	if !got.OK || got.Outcome != MutationConfirmed || got.RetrySafe || got.MessageID != 0 || got.ReconciliationHandle != "random_id:42" {
		t.Fatalf("mutationResult = %+v", got)
	}
}

func TestConfirmedMutationOutputError(t *testing.T) {
	err := ConfirmedMutationOutputError(MutationResult{ReconciliationHandle: "random_id:42"}, errors.New("broken pipe"))
	var mutationErr MutationError
	if !errors.As(err, &mutationErr) {
		t.Fatalf("ConfirmedMutationOutputError returned %T", err)
	}
	if mutationErr.Outcome != MutationConfirmed || mutationErr.RetrySafe || mutationErr.ReconciliationHandle != "random_id:42" {
		t.Fatalf("ConfirmedMutationOutputError = %+v", mutationErr)
	}
}

func TestChatsFromDialogsKeepsSameIDPreviewsScopedToPeer(t *testing.T) {
	user1 := &tg.User{ID: 1, AccessHash: 10, FirstName: "One"}
	user1.SetFlags()
	user2 := &tg.User{ID: 2, AccessHash: 20, FirstName: "Two"}
	user2.SetFlags()
	res := &tg.MessagesDialogsSlice{
		Dialogs: []tg.DialogClass{
			&tg.Dialog{Peer: &tg.PeerUser{UserID: 1}, TopMessage: 5},
			&tg.Dialog{Peer: &tg.PeerUser{UserID: 2}, TopMessage: 5},
		},
		Messages: []tg.MessageClass{
			&tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: 1}, Message: "one"},
			&tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: 2}, Message: "two"},
		},
		Users: []tg.UserClass{user1, user2},
	}
	chats, _ := chatsFromDialogs(res)
	if len(chats) != 2 || chats[0].LastMessagePreview != "one" || chats[1].LastMessagePreview != "two" {
		t.Fatalf("chatsFromDialogs previews = %+v", chats)
	}
}
