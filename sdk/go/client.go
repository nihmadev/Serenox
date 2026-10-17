package plugin

import (
	"context"
	"fmt"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

// Client is the bounded read-only Telegram client. Its fields are typed domain
// groups; there is intentionally no raw operation or transport escape hatch.
type Client struct {
	rpc           *serverRPC
	Identity      *IdentityClient
	Peers         *PeerClient
	Users         *UserClient
	Chats         *ChatClient
	Messages      *MessageClient
	History       *HistoryClient
	Search        *SearchClient
	Dialogs       *DialogClient
	Participants  *ParticipantClient
	MediaMetadata *MediaMetadataClient
}

type IdentityClient struct{ client *Client }
type PeerClient struct{ client *Client }
type UserClient struct{ client *Client }
type ChatClient struct{ client *Client }
type MessageClient struct{ client *Client }
type HistoryClient struct{ client *Client }
type SearchClient struct{ client *Client }
type DialogClient struct{ client *Client }
type ParticipantClient struct{ client *Client }
type MediaMetadataClient struct{ client *Client }

type MessagePage struct {
	Messages []protocol.Message
	Page     protocol.PageInfo
}
type DialogPage struct {
	Dialogs []protocol.Dialog
	Page    protocol.PageInfo
}
type ParticipantPage struct {
	Participants []protocol.Participant
	Page         protocol.PageInfo
}

func newClient(rpc *serverRPC) *Client {
	client := &Client{}
	client.Identity = &IdentityClient{client: client}
	client.Peers = &PeerClient{client: client}
	client.Users = &UserClient{client: client}
	client.Chats = &ChatClient{client: client}
	client.Messages = &MessageClient{client: client}
	client.History = &HistoryClient{client: client}
	client.Search = &SearchClient{client: client}
	client.Dialogs = &DialogClient{client: client}
	client.Participants = &ParticipantClient{client: client}
	client.MediaMetadata = &MediaMetadataClient{client: client}
	client.rpc = rpc
	return client
}

// rpc is kept private so callers can only use explicit operations.
func (c *Client) call(ctx context.Context, request protocol.ClientRequest) (protocol.ClientResult, error) {
	if c == nil || c.rpc == nil {
		return protocol.ClientResult{}, fmt.Errorf("client is unavailable")
	}
	if err := request.Validate(); err != nil {
		return protocol.ClientResult{}, err
	}
	var result protocol.ClientResult
	if err := c.rpc.Call(ctx, protocol.MethodHostClient, request, &result); err != nil {
		return protocol.ClientResult{}, err
	}
	if err := result.Validate(); err != nil {
		return protocol.ClientResult{}, err
	}
	return result, nil
}

func (c *IdentityClient) GetMe(ctx context.Context) (*protocol.Peer, error) {
	result, err := c.client.call(ctx, protocol.ClientRequest{Operation: protocol.ClientGetMe})
	return result.Me, err
}
func (c *PeerClient) Resolve(ctx context.Context, id int64) (*protocol.Peer, error) {
	result, err := c.client.call(ctx, protocol.ClientRequest{Operation: protocol.ClientResolvePeer, PeerID: id})
	return result.Peer, err
}
func (c *PeerClient) ResolveUsername(ctx context.Context, username string) (*protocol.Peer, error) {
	result, err := c.client.call(ctx, protocol.ClientRequest{Operation: protocol.ClientResolvePeer, Username: username})
	return result.Peer, err
}
func (c *UserClient) Get(ctx context.Context, id int64) (*protocol.User, error) {
	result, err := c.client.call(ctx, protocol.ClientRequest{Operation: protocol.ClientGetUser, PeerID: id})
	return firstUser(result.Users), err
}
func (c *UserClient) GetMany(ctx context.Context, ids []int64) ([]protocol.User, error) {
	result, err := c.client.call(ctx, protocol.ClientRequest{Operation: protocol.ClientGetUsers, UserIDs: ids})
	return result.Users, err
}
func (c *ChatClient) Get(ctx context.Context, id int64) (*protocol.Chat, error) {
	result, err := c.client.call(ctx, protocol.ClientRequest{Operation: protocol.ClientGetChat, PeerID: id})
	return firstChat(result.Chats), err
}
func (c *ChatClient) GetMany(ctx context.Context, ids []int64) ([]protocol.Chat, error) {
	result, err := c.client.call(ctx, protocol.ClientRequest{Operation: protocol.ClientGetChats, ChatIDs: ids})
	return result.Chats, err
}
func (c *MessageClient) Get(ctx context.Context, peerID, messageID int64) (*protocol.Message, error) {
	result, err := c.client.call(ctx, protocol.ClientRequest{Operation: protocol.ClientGetMessage, PeerID: peerID, MessageID: messageID})
	return result.Message, err
}
func (c *MessageClient) GetMany(ctx context.Context, peerID int64, ids []int64) ([]protocol.Message, error) {
	result, err := c.client.call(ctx, protocol.ClientRequest{Operation: protocol.ClientGetMessages, PeerID: peerID, MessageIDs: ids})
	return result.Messages, err
}

func (c *HistoryClient) List(ctx context.Context, peerID int64, limit int, cursor string) (MessagePage, error) {
	result, err := c.client.call(ctx, protocol.ClientRequest{Operation: protocol.ClientGetHistory, PeerID: peerID, Limit: limit, Cursor: cursor, Direction: "older"})
	return MessagePage{Messages: result.Messages, Page: pageValue(result.Page)}, err
}
func (c *SearchClient) Messages(ctx context.Context, peerID int64, query string, limit int, cursor string) (MessagePage, error) {
	result, err := c.client.call(ctx, protocol.ClientRequest{Operation: protocol.ClientSearchMessages, PeerID: peerID, Query: query, Limit: limit, Cursor: cursor})
	return MessagePage{Messages: result.Messages, Page: pageValue(result.Page)}, err
}
func (c *SearchClient) BySender(ctx context.Context, peerID, senderID int64, query string, limit int, cursor string) (MessagePage, error) {
	result, err := c.client.call(ctx, protocol.ClientRequest{Operation: protocol.ClientSearchMessagesBySender, PeerID: peerID, SenderID: senderID, Query: query, Limit: limit, Cursor: cursor})
	return MessagePage{Messages: result.Messages, Page: pageValue(result.Page)}, err
}
func (c *SearchClient) ByMedia(ctx context.Context, peerID int64, mediaKind string, limit int, cursor string) (MessagePage, error) {
	result, err := c.client.call(ctx, protocol.ClientRequest{Operation: protocol.ClientSearchMessagesByMedia, PeerID: peerID, MediaKind: mediaKind, Limit: limit, Cursor: cursor})
	return MessagePage{Messages: result.Messages, Page: pageValue(result.Page)}, err
}
func (c *DialogClient) List(ctx context.Context, folderID, limit int, cursor string) (DialogPage, error) {
	result, err := c.client.call(ctx, protocol.ClientRequest{Operation: protocol.ClientListDialogs, FolderID: folderID, Limit: limit, Cursor: cursor})
	return DialogPage{Dialogs: result.Dialogs, Page: pageValue(result.Page)}, err
}
func (c *ParticipantClient) List(ctx context.Context, peerID int64, limit int, cursor string) (ParticipantPage, error) {
	result, err := c.client.call(ctx, protocol.ClientRequest{Operation: protocol.ClientListParticipants, PeerID: peerID, Limit: limit, Cursor: cursor})
	return ParticipantPage{Participants: result.Participants, Page: pageValue(result.Page)}, err
}
func (c *MediaMetadataClient) Inspect(ctx context.Context, handle string) (*protocol.Media, error) {
	result, err := c.client.call(ctx, protocol.ClientRequest{Operation: protocol.ClientInspectMedia, Handle: handle})
	return result.Media, err
}

func firstUser(values []protocol.User) *protocol.User {
	if len(values) == 0 {
		return nil
	}
	return &values[0]
}
func firstChat(values []protocol.Chat) *protocol.Chat {
	if len(values) == 0 {
		return nil
	}
	return &values[0]
}
func pageValue(value *protocol.PageInfo) protocol.PageInfo {
	if value == nil {
		return protocol.PageInfo{}
	}
	return *value
}
