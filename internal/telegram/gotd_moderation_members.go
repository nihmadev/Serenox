package telegram

import (
	"context"

	"github.com/gotd/td/tg"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type gotdMemberAPI interface {
	ChannelsGetParticipant(context.Context, *tg.ChannelsGetParticipantRequest) (*tg.ChannelsChannelParticipant, error)
	ChannelsGetParticipants(context.Context, *tg.ChannelsGetParticipantsRequest) (tg.ChannelsChannelParticipantsClass, error)
	ChannelsGetFullChannel(context.Context, tg.InputChannelClass) (*tg.MessagesChatFull, error)
	ChannelsEditBanned(context.Context, *tg.ChannelsEditBannedRequest) (tg.UpdatesClass, error)
}

func (t *LiveTransport) ModerationGetMember(ctx context.Context, chat pluginv1.ModerationChatTarget, target pluginv1.ModerationParticipantTarget) (*pluginv1.ModerationMember, error) {
	_, channel, kind, _, err := t.moderationChat(chat)
	if err != nil {
		return nil, err
	}
	if err := assertChannel(channel, kind); err != nil {
		return nil, err
	}
	peer, _, _, err := t.moderationTarget(ctx, chat, target)
	if err != nil {
		return nil, err
	}
	api, ok := t.api.(gotdMemberAPI)
	if !ok {
		return nil, moderationUnsupported("gotd participant methods are unavailable")
	}
	result, err := api.ChannelsGetParticipant(ctx, &tg.ChannelsGetParticipantRequest{Channel: channel, Participant: peer})
	if err != nil {
		return nil, safeModerationRPCError(err)
	}
	t.peers.learn(entitiesFromParts(result.Users, result.Chats))
	member, err := convertChannelParticipant(result.Participant)
	if err != nil {
		return nil, err
	}
	return &member, nil
}

func (t *LiveTransport) ModerationListMembers(ctx context.Context, chat pluginv1.ModerationChatTarget, page pluginv1.ModerationPageRequest) ([]pluginv1.ModerationMember, *pluginv1.PageInfo, error) {
	_, channel, kind, _, err := t.moderationChat(chat)
	if err != nil {
		return nil, nil, err
	}
	if err := assertChannel(channel, kind); err != nil {
		return nil, nil, err
	}
	filter, err := participantFilter(page)
	if err != nil {
		return nil, nil, err
	}
	api, ok := t.api.(gotdMemberAPI)
	if !ok {
		return nil, nil, moderationUnsupported("gotd participant methods are unavailable")
	}
	raw, err := api.ChannelsGetParticipants(ctx, &tg.ChannelsGetParticipantsRequest{Channel: channel, Filter: filter, Offset: page.Offset, Limit: page.Limit})
	if err != nil {
		return nil, nil, safeModerationRPCError(err)
	}
	result, ok := raw.(*tg.ChannelsChannelParticipants)
	if !ok {
		return nil, nil, &pluginv1.Error{Code: pluginv1.ErrorConversion, Message: "Telegram returned an unconvertible participant page"}
	}
	t.peers.learn(entitiesFromParts(result.Users, result.Chats))
	members := make([]pluginv1.ModerationMember, 0, len(result.Participants))
	for _, participant := range result.Participants {
		member, convertErr := convertChannelParticipant(participant)
		if convertErr != nil {
			return nil, nil, convertErr
		}
		members = append(members, member)
	}
	return members, &pluginv1.PageInfo{HasMore: page.Offset+len(members) < result.Count, TotalHint: int64(result.Count), BackendCallsUsed: 1}, nil
}

func participantFilter(page pluginv1.ModerationPageRequest) (tg.ChannelParticipantsFilterClass, error) {
	switch page.Filter {
	case "", "recent":
		if page.Query != "" {
			return &tg.ChannelParticipantsSearch{Q: page.Query}, nil
		}
		return &tg.ChannelParticipantsRecent{}, nil
	case "administrators":
		return &tg.ChannelParticipantsAdmins{}, nil
	case "restricted":
		return &tg.ChannelParticipantsBanned{Q: page.Query}, nil
	case "banned":
		return &tg.ChannelParticipantsKicked{Q: page.Query}, nil
	default:
		return nil, &pluginv1.Error{Code: pluginv1.ErrorInvalidRequest, Message: "unsupported participant filter"}
	}
}

func (t *LiveTransport) ModerationSetRestriction(ctx context.Context, chat pluginv1.ModerationChatTarget, target pluginv1.ModerationParticipantTarget, restriction *pluginv1.Restriction) error {
	_, channel, kind, _, err := t.moderationChat(chat)
	if err != nil {
		return err
	}
	if err := assertChannel(channel, kind); err != nil {
		return err
	}
	if kind == pluginv1.ModerationChatBroadcast {
		return &pluginv1.Error{Code: pluginv1.ErrorModerationUnsupportedChat, Message: "per-member send restrictions do not apply coherently to broadcast channels; use ban or unban"}
	}
	peer, _, id, err := t.moderationTarget(ctx, chat, target)
	if err != nil {
		return err
	}
	if err := t.assertNotSelfTarget(id); err != nil {
		return err
	}
	api, ok := t.api.(gotdMemberAPI)
	if !ok {
		return moderationUnsupported("gotd restriction methods are unavailable")
	}
	var current *tg.ChatBannedRights
	if restriction != nil {
		value, getErr := api.ChannelsGetParticipant(ctx, &tg.ChannelsGetParticipantRequest{Channel: channel, Participant: peer})
		if getErr == nil {
			member, convertErr := convertChannelParticipant(value.Participant)
			if convertErr != nil {
				return convertErr
			}
			if err := t.assertRestrictableTarget(member); err != nil {
				return err
			}
			if banned, yes := value.Participant.(*tg.ChannelParticipantBanned); yes {
				copy := banned.BannedRights
				current = &copy
			}
		}
		if getErr != nil {
			mapped := safeModerationRPCErrorAt(getErr, "channels.getParticipant(target)", "")
			var wire *pluginv1.Error
			if !errorsAsNotFound(mapped, &wire) {
				return mapped
			}
		}
	}
	rights, err := restrictionToTG(restriction, current)
	if err != nil {
		return err
	}
	_, err = api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{Channel: channel, Participant: peer, BannedRights: rights})
	return t.bannedMutationError(ctx, api, channel, err)
}

func (t *LiveTransport) assertRestrictableTarget(member pluginv1.ModerationMember) error {
	if err := t.assertNotSelfTarget(member.UserID); err != nil {
		return err
	}
	if member.Status == pluginv1.MemberCreator || member.Status == pluginv1.MemberAdmin {
		return &pluginv1.Error{Code: pluginv1.ErrorModerationProtectedTarget, Message: "Telegram does not allow restricting a creator or administrator; demote the target first"}
	}
	return nil
}

func (t *LiveTransport) assertNotSelfTarget(userID int64) error {
	t.mu.Lock()
	selfID := t.identity.UserID
	t.mu.Unlock()
	if userID == selfID && selfID != 0 {
		return &pluginv1.Error{Code: pluginv1.ErrorModerationProtectedTarget, Message: "the acting account cannot restrict itself"}
	}
	return nil
}

func (t *LiveTransport) ModerationSetBanned(ctx context.Context, chat pluginv1.ModerationChatTarget, target pluginv1.ModerationParticipantTarget, banned, kick bool) error {
	if kick {
		return moderationUnsupported("live kick is unsupported because ban-then-unban cannot be represented atomically")
	}
	_, channel, kind, _, err := t.moderationChat(chat)
	if err != nil {
		return err
	}
	if err := assertChannel(channel, kind); err != nil {
		return err
	}
	peer, _, id, err := t.moderationTarget(ctx, chat, target)
	if err != nil {
		return err
	}
	if err := t.assertNotSelfTarget(id); err != nil {
		return err
	}
	api, ok := t.api.(gotdMemberAPI)
	if !ok {
		return moderationUnsupported("gotd ban methods are unavailable")
	}
	rights := tg.ChatBannedRights{}
	if banned {
		value, getErr := api.ChannelsGetParticipant(ctx, &tg.ChannelsGetParticipantRequest{Channel: channel, Participant: peer})
		if getErr == nil {
			member, convertErr := convertChannelParticipant(value.Participant)
			if convertErr != nil {
				return convertErr
			}
			if err := t.assertRestrictableTarget(member); err != nil {
				return err
			}
		} else {
			mapped := safeModerationRPCErrorAt(getErr, "channels.getParticipant(target)", "")
			var wire *pluginv1.Error
			if !errorsAsNotFound(mapped, &wire) {
				return mapped
			}
		}
		rights.ViewMessages = true
	}
	_, err = api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{Channel: channel, Participant: peer, BannedRights: rights})
	return t.bannedMutationError(ctx, api, channel, err)
}

// bannedMutationError performs one read-only diagnostic only after Telegram
// has definitively rejected editBanned for administrator access. This lets us
// distinguish "not an admin" from "admin without ban_users", and also makes a
// contradictory Telegram response explicit instead of lying to the operator.
func (t *LiveTransport) bannedMutationError(ctx context.Context, api gotdMemberAPI, channel *tg.InputChannel, err error) error {
	if err == nil {
		return nil
	}
	mapped := safeModerationMutationErrorAt(err, "channels.editBanned", "ban_users")
	wire, ok := mapped.(*pluginv1.Error)
	if !ok || wire.Code != pluginv1.ErrorModerationRightRequired {
		return mapped
	}
	result, diagnosticErr := api.ChannelsGetFullChannel(ctx, channel)
	if diagnosticErr != nil || result == nil {
		wire.Details["acting_rights_check"] = "unavailable"
		return wire
	}
	t.peers.learn(entitiesFromParts(result.Users, result.Chats))
	t.mu.Lock()
	selfID := t.identity.UserID
	t.mu.Unlock()
	member, found := channelActingMember(result.Chats, channel.ChannelID, selfID)
	if !found {
		wire.Details["acting_rights_check"] = "unavailable"
		return wire
	}
	return editBannedPermissionError(member)
}

// channelActingMember follows Telegram's documented source of truth for the
// current session: channel.creator and channel.admin_rights on the Chat entity.
// channelParticipantSelf only describes membership and must not be treated as
// proof that the current session is an ordinary member.
func channelActingMember(chats []tg.ChatClass, channelID, selfID int64) (pluginv1.ModerationMember, bool) {
	for _, item := range chats {
		channel, ok := item.(*tg.Channel)
		if !ok || channel.ID != channelID {
			continue
		}
		if channel.Creator {
			role := &pluginv1.AdminRole{}
			if rights, present := channel.GetAdminRights(); present {
				role = adminFromTG(rights, "")
			}
			return pluginv1.ModerationMember{UserID: selfID, Status: pluginv1.MemberCreator, Admin: role}, true
		}
		if rights, present := channel.GetAdminRights(); present {
			return pluginv1.ModerationMember{UserID: selfID, Status: pluginv1.MemberAdmin, Admin: adminFromTG(rights, "")}, true
		}
		return pluginv1.ModerationMember{UserID: selfID, Status: pluginv1.MemberRegular}, true
	}
	return pluginv1.ModerationMember{}, false
}

func editBannedPermissionError(acting pluginv1.ModerationMember) error {
	details := moderationErrorDetails("channels.editBanned", "ban_users")
	details["acting_status"] = string(acting.Status)
	switch acting.Status {
	case pluginv1.MemberCreator:
		details["effective_right"] = "creator"
		return &pluginv1.Error{
			Code:    pluginv1.ErrorModerationRightRequired,
			Message: "Telegram denied channels.editBanned even though the acting session is the creator in this exact chat",
			Details: details,
		}
	case pluginv1.MemberAdmin:
		if acting.Admin != nil && acting.Admin.Rights.BanUsers != nil && *acting.Admin.Rights.BanUsers {
			details["effective_right"] = "true"
			return &pluginv1.Error{
				Code:    pluginv1.ErrorModerationRightRequired,
				Message: "Telegram denied channels.editBanned even though the acting session reports ban_users=true in this exact chat",
				Details: details,
			}
		}
		details["effective_right"] = "false"
		return &pluginv1.Error{
			Code:    pluginv1.ErrorModerationRightRequired,
			Message: "the acting account is an administrator in this exact chat but Telegram reports ban_users=false",
			Details: details,
		}
	default:
		details["effective_right"] = "false"
		return &pluginv1.Error{
			Code:    pluginv1.ErrorModerationAdminRequired,
			Message: "Telegram does not report the acting account as an administrator in this exact chat",
			Details: details,
		}
	}
}

func errorsAsNotFound(err error, target **pluginv1.Error) bool {
	if value, ok := err.(*pluginv1.Error); ok {
		*target = value
		return value.Code == pluginv1.ErrorNotFound
	}
	return false
}

func convertChannelParticipant(value tg.ChannelParticipantClass) (pluginv1.ModerationMember, error) {
	switch item := value.(type) {
	case *tg.ChannelParticipant:
		return pluginv1.ModerationMember{UserID: item.UserID, Status: pluginv1.MemberRegular}, nil
	case *tg.ChannelParticipantSelf:
		return pluginv1.ModerationMember{UserID: item.UserID, Status: pluginv1.MemberRegular}, nil
	case *tg.ChannelParticipantCreator:
		return pluginv1.ModerationMember{UserID: item.UserID, Status: pluginv1.MemberCreator, Admin: adminFromTG(item.AdminRights, item.Rank)}, nil
	case *tg.ChannelParticipantAdmin:
		return pluginv1.ModerationMember{UserID: item.UserID, Status: pluginv1.MemberAdmin, Admin: adminFromTG(item.AdminRights, item.Rank)}, nil
	case *tg.ChannelParticipantBanned:
		id, peerKind := peerIDAndKind(item.Peer)
		if peerKind != "private" || id <= 0 {
			return pluginv1.ModerationMember{}, &pluginv1.Error{Code: pluginv1.ErrorConversion, Message: "banned participant is not a user"}
		}
		status := pluginv1.MemberRestricted
		if item.BannedRights.ViewMessages {
			status = pluginv1.MemberBanned
		}
		return pluginv1.ModerationMember{UserID: id, Status: status, Restriction: restrictionFromTG(item.BannedRights)}, nil
	case *tg.ChannelParticipantLeft:
		id, peerKind := peerIDAndKind(item.Peer)
		if peerKind != "private" || id <= 0 {
			return pluginv1.ModerationMember{}, &pluginv1.Error{Code: pluginv1.ErrorConversion, Message: "left participant is not a user"}
		}
		return pluginv1.ModerationMember{UserID: id, Status: pluginv1.MemberLeft}, nil
	default:
		return pluginv1.ModerationMember{}, &pluginv1.Error{Code: pluginv1.ErrorConversion, Message: "unknown Telegram participant constructor"}
	}
}
