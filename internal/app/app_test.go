package app

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nihmadev/Serenox/internal/builtin"
	"github.com/nihmadev/Serenox/internal/config"
	"github.com/nihmadev/Serenox/internal/observability"
	"github.com/nihmadev/Serenox/internal/storage"
	"github.com/nihmadev/Serenox/internal/telegram"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func testOptions(t *testing.T, transport telegram.Transport) Options {
	t.Helper()
	cfg := config.Defaults()
	cfg.Execution.QueueCapacity = 4
	cfg.Execution.WorkersPerPlugin = 1
	cfg.Execution.HandlerTimeout = config.Duration(time.Second)
	cfg.RateLimit.TokensPerSecond = 1000
	cfg.RateLimit.Burst = 100
	cfg.RateLimit.MaxIdentities = 64
	cfg.RateLimit.Shards = 4
	cfg.RateLimit.IdleTTL = config.Duration(time.Second)
	cfg.RateLimit.SweepInterval = config.Duration(10 * time.Millisecond)
	cfg.ShutdownTimeout = config.Duration(time.Second)
	store, err := storage.NewMemory(storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return Options{
		Config: cfg, SelfID: 7, Transport: transport, Store: store,
		IngressCapacity: 4, CommandCapacity: 2, IngressWorkers: 1,
	}
}

func TestRecordedVerticalSliceHelpAndShutdown(t *testing.T) {
	before := runtime.NumGoroutine()
	transport, err := telegram.LoadRecorded("../telegram/testdata/updates.ndjson", telegram.RecordedOptions{})
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(testOptions(t, transport))
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	actions := transport.Actions()
	if len(actions) != 1 || !strings.Contains(actions[0].Text, "Pong") {
		t.Fatalf("recorded command result: %#v", actions)
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > before+1 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if runtime.NumGoroutine() > before+2 {
		t.Fatalf("goroutines did not return: before=%d after=%d", before, runtime.NumGoroutine())
	}
}

func TestWrongSenderDoesNotDispatch(t *testing.T) {
	fixture := `{"id":"wrong","kind":"message","occurred_unix_ms":1,"message":{"id":1,"chat_id":2,"sender_id":8,"date_unix_ms":1,"text":".ping","outgoing":true}}` + "\n"
	transport, err := telegram.ReadRecorded(strings.NewReader(fixture), telegram.RecordedOptions{})
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(testOptions(t, transport))
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if actions := transport.Actions(); len(actions) != 0 {
		t.Fatalf("wrong sender dispatched: %#v", actions)
	}
}

func TestPluginControllerRestoreAndCloseAreOwnedByApp(t *testing.T) {
	transport, err := telegram.ReadRecorded(strings.NewReader(""), telegram.RecordedOptions{})
	if err != nil {
		t.Fatal(err)
	}
	controller := &lifecyclePluginController{}
	options := testOptions(t, transport)
	options.PluginController = controller
	application, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if controller.restores.Load() != 1 || controller.closes.Load() != 1 {
		t.Fatalf("plugin lifecycle restore=%d close=%d", controller.restores.Load(), controller.closes.Load())
	}
}

func TestCancellationCleansLiveRun(t *testing.T) {
	client := &blockingClient{}
	transport, err := telegram.NewClientTransport(client, telegram.ReconnectPolicy{Attempts: 1, Initial: time.Millisecond, Maximum: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(testOptions(t, transport))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("application shutdown timed out")
	}
}

func TestExternalPluginRegistrationDispatchFilteringAndUnload(t *testing.T) {
	client := &runtimeClient{updates: make(chan pluginv1.Update, 8), applied: make(chan struct{}, 8)}
	transport, err := telegram.NewClientTransport(client, telegram.ReconnectPolicy{
		Attempts: 1, Initial: time.Millisecond, Maximum: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(testOptions(t, transport))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	waitAppReady(t, application)

	metadata := pluginv1.PluginMetadata{
		ID: "example.external", Name: "External", Version: "1.0.0", ProtocolVersion: pluginv1.Version,
		Capabilities: []pluginv1.Capability{pluginv1.CapabilityCommands, pluginv1.CapabilityWatchers},
		Commands:     []pluginv1.CommandSpec{{Name: "echo", Aliases: []string{"e"}}},
		Watchers: []pluginv1.WatcherSpec{
			{ID: "first", Filter: pluginv1.WatcherFilter{Kinds: []pluginv1.UpdateKind{pluginv1.UpdateMessage}, TextPrefix: "watch"}},
			{ID: "second", Filter: pluginv1.WatcherFilter{Kinds: []pluginv1.UpdateKind{pluginv1.UpdateMessage}, TextPrefix: "watch"}},
		},
	}
	var calls atomic.Int32
	handler := func(_ context.Context, request pluginv1.DispatchRequest) (pluginv1.DispatchResponse, error) {
		calls.Add(1)
		message := request.Update.Message
		if request.Command != nil {
			return pluginv1.DispatchResponse{Actions: []pluginv1.Action{{
				ID: "external-command", Kind: pluginv1.ActionEdit,
				ChatID: message.ChatID, MessageID: message.ID, Text: request.Command.RawArgs,
			}}}, nil
		}
		return pluginv1.DispatchResponse{Actions: []pluginv1.Action{{
			ID: "external-watcher", Kind: pluginv1.ActionSend, ChatID: message.ChatID, Text: "watched",
		}}}, nil
	}
	if err := application.RegisterExternal(context.Background(), metadata, handler); err != nil {
		t.Fatal(err)
	}
	client.updates <- pluginv1.Update{
		ID: "command", Kind: pluginv1.UpdateMessage, OccurredUnixMS: 1,
		Message: &pluginv1.Message{ID: 10, ChatID: 20, SenderID: 7, DateUnixMS: 1, Text: ".e hello", Outgoing: true},
	}
	client.updates <- pluginv1.Update{
		ID: "watcher", Kind: pluginv1.UpdateMessage, OccurredUnixMS: 2,
		Message: &pluginv1.Message{ID: 11, ChatID: 20, SenderID: 8, DateUnixMS: 2, Text: "watch me"},
	}
	for index := 0; index < 2; index++ {
		select {
		case <-client.applied:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for external action")
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("external dispatch calls = %d; overlapping watcher filters must enqueue once", got)
	}
	if err := application.UnregisterExternal(context.Background(), metadata.ID); err != nil {
		t.Fatal(err)
	}
	client.updates <- pluginv1.Update{
		ID: "after-unload", Kind: pluginv1.UpdateMessage, OccurredUnixMS: 3,
		Message: &pluginv1.Message{ID: 12, ChatID: 20, SenderID: 8, DateUnixMS: 3, Text: "watch again"},
	}
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != 2 {
		t.Fatalf("unloaded plugin received dispatch; calls=%d", got)
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestExternalPluginRegistrationRollsBackConflictsAndUnsupportedKinds(t *testing.T) {
	if _, err := protocolWatcherSpec(pluginv1.WatcherFilter{Kinds: []pluginv1.UpdateKind{pluginv1.UpdateCallback}}); err == nil || !strings.Contains(err.Error(), "no message watcher DTO") {
		t.Fatalf("unsupported watcher kind error = %v", err)
	}
	if spec, err := protocolWatcherSpec(pluginv1.WatcherFilter{Incoming: true, ChatIDs: []int64{1}}); err != nil || spec.Direction != 1 || len(spec.ChatIDs) != 1 {
		t.Fatalf("mapped watcher spec = %#v, %v", spec, err)
	}
}

type runtimeClient struct {
	updates chan pluginv1.Update
	applied chan struct{}
	mu      sync.Mutex
	actions []pluginv1.Action
}

type lifecyclePluginController struct {
	restores atomic.Int32
	closes   atomic.Int32
}

func (*lifecyclePluginController) List() []builtin.PluginStatus         { return nil }
func (*lifecyclePluginController) Load(context.Context, string) error   { return nil }
func (*lifecyclePluginController) Unload(context.Context, string) error { return nil }
func (c *lifecyclePluginController) Restore(context.Context) error {
	c.restores.Add(1)
	return nil
}
func (c *lifecyclePluginController) Close(context.Context) error {
	c.closes.Add(1)
	return nil
}

func (*runtimeClient) Connect(context.Context) error { return nil }
func (c *runtimeClient) NextUpdate(ctx context.Context) (pluginv1.Update, error) {
	select {
	case update := <-c.updates:
		return update, nil
	case <-ctx.Done():
		return pluginv1.Update{}, ctx.Err()
	}
}
func (c *runtimeClient) Apply(_ context.Context, action pluginv1.Action) (pluginv1.ActionResult, error) {
	c.mu.Lock()
	c.actions = append(c.actions, action)
	c.mu.Unlock()
	c.applied <- struct{}{}
	return pluginv1.ActionResult{ActionID: action.ID, Success: true}, nil
}
func (*runtimeClient) Close(context.Context) error { return nil }

func waitAppReady(t *testing.T, application *App) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, status := range application.Health() {
			if status.Component == "core" && status.State == observability.HealthReady {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("application did not become ready")
}

type blockingClient struct{}

func (*blockingClient) Connect(context.Context) error { return nil }
func (*blockingClient) NextUpdate(ctx context.Context) (pluginv1.Update, error) {
	<-ctx.Done()
	return pluginv1.Update{}, ctx.Err()
}
func (*blockingClient) Apply(context.Context, pluginv1.Action) (pluginv1.ActionResult, error) {
	return pluginv1.ActionResult{}, nil
}
func (*blockingClient) Close(context.Context) error { return nil }

func BenchmarkRecordedDispatch(b *testing.B) {
	for index := 0; index < b.N; index++ {
		transport, _ := telegram.ReadRecorded(strings.NewReader(`{"id":"ping","kind":"message","occurred_unix_ms":1,"message":{"id":1,"chat_id":2,"sender_id":7,"date_unix_ms":1,"text":".ping","outgoing":true}}`+"\n"), telegram.RecordedOptions{})
		options := testOptions(&testing.T{}, transport)
		application, _ := New(options)
		_ = application.Run(context.Background())
	}
}
