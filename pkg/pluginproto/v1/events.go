package v1

type Entity struct {
	Type     string `json:"type"`
	Offset   int    `json:"offset"`
	Length   int    `json:"length"`
	URL      string `json:"url,omitempty"`
	UserID   int64  `json:"user_id,omitempty"`
	Language string `json:"language,omitempty"`
}

type Message struct {
	ID               int64             `json:"id"`
	ChatID           int64             `json:"chat_id"`
	SenderID         int64             `json:"sender_id,omitempty"`
	DateUnixMS       int64             `json:"date_unix_ms"`
	EditDateUnixMS   int64             `json:"edit_date_unix_ms,omitempty"`
	Text             string            `json:"text,omitempty"`
	Outgoing         bool              `json:"outgoing,omitempty"`
	Mentioned        bool              `json:"mentioned,omitempty"`
	ChatKind         string            `json:"chat_kind,omitempty"`
	ViaBot           bool              `json:"via_bot,omitempty"`
	Forwarded        bool              `json:"forwarded,omitempty"`
	ReplyToMessageID int64             `json:"reply_to_message_id,omitempty"`
	Entities         []Entity          `json:"entities,omitempty"`
	Media            *Media            `json:"media,omitempty"`
	Peer             *Peer             `json:"peer,omitempty"`
	Sender           *User             `json:"sender,omitempty"`
	ReplyTo          *MessageReference `json:"reply_to,omitempty"`
	Forward          *ForwardReference `json:"forward,omitempty"`
	GroupedID        int64             `json:"grouped_id,omitempty"`
	Service          *ServiceMetadata  `json:"service,omitempty"`
	Reactions        *ReactionSummary  `json:"reactions,omitempty"`
	Poll             *PollSummary      `json:"poll,omitempty"`
}

type Callback struct {
	ID        string   `json:"id"`
	SenderID  int64    `json:"sender_id"`
	ChatID    int64    `json:"chat_id,omitempty"`
	MessageID int64    `json:"message_id,omitempty"`
	Data      string   `json:"data,omitempty"`
	Message   *Message `json:"message,omitempty"`
}

type InlineQuery struct {
	ID       string `json:"id"`
	SenderID int64  `json:"sender_id"`
	Query    string `json:"query,omitempty"`
	Offset   string `json:"offset,omitempty"`
}

// ControlBotIdentity and ControlActor identify a Bot API source without
// leaking a library object, token, access hash, or raw update payload.
type ControlBotIdentity struct {
	ID       int64  `json:"id"`
	Username string `json:"username,omitempty"`
}

type ControlActor struct {
	ID       int64  `json:"id"`
	Username string `json:"username,omitempty"`
}

// ControlEvent carries the Bot API-only fields that cannot be inferred from a
// user-session update. Exactly one payload is set for the matching control
// update kind.
type ControlEvent struct {
	Bot              ControlBotIdentity `json:"bot"`
	Actor            ControlActor       `json:"actor"`
	ChatID           int64              `json:"chat_id,omitempty"`
	MessageID        int64              `json:"message_id,omitempty"`
	InlineMessageID  string             `json:"inline_message_id,omitempty"`
	ChatInstance     string             `json:"chat_instance,omitempty"`
	QueryID          string             `json:"query_id,omitempty"`
	Query            string             `json:"query,omitempty"`
	Offset           string             `json:"offset,omitempty"`
	ChatType         string             `json:"chat_type,omitempty"`
	CallbackData     string             `json:"callback_data,omitempty"`
	Command          string             `json:"command,omitempty"`
	CommandArguments string             `json:"command_arguments,omitempty"`
}

type Update struct {
	ID             string        `json:"id"`
	Kind           UpdateKind    `json:"kind"`
	OccurredUnixMS int64         `json:"occurred_unix_ms"`
	Message        *Message      `json:"message,omitempty"`
	DeletedIDs     []int64       `json:"deleted_ids,omitempty"`
	Callback       *Callback     `json:"callback,omitempty"`
	InlineQuery    *InlineQuery  `json:"inline_query,omitempty"`
	Control        *ControlEvent `json:"control,omitempty"`
}

type Command struct {
	Name    string   `json:"name"`
	Prefix  string   `json:"prefix"`
	RawArgs string   `json:"raw_args,omitempty"`
	Args    []string `json:"args,omitempty"`
	Message Message  `json:"message"`
}
