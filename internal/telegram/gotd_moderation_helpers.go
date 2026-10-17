package telegram

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func boolp(value bool) *bool { return &value }

func (t *LiveTransport) moderationChat(chat pluginv1.ModerationChatTarget) (tg.InputPeerClass, *tg.InputChannel, pluginv1.ModerationChatKind, bool, error) {
	if err := t.ensureReadable(); err != nil {
		return nil, nil, "", false, err
	}
	peer, cachedKind, forum, err := t.peers.moderationInfo(chat.ChatID)
	if err != nil {
		return nil, nil, "", false, err
	}
	kind := pluginv1.ModerationChatKind(cachedKind)
	if chat.ExpectedKind != "" && chat.ExpectedKind != kind {
		return nil, nil, kind, forum, &pluginv1.Error{Code: pluginv1.ErrorModerationUnsupportedChat, Message: "resolved chat kind does not match the requested kind"}
	}
	channelPeer, ok := peer.(*tg.InputPeerChannel)
	if !ok {
		return peer, nil, kind, forum, nil
	}
	return peer, &tg.InputChannel{ChannelID: channelPeer.ChannelID, AccessHash: channelPeer.AccessHash}, kind, forum, nil
}

func (t *LiveTransport) moderationTarget(ctx context.Context, chat pluginv1.ModerationChatTarget, target pluginv1.ModerationParticipantTarget) (tg.InputPeerClass, tg.InputUserClass, int64, error) {
	id := target.UserID
	if target.Reply != nil {
		message, err := t.GetMessage(ctx, target.Reply.PeerID, target.Reply.MessageID)
		if err != nil {
			return nil, nil, 0, err
		}
		if message == nil || message.SenderID == 0 {
			return nil, nil, 0, &pluginv1.Error{Code: pluginv1.ErrorModerationAnonymousSender, Message: "replied message has no visible user sender"}
		}
		if message.SenderID < 0 {
			return nil, nil, 0, &pluginv1.Error{Code: pluginv1.ErrorModerationChannelSender, Message: "replied message was sent as a channel"}
		}
		id = message.SenderID
	}
	if target.Username != "" {
		peer, err := t.ResolvePeer(ctx, 0, target.Username)
		if err != nil {
			return nil, nil, 0, err
		}
		if peer == nil || peer.Kind != "private" {
			return nil, nil, 0, &pluginv1.Error{Code: pluginv1.ErrorModerationTargetUnavailable, Message: "username does not resolve to a user"}
		}
		id = peer.ID
	}
	if id <= 0 {
		return nil, nil, 0, &pluginv1.Error{Code: pluginv1.ErrorModerationTargetUnavailable, Message: "participant target is not a user"}
	}
	peer, err := t.peers.resolve(id)
	if err != nil {
		return nil, nil, 0, err
	}
	var user tg.InputUserClass
	switch value := peer.(type) {
	case *tg.InputPeerSelf:
		user = &tg.InputUserSelf{}
	case *tg.InputPeerUser:
		user = &tg.InputUser{UserID: value.UserID, AccessHash: value.AccessHash}
	default:
		return nil, nil, 0, &pluginv1.Error{Code: pluginv1.ErrorModerationTargetUnavailable, Message: "participant target is not a user"}
	}
	return peer, user, id, nil
}

func restrictionToTG(value *pluginv1.Restriction, current *tg.ChatBannedRights) (tg.ChatBannedRights, error) {
	if value == nil {
		return tg.ChatBannedRights{}, nil
	}
	result := tg.ChatBannedRights{}
	if current != nil {
		result = *current
	}
	rights := value.Denied
	setBool := func(source *bool, destination *bool) {
		if source != nil {
			*destination = *source
		}
	}
	setBool(rights.SendMessages, &result.SendMessages)
	setBool(rights.SendMedia, &result.SendMedia)
	setBool(rights.SendPhotos, &result.SendPhotos)
	setBool(rights.SendVideos, &result.SendVideos)
	setBool(rights.SendRoundVideos, &result.SendRoundvideos)
	setBool(rights.SendVoice, &result.SendVoices)
	setBool(rights.SendAudios, &result.SendAudios)
	setBool(rights.SendDocuments, &result.SendDocs)
	setBool(rights.SendStickers, &result.SendStickers)
	setBool(rights.SendGIFs, &result.SendGifs)
	setBool(rights.SendGames, &result.SendGames)
	setBool(rights.UseInlineBots, &result.SendInline)
	setBool(rights.EmbedLinks, &result.EmbedLinks)
	setBool(rights.SendPolls, &result.SendPolls)
	setBool(rights.ChangeInfo, &result.ChangeInfo)
	setBool(rights.InviteUsers, &result.InviteUsers)
	setBool(rights.PinMessages, &result.PinMessages)
	setBool(rights.ManageTopics, &result.ManageTopics)
	setBool(rights.SendPlain, &result.SendPlain)
	if value.Permanent {
		result.UntilDate = 0
	} else if value.UntilUnixMS != 0 {
		if value.UntilUnixMS/1000 > math.MaxInt32 {
			return tg.ChatBannedRights{}, &pluginv1.Error{Code: pluginv1.ErrorModerationInvalidDeadline, Message: "restriction deadline exceeds Telegram range"}
		}
		result.UntilDate = int(value.UntilUnixMS / 1000)
	}
	return result, nil
}

func restrictionFromTG(value tg.ChatBannedRights) *pluginv1.Restriction {
	return &pluginv1.Restriction{Denied: pluginv1.DeniedRights{SendMessages: boolp(value.SendMessages), SendMedia: boolp(value.SendMedia), SendPhotos: boolp(value.SendPhotos), SendVideos: boolp(value.SendVideos), SendRoundVideos: boolp(value.SendRoundvideos), SendVoice: boolp(value.SendVoices), SendAudios: boolp(value.SendAudios), SendDocuments: boolp(value.SendDocs), SendStickers: boolp(value.SendStickers), SendGIFs: boolp(value.SendGifs), SendGames: boolp(value.SendGames), UseInlineBots: boolp(value.SendInline), EmbedLinks: boolp(value.EmbedLinks), SendPolls: boolp(value.SendPolls), ChangeInfo: boolp(value.ChangeInfo), InviteUsers: boolp(value.InviteUsers), PinMessages: boolp(value.PinMessages), ManageTopics: boolp(value.ManageTopics), SendPlain: boolp(value.SendPlain)}, UntilUnixMS: int64(value.UntilDate) * 1000, Permanent: value.UntilDate == 0}
}

func adminToTG(value *pluginv1.AdminRole) (tg.ChatAdminRights, string) {
	if value == nil {
		return tg.ChatAdminRights{}, ""
	}
	r := value.Rights
	result := tg.ChatAdminRights{}
	set := func(source *bool, destination *bool) {
		if source != nil {
			*destination = *source
		}
	}
	set(r.ChangeInfo, &result.ChangeInfo)
	set(r.PostMessages, &result.PostMessages)
	set(r.EditMessages, &result.EditMessages)
	set(r.DeleteMessages, &result.DeleteMessages)
	set(r.BanUsers, &result.BanUsers)
	set(r.InviteUsers, &result.InviteUsers)
	set(r.PinMessages, &result.PinMessages)
	set(r.AddAdmins, &result.AddAdmins)
	set(r.Anonymous, &result.Anonymous)
	set(r.ManageCall, &result.ManageCall)
	set(r.ManageTopics, &result.ManageTopics)
	set(r.PostStories, &result.PostStories)
	set(r.EditStories, &result.EditStories)
	set(r.DeleteStories, &result.DeleteStories)
	rank := ""
	if value.Rank != nil {
		rank = *value.Rank
	}
	return result, rank
}

func adminFromTG(value tg.ChatAdminRights, rank string) *pluginv1.AdminRole {
	return &pluginv1.AdminRole{Rights: pluginv1.AdminRights{ChangeInfo: boolp(value.ChangeInfo), PostMessages: boolp(value.PostMessages), EditMessages: boolp(value.EditMessages), DeleteMessages: boolp(value.DeleteMessages), BanUsers: boolp(value.BanUsers), InviteUsers: boolp(value.InviteUsers), PinMessages: boolp(value.PinMessages), AddAdmins: boolp(value.AddAdmins), Anonymous: boolp(value.Anonymous), ManageCall: boolp(value.ManageCall), ManageTopics: boolp(value.ManageTopics), PostStories: boolp(value.PostStories), EditStories: boolp(value.EditStories), DeleteStories: boolp(value.DeleteStories)}, Rank: &rank}
}

func moderationUnsupported(message string) error {
	return &pluginv1.Error{Code: pluginv1.ErrorUnsupported, Message: message}
}

func safeModerationRPCError(err error) error {
	return safeModerationRPCErrorAt(err, "", "")
}

// safeModerationRPCErrorAt maps Telegram's deliberately coarse RPC errors to
// the public moderation taxonomy while retaining enough fixed, non-sensitive
// context to identify which permission and RPC stage Telegram rejected.
func safeModerationRPCErrorAt(err error, stage, requiredRight string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if wait, ok := tgerr.AsFloodWait(err); ok {
		seconds := int64((wait + time.Second - 1) / time.Second)
		return &pluginv1.Error{Code: pluginv1.ErrorFloodWait, Message: "Telegram flood wait", Retryable: true, FloodWaitSeconds: seconds}
	}
	upper := strings.ToUpper(err.Error())
	switch {
	case strings.Contains(upper, "CHAT_NOT_MODIFIED"):
		return errModerationNoChange
	case strings.Contains(upper, "CHAT_ADMIN_REQUIRED"):
		details := moderationErrorDetails(stage, requiredRight)
		if requiredRight != "" {
			operation := "the operation"
			if stage != "" {
				operation = stage
			}
			return &pluginv1.Error{
				Code:    pluginv1.ErrorModerationRightRequired,
				Message: "Telegram denied " + operation + " in this exact chat: the acting account is not an administrator there or lacks the " + requiredRight + " right",
				Details: details,
			}
		}
		operation := "this operation"
		if stage != "" {
			operation = stage
		}
		return &pluginv1.Error{
			Code:    pluginv1.ErrorModerationAdminRequired,
			Message: "Telegram requires administrator access for " + operation + " in this exact chat",
			Details: details,
		}
	case strings.Contains(upper, "USER_CREATOR") || strings.Contains(upper, "USER_ADMIN_INVALID"):
		return &pluginv1.Error{Code: pluginv1.ErrorModerationProtectedTarget, Message: "Telegram protects this creator or administrator target"}
	case strings.Contains(upper, "PARTICIPANT_ID_INVALID") || strings.Contains(upper, "USER_NOT_PARTICIPANT"):
		return &pluginv1.Error{Code: pluginv1.ErrorNotFound, Message: "participant was not found"}
	case strings.Contains(upper, "MESSAGE_ID_INVALID"):
		return &pluginv1.Error{Code: pluginv1.ErrorMessageNotFound, Message: "message was not found"}
	case strings.Contains(upper, "TOPIC_ID_INVALID"):
		return &pluginv1.Error{Code: pluginv1.ErrorModerationTopicNotFound, Message: "forum topic was not found"}
	case strings.Contains(upper, "INVITE_HASH") || strings.Contains(upper, "INVITE_SLUG"):
		return &pluginv1.Error{Code: pluginv1.ErrorModerationInviteNotFound, Message: "invite was not found"}
	}
	return &pluginv1.Error{Code: pluginv1.ErrorInternal, Message: "Telegram rejected the moderation operation", Retryable: false}
}

func safeModerationMutationError(err error) error {
	return safeModerationMutationErrorAt(err, "", "")
}

func safeModerationMutationErrorAt(err error, stage, requiredRight string) error {
	mapped := safeModerationRPCErrorAt(err, stage, requiredRight)
	if wire, ok := mapped.(*pluginv1.Error); ok && wire.Code == pluginv1.ErrorInternal {
		return &pluginv1.Error{Code: pluginv1.ErrorOutcomeUnknown, Message: "Telegram mutation outcome is unknown", Details: moderationErrorDetails(stage, requiredRight)}
	}
	return mapped
}

func moderationErrorDetails(stage, requiredRight string) map[string]string {
	details := map[string]string{}
	if stage != "" {
		details["telegram_stage"] = stage
	}
	if requiredRight != "" {
		details["required_right"] = requiredRight
	}
	if len(details) == 0 {
		return nil
	}
	return details
}

func validateModerationDeadline(now time.Time, value int64) error {
	if value == 0 {
		return nil
	}
	deadline := time.UnixMilli(value)
	if deadline.Before(now.Add(30*time.Second)) || deadline.After(now.Add(time.Duration(pluginv1.MaxModerationDeadlineDays)*24*time.Hour)) {
		return &pluginv1.Error{Code: pluginv1.ErrorModerationInvalidDeadline, Message: "deadline must be between 30 seconds and 366 days in the future"}
	}
	return nil
}

func assertChannel(channel *tg.InputChannel, kind pluginv1.ModerationChatKind) error {
	if channel == nil || kind == pluginv1.ModerationChatBasicGroup || kind == pluginv1.ModerationChatPrivate {
		return &pluginv1.Error{Code: pluginv1.ErrorModerationUnsupportedChat, Message: fmt.Sprintf("operation requires a megagroup or broadcast channel, got %s", kind)}
	}
	return nil
}
