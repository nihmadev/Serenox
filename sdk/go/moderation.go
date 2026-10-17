package plugin

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type ModerationClient struct {
	rpc          *serverRPC
	sequence     atomic.Uint64
	Members      *ModerationMembers
	Admins       *ModerationAdmins
	Messages     *ModerationMessages
	Settings     *ModerationSettings
	JoinRequests *ModerationJoinRequests
	Invites      *ModerationInvites
	Topics       *ModerationTopics
}

type ModerationMembers struct{ client *ModerationClient }
type ModerationAdmins struct{ client *ModerationClient }
type ModerationMessages struct{ client *ModerationClient }
type ModerationSettings struct{ client *ModerationClient }
type ModerationJoinRequests struct{ client *ModerationClient }
type ModerationInvites struct{ client *ModerationClient }
type ModerationTopics struct{ client *ModerationClient }

func newModerationClient(rpc *serverRPC) *ModerationClient {
	c := &ModerationClient{rpc: rpc}
	c.Members, c.Admins, c.Messages = &ModerationMembers{c}, &ModerationAdmins{c}, &ModerationMessages{c}
	c.Settings, c.JoinRequests, c.Invites, c.Topics = &ModerationSettings{c}, &ModerationJoinRequests{c}, &ModerationInvites{c}, &ModerationTopics{c}
	return c
}

func (c *ModerationClient) call(ctx context.Context, request protocol.ModerationRequest) (protocol.ModerationResult, error) {
	if c == nil || c.rpc == nil {
		return protocol.ModerationResult{}, fmt.Errorf("moderation service is unavailable")
	}
	if request.RequestID == "" {
		request.RequestID = fmt.Sprintf("sdk-%d", c.sequence.Add(1))
	}
	if err := request.Validate(); err != nil {
		return protocol.ModerationResult{}, err
	}
	var result protocol.ModerationResult
	if err := c.rpc.Call(ctx, protocol.MethodHostModeration, request, &result); err != nil {
		return protocol.ModerationResult{}, err
	}
	if err := result.Validate(); err != nil {
		return protocol.ModerationResult{}, err
	}
	return result, nil
}

func UserTarget(userID int64) protocol.ModerationParticipantTarget {
	return protocol.ModerationParticipantTarget{UserID: userID}
}
func UsernameTarget(username string) protocol.ModerationParticipantTarget {
	return protocol.ModerationParticipantTarget{Username: username}
}
func ReplyTarget(chatID, messageID int64) protocol.ModerationParticipantTarget {
	return protocol.ModerationParticipantTarget{Reply: &protocol.MessageReference{PeerID: chatID, MessageID: messageID}}
}

func (m *ModerationMembers) Get(ctx context.Context, chat protocol.ModerationChatTarget, target protocol.ModerationParticipantTarget) (*protocol.ModerationMember, error) {
	result, err := m.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationGetMember, Chat: chat, Participant: &target})
	return result.Member, err
}
func (m *ModerationMembers) List(ctx context.Context, chat protocol.ModerationChatTarget, filter, query string, limit int, cursor string) (protocol.ModerationResult, error) {
	return m.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationListMembers, Chat: chat, Page: &protocol.ModerationPageRequest{Filter: filter, Query: query, Limit: limit, Cursor: cursor}})
}
func (m *ModerationMembers) Restrict(ctx context.Context, chat protocol.ModerationChatTarget, target protocol.ModerationParticipantTarget, restriction protocol.Restriction, key string) (protocol.ModerationResult, error) {
	return m.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationRestrictMember, Chat: chat, Participant: &target, Restriction: &restriction, IdempotencyKey: key})
}
func (m *ModerationMembers) RestrictIfState(ctx context.Context, chat protocol.ModerationChatTarget, target protocol.ModerationParticipantTarget, restriction protocol.Restriction, expectedState, key string) (protocol.ModerationResult, error) {
	return m.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationRestrictMember, Chat: chat, Participant: &target, Restriction: &restriction, ExpectedState: expectedState, IdempotencyKey: key})
}
func (m *ModerationMembers) Mute(ctx context.Context, chat protocol.ModerationChatTarget, target protocol.ModerationParticipantTarget, duration time.Duration, key string) (protocol.ModerationResult, error) {
	denied := true
	restriction := protocol.Restriction{Denied: protocol.DeniedRights{SendMessages: &denied, SendMedia: &denied, SendPhotos: &denied, SendVideos: &denied, SendRoundVideos: &denied, SendVoice: &denied, SendAudios: &denied, SendDocuments: &denied, SendStickers: &denied, SendGIFs: &denied, SendGames: &denied, UseInlineBots: &denied, EmbedLinks: &denied, SendPolls: &denied, SendPlain: &denied}}
	if duration == 0 {
		restriction.Permanent = true
	} else {
		restriction.UntilUnixMS = time.Now().UTC().Add(duration).UnixMilli()
	}
	return m.Restrict(ctx, chat, target, restriction, key)
}
func (m *ModerationMembers) mutate(ctx context.Context, operation protocol.ModerationOperation, chat protocol.ModerationChatTarget, target protocol.ModerationParticipantTarget, key string) (protocol.ModerationResult, error) {
	return m.client.call(ctx, protocol.ModerationRequest{Operation: operation, Chat: chat, Participant: &target, IdempotencyKey: key})
}
func (m *ModerationMembers) Unmute(ctx context.Context, chat protocol.ModerationChatTarget, target protocol.ModerationParticipantTarget, key string) (protocol.ModerationResult, error) {
	return m.mutate(ctx, protocol.ModerationUnrestrictMember, chat, target, key)
}
func (m *ModerationMembers) UnmuteIfState(ctx context.Context, chat protocol.ModerationChatTarget, target protocol.ModerationParticipantTarget, expectedState, key string) (protocol.ModerationResult, error) {
	return m.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationUnrestrictMember, Chat: chat, Participant: &target, ExpectedState: expectedState, IdempotencyKey: key})
}
func (m *ModerationMembers) Ban(ctx context.Context, chat protocol.ModerationChatTarget, target protocol.ModerationParticipantTarget, key string) (protocol.ModerationResult, error) {
	return m.mutate(ctx, protocol.ModerationBanMember, chat, target, key)
}
func (m *ModerationMembers) Unban(ctx context.Context, chat protocol.ModerationChatTarget, target protocol.ModerationParticipantTarget, key string) (protocol.ModerationResult, error) {
	return m.mutate(ctx, protocol.ModerationUnbanMember, chat, target, key)
}
func (m *ModerationMembers) Kick(ctx context.Context, chat protocol.ModerationChatTarget, target protocol.ModerationParticipantTarget, key string) (protocol.ModerationResult, error) {
	return m.mutate(ctx, protocol.ModerationKickMember, chat, target, key)
}

func (a *ModerationAdmins) ActingRights(ctx context.Context, chat protocol.ModerationChatTarget) (*protocol.ModerationMember, error) {
	result, err := a.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationGetActingRights, Chat: chat})
	return result.Member, err
}
func (a *ModerationAdmins) Get(ctx context.Context, chat protocol.ModerationChatTarget, target protocol.ModerationParticipantTarget) (*protocol.ModerationMember, error) {
	result, err := a.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationGetAdmin, Chat: chat, Participant: &target})
	return result.Member, err
}
func (a *ModerationAdmins) List(ctx context.Context, chat protocol.ModerationChatTarget, limit int, cursor string) (protocol.ModerationResult, error) {
	return a.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationListAdmins, Chat: chat, Page: &protocol.ModerationPageRequest{Limit: limit, Cursor: cursor}})
}
func (a *ModerationAdmins) Set(ctx context.Context, chat protocol.ModerationChatTarget, target protocol.ModerationParticipantTarget, role protocol.AdminRole, key string) (protocol.ModerationResult, error) {
	return a.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationSetAdmin, Chat: chat, Participant: &target, Admin: &role, IdempotencyKey: key})
}
func (a *ModerationAdmins) Demote(ctx context.Context, chat protocol.ModerationChatTarget, target protocol.ModerationParticipantTarget, key string) (protocol.ModerationResult, error) {
	return a.client.call(ctx, protocol.ModerationRequest{Operation: protocol.ModerationDemoteAdmin, Chat: chat, Participant: &target, IdempotencyKey: key})
}
