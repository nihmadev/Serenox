package pluginruntime

import (
	"context"
	"testing"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestModerationPermissionAndCapabilityAreBothRequired(t *testing.T) {
	called := false
	handler := func(_ context.Context, _ string, _ int64, _ string, request pluginv1.ModerationRequest) (pluginv1.ModerationResult, error) {
		called = true
		return pluginv1.ModerationResult{Operation: request.Operation, RequestID: request.RequestID, ChatID: request.Chat.ChatID, Status: pluginv1.ActionResultSuccess}, nil
	}
	request := pluginv1.ModerationRequest{Operation: pluginv1.ModerationBanMember, RequestID: "ban", Chat: pluginv1.ModerationChatTarget{ChatID: -1}, Participant: &pluginv1.ModerationParticipantTarget{UserID: 2}}
	withoutCapability := permissionCheckedModeration(handler, []string{pluginv1.PermissionModerationBan}, nil)
	if _, err := withoutCapability(context.Background(), "p", 1, "", request); err == nil {
		t.Fatal("missing capability was accepted")
	}
	withoutPermission := permissionCheckedModeration(handler, nil, []string{string(pluginv1.CapabilityModeration)})
	if _, err := withoutPermission(context.Background(), "p", 1, "", request); err == nil {
		t.Fatal("missing permission was accepted")
	}
	allowed := permissionCheckedModeration(handler, []string{pluginv1.PermissionModerationBan}, []string{string(pluginv1.CapabilityModeration)})
	if _, err := allowed(context.Background(), "p", 1, "", request); err != nil || !called {
		t.Fatalf("allowed request failed: %v", err)
	}
}
