package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

const storageNamespace, storageKey = "autopilot", "state-v1"

type Clock interface{ Now() time.Time }
type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

type IDSource interface{ New(string) (string, error) }
type randomIDs struct{}

func (randomIDs) New(prefix string) (string, error) {
	raw := make([]byte, 9)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw), nil
}

type Engine struct {
	mu                                        sync.Mutex
	policy                                    Policy
	clock                                     Clock
	ids                                       IDSource
	state                                     persistedState
	loaded                                    bool
	loadErr                                   error
	chaosEnabled, controlEnabled, httpEnabled bool
	connectors                                map[string]connector
}

func newEngine(clock Clock, ids IDSource) *Engine {
	if clock == nil {
		clock = realClock{}
	}
	if ids == nil {
		ids = randomIDs{}
	}
	return &Engine{policy: DefaultPolicy(), clock: clock, ids: ids, state: emptyState(), connectors: connectorCatalog()}
}

func (e *Engine) configure(chaos, control, http bool) {
	e.mu.Lock()
	e.chaosEnabled, e.controlEnabled, e.httpEnabled = chaos, control, http
	e.mu.Unlock()
}

func (e *Engine) installWorkflow(ctx context.Context, host Host, workflow Workflow) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureLoaded(ctx, host); err != nil {
		return err
	}
	if err := workflow.Validate(e.policy); err != nil {
		return err
	}
	if _, exists := e.state.Workflows[workflow.ID]; !exists && len(e.state.Workflows) >= e.policy.MaxWorkflows {
		return errors.New("workflow capacity reached")
	}
	e.state.Workflows[workflow.ID] = workflow
	e.state.addAudit(e.clock.Now(), e.policy, nil, "workflow", "installed "+workflow.ID)
	if err := e.save(ctx, host); err != nil {
		return err
	}
	if spec, scheduled, err := workflowJobSpec(workflow); err != nil {
		return err
	} else if scheduled {
		if host.Jobs == nil {
			return errors.New("jobs service is unavailable")
		}
		_, err = host.Jobs.Create(ctx, spec)
		if err != nil && !strings.Contains(err.Error(), "already exists") {
			return err
		}
	}
	return nil
}

func (e *Engine) trigger(ctx context.Context, host Host, workflowID string, source SourceSummary, messageText string) (Run, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureLoaded(ctx, host); err != nil {
		return Run{}, err
	}
	if e.state.Paused {
		return Run{}, errors.New("autopilot is paused")
	}
	workflow, ok := e.state.Workflows[workflowID]
	if !ok {
		return Run{}, errors.New("workflow not found")
	}
	if !workflow.Enabled {
		return Run{}, errors.New("workflow is disabled")
	}
	active := 0
	for _, run := range e.state.Runs {
		if run.WorkflowID == workflowID && !terminalRun(run.State) {
			active++
		}
	}
	limit := workflow.Limits.Concurrency
	if limit == 0 {
		limit = 1
	}
	if active >= limit {
		return Run{}, errors.New("workflow concurrency limit reached")
	}
	id, err := e.ids.New("r-")
	if err != nil {
		return Run{}, err
	}
	digest, err := workflow.Digest()
	if err != nil {
		return Run{}, err
	}
	now := e.clock.Now()
	run := Run{ID: id, WorkflowID: workflow.ID, Revision: workflow.Revision, Digest: digest, State: runRunning, Context: map[string]json.RawMessage{}, Source: source, CreatedAt: now, UpdatedAt: now, StartedAt: now, Definition: workflow}
	if messageText != "" {
		raw, _ := json.Marshal(messageText)
		run.Context["message.text"] = raw
	}
	e.state.Runs[id] = run
	e.state.Counters.RunsCreated++
	e.state.addAudit(now, e.policy, &run, "run", "created")
	if err := e.save(ctx, host); err != nil {
		delete(e.state.Runs, id)
		return Run{}, err
	}
	return e.executeLocked(ctx, host, id)
}

func (e *Engine) executeLocked(ctx context.Context, host Host, runID string) (Run, error) {
	for {
		run, ok := e.state.Runs[runID]
		if !ok {
			return Run{}, errors.New("run not found")
		}
		if run.State != runRunning {
			return cloneRun(run), nil
		}
		if run.Transitions >= e.policy.MaxTransitions {
			return e.failLocked(ctx, host, run, "transition_budget", "transition budget exhausted")
		}
		if run.Step < 0 || run.Step >= len(run.Definition.Steps) {
			return e.failLocked(ctx, host, run, "invalid_step", "current step is outside definition")
		}
		step := run.Definition.Steps[run.Step]
		run.Transitions++
		run.UpdatedAt = e.clock.Now()
		e.state.Counters.Transitions++
		e.state.Runs[runID] = run
		if err := e.save(ctx, host); err != nil {
			return cloneRun(run), err
		}
		next, wait, err := e.performStep(ctx, host, &run, step)
		if err != nil {
			return e.failLocked(ctx, host, run, "step_failed", safeError(err))
		}
		if wait {
			e.state.Runs[runID] = run
			if err := e.save(ctx, host); err != nil {
				return cloneRun(run), err
			}
			return cloneRun(run), nil
		}
		if terminalRun(run.State) {
			e.state.Runs[runID] = run
			_ = e.save(ctx, host)
			return cloneRun(run), nil
		}
		run.Step = nextIndex(run.Step, next)
		run.UpdatedAt = e.clock.Now()
		e.state.Runs[runID] = run
	}
}

func nextIndex(current, selected int) int {
	if selected == 0 {
		return current + 1
	}
	return selected
}

func (e *Engine) performStep(ctx context.Context, host Host, run *Run, step Step) (int, bool, error) {
	switch step.Kind {
	case "set":
		run.Context["vars."+step.Key] = append(json.RawMessage(nil), step.Value...)
		return step.Next, false, e.checkContext(*run)
	case "condition":
		matched, err := evaluateCondition(step.Condition, run.Context)
		if err != nil {
			return 0, false, err
		}
		if matched {
			return step.Condition.True, false, nil
		}
		return step.Condition.False, false, nil
	case "notify":
		return step.Next, false, e.notify(ctx, host, run, step)
	case "wait":
		return 0, true, e.wait(ctx, host, run, step)
	case "approval":
		return 0, true, e.requestApproval(ctx, host, run, step)
	case "http":
		return step.Next, false, e.callHTTP(ctx, host, run, step)
	case "complete":
		e.finish(run, runCompleted, "", step.Text)
		e.state.Counters.Completed++
		return 0, false, nil
	case "fail":
		e.finish(run, runFailed, "explicit_failure", step.Text)
		e.state.Counters.Failed++
		return 0, false, nil
	default:
		return 0, false, fmt.Errorf("unsupported step %q", step.Kind)
	}
}

func (e *Engine) notify(ctx context.Context, host Host, run *Run, step Step) error {
	if host.Actions == nil {
		return errors.New("typed action service is unavailable")
	}
	text, err := renderTemplate(step.Text, *run, e.policy)
	if err != nil {
		return err
	}
	op := fmt.Sprintf("%s-%d-a1", run.ID, run.Step)
	run.LastOperation, run.RetrySafe = op, false
	result, err := host.Actions.Apply(ctx, protocol.Action{ID: op, IdempotencyKey: op, Kind: protocol.ActionSend, ChatID: run.Source.ChatID, Text: text})
	if err != nil {
		return err
	}
	run.LastResult = string(result.Status)
	switch result.Status {
	case protocol.ActionResultSuccess:
		e.state.Counters.ActionSuccess++
		if result.MessageID != 0 {
			e.state.Dedup[fmt.Sprintf("%d/%d", run.Source.ChatID, result.MessageID)] = e.clock.Now()
		}
		return nil
	case protocol.ActionResultOutcomeUnknown:
		e.state.Counters.ActionUnknown++
		return errors.New("telegram mutation outcome is unknown; automatic retry is unsafe")
	default:
		e.state.Counters.ActionFailure++
		if result.Error != nil {
			return errors.New(result.Error.Message)
		}
		return errors.New("telegram action failed")
	}
}

func (e *Engine) wait(ctx context.Context, host Host, run *Run, step Step) error {
	if host.Jobs == nil {
		return errors.New("jobs service is unavailable")
	}
	jobID := "ap-w-" + run.ID
	payload, _ := json.Marshal(map[string]any{"run_id": run.ID, "revision": run.Revision, "step": run.Step})
	spec := baseJobSpec(jobID, "resume_wait", "autopilot.resume/v1", payload)
	spec.Schedule = protocol.JobSchedule{Kind: protocol.JobScheduleOneShot, OneShot: &protocol.OneShotSchedule{RunAtUnixMS: e.clock.Now().Add(time.Duration(step.DelayMS) * time.Millisecond).UnixMilli()}}
	run.State, run.ExpectedJob = runWaitingJob, &protocol.JobIdentity{ID: jobID}
	e.state.Runs[run.ID] = *run
	if err := e.save(ctx, host); err != nil {
		return err
	}
	status, err := host.Jobs.Create(ctx, spec)
	if err != nil {
		return err
	}
	run.ExpectedJob = &status.Identity
	return nil
}

func (e *Engine) requestApproval(ctx context.Context, host Host, run *Run, step Step) error {
	token, err := e.ids.New("p-")
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(token))
	expires := e.clock.Now().Add(15 * time.Minute)
	run.State = runWaitingApproval
	run.Approval = &Approval{TokenHash: hex.EncodeToString(digest[:]), ActorID: run.Source.ActorID, ExpiresAt: expires}
	e.state.Counters.Approvals++
	if host.Jobs == nil {
		return errors.New("jobs service is unavailable for approval expiry")
	}
	payload, _ := json.Marshal(map[string]any{"run_id": run.ID, "revision": run.Revision})
	spec := baseJobSpec("ap-a-"+run.ID, "approval_expiry", "autopilot.approval-expiry/v1", payload)
	spec.Schedule = protocol.JobSchedule{Kind: protocol.JobScheduleOneShot, OneShot: &protocol.OneShotSchedule{RunAtUnixMS: expires.UnixMilli()}}
	if _, err := host.Jobs.Create(ctx, spec); err != nil {
		return fmt.Errorf("schedule approval expiry: %w", err)
	}
	if e.controlEnabled && host.Actions != nil && run.Source.ActorID != 0 {
		text, renderErr := renderTemplate(step.Text, *run, e.policy)
		if renderErr != nil {
			return renderErr
		}
		action := protocol.Action{ID: run.ID + "-approval", Kind: protocol.ActionBotSend, ResponseChannel: protocol.ResponseChannelControlBot, ChatID: run.Source.ActorID, Text: text, Buttons: [][]protocol.InlineButton{{{Text: "Approve", CallbackData: "ap:" + token + ":a"}, {Text: "Reject", CallbackData: "ap:" + token + ":r"}}}}
		result, sendErr := host.Actions.Apply(ctx, action)
		if sendErr != nil || result.Status != protocol.ActionResultSuccess {
			e.state.addAudit(e.clock.Now(), e.policy, run, "approval", "control bot unavailable; use .flow approve or reject")
		}
	}
	return nil
}

func (e *Engine) callHTTP(ctx context.Context, host Host, run *Run, step Step) error {
	if !e.httpEnabled || host.HTTP == nil {
		return errors.New("HTTP connectors are unavailable")
	}
	request, item, err := buildHTTPRequest(step, e.connectors)
	if err != nil {
		return err
	}
	response, err := host.HTTP.Request(ctx, request)
	if item.ReadOnly {
		e.state.Counters.HTTPRead++
	} else {
		e.state.Counters.HTTPWrite++
	}
	if err != nil {
		if !item.ReadOnly {
			run.RetrySafe = false
			return errors.New("HTTP write outcome may be unknown; inspect before retry")
		}
		return err
	}
	status, _ := json.Marshal(response.Status)
	run.Context["http.status"] = status
	if response.Rejected || response.Status < 200 || response.Status >= 300 {
		return fmt.Errorf("connector returned status %d", response.Status)
	}
	return e.checkContext(*run)
}

func evaluateCondition(condition *Condition, values map[string]json.RawMessage) (bool, error) {
	if condition == nil {
		return false, errors.New("condition is missing")
	}
	raw, exists := values[condition.Field]
	if condition.Op == "exists" {
		return exists, nil
	}
	if !exists {
		return condition.Op == "neq", nil
	}
	equal := string(raw) == string(condition.Value)
	if condition.Op == "eq" {
		return equal, nil
	}
	if condition.Op == "neq" {
		return !equal, nil
	}
	return false, errors.New("condition operator is unsupported")
}

func (e *Engine) checkContext(run Run) error {
	encoded, _ := json.Marshal(run.Context)
	if len(encoded) > e.policy.MaxContextBytes {
		return errors.New("run context limit exceeded")
	}
	return nil
}

func (e *Engine) finish(run *Run, state RunState, code, summary string) {
	now := e.clock.Now()
	run.State, run.UpdatedAt, run.FinishedAt = state, now, now
	run.LastErrorCode, run.LastError = code, safeSummary(summary)
	e.state.addAudit(now, e.policy, run, string(state), safeSummary(summary))
}
func (e *Engine) failLocked(ctx context.Context, host Host, run Run, code, summary string) (Run, error) {
	e.finish(&run, runFailed, code, summary)
	e.state.Counters.Failed++
	e.state.Runs[run.ID] = run
	err := e.save(ctx, host)
	if err != nil {
		return cloneRun(run), err
	}
	return cloneRun(run), errors.New(summary)
}
func safeSummary(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if len(value) > 160 {
		value = value[:160]
	}
	return value
}
func safeError(err error) string {
	if err == nil {
		return ""
	}
	return safeSummary(err.Error())
}
