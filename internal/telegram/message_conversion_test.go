package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestConvertMessagesHydratesIdentityAndContext(t *testing.T) {
	user := &tg.User{ID: 10, AccessHash: 100, FirstName: "Alice", Username: "alice"}
	user.SetFlags()
	channel := &tg.Channel{ID: 20, AccessHash: 200, Title: "Builders", Username: "builders", Megagroup: true}
	channel.SetFlags()
	msg := &tg.Message{
		ID:        30,
		Date:      1000,
		PeerID:    &tg.PeerChannel{ChannelID: 20},
		FromID:    &tg.PeerUser{UserID: 10},
		Message:   "hello",
		ReplyTo:   &tg.MessageReplyHeader{ReplyToMsgID: 29, ReplyToTopID: 25, ForumTopic: true},
		FwdFrom:   tg.MessageFwdHeader{FromID: &tg.PeerUser{UserID: 10}, Date: 900},
		EditDate:  1100,
		GroupedID: 1234,
		Entities:  []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 5}},
	}
	msg.SetFlags()
	res := &tg.MessagesMessagesSlice{
		Messages: []tg.MessageClass{msg},
		Users:    []tg.UserClass{user},
		Chats:    []tg.ChatClass{channel},
	}
	messages, peers := convertMessages("wrong-fallback", res)
	if len(messages) != 1 || len(peers) != 2 {
		t.Fatalf("convertMessages returned %d messages and %d peers", len(messages), len(peers))
	}
	got := messages[0]
	if got.SourcePeerRef != "supergroup:20" || got.SourcePeerLabel != "Builders @builders" {
		t.Fatalf("source identity = %q %q", got.SourcePeerRef, got.SourcePeerLabel)
	}
	if got.SenderPeerRef != "user:10" || got.SenderLabel != "Alice @alice" {
		t.Fatalf("sender identity = %q %q", got.SenderPeerRef, got.SenderLabel)
	}
	if got.ReplyToMessageID != 29 || got.ThreadID != 25 || !got.ForumTopic {
		t.Fatalf("reply context = %+v", got)
	}
	if got.ForwardedFromPeerRef != "user:10" || got.ForwardedFromLabel != "Alice @alice" || got.ForwardedDate == "" {
		t.Fatalf("forward context = %+v", got)
	}
	if got.EditDate == "" || got.GroupedID != 1234 || len(got.Entities) != 1 || got.Entities[0].Type != "messageEntityBold" {
		t.Fatalf("edit/album/entities context = %+v", got)
	}
}

func TestConvertMessagesUsesActualSourceAndIncomingDirectSender(t *testing.T) {
	user := &tg.User{ID: 10, FirstName: "Alice"}
	user.SetFlags()
	res := &tg.MessagesMessages{
		Messages: []tg.MessageClass{&tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 10}, Message: "hello"}},
		Users:    []tg.UserClass{user},
	}
	messages, _ := convertMessages("wrong-fallback", res)
	got := messages[0]
	if got.SourcePeerRef != "user:10" || got.SenderPeerRef != "user:10" || got.SenderLabel != "Alice" {
		t.Fatalf("direct identity = %+v", got)
	}
}
