package v1

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestModerationRequestGoldenPreservesRightsPresence(t *testing.T) {
	deny, allow := true, false
	request := ModerationRequest{Operation: ModerationRestrictMember, RequestID: "req-1", Chat: ModerationChatTarget{ChatID: -1000000000042, ExpectedKind: ModerationChatMegagroup}, Participant: &ModerationParticipantTarget{Reply: &MessageReference{PeerID: -1000000000042, MessageID: 7}}, Restriction: &Restriction{Denied: DeniedRights{SendMessages: &deny, SendMedia: &allow}, UntilUnixMS: time.Now().Add(time.Hour).UnixMilli()}, IdempotencyKey: "mute-7"}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ModerationRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Restriction.Denied.SendMessages == nil || !*decoded.Restriction.Denied.SendMessages || decoded.Restriction.Denied.SendMedia == nil || *decoded.Restriction.Denied.SendMedia || decoded.Restriction.Denied.SendPolls != nil {
		t.Fatal("presence-aware rights did not round trip")
	}
}

func TestModerationOperationPermissionCatalogIsExhaustive(t *testing.T) {
	operations := []ModerationOperation{ModerationGetMember, ModerationListMembers, ModerationRestrictMember, ModerationUnrestrictMember, ModerationBanMember, ModerationUnbanMember, ModerationKickMember, ModerationGetActingRights, ModerationGetAdmin, ModerationListAdmins, ModerationSetAdmin, ModerationDemoteAdmin, ModerationDeleteMessages, ModerationPinMessage, ModerationUnpinMessage, ModerationUnpinAll, ModerationGetPinned, ModerationGetSettings, ModerationSetDefaultRights, ModerationSetSlowMode, ModerationSetProtected, ModerationListJoinRequests, ModerationApproveJoin, ModerationDeclineJoin, ModerationCreateInvite, ModerationListInvites, ModerationEditInvite, ModerationRevokeInvite, ModerationGetTopic, ModerationCloseTopic, ModerationReopenTopic, ModerationHideGeneralTopic, ModerationUnhideGeneralTopic, ModerationDeleteTopic}
	seen := map[ModerationOperation]bool{}
	for _, operation := range operations {
		if seen[operation] || ModerationPermission(operation) == "" {
			t.Fatalf("missing or duplicate permission for %q", operation)
		}
		seen[operation] = true
	}
	if ModerationPermission("raw.invoke") != "" {
		t.Fatal("unknown operation received a permission")
	}
}

func TestModerationValidationRejectsAmbiguousAndOversizedTargets(t *testing.T) {
	base := ModerationRequest{Operation: ModerationBanMember, RequestID: "x", Chat: ModerationChatTarget{ChatID: -42}, Participant: &ModerationParticipantTarget{UserID: 1, Username: "also-a-user"}}
	if err := base.Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("ambiguous target: %v", err)
	}
	base.Participant = &ModerationParticipantTarget{UserID: 1}
	base.Messages = make([]ModerationMessageTarget, MaxModerationBatchItems+1)
	if err := base.Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("oversized batch: %v", err)
	}
}

func TestModerationResultBounds(t *testing.T) {
	result := ModerationResult{Operation: ModerationDeleteMessages, RequestID: "r", ChatID: -1, Status: ActionResultPartial, Items: make([]ModerationItemResult, MaxModerationBatchItems+1)}
	if err := result.Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("oversized result: %v", err)
	}
}

func TestModerationOperationSpecificValidation(t *testing.T) {
	truth := true
	cases := []ModerationRequest{
		{Operation: ModerationRestrictMember, RequestID: "r", Chat: ModerationChatTarget{ChatID: -1}, Participant: &ModerationParticipantTarget{UserID: 2}, Restriction: &Restriction{Denied: DeniedRights{SendMessages: &truth}}},
		{Operation: ModerationSetAdmin, RequestID: "a", Chat: ModerationChatTarget{ChatID: -1}, Participant: &ModerationParticipantTarget{UserID: 2}, Admin: &AdminRole{}},
		{Operation: ModerationSetSlowMode, RequestID: "s", Chat: ModerationChatTarget{ChatID: -1}, Settings: &ChatModerationSettings{SlowModeSeconds: intp(11)}},
		{Operation: ModerationSetProtected, RequestID: "p", Chat: ModerationChatTarget{ChatID: -1}, Settings: &ChatModerationSettings{}},
		{Operation: ModerationGetMember, RequestID: "g", Chat: ModerationChatTarget{ChatID: -1}, Participant: &ModerationParticipantTarget{UserID: 2}, Page: &ModerationPageRequest{Limit: 1}},
	}
	for index, request := range cases {
		if err := request.Validate(); !errors.Is(err, ErrInvalidDTO) {
			t.Fatalf("case %d was accepted: %v", index, err)
		}
	}
}

func TestModerationDecodeIsAdditive(t *testing.T) {
	payload := json.RawMessage(`{"operation":"members.get","request_id":"r","chat":{"chat_id":-1,"future_chat_field":true},"participant":{"user_id":2},"future_top_level":"ignored"}`)
	var request ModerationRequest
	if err := DecodePayload(payload, &request); err != nil {
		t.Fatal(err)
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
}

func intp(value int) *int { return &value }

func BenchmarkModerationValidation(b *testing.B) {
	truth := true
	request := ModerationRequest{Operation: ModerationRestrictMember, RequestID: "bench", Chat: ModerationChatTarget{ChatID: -42}, Participant: &ModerationParticipantTarget{UserID: 7}, Restriction: &Restriction{Denied: DeniedRights{SendMessages: &truth}, Permanent: true}, IdempotencyKey: "bench-key"}
	b.ReportAllocs()
	for range b.N {
		if err := request.Validate(); err != nil {
			b.Fatal(err)
		}
	}
}
