package telegram

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/ardasevinc/tele/internal/peerstore"
)

type mutationAPI interface {
	MessagesEditMessage(context.Context, *tg.MessagesEditMessageRequest) (tg.UpdatesClass, error)
	MessagesSendReaction(context.Context, *tg.MessagesSendReactionRequest) (tg.UpdatesClass, error)
	MessagesDeleteMessages(context.Context, *tg.MessagesDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error)
	ChannelsDeleteMessages(context.Context, *tg.ChannelsDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error)
	MessagesSendMessage(context.Context, *tg.MessagesSendMessageRequest) (tg.UpdatesClass, error)
}

type mutationResolver func(context.Context, string) (tg.InputPeerClass, peerstore.Peer, error)

type mutationAttempt struct {
	result     MutationResult
	handle     string
	dispatched bool
	err        error
}

func (a App) Send(ctx context.Context, peerToken, text string, replyTo int) (MutationResult, error) {
	return a.send(ctx, peerToken, text, replyTo, "send")
}

func (a App) PreviewMutation(ctx context.Context, action, peerToken string, msgID int, scope DeleteScope) (MutationPreview, error) {
	var out MutationPreview
	err := a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		var err error
		out, err = previewMutation(ctx, a.resolver(c), action, peerToken, msgID, scope)
		return err
	})
	return out, err
}

func previewMutation(ctx context.Context, resolve mutationResolver, action, peerToken string, msgID int, scope DeleteScope) (MutationPreview, error) {
	input, peerRef, err := resolve(ctx, peerToken)
	if err != nil {
		return MutationPreview{}, err
	}
	if err := validateMutationPreview(action, input, scope); err != nil {
		return MutationPreview{}, err
	}
	return MutationPreview{
		OK:        true,
		DryRun:    true,
		Action:    action,
		PeerRef:   peerRef.Ref,
		MessageID: msgID,
		Scope:     scope,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func validateMutationPreview(action string, input tg.InputPeerClass, scope DeleteScope) error {
	switch action {
	case "send", "reply", "react", "edit":
		return nil
	case "delete":
		_, err := planDelete(input, scope)
		return err
	default:
		return fmt.Errorf("unsupported mutation action %q", action)
	}
}

func (a App) Edit(ctx context.Context, peerToken string, msgID int, text string) (MutationResult, error) {
	attempt := mutationAttempt{handle: mutationHandle("edit", peerToken, msgID)}
	err := a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		attempt = performEdit(ctx, c.API(), a.resolver(c), peerToken, msgID, text)
		return attemptError(attempt)
	})
	return attempt.result, mutationFailure(err, attempt.dispatched, attempt.handle)
}

func performEdit(ctx context.Context, api mutationAPI, resolve mutationResolver, peerToken string, msgID int, text string) mutationAttempt {
	attempt := mutationAttempt{handle: mutationHandle("edit", peerToken, msgID)}
	input, peerRef, err := resolve(ctx, peerToken)
	if err != nil {
		return failedAttempt(attempt, err)
	}
	attempt.handle = mutationHandle("edit", peerRef.Ref, msgID)
	req := &tg.MessagesEditMessageRequest{Peer: input, ID: msgID}
	req.SetMessage(strings.TrimSpace(text))
	attempt.dispatched = true
	if _, err := api.MessagesEditMessage(ctx, req); err != nil {
		return failedAttempt(attempt, err)
	}
	attempt.result = mutationResult("edit", peerRef.Ref, msgID, attempt.handle)
	return attempt
}

func (a App) React(ctx context.Context, peerToken string, msgID int, emoji string) (MutationResult, error) {
	attempt := mutationAttempt{handle: mutationHandle("react", peerToken, msgID)}
	err := a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		attempt = performReact(ctx, c.API(), a.resolver(c), peerToken, msgID, emoji)
		return attemptError(attempt)
	})
	return attempt.result, mutationFailure(err, attempt.dispatched, attempt.handle)
}

func performReact(ctx context.Context, api mutationAPI, resolve mutationResolver, peerToken string, msgID int, emoji string) mutationAttempt {
	attempt := mutationAttempt{handle: mutationHandle("react", peerToken, msgID)}
	input, peerRef, err := resolve(ctx, peerToken)
	if err != nil {
		return failedAttempt(attempt, err)
	}
	attempt.handle = mutationHandle("react", peerRef.Ref, msgID)
	req := &tg.MessagesSendReactionRequest{Peer: input, MsgID: msgID, AddToRecent: true}
	req.SetReaction([]tg.ReactionClass{&tg.ReactionEmoji{Emoticon: strings.TrimSpace(emoji)}})
	attempt.dispatched = true
	if _, err := api.MessagesSendReaction(ctx, req); err != nil {
		return failedAttempt(attempt, err)
	}
	attempt.result = mutationResult("react", peerRef.Ref, msgID, attempt.handle)
	return attempt
}

func (a App) DeleteMessage(ctx context.Context, peerToken string, msgID int, scope DeleteScope) (MutationResult, error) {
	attempt := mutationAttempt{handle: mutationHandle("delete", peerToken, msgID)}
	err := a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		attempt = performDelete(ctx, c.API(), a.resolver(c), peerToken, msgID, scope)
		return attemptError(attempt)
	})
	return attempt.result, mutationFailure(err, attempt.dispatched, attempt.handle)
}

func performDelete(ctx context.Context, api mutationAPI, resolve mutationResolver, peerToken string, msgID int, scope DeleteScope) mutationAttempt {
	attempt := mutationAttempt{handle: mutationHandle("delete", peerToken, msgID)}
	input, peerRef, err := resolve(ctx, peerToken)
	if err != nil {
		return failedAttempt(attempt, err)
	}
	plan, err := planDelete(input, scope)
	if err != nil {
		return failedAttempt(attempt, err)
	}
	attempt.handle = mutationHandle("delete", peerRef.Ref, msgID)
	attempt.dispatched = true
	switch plan.Route {
	case deleteRouteChannels:
		_, err = api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{Channel: plan.Channel, ID: []int{msgID}})
	case deleteRouteMessages:
		req := &tg.MessagesDeleteMessagesRequest{ID: []int{msgID}}
		req.SetRevoke(plan.Revoke)
		_, err = api.MessagesDeleteMessages(ctx, req)
	default:
		err = fmt.Errorf("unsupported delete route %q", plan.Route)
	}
	if err != nil {
		return failedAttempt(attempt, err)
	}
	attempt.result = mutationResult("delete", peerRef.Ref, msgID, attempt.handle)
	attempt.result.MessageIDs = []int{msgID}
	return attempt
}

func planDelete(input tg.InputPeerClass, scope DeleteScope) (deletePlan, error) {
	if scope != DeleteScopeForMe && scope != DeleteScopeRevoke {
		return deletePlan{}, fmt.Errorf("unsupported delete scope %q", scope)
	}
	if channel, ok := input.(*tg.InputPeerChannel); ok {
		if scope != DeleteScopeRevoke {
			return deletePlan{}, fmt.Errorf("channel and supergroup messages can only be deleted with --revoke --yes")
		}
		return deletePlan{Route: deleteRouteChannels, Channel: &tg.InputChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash}}, nil
	}
	return deletePlan{Route: deleteRouteMessages, Revoke: scope == DeleteScopeRevoke}, nil
}

func (a App) send(ctx context.Context, peerToken, text string, replyTo int, action string) (MutationResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return MutationResult{}, fmt.Errorf("message text is required")
	}
	requestID := randomID()
	attempt := mutationAttempt{handle: "random_id:" + strconv.FormatInt(requestID, 10)}
	err := a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		attempt = performSend(ctx, c.API(), a.resolver(c), peerToken, text, replyTo, action, requestID)
		return attemptError(attempt)
	})
	return attempt.result, mutationFailure(err, attempt.dispatched, attempt.handle)
}

func performSend(ctx context.Context, api mutationAPI, resolve mutationResolver, peerToken, text string, replyTo int, action string, requestID int64) mutationAttempt {
	attempt := mutationAttempt{handle: "random_id:" + strconv.FormatInt(requestID, 10)}
	input, peerRef, err := resolve(ctx, peerToken)
	if err != nil {
		return failedAttempt(attempt, err)
	}
	req := &tg.MessagesSendMessageRequest{Peer: input, Message: text, RandomID: requestID, NoWebpage: true}
	if replyTo > 0 {
		req.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: replyTo})
	}
	attempt.dispatched = true
	updates, err := api.MessagesSendMessage(ctx, req)
	if err != nil {
		return failedAttempt(attempt, err)
	}
	attempt.result = mutationResult(action, peerRef.Ref, sentMessageID(updates), attempt.handle)
	return attempt
}

func (a App) resolver(c *telegram.Client) mutationResolver {
	return func(ctx context.Context, token string) (tg.InputPeerClass, peerstore.Peer, error) {
		return a.resolvePeer(ctx, c, token)
	}
}

func failedAttempt(attempt mutationAttempt, err error) mutationAttempt {
	attempt.result = MutationResult{}
	attempt.err = err
	return attempt
}

func attemptError(attempt mutationAttempt) error { return attempt.err }

func mutationResult(action, peerRef string, msgID int, handle string) MutationResult {
	return MutationResult{OK: true, Outcome: MutationConfirmed, RetrySafe: false, Action: action, PeerRef: peerRef, MessageID: msgID, ReconciliationHandle: handle, Timestamp: time.Now().UTC().Format(time.RFC3339)}
}

func mutationHandle(action, peerRef string, msgID int) string {
	return fmt.Sprintf("action:%s/peer:%s/message:%d", action, peerRef, msgID)
}

func mutationFailure(err error, dispatched bool, handle string) error {
	if err == nil {
		return nil
	}
	outcome := MutationRejected
	retrySafe := !dispatched
	if dispatched {
		if _, ok := tgerr.As(err); ok {
			retrySafe = true
		} else {
			outcome = MutationOutcomeUnknown
			retrySafe = false
		}
	}
	return MutationError{Outcome: outcome, RetrySafe: retrySafe, ReconciliationHandle: handle, Err: err}
}

func randomID() int64 {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 63))
	if err != nil {
		return time.Now().UnixNano()
	}
	return n.Int64()
}

func sentMessageID(updates tg.UpdatesClass) int {
	switch v := updates.(type) {
	case *tg.Updates:
		for _, update := range v.Updates {
			if msgID := messageIDFromUpdate(update); msgID != 0 {
				return msgID
			}
		}
	case *tg.UpdateShortSentMessage:
		return v.ID
	case *tg.UpdateShortMessage:
		return v.ID
	case *tg.UpdateShortChatMessage:
		return v.ID
	case *tg.UpdateShort:
		return messageIDFromUpdate(v.Update)
	case *tg.UpdatesCombined:
		for _, update := range v.Updates {
			if msgID := messageIDFromUpdate(update); msgID != 0 {
				return msgID
			}
		}
	}
	return 0
}

func messageIDFromUpdate(update tg.UpdateClass) int {
	switch v := update.(type) {
	case *tg.UpdateNewMessage:
		if msg, ok := v.Message.(*tg.Message); ok {
			return msg.ID
		}
	case *tg.UpdateNewChannelMessage:
		if msg, ok := v.Message.(*tg.Message); ok {
			return msg.ID
		}
	case *tg.UpdateEditMessage:
		if msg, ok := v.Message.(*tg.Message); ok {
			return msg.ID
		}
	case *tg.UpdateEditChannelMessage:
		if msg, ok := v.Message.(*tg.Message); ok {
			return msg.ID
		}
	}
	return 0
}
