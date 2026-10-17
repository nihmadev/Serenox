package telegram

import (
	"context"

	"github.com/gotd/td/tg"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type gotdAdminAPI interface {
	ChannelsGetParticipant(context.Context, *tg.ChannelsGetParticipantRequest) (*tg.ChannelsChannelParticipant, error)
	ChannelsGetFullChannel(context.Context, tg.InputChannelClass) (*tg.MessagesChatFull, error)
	ChannelsEditAdmin(context.Context, *tg.ChannelsEditAdminRequest) (tg.UpdatesClass, error)
}

func (t *LiveTransport) ModerationGetActingRights(ctx context.Context, chat pluginv1.ModerationChatTarget) (*pluginv1.ModerationMember, error) {
	_, channel, kind, _, err := t.moderationChat(chat)
	if err != nil {
		return nil, err
	}
	if err := assertChannel(channel, kind); err != nil {
		return nil, err
	}
	api, ok := t.api.(gotdAdminAPI)
	if !ok {
		return nil, moderationUnsupported("gotd administrator methods are unavailable")
	}
	result, err := api.ChannelsGetFullChannel(ctx, channel)
	if err != nil {
		return nil, safeModerationRPCError(err)
	}
	t.peers.learn(entitiesFromParts(result.Users, result.Chats))
	t.mu.Lock()
	selfID := t.identity.UserID
	t.mu.Unlock()
	member, found := channelActingMember(result.Chats, channel.ChannelID, selfID)
	if !found {
		return nil, &pluginv1.Error{Code: pluginv1.ErrorConversion, Message: "Telegram full-chat response omitted the requested channel"}
	}
	return &member, nil
}

func (t *LiveTransport) ModerationSetAdmin(ctx context.Context, chat pluginv1.ModerationChatTarget, target pluginv1.ModerationParticipantTarget, role *pluginv1.AdminRole) error {
	_, channel, kind, _, err := t.moderationChat(chat)
	if err != nil {
		return err
	}
	if err := assertChannel(channel, kind); err != nil {
		return err
	}
	peer, user, id, err := t.moderationTarget(ctx, chat, target)
	if err != nil {
		return err
	}
	t.mu.Lock()
	selfID := t.identity.UserID
	t.mu.Unlock()
	if id == selfID {
		return &pluginv1.Error{Code: pluginv1.ErrorModerationProtectedTarget, Message: "the acting account cannot change its own administrator role"}
	}
	if err := validateAdminChatRights(kind, role); err != nil {
		return err
	}
	api, ok := t.api.(gotdAdminAPI)
	if !ok {
		return moderationUnsupported("gotd administrator methods are unavailable")
	}
	if role != nil {
		current, getErr := api.ChannelsGetParticipant(ctx, &tg.ChannelsGetParticipantRequest{Channel: channel, Participant: peer})
		if getErr != nil {
			return safeModerationRPCError(getErr)
		}
		member, convertErr := convertChannelParticipant(current.Participant)
		if convertErr != nil {
			return convertErr
		}
		if member.Status == pluginv1.MemberCreator {
			return &pluginv1.Error{Code: pluginv1.ErrorModerationProtectedTarget, Message: "the creator's administrator rights cannot be changed"}
		}
		role = mergeAdminRole(member.Admin, role)
	}
	rights, rank := adminToTG(role)
	_, err = api.ChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{Channel: channel, UserID: user, AdminRights: rights, Rank: rank})
	return safeModerationMutationError(err)
}

func validateAdminChatRights(kind pluginv1.ModerationChatKind, role *pluginv1.AdminRole) error {
	if role == nil {
		return nil
	}
	r := role.Rights
	if kind == pluginv1.ModerationChatMegagroup || kind == pluginv1.ModerationChatDiscussion {
		if present(r.PostMessages) || present(r.EditMessages) || present(r.PostStories) || present(r.EditStories) || present(r.DeleteStories) {
			return &pluginv1.Error{Code: pluginv1.ErrorModerationInvalidRight, Message: "broadcast posting and story rights do not apply to megagroups"}
		}
	}
	if kind == pluginv1.ModerationChatBroadcast && (present(r.PinMessages) || present(r.ManageTopics)) {
		return &pluginv1.Error{Code: pluginv1.ErrorModerationInvalidRight, Message: "pin and topic rights do not apply to broadcast channels"}
	}
	return nil
}

func present(value *bool) bool { return value != nil }

func mergeAdminRole(current, requested *pluginv1.AdminRole) *pluginv1.AdminRole {
	if requested == nil {
		return nil
	}
	result := pluginv1.AdminRole{}
	if current != nil {
		result = *current
	}
	merge := func(destination **bool, source *bool) {
		if source != nil {
			value := *source
			*destination = &value
		}
	}
	merge(&result.Rights.ChangeInfo, requested.Rights.ChangeInfo)
	merge(&result.Rights.PostMessages, requested.Rights.PostMessages)
	merge(&result.Rights.EditMessages, requested.Rights.EditMessages)
	merge(&result.Rights.DeleteMessages, requested.Rights.DeleteMessages)
	merge(&result.Rights.BanUsers, requested.Rights.BanUsers)
	merge(&result.Rights.InviteUsers, requested.Rights.InviteUsers)
	merge(&result.Rights.PinMessages, requested.Rights.PinMessages)
	merge(&result.Rights.AddAdmins, requested.Rights.AddAdmins)
	merge(&result.Rights.Anonymous, requested.Rights.Anonymous)
	merge(&result.Rights.ManageCall, requested.Rights.ManageCall)
	merge(&result.Rights.ManageTopics, requested.Rights.ManageTopics)
	merge(&result.Rights.PostStories, requested.Rights.PostStories)
	merge(&result.Rights.EditStories, requested.Rights.EditStories)
	merge(&result.Rights.DeleteStories, requested.Rights.DeleteStories)
	if requested.Rank != nil {
		rank := *requested.Rank
		result.Rank = &rank
	}
	return &result
}
