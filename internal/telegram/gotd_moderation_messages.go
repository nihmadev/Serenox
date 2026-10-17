package telegram

import (
	"context"

	"github.com/gotd/td/tg"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type gotdModerationMessageAPI interface {
	MessagesDeleteMessages(context.Context, *tg.MessagesDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error)
	ChannelsDeleteMessages(context.Context, *tg.ChannelsDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error)
	MessagesUpdatePinnedMessage(context.Context, *tg.MessagesUpdatePinnedMessageRequest) (tg.UpdatesClass, error)
	MessagesUnpinAllMessages(context.Context, *tg.MessagesUnpinAllMessagesRequest) (*tg.MessagesAffectedHistory, error)
}

func (t *LiveTransport) ModerationDeleteMessages(ctx context.Context, chat pluginv1.ModerationChatTarget, messages []pluginv1.ModerationMessageTarget) ([]pluginv1.ModerationItemResult, error) {
	peer, channel, kind, _, err := t.moderationChat(chat)
	if err != nil {
		return nil, err
	}
	if kind == pluginv1.ModerationChatPrivate {
		return nil, &pluginv1.Error{Code: pluginv1.ErrorModerationUnsupportedChat, Message: "administrative deletion does not apply to private dialogs; use telegram.delete"}
	}
	ids := make([]int, len(messages))
	for i, message := range messages {
		id, convertErr := telegramMessageID(message.MessageID)
		if convertErr != nil {
			return nil, convertErr
		}
		ids[i] = id
	}
	api, ok := t.api.(gotdModerationMessageAPI)
	if !ok {
		return nil, moderationUnsupported("gotd moderation message methods are unavailable")
	}
	if channel != nil {
		_, err = api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{Channel: channel, ID: ids})
	} else {
		_, err = api.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{Revoke: true, ID: ids})
	}
	if err != nil {
		return nil, safeModerationMutationError(err)
	}
	items := make([]pluginv1.ModerationItemResult, len(messages))
	for i, message := range messages {
		items[i] = pluginv1.ModerationItemResult{ID: message.MessageID, Status: pluginv1.ActionResultSuccess}
	}
	_ = peer
	return items, nil
}

func (t *LiveTransport) ModerationPin(ctx context.Context, chat pluginv1.ModerationChatTarget, target *pluginv1.ModerationMessageTarget, pin, silent, oneSide bool) error {
	peer, _, kind, _, err := t.moderationChat(chat)
	if err != nil {
		return err
	}
	api, ok := t.api.(gotdModerationMessageAPI)
	if !ok {
		return moderationUnsupported("gotd pin methods are unavailable")
	}
	if target == nil {
		_, err = api.MessagesUnpinAllMessages(ctx, &tg.MessagesUnpinAllMessagesRequest{Peer: peer})
		return safeModerationMutationError(err)
	}
	if oneSide && kind != pluginv1.ModerationChatPrivate {
		return &pluginv1.Error{Code: pluginv1.ErrorInvalidRequest, Message: "one-side pin semantics apply only to private dialogs"}
	}
	id, err := telegramMessageID(target.MessageID)
	if err != nil {
		return err
	}
	_, err = api.MessagesUpdatePinnedMessage(ctx, &tg.MessagesUpdatePinnedMessageRequest{Silent: silent, Unpin: !pin, PmOneside: oneSide, Peer: peer, ID: id})
	return safeModerationMutationError(err)
}
