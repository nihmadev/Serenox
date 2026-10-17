package app

import (
	"context"
	"testing"

	"github.com/nihmadev/Serenox/internal/telegram"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type profileTestTransport struct{ current pluginv1.Peer }

func (t *profileTestTransport) Run(context.Context, telegram.Sink) error { return nil }
func (t *profileTestTransport) Apply(context.Context, pluginv1.Action) (pluginv1.ActionResult, error) {
	return pluginv1.ActionResult{}, nil
}
func (t *profileTestTransport) Close(context.Context) error { return nil }
func (t *profileTestTransport) GetProfileIdentity(context.Context) (*pluginv1.Peer, error) {
	copy := t.current
	return &copy, nil
}
func (t *profileTestTransport) UpdateProfileName(_ context.Context, first, last string) (*pluginv1.Peer, error) {
	t.current.FirstName, t.current.LastName = first, last
	copy := t.current
	return &copy, nil
}

// Keep the fake's Run signature exactly compatible with telegram.Transport.
var _ interface {
	Run(context.Context, telegram.Sink) error
} = (*profileTestTransport)(nil)

func TestProfileLeaseRejectsStaleOwnerAndRestoresStructuredName(t *testing.T) {
	transport := &profileTestTransport{current: pluginv1.Peer{ID: 7, Kind: "user", FirstName: "Alice | Bob", LastName: "Smith"}}
	app := &App{options: Options{SelfID: 7}, transport: transport, profile: newProfileLeaseManager()}
	acquire := pluginv1.Action{ID: "acquire", Kind: pluginv1.ActionProfileUpdateName, IdempotencyKey: "acquire-1", Profile: &pluginv1.ProfileNameAction{Operation: pluginv1.ProfileAcquire}}
	first, err := app.applyProfileAction(context.Background(), "profile.clock", acquire)
	if err != nil || first.Status != pluginv1.ActionResultSuccess {
		t.Fatalf("acquire: %#v %v", first, err)
	}
	duplicate, _ := app.applyProfileAction(context.Background(), "profile.clock", acquire)
	if duplicate.Profile == nil || duplicate.Profile.LeaseToken != first.Profile.LeaseToken {
		t.Fatalf("idempotent acquire: %#v %#v", first.Profile, duplicate.Profile)
	}
	lease := first.Profile
	update := pluginv1.Action{ID: "update", Kind: pluginv1.ActionProfileUpdateName, IdempotencyKey: "update-1", Profile: &pluginv1.ProfileNameAction{Operation: pluginv1.ProfileUpdate, FirstName: "Alice | Bob | 14:37", LastName: "Smith", Generation: lease.Generation, LeaseToken: lease.LeaseToken}}
	if result, _ := app.applyProfileAction(context.Background(), "profile.clock", update); result.Status != pluginv1.ActionResultSuccess {
		t.Fatalf("update: %#v", result)
	}
	transport.current.FirstName = "User changed this"
	stale := update
	stale.ID, stale.IdempotencyKey = "stale", "stale-1"
	result, _ := app.applyProfileAction(context.Background(), "profile.clock", stale)
	if result.Error == nil || result.Error.Code != pluginv1.ErrorProfileStaleLease {
		t.Fatalf("stale result: %#v", result)
	}
	if app.profile.lease != nil {
		t.Fatal("stale ownership was retained")
	}
}
