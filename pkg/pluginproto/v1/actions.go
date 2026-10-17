package v1

type InlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
	SwitchQuery  string `json:"switch_query,omitempty"`
}

type InlineResult struct {
	ID          string           `json:"id"`
	Type        string           `json:"type"`
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description,omitempty"`
	Text        string           `json:"text,omitempty"`
	Buttons     [][]InlineButton `json:"buttons,omitempty"`
}

type ClientOperation struct {
	Name      string `json:"name"`
	PeerID    int64  `json:"peer_id,omitempty"`
	MessageID int64  `json:"message_id,omitempty"`
	UserID    int64  `json:"user_id,omitempty"`
	Value     string `json:"value,omitempty"`
}

type Action struct {
	ID              string          `json:"id"`
	Kind            ActionKind      `json:"kind"`
	ResponseChannel ResponseChannel `json:"response_channel,omitempty"`
	// IdempotencyKey is optional. When present it is scoped by the host to the
	// plugin, account, and operation for the configured retention window.
	IdempotencyKey   string             `json:"idempotency_key,omitempty"`
	ChatID           int64              `json:"chat_id,omitempty"`
	MessageID        int64              `json:"message_id,omitempty"`
	ReplyToMessageID int64              `json:"reply_to_message_id,omitempty"`
	Text             string             `json:"text,omitempty"`
	ParseMode        string             `json:"parse_mode,omitempty"`
	CallbackID       string             `json:"callback_id,omitempty"`
	InlineQueryID    string             `json:"inline_query_id,omitempty"`
	InlineMessageID  string             `json:"inline_message_id,omitempty"`
	CallbackText     string             `json:"callback_text,omitempty"`
	ShowAlert        bool               `json:"show_alert,omitempty"`
	CacheTimeSeconds int                `json:"cache_time_seconds,omitempty"`
	IsPersonal       bool               `json:"is_personal,omitempty"`
	NextOffset       string             `json:"next_offset,omitempty"`
	Buttons          [][]InlineButton   `json:"buttons,omitempty"`
	InlineResults    []InlineResult     `json:"inline_results,omitempty"`
	ClientOperation  *ClientOperation   `json:"client_operation,omitempty"`
	StorageMutation  *StorageMutation   `json:"storage_mutation,omitempty"`
	Media            *MediaAttachment   `json:"media,omitempty"`
	Profile          *ProfileNameAction `json:"profile,omitempty"`
}

type ActionResult struct {
	ActionID  string            `json:"action_id"`
	Operation string            `json:"operation"`
	Status    ActionResultState `json:"status"`
	// Success is retained for v1 wire compatibility. New producers should use
	// Status; the validator requires the two fields to agree when both exist.
	Success          bool              `json:"success"`
	OutcomeUnknown   bool              `json:"outcome_unknown,omitempty"`
	Retryable        bool              `json:"retryable,omitempty"`
	FloodWaitSeconds int64             `json:"flood_wait_seconds,omitempty"`
	Message          *Message          `json:"message,omitempty"`
	Peer             *Peer             `json:"peer,omitempty"`
	MessageID        int64             `json:"message_id,omitempty"`
	DateUnixMS       int64             `json:"date_unix_ms,omitempty"`
	MediaHandle      string            `json:"media_handle,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	Error            *Error            `json:"error,omitempty"`
	Profile          *ProfileResult    `json:"profile,omitempty"`
}

type ActionResultState string

const (
	ActionResultSuccess        ActionResultState = "success"
	ActionResultFailure        ActionResultState = "failure"
	ActionResultSkipped        ActionResultState = "skipped"
	ActionResultNoop           ActionResultState = "noop"
	ActionResultPartial        ActionResultState = "partial"
	ActionResultOutcomeUnknown ActionResultState = "outcome_unknown"
)

type ActionBatchRequest struct {
	Actions     []Action `json:"actions"`
	StopOnError bool     `json:"stop_on_error,omitempty"`
}

type ActionBatchResponse struct {
	Results []ActionResult `json:"results"`
	Stopped bool           `json:"stopped,omitempty"`
	Partial bool           `json:"partial,omitempty"`
}

func (a Action) OperationName() string {
	if a.Kind == ActionClient && a.ClientOperation != nil && a.ClientOperation.Name != "" {
		return a.ClientOperation.Name
	}
	return string(a.Kind)
}

func (a Action) IsMutating() bool {
	return a.Kind != ActionClient || a.StorageMutation != nil
}
