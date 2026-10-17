package v1

// ModerationChatKind is explicit because Telegram uses different methods and
// rights for legacy basic groups, megagroups, and broadcast channels.
type ModerationChatKind string

const (
	ModerationChatPrivate    ModerationChatKind = "private"
	ModerationChatBasicGroup ModerationChatKind = "basic_group"
	ModerationChatMegagroup  ModerationChatKind = "megagroup"
	ModerationChatBroadcast  ModerationChatKind = "broadcast"
	ModerationChatDiscussion ModerationChatKind = "linked_discussion"
)

type MemberStatus string

const (
	MemberCreator    MemberStatus = "creator"
	MemberAdmin      MemberStatus = "administrator"
	MemberRestricted MemberStatus = "restricted"
	MemberRegular    MemberStatus = "member"
	MemberLeft       MemberStatus = "left"
	MemberBanned     MemberStatus = "banned"
	MemberPending    MemberStatus = "pending_join_request"
	MemberUnknown    MemberStatus = "unknown"
)

type ModerationChatTarget struct {
	ChatID       int64              `json:"chat_id"`
	ExpectedKind ModerationChatKind `json:"expected_kind,omitempty"`
}

// ModerationParticipantTarget contains exactly one selector. Reply targets are
// resolved by the core from the bounded message DTO; access hashes never cross
// this boundary.
type ModerationParticipantTarget struct {
	UserID   int64             `json:"user_id,omitempty"`
	Username string            `json:"username,omitempty"`
	Reply    *MessageReference `json:"reply,omitempty"`
}

type ModerationMessageTarget struct {
	ChatID    int64 `json:"chat_id"`
	MessageID int64 `json:"message_id"`
	TopicID   int64 `json:"topic_id,omitempty"`
}

// DeniedRights uses presence-aware, non-inverted public semantics. A present
// true denies the capability; present false explicitly permits it. Omitted
// fields leave the current/default policy unchanged.
type DeniedRights struct {
	SendMessages    *bool `json:"send_messages,omitempty"`
	SendMedia       *bool `json:"send_media,omitempty"`
	SendPhotos      *bool `json:"send_photos,omitempty"`
	SendVideos      *bool `json:"send_videos,omitempty"`
	SendRoundVideos *bool `json:"send_round_videos,omitempty"`
	SendVoice       *bool `json:"send_voice,omitempty"`
	SendAudios      *bool `json:"send_audios,omitempty"`
	SendDocuments   *bool `json:"send_documents,omitempty"`
	SendStickers    *bool `json:"send_stickers,omitempty"`
	SendGIFs        *bool `json:"send_gifs,omitempty"`
	SendGames       *bool `json:"send_games,omitempty"`
	UseInlineBots   *bool `json:"use_inline_bots,omitempty"`
	EmbedLinks      *bool `json:"embed_links,omitempty"`
	SendPolls       *bool `json:"send_polls,omitempty"`
	ChangeInfo      *bool `json:"change_info,omitempty"`
	InviteUsers     *bool `json:"invite_users,omitempty"`
	PinMessages     *bool `json:"pin_messages,omitempty"`
	ManageTopics    *bool `json:"manage_topics,omitempty"`
	SendPlain       *bool `json:"send_plain,omitempty"`
}

type Restriction struct {
	Denied      DeniedRights `json:"denied"`
	UntilUnixMS int64        `json:"until_unix_ms,omitempty"`
	Permanent   bool         `json:"permanent,omitempty"`
}

// AdminRights uses direct semantics: present true grants and present false
// removes the right. Ownership transfer is intentionally absent.
type AdminRights struct {
	ChangeInfo     *bool `json:"change_info,omitempty"`
	PostMessages   *bool `json:"post_messages,omitempty"`
	EditMessages   *bool `json:"edit_messages,omitempty"`
	DeleteMessages *bool `json:"delete_messages,omitempty"`
	BanUsers       *bool `json:"ban_users,omitempty"`
	InviteUsers    *bool `json:"invite_users,omitempty"`
	PinMessages    *bool `json:"pin_messages,omitempty"`
	AddAdmins      *bool `json:"add_admins,omitempty"`
	Anonymous      *bool `json:"anonymous,omitempty"`
	ManageCall     *bool `json:"manage_call,omitempty"`
	ManageTopics   *bool `json:"manage_topics,omitempty"`
	PostStories    *bool `json:"post_stories,omitempty"`
	EditStories    *bool `json:"edit_stories,omitempty"`
	DeleteStories  *bool `json:"delete_stories,omitempty"`
}

type AdminRole struct {
	Rights AdminRights `json:"rights"`
	Rank   *string     `json:"rank,omitempty"`
}

type ModerationPageRequest struct {
	Filter string `json:"filter,omitempty"`
	Query  string `json:"query,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Offset int    `json:"-"`
	Page   int    `json:"-"`
}

type ChatModerationSettings struct {
	DefaultDenied      *DeniedRights `json:"default_denied,omitempty"`
	SlowModeSeconds    *int          `json:"slow_mode_seconds,omitempty"`
	ProtectedContent   *bool         `json:"protected_content,omitempty"`
	JoinRequests       *bool         `json:"join_requests,omitempty"`
	ParticipantsHidden *bool         `json:"participants_hidden,omitempty"`
	Signatures         *bool         `json:"signatures,omitempty"`
	JoinToSend         *bool         `json:"join_to_send,omitempty"`
	Forum              *bool         `json:"forum,omitempty"`
	LinkedDiscussionID *int64        `json:"linked_discussion_id,omitempty"`
}

type InviteSettings struct {
	Link            string  `json:"link,omitempty"`
	Name            *string `json:"name,omitempty"`
	ExpireUnixMS    *int64  `json:"expire_unix_ms,omitempty"`
	UsageLimit      *int    `json:"usage_limit,omitempty"`
	RequireApproval *bool   `json:"require_approval,omitempty"`
}

type ForumTopicTarget struct {
	TopicID int64 `json:"topic_id"`
}
