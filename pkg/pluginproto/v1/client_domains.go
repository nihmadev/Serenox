package v1

type MessageReference struct {
	PeerID    int64 `json:"peer_id"`
	MessageID int64 `json:"message_id"`
}

type ForwardReference struct {
	FromPeer   *Peer  `json:"from_peer,omitempty"`
	DateUnixMS int64  `json:"date_unix_ms,omitempty"`
	PostAuthor string `json:"post_author,omitempty"`
}

type Reaction struct {
	Key    string `json:"key"`
	Count  int    `json:"count"`
	Chosen bool   `json:"chosen,omitempty"`
}

type ReactionSummary struct {
	Reactions []Reaction `json:"reactions,omitempty"`
	Count     int        `json:"count,omitempty"`
	CanSee    bool       `json:"can_see,omitempty"`
}

type PollOption struct {
	Text   string `json:"text"`
	Voters int    `json:"voters,omitempty"`
	Chosen bool   `json:"chosen,omitempty"`
}

type PollSummary struct {
	Question    string       `json:"question"`
	Kind        string       `json:"kind"`
	Closed      bool         `json:"closed,omitempty"`
	TotalVoters int          `json:"total_voters,omitempty"`
	Options     []PollOption `json:"options,omitempty"`
}

type ServiceMetadata struct {
	Kind  string `json:"kind"`
	Actor *Peer  `json:"actor,omitempty"`
	Text  string `json:"text,omitempty"`
}

type Dialog struct {
	Peer           Peer  `json:"peer"`
	TopMessageID   int64 `json:"top_message_id,omitempty"`
	UnreadCount    int   `json:"unread_count,omitempty"`
	UnreadMentions int   `json:"unread_mentions,omitempty"`
	FolderID       int   `json:"folder_id,omitempty"`
	Pinned         bool  `json:"pinned,omitempty"`
}

type Participant struct {
	User       User   `json:"user"`
	Status     string `json:"status"`
	Admin      bool   `json:"admin,omitempty"`
	Owner      bool   `json:"owner,omitempty"`
	Restricted bool   `json:"restricted,omitempty"`
	Banned     bool   `json:"banned,omitempty"`
}

type PageInfo struct {
	Cursor           string `json:"cursor,omitempty"`
	HasMore          bool   `json:"has_more"`
	TotalHint        int64  `json:"total_hint,omitempty"`
	SnapshotUnixMS   int64  `json:"snapshot_unix_ms,omitempty"`
	BackendCallsUsed int    `json:"backend_calls_used,omitempty"`
}

type ReadError struct {
	Operation        string    `json:"operation"`
	Code             ErrorCode `json:"code"`
	Retryable        bool      `json:"retryable,omitempty"`
	FloodWaitSeconds int64     `json:"flood_wait_seconds,omitempty"`
	State            string    `json:"state,omitempty"`
}
