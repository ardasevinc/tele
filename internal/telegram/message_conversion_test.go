package telegram

import (
	"strings"
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

func TestConvertMessagesCoversGroupAndChannelIdentityShapes(t *testing.T) {
	user := &tg.User{ID: 10, AccessHash: 100, FirstName: "Alice"}
	user.SetFlags()
	group := &tg.Chat{ID: 30, Title: "Basic Group"}
	channel := &tg.Channel{ID: 40, AccessHash: 400, Title: "News"}
	channel.SetFlags()
	supergroup := &tg.Channel{ID: 50, AccessHash: 500, Title: "Anon Group", Megagroup: true}
	supergroup.SetFlags()

	tests := []struct {
		name        string
		message     *tg.Message
		chats       []tg.ChatClass
		sourceRef   string
		senderRef   string
		senderLabel string
	}{
		{
			name:        "basic group",
			message:     &tg.Message{ID: 1, PeerID: &tg.PeerChat{ChatID: 30}, FromID: &tg.PeerUser{UserID: 10}, Message: "hello"},
			chats:       []tg.ChatClass{group},
			sourceRef:   "chat:30",
			senderRef:   "user:10",
			senderLabel: "Alice",
		},
		{
			name:        "channel post",
			message:     &tg.Message{ID: 2, PeerID: &tg.PeerChannel{ChannelID: 40}, Message: "news", Post: true},
			chats:       []tg.ChatClass{channel},
			sourceRef:   "channel:40",
			senderRef:   "channel:40",
			senderLabel: "News",
		},
		{
			name:        "anonymous admin",
			message:     &tg.Message{ID: 3, PeerID: &tg.PeerChannel{ChannelID: 50}, FromID: &tg.PeerChannel{ChannelID: 50}, Message: "admin"},
			chats:       []tg.ChatClass{supergroup},
			sourceRef:   "supergroup:50",
			senderRef:   "supergroup:50",
			senderLabel: "Anon Group",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.message.SetFlags()
			messages, _ := convertMessages("", &tg.MessagesMessages{
				Messages: []tg.MessageClass{tt.message},
				Users:    []tg.UserClass{user},
				Chats:    tt.chats,
			})
			got := messages[0]
			if got.SourcePeerRef != tt.sourceRef || got.SenderPeerRef != tt.senderRef || got.SenderLabel != tt.senderLabel {
				t.Fatalf("identity = source %q sender %q label %q", got.SourcePeerRef, got.SenderPeerRef, got.SenderLabel)
			}
		})
	}
}

func TestConvertMessagesRepresentsHiddenForwardAndMissingEntityHonestly(t *testing.T) {
	message := &tg.Message{
		ID:      1,
		PeerID:  &tg.PeerUser{UserID: 99},
		FromID:  &tg.PeerUser{UserID: 99},
		Message: "forwarded",
		FwdFrom: tg.MessageFwdHeader{FromName: "Hidden Sender", Date: 900},
	}
	message.SetFlags()
	messages, peers := convertMessages("", &tg.MessagesMessages{Messages: []tg.MessageClass{message}})
	got := messages[0]
	if got.SourcePeerRef != "user:99" || got.SourcePeerLabel != "" || got.SenderPeerRef != "user:99" || got.SenderLabel != "" {
		t.Fatalf("missing entity was embellished: %+v", got)
	}
	if got.ForwardedFromPeerRef != "" || got.ForwardedFromLabel != "Hidden Sender" {
		t.Fatalf("hidden forward identity = ref %q label %q", got.ForwardedFromPeerRef, got.ForwardedFromLabel)
	}
	if len(peers) != 0 {
		t.Fatalf("missing entity created %d resolvable peers", len(peers))
	}
}

func TestConvertMessagesMarksTelegramLoginCodeRedaction(t *testing.T) {
	service := &tg.User{ID: 777000, AccessHash: 1, FirstName: "Telegram"}
	service.SetFlags()
	message := &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 777000}, Message: "Login code: 12345"}
	message.SetFlags()
	messages, _ := convertMessages("", &tg.MessagesMessages{
		Messages: []tg.MessageClass{message},
		Users:    []tg.UserClass{service},
	})
	got := messages[0]
	if strings.Contains(got.Text, "12345") || len(got.Redactions) != 1 || got.Redactions[0] != "telegram_login_code" {
		t.Fatalf("converted service message = %+v", got)
	}
}
