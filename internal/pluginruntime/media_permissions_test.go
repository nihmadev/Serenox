package pluginruntime

import (
	"testing"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestMediaActionRequiresAttachPermission(t *testing.T) {
	action := pluginv1.Action{ID: "send-media", Kind: pluginv1.ActionSend, ChatID: 1, Media: &pluginv1.MediaAttachment{Handle: "m1_x", Kind: "photo"}}
	if err := checkActionPermissions([]pluginv1.Action{action}, permissionSet([]string{"telegram.send"})); err == nil {
		t.Fatal("media action bypassed media.attach permission")
	}
	if err := checkActionPermissions([]pluginv1.Action{action}, permissionSet([]string{"media.attach"})); err != nil {
		t.Fatal(err)
	}
}
