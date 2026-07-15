package telegram

import (
	"strconv"
	"strings"

	"github.com/gotd/td/tg"

	"github.com/ardasevinc/tele/internal/peerstore"
)

type peerIdentity struct {
	Ref   string
	Label string
}

func convertMessages(sourceFallback string, res tg.MessagesMessagesClass) ([]Message, []peerstore.Peer) {
	identities, peers := messagePeerCatalog(res)
	classes := messageClasses(res)
	out := make([]Message, 0, len(classes))
	for _, class := range classes {
		switch msg := class.(type) {
		case *tg.Message:
			item := Message{
				ID:       msg.ID,
				Date:     unixDate(msg.Date),
				Text:     msg.Message,
				Outgoing: msg.Out,
				Post:     msg.Post,
			}
			hydrateMessageIdentity(&item, msg.PeerID, msg.FromID, sourceFallback, identities)
			item.Text, item.Redactions = redactMessageText(item.SourcePeerRef, item.Text)
			if media, ok := msg.GetMedia(); ok {
				item.Media = media.TypeName()
			}
			if reply, ok := msg.ReplyTo.(*tg.MessageReplyHeader); ok {
				item.ReplyToMessageID = reply.ReplyToMsgID
				item.ThreadID = reply.ReplyToTopID
				item.ForumTopic = reply.ForumTopic
			}
			if forward, ok := msg.GetFwdFrom(); ok {
				identity := identityForPeer(forward.FromID, identities)
				item.ForwardedFromPeerRef = identity.Ref
				item.ForwardedFromLabel = firstIdentityLabel(identity.Label, forward.FromName, forward.PostAuthor)
				item.ForwardedDate = unixDate(forward.Date)
			}
			if editDate, ok := msg.GetEditDate(); ok {
				item.EditDate = unixDate(editDate)
			}
			if groupedID, ok := msg.GetGroupedID(); ok {
				item.GroupedID = groupedID
			}
			for _, entity := range msg.Entities {
				item.Entities = append(item.Entities, MessageEntity{
					Type:   entity.TypeName(),
					Offset: entity.GetOffset(),
					Length: entity.GetLength(),
				})
			}
			out = append(out, item)
		case *tg.MessageService:
			item := Message{
				ID:       msg.ID,
				Date:     unixDate(msg.Date),
				Outgoing: msg.Out,
				Post:     msg.Post,
				Service:  msg.Action.TypeName(),
			}
			hydrateMessageIdentity(&item, msg.PeerID, msg.FromID, sourceFallback, identities)
			out = append(out, item)
		}
	}
	return out, peers
}

func hydrateMessageIdentity(item *Message, source, sender tg.PeerClass, sourceFallback string, identities map[string]peerIdentity) {
	sourceIdentity := identityForPeer(source, identities)
	item.SourcePeerRef = firstIdentityLabel(sourceIdentity.Ref, sourceFallback)
	item.SourcePeerLabel = sourceIdentity.Label
	senderIdentity := identityForPeer(sender, identities)
	if senderIdentity.Ref == "" && (item.Post || !item.Outgoing) {
		senderIdentity = sourceIdentity
	}
	item.SenderPeerRef = senderIdentity.Ref
	item.SenderLabel = senderIdentity.Label
}

func messagePeerCatalog(res tg.MessagesMessagesClass) (map[string]peerIdentity, []peerstore.Peer) {
	identities := map[string]peerIdentity{}
	var peers []peerstore.Peer
	users, chats := messageResultPeers(res)
	for _, class := range users {
		user, ok := class.(*tg.User)
		if !ok {
			continue
		}
		key := "user:" + formatPeerID(user.ID)
		username, _ := user.GetUsername()
		label := strings.TrimSpace(user.FirstName + " " + user.LastName)
		if username != "" {
			if label != "" {
				label += " "
			}
			label += "@" + strings.TrimPrefix(username, "@")
		}
		identities[key] = peerIdentity{Ref: key, Label: label}
		if peer, ok := peerstore.FromUser(user); ok {
			peers = append(peers, peer)
			identities[key] = peerIdentity{Ref: peer.Ref, Label: peerDisplayLabel(peer)}
		}
	}
	for _, class := range chats {
		var peer peerstore.Peer
		var ok bool
		switch value := class.(type) {
		case *tg.Chat:
			peer, ok = peerstore.FromChat(value)
		case *tg.Channel:
			key := "channel:" + formatPeerID(value.ID)
			username, _ := value.GetUsername()
			label := strings.TrimSpace(value.Title)
			if username != "" {
				if label != "" {
					label += " "
				}
				label += "@" + strings.TrimPrefix(username, "@")
			}
			identities[key] = peerIdentity{Ref: key, Label: label}
			peer, ok = peerstore.FromChannel(value)
		}
		if !ok {
			continue
		}
		peers = append(peers, peer)
		identities[peerKeyForKind(peer.Kind, peer.ID)] = peerIdentity{Ref: peer.Ref, Label: peerDisplayLabel(peer)}
		if peer.Kind == "supergroup" {
			identities["channel:"+formatPeerID(peer.ID)] = peerIdentity{Ref: peer.Ref, Label: peerDisplayLabel(peer)}
		}
	}
	return identities, peers
}

func messageResultPeers(res tg.MessagesMessagesClass) ([]tg.UserClass, []tg.ChatClass) {
	switch value := res.(type) {
	case *tg.MessagesMessages:
		return value.Users, value.Chats
	case *tg.MessagesMessagesSlice:
		return value.Users, value.Chats
	case *tg.MessagesChannelMessages:
		return value.Users, value.Chats
	default:
		return nil, nil
	}
}

func identityForPeer(peer tg.PeerClass, identities map[string]peerIdentity) peerIdentity {
	key := peerKey(peer)
	if identity, ok := identities[key]; ok {
		return identity
	}
	return peerIdentity{Ref: key}
}

func peerDisplayLabel(peer peerstore.Peer) string {
	label := strings.TrimSpace(peer.Title)
	if peer.Username != "" {
		username := "@" + strings.TrimPrefix(peer.Username, "@")
		if label == "" {
			return username
		}
		return label + " " + username
	}
	return label
}

func peerKeyForKind(kind string, id int64) string {
	if kind == "supergroup" {
		kind = "channel"
	}
	return kind + ":" + formatPeerID(id)
}

func formatPeerID(id int64) string {
	return strconv.FormatInt(id, 10)
}

func firstIdentityLabel(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
