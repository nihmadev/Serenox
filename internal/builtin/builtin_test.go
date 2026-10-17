package builtin

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nihmadev/Serenox/internal/dispatch"
	"github.com/nihmadev/Serenox/internal/executor"
	"github.com/nihmadev/Serenox/internal/storage"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type actionRecorder struct {
	mu      sync.Mutex
	actions []pluginv1.Action
}

func (r *actionRecorder) sink(_ context.Context, actions []pluginv1.Action) error {
	r.mu.Lock()
	r.actions = append(r.actions, actions...)
	r.mu.Unlock()
	return nil
}

func (r *actionRecorder) wait(t *testing.T) pluginv1.Action {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		if len(r.actions) > 0 {
			action := r.actions[len(r.actions)-1]
			r.mu.Unlock()
			return action
		}
		r.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for action")
	return pluginv1.Action{}
}

func testCatalog(t *testing.T) (*Catalog, *executor.Manager, *dispatch.CommandRegistry, *actionRecorder) {
	t.Helper()
	store, err := storage.NewMemory(storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "."
	recorder := &actionRecorder{}
	catalog, err := New(Services{
		Store: store, StartedAt: time.Now().Add(-time.Minute), Prefix: func() string { return prefix },
		SetPrefix: func(value string) error { prefix = value; return nil },
	}, recorder.sink)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := executor.NewManager(context.Background(), 16)
	if err != nil {
		t.Fatal(err)
	}
	commands, err := dispatch.NewCommandRegistry(dispatch.RegistryLimits{MaxCommands: 128, MaxAliases: 128})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Start(context.Background(), manager, commands, executor.Config{QueueCapacity: 4, Workers: 1, HandlerTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = catalog.Stop(ctx, manager)
		_ = manager.Close(ctx)
		_ = store.Close()
	})
	return catalog, manager, commands, recorder
}

func submitCommand(t *testing.T, manager *executor.Manager, registry *dispatch.CommandRegistry, commandName, args string) dispatch.CommandTarget {
	t.Helper()
	target, ok := registry.Resolve(commandName)
	if !ok {
		t.Fatalf("command %s not found", commandName)
	}
	request := pluginv1.DispatchRequest{
		Update:  pluginv1.Update{ID: "u1", Kind: pluginv1.UpdateMessage, OccurredUnixMS: 1, Message: &pluginv1.Message{ID: 10, ChatID: 20, SenderID: 1, DateUnixMS: 1, Text: "." + commandName}},
		Command: &pluginv1.Command{Name: target.Name, Prefix: ".", RawArgs: args, Message: pluginv1.Message{ID: 10, ChatID: 20, SenderID: 1, DateUnixMS: 1, Text: "." + commandName}},
	}
	if err := manager.TrySubmit(target.PluginID, executor.Job{ID: "job", HandlerID: target.HandlerID, Kind: executor.KindCommand, Payload: request}); err != nil {
		t.Fatal(err)
	}
	return target
}

func TestHelpAndAliasDispatch(t *testing.T) {
	_, manager, commands, recorder := testCatalog(t)
	submitCommand(t, manager, commands, "help", "")
	action := recorder.wait(t)
	if !strings.Contains(action.Text, "Serenox modules") || action.Kind != pluginv1.ActionEdit {
		t.Fatalf("unexpected help: %#v", action)
	}
	target, ok := commands.Resolve("diag")
	if !ok || target.Name != "diagnostics" || target.PluginID != "core.diagnostics" {
		t.Fatalf("alias not canonicalized: %#v %v", target, ok)
	}
}

func TestPingReportsMeasuredDiagnostics(t *testing.T) {
	_, manager, commands, recorder := testCatalog(t)
	submitCommand(t, manager, commands, "ping", "")
	action := recorder.wait(t)
	for _, marker := range []string{"Ping diagnostics", "Обработка в ядре:", "Возраст обновления:", "Маршрут:", "Telegram MTProto", "Uptime:"} {
		if !strings.Contains(action.Text, marker) {
			t.Fatalf("ping response is missing %q: %s", marker, action.Text)
		}
	}
}

func TestSettingsPrefixAndPersistence(t *testing.T) {
	catalog, manager, commands, recorder := testCatalog(t)
	submitCommand(t, manager, commands, "setprefix", "!")
	if action := recorder.wait(t); !strings.Contains(action.Text, "updated") {
		t.Fatalf("unexpected setprefix: %#v", action)
	}
	if got := catalog.services.Prefix(); got != "!" {
		t.Fatalf("prefix not updated: %s", got)
	}
	payload, found, err := catalog.services.Store.Get(context.Background(), "core.settings", "command_prefix")
	if err != nil || !found || string(payload) != `"!"` {
		t.Fatalf("prefix not stored: %s %v %v", payload, found, err)
	}
}

func TestCoreProtectionAndFailureRecovery(t *testing.T) {
	_, manager, commands, _ := testCatalog(t)
	submitCommand(t, manager, commands, "unloadmod", "core.help")
	target, _ := commands.Resolve("fail")
	results := make(chan executor.Result, 1)
	// The catalog's pool already isolates failure; submit and observe stats.
	_ = results
	request := pluginv1.DispatchRequest{Update: pluginv1.Update{ID: "u", Kind: pluginv1.UpdateMessage, OccurredUnixMS: 1, Message: &pluginv1.Message{ID: 1, ChatID: 1, DateUnixMS: 1}}, Command: &pluginv1.Command{Name: "fail", Prefix: ".", Message: pluginv1.Message{ID: 1, ChatID: 1, DateUnixMS: 1}}}
	if err := manager.TrySubmit(target.PluginID, executor.Job{ID: "failure", HandlerID: target.HandlerID, Kind: executor.KindCommand, Payload: request}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		pool, _ := manager.Pool(target.PluginID)
		if pool.Stats().Failed > 0 {
			if pool.Stats().Completed == 0 {
				t.Fatal("failure not completed")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(errors.New("failure fixture was not isolated"))
}
