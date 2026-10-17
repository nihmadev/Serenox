package pluginruntime

import (
	"context"
	"errors"
	"testing"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestReadClientUsesDomainPermission(t *testing.T) {
	called := false
	handle := permissionCheckedClient(func(context.Context, string, int64, pluginv1.ClientRequest) (pluginv1.ClientResult, error) {
		called = true
		return pluginv1.ClientResult{Me: &pluginv1.Peer{ID: 7, Kind: "user"}}, nil
	}, []string{"client.identity.read"})
	if _, err := handle(context.Background(), "plugin.a", 7, pluginv1.ClientRequest{Operation: pluginv1.ClientGetMe}); err != nil || !called {
		t.Fatalf("domain read permission: called=%v err=%v", called, err)
	}
	legacy := permissionCheckedClient(func(context.Context, string, int64, pluginv1.ClientRequest) (pluginv1.ClientResult, error) {
		return pluginv1.ClientResult{}, nil
	}, []string{"client.get_me"})
	if _, err := legacy(context.Background(), "plugin.a", 7, pluginv1.ClientRequest{Operation: pluginv1.ClientGetMe}); err != nil {
		t.Fatalf("legacy permission compatibility: %v", err)
	}
}

func TestProfileActionRequiresDedicatedPermission(t *testing.T) {
	action := pluginv1.Action{ID: "profile", Kind: pluginv1.ActionProfileUpdateName, IdempotencyKey: "one", Profile: &pluginv1.ProfileNameAction{Operation: pluginv1.ProfileAcquire}}
	if err := checkActionPermissions([]pluginv1.Action{action}, map[string]struct{}{"telegram.send": {}}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("profile action without permission: %v", err)
	}
	if err := checkActionPermissions([]pluginv1.Action{action}, map[string]struct{}{pluginv1.ProfilePermission: {}}); err != nil {
		t.Fatalf("profile action with permission: %v", err)
	}
}
