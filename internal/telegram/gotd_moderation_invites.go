package telegram

import (
	"context"
	"math"

	"github.com/gotd/td/tg"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type gotdModerationInviteAPI interface {
	MessagesGetChatInviteImporters(context.Context, *tg.MessagesGetChatInviteImportersRequest) (*tg.MessagesChatInviteImporters, error)
	MessagesHideChatJoinRequest(context.Context, *tg.MessagesHideChatJoinRequestRequest) (tg.UpdatesClass, error)
	MessagesExportChatInvite(context.Context, *tg.MessagesExportChatInviteRequest) (tg.ExportedChatInviteClass, error)
	MessagesGetExportedChatInvites(context.Context, *tg.MessagesGetExportedChatInvitesRequest) (*tg.MessagesExportedChatInvites, error)
	MessagesEditExportedChatInvite(context.Context, *tg.MessagesEditExportedChatInviteRequest) (tg.MessagesExportedChatInviteClass, error)
}

func (t *LiveTransport) ModerationListJoinRequests(ctx context.Context, chat pluginv1.ModerationChatTarget, page pluginv1.ModerationPageRequest) ([]pluginv1.JoinRequestSummary, *pluginv1.PageInfo, error) {
	peer, _, kind, _, err := t.moderationChat(chat)
	if err != nil {
		return nil, nil, err
	}
	if kind == pluginv1.ModerationChatPrivate {
		return nil, nil, &pluginv1.Error{Code: pluginv1.ErrorModerationUnsupportedChat, Message: "private dialogs do not have join requests"}
	}
	if page.Offset != 0 {
		return nil, nil, moderationUnsupported("live join-request continuation is gated until the two-field Telegram cursor is represented")
	}
	api, ok := t.api.(gotdModerationInviteAPI)
	if !ok {
		return nil, nil, moderationUnsupported("gotd join request methods are unavailable")
	}
	request := &tg.MessagesGetChatInviteImportersRequest{Requested: true, Peer: peer, Q: page.Query, Limit: page.Limit}
	result, err := api.MessagesGetChatInviteImporters(ctx, request)
	if err != nil {
		return nil, nil, safeModerationRPCError(err)
	}
	t.peers.learn(entitiesFromParts(result.Users, nil))
	items := make([]pluginv1.JoinRequestSummary, 0, len(result.Importers))
	for _, item := range result.Importers {
		if item.Requested {
			items = append(items, pluginv1.JoinRequestSummary{UserID: item.UserID, DateUnixMS: int64(item.Date) * 1000})
		}
	}
	return items, &pluginv1.PageInfo{HasMore: false, TotalHint: int64(result.Count), BackendCallsUsed: 1}, nil
}

func (t *LiveTransport) ModerationDecideJoinRequest(ctx context.Context, chat pluginv1.ModerationChatTarget, target pluginv1.ModerationParticipantTarget, approve bool) error {
	peer, _, kind, _, err := t.moderationChat(chat)
	if err != nil {
		return err
	}
	if kind == pluginv1.ModerationChatPrivate {
		return &pluginv1.Error{Code: pluginv1.ErrorModerationUnsupportedChat, Message: "private dialogs do not have join requests"}
	}
	_, user, _, err := t.moderationTarget(ctx, chat, target)
	if err != nil {
		return err
	}
	api, ok := t.api.(gotdModerationInviteAPI)
	if !ok {
		return moderationUnsupported("gotd join request methods are unavailable")
	}
	_, err = api.MessagesHideChatJoinRequest(ctx, &tg.MessagesHideChatJoinRequestRequest{Approved: approve, Peer: peer, UserID: user})
	return safeModerationMutationError(err)
}

func (t *LiveTransport) ModerationCreateInvite(ctx context.Context, chat pluginv1.ModerationChatTarget, settings pluginv1.InviteSettings) (*pluginv1.InviteSummary, error) {
	peer, _, kind, _, err := t.moderationChat(chat)
	if err != nil {
		return nil, err
	}
	if kind == pluginv1.ModerationChatPrivate {
		return nil, &pluginv1.Error{Code: pluginv1.ErrorModerationUnsupportedChat, Message: "private dialogs do not have invite links"}
	}
	api, ok := t.api.(gotdModerationInviteAPI)
	if !ok {
		return nil, moderationUnsupported("gotd invite methods are unavailable")
	}
	request := &tg.MessagesExportChatInviteRequest{Peer: peer}
	if settings.Name != nil {
		request.SetTitle(*settings.Name)
	}
	if settings.ExpireUnixMS != nil {
		if *settings.ExpireUnixMS/1000 > math.MaxInt32 {
			return nil, &pluginv1.Error{Code: pluginv1.ErrorModerationInvalidDeadline, Message: "invite expiry exceeds Telegram range"}
		}
		request.SetExpireDate(int(*settings.ExpireUnixMS / 1000))
	}
	if settings.UsageLimit != nil {
		request.SetUsageLimit(*settings.UsageLimit)
	}
	if settings.RequireApproval != nil {
		request.RequestNeeded = *settings.RequireApproval
	}
	raw, err := api.MessagesExportChatInvite(ctx, request)
	if err != nil {
		return nil, safeModerationMutationError(err)
	}
	return convertInvite(raw)
}

func (t *LiveTransport) ModerationListInvites(ctx context.Context, chat pluginv1.ModerationChatTarget, page pluginv1.ModerationPageRequest) ([]pluginv1.InviteSummary, *pluginv1.PageInfo, error) {
	peer, _, kind, _, err := t.moderationChat(chat)
	if err != nil {
		return nil, nil, err
	}
	if kind == pluginv1.ModerationChatPrivate {
		return nil, nil, &pluginv1.Error{Code: pluginv1.ErrorModerationUnsupportedChat, Message: "private dialogs do not have invite links"}
	}
	if page.Offset != 0 {
		return nil, nil, moderationUnsupported("live invite continuation is gated until the Telegram date/link cursor is represented")
	}
	api, ok := t.api.(gotdModerationInviteAPI)
	if !ok {
		return nil, nil, moderationUnsupported("gotd invite methods are unavailable")
	}
	result, err := api.MessagesGetExportedChatInvites(ctx, &tg.MessagesGetExportedChatInvitesRequest{Peer: peer, AdminID: &tg.InputUserSelf{}, Limit: page.Limit})
	if err != nil {
		return nil, nil, safeModerationRPCError(err)
	}
	t.peers.learn(entitiesFromParts(result.Users, nil))
	items := make([]pluginv1.InviteSummary, 0, len(result.Invites))
	for _, raw := range result.Invites {
		item, convertErr := convertInvite(raw)
		if convertErr != nil {
			return nil, nil, convertErr
		}
		items = append(items, *item)
	}
	return items, &pluginv1.PageInfo{HasMore: false, TotalHint: int64(result.Count), BackendCallsUsed: 1}, nil
}

func (t *LiveTransport) ModerationEditInvite(ctx context.Context, chat pluginv1.ModerationChatTarget, settings pluginv1.InviteSettings) (*pluginv1.InviteSummary, error) {
	peer, _, _, _, err := t.moderationChat(chat)
	if err != nil {
		return nil, err
	}
	api, ok := t.api.(gotdModerationInviteAPI)
	if !ok {
		return nil, moderationUnsupported("gotd invite methods are unavailable")
	}
	request := &tg.MessagesEditExportedChatInviteRequest{Peer: peer, Link: settings.Link}
	if settings.Name != nil {
		request.SetTitle(*settings.Name)
	}
	if settings.ExpireUnixMS != nil {
		request.SetExpireDate(int(*settings.ExpireUnixMS / 1000))
	}
	if settings.UsageLimit != nil {
		request.SetUsageLimit(*settings.UsageLimit)
	}
	if settings.RequireApproval != nil {
		request.SetRequestNeeded(*settings.RequireApproval)
	}
	raw, err := api.MessagesEditExportedChatInvite(ctx, request)
	if err != nil {
		return nil, safeModerationMutationError(err)
	}
	result, ok := raw.(*tg.MessagesExportedChatInvite)
	if !ok {
		return nil, &pluginv1.Error{Code: pluginv1.ErrorConversion, Message: "Telegram returned an unconvertible invite"}
	}
	return convertInvite(result.Invite)
}

func (t *LiveTransport) ModerationRevokeInvite(ctx context.Context, chat pluginv1.ModerationChatTarget, settings pluginv1.InviteSettings) error {
	peer, _, _, _, err := t.moderationChat(chat)
	if err != nil {
		return err
	}
	api, ok := t.api.(gotdModerationInviteAPI)
	if !ok {
		return moderationUnsupported("gotd invite methods are unavailable")
	}
	request := &tg.MessagesEditExportedChatInviteRequest{Peer: peer, Link: settings.Link}
	request.SetRevoked(true)
	_, err = api.MessagesEditExportedChatInvite(ctx, request)
	return safeModerationMutationError(err)
}

func convertInvite(raw tg.ExportedChatInviteClass) (*pluginv1.InviteSummary, error) {
	value, ok := raw.(*tg.ChatInviteExported)
	if !ok {
		return nil, &pluginv1.Error{Code: pluginv1.ErrorConversion, Message: "Telegram returned an unsupported invite constructor"}
	}
	return &pluginv1.InviteSummary{Link: value.Link, Name: value.Title, CreatedUnixMS: int64(value.Date) * 1000, ExpireUnixMS: int64(value.ExpireDate) * 1000, UsageLimit: value.UsageLimit, UsageCount: value.Usage, RequireApproval: value.RequestNeeded, Revoked: value.Revoked}, nil
}
