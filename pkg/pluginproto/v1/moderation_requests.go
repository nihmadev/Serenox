package v1

type ModerationOperation string

const (
	ModerationGetMember          ModerationOperation = "members.get"
	ModerationListMembers        ModerationOperation = "members.list"
	ModerationRestrictMember     ModerationOperation = "members.restrict"
	ModerationUnrestrictMember   ModerationOperation = "members.unrestrict"
	ModerationBanMember          ModerationOperation = "members.ban"
	ModerationUnbanMember        ModerationOperation = "members.unban"
	ModerationKickMember         ModerationOperation = "members.kick"
	ModerationGetActingRights    ModerationOperation = "admins.get_acting_rights"
	ModerationGetAdmin           ModerationOperation = "admins.get"
	ModerationListAdmins         ModerationOperation = "admins.list"
	ModerationSetAdmin           ModerationOperation = "admins.set"
	ModerationDemoteAdmin        ModerationOperation = "admins.demote"
	ModerationDeleteMessages     ModerationOperation = "messages.delete"
	ModerationPinMessage         ModerationOperation = "messages.pin"
	ModerationUnpinMessage       ModerationOperation = "messages.unpin"
	ModerationUnpinAll           ModerationOperation = "messages.unpin_all"
	ModerationGetPinned          ModerationOperation = "messages.get_pinned"
	ModerationGetSettings        ModerationOperation = "settings.get"
	ModerationSetDefaultRights   ModerationOperation = "settings.set_default_rights"
	ModerationSetSlowMode        ModerationOperation = "settings.set_slow_mode"
	ModerationSetProtected       ModerationOperation = "settings.set_protected_content"
	ModerationListJoinRequests   ModerationOperation = "join_requests.list"
	ModerationApproveJoin        ModerationOperation = "join_requests.approve"
	ModerationDeclineJoin        ModerationOperation = "join_requests.decline"
	ModerationCreateInvite       ModerationOperation = "invites.create"
	ModerationListInvites        ModerationOperation = "invites.list"
	ModerationEditInvite         ModerationOperation = "invites.edit"
	ModerationRevokeInvite       ModerationOperation = "invites.revoke"
	ModerationGetTopic           ModerationOperation = "topics.get"
	ModerationCloseTopic         ModerationOperation = "topics.close"
	ModerationReopenTopic        ModerationOperation = "topics.reopen"
	ModerationHideGeneralTopic   ModerationOperation = "topics.hide_general"
	ModerationUnhideGeneralTopic ModerationOperation = "topics.unhide_general"
	ModerationDeleteTopic        ModerationOperation = "topics.delete"
)

type ModerationRequest struct {
	Operation      ModerationOperation          `json:"operation"`
	RequestID      string                       `json:"request_id"`
	Chat           ModerationChatTarget         `json:"chat"`
	Participant    *ModerationParticipantTarget `json:"participant,omitempty"`
	Messages       []ModerationMessageTarget    `json:"messages,omitempty"`
	Restriction    *Restriction                 `json:"restriction,omitempty"`
	Admin          *AdminRole                   `json:"admin,omitempty"`
	Settings       *ChatModerationSettings      `json:"settings,omitempty"`
	Page           *ModerationPageRequest       `json:"page,omitempty"`
	Invite         *InviteSettings              `json:"invite,omitempty"`
	Topic          *ForumTopicTarget            `json:"topic,omitempty"`
	Silent         bool                         `json:"silent,omitempty"`
	OneSide        bool                         `json:"one_side,omitempty"`
	Reason         string                       `json:"reason,omitempty"`
	IdempotencyKey string                       `json:"idempotency_key,omitempty"`
	ExpectedState  string                       `json:"expected_state,omitempty"`
}

func (o ModerationOperation) Mutating() bool {
	switch o {
	case ModerationGetMember, ModerationListMembers, ModerationGetActingRights,
		ModerationGetAdmin, ModerationListAdmins, ModerationGetPinned,
		ModerationGetSettings, ModerationListJoinRequests, ModerationListInvites,
		ModerationGetTopic:
		return false
	default:
		return true
	}
}
