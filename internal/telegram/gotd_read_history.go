package telegram

import (
	"context"
	"errors"
	"fmt"

	"github.com/gotd/td/tg"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func (t *LiveTransport) GetHistory(ctx context.Context, peerID, offsetID int64, limit int, direction string) ([]pluginv1.Message, bool, error) {
	if err := t.ensureReadable(); err != nil {
		return nil, false, err
	}
	if limit <= 0 || limit > pluginv1.MaxClientPageItems {
		return nil, false, errors.New("history page size is outside its bound")
	}
	peer, err := t.peers.resolve(peerID)
	if err != nil {
		return nil, false, err
	}
	history, ok := t.api.(interface {
		MessagesGetHistory(context.Context, *tg.MessagesGetHistoryRequest) (tg.MessagesMessagesClass, error)
	})
	if !ok {
		return nil, false, &pluginv1.Error{Code: pluginv1.ErrorUnsupported, Message: "Telegram history is unavailable"}
	}
	request := &tg.MessagesGetHistoryRequest{Peer: peer, OffsetID: int(offsetID), Limit: limit}
	if direction == "newer" && offsetID > 0 {
		request.MinID = int(offsetID)
	}
	result, err := history.MessagesGetHistory(ctx, request)
	if err != nil {
		return nil, false, fmt.Errorf("read Telegram history: %w", err)
	}
	messages, entities := messagesAndEntities(result)
	t.peers.learn(entities)
	converted, err := convertMessages(messages, entities, t.identity.UserID)
	if err != nil {
		return nil, false, err
	}
	return converted, pageHasMore(result, len(converted), limit), nil
}

func (t *LiveTransport) GetMessages(ctx context.Context, peerID int64, messageIDs []int64) ([]pluginv1.Message, error) {
	if err := t.ensureReadable(); err != nil {
		return nil, err
	}
	if len(messageIDs) == 0 || len(messageIDs) > pluginv1.MaxClientBatchItems {
		return nil, errors.New("message batch is outside its bound")
	}
	getter, ok := t.api.(interface {
		MessagesGetMessages(context.Context, []tg.InputMessageClass) (tg.MessagesMessagesClass, error)
	})
	if !ok {
		return nil, &pluginv1.Error{Code: pluginv1.ErrorUnsupported, Message: "Telegram message batch reads are unavailable"}
	}
	ids := make([]tg.InputMessageClass, len(messageIDs))
	for i, id := range messageIDs {
		if id <= 0 {
			return nil, errors.New("message ID must be positive")
		}
		ids[i] = &tg.InputMessageID{ID: int(id)}
	}
	result, err := getter.MessagesGetMessages(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("read Telegram message batch: %w", err)
	}
	messages, entities := messagesAndEntities(result)
	t.peers.learn(entities)
	converted, err := convertMessages(messages, entities, t.identity.UserID)
	if err != nil {
		return nil, err
	}
	filtered := converted[:0]
	for _, message := range converted {
		if message.ChatID == peerID {
			filtered = append(filtered, message)
		}
	}
	return filtered, nil
}

func (t *LiveTransport) SearchMessages(ctx context.Context, peerID int64, query string, senderID int64, mediaKind string, offsetID int64, limit int) ([]pluginv1.Message, bool, error) {
	if err := t.ensureReadable(); err != nil {
		return nil, false, err
	}
	if limit <= 0 || limit > pluginv1.MaxClientPageItems {
		return nil, false, errors.New("search page size is outside its bound")
	}
	peer, err := t.peers.resolve(peerID)
	if err != nil {
		return nil, false, err
	}
	search, ok := t.api.(interface {
		MessagesSearch(context.Context, *tg.MessagesSearchRequest) (tg.MessagesMessagesClass, error)
	})
	if !ok {
		return nil, false, &pluginv1.Error{Code: pluginv1.ErrorUnsupported, Message: "Telegram search is unavailable"}
	}
	request := &tg.MessagesSearchRequest{Peer: peer, Q: query, OffsetID: int(offsetID), Limit: limit}
	if senderID != 0 {
		sender, resolveErr := t.peers.resolve(senderID)
		if resolveErr != nil {
			return nil, false, resolveErr
		}
		request.SetFromID(sender)
	}
	if mediaKind != "" {
		filter, filterErr := messageFilter(mediaKind)
		if filterErr != nil {
			return nil, false, filterErr
		}
		request.Filter = filter
	}
	result, err := search.MessagesSearch(ctx, request)
	if err != nil {
		return nil, false, fmt.Errorf("search Telegram messages: %w", err)
	}
	messages, entities := messagesAndEntities(result)
	t.peers.learn(entities)
	converted, err := convertMessages(messages, entities, t.identity.UserID)
	if err != nil {
		return nil, false, err
	}
	return converted, pageHasMore(result, len(converted), limit), nil
}

func convertMessages(messages []tg.MessageClass, entities tg.Entities, selfID int64) ([]pluginv1.Message, error) {
	result := make([]pluginv1.Message, 0, len(messages))
	for _, raw := range messages {
		update, found, err := convertMessage(pluginv1.UpdateMessage, raw, entities, selfID)
		if err != nil {
			return nil, err
		}
		if found && update.Message != nil {
			result = append(result, *update.Message)
		}
	}
	return result, nil
}

func pageHasMore(value tg.MessagesMessagesClass, count, limit int) bool {
	if count == 0 {
		return false
	}
	switch result := value.(type) {
	case *tg.MessagesMessagesSlice:
		return result.Count > count
	case *tg.MessagesChannelMessages:
		return result.Count > count
	default:
		return count >= limit
	}
}

func messageFilter(kind string) (tg.MessagesFilterClass, error) {
	switch kind {
	case "photo":
		return &tg.InputMessagesFilterPhotos{}, nil
	case "video":
		return &tg.InputMessagesFilterVideo{}, nil
	case "document":
		return &tg.InputMessagesFilterDocument{}, nil
	case "voice":
		return &tg.InputMessagesFilterVoice{}, nil
	case "audio":
		return &tg.InputMessagesFilterMusic{}, nil
	case "round_video":
		return &tg.InputMessagesFilterRoundVideo{}, nil
	default:
		return nil, fmt.Errorf("unsupported media search kind %q", kind)
	}
}
