package v1

import (
	"fmt"
	"strings"
)

func (r ClientRequest) Validate() error {
	if !knownClientOperation(r.Operation) {
		return invalid("client.operation", "unknown operation %q", r.Operation)
	}
	if r.PeerID == 0 && r.OperationRequiresPeer() {
		return invalid("client.peer_id", "is required for %q", r.Operation)
	}
	if r.Limit < 0 || r.Limit > MaxClientPageItems {
		return invalid("client.limit", "must be between 0 and %d", MaxClientPageItems)
	}
	if r.Before < 0 || r.Before > MaxClientPageItems || r.After < 0 || r.After > MaxClientPageItems {
		return invalid("client.context", "before and after must be between 0 and %d", MaxClientPageItems)
	}
	if r.OffsetID < 0 || r.MessageID < 0 {
		return invalid("client.ids", "offset and message IDs must not be negative")
	}
	if len(r.MessageIDs) > MaxClientBatchItems || len(r.UserIDs) > MaxClientBatchItems || len(r.ChatIDs) > MaxClientBatchItems {
		return invalid("client.ids", "batch exceeds %d items", MaxClientBatchItems)
	}
	if err := validatePositiveIDs("client.message_ids", r.MessageIDs); err != nil {
		return err
	}
	if err := validatePositiveIDs("client.user_ids", r.UserIDs); err != nil {
		return err
	}
	if err := validatePositiveIDs("client.chat_ids", r.ChatIDs); err != nil {
		return err
	}
	for field, value := range map[string]string{
		"client.username": r.Username, "client.cursor": r.Cursor,
		"client.query": r.Query, "client.handle": r.Handle, "client.media_kind": r.MediaKind,
	} {
		if err := validString(field, value, false); err != nil {
			return err
		}
	}
	if len(r.Cursor) > MaxClientCursorBytes {
		return invalid("client.cursor", "exceeds %d bytes", MaxClientCursorBytes)
	}
	if len(r.Query) > MaxClientQueryBytes {
		return invalid("client.query", "exceeds %d bytes", MaxClientQueryBytes)
	}
	if r.Direction != "" && r.Direction != "older" && r.Direction != "newer" {
		return invalid("client.direction", "must be older or newer")
	}
	if r.FolderID < 0 {
		return invalid("client.folder_id", "must not be negative")
	}
	if r.Operation == ClientGetMe && (r.PeerID != 0 || r.Username != "" || r.MessageID != 0 || len(r.MessageIDs) != 0 || r.Limit != 0 || r.Cursor != "") {
		return invalid("client", "get_me does not accept target or pagination fields")
	}
	if r.Operation == ClientGetMessage && (len(r.MessageIDs) != 0 || r.Cursor != "" || r.Query != "" || r.Limit != 0 || r.Before != 0 || r.After != 0) {
		return invalid("client", "get_message does not accept batch or pagination fields")
	}
	switch r.Operation {
	case ClientGetMe:
	case ClientGetMessage, ClientGetMessageAround, ClientGetReply, ClientGetReplies, ClientGetPinnedMessage,
		ClientGetAlbum, ClientGetMessageReactions, ClientGetPoll, ClientGetMessageMediaGroup:
		if r.MessageID <= 0 {
			return invalid("client.message_id", "must be positive for %q", r.Operation)
		}
	case ClientGetHistoryAround:
		if r.MessageID <= 0 {
			return invalid("client.message_id", "must be positive for %q", r.Operation)
		}
	case ClientGetMessages:
		if len(r.MessageIDs) == 0 {
			return invalid("client.message_ids", "must contain at least one ID")
		}
	case ClientResolvePeer, ClientSearchPublicUsername:
		if r.PeerID == 0 && strings.TrimSpace(r.Username) == "" {
			return invalid("client.peer", "peer_id or username is required")
		}
	case ClientGetUser:
		if r.PeerID == 0 && strings.TrimSpace(r.Username) == "" {
			return invalid("client.user", "peer_id or username is required")
		}
	case ClientGetUsers:
		if len(r.UserIDs) == 0 {
			return invalid("client.user_ids", "must contain at least one ID")
		}
	case ClientGetChat, ClientGetDialog, ClientGetParticipant:
		if r.PeerID == 0 {
			return invalid("client.peer_id", "is required")
		}
	case ClientGetChats:
		if len(r.ChatIDs) == 0 {
			return invalid("client.chat_ids", "must contain at least one ID")
		}
	case ClientInspectMedia, ClientGetMediaAttributes:
		if r.Handle == "" {
			return invalid("client.handle", "is required")
		}
	}
	return nil
}

func (r ClientRequest) OperationRequiresPeer() bool {
	switch r.Operation {
	case ClientGetMe, ClientResolvePeer, ClientGetUser, ClientGetUsers, ClientGetChats,
		ClientSearchPublicUsername, ClientListDialogs, ClientListPinnedDialogs,
		ClientGetCommonChats, ClientGetSavedMessagesContext, ClientSearchGlobalMessages,
		ClientInspectMedia, ClientGetMediaAttributes:
		return false
	default:
		return true
	}
}

func validatePositiveIDs(field string, values []int64) error {
	for i, value := range values {
		if value <= 0 {
			return invalid(fmt.Sprintf("%s[%d]", field, i), "must be positive")
		}
	}
	return nil
}

func knownClientOperation(operation ClientReadOperation) bool {
	switch operation {
	case ClientGetMe, ClientResolvePeer, ClientGetUser, ClientGetUsers, ClientGetChat, ClientGetChats,
		ClientSearchPublicUsername, ClientGetMessage, ClientGetMessages, ClientGetMessageAround, ClientGetReply,
		ClientGetReplies, ClientGetPinnedMessage, ClientGetAlbum, ClientGetMessageReactions, ClientGetPoll,
		ClientGetHistory, ClientGetHistoryAround, ClientSearchMessages, ClientSearchMessagesBySender,
		ClientSearchMessagesByMedia, ClientSearchGlobalMessages, ClientListDialogs, ClientGetDialog,
		ClientListPinnedDialogs, ClientGetCommonChats, ClientGetSavedMessagesContext, ClientListParticipants,
		ClientSearchParticipants, ClientGetParticipant, ClientGetRecentSenders, ClientInspectMedia,
		ClientGetMediaAttributes, ClientGetMessageMediaGroup:
		return true
	default:
		return false
	}
}

func (r ClientResult) Validate() error {
	if r.Message != nil {
		if err := validateMessage(*r.Message, "client.message"); err != nil {
			return err
		}
	}
	if r.Me != nil {
		if err := r.Me.Validate(); err != nil {
			return invalid("client.me", "%v", err)
		}
	}
	if r.Peer != nil {
		if err := r.Peer.Validate(); err != nil {
			return invalid("client.peer", "%v", err)
		}
	}
	if len(r.Messages) > MaxClientPageItems || len(r.Users) > MaxClientBatchItems || len(r.Chats) > MaxClientBatchItems || len(r.Dialogs) > MaxClientDialogs || len(r.Participants) > MaxClientParticipants {
		return invalid("client.result", "collection exceeds its bound")
	}
	for i := range r.Messages {
		if err := validateMessage(r.Messages[i], fmt.Sprintf("client.messages[%d]", i)); err != nil {
			return err
		}
	}
	for i := range r.Users {
		if err := r.Users[i].Validate(); err != nil {
			return invalid("client.users", "%v", err)
		}
	}
	for i := range r.Chats {
		if err := r.Chats[i].Validate(); err != nil {
			return invalid("client.chats", "%v", err)
		}
	}
	for i := range r.Dialogs {
		if err := r.Dialogs[i].Peer.Validate(); err != nil {
			return invalid("client.dialogs", "%v", err)
		}
	}
	for i := range r.Participants {
		if err := r.Participants[i].User.Validate(); err != nil {
			return invalid("client.participants", "%v", err)
		}
	}
	if r.Page != nil && (len(r.Page.Cursor) > MaxClientCursorBytes || r.Page.BackendCallsUsed < 0 || r.Page.BackendCallsUsed > MaxClientBackendCalls) {
		return invalid("client.page", "is outside its bounds")
	}
	if r.ReadError != nil {
		if err := validIdentifier("client.read_error.operation", r.ReadError.Operation); err != nil {
			return err
		}
	}
	return nil
}
