package plugin

import (
	"context"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func (m *ModerationMessages) Delete(ctx context.Context, chat protocol.ModerationChatTarget, ids []int64, key string) (protocol.ModerationResult, error) {
	targets := make([]protocol.ModerationMessageTarget, len(ids))
	for i, id := range ids {
		targets[i] = protocol.ModerationMessageTarget{ChatID: chat.ChatID, MessageID: id}
	}
	return m.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationDeleteMessages, Chat: chat, Messages: targets, IdempotencyKey: key})
}
func (m *ModerationMessages) Pin(ctx context.Context, chat protocol.ModerationChatTarget, messageID int64, silent, oneSide bool, key string) (protocol.ModerationResult, error) {
	return m.pin(ctx, protocol.ModerationPinMessage, chat, messageID, silent, oneSide, key)
}
func (m *ModerationMessages) Unpin(ctx context.Context, chat protocol.ModerationChatTarget, messageID int64, key string) (protocol.ModerationResult, error) {
	return m.pin(ctx, protocol.ModerationUnpinMessage, chat, messageID, false, false, key)
}
func (m *ModerationMessages) pin(ctx context.Context, operation protocol.ModerationOperation, chat protocol.ModerationChatTarget, messageID int64, silent, oneSide bool, key string) (protocol.ModerationResult, error) {
	target := protocol.ModerationMessageTarget{ChatID: chat.ChatID, MessageID: messageID}
	return m.client.call(ctx, protocol.ModerationRequest{Operation: operation, Chat: chat, Messages: []protocol.ModerationMessageTarget{target}, Silent: silent, OneSide: oneSide, IdempotencyKey: key})
}
func (m *ModerationMessages) UnpinAll(ctx context.Context, chat protocol.ModerationChatTarget, key string) (protocol.ModerationResult, error) {
	return m.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationUnpinAll, Chat: chat, IdempotencyKey: key})
}

func (s *ModerationSettings) Get(ctx context.Context, chat protocol.ModerationChatTarget) (*protocol.ChatModerationSettings, error) {
	result, err := s.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationGetSettings, Chat: chat})
	return result.Settings, err
}
func (s *ModerationSettings) SetDefaultRights(ctx context.Context, chat protocol.ModerationChatTarget, denied protocol.DeniedRights, key string) (protocol.ModerationResult, error) {
	settings := protocol.ChatModerationSettings{DefaultDenied: &denied}
	return s.set(ctx, protocol.ModerationSetDefaultRights, chat, settings, key)
}
func (s *ModerationSettings) SetSlowMode(ctx context.Context, chat protocol.ModerationChatTarget, seconds int, key string) (protocol.ModerationResult, error) {
	settings := protocol.ChatModerationSettings{SlowModeSeconds: &seconds}
	return s.set(ctx, protocol.ModerationSetSlowMode, chat, settings, key)
}
func (s *ModerationSettings) SetProtectedContent(ctx context.Context, chat protocol.ModerationChatTarget, enabled bool, key string) (protocol.ModerationResult, error) {
	settings := protocol.ChatModerationSettings{ProtectedContent: &enabled}
	return s.set(ctx, protocol.ModerationSetProtected, chat, settings, key)
}
func (s *ModerationSettings) set(ctx context.Context, operation protocol.ModerationOperation, chat protocol.ModerationChatTarget, settings protocol.ChatModerationSettings, key string) (protocol.ModerationResult, error) {
	return s.client.call(ctx, protocol.ModerationRequest{Operation: operation, Chat: chat, Settings: &settings, IdempotencyKey: key})
}
