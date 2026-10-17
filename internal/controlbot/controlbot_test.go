package controlbot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

const testToken = "123456789:abcdefghijklmnopqrstuvwxyzABCDE"

func fakeAPI(t *testing.T, handler func(string, map[string]any) any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		method := filepath.Base(r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": handler(method, payload)})
	}))
}

func TestAttachVerifiesBeforePrivatePersistence(t *testing.T) {
	server := fakeAPI(t, func(method string, _ map[string]any) any {
		if method != "getMe" {
			t.Fatalf("method = %s", method)
		}
		return map[string]any{"id": 42, "is_bot": true, "username": "control_bot"}
	})
	defer server.Close()
	store, err := NewSecretStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, ClientOptions{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := manager.Attach(context.Background(), testToken)
	if err != nil {
		t.Fatal(err)
	}
	if identity.ID != 42 || identity.Username != "control_bot" {
		t.Fatalf("identity = %+v", identity)
	}
	if stored, err := store.Load(); err != nil || stored != testToken {
		t.Fatalf("stored = %q, %v", stored, err)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestDefaultClientDeadlineExceedsLongPoll(t *testing.T) {
	client, err := NewClient(testToken, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if client.http.Timeout <= 25*time.Second {
		t.Fatalf("HTTP timeout %s cancels the 25-second Bot API long poll", client.http.Timeout)
	}
}

func TestManagerRestartsAfterTerminatedPoller(t *testing.T) {
	server := fakeAPI(t, func(method string, _ map[string]any) any {
		switch method {
		case "getMe":
			return map[string]any{"id": 42, "is_bot": true, "username": "control_bot"}
		case "getUpdates":
			return []any{}
		default:
			t.Fatalf("unexpected method %s", method)
			return nil
		}
	})
	defer server.Close()
	store, err := NewSecretStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(testToken); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(testToken, ClientOptions{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, ClientOptions{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	manager.client, manager.identity = client, Identity{ID: 42, Username: "control_bot"}
	manager.poller = &Poller{} // a previous poller has already terminated
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx, SinkFunc(func(context.Context, pluginv1.Update) error { return nil })); err != nil {
		t.Fatalf("restart after terminated poller: %v", err)
	}
	if !manager.Status().Running {
		t.Fatal("replacement poller did not start")
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBotActionsUseOnlyControlChannel(t *testing.T) {
	server := fakeAPI(t, func(method string, payload map[string]any) any {
		if method != "sendMessage" {
			t.Fatalf("method = %s", method)
		}
		if payload["chat_id"].(float64) != 99 {
			t.Fatal("wrong chat")
		}
		return map[string]any{"message_id": 7}
	})
	defer server.Close()
	client, err := NewClient(testToken, ClientOptions{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Apply(context.Background(), pluginv1.Action{ID: "a", Kind: pluginv1.ActionBotSend, ResponseChannel: pluginv1.ResponseChannelControlBot, ChatID: 99, Text: "hello"})
	if err != nil || !result.Success || result.MessageID != 7 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := client.Apply(context.Background(), pluginv1.Action{ID: "b", Kind: pluginv1.ActionBotSend, ChatID: 99, Text: "hello"}); err == nil {
		t.Fatal("accepted bot action without control channel")
	}
}

func TestInlineArticleUsesBotAPISchema(t *testing.T) {
	server := fakeAPI(t, func(method string, payload map[string]any) any {
		if method != "answerInlineQuery" {
			t.Fatalf("method = %s", method)
		}
		results, ok := payload["results"].([]any)
		if !ok || len(results) != 1 {
			t.Fatalf("results = %#v", payload["results"])
		}
		result := results[0].(map[string]any)
		content, ok := result["input_message_content"].(map[string]any)
		if !ok || content["message_text"] != "Pong" {
			t.Fatalf("inline result = %#v", result)
		}
		return true
	})
	defer server.Close()
	client, err := NewClient(testToken, ClientOptions{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Apply(context.Background(), pluginv1.Action{
		ID: "inline", Kind: pluginv1.ActionBotAnswerInline, ResponseChannel: pluginv1.ResponseChannelControlBot,
		InlineQueryID: "query", InlineResults: []pluginv1.InlineResult{{ID: "one", Type: "article", Title: "Ping", Text: "Pong"}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRegistryAndFlowAuthorization(t *testing.T) {
	registry, err := NewRegistry(1, 8)
	if err != nil {
		t.Fatal(err)
	}
	declaration := pluginv1.ControlCommand{Name: "search", HandlerID: "search", Surfaces: []pluginv1.ControlSurface{pluginv1.ControlSurfaceInline}, ResponseMode: pluginv1.ControlResponseInlineResult}
	if err := registry.Register("plugin", []pluginv1.ControlCommand{declaration}, []pluginv1.Capability{pluginv1.CapabilityControlBot}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve("search", 2, pluginv1.ControlSurfaceInline, "x"); !errors.Is(err, ErrCommandUnauthorized) {
		t.Fatalf("want unauthorized, got %v", err)
	}
	if _, err := registry.Resolve("search", 1, pluginv1.ControlSurfaceInline, "x"); err != nil {
		t.Fatal(err)
	}
	flows, err := NewFlowStore(2, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	flow, err := flows.Create(10, 20, 1, 30, "plugin", []byte("state"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flows.Consume(flow.ID, "plugin", 10, 20, 2, 30); !errors.Is(err, ErrCommandUnauthorized) {
		t.Fatalf("cross-user flow consumed: %v", err)
	}
	if _, err := flows.Consume(flow.ID, "plugin", 10, 20, 1, 30); err != nil {
		t.Fatal(err)
	}
}
