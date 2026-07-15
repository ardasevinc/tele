package telegram

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/ardasevinc/tele/internal/peerstore"
)

type fakeMutationAPI struct {
	editRequest          *tg.MessagesEditMessageRequest
	reactionRequest      *tg.MessagesSendReactionRequest
	deleteRequest        *tg.MessagesDeleteMessagesRequest
	channelDeleteRequest *tg.ChannelsDeleteMessagesRequest
	sendRequest          *tg.MessagesSendMessageRequest
	err                  error
}

func (f *fakeMutationAPI) MessagesEditMessage(_ context.Context, req *tg.MessagesEditMessageRequest) (tg.UpdatesClass, error) {
	f.editRequest = req
	return &tg.Updates{}, f.err
}

func (f *fakeMutationAPI) MessagesSendReaction(_ context.Context, req *tg.MessagesSendReactionRequest) (tg.UpdatesClass, error) {
	f.reactionRequest = req
	return &tg.Updates{}, f.err
}

func (f *fakeMutationAPI) MessagesDeleteMessages(_ context.Context, req *tg.MessagesDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error) {
	f.deleteRequest = req
	return &tg.MessagesAffectedMessages{}, f.err
}

func (f *fakeMutationAPI) ChannelsDeleteMessages(_ context.Context, req *tg.ChannelsDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error) {
	f.channelDeleteRequest = req
	return &tg.MessagesAffectedMessages{}, f.err
}

func (f *fakeMutationAPI) MessagesSendMessage(_ context.Context, req *tg.MessagesSendMessageRequest) (tg.UpdatesClass, error) {
	f.sendRequest = req
	return &tg.UpdateShortSentMessage{ID: 91}, f.err
}

func resolvedPeer(input tg.InputPeerClass, ref string) mutationResolver {
	return func(context.Context, string) (tg.InputPeerClass, peerstore.Peer, error) {
		return input, peerstore.Peer{Ref: ref}, nil
	}
}

func TestPerformSendBuildsReplyAndReceipt(t *testing.T) {
	api := &fakeMutationAPI{}
	attempt := performSend(context.Background(), api, resolvedPeer(&tg.InputPeerUser{UserID: 42, AccessHash: 7}, "user:42"), "@alice", "hello", 12, "reply", 99)
	if attempt.err != nil {
		t.Fatal(attempt.err)
	}
	if !attempt.dispatched || api.sendRequest == nil {
		t.Fatalf("send attempt = %+v, request = %+v", attempt, api.sendRequest)
	}
	if api.sendRequest.Message != "hello" || api.sendRequest.RandomID != 99 || !api.sendRequest.NoWebpage {
		t.Fatalf("send request = %+v", api.sendRequest)
	}
	reply, ok := api.sendRequest.GetReplyTo()
	if !ok {
		t.Fatal("send request has no reply target")
	}
	if target, ok := reply.(*tg.InputReplyToMessage); !ok || target.ReplyToMsgID != 12 {
		t.Fatalf("reply target = %#v", reply)
	}
	if got := attempt.result; !got.OK || got.Action != "reply" || got.PeerRef != "user:42" || got.MessageID != 91 || got.ReconciliationHandle != "random_id:99" {
		t.Fatalf("result = %+v", got)
	}
}

func TestPerformEditBuildsTrimmedMessage(t *testing.T) {
	api := &fakeMutationAPI{}
	attempt := performEdit(context.Background(), api, resolvedPeer(&tg.InputPeerUser{}, "user:42"), "alice", 17, "  corrected  ")
	if attempt.err != nil {
		t.Fatal(attempt.err)
	}
	message, ok := api.editRequest.GetMessage()
	if !ok || message != "corrected" || api.editRequest.ID != 17 {
		t.Fatalf("edit request = %+v, message = %q set=%t", api.editRequest, message, ok)
	}
	if attempt.result.ReconciliationHandle != "action:edit/peer:user:42/message:17" {
		t.Fatalf("result = %+v", attempt.result)
	}
}

func TestPerformReactBuildsEmojiReaction(t *testing.T) {
	api := &fakeMutationAPI{}
	attempt := performReact(context.Background(), api, resolvedPeer(&tg.InputPeerUser{}, "user:42"), "alice", 18, "  👍  ")
	if attempt.err != nil {
		t.Fatal(attempt.err)
	}
	reactions, ok := api.reactionRequest.GetReaction()
	if !ok || len(reactions) != 1 || !api.reactionRequest.AddToRecent {
		t.Fatalf("reaction request = %+v", api.reactionRequest)
	}
	emoji, ok := reactions[0].(*tg.ReactionEmoji)
	if !ok || emoji.Emoticon != "👍" {
		t.Fatalf("reaction = %#v", reactions[0])
	}
}

func TestPerformDeleteRoutesByPeerKindAndScope(t *testing.T) {
	t.Run("direct for me", func(t *testing.T) {
		api := &fakeMutationAPI{}
		attempt := performDelete(context.Background(), api, resolvedPeer(&tg.InputPeerUser{}, "user:42"), "alice", 19, DeleteScopeForMe)
		if attempt.err != nil {
			t.Fatal(attempt.err)
		}
		if api.deleteRequest == nil || api.deleteRequest.GetRevoke() || api.channelDeleteRequest != nil {
			t.Fatalf("delete requests = messages %+v channel %+v", api.deleteRequest, api.channelDeleteRequest)
		}
	})

	t.Run("channel revoke", func(t *testing.T) {
		api := &fakeMutationAPI{}
		attempt := performDelete(context.Background(), api, resolvedPeer(&tg.InputPeerChannel{ChannelID: 51, AccessHash: 8}, "channel:51"), "news", 20, DeleteScopeRevoke)
		if attempt.err != nil {
			t.Fatal(attempt.err)
		}
		if api.channelDeleteRequest == nil {
			t.Fatal("channel delete request was not sent")
		}
		channel, ok := api.channelDeleteRequest.Channel.(*tg.InputChannel)
		if api.deleteRequest != nil || !ok || channel.ChannelID != 51 || api.channelDeleteRequest.ID[0] != 20 {
			t.Fatalf("delete requests = messages %+v channel %+v", api.deleteRequest, api.channelDeleteRequest)
		}
		if len(attempt.result.MessageIDs) != 1 || attempt.result.MessageIDs[0] != 20 {
			t.Fatalf("result = %+v", attempt.result)
		}
	})
}

func TestPerformDeleteRejectsChannelForMeBeforeDispatch(t *testing.T) {
	api := &fakeMutationAPI{}
	attempt := performDelete(context.Background(), api, resolvedPeer(&tg.InputPeerChannel{}, "channel:51"), "news", 20, DeleteScopeForMe)
	if attempt.err == nil || attempt.dispatched || api.deleteRequest != nil || api.channelDeleteRequest != nil {
		t.Fatalf("attempt = %+v, requests = %+v %+v", attempt, api.deleteRequest, api.channelDeleteRequest)
	}
}

func TestMutationResolverFailureIsNotDispatched(t *testing.T) {
	wantErr := errors.New("resolve failed")
	resolve := func(context.Context, string) (tg.InputPeerClass, peerstore.Peer, error) {
		return nil, peerstore.Peer{}, wantErr
	}
	api := &fakeMutationAPI{}
	attempt := performSend(context.Background(), api, resolve, "missing", "hello", 0, "send", 100)
	if !errors.Is(attempt.err, wantErr) || attempt.dispatched || api.sendRequest != nil || attempt.handle != "random_id:100" {
		t.Fatalf("attempt = %+v, request = %+v", attempt, api.sendRequest)
	}
}

func TestMutationAPIFailurePreservesDispatchedState(t *testing.T) {
	wantErr := errors.New("transport failed")
	api := &fakeMutationAPI{err: wantErr}
	attempt := performEdit(context.Background(), api, resolvedPeer(&tg.InputPeerUser{}, "user:42"), "alice", 21, "hello")
	if !errors.Is(attempt.err, wantErr) || !attempt.dispatched || api.editRequest == nil || attempt.handle != "action:edit/peer:user:42/message:21" {
		t.Fatalf("attempt = %+v, request = %+v", attempt, api.editRequest)
	}
}

func TestPreviewMutationUsesCanonicalPeerAndValidatesDelete(t *testing.T) {
	preview, err := previewMutation(context.Background(), resolvedPeer(&tg.InputPeerUser{}, "user:42"), "delete", "alice", 22, DeleteScopeForMe)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.OK || !preview.DryRun || preview.PeerRef != "user:42" || preview.MessageID != 22 {
		t.Fatalf("preview = %+v", preview)
	}
	if _, err := previewMutation(context.Background(), resolvedPeer(&tg.InputPeerChannel{}, "channel:51"), "delete", "news", 22, DeleteScopeForMe); err == nil {
		t.Fatal("channel for-me preview was accepted")
	}
}
