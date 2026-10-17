package telegram

import (
	"context"

	"github.com/gotd/td/tg"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type gotdModerationTopicAPI interface {
	MessagesGetForumTopicsByID(context.Context, *tg.MessagesGetForumTopicsByIDRequest) (*tg.MessagesForumTopics, error)
	MessagesEditForumTopic(context.Context, *tg.MessagesEditForumTopicRequest) (tg.UpdatesClass, error)
	MessagesDeleteTopicHistory(context.Context, *tg.MessagesDeleteTopicHistoryRequest) (*tg.MessagesAffectedHistory, error)
}

func (t *LiveTransport) ModerationGetTopic(ctx context.Context, chat pluginv1.ModerationChatTarget, topic pluginv1.ForumTopicTarget) (*pluginv1.ForumTopicState, error) {
	peer, channel, kind, forum, err := t.moderationChat(chat)
	if err != nil {
		return nil, err
	}
	if err := validateForumTarget(channel, kind, forum); err != nil {
		return nil, err
	}
	api, ok := t.api.(gotdModerationTopicAPI)
	if !ok {
		return nil, moderationUnsupported("gotd forum topic methods are unavailable")
	}
	id, err := telegramMessageID(topic.TopicID)
	if err != nil {
		return nil, err
	}
	result, err := api.MessagesGetForumTopicsByID(ctx, &tg.MessagesGetForumTopicsByIDRequest{Peer: peer, Topics: []int{id}})
	if err != nil {
		return nil, safeModerationRPCError(err)
	}
	for _, raw := range result.Topics {
		if value, yes := raw.(*tg.ForumTopic); yes && value.ID == id {
			return &pluginv1.ForumTopicState{TopicID: int64(value.ID), Closed: value.Closed, Hidden: value.Hidden}, nil
		}
	}
	return nil, &pluginv1.Error{Code: pluginv1.ErrorModerationTopicNotFound, Message: "forum topic was not found"}
}

func (t *LiveTransport) ModerationSetTopic(ctx context.Context, chat pluginv1.ModerationChatTarget, operation pluginv1.ModerationOperation, topic pluginv1.ForumTopicTarget) error {
	peer, channel, kind, forum, err := t.moderationChat(chat)
	if err != nil {
		return err
	}
	if err := validateForumTarget(channel, kind, forum); err != nil {
		return err
	}
	api, ok := t.api.(gotdModerationTopicAPI)
	if !ok {
		return moderationUnsupported("gotd forum topic methods are unavailable")
	}
	if operation == pluginv1.ModerationDeleteTopic {
		id, convertErr := telegramMessageID(topic.TopicID)
		if convertErr != nil {
			return convertErr
		}
		_, err = api.MessagesDeleteTopicHistory(ctx, &tg.MessagesDeleteTopicHistoryRequest{Peer: peer, TopMsgID: id})
		return safeModerationMutationError(err)
	}
	request := &tg.MessagesEditForumTopicRequest{Peer: peer}
	switch operation {
	case pluginv1.ModerationHideGeneralTopic, pluginv1.ModerationUnhideGeneralTopic:
		request.TopicID = 1
		request.SetHidden(operation == pluginv1.ModerationHideGeneralTopic)
	case pluginv1.ModerationCloseTopic, pluginv1.ModerationReopenTopic:
		id, convertErr := telegramMessageID(topic.TopicID)
		if convertErr != nil {
			return convertErr
		}
		request.TopicID = id
		request.SetClosed(operation == pluginv1.ModerationCloseTopic)
	default:
		return moderationUnsupported("forum topic mutation is not implemented")
	}
	_, err = api.MessagesEditForumTopic(ctx, request)
	return safeModerationMutationError(err)
}

func validateForumTarget(channel *tg.InputChannel, kind pluginv1.ModerationChatKind, forum bool) error {
	if channel == nil || kind != pluginv1.ModerationChatMegagroup && kind != pluginv1.ModerationChatDiscussion {
		return &pluginv1.Error{Code: pluginv1.ErrorModerationUnsupportedChat, Message: "topic operation requires a megagroup"}
	}
	if !forum {
		return &pluginv1.Error{Code: pluginv1.ErrorModerationNotForum, Message: "chat is not a forum"}
	}
	return nil
}

var _ ModerationBackend = (*LiveTransport)(nil)

func (*LiveTransport) ModerationSupportStatus() string { return "live-gated" }
