package plugin

import (
	"context"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func (j *ModerationJoinRequests) List(ctx context.Context, chat protocol.ModerationChatTarget, query string, limit int, cursor string) (protocol.ModerationResult, error) {
	return j.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationListJoinRequests, Chat: chat, Page: &protocol.ModerationPageRequest{Query: query, Limit: limit, Cursor: cursor}})
}
func (j *ModerationJoinRequests) Decide(ctx context.Context, chat protocol.ModerationChatTarget, target protocol.ModerationParticipantTarget, approve bool, key string) (protocol.ModerationResult, error) {
	operation := protocol.ModerationDeclineJoin
	if approve {
		operation = protocol.ModerationApproveJoin
	}
	return j.client.call(ctx, protocol.ModerationRequest{Operation: operation, Chat: chat, Participant: &target, IdempotencyKey: key})
}

func (i *ModerationInvites) Create(ctx context.Context, chat protocol.ModerationChatTarget, settings protocol.InviteSettings, key string) (protocol.ModerationResult, error) {
	return i.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationCreateInvite, Chat: chat, Invite: &settings, IdempotencyKey: key})
}
func (i *ModerationInvites) List(ctx context.Context, chat protocol.ModerationChatTarget, limit int, cursor string) (protocol.ModerationResult, error) {
	return i.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationListInvites, Chat: chat, Page: &protocol.ModerationPageRequest{Limit: limit, Cursor: cursor}})
}
func (i *ModerationInvites) Edit(ctx context.Context, chat protocol.ModerationChatTarget, settings protocol.InviteSettings, key string) (protocol.ModerationResult, error) {
	return i.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationEditInvite, Chat: chat, Invite: &settings, IdempotencyKey: key})
}
func (i *ModerationInvites) Revoke(ctx context.Context, chat protocol.ModerationChatTarget, link, key string) (protocol.ModerationResult, error) {
	settings := protocol.InviteSettings{Link: link}
	return i.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationRevokeInvite, Chat: chat, Invite: &settings, IdempotencyKey: key})
}

func (t *ModerationTopics) Get(ctx context.Context, chat protocol.ModerationChatTarget, topicID int64) (*protocol.ForumTopicState, error) {
	topic := protocol.ForumTopicTarget{TopicID: topicID}
	result, err := t.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationGetTopic, Chat: chat, Topic: &topic})
	return result.Topic, err
}
func (t *ModerationTopics) SetClosed(ctx context.Context, chat protocol.ModerationChatTarget, topicID int64, closed bool, key string) (protocol.ModerationResult, error) {
	operation := protocol.ModerationReopenTopic
	if closed {
		operation = protocol.ModerationCloseTopic
	}
	topic := protocol.ForumTopicTarget{TopicID: topicID}
	return t.client.call(ctx, protocol.ModerationRequest{Operation: operation, Chat: chat, Topic: &topic, IdempotencyKey: key})
}
func (t *ModerationTopics) SetGeneralHidden(ctx context.Context, chat protocol.ModerationChatTarget, hidden bool, key string) (protocol.ModerationResult, error) {
	operation := protocol.ModerationUnhideGeneralTopic
	if hidden {
		operation = protocol.ModerationHideGeneralTopic
	}
	return t.client.call(ctx, protocol.ModerationRequest{Operation: operation, Chat: chat, IdempotencyKey: key})
}
func (t *ModerationTopics) Delete(ctx context.Context, chat protocol.ModerationChatTarget, topicID int64, key string) (protocol.ModerationResult, error) {
	topic := protocol.ForumTopicTarget{TopicID: topicID}
	return t.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationDeleteTopic, Chat: chat, Topic: &topic, IdempotencyKey: key})
}
