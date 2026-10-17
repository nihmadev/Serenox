package telegram

import (
	"context"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type ModerationScope struct {
	PluginID   string
	AccountID  int64
	Permission string
}

type MemberModerationBackend interface {
	ModerationGetMember(context.Context, pluginv1.ModerationChatTarget, pluginv1.ModerationParticipantTarget) (*pluginv1.ModerationMember, error)
	ModerationListMembers(context.Context, pluginv1.ModerationChatTarget, pluginv1.ModerationPageRequest) ([]pluginv1.ModerationMember, *pluginv1.PageInfo, error)
	ModerationSetRestriction(context.Context, pluginv1.ModerationChatTarget, pluginv1.ModerationParticipantTarget, *pluginv1.Restriction) error
	ModerationSetBanned(context.Context, pluginv1.ModerationChatTarget, pluginv1.ModerationParticipantTarget, bool, bool) error
}

type AdminModerationBackend interface {
	ModerationGetActingRights(context.Context, pluginv1.ModerationChatTarget) (*pluginv1.ModerationMember, error)
	ModerationSetAdmin(context.Context, pluginv1.ModerationChatTarget, pluginv1.ModerationParticipantTarget, *pluginv1.AdminRole) error
}

type MessageModerationBackend interface {
	ModerationDeleteMessages(context.Context, pluginv1.ModerationChatTarget, []pluginv1.ModerationMessageTarget) ([]pluginv1.ModerationItemResult, error)
	ModerationPin(context.Context, pluginv1.ModerationChatTarget, *pluginv1.ModerationMessageTarget, bool, bool, bool) error
}

type SettingsModerationBackend interface {
	ModerationGetSettings(context.Context, pluginv1.ModerationChatTarget) (*pluginv1.ChatModerationSettings, error)
	ModerationSetSettings(context.Context, pluginv1.ModerationChatTarget, pluginv1.ModerationOperation, pluginv1.ChatModerationSettings) error
}

type JoinModerationBackend interface {
	ModerationListJoinRequests(context.Context, pluginv1.ModerationChatTarget, pluginv1.ModerationPageRequest) ([]pluginv1.JoinRequestSummary, *pluginv1.PageInfo, error)
	ModerationDecideJoinRequest(context.Context, pluginv1.ModerationChatTarget, pluginv1.ModerationParticipantTarget, bool) error
}

type InviteModerationBackend interface {
	ModerationCreateInvite(context.Context, pluginv1.ModerationChatTarget, pluginv1.InviteSettings) (*pluginv1.InviteSummary, error)
	ModerationListInvites(context.Context, pluginv1.ModerationChatTarget, pluginv1.ModerationPageRequest) ([]pluginv1.InviteSummary, *pluginv1.PageInfo, error)
	ModerationEditInvite(context.Context, pluginv1.ModerationChatTarget, pluginv1.InviteSettings) (*pluginv1.InviteSummary, error)
	ModerationRevokeInvite(context.Context, pluginv1.ModerationChatTarget, pluginv1.InviteSettings) error
}

type TopicModerationBackend interface {
	ModerationGetTopic(context.Context, pluginv1.ModerationChatTarget, pluginv1.ForumTopicTarget) (*pluginv1.ForumTopicState, error)
	ModerationSetTopic(context.Context, pluginv1.ModerationChatTarget, pluginv1.ModerationOperation, pluginv1.ForumTopicTarget) error
}

type ModerationBackend interface {
	MemberModerationBackend
	AdminModerationBackend
	MessageModerationBackend
	SettingsModerationBackend
	JoinModerationBackend
	InviteModerationBackend
	TopicModerationBackend
}

type ModerationClient interface {
	Moderate(context.Context, ModerationScope, pluginv1.ModerationRequest) (pluginv1.ModerationResult, error)
}

type moderationSupportBackend interface{ ModerationSupportStatus() string }
