package main

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
	serenox "github.com/nihmadev/Serenox/sdk/go"
)

func TestMetadataMatchesManifestPolicy(t *testing.T) {
	handler := newAutopilotHandler(&fakeExit{})
	metadata, err := handler.Metadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := metadata.Validate(); err != nil {
		t.Fatal(err)
	}
	if metadata.ID != pluginID || metadata.Version != pluginVersion {
		t.Fatalf("identity=%s@%s", metadata.ID, metadata.Version)
	}
	wanted := map[protocol.Capability]bool{protocol.CapabilityCommands: true, protocol.CapabilityWatchers: true, protocol.CapabilityStorage: true, protocol.CapabilityJobs: true, protocol.CapabilityHTTP: true, protocol.CapabilityControlBot: true, protocol.CapabilityInline: true}
	for _, capability := range metadata.Capabilities {
		delete(wanted, capability)
	}
	if len(wanted) != 0 {
		t.Fatalf("missing capabilities: %v", wanted)
	}
	if len(metadata.Jobs) != 5 || len(metadata.Commands) != 2 || len(metadata.ControlCommands) != 1 {
		t.Fatalf("metadata surface is incomplete: %#v", metadata)
	}
	raw, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		ID, Version               string
		Capabilities, Permissions []string
		Network                   struct {
			Origins []struct{ Scheme, Host string }
		}
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	reported := make([]string, len(metadata.Capabilities))
	for index, capability := range metadata.Capabilities {
		reported[index] = string(capability)
	}
	slices.Sort(reported)
	slices.Sort(manifest.Capabilities)
	if manifest.ID != metadata.ID || manifest.Version != metadata.Version || !slices.Equal(reported, manifest.Capabilities) {
		t.Fatalf("manifest/metadata mismatch: %#v %#v", manifest, metadata)
	}
	for _, permission := range []string{"storage.read", "storage.write", "jobs.schedule", "telegram.send", "telegram.reply", "telegram.bot.write_ui", "network.http.read"} {
		if !slices.Contains(manifest.Permissions, permission) {
			t.Fatalf("missing permission %s", permission)
		}
	}
	if len(manifest.Network.Origins) != 1 || manifest.Network.Origins[0].Scheme != "https" || manifest.Network.Origins[0].Host != "example.com" {
		t.Fatalf("network policy=%#v", manifest.Network)
	}
}

func TestCommandsImportExportConfirmationAndRedaction(t *testing.T) {
	ctx := context.Background()
	host, _, _, _ := testHost()
	handler := newAutopilotHandler(&fakeExit{})
	handler.engine, _ = testEngine()
	workflow := validWorkflow()
	raw, _ := workflow.Canonical()
	message := protocol.Message{ID: 1, ChatID: 2, SenderID: 3}
	text, err := handler.flowCommand(ctx, host, "import "+string(raw), message)
	if err != nil || text != "Workflow imported." {
		t.Fatalf("text=%q err=%v", text, err)
	}
	exported, err := handler.flowCommand(ctx, host, "export "+workflow.ID, message)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(exported, "token") || !json.Valid([]byte(exported)) {
		t.Fatalf("unsafe export: %s", exported)
	}
	if _, err := handler.flowCommand(ctx, host, "delete "+workflow.ID, message); err == nil {
		t.Fatal("delete lacked confirmation")
	}
	if _, err := handler.flowCommand(ctx, host, "delete "+workflow.ID+" confirm", message); err != nil {
		t.Fatal(err)
	}
}

func TestConnectorCatalogAndAmbiguousWritePolicy(t *testing.T) {
	request, item, err := buildHTTPRequest(Step{Kind: "http", Connector: "demo-status", Method: "GET", Path: "/"}, connectorCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if request.URL != "https://example.com/" || !item.ReadOnly || request.MaxResponseBytes != 4096 {
		t.Fatalf("request=%#v connector=%#v", request, item)
	}
	for _, step := range []Step{{Connector: "unknown", Method: "GET", Path: "/"}, {Connector: "demo-status", Method: "POST", Path: "/"}, {Connector: "demo-status", Method: "GET", Path: "/../secret"}} {
		if _, _, err := buildHTTPRequest(step, connectorCatalog()); err == nil {
			t.Fatalf("unsafe connector accepted: %#v", step)
		}
	}
	ctx := context.Background()
	host, _, _, _ := testHost()
	engine, _ := testEngine()
	engine.httpEnabled = true
	workflow := Workflow{Version: definitionVersion, ID: "http-demo", Name: "HTTP", Enabled: true, Revision: 1, CreatedAt: engine.clock.Now(), UpdatedAt: engine.clock.Now(), Trigger: Trigger{Kind: "manual"}, Steps: []Step{{ID: "get", Kind: "http", Connector: "demo-status", Method: "GET", Path: "/"}, {ID: "done", Kind: "complete"}}}
	if err := engine.installWorkflow(ctx, host, workflow); err != nil {
		t.Fatal(err)
	}
	run, err := engine.trigger(ctx, host, workflow.ID, SourceSummary{ChatID: 1}, "")
	if err != nil || run.State != runCompleted {
		t.Fatalf("run=%s err=%v", run.State, err)
	}
}

func TestChaosRequiresPolicyAndDurableMarker(t *testing.T) {
	ctx := context.Background()
	host, store, _, _ := testHost()
	exit := &fakeExit{}
	host.Exit = exit
	handler := newAutopilotHandler(exit)
	handler.engine, _ = testEngine()
	message := protocol.Message{ChatID: 2, SenderID: 3}
	if _, err := handler.chaosCommand(ctx, host, []string{"exit-process", "confirm"}, message); err == nil {
		t.Fatal("disabled chaos executed")
	}
	handler.engine.configure(true, false, false)
	if _, err := handler.chaosCommand(ctx, host, []string{"exit-process", "wrong"}, message); err == nil {
		t.Fatal("bad confirmation executed")
	}
	if _, err := handler.chaosCommand(ctx, host, []string{"exit-process", "confirm"}, message); err != nil {
		t.Fatal(err)
	}
	if exit.code != 23 {
		t.Fatalf("exit code=%d", exit.code)
	}
	var state persistedState
	if err := json.Unmarshal(store.raw, &state); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, run := range state.Runs {
		if run.State == runRunning && run.WorkflowID == "chaos-recovery" {
			found = true
		}
	}
	if !found {
		t.Fatal("durable running marker missing before exit")
	}
}

func TestCallbackBindingExpiryAndReplay(t *testing.T) {
	ctx := context.Background()
	host, _, _, _ := testHost()
	engine, clock := testEngine()
	workflow := demoWorkflows(clock.Now())[1]
	if err := engine.installWorkflow(ctx, host, workflow); err != nil {
		t.Fatal(err)
	}
	run, err := engine.trigger(ctx, host, workflow.ID, SourceSummary{ChatID: 1, ActorID: 9}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.decide(ctx, host, run.ID, "approved", 8, ""); err == nil {
		t.Fatal("foreign actor approved")
	}
	clock.Advance(16 * 60 * 1e9)
	if _, err := engine.decide(ctx, host, run.ID, "approved", 9, ""); err == nil {
		t.Fatal("expired approval accepted")
	}
	if _, err := engine.decide(ctx, host, run.ID, "approved", 9, ""); err == nil {
		t.Fatal("replay accepted")
	}
}

var _ serenox.Handler = newAutopilotHandler(&fakeExit{})
