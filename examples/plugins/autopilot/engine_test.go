package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestDurableWaitDuplicateJobAndRecovery(t *testing.T) {
	ctx := context.Background()
	host, store, actions, jobs := testHost()
	engine, _ := testEngine()
	workflow := demoWorkflows(engine.clock.Now())[0]
	if err := engine.installWorkflow(ctx, host, workflow); err != nil {
		t.Fatal(err)
	}
	run, err := engine.trigger(ctx, host, workflow.ID, SourceSummary{Kind: "manual", ChatID: 7, ActorID: 9}, "")
	if err != nil {
		t.Fatal(err)
	}
	if run.State != runWaitingJob || len(jobs.specs) < 1 {
		t.Fatalf("run=%#v jobs=%d", run, len(jobs.specs))
	}
	spec := jobs.specs[len(jobs.specs)-1]
	inv := protocol.JobInvocation{Identity: spec.Identity, Handler: spec.Handler, Payload: spec.Payload, RunID: "host-run"}
	run, err = engine.handleJob(ctx, host, inv)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != runCompleted || len(actions.actions) != 2 {
		t.Fatalf("run=%s actions=%d", run.State, len(actions.actions))
	}
	if _, err = engine.handleJob(ctx, host, inv); err == nil {
		t.Fatal("duplicate job advanced run")
	}
	var persisted persistedState
	if err = json.Unmarshal(store.raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Runs[run.ID].State != runCompleted {
		t.Fatal("completion was not durable")
	}
}

func TestRestartMarksActiveRunInterrupted(t *testing.T) {
	ctx := context.Background()
	host, store, _, _ := testHost()
	engine, _ := testEngine()
	run, err := engine.prepareChaosRunLocked()
	if err != nil {
		t.Fatal(err)
	}
	state := emptyState()
	state.Runs[run.ID] = run
	store.raw, _ = json.Marshal(state)
	store.found = true
	restarted, _ := testEngine()
	loaded, err := restarted.status(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Runs[run.ID].State != runInterrupted {
		t.Fatalf("state=%s", loaded.Runs[run.ID].State)
	}
	if loaded.Counters.Recovery != 1 {
		t.Fatalf("recovery=%d", loaded.Counters.Recovery)
	}
}

func TestConcurrentApproveCancelHasSingleWinner(t *testing.T) {
	ctx := context.Background()
	host, _, _, _ := testHost()
	engine, _ := testEngine()
	workflow := demoWorkflows(engine.clock.Now())[1]
	if err := engine.installWorkflow(ctx, host, workflow); err != nil {
		t.Fatal(err)
	}
	run, err := engine.trigger(ctx, host, workflow.ID, SourceSummary{Kind: "manual", ChatID: 7, ActorID: 9}, "")
	if err != nil || run.State != runWaitingApproval {
		t.Fatalf("run=%s err=%v", run.State, err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan error, 2)
	go func() { defer wg.Done(); _, err := engine.decide(ctx, host, run.ID, "approved", 9, ""); results <- err }()
	go func() { defer wg.Done(); _, err := engine.cancel(ctx, host, run.ID); results <- err }()
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("successful mutations=%d", success)
	}
	state, _ := engine.status(ctx, host)
	if !terminalRun(state.Runs[run.ID].State) {
		t.Fatalf("state=%s", state.Runs[run.ID].State)
	}
}

func TestStorageFailureFailsClosed(t *testing.T) {
	ctx := context.Background()
	host, store, _, _ := testHost()
	engine, _ := testEngine()
	store.getErr = errCommit
	if _, err := engine.status(ctx, host); err == nil {
		t.Fatal("load failure ignored")
	}
	engine, _ = testEngine()
	store.getErr = nil
	store.setErr = errCommit
	if err := engine.installWorkflow(ctx, host, validWorkflow()); err == nil {
		t.Fatal("commit failure ignored")
	}
}

func TestWatcherMatchingCooldownDedupAndSelfPrevention(t *testing.T) {
	ctx := context.Background()
	host, _, _, _ := testHost()
	engine, clock := testEngine()
	workflow := demoWorkflows(clock.Now())[2]
	workflow.Limits.CooldownMS = 1000
	if err := engine.installWorkflow(ctx, host, workflow); err != nil {
		t.Fatal(err)
	}
	update := protocol.Update{ID: "u1", Kind: protocol.UpdateMessage, Message: &protocol.Message{ID: 1, ChatID: 2, SenderID: 3, Text: "autopilot demo"}}
	runs, err := engine.handleWatcher(ctx, host, update)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%d err=%v", len(runs), err)
	}
	runs, _ = engine.handleWatcher(ctx, host, update)
	if len(runs) != 0 {
		t.Fatal("duplicate update triggered")
	}
	update.ID = "u2"
	update.Message.ID = 2
	runs, _ = engine.handleWatcher(ctx, host, update)
	if len(runs) != 0 {
		t.Fatal("cooldown ignored")
	}
	clock.Advance(time.Second)
	update.ID = "u3"
	update.Message.ID = 3
	update.Message.Text = ".autopilot demo"
	runs, _ = engine.handleWatcher(ctx, host, update)
	if len(runs) != 0 {
		t.Fatal("command feedback loop admitted")
	}
}

func TestActionUnknownIsNotRetrySafe(t *testing.T) {
	ctx := context.Background()
	host, _, actions, _ := testHost()
	engine, _ := testEngine()
	workflow := Workflow{Version: definitionVersion, ID: "notify", Name: "Notify", Enabled: true, Revision: 1, CreatedAt: engine.clock.Now(), UpdatedAt: engine.clock.Now(), Trigger: Trigger{Kind: "manual"}, Steps: []Step{{ID: "send", Kind: "notify", Text: "hello"}, {ID: "done", Kind: "complete"}}}
	actions.results = []protocol.ActionResult{{ActionID: "x", Operation: "send", Status: protocol.ActionResultOutcomeUnknown, OutcomeUnknown: true, Error: &protocol.Error{Code: protocol.ErrorOutcomeUnknown, Message: "timeout"}}}
	if err := engine.installWorkflow(ctx, host, workflow); err != nil {
		t.Fatal(err)
	}
	run, err := engine.trigger(ctx, host, workflow.ID, SourceSummary{ChatID: 1, ActorID: 2}, "")
	if err == nil || run.State != runFailed {
		t.Fatalf("run=%s err=%v", run.State, err)
	}
	if _, err := engine.retry(ctx, host, run.ID); err == nil {
		t.Fatal("ambiguous action retried")
	}
}
