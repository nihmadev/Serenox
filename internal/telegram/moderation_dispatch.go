package telegram

import (
	"context"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func baseModerationResult(r pluginv1.ModerationRequest) pluginv1.ModerationResult {
	result := pluginv1.ModerationResult{Operation: r.Operation, RequestID: r.RequestID, ChatID: r.Chat.ChatID, Status: pluginv1.ActionResultSuccess}
	if r.Participant != nil {
		result.TargetUserID = r.Participant.UserID
	}
	return result
}

func (s *ModerationService) dispatch(ctx context.Context, r pluginv1.ModerationRequest) (pluginv1.ModerationResult, error) {
	result := baseModerationResult(r)
	switch r.Operation {
	case pluginv1.ModerationGetMember, pluginv1.ModerationGetAdmin:
		member, err := s.backend.ModerationGetMember(ctx, r.Chat, *r.Participant)
		result.Member = member
		return result, err
	case pluginv1.ModerationListMembers, pluginv1.ModerationListAdmins:
		page := pageRequest(r)
		if r.Operation == pluginv1.ModerationListAdmins {
			page.Filter = "administrators"
		}
		members, info, err := s.backend.ModerationListMembers(ctx, r.Chat, page)
		result.Members, result.Page = members, info
		return result, err
	case pluginv1.ModerationRestrictMember:
		err := s.backend.ModerationSetRestriction(ctx, r.Chat, *r.Participant, r.Restriction)
		result.Acknowledged = err == nil
		return result, err
	case pluginv1.ModerationUnrestrictMember:
		err := s.backend.ModerationSetRestriction(ctx, r.Chat, *r.Participant, nil)
		result.Acknowledged = err == nil
		return result, err
	case pluginv1.ModerationBanMember, pluginv1.ModerationUnbanMember, pluginv1.ModerationKickMember:
		err := s.backend.ModerationSetBanned(ctx, r.Chat, *r.Participant, r.Operation != pluginv1.ModerationUnbanMember, r.Operation == pluginv1.ModerationKickMember)
		result.Acknowledged = err == nil
		return result, err
	case pluginv1.ModerationGetActingRights:
		member, err := s.backend.ModerationGetActingRights(ctx, r.Chat)
		result.Member = member
		return result, err
	case pluginv1.ModerationSetAdmin, pluginv1.ModerationDemoteAdmin:
		role := r.Admin
		if r.Operation == pluginv1.ModerationDemoteAdmin {
			role = nil
		}
		err := s.backend.ModerationSetAdmin(ctx, r.Chat, *r.Participant, role)
		result.Acknowledged = err == nil
		return result, err
	case pluginv1.ModerationDeleteMessages:
		items, err := s.backend.ModerationDeleteMessages(ctx, r.Chat, r.Messages)
		result.Items, result.Acknowledged = items, err == nil
		return result, err
	case pluginv1.ModerationPinMessage, pluginv1.ModerationUnpinMessage, pluginv1.ModerationUnpinAll:
		var target *pluginv1.ModerationMessageTarget
		if len(r.Messages) != 0 {
			target = &r.Messages[0]
		}
		err := s.backend.ModerationPin(ctx, r.Chat, target, r.Operation == pluginv1.ModerationPinMessage, r.Silent, r.OneSide)
		result.Acknowledged = err == nil
		return result, err
	case pluginv1.ModerationGetPinned:
		return result, &pluginv1.Error{Code: pluginv1.ErrorUnsupported, Message: "use the typed client messages.get_pinned read until the moderation snapshot adapter is verified"}
	case pluginv1.ModerationGetSettings:
		settings, err := s.backend.ModerationGetSettings(ctx, r.Chat)
		result.Settings = settings
		return result, err
	case pluginv1.ModerationSetDefaultRights, pluginv1.ModerationSetSlowMode, pluginv1.ModerationSetProtected:
		err := s.backend.ModerationSetSettings(ctx, r.Chat, r.Operation, *r.Settings)
		result.Acknowledged = err == nil
		return result, err
	case pluginv1.ModerationListJoinRequests:
		items, page, err := s.backend.ModerationListJoinRequests(ctx, r.Chat, pageRequest(r))
		result.JoinRequests, result.Page = items, page
		return result, err
	case pluginv1.ModerationApproveJoin, pluginv1.ModerationDeclineJoin:
		err := s.backend.ModerationDecideJoinRequest(ctx, r.Chat, *r.Participant, r.Operation == pluginv1.ModerationApproveJoin)
		result.Acknowledged = err == nil
		return result, err
	case pluginv1.ModerationCreateInvite:
		invite, err := s.backend.ModerationCreateInvite(ctx, r.Chat, *r.Invite)
		if invite != nil {
			result.Invites = []pluginv1.InviteSummary{*invite}
		}
		return result, err
	case pluginv1.ModerationListInvites:
		items, page, err := s.backend.ModerationListInvites(ctx, r.Chat, pageRequest(r))
		result.Invites, result.Page = items, page
		return result, err
	case pluginv1.ModerationEditInvite:
		invite, err := s.backend.ModerationEditInvite(ctx, r.Chat, *r.Invite)
		if invite != nil {
			result.Invites = []pluginv1.InviteSummary{*invite}
		}
		return result, err
	case pluginv1.ModerationRevokeInvite:
		err := s.backend.ModerationRevokeInvite(ctx, r.Chat, *r.Invite)
		result.Acknowledged = err == nil
		return result, err
	case pluginv1.ModerationGetTopic:
		topic, err := s.backend.ModerationGetTopic(ctx, r.Chat, *r.Topic)
		result.Topic = topic
		return result, err
	default:
		err := s.backend.ModerationSetTopic(ctx, r.Chat, r.Operation, topicRequest(r))
		result.Acknowledged = err == nil
		return result, err
	}
}

func pageRequest(r pluginv1.ModerationRequest) pluginv1.ModerationPageRequest {
	if r.Page == nil {
		return pluginv1.ModerationPageRequest{Limit: pluginv1.MaxModerationPageItems}
	}
	value := *r.Page
	if value.Limit == 0 {
		value.Limit = pluginv1.MaxModerationPageItems
	}
	return value
}
func topicRequest(r pluginv1.ModerationRequest) pluginv1.ForumTopicTarget {
	if r.Topic != nil {
		return *r.Topic
	}
	return pluginv1.ForumTopicTarget{}
}
