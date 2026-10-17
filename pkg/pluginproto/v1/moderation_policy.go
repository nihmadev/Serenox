package v1

const (
	PermissionModerationReadChat       = "telegram.moderation.chat.read"
	PermissionModerationReadMembers    = "telegram.moderation.members.read"
	PermissionModerationRestrict       = "telegram.moderation.members.restrict"
	PermissionModerationBan            = "telegram.moderation.members.ban"
	PermissionModerationAdminsRead     = "telegram.moderation.admins.read"
	PermissionModerationAdminsManage   = "telegram.moderation.admins.manage"
	PermissionModerationMessagesDelete = "telegram.moderation.messages.delete"
	PermissionModerationMessagesPin    = "telegram.moderation.messages.pin"
	PermissionModerationUnpinAll       = "telegram.moderation.messages.unpin_all"
	PermissionModerationSettings       = "telegram.moderation.settings.manage"
	PermissionModerationJoinRead       = "telegram.moderation.join_requests.read"
	PermissionModerationJoinManage     = "telegram.moderation.join_requests.manage"
	PermissionModerationInvitesRead    = "telegram.moderation.invites.read"
	PermissionModerationInvitesManage  = "telegram.moderation.invites.manage"
	PermissionModerationTopicsManage   = "telegram.moderation.topics.manage"
	PermissionModerationTopicsDelete   = "telegram.moderation.topics.delete"
)

// ModerationPermission is the authoritative operation-to-permission mapping
// shared by package admission and immediate pre-invocation checks.
func ModerationPermission(operation ModerationOperation) string {
	switch operation {
	case ModerationGetMember, ModerationListMembers:
		return PermissionModerationReadMembers
	case ModerationRestrictMember, ModerationUnrestrictMember:
		return PermissionModerationRestrict
	case ModerationBanMember, ModerationUnbanMember, ModerationKickMember:
		return PermissionModerationBan
	case ModerationGetActingRights, ModerationGetAdmin, ModerationListAdmins:
		return PermissionModerationAdminsRead
	case ModerationSetAdmin, ModerationDemoteAdmin:
		return PermissionModerationAdminsManage
	case ModerationDeleteMessages:
		return PermissionModerationMessagesDelete
	case ModerationPinMessage, ModerationUnpinMessage:
		return PermissionModerationMessagesPin
	case ModerationUnpinAll:
		return PermissionModerationUnpinAll
	case ModerationGetPinned, ModerationGetSettings:
		return PermissionModerationReadChat
	case ModerationSetDefaultRights, ModerationSetSlowMode, ModerationSetProtected:
		return PermissionModerationSettings
	case ModerationListJoinRequests:
		return PermissionModerationJoinRead
	case ModerationApproveJoin, ModerationDeclineJoin:
		return PermissionModerationJoinManage
	case ModerationListInvites:
		return PermissionModerationInvitesRead
	case ModerationCreateInvite, ModerationEditInvite, ModerationRevokeInvite:
		return PermissionModerationInvitesManage
	case ModerationGetTopic:
		return PermissionModerationReadChat
	case ModerationCloseTopic, ModerationReopenTopic, ModerationHideGeneralTopic, ModerationUnhideGeneralTopic:
		return PermissionModerationTopicsManage
	case ModerationDeleteTopic:
		return PermissionModerationTopicsDelete
	default:
		return ""
	}
}

func KnownModerationPermission(permission string) bool {
	switch permission {
	case PermissionModerationReadChat, PermissionModerationReadMembers, PermissionModerationRestrict,
		PermissionModerationBan, PermissionModerationAdminsRead, PermissionModerationAdminsManage,
		PermissionModerationMessagesDelete, PermissionModerationMessagesPin, PermissionModerationUnpinAll,
		PermissionModerationSettings, PermissionModerationJoinRead, PermissionModerationJoinManage,
		PermissionModerationInvitesRead, PermissionModerationInvitesManage,
		PermissionModerationTopicsManage, PermissionModerationTopicsDelete:
		return true
	default:
		return false
	}
}
