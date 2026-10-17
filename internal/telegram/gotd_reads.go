package telegram

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/gotd/td/tg"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

// GetMe, GetMessage, and ResolvePeer are the intentionally small read-only
// adapter surface. The returned values are protocol DTOs; gotd values never
// leave this package.
func (t *LiveTransport) GetMe(ctx context.Context) (*pluginv1.Peer, error) {
	if err := t.ensureReadable(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	identity := t.identity
	t.mu.Unlock()
	return &pluginv1.Peer{ID: identity.UserID, Kind: "user", Username: identity.Username, FirstName: identity.FirstName, LastName: identity.LastName}, nil
}

func (t *LiveTransport) ResolvePeer(ctx context.Context, peerID int64, username string) (*pluginv1.Peer, error) {
	if err := t.ensureReadable(); err != nil {
		return nil, err
	}
	if peerID != 0 {
		if _, err := t.peers.resolve(peerID); err != nil {
			return nil, fmt.Errorf("resolve peer: %w", err)
		}
		return peerDTO(peerID, ""), nil
	}
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if username == "" {
		return nil, errors.New("peer username is empty")
	}
	resolver, ok := t.api.(interface {
		ContactsResolveUsername(context.Context, *tg.ContactsResolveUsernameRequest) (*tg.ContactsResolvedPeer, error)
	})
	if !ok {
		return nil, errors.New("Telegram peer resolution is unavailable")
	}
	resolved, err := resolver.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: username})
	if err != nil {
		return nil, fmt.Errorf("resolve Telegram username: %w", err)
	}
	if resolved == nil || resolved.Peer == nil {
		return nil, errors.New("Telegram peer was not found")
	}
	entities := entitiesFromResolved(resolved)
	t.peers.learn(entities)
	id, kind := peerIDAndKind(resolved.Peer)
	if id == 0 {
		return nil, errors.New("Telegram peer response did not contain an ID")
	}
	return &pluginv1.Peer{ID: id, Kind: kind, Username: username}, nil
}

func (t *LiveTransport) GetMessage(ctx context.Context, peerID, messageID int64) (*pluginv1.Message, error) {
	if err := t.ensureReadable(); err != nil {
		return nil, err
	}
	if messageID <= 0 || messageID > math.MaxInt32 {
		return nil, errors.New("message ID is outside Telegram range")
	}
	peer, err := t.peers.resolve(peerID)
	if err != nil {
		return nil, err
	}
	history, ok := t.api.(interface {
		MessagesGetHistory(context.Context, *tg.MessagesGetHistoryRequest) (tg.MessagesMessagesClass, error)
	})
	if !ok {
		return nil, errors.New("Telegram message reads are unavailable")
	}
	result, err := history.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, OffsetID: int(messageID) + 1, AddOffset: -1, Limit: 3})
	if err != nil {
		return nil, fmt.Errorf("read Telegram message: %w", err)
	}
	messages, entities := messagesAndEntities(result)
	t.peers.learn(entities)
	for _, raw := range messages {
		converted, found, convertErr := convertMessage(pluginv1.UpdateMessage, raw, entities, t.identity.UserID)
		if convertErr != nil {
			return nil, convertErr
		}
		if found && converted.Message != nil && converted.Message.ID == messageID {
			return converted.Message, nil
		}
	}
	return nil, nil
}

func (t *LiveTransport) ensureReadable() error {
	t.mu.Lock()
	ready := t.running && !t.closed && t.identity.UserID != 0 && t.api != nil
	t.mu.Unlock()
	if !ready {
		return ErrNotAuthorized
	}
	return nil
}

func peerDTO(id int64, kind string) *pluginv1.Peer {
	if kind == "" {
		kind = "private"
		if id < 0 {
			kind = "group"
		}
	}
	return &pluginv1.Peer{ID: id, Kind: kind}
}

func peerIDAndKind(peer tg.PeerClass) (int64, string) {
	switch value := peer.(type) {
	case *tg.PeerUser:
		return value.UserID, "private"
	case *tg.PeerChat:
		return -value.ChatID, "group"
	case *tg.PeerChannel:
		return channelChatID(value.ChannelID), "channel"
	default:
		return 0, ""
	}
}

func entitiesFromResolved(value *tg.ContactsResolvedPeer) tg.Entities {
	result := tg.Entities{Users: make(map[int64]*tg.User), Chats: make(map[int64]*tg.Chat), Channels: make(map[int64]*tg.Channel)}
	for _, item := range value.Users {
		if user, ok := item.(*tg.User); ok {
			result.Users[user.ID] = user
		}
	}
	for _, item := range value.Chats {
		switch chat := item.(type) {
		case *tg.Chat:
			result.Chats[chat.ID] = chat
		case *tg.Channel:
			result.Channels[chat.ID] = chat
		}
	}
	return result
}

func messagesAndEntities(value tg.MessagesMessagesClass) ([]tg.MessageClass, tg.Entities) {
	entities := tg.Entities{Users: make(map[int64]*tg.User), Chats: make(map[int64]*tg.Chat), Channels: make(map[int64]*tg.Channel)}
	var messages []tg.MessageClass
	switch result := value.(type) {
	case *tg.MessagesMessages:
		messages, entities = result.Messages, entitiesFromParts(result.Users, result.Chats)
	case *tg.MessagesMessagesSlice:
		messages, entities = result.Messages, entitiesFromParts(result.Users, result.Chats)
	case *tg.MessagesChannelMessages:
		messages, entities = result.Messages, entitiesFromParts(result.Users, result.Chats)
	}
	return messages, entities
}

func entitiesFromParts(users []tg.UserClass, chats []tg.ChatClass) tg.Entities {
	result := tg.Entities{Users: make(map[int64]*tg.User), Chats: make(map[int64]*tg.Chat), Channels: make(map[int64]*tg.Channel)}
	for _, item := range users {
		if user, ok := item.(*tg.User); ok {
			result.Users[user.ID] = user
		}
	}
	for _, item := range chats {
		switch chat := item.(type) {
		case *tg.Chat:
			result.Chats[chat.ID] = chat
		case *tg.Channel:
			result.Channels[chat.ID] = chat
		}
	}
	return result
}
