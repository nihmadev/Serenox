package telegram

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/tg"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type memorySession struct {
	payload []byte
	loadErr error
	saved   []byte
}

func (s *memorySession) Load() ([]byte, error) { return append([]byte(nil), s.payload...), s.loadErr }
func (s *memorySession) Save(value []byte) error {
	s.saved = append([]byte(nil), value...)
	return nil
}
func (*memorySession) Delete() error { return nil }

func TestSessionAdapterMapsMissingAndHonorsCancellation(t *testing.T) {
	storage := gotdSessionStorage{store: &memorySession{loadErr: os.ErrNotExist}}
	if _, err := storage.LoadSession(nil); err == nil {
		t.Fatal("expected nil load context error")
	}
	if err := storage.StoreSession(nil, nil); err == nil {
		t.Fatal("expected nil store context error")
	}
	if _, err := storage.LoadSession(context.Background()); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("missing session error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := storage.LoadSession(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled load error = %v", err)
	}
	if err := storage.StoreSession(ctx, []byte("secret")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled store error = %v", err)
	}
}

func TestSessionAdapterUsesBoundedAtomicFileStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session", "gotd.json")
	file, err := NewFileSession(path, 16)
	if err != nil {
		t.Fatal(err)
	}
	storage := gotdSessionStorage{store: file}
	if err := storage.StoreSession(context.Background(), []byte("session")); err != nil {
		t.Fatal(err)
	}
	got, err := storage.LoadSession(context.Background())
	if err != nil || string(got) != "session" {
		t.Fatalf("LoadSession = %q, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("session permissions = %v, %v", info, err)
	}
}

type fakePrompt struct {
	phone    string
	code     string
	password string
	metadata CodePrompt
}

func (p *fakePrompt) Phone(context.Context) (string, error) { return p.phone, nil }
func (p *fakePrompt) Code(_ context.Context, metadata CodePrompt) (string, error) {
	p.metadata = metadata
	return p.code, nil
}
func (p *fakePrompt) Password(context.Context) (string, error) { return p.password, nil }

func TestAuthorizePromptAuthenticatorPhoneCodeAnd2FA(t *testing.T) {
	prompt := &fakePrompt{phone: " +15551234567 ", code: " 12345 ", password: "two factor secret"}
	authenticator := &promptAuthenticator{prompt: prompt}
	phone, err := authenticator.Phone(context.Background())
	if err != nil || phone != "+15551234567" {
		t.Fatalf("Phone = %q, %v", phone, err)
	}
	sent := &tg.AuthSentCode{Type: &tg.AuthSentCodeTypeSMS{Length: 5}}
	sent.SetTimeout(30)
	code, err := authenticator.Code(context.Background(), sent)
	if err != nil || code != "12345" {
		t.Fatalf("Code = %q, %v", code, err)
	}
	if prompt.metadata.Delivery != "sms" || prompt.metadata.Length != 5 || prompt.metadata.Timeout != 30*time.Second {
		t.Fatalf("code metadata = %#v", prompt.metadata)
	}
	password, err := authenticator.Password(context.Background())
	if err != nil || password != "two factor secret" {
		t.Fatalf("Password returned unexpected value/error: length=%d err=%v", len(password), err)
	}
	if err := authenticator.AcceptTermsOfService(context.Background(), tg.HelpTermsOfService{}); !errors.Is(err, ErrTermsUnsupported) {
		t.Fatalf("terms error = %v", err)
	}
}

func TestLiveConfigValidationAndDefaults(t *testing.T) {
	config, err := (LiveConfig{AppID: 1, AppHash: "hash", Session: &memorySession{}}).normalized()
	if err != nil {
		t.Fatal(err)
	}
	if config.UpdateCapacity != 256 || config.PeerCacheCapacity != 4096 {
		t.Fatalf("defaults = %#v", config)
	}
	if _, err := (LiveConfig{AppID: 1, AppHash: "hash", Session: &memorySession{}, UpdateCapacity: -1}).normalized(); err == nil {
		t.Fatal("expected invalid capacity")
	}
}

func TestLiveCloseIsIdempotentAndPreventsStart(t *testing.T) {
	done := make(chan struct{})
	close(done)
	cancelCalled := false
	live := &LiveTransport{
		running: true,
		done:    done,
		cancel:  func() { cancelCalled = true },
	}
	if err := live.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !cancelCalled || !live.closed {
		t.Fatalf("cancel=%t closed=%t", cancelCalled, live.closed)
	}
	if err := live.Close(context.Background()); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if err := live.Run(context.Background(), SinkFunc(func(context.Context, pluginv1.Update) error { return nil })); !errors.Is(err, ErrClosed) {
		t.Fatalf("run after close error=%v", err)
	}
}

func TestPeerBookIsBoundedAndReturnsDetachedPeers(t *testing.T) {
	book := newPeerBook(2)
	learnUser := func(id, accessHash int64) {
		user := &tg.User{ID: id}
		user.SetAccessHash(accessHash)
		book.learn(tg.Entities{Users: map[int64]*tg.User{id: user}})
	}
	learnUser(1, 101)
	learnUser(2, 202)
	learnUser(3, 303)
	if len(book.items) != 2 || book.order.Len() != 2 {
		t.Fatalf("peer cache size: map=%d list=%d", len(book.items), book.order.Len())
	}
	if _, err := book.resolve(1); !errors.Is(err, ErrPeerNotResolved) {
		t.Fatalf("evicted peer error=%v", err)
	}
	resolved, err := book.resolve(2)
	if err != nil {
		t.Fatal(err)
	}
	userPeer := resolved.(*tg.InputPeerUser)
	userPeer.AccessHash = 999
	again, err := book.resolve(2)
	if err != nil || again.(*tg.InputPeerUser).AccessHash != 202 {
		t.Fatalf("detached peer=%#v err=%v", again, err)
	}
}

func TestLiveConvertsAndBoundsUpdateQueue(t *testing.T) {
	live := &LiveTransport{updates: make(chan pluginv1.Update, 1), peers: newPeerBook(2)}
	entities := tg.Entities{Users: map[int64]*tg.User{}, Chats: map[int64]*tg.Chat{}, Channels: map[int64]*tg.Channel{}}
	message := &tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 42}, Date: 100, Message: "hello"}
	message.SetFromID(&tg.PeerUser{UserID: 42})
	if err := live.enqueueMessage(context.Background(), pluginv1.UpdateMessage, entities, message); err != nil {
		t.Fatal(err)
	}
	got := <-live.updates
	if got.Kind != pluginv1.UpdateMessage || got.Message == nil || got.Message.ChatID != 42 || got.Message.Text != "hello" {
		t.Fatalf("converted update = %#v", got)
	}
	live.updates <- got
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := live.enqueueMessage(ctx, pluginv1.UpdateEditedMessage, entities, message); !errors.Is(err, context.Canceled) {
		t.Fatalf("backpressure cancellation error = %v", err)
	}
}

func TestLiveDeletedUpdatesAreCopiedAndBounded(t *testing.T) {
	live := &LiveTransport{updates: make(chan pluginv1.Update, 1)}
	messageIDs := []int{10, 11}
	if err := live.enqueueDeleted(context.Background(), "channel", 42, 7, messageIDs); err != nil {
		t.Fatal(err)
	}
	messageIDs[0] = 999
	got := <-live.updates
	if got.Kind != pluginv1.UpdateDeleted || len(got.DeletedIDs) != 2 || got.DeletedIDs[0] != 10 {
		t.Fatalf("deleted update = %#v", got)
	}
	tooMany := make([]int, pluginv1.MaxCollectionItems+1)
	for index := range tooMany {
		tooMany[index] = index + 1
	}
	if err := live.enqueueDeleted(context.Background(), "messages", 0, 8, tooMany); err == nil {
		t.Fatal("expected deleted ID bound error")
	}
}

func TestDocumentMediaClassificationPreservesCommandFilters(t *testing.T) {
	tests := []struct {
		name  string
		media *tg.MessageMediaDocument
		kind  string
	}{
		{"voice-flag", &tg.MessageMediaDocument{Voice: true}, "voice"},
		{"video-flag", &tg.MessageMediaDocument{Video: true}, "video"},
		{"sticker", &tg.MessageMediaDocument{Document: &tg.Document{Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeSticker{}}}}, "sticker"},
		{"audio", &tg.MessageMediaDocument{Document: &tg.Document{Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{}}}}, "audio"},
		{"video", &tg.MessageMediaDocument{Document: &tg.Document{Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeVideo{}}}}, "video"},
		{"document", &tg.MessageMediaDocument{Document: &tg.Document{}}, "document"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := convertDocumentMedia(test.media)
			if got == nil || got.Kind != test.kind {
				t.Fatalf("media=%#v want kind=%q", got, test.kind)
			}
		})
	}
}

func TestMessageEntityConversionUsesStableDTOsAndBounds(t *testing.T) {
	got, err := convertEntities([]tg.MessageEntityClass{
		&tg.MessageEntityBold{Offset: 0, Length: 4},
		&tg.MessageEntityPre{Offset: 5, Length: 3, Language: "go"},
		&tg.MessageEntityTextURL{Offset: 9, Length: 4, URL: "https://example.invalid"},
		&tg.MessageEntityMentionName{Offset: 14, Length: 5, UserID: 42},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].Type != "bold" || got[1].Language != "go" || got[2].URL == "" || got[3].UserID != 42 {
		t.Fatalf("entities=%#v", got)
	}
	tooMany := make([]tg.MessageEntityClass, pluginv1.MaxCollectionItems+1)
	for index := range tooMany {
		tooMany[index] = &tg.MessageEntityBold{Offset: index, Length: 1}
	}
	if _, err := convertEntities(tooMany); err == nil {
		t.Fatal("expected entity bound error")
	}
}

type fakeActionAPI struct {
	send          *tg.MessagesSendMessageRequest
	edit          *tg.MessagesEditMessageRequest
	delete        *tg.MessagesDeleteMessagesRequest
	channelDelete *tg.ChannelsDeleteMessagesRequest
}

func (f *fakeActionAPI) MessagesSendMessage(_ context.Context, request *tg.MessagesSendMessageRequest) (tg.UpdatesClass, error) {
	f.send = request
	return nil, nil
}

func (f *fakeActionAPI) MessagesEditMessage(_ context.Context, request *tg.MessagesEditMessageRequest) (tg.UpdatesClass, error) {
	f.edit = request
	return nil, nil
}

func (f *fakeActionAPI) MessagesDeleteMessages(_ context.Context, request *tg.MessagesDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error) {
	f.delete = request
	return &tg.MessagesAffectedMessages{}, nil
}

func (f *fakeActionAPI) ChannelsDeleteMessages(_ context.Context, request *tg.ChannelsDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error) {
	f.channelDelete = request
	return &tg.MessagesAffectedMessages{}, nil
}

func TestLiveApplySendReplyEditDeleteAndHTML(t *testing.T) {
	api := &fakeActionAPI{}
	peers := newPeerBook(4)
	peers.setSelf(7)
	live := &LiveTransport{api: api, running: true, identity: Identity{UserID: 7}, peers: peers}

	result, err := live.Apply(context.Background(), pluginv1.Action{
		ID: "edit", Kind: pluginv1.ActionEdit, ChatID: 7, MessageID: 10,
		Text: "<b>pong</b>", ParseMode: "HTML",
	})
	if err != nil || !result.Success || result.ActionID != "edit" {
		t.Fatalf("edit result=%#v err=%v", result, err)
	}
	if api.edit == nil || api.edit.Message != "pong" || len(api.edit.Entities) != 1 || api.edit.ID != 10 {
		t.Fatalf("edit request=%#v", api.edit)
	}

	result, err = live.Apply(context.Background(), pluginv1.Action{
		ID: "reply", Kind: pluginv1.ActionReply, ChatID: 7, MessageID: 11, Text: "hello",
	})
	if err != nil || !result.Success || api.send == nil {
		t.Fatalf("reply result=%#v request=%#v err=%v", result, api.send, err)
	}
	reply, ok := api.send.ReplyTo.(*tg.InputReplyToMessage)
	if !ok || reply.ReplyToMsgID != 11 || api.send.RandomID == 0 {
		t.Fatalf("reply request=%#v", api.send)
	}

	result, err = live.Apply(context.Background(), pluginv1.Action{
		ID: "delete", Kind: pluginv1.ActionDelete, ChatID: 7, MessageID: 12,
	})
	if err != nil || !result.Success || api.delete == nil || len(api.delete.ID) != 1 || api.delete.ID[0] != 12 || !api.delete.Revoke {
		t.Fatalf("delete result=%#v request=%#v err=%v", result, api.delete, err)
	}
}

func TestLiveApplyFailsClosedForUnsupportedOptionsAndUnknownPeers(t *testing.T) {
	api := &fakeActionAPI{}
	peers := newPeerBook(2)
	peers.setSelf(7)
	live := &LiveTransport{api: api, running: true, identity: Identity{UserID: 7}, peers: peers}

	_, err := live.Apply(context.Background(), pluginv1.Action{
		ID: "markdown", Kind: pluginv1.ActionSend, ChatID: 7, Text: "x", ParseMode: "MarkdownV2",
	})
	if !errors.Is(err, ErrUnsupportedActionOptions) {
		t.Fatalf("parse mode error=%v", err)
	}
	_, err = live.Apply(context.Background(), pluginv1.Action{
		ID: "peer", Kind: pluginv1.ActionSend, ChatID: 123, Text: "x",
	})
	if !errors.Is(err, ErrPeerNotResolved) {
		t.Fatalf("peer error=%v", err)
	}
	_, err = live.Apply(context.Background(), pluginv1.Action{
		ID: "client", Kind: pluginv1.ActionClient,
		ClientOperation: &pluginv1.ClientOperation{Name: "get_entity"},
	})
	if !errors.Is(err, ErrActionUnsupported) {
		t.Fatalf("client operation error=%v", err)
	}
	if api.send != nil || api.edit != nil || api.delete != nil || api.channelDelete != nil {
		t.Fatal("unsupported action reached Telegram API")
	}
}
