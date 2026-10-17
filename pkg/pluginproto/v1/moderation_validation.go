package v1

import (
	"fmt"
	"strings"
)

func (r ModerationRequest) Validate() error {
	if ModerationPermission(r.Operation) == "" {
		return invalid("moderation.operation", "unknown operation %q", r.Operation)
	}
	if err := validIdentifier("moderation.request_id", r.RequestID); err != nil {
		return err
	}
	if r.Chat.ChatID == 0 {
		return invalid("moderation.chat.chat_id", "must be non-zero")
	}
	if r.Chat.ExpectedKind != "" && !knownModerationChatKind(r.Chat.ExpectedKind) {
		return invalid("moderation.chat.expected_kind", "unknown chat kind %q", r.Chat.ExpectedKind)
	}
	if len(r.Messages) > MaxModerationBatchItems {
		return invalid("moderation.messages", "exceeds %d items", MaxModerationBatchItems)
	}
	for i, message := range r.Messages {
		if message.ChatID == 0 || message.ChatID != r.Chat.ChatID || message.MessageID <= 0 || message.TopicID < 0 {
			return invalid(fmt.Sprintf("moderation.messages[%d]", i), "must target a positive message in the request chat")
		}
	}
	if len(r.Reason) > MaxModerationReasonBytes {
		return invalid("moderation.reason", "exceeds %d bytes", MaxModerationReasonBytes)
	}
	if err := validString("moderation.reason", r.Reason, false); err != nil {
		return err
	}
	if len(r.IdempotencyKey) > MaxIdempotencyKeyBytes || len(r.ExpectedState) > 128 {
		return invalid("moderation.idempotency", "key or expected state exceeds its bound")
	}
	if r.ExpectedState != "" && !r.Operation.Mutating() {
		return invalid("moderation.expected_state", "is accepted only for mutations")
	}
	if r.ExpectedState != "" && r.Participant == nil {
		return invalid("moderation.expected_state", "requires a participant target")
	}
	if err := validString("moderation.expected_state", r.ExpectedState, false); err != nil {
		return err
	}
	if r.Operation.Mutating() && r.IdempotencyKey != "" {
		if err := validIdentifier("moderation.idempotency_key", r.IdempotencyKey); err != nil {
			return err
		}
	}
	if err := r.validateParticipant(); err != nil {
		return err
	}
	if err := r.validatePayload(); err != nil {
		return err
	}
	if err := r.validateForbiddenPayload(); err != nil {
		return err
	}
	return nil
}

func (r ModerationRequest) validateParticipant() error {
	required := operationNeedsParticipant(r.Operation)
	if r.Participant == nil {
		if required {
			return invalid("moderation.participant", "is required for %q", r.Operation)
		}
		return nil
	}
	selectors := 0
	if r.Participant.UserID != 0 {
		selectors++
		if r.Participant.UserID < 0 {
			return invalid("moderation.participant.user_id", "must be positive")
		}
	}
	if strings.TrimSpace(r.Participant.Username) != "" {
		selectors++
		if len(r.Participant.Username) > 64 {
			return invalid("moderation.participant.username", "exceeds 64 bytes")
		}
	}
	if r.Participant.Reply != nil {
		selectors++
	}
	if selectors != 1 {
		return invalid("moderation.participant", "must contain exactly one selector")
	}
	if r.Participant.Reply != nil && (r.Participant.Reply.PeerID != r.Chat.ChatID || r.Participant.Reply.MessageID <= 0) {
		return invalid("moderation.participant.reply", "must reference a positive message in the request chat")
	}
	if !required {
		return invalid("moderation.participant", "is not accepted for %q", r.Operation)
	}
	return validString("moderation.participant.username", r.Participant.Username, false)
}

func (r ModerationRequest) validatePayload() error {
	if r.Page != nil {
		if r.Page.Limit < 0 || r.Page.Limit > MaxModerationPageItems || len(r.Page.Cursor) > MaxClientCursorBytes || len(r.Page.Query) > MaxClientQueryBytes || len(r.Page.Filter) > 32 {
			return invalid("moderation.page", "is outside bounded page limits")
		}
		if err := validString("moderation.page.filter", r.Page.Filter, false); err != nil {
			return err
		}
		if err := validString("moderation.page.query", r.Page.Query, false); err != nil {
			return err
		}
	}
	switch r.Operation {
	case ModerationRestrictMember:
		if r.Restriction == nil {
			return invalid("moderation.restriction", "is required")
		}
		if !r.Restriction.Denied.anyPresent() {
			return invalid("moderation.restriction.denied", "must contain at least one explicit right")
		}
		return r.Restriction.Validate()
	case ModerationSetAdmin:
		if r.Admin == nil {
			return invalid("moderation.admin", "is required")
		}
		if !r.Admin.Rights.anyPresent() && r.Admin.Rank == nil {
			return invalid("moderation.admin", "must contain an explicit right or rank")
		}
		return r.Admin.Validate()
	case ModerationDeleteMessages:
		if len(r.Messages) == 0 {
			return invalid("moderation.messages", "must not be empty")
		}
	case ModerationPinMessage, ModerationUnpinMessage:
		if len(r.Messages) != 1 {
			return invalid("moderation.messages", "must contain exactly one item")
		}
	case ModerationSetDefaultRights, ModerationSetSlowMode, ModerationSetProtected:
		if r.Settings == nil {
			return invalid("moderation.settings", "is required")
		}
		return r.Settings.Validate(r.Operation)
	case ModerationCreateInvite, ModerationEditInvite, ModerationRevokeInvite:
		if r.Invite == nil {
			return invalid("moderation.invite", "is required")
		}
		return r.Invite.Validate(r.Operation)
	case ModerationGetTopic, ModerationCloseTopic, ModerationReopenTopic, ModerationDeleteTopic:
		if r.Topic == nil || r.Topic.TopicID <= 0 {
			return invalid("moderation.topic", "positive topic_id is required")
		}
	}
	return nil
}

func (r ModerationRequest) validateForbiddenPayload() error {
	pageAllowed := r.Operation == ModerationListMembers || r.Operation == ModerationListAdmins || r.Operation == ModerationListJoinRequests || r.Operation == ModerationListInvites
	messagesAllowed := r.Operation == ModerationDeleteMessages || r.Operation == ModerationPinMessage || r.Operation == ModerationUnpinMessage
	topicAllowed := r.Operation == ModerationGetTopic || r.Operation == ModerationCloseTopic || r.Operation == ModerationReopenTopic || r.Operation == ModerationDeleteTopic
	inviteAllowed := r.Operation == ModerationCreateInvite || r.Operation == ModerationEditInvite || r.Operation == ModerationRevokeInvite
	settingsAllowed := r.Operation == ModerationSetDefaultRights || r.Operation == ModerationSetSlowMode || r.Operation == ModerationSetProtected
	if r.Page != nil && !pageAllowed || len(r.Messages) != 0 && !messagesAllowed || r.Topic != nil && !topicAllowed || r.Invite != nil && !inviteAllowed || r.Settings != nil && !settingsAllowed {
		return invalid("moderation", "contains a payload that does not apply to %q", r.Operation)
	}
	if r.Restriction != nil && r.Operation != ModerationRestrictMember || r.Admin != nil && r.Operation != ModerationSetAdmin {
		return invalid("moderation", "contains rights that do not apply to %q", r.Operation)
	}
	if (r.Silent || r.OneSide) && r.Operation != ModerationPinMessage {
		return invalid("moderation.pin_options", "apply only to messages.pin")
	}
	return nil
}

func (r DeniedRights) anyPresent() bool {
	return r.SendMessages != nil || r.SendMedia != nil || r.SendPhotos != nil || r.SendVideos != nil || r.SendRoundVideos != nil || r.SendVoice != nil || r.SendAudios != nil || r.SendDocuments != nil || r.SendStickers != nil || r.SendGIFs != nil || r.SendGames != nil || r.UseInlineBots != nil || r.EmbedLinks != nil || r.SendPolls != nil || r.ChangeInfo != nil || r.InviteUsers != nil || r.PinMessages != nil || r.ManageTopics != nil || r.SendPlain != nil
}

func (r AdminRights) anyPresent() bool {
	return r.ChangeInfo != nil || r.PostMessages != nil || r.EditMessages != nil || r.DeleteMessages != nil || r.BanUsers != nil || r.InviteUsers != nil || r.PinMessages != nil || r.AddAdmins != nil || r.Anonymous != nil || r.ManageCall != nil || r.ManageTopics != nil || r.PostStories != nil || r.EditStories != nil || r.DeleteStories != nil
}

func (r Restriction) Validate() error {
	if r.Permanent && r.UntilUnixMS != 0 {
		return invalid("moderation.restriction", "permanent and deadline are mutually exclusive")
	}
	if r.UntilUnixMS < 0 {
		return invalid("moderation.restriction.until_unix_ms", "must not be negative")
	}
	if !r.Permanent && r.UntilUnixMS == 0 {
		return invalid("moderation.restriction", "must be permanent or have a finite deadline")
	}
	return nil
}

func (s ChatModerationSettings) Validate(operation ModerationOperation) error {
	switch operation {
	case ModerationSetDefaultRights:
		if s.DefaultDenied == nil || !s.DefaultDenied.anyPresent() {
			return invalid("moderation.settings.default_denied", "must contain at least one explicit right")
		}
		if s.SlowModeSeconds != nil || s.ProtectedContent != nil || s.hasReadOnlyFields() {
			return invalid("moderation.settings", "contains fields unrelated to default rights")
		}
	case ModerationSetSlowMode:
		if s.SlowModeSeconds == nil || !knownSlowMode(*s.SlowModeSeconds) {
			return invalid("moderation.settings.slow_mode_seconds", "must be one of 0, 10, 30, 60, 300, 900, or 3600")
		}
		if s.DefaultDenied != nil || s.ProtectedContent != nil || s.hasReadOnlyFields() {
			return invalid("moderation.settings", "contains fields unrelated to slow mode")
		}
	case ModerationSetProtected:
		if s.ProtectedContent == nil {
			return invalid("moderation.settings.protected_content", "is required")
		}
		if s.DefaultDenied != nil || s.SlowModeSeconds != nil || s.hasReadOnlyFields() {
			return invalid("moderation.settings", "contains fields unrelated to protected content")
		}
	}
	return nil
}

func (s ChatModerationSettings) hasReadOnlyFields() bool {
	return s.JoinRequests != nil || s.ParticipantsHidden != nil || s.Signatures != nil || s.JoinToSend != nil || s.Forum != nil || s.LinkedDiscussionID != nil
}

func knownSlowMode(seconds int) bool {
	switch seconds {
	case 0, 10, 30, 60, 300, 900, 3600:
		return true
	default:
		return false
	}
}

func (r AdminRole) Validate() error {
	if r.Rank != nil {
		if len(*r.Rank) > MaxModerationRankBytes {
			return invalid("moderation.admin.rank", "exceeds %d bytes", MaxModerationRankBytes)
		}
		if err := validString("moderation.admin.rank", *r.Rank, false); err != nil {
			return err
		}
	}
	return nil
}

func (r InviteSettings) Validate(operation ModerationOperation) error {
	if len(r.Link) > 2048 || len(r.Link) != 0 && !strings.HasPrefix(r.Link, "https://") {
		return invalid("moderation.invite.link", "must be a bounded HTTPS invite")
	}
	if (operation == ModerationEditInvite || operation == ModerationRevokeInvite) && r.Link == "" {
		return invalid("moderation.invite.link", "is required")
	}
	if operation == ModerationCreateInvite && r.Link != "" {
		return invalid("moderation.invite.link", "must be omitted when creating an invite")
	}
	if operation == ModerationEditInvite && r.Name == nil && r.ExpireUnixMS == nil && r.UsageLimit == nil && r.RequireApproval == nil {
		return invalid("moderation.invite", "edit requires at least one finite setting")
	}
	if operation == ModerationRevokeInvite && (r.Name != nil || r.ExpireUnixMS != nil || r.UsageLimit != nil || r.RequireApproval != nil) {
		return invalid("moderation.invite", "revoke accepts only the invite link")
	}
	if r.Name != nil && len(*r.Name) > MaxModerationInviteNameBytes {
		return invalid("moderation.invite.name", "exceeds %d bytes", MaxModerationInviteNameBytes)
	}
	if r.Name != nil {
		if err := validString("moderation.invite.name", *r.Name, false); err != nil {
			return err
		}
	}
	if r.ExpireUnixMS != nil && *r.ExpireUnixMS < 0 || r.UsageLimit != nil && (*r.UsageLimit < 0 || *r.UsageLimit > 100000) {
		return invalid("moderation.invite", "expiry or usage limit is outside bounds")
	}
	return nil
}

func (r ModerationResult) Validate() error {
	if ModerationPermission(r.Operation) == "" || r.RequestID == "" || r.ChatID == 0 {
		return invalid("moderation.result", "operation, request_id, and chat_id are required")
	}
	switch r.Status {
	case ActionResultSuccess, ActionResultFailure, ActionResultSkipped, ActionResultNoop, ActionResultPartial, ActionResultOutcomeUnknown:
	default:
		return invalid("moderation.result.status", "unknown status %q", r.Status)
	}
	if len(r.Members) > MaxModerationPageItems || len(r.JoinRequests) > MaxModerationPageItems || len(r.Invites) > MaxModerationPageItems || len(r.Items) > MaxModerationBatchItems || len(r.Warnings) > MaxModerationWarnings {
		return invalid("moderation.result", "collection exceeds its bound")
	}
	if r.Status == ActionResultFailure && r.Error == nil {
		return invalid("moderation.result.error", "is required for failure")
	}
	if r.Status == ActionResultOutcomeUnknown && (r.Error == nil || r.Error.Code != ErrorOutcomeUnknown) {
		return invalid("moderation.result.error", "outcome_unknown requires its stable error")
	}
	if r.Error != nil {
		if err := r.Error.Validate(); err != nil {
			return err
		}
	}
	for i := range r.Members {
		if err := r.Members[i].Validate(); err != nil {
			return invalid(fmt.Sprintf("moderation.result.members[%d]", i), "%v", err)
		}
	}
	if r.Member != nil {
		if err := r.Member.Validate(); err != nil {
			return invalid("moderation.result.member", "%v", err)
		}
	}
	for i, item := range r.Items {
		if item.ID <= 0 {
			return invalid(fmt.Sprintf("moderation.result.items[%d].id", i), "must be positive")
		}
		switch item.Status {
		case ActionResultSuccess, ActionResultFailure, ActionResultSkipped, ActionResultNoop, ActionResultPartial, ActionResultOutcomeUnknown:
		default:
			return invalid(fmt.Sprintf("moderation.result.items[%d].status", i), "is unknown")
		}
		if item.Status == ActionResultFailure && item.Error == nil {
			return invalid(fmt.Sprintf("moderation.result.items[%d].error", i), "is required for failure")
		}
		if item.Error != nil {
			if err := item.Error.Validate(); err != nil {
				return err
			}
		}
	}
	for i, item := range r.JoinRequests {
		if item.UserID <= 0 || item.DateUnixMS < 0 {
			return invalid(fmt.Sprintf("moderation.result.join_requests[%d]", i), "contains an invalid user or date")
		}
	}
	for i, item := range r.Invites {
		if len(item.Link) > 2048 || item.Link != "" && !strings.HasPrefix(item.Link, "https://") || len(item.Name) > MaxModerationInviteNameBytes || item.CreatedUnixMS < 0 || item.ExpireUnixMS < 0 || item.UsageLimit < 0 || item.UsageLimit > 100000 || item.UsageCount < 0 {
			return invalid(fmt.Sprintf("moderation.result.invites[%d]", i), "contains an invalid bounded invite summary")
		}
	}
	if r.Topic != nil && r.Topic.TopicID <= 0 {
		return invalid("moderation.result.topic.topic_id", "must be positive")
	}
	for i, warning := range r.Warnings {
		if err := validString(fmt.Sprintf("moderation.result.warnings[%d]", i), warning, true); err != nil {
			return err
		}
		if len(warning) > MaxModerationReasonBytes {
			return invalid("moderation.result.warnings", "warning exceeds %d bytes", MaxModerationReasonBytes)
		}
	}
	if r.Page != nil && (len(r.Page.Cursor) > MaxClientCursorBytes || r.Page.BackendCallsUsed < 0 || r.Page.BackendCallsUsed > MaxClientBackendCalls) {
		return invalid("moderation.result.page", "is outside bounds")
	}
	if r.Support != nil {
		if !knownModerationChatKind(r.Support.ChatKind) && r.Support.ChatKind != "" {
			return invalid("moderation.result.support.chat_kind", "is unknown")
		}
		switch r.Support.Status {
		case "verified", "partial", "recorded-only", "live-gated", "unsupported":
		default:
			return invalid("moderation.result.support.status", "is unknown")
		}
		if r.Support.BackendCalls < 0 || r.Support.BackendCalls > MaxClientBackendCalls {
			return invalid("moderation.result.support.backend_calls", "is outside bounds")
		}
		if len(r.Support.Note) > MaxModerationReasonBytes {
			return invalid("moderation.result.support.note", "exceeds %d bytes", MaxModerationReasonBytes)
		}
	}
	return nil
}

func (m ModerationMember) Validate() error {
	if m.UserID <= 0 {
		return invalid("moderation.member.user_id", "must be positive")
	}
	switch m.Status {
	case MemberCreator, MemberAdmin, MemberRestricted, MemberRegular, MemberLeft, MemberBanned, MemberPending, MemberUnknown:
	default:
		return invalid("moderation.member.status", "is unknown")
	}
	if m.Restriction != nil {
		if err := m.Restriction.Validate(); err != nil {
			return err
		}
	}
	if m.Admin != nil {
		if err := m.Admin.Validate(); err != nil {
			return err
		}
	}
	if len(m.StateToken) > 128 {
		return invalid("moderation.member.state_token", "exceeds 128 bytes")
	}
	return nil
}

func knownModerationChatKind(kind ModerationChatKind) bool {
	switch kind {
	case ModerationChatPrivate, ModerationChatBasicGroup, ModerationChatMegagroup, ModerationChatBroadcast, ModerationChatDiscussion:
		return true
	}
	return false
}

func operationNeedsParticipant(operation ModerationOperation) bool {
	switch operation {
	case ModerationGetMember, ModerationRestrictMember, ModerationUnrestrictMember, ModerationBanMember, ModerationUnbanMember, ModerationKickMember, ModerationGetAdmin, ModerationSetAdmin, ModerationDemoteAdmin, ModerationApproveJoin, ModerationDeclineJoin:
		return true
	}
	return false
}
