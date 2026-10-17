package v1

// ClientCapability returns the least-privilege capability required for a
// read. The legacy client_operations capability remains accepted for v1
// compatibility, but new manifests should use the domain capability.
func ClientCapability(operation ClientReadOperation) Capability {
	switch operation {
	case ClientGetMe:
		return CapabilityClientIdentity
	case ClientResolvePeer, ClientSearchPublicUsername:
		return CapabilityClientPeer
	case ClientGetUser, ClientGetUsers:
		return CapabilityClientUser
	case ClientGetChat, ClientGetChats:
		return CapabilityClientChat
	case ClientGetMessage, ClientGetMessages, ClientGetMessageAround, ClientGetReply, ClientGetReplies,
		ClientGetPinnedMessage, ClientGetAlbum, ClientGetMessageReactions, ClientGetPoll, ClientGetMessageMediaGroup:
		return CapabilityClientMessage
	case ClientGetHistory, ClientGetHistoryAround:
		return CapabilityClientHistory
	case ClientSearchMessages, ClientSearchMessagesBySender, ClientSearchMessagesByMedia, ClientSearchGlobalMessages:
		return CapabilityClientSearch
	case ClientListDialogs, ClientGetDialog, ClientListPinnedDialogs, ClientGetCommonChats, ClientGetSavedMessagesContext:
		return CapabilityClientDialogs
	case ClientListParticipants, ClientSearchParticipants, ClientGetParticipant, ClientGetRecentSenders:
		return CapabilityClientParticipants
	case ClientInspectMedia, ClientGetMediaAttributes:
		return CapabilityClientMediaMetadata
	default:
		return ""
	}
}

func ClientPermission(operation ClientReadOperation) string {
	capability := ClientCapability(operation)
	if capability == "" {
		return ""
	}
	return string(capability)
}
