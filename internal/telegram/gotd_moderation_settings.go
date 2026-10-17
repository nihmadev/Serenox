package telegram

import (
	"context"

	"github.com/gotd/td/tg"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type gotdModerationSettingsAPI interface {
	ChannelsGetFullChannel(context.Context, tg.InputChannelClass) (*tg.MessagesChatFull, error)
	MessagesGetFullChat(context.Context, int64) (*tg.MessagesChatFull, error)
	MessagesEditChatDefaultBannedRights(context.Context, *tg.MessagesEditChatDefaultBannedRightsRequest) (tg.UpdatesClass, error)
	ChannelsToggleSlowMode(context.Context, *tg.ChannelsToggleSlowModeRequest) (tg.UpdatesClass, error)
	MessagesToggleNoForwards(context.Context, *tg.MessagesToggleNoForwardsRequest) (tg.UpdatesClass, error)
}

func (t *LiveTransport) ModerationGetSettings(ctx context.Context, chat pluginv1.ModerationChatTarget) (*pluginv1.ChatModerationSettings, error) {
	_, channel, kind, forum, err := t.moderationChat(chat)
	if err != nil {
		return nil, err
	}
	api, ok := t.api.(gotdModerationSettingsAPI)
	if !ok {
		return nil, moderationUnsupported("gotd chat settings methods are unavailable")
	}
	var full *tg.MessagesChatFull
	if channel != nil {
		full, err = api.ChannelsGetFullChannel(ctx, channel)
	} else if kind == pluginv1.ModerationChatBasicGroup {
		full, err = api.MessagesGetFullChat(ctx, -chat.ChatID)
	} else {
		return nil, &pluginv1.Error{Code: pluginv1.ErrorModerationUnsupportedChat, Message: "moderation settings do not apply to private dialogs"}
	}
	if err != nil {
		return nil, safeModerationRPCError(err)
	}
	t.peers.learn(entitiesFromParts(full.Users, full.Chats))
	settings := &pluginv1.ChatModerationSettings{Forum: boolp(forum)}
	applyChatSettings(settings, full.Chats)
	if value, ok := full.FullChat.(*tg.ChannelFull); ok {
		settings.ParticipantsHidden = boolp(value.ParticipantsHidden)
		if seconds, present := value.GetSlowmodeSeconds(); present {
			settings.SlowModeSeconds = &seconds
		}
		if linked, present := value.GetLinkedChatID(); present && linked > 0 {
			id := channelChatID(linked)
			settings.LinkedDiscussionID = &id
		}
	}
	return settings, nil
}

func applyChatSettings(settings *pluginv1.ChatModerationSettings, chats []tg.ChatClass) {
	for _, item := range chats {
		switch value := item.(type) {
		case *tg.Chat:
			settings.ProtectedContent = boolp(value.Noforwards)
			if rights, ok := value.GetDefaultBannedRights(); ok {
				settings.DefaultDenied = &restrictionFromTG(rights).Denied
			}
		case *tg.Channel:
			settings.ProtectedContent, settings.JoinToSend, settings.Signatures, settings.Forum = boolp(value.Noforwards), boolp(value.JoinToSend), boolp(value.Signatures), boolp(value.Forum)
			if rights, ok := value.GetDefaultBannedRights(); ok {
				settings.DefaultDenied = &restrictionFromTG(rights).Denied
			}
		}
	}
}

func (t *LiveTransport) ModerationSetSettings(ctx context.Context, chat pluginv1.ModerationChatTarget, operation pluginv1.ModerationOperation, settings pluginv1.ChatModerationSettings) error {
	peer, channel, kind, _, err := t.moderationChat(chat)
	if err != nil {
		return err
	}
	if kind == pluginv1.ModerationChatPrivate {
		return &pluginv1.Error{Code: pluginv1.ErrorModerationUnsupportedChat, Message: "moderation settings do not apply to private dialogs"}
	}
	api, ok := t.api.(gotdModerationSettingsAPI)
	if !ok {
		return moderationUnsupported("gotd chat settings methods are unavailable")
	}
	switch operation {
	case pluginv1.ModerationSetDefaultRights:
		if settings.DefaultDenied == nil {
			return &pluginv1.Error{Code: pluginv1.ErrorInvalidRequest, Message: "default_denied is required"}
		}
		current, getErr := t.ModerationGetSettings(ctx, chat)
		if getErr != nil {
			return getErr
		}
		var base *tg.ChatBannedRights
		if current.DefaultDenied != nil {
			converted, convertErr := restrictionToTG(&pluginv1.Restriction{Denied: *current.DefaultDenied, Permanent: true}, nil)
			if convertErr != nil {
				return convertErr
			}
			base = &converted
		}
		rights, convertErr := restrictionToTG(&pluginv1.Restriction{Denied: *settings.DefaultDenied, Permanent: true}, base)
		if convertErr != nil {
			return convertErr
		}
		_, err = api.MessagesEditChatDefaultBannedRights(ctx, &tg.MessagesEditChatDefaultBannedRightsRequest{Peer: peer, BannedRights: rights})
	case pluginv1.ModerationSetSlowMode:
		if channel == nil || settings.SlowModeSeconds == nil || !validSlowMode(*settings.SlowModeSeconds) {
			return &pluginv1.Error{Code: pluginv1.ErrorInvalidRequest, Message: "slow mode requires a megagroup and one supported interval"}
		}
		if kind != pluginv1.ModerationChatMegagroup && kind != pluginv1.ModerationChatDiscussion {
			return &pluginv1.Error{Code: pluginv1.ErrorModerationUnsupportedChat, Message: "slow mode applies only to megagroups"}
		}
		_, err = api.ChannelsToggleSlowMode(ctx, &tg.ChannelsToggleSlowModeRequest{Channel: channel, Seconds: *settings.SlowModeSeconds})
	case pluginv1.ModerationSetProtected:
		if settings.ProtectedContent == nil {
			return &pluginv1.Error{Code: pluginv1.ErrorInvalidRequest, Message: "protected_content is required"}
		}
		_, err = api.MessagesToggleNoForwards(ctx, &tg.MessagesToggleNoForwardsRequest{Peer: peer, Enabled: *settings.ProtectedContent})
	default:
		return moderationUnsupported("chat setting mutation is not implemented")
	}
	return safeModerationMutationError(err)
}

func validSlowMode(seconds int) bool {
	switch seconds {
	case 0, 10, 30, 60, 300, 900, 3600:
		return true
	}
	return false
}
