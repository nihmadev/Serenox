package v1

type ModerationSupport struct {
	ChatKind     ModerationChatKind `json:"chat_kind"`
	Status       string             `json:"status"`
	BackendCalls int                `json:"backend_calls,omitempty"`
	Note         string             `json:"note,omitempty"`
}

type ModerationMember struct {
	UserID      int64        `json:"user_id"`
	Status      MemberStatus `json:"status"`
	Bot         bool         `json:"bot,omitempty"`
	Deleted     bool         `json:"deleted,omitempty"`
	Restriction *Restriction `json:"restriction,omitempty"`
	Admin       *AdminRole   `json:"admin,omitempty"`
	StateToken  string       `json:"state_token,omitempty"`
}

type JoinRequestSummary struct {
	UserID     int64 `json:"user_id"`
	DateUnixMS int64 `json:"date_unix_ms"`
}

type InviteSummary struct {
	Link            string `json:"link,omitempty"`
	Name            string `json:"name,omitempty"`
	CreatedUnixMS   int64  `json:"created_unix_ms,omitempty"`
	ExpireUnixMS    int64  `json:"expire_unix_ms,omitempty"`
	UsageLimit      int    `json:"usage_limit,omitempty"`
	UsageCount      int    `json:"usage_count,omitempty"`
	RequireApproval bool   `json:"require_approval,omitempty"`
	Revoked         bool   `json:"revoked,omitempty"`
}

type ForumTopicState struct {
	TopicID int64 `json:"topic_id"`
	Closed  bool  `json:"closed,omitempty"`
	Hidden  bool  `json:"hidden,omitempty"`
}

type ModerationItemResult struct {
	ID     int64             `json:"id"`
	Status ActionResultState `json:"status"`
	Error  *Error            `json:"error,omitempty"`
}

type ModerationResult struct {
	Operation        ModerationOperation     `json:"operation"`
	RequestID        string                  `json:"request_id"`
	Status           ActionResultState       `json:"status"`
	ChatID           int64                   `json:"chat_id"`
	TargetUserID     int64                   `json:"target_user_id,omitempty"`
	Member           *ModerationMember       `json:"member,omitempty"`
	Members          []ModerationMember      `json:"members,omitempty"`
	Settings         *ChatModerationSettings `json:"settings,omitempty"`
	JoinRequests     []JoinRequestSummary    `json:"join_requests,omitempty"`
	Invites          []InviteSummary         `json:"invites,omitempty"`
	Topic            *ForumTopicState        `json:"topic,omitempty"`
	Page             *PageInfo               `json:"page,omitempty"`
	Items            []ModerationItemResult  `json:"items,omitempty"`
	Warnings         []string                `json:"warnings,omitempty"`
	Support          *ModerationSupport      `json:"support,omitempty"`
	Error            *Error                  `json:"error,omitempty"`
	Acknowledged     bool                    `json:"acknowledged,omitempty"`
	IdempotentReplay bool                    `json:"idempotent_replay,omitempty"`
}
