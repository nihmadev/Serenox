package v1

type ClientRequest struct {
	Operation  ClientReadOperation `json:"operation"`
	PeerID     int64               `json:"peer_id,omitempty"`
	Username   string              `json:"username,omitempty"`
	MessageID  int64               `json:"message_id,omitempty"`
	MessageIDs []int64             `json:"message_ids,omitempty"`
	UserIDs    []int64             `json:"user_ids,omitempty"`
	ChatIDs    []int64             `json:"chat_ids,omitempty"`
	SenderID   int64               `json:"sender_id,omitempty"`
	Limit      int                 `json:"limit,omitempty"`
	OffsetID   int64               `json:"offset_id,omitempty"`
	Before     int                 `json:"before,omitempty"`
	After      int                 `json:"after,omitempty"`
	Cursor     string              `json:"cursor,omitempty"`
	Query      string              `json:"query,omitempty"`
	Handle     string              `json:"handle,omitempty"`
	MediaKind  string              `json:"media_kind,omitempty"`
	FolderID   int                 `json:"folder_id,omitempty"`
	Direction  string              `json:"direction,omitempty"`
}

type ClientReadOperation string

const (
	ClientGetMe                   ClientReadOperation = "get_me"
	ClientGetMessage              ClientReadOperation = "get_message"
	ClientResolvePeer             ClientReadOperation = "resolve_peer"
	ClientGetUser                 ClientReadOperation = "get_user"
	ClientGetUsers                ClientReadOperation = "get_users"
	ClientGetChat                 ClientReadOperation = "get_chat"
	ClientGetChats                ClientReadOperation = "get_chats"
	ClientSearchPublicUsername    ClientReadOperation = "search_public_username"
	ClientGetMessages             ClientReadOperation = "get_messages"
	ClientGetMessageAround        ClientReadOperation = "get_message_around"
	ClientGetReply                ClientReadOperation = "get_reply"
	ClientGetReplies              ClientReadOperation = "get_replies"
	ClientGetPinnedMessage        ClientReadOperation = "get_pinned_message"
	ClientGetAlbum                ClientReadOperation = "get_album"
	ClientGetMessageReactions     ClientReadOperation = "get_message_reactions"
	ClientGetPoll                 ClientReadOperation = "get_poll"
	ClientGetHistory              ClientReadOperation = "get_history"
	ClientGetHistoryAround        ClientReadOperation = "get_history_around"
	ClientSearchMessages          ClientReadOperation = "search_messages"
	ClientSearchMessagesBySender  ClientReadOperation = "search_messages_by_sender"
	ClientSearchMessagesByMedia   ClientReadOperation = "search_messages_by_media"
	ClientSearchGlobalMessages    ClientReadOperation = "search_global_messages"
	ClientListDialogs             ClientReadOperation = "list_dialogs"
	ClientGetDialog               ClientReadOperation = "get_dialog"
	ClientListPinnedDialogs       ClientReadOperation = "list_pinned_dialogs"
	ClientGetCommonChats          ClientReadOperation = "get_common_chats"
	ClientGetSavedMessagesContext ClientReadOperation = "get_saved_messages_context"
	ClientListParticipants        ClientReadOperation = "list_participants"
	ClientSearchParticipants      ClientReadOperation = "search_participants"
	ClientGetParticipant          ClientReadOperation = "get_participant"
	ClientGetRecentSenders        ClientReadOperation = "get_recent_senders"
	ClientInspectMedia            ClientReadOperation = "inspect_media"
	ClientGetMediaAttributes      ClientReadOperation = "get_media_attributes"
	ClientGetMessageMediaGroup    ClientReadOperation = "get_message_media_group"
)

// Peer is a transport-neutral peer descriptor. Access hashes and Telegram
// library input peers are intentionally not part of this DTO.
type Peer struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Username  string `json:"username,omitempty"`
	Title     string `json:"title,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

func (p Peer) Validate() error {
	if p.ID == 0 {
		return invalid("peer.id", "must be non-zero")
	}
	if err := validIdentifier("peer.kind", p.Kind); err != nil {
		return err
	}
	for field, value := range map[string]string{"peer.username": p.Username, "peer.title": p.Title} {
		if err := validString(field, value, false); err != nil {
			return err
		}
	}
	if err := validateProfileName("peer.first_name", p.FirstName, false); err != nil {
		return err
	}
	if err := validateProfileName("peer.last_name", p.LastName, false); err != nil {
		return err
	}
	return nil
}

type ClientResult struct {
	Message      *Message      `json:"message,omitempty"`
	Peer         *Peer         `json:"peer,omitempty"`
	Me           *Peer         `json:"me,omitempty"`
	Messages     []Message     `json:"messages,omitempty"`
	Users        []User        `json:"users,omitempty"`
	Chats        []Chat        `json:"chats,omitempty"`
	Dialogs      []Dialog      `json:"dialogs,omitempty"`
	Participants []Participant `json:"participants,omitempty"`
	Media        *Media        `json:"media,omitempty"`
	Page         *PageInfo     `json:"page,omitempty"`
	ReadError    *ReadError    `json:"read_error,omitempty"`
	NextOffsetID int64         `json:"next_offset_id,omitempty"`
}
