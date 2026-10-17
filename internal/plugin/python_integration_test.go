package plugin

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestPythonSidecarEndToEndFixture(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	requireUnixSockets(t)
	base := t.TempDir()
	fixture := t.TempDir()
	pluginSource := `from serenox_sdk import EventPublication, EventSubscription, Module, event_handler, event_success, loader

class Fixture(Module):
    id = "fixture.python"
    version = "1.0.0"
    strings = {"name": "Python fixture"}
    event_publications = (EventPublication("fixture.note.processed"),)
    event_subscriptions = (EventSubscription("fixture.note.created", "consume-note", source_policy="exact_plugin", source_plugin="fixture.go"),)

    @loader.command(name="echo")
    async def echo(self, message):
        await message.edit("ok")

    @loader.pre_send(name="rewrite")
    async def rewrite(self, message):
        message.text = "pre:" + message.text

    @event_handler("consume-note")
    async def consume_note(self, envelope):
        try:
            envelope["id"] = "mutated"
            raise RuntimeError("event envelope was mutable")
        except TypeError:
            pass
        await self.events.publish("fixture.note.processed", {"source_event_id": envelope["id"]}, causation_id=envelope["id"], trace_id=envelope["trace_id"])
        return event_success()
`
	if err := os.WriteFile(filepath.Join(fixture, "plugin.py"), []byte(pluginSource), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceRoot, err := filepath.Abs(filepath.Join("..", "..", "sidecar", "python", "src"))
	if err != nil {
		t.Fatal(err)
	}
	var actionMu sync.Mutex
	var actions []protocol.Action
	events := make(chan protocol.EventPublishRequest, 1)
	supervisor, err := NewSupervisor(SupervisorConfig{
		Command: python, Args: []string{"-m", "serenox_sidecar", "--module", "plugin.py", "--class", "Fixture", "--capabilities", "commands,pre_send,events.publish,events.subscribe"},
		Env: []string{"PYTHONPATH=" + sourceRoot}, Dir: fixture, RuntimeBaseDir: base,
		ExpectedPluginID: "fixture.python", HostVersion: "test", InstanceID: "python-fixture",
		PluginID: "fixture.python", AccountID: 7,
		HandshakeTimeout: 2 * time.Second, ConfigureTimeout: time.Second, StartTimeout: time.Second,
		HealthTimeout: time.Second, DispatchTimeout: time.Second, StopTimeout: time.Second,
		TerminateTimeout: time.Second, Restart: RestartPolicy{Disabled: true},
		ActionHandler: func(_ context.Context, batch []protocol.Action) error {
			actionMu.Lock()
			actions = append(actions, batch...)
			actionMu.Unlock()
			return nil
		},
		EventsHandler: func(_ context.Context, pluginID string, accountID int64, request protocol.EventPublishRequest) (protocol.EventPublishResult, error) {
			if pluginID != "fixture.python" || accountID != 7 {
				return protocol.EventPublishResult{}, errors.New("untrusted event identity")
			}
			events <- request
			return protocol.EventPublishResult{EventID: "evt_00000000000000000000000000000002", Accepted: true, DeliveryClass: protocol.EventDurable}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := supervisor.Start(ctx); err != nil {
		t.Fatalf("start Python sidecar: %v\nstdout=%s\nstderr=%s", err, supervisor.Stdout(), supervisor.Stderr())
	}
	message := protocol.Message{ID: 1, ChatID: 2, SenderID: 3, DateUnixMS: time.Now().UnixMilli(), Text: ".echo"}
	response, err := supervisor.Dispatch(context.Background(), protocol.DispatchRequest{
		Update:  protocol.Update{ID: "fixture-1", Kind: protocol.UpdateMessage, OccurredUnixMS: time.Now().UnixMilli(), Message: &message},
		Command: &protocol.Command{Name: "echo", Prefix: ".", Message: message},
	})
	if err != nil {
		t.Fatalf("dispatch Python fixture: %v", err)
	}
	if len(response.Actions) != 0 {
		t.Fatalf("streaming Python fixture response = %#v", response)
	}
	waitFor(t, time.Second, func() bool {
		actionMu.Lock()
		defer actionMu.Unlock()
		return len(actions) == 1
	})
	actionMu.Lock()
	if actions[0].Kind != protocol.ActionEdit || actions[0].Text != "ok" {
		actionMu.Unlock()
		t.Fatalf("Python fixture actions = %#v", actions)
	}
	actionMu.Unlock()
	preSend := protocol.Action{ID: "send-1", Kind: protocol.ActionSend, ChatID: 2, Text: "hello"}
	preSendResponse, err := supervisor.Dispatch(context.Background(), protocol.DispatchRequest{
		PreSend: &protocol.PreSendRequest{Handler: "rewrite", Action: preSend},
	})
	if err != nil {
		t.Fatalf("dispatch Python pre-send fixture: %v", err)
	}
	if preSendResponse.PreSend == nil || preSendResponse.PreSend.Action.Text != "pre:hello" {
		t.Fatalf("Python pre-send response = %#v", preSendResponse)
	}
	eventResponse, err := supervisor.Dispatch(context.Background(), protocol.DispatchRequest{Event: &protocol.EventDelivery{
		SubscriptionID: "consume-note",
		Envelope: protocol.EventEnvelope{ID: "evt_00000000000000000000000000000001", Type: "fixture.note.created", Version: 1, Source: "fixture.go", AccountID: 7,
			OccurredUnixMS: time.Now().UnixMilli(), AcceptedUnixMS: time.Now().UnixMilli(), TraceID: "trace-fixture", ContentType: "application/json", Attempt: 1, Payload: []byte(`{"text":"hello"}`)},
	}})
	if err != nil {
		t.Fatalf("dispatch Python event fixture: %v", err)
	}
	if eventResponse.EventResult == nil || eventResponse.EventResult.Outcome != protocol.EventAcknowledged {
		t.Fatalf("Python event outcome = %#v", eventResponse.EventResult)
	}
	select {
	case published := <-events:
		if published.Type != "fixture.note.processed" || published.CausationID == "" || published.TraceID != "trace-fixture" {
			t.Fatalf("Python published event = %#v", published)
		}
	case <-time.After(time.Second):
		t.Fatal("Python event publication timed out")
	}
	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertRuntimeBaseEmpty(t, base)
}
