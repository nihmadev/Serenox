package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

// ProfileClient is the transport-neutral profile surface. Telegram request
// and response objects are intentionally private to this package.
type ProfileClient interface {
	GetProfileIdentity(context.Context) (*pluginv1.Peer, error)
	UpdateProfileName(context.Context, string, string) (*pluginv1.Peer, error)
}

// ProfileActionRecorder is an optional deterministic-test hook. It receives
// the already validated transport-neutral action; live adapters need not
// implement it.
type ProfileActionRecorder interface {
	ApplyProfileName(context.Context, pluginv1.Action) (*pluginv1.Peer, error)
}

func profileDTO(user *tg.User) (*pluginv1.Peer, error) {
	if user == nil || user.ID == 0 {
		return nil, &pluginv1.Error{Code: pluginv1.ErrorOutcomeUnknown, Message: "Telegram did not return a profile identity"}
	}
	first, _ := user.GetFirstName()
	last, _ := user.GetLastName()
	result := &pluginv1.Peer{ID: user.ID, Kind: "user", FirstName: first, LastName: last}
	result.Username, _ = user.GetUsername()
	if err := result.Validate(); err != nil {
		return nil, &pluginv1.Error{Code: pluginv1.ErrorProfileInvalidName, Message: "Telegram returned an invalid profile name"}
	}
	return result, nil
}

func mapProfileRPCError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if wait, ok := tgerr.AsFloodWait(err); ok {
		return &pluginv1.Error{Code: pluginv1.ErrorFloodWait, Message: "Telegram profile update is rate limited", Retryable: true, FloodWaitSeconds: int64(wait.Seconds())}
	}
	upper := strings.ToUpper(err.Error())
	switch {
	case strings.Contains(upper, "FIRST_NAME_INVALID"), strings.Contains(upper, "LAST_NAME_INVALID"), strings.Contains(upper, "NAME_INVALID"):
		return &pluginv1.Error{Code: pluginv1.ErrorProfileInvalidName, Message: "Telegram rejected the profile name"}
	case strings.Contains(upper, "AUTH_KEY") || strings.Contains(upper, "SESSION_PASSWORD") || strings.Contains(upper, "UNAUTHORIZED"):
		return &pluginv1.Error{Code: pluginv1.ErrorAuthentication, Message: "Telegram authorization is unavailable"}
	case strings.Contains(upper, "FORBIDDEN") || strings.Contains(upper, "PERMISSION"):
		return &pluginv1.Error{Code: pluginv1.ErrorPermissionDenied, Message: "Telegram denied the profile update"}
	case strings.Contains(upper, "DISCONNECT") || strings.Contains(upper, "NETWORK"):
		return &pluginv1.Error{Code: pluginv1.ErrorDisconnected, Message: "Telegram disconnected during the profile update"}
	default:
		return fmt.Errorf("telegram profile update: %w", err)
	}
}

func (t *LiveTransport) GetProfileIdentity(ctx context.Context) (*pluginv1.Peer, error) {
	if err := t.ensureReadable(); err != nil {
		return nil, err
	}
	user, err := t.client.Self(ctx)
	if err != nil {
		return nil, mapProfileRPCError(err)
	}
	return profileDTO(user)
}

func (t *LiveTransport) UpdateProfileName(ctx context.Context, firstName, lastName string) (*pluginv1.Peer, error) {
	if err := t.ensureReadable(); err != nil {
		return nil, err
	}
	if err := (pluginv1.ProfileNameAction{Operation: pluginv1.ProfileUpdate, FirstName: firstName, LastName: lastName, Generation: 1, LeaseToken: "profile"}).Validate(); err != nil {
		return nil, &pluginv1.Error{Code: pluginv1.ErrorProfileInvalidName, Message: "profile name is outside the bounded contract"}
	}
	api, ok := t.api.(interface {
		AccountUpdateProfile(context.Context, *tg.AccountUpdateProfileRequest) (tg.UserClass, error)
	})
	if !ok {
		return nil, &pluginv1.Error{Code: pluginv1.ErrorUnsupported, Message: "Telegram profile update is unavailable"}
	}
	user, err := api.AccountUpdateProfile(ctx, &tg.AccountUpdateProfileRequest{FirstName: firstName, LastName: lastName})
	if err != nil {
		return nil, mapProfileRPCError(err)
	}
	converted, ok := user.(*tg.User)
	if !ok {
		return nil, &pluginv1.Error{Code: pluginv1.ErrorOutcomeUnknown, Message: "Telegram accepted a profile update without returning a bounded profile"}
	}
	return profileDTO(converted)
}

var _ ProfileClient = (*LiveTransport)(nil)
