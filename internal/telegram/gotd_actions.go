package telegram

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

var (
	ErrActionUnsupported        = errors.New("native Telegram action is not implemented")
	ErrUnsupportedActionOptions = errors.New("native Telegram action contains unsupported options")
)

type actionAPI interface {
	MessagesSendMessage(context.Context, *tg.MessagesSendMessageRequest) (tg.UpdatesClass, error)
	MessagesEditMessage(context.Context, *tg.MessagesEditMessageRequest) (tg.UpdatesClass, error)
	MessagesDeleteMessages(context.Context, *tg.MessagesDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error)
	ChannelsDeleteMessages(context.Context, *tg.ChannelsDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error)
}

func (t *LiveTransport) Apply(ctx context.Context, action pluginv1.Action) (pluginv1.ActionResult, error) {
	if ctx == nil {
		return pluginv1.ActionResult{}, errors.New("nil Telegram action context")
	}
	if err := action.Validate(); err != nil {
		return pluginv1.ActionResult{}, fmt.Errorf("invalid Telegram action: %w", err)
	}
	t.mu.Lock()
	ready := t.running && t.identity.UserID != 0 && !t.closed && t.api != nil && t.peers != nil
	t.mu.Unlock()
	if !ready {
		return pluginv1.ActionResult{}, ErrNotAuthorized
	}
	if action.Kind == pluginv1.ActionInlineAnswer || action.Kind == pluginv1.ActionClient {
		return pluginv1.ActionResult{}, fmt.Errorf("%w: %s", ErrActionUnsupported, action.Kind)
	}
	if len(action.Buttons) != 0 || action.CallbackID != "" || action.InlineQueryID != "" || action.ClientOperation != nil || len(action.InlineResults) != 0 {
		return pluginv1.ActionResult{}, fmt.Errorf("%w: buttons, inline answers, and client operations are not available", ErrUnsupportedActionOptions)
	}

	var err error
	switch action.Kind {
	case pluginv1.ActionSend, pluginv1.ActionReply, pluginv1.ActionEdit:
		peer, resolveErr := t.peers.resolve(action.ChatID)
		if resolveErr != nil {
			return pluginv1.ActionResult{}, resolveErr
		}
		text, entities, parseErr := parseActionText(action.Text, action.ParseMode, t.resolveInputUser)
		if parseErr != nil {
			return pluginv1.ActionResult{}, parseErr
		}
		switch action.Kind {
		case pluginv1.ActionSend:
			err = t.send(ctx, peer, text, entities, 0)
		case pluginv1.ActionReply:
			replyTo := action.ReplyToMessageID
			if replyTo == 0 {
				replyTo = action.MessageID
			}
			if replyTo == 0 {
				return pluginv1.ActionResult{}, errors.New("reply action requires reply_to_message_id or message_id")
			}
			err = t.send(ctx, peer, text, entities, replyTo)
		case pluginv1.ActionEdit:
			messageID, idErr := telegramMessageID(action.MessageID)
			if idErr != nil {
				return pluginv1.ActionResult{}, idErr
			}
			_, err = t.api.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{
				Peer: peer, ID: messageID, Message: text, Entities: entities,
			})
		}
	case pluginv1.ActionDelete:
		peer, resolveErr := t.peers.resolve(action.ChatID)
		if resolveErr != nil {
			return pluginv1.ActionResult{}, resolveErr
		}
		err = t.delete(ctx, peer, action.MessageID)
	default:
		return pluginv1.ActionResult{}, fmt.Errorf("%w: %s", ErrActionUnsupported, action.Kind)
	}
	if err != nil {
		return pluginv1.ActionResult{}, fmt.Errorf("apply Telegram %s action: %w", action.Kind, err)
	}
	return pluginv1.ActionResult{ActionID: action.ID, Success: true}, nil
}

func parseActionText(text, parseMode string, resolver func(int64) (tg.InputUserClass, error)) (string, []tg.MessageEntityClass, error) {
	switch strings.ToLower(strings.TrimSpace(parseMode)) {
	case "", "plain", "none":
		return text, nil, nil
	case "html":
		builder := entity.Builder{}
		if err := styling.Perform(&builder, html.String(resolver, text)); err != nil {
			return "", nil, fmt.Errorf("parse Telegram HTML: %w", err)
		}
		parsed, entities := builder.Complete()
		if len(entities) > pluginv1.MaxCollectionItems {
			return "", nil, fmt.Errorf("Telegram HTML produces more than %d entities", pluginv1.MaxCollectionItems)
		}
		return parsed, entities, nil
	default:
		return "", nil, fmt.Errorf("%w: parse_mode %q; supported values are plain and HTML", ErrUnsupportedActionOptions, parseMode)
	}
}

func (t *LiveTransport) resolveInputUser(userID int64) (tg.InputUserClass, error) {
	peer, err := t.peers.resolve(userID)
	if err != nil {
		return nil, err
	}
	switch value := peer.(type) {
	case *tg.InputPeerSelf:
		return &tg.InputUserSelf{}, nil
	case *tg.InputPeerUser:
		return &tg.InputUser{UserID: value.UserID, AccessHash: value.AccessHash}, nil
	default:
		return nil, fmt.Errorf("%w: user_id=%d", ErrPeerNotResolved, userID)
	}
}

func (t *LiveTransport) send(ctx context.Context, peer tg.InputPeerClass, text string, entities []tg.MessageEntityClass, replyTo int64) error {
	randomID, err := randomMessageID()
	if err != nil {
		return err
	}
	request := &tg.MessagesSendMessageRequest{Peer: peer, Message: text, Entities: entities, RandomID: randomID}
	if replyTo != 0 {
		messageID, idErr := telegramMessageID(replyTo)
		if idErr != nil {
			return idErr
		}
		request.ReplyTo = &tg.InputReplyToMessage{ReplyToMsgID: messageID}
	}
	_, err = t.api.MessagesSendMessage(ctx, request)
	return err
}

func (t *LiveTransport) delete(ctx context.Context, peer tg.InputPeerClass, rawMessageID int64) error {
	messageID, err := telegramMessageID(rawMessageID)
	if err != nil {
		return err
	}
	if channel, ok := peer.(*tg.InputPeerChannel); ok {
		_, err = t.api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash},
			ID:      []int{messageID},
		})
		return err
	}
	_, err = t.api.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{Revoke: true, ID: []int{messageID}})
	return err
}

func telegramMessageID(value int64) (int, error) {
	if value <= 0 || value > math.MaxInt32 {
		return 0, fmt.Errorf("Telegram message ID %d is outside the protocol range", value)
	}
	return int(value), nil
}

func randomMessageID() (int64, error) {
	var payload [8]byte
	if _, err := cryptorand.Read(payload[:]); err != nil {
		return 0, fmt.Errorf("generate Telegram random ID: %w", err)
	}
	value := int64(binary.LittleEndian.Uint64(payload[:]))
	if value == 0 {
		value = 1
	}
	return value, nil
}
