package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestSafeModerationRPCErrorAtDistinguishesRequiredRight(t *testing.T) {
	err := safeModerationRPCErrorAt(
		tgerr.New(403, "CHAT_ADMIN_REQUIRED"),
		"channels.editBanned",
		"ban_users",
	)
	wire, ok := err.(*pluginv1.Error)
	if !ok {
		t.Fatalf("expected protocol error, got %T", err)
	}
	if wire.Code != pluginv1.ErrorModerationRightRequired {
		t.Fatalf("code = %q, want %q", wire.Code, pluginv1.ErrorModerationRightRequired)
	}
	if wire.Details["telegram_stage"] != "channels.editBanned" {
		t.Fatalf("stage detail = %q", wire.Details["telegram_stage"])
	}
	if wire.Details["required_right"] != "ban_users" {
		t.Fatalf("required-right detail = %q", wire.Details["required_right"])
	}
}

func TestChannelActingMemberUsesChannelEffectiveRights(t *testing.T) {
	channel := &tg.Channel{ID: 42, Megagroup: true}
	channel.SetAdminRights(tg.ChatAdminRights{BanUsers: true})

	member, found := channelActingMember([]tg.ChatClass{channel}, 42, 99)
	if !found {
		t.Fatal("acting member was not found")
	}
	if member.Status != pluginv1.MemberAdmin {
		t.Fatalf("status = %q, want %q", member.Status, pluginv1.MemberAdmin)
	}
	if member.Admin == nil || member.Admin.Rights.BanUsers == nil || !*member.Admin.Rights.BanUsers {
		t.Fatalf("ban_users was not preserved: %#v", member.Admin)
	}

	channel.Creator = true
	member, found = channelActingMember([]tg.ChatClass{channel}, 42, 99)
	if !found || member.Status != pluginv1.MemberCreator {
		t.Fatalf("creator status was not preserved: %#v", member)
	}
}

func TestEditBannedPermissionErrorReportsEffectiveRights(t *testing.T) {
	t.Run("admin without ban users", func(t *testing.T) {
		allowed := false
		err := editBannedPermissionError(pluginv1.ModerationMember{
			Status: pluginv1.MemberAdmin,
			Admin:  &pluginv1.AdminRole{Rights: pluginv1.AdminRights{BanUsers: &allowed}},
		})
		wire := err.(*pluginv1.Error)
		if wire.Code != pluginv1.ErrorModerationRightRequired || wire.Details["effective_right"] != "false" {
			t.Fatalf("unexpected error: %#v", wire)
		}
	})

	t.Run("admin with ban users", func(t *testing.T) {
		allowed := true
		err := editBannedPermissionError(pluginv1.ModerationMember{
			Status: pluginv1.MemberAdmin,
			Admin:  &pluginv1.AdminRole{Rights: pluginv1.AdminRights{BanUsers: &allowed}},
		})
		wire := err.(*pluginv1.Error)
		if wire.Details["effective_right"] != "true" {
			t.Fatalf("unexpected error: %#v", wire)
		}
	})

	t.Run("regular member", func(t *testing.T) {
		err := editBannedPermissionError(pluginv1.ModerationMember{Status: pluginv1.MemberRegular})
		wire := err.(*pluginv1.Error)
		if wire.Code != pluginv1.ErrorModerationAdminRequired {
			t.Fatalf("code = %q, want %q", wire.Code, pluginv1.ErrorModerationAdminRequired)
		}
	})
}
