package telegram

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type blockingModerationBackend struct {
	*RecordedTransport
	entered chan struct{}
	release chan struct{}
	active  atomic.Int64
	maximum atomic.Int64
}

func (b *blockingModerationBackend) ModerationSetRestriction(ctx context.Context, _ pluginv1.ModerationChatTarget, _ pluginv1.ModerationParticipantTarget, _ *pluginv1.Restriction) error {
	active := b.active.Add(1)
	defer b.active.Add(-1)
	for {
		maximum := b.maximum.Load()
		if active <= maximum || b.maximum.CompareAndSwap(maximum, active) {
			break
		}
	}
	b.entered <- struct{}{}
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func recordedModerationBackend(t *testing.T) *RecordedTransport {
	t.Helper()
	transport, err := ReadRecorded(strings.NewReader(""), RecordedOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return transport
}

func TestRecordedModerationReplyMuteIdempotencyAndScopedCursor(t *testing.T) {
	fixture := `{"id":"m","kind":"message","occurred_unix_ms":1,"message":{"id":9,"chat_id":-42,"sender_id":77,"date_unix_ms":1,"text":"synthetic"}}` + "\n"
	transport, err := ReadRecorded(strings.NewReader(fixture), RecordedOptions{})
	if err != nil {
		t.Fatal(err)
	}
	scope := ModerationScope{PluginID: "plugin.a", AccountID: 1, Permission: pluginv1.PermissionModerationRestrict}
	truth := true
	restrict := pluginv1.ModerationRequest{Operation: pluginv1.ModerationRestrictMember, RequestID: "r1", Chat: pluginv1.ModerationChatTarget{ChatID: -42}, Participant: &pluginv1.ModerationParticipantTarget{Reply: &pluginv1.MessageReference{PeerID: -42, MessageID: 9}}, Restriction: &pluginv1.Restriction{Denied: pluginv1.DeniedRights{SendMessages: &truth}, UntilUnixMS: time.Now().Add(time.Hour).UnixMilli()}, IdempotencyKey: "same"}
	first, err := transport.Moderate(context.Background(), scope, restrict)
	if err != nil || !first.Acknowledged {
		t.Fatalf("first mute: %#v %v", first, err)
	}
	second, err := transport.Moderate(context.Background(), scope, restrict)
	if err != nil || !second.IdempotentReplay {
		t.Fatalf("replay: %#v %v", second, err)
	}
	for _, id := range []int64{78, 79} {
		request := restrict
		request.RequestID = "next"
		request.Participant = &pluginv1.ModerationParticipantTarget{UserID: id}
		request.IdempotencyKey = "key-" + request.RequestID + string(rune(id))
		if _, err := transport.Moderate(context.Background(), scope, request); err != nil {
			t.Fatal(err)
		}
	}
	readScope := ModerationScope{PluginID: "plugin.a", AccountID: 1, Permission: pluginv1.PermissionModerationReadMembers}
	list := pluginv1.ModerationRequest{Operation: pluginv1.ModerationListMembers, RequestID: "list", Chat: restrict.Chat, Page: &pluginv1.ModerationPageRequest{Limit: 1}}
	page, err := transport.Moderate(context.Background(), readScope, list)
	if err != nil || page.Page == nil || page.Page.Cursor == "" {
		t.Fatalf("first page: %#v %v", page, err)
	}
	list.Page.Cursor = page.Page.Cursor
	if _, err := transport.Moderate(context.Background(), ModerationScope{PluginID: "plugin.b", AccountID: 1, Permission: pluginv1.PermissionModerationReadMembers}, list); err == nil {
		t.Fatal("cursor replay across plugins was accepted")
	}
}

func TestRecordedModerationRejectsAnonymousAndChannelReplyTargets(t *testing.T) {
	fixture := `{"id":"a","kind":"message","occurred_unix_ms":1,"message":{"id":1,"chat_id":-42,"date_unix_ms":1}}` + "\n" + `{"id":"c","kind":"message","occurred_unix_ms":1,"message":{"id":2,"chat_id":-42,"sender_id":-1000000000007,"date_unix_ms":1}}` + "\n"
	transport, err := ReadRecorded(strings.NewReader(fixture), RecordedOptions{})
	if err != nil {
		t.Fatal(err)
	}
	scope := ModerationScope{PluginID: "p", AccountID: 1, Permission: pluginv1.PermissionModerationBan}
	for id, code := range map[int64]pluginv1.ErrorCode{1: pluginv1.ErrorModerationAnonymousSender, 2: pluginv1.ErrorModerationChannelSender} {
		request := pluginv1.ModerationRequest{Operation: pluginv1.ModerationBanMember, RequestID: "ban", Chat: pluginv1.ModerationChatTarget{ChatID: -42}, Participant: &pluginv1.ModerationParticipantTarget{Reply: &pluginv1.MessageReference{PeerID: -42, MessageID: id}}}
		_, err := transport.Moderate(context.Background(), scope, request)
		wire, ok := err.(*pluginv1.Error)
		if !ok || wire.Code != code {
			t.Fatalf("reply %d: %v", id, err)
		}
	}
}

func TestGotdRightsRoundTripAndDeadlineBounds(t *testing.T) {
	truth, falsity := true, false
	source := &pluginv1.Restriction{Denied: pluginv1.DeniedRights{SendMessages: &truth, SendMedia: &falsity, SendVoice: &truth}, Permanent: true}
	raw, err := restrictionToTG(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	round := restrictionFromTG(raw)
	if round.Denied.SendMessages == nil || !*round.Denied.SendMessages || round.Denied.SendMedia == nil || *round.Denied.SendMedia || round.Denied.SendVoice == nil || !*round.Denied.SendVoice {
		t.Fatal("rights did not round trip")
	}
	now := time.Unix(2_000_000_000, 0)
	if validateModerationDeadline(now, now.Add(29*time.Second).UnixMilli()) == nil || validateModerationDeadline(now, now.Add(time.Hour).UnixMilli()) != nil {
		t.Fatal("deadline bounds are incorrect")
	}
}

func TestModerationOrderingSerializesSameAccountTarget(t *testing.T) {
	backend := &blockingModerationBackend{RecordedTransport: recordedModerationBackend(t), entered: make(chan struct{}, 2), release: make(chan struct{})}
	service, err := NewModerationService(backend, 8, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	truth := true
	request := pluginv1.ModerationRequest{Operation: pluginv1.ModerationRestrictMember, RequestID: "one", Chat: pluginv1.ModerationChatTarget{ChatID: -42}, Participant: &pluginv1.ModerationParticipantTarget{UserID: 7}, Restriction: &pluginv1.Restriction{Denied: pluginv1.DeniedRights{SendMessages: &truth}, Permanent: true}}
	var wait sync.WaitGroup
	wait.Add(2)
	for _, pluginID := range []string{"plugin.one", "plugin.two"} {
		go func(pluginID string) {
			defer wait.Done()
			_, _ = service.Moderate(context.Background(), ModerationScope{PluginID: pluginID, AccountID: 1, Permission: pluginv1.PermissionModerationRestrict}, request)
		}(pluginID)
	}
	select {
	case <-backend.entered:
	case <-time.After(time.Second):
		t.Fatal("first mutation did not enter backend")
	}
	select {
	case <-backend.entered:
		t.Fatal("same target entered backend concurrently")
	case <-time.After(25 * time.Millisecond):
	}
	close(backend.release)
	wait.Wait()
	if backend.maximum.Load() != 1 || service.Stats().OrderingEntries != 0 {
		t.Fatalf("ordering was not bounded and serial: max=%d stats=%+v", backend.maximum.Load(), service.Stats())
	}
}

func TestModerationNoopRateLimitAndLifecycleCancellation(t *testing.T) {
	backend := recordedModerationBackend(t)
	service, err := NewModerationService(backend, 2, 8)
	if err != nil {
		t.Fatal(err)
	}
	truth := true
	scope := ModerationScope{PluginID: "plugin", AccountID: 1, Permission: pluginv1.PermissionModerationRestrict}
	request := pluginv1.ModerationRequest{Operation: pluginv1.ModerationRestrictMember, RequestID: "one", Chat: pluginv1.ModerationChatTarget{ChatID: -42}, Participant: &pluginv1.ModerationParticipantTarget{UserID: 7}, Restriction: &pluginv1.Restriction{Denied: pluginv1.DeniedRights{SendMessages: &truth}, Permanent: true}}
	if _, err := service.Moderate(context.Background(), scope, request); err != nil {
		t.Fatal(err)
	}
	request.RequestID = "two"
	result, err := service.Moderate(context.Background(), scope, request)
	if err != nil || result.Status != pluginv1.ActionResultNoop {
		t.Fatalf("repeated mutation was not a no-op: %#v %v", result, err)
	}
	service.Close()
	if service.Stats().Noop != 1 || service.Stats().OrderingEntries != 0 {
		t.Fatalf("unexpected aggregate stats: %+v", service.Stats())
	}

	rateService, err := NewModerationService(recordedModerationBackend(t), 2, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer rateService.Close()
	readScope := ModerationScope{PluginID: "rate", AccountID: 1, Permission: pluginv1.PermissionModerationReadMembers}
	read := pluginv1.ModerationRequest{Operation: pluginv1.ModerationGetMember, Chat: pluginv1.ModerationChatTarget{ChatID: -1}, Participant: &pluginv1.ModerationParticipantTarget{UserID: 2}}
	limited := false
	for index := 0; index < 24; index++ {
		read.RequestID = fmt.Sprintf("read-%d", index)
		_, callErr := rateService.Moderate(context.Background(), readScope, read)
		var wire *pluginv1.Error
		if errors.As(callErr, &wire) && wire.Code == pluginv1.ErrorRateLimited {
			limited = true
			break
		}
	}
	if !limited || rateService.Stats().RateLimited == 0 {
		t.Fatalf("rate bound was not enforced: %+v", rateService.Stats())
	}

	blocking := &blockingModerationBackend{RecordedTransport: recordedModerationBackend(t), entered: make(chan struct{}, 1), release: make(chan struct{})}
	lifecycleService, err := NewModerationService(blocking, 1, 8)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan pluginv1.ModerationResult, 1)
	go func() {
		value, _ := lifecycleService.Moderate(context.Background(), scope, request)
		done <- value
	}()
	<-blocking.entered
	lifecycleService.Close()
	select {
	case value := <-done:
		if value.Status != pluginv1.ActionResultOutcomeUnknown {
			t.Fatalf("in-flight mutation cancellation was not ambiguous: %#v", value)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not cancel in-flight mutation")
	}
}

func TestModerationResourceSoak(t *testing.T) {
	if os.Getenv("SERENOX_SOAK") != "1" {
		t.Skip("set SERENOX_SOAK=1")
	}
	service, err := NewModerationService(recordedModerationBackend(t), 8, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	truth := true
	for index := 0; index < 1000; index++ {
		request := pluginv1.ModerationRequest{Operation: pluginv1.ModerationRestrictMember, RequestID: fmt.Sprintf("soak-%d", index), Chat: pluginv1.ModerationChatTarget{ChatID: int64(-1 - index%100)}, Participant: &pluginv1.ModerationParticipantTarget{UserID: int64(10 + index%100)}, Restriction: &pluginv1.Restriction{Denied: pluginv1.DeniedRights{SendMessages: &truth}, Permanent: true}, IdempotencyKey: fmt.Sprintf("key-%d", index)}
		scope := ModerationScope{PluginID: "soak", AccountID: 1, Permission: pluginv1.PermissionModerationRestrict}
		if _, callErr := service.Moderate(context.Background(), scope, request); callErr != nil {
			t.Fatal(callErr)
		}
	}
	stats := service.Stats()
	if len(service.entries) > 64 || stats.OrderingEntries != 0 || stats.Rate.Identities > stats.Rate.MaxIdentities {
		t.Fatalf("resource bounds failed: entries=%d stats=%+v", len(service.entries), stats)
	}
}

func BenchmarkModerationRightsRoundTrip(b *testing.B) {
	truth := true
	restriction := &pluginv1.Restriction{Denied: pluginv1.DeniedRights{SendMessages: &truth, SendMedia: &truth}, Permanent: true}
	b.ReportAllocs()
	for range b.N {
		raw, err := restrictionToTG(restriction, nil)
		if err != nil {
			b.Fatal(err)
		}
		_ = restrictionFromTG(raw)
	}
}
