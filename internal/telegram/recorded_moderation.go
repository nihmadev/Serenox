package telegram

import (
	"context"
	"reflect"
	"sort"
	"sync"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type recordedModerationState struct {
	mu       sync.Mutex
	members  map[int64]pluginv1.ModerationMember
	settings map[int64]pluginv1.ChatModerationSettings
	invites  map[int64][]pluginv1.InviteSummary
	topics   map[int64]map[int64]pluginv1.ForumTopicState
}

func newRecordedModerationState() *recordedModerationState {
	return &recordedModerationState{members: make(map[int64]pluginv1.ModerationMember), settings: make(map[int64]pluginv1.ChatModerationSettings), invites: make(map[int64][]pluginv1.InviteSummary), topics: make(map[int64]map[int64]pluginv1.ForumTopicState)}
}

func (t *RecordedTransport) recordedTarget(target pluginv1.ModerationParticipantTarget) (int64, error) {
	if target.UserID > 0 {
		return target.UserID, nil
	}
	if target.Username != "" {
		return 0, &pluginv1.Error{Code: pluginv1.ErrorUnsupported, Message: "recorded username resolution fixture is unavailable"}
	}
	if target.Reply == nil {
		return 0, &pluginv1.Error{Code: pluginv1.ErrorModerationTargetUnavailable, Message: "participant target is unavailable"}
	}
	for _, update := range t.updates {
		if update.Message != nil && update.Message.ChatID == target.Reply.PeerID && update.Message.ID == target.Reply.MessageID {
			if update.Message.SenderID == 0 {
				return 0, &pluginv1.Error{Code: pluginv1.ErrorModerationAnonymousSender, Message: "replied message has no visible user sender"}
			}
			if update.Message.SenderID < 0 {
				return 0, &pluginv1.Error{Code: pluginv1.ErrorModerationChannelSender, Message: "replied message was sent as a channel"}
			}
			return update.Message.SenderID, nil
		}
	}
	return 0, &pluginv1.Error{Code: pluginv1.ErrorModerationTargetUnavailable, Message: "replied message is unavailable"}
}

func (t *RecordedTransport) ModerationGetMember(_ context.Context, _ pluginv1.ModerationChatTarget, target pluginv1.ModerationParticipantTarget) (*pluginv1.ModerationMember, error) {
	id, err := t.recordedTarget(target)
	if err != nil {
		return nil, err
	}
	t.moderationState.mu.Lock()
	defer t.moderationState.mu.Unlock()
	member, ok := t.moderationState.members[id]
	if !ok {
		member = pluginv1.ModerationMember{UserID: id, Status: pluginv1.MemberRegular}
	}
	return &member, nil
}

func (t *RecordedTransport) ModerationListMembers(_ context.Context, _ pluginv1.ModerationChatTarget, page pluginv1.ModerationPageRequest) ([]pluginv1.ModerationMember, *pluginv1.PageInfo, error) {
	t.moderationState.mu.Lock()
	defer t.moderationState.mu.Unlock()
	items := make([]pluginv1.ModerationMember, 0, len(t.moderationState.members))
	for _, member := range t.moderationState.members {
		if page.Filter == "administrators" && member.Status != pluginv1.MemberAdmin && member.Status != pluginv1.MemberCreator {
			continue
		}
		if page.Filter == "restricted" && member.Status != pluginv1.MemberRestricted {
			continue
		}
		if page.Filter == "banned" && member.Status != pluginv1.MemberBanned {
			continue
		}
		items = append(items, member)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UserID < items[j].UserID })
	total := len(items)
	if page.Offset > total {
		page.Offset = total
	}
	items = items[page.Offset:]
	if len(items) > page.Limit {
		items = items[:page.Limit]
	}
	return items, &pluginv1.PageInfo{HasMore: page.Offset+len(items) < total, TotalHint: int64(total), BackendCallsUsed: 1}, nil
}

func (t *RecordedTransport) ModerationSetRestriction(_ context.Context, _ pluginv1.ModerationChatTarget, target pluginv1.ModerationParticipantTarget, restriction *pluginv1.Restriction) error {
	id, err := t.recordedTarget(target)
	if err != nil {
		return err
	}
	t.moderationState.mu.Lock()
	defer t.moderationState.mu.Unlock()
	member := t.moderationState.members[id]
	member.UserID = id
	if restriction == nil {
		if member.Status != pluginv1.MemberRestricted && member.Restriction == nil {
			return errModerationNoChange
		}
		member.Status, member.Restriction = pluginv1.MemberRegular, nil
	} else {
		copy := *restriction
		if member.Status == pluginv1.MemberRestricted && reflect.DeepEqual(member.Restriction, &copy) {
			return errModerationNoChange
		}
		member.Status, member.Restriction = pluginv1.MemberRestricted, &copy
	}
	t.moderationState.members[id] = member
	return nil
}

func (t *RecordedTransport) ModerationSetBanned(_ context.Context, _ pluginv1.ModerationChatTarget, target pluginv1.ModerationParticipantTarget, banned, kick bool) error {
	id, err := t.recordedTarget(target)
	if err != nil {
		return err
	}
	t.moderationState.mu.Lock()
	defer t.moderationState.mu.Unlock()
	member := t.moderationState.members[id]
	member.UserID = id
	member.Restriction = nil
	if banned && !kick && member.Status == pluginv1.MemberBanned || kick && member.Status == pluginv1.MemberLeft || !banned && member.Status == pluginv1.MemberLeft {
		return errModerationNoChange
	}
	if banned {
		member.Status = pluginv1.MemberBanned
		if kick {
			member.Status = pluginv1.MemberLeft
		}
	} else {
		member.Status = pluginv1.MemberLeft
	}
	t.moderationState.members[id] = member
	return nil
}

func (t *RecordedTransport) ModerationGetActingRights(_ context.Context, _ pluginv1.ModerationChatTarget) (*pluginv1.ModerationMember, error) {
	truth := true
	return &pluginv1.ModerationMember{UserID: 1, Status: pluginv1.MemberCreator, Admin: &pluginv1.AdminRole{Rights: pluginv1.AdminRights{BanUsers: &truth, DeleteMessages: &truth, AddAdmins: &truth}}}, nil
}

func (t *RecordedTransport) ModerationSetAdmin(_ context.Context, _ pluginv1.ModerationChatTarget, target pluginv1.ModerationParticipantTarget, role *pluginv1.AdminRole) error {
	id, err := t.recordedTarget(target)
	if err != nil {
		return err
	}
	t.moderationState.mu.Lock()
	defer t.moderationState.mu.Unlock()
	member := t.moderationState.members[id]
	member.UserID = id
	if role == nil {
		if member.Status != pluginv1.MemberAdmin || member.Admin == nil {
			return errModerationNoChange
		}
		member.Status, member.Admin = pluginv1.MemberRegular, nil
	} else {
		copy := *role
		if member.Status == pluginv1.MemberAdmin && reflect.DeepEqual(member.Admin, &copy) {
			return errModerationNoChange
		}
		member.Status, member.Admin = pluginv1.MemberAdmin, &copy
	}
	t.moderationState.members[id] = member
	return nil
}

func (t *RecordedTransport) ModerationDeleteMessages(_ context.Context, _ pluginv1.ModerationChatTarget, messages []pluginv1.ModerationMessageTarget) ([]pluginv1.ModerationItemResult, error) {
	items := make([]pluginv1.ModerationItemResult, len(messages))
	for i, message := range messages {
		items[i] = pluginv1.ModerationItemResult{ID: message.MessageID, Status: pluginv1.ActionResultSuccess}
	}
	return items, nil
}

func (*RecordedTransport) ModerationPin(context.Context, pluginv1.ModerationChatTarget, *pluginv1.ModerationMessageTarget, bool, bool, bool) error {
	return nil
}

func (t *RecordedTransport) ModerationGetSettings(_ context.Context, chat pluginv1.ModerationChatTarget) (*pluginv1.ChatModerationSettings, error) {
	t.moderationState.mu.Lock()
	defer t.moderationState.mu.Unlock()
	value := t.moderationState.settings[chat.ChatID]
	return &value, nil
}

func (t *RecordedTransport) ModerationSetSettings(_ context.Context, chat pluginv1.ModerationChatTarget, operation pluginv1.ModerationOperation, settings pluginv1.ChatModerationSettings) error {
	t.moderationState.mu.Lock()
	defer t.moderationState.mu.Unlock()
	current := t.moderationState.settings[chat.ChatID]
	switch operation {
	case pluginv1.ModerationSetDefaultRights:
		current.DefaultDenied = settings.DefaultDenied
	case pluginv1.ModerationSetSlowMode:
		current.SlowModeSeconds = settings.SlowModeSeconds
	case pluginv1.ModerationSetProtected:
		current.ProtectedContent = settings.ProtectedContent
	}
	if reflect.DeepEqual(t.moderationState.settings[chat.ChatID], current) {
		return errModerationNoChange
	}
	t.moderationState.settings[chat.ChatID] = current
	return nil
}

func (*RecordedTransport) ModerationListJoinRequests(context.Context, pluginv1.ModerationChatTarget, pluginv1.ModerationPageRequest) ([]pluginv1.JoinRequestSummary, *pluginv1.PageInfo, error) {
	return []pluginv1.JoinRequestSummary{}, &pluginv1.PageInfo{BackendCallsUsed: 1}, nil
}
func (t *RecordedTransport) ModerationDecideJoinRequest(_ context.Context, _ pluginv1.ModerationChatTarget, target pluginv1.ModerationParticipantTarget, _ bool) error {
	_, err := t.recordedTarget(target)
	return err
}

func (t *RecordedTransport) ModerationCreateInvite(_ context.Context, chat pluginv1.ModerationChatTarget, settings pluginv1.InviteSettings) (*pluginv1.InviteSummary, error) {
	item := pluginv1.InviteSummary{}
	if settings.Name != nil {
		item.Name = *settings.Name
	}
	if settings.ExpireUnixMS != nil {
		item.ExpireUnixMS = *settings.ExpireUnixMS
	}
	if settings.UsageLimit != nil {
		item.UsageLimit = *settings.UsageLimit
	}
	if settings.RequireApproval != nil {
		item.RequireApproval = *settings.RequireApproval
	}
	t.moderationState.mu.Lock()
	t.moderationState.invites[chat.ChatID] = append(t.moderationState.invites[chat.ChatID], item)
	t.moderationState.mu.Unlock()
	return &item, nil
}
func (t *RecordedTransport) ModerationListInvites(_ context.Context, chat pluginv1.ModerationChatTarget, _ pluginv1.ModerationPageRequest) ([]pluginv1.InviteSummary, *pluginv1.PageInfo, error) {
	t.moderationState.mu.Lock()
	defer t.moderationState.mu.Unlock()
	return append([]pluginv1.InviteSummary(nil), t.moderationState.invites[chat.ChatID]...), &pluginv1.PageInfo{BackendCallsUsed: 1}, nil
}
func (t *RecordedTransport) ModerationEditInvite(_ context.Context, _ pluginv1.ModerationChatTarget, settings pluginv1.InviteSettings) (*pluginv1.InviteSummary, error) {
	item := pluginv1.InviteSummary{}
	if settings.Name != nil {
		item.Name = *settings.Name
	}
	return &item, nil
}
func (*RecordedTransport) ModerationRevokeInvite(context.Context, pluginv1.ModerationChatTarget, pluginv1.InviteSettings) error {
	return nil
}

func (t *RecordedTransport) ModerationGetTopic(_ context.Context, chat pluginv1.ModerationChatTarget, topic pluginv1.ForumTopicTarget) (*pluginv1.ForumTopicState, error) {
	t.moderationState.mu.Lock()
	defer t.moderationState.mu.Unlock()
	value := t.moderationState.topics[chat.ChatID][topic.TopicID]
	value.TopicID = topic.TopicID
	return &value, nil
}
func (t *RecordedTransport) ModerationSetTopic(_ context.Context, chat pluginv1.ModerationChatTarget, operation pluginv1.ModerationOperation, topic pluginv1.ForumTopicTarget) error {
	t.moderationState.mu.Lock()
	defer t.moderationState.mu.Unlock()
	if t.moderationState.topics[chat.ChatID] == nil {
		t.moderationState.topics[chat.ChatID] = make(map[int64]pluginv1.ForumTopicState)
	}
	value := t.moderationState.topics[chat.ChatID][topic.TopicID]
	value.TopicID = topic.TopicID
	switch operation {
	case pluginv1.ModerationCloseTopic:
		if value.Closed {
			return errModerationNoChange
		}
		value.Closed = true
	case pluginv1.ModerationReopenTopic:
		if !value.Closed {
			return errModerationNoChange
		}
		value.Closed = false
	case pluginv1.ModerationHideGeneralTopic:
		if value.Hidden {
			return errModerationNoChange
		}
		value.Hidden = true
	case pluginv1.ModerationUnhideGeneralTopic:
		if !value.Hidden {
			return errModerationNoChange
		}
		value.Hidden = false
	case pluginv1.ModerationDeleteTopic:
		if _, exists := t.moderationState.topics[chat.ChatID][topic.TopicID]; !exists {
			return errModerationNoChange
		}
		delete(t.moderationState.topics[chat.ChatID], topic.TopicID)
		return nil
	}
	t.moderationState.topics[chat.ChatID][topic.TopicID] = value
	return nil
}

var _ ModerationBackend = (*RecordedTransport)(nil)
var _ ModerationClient = (*RecordedTransport)(nil)

func (*RecordedTransport) ModerationSupportStatus() string { return "recorded-only" }
