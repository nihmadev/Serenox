package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func (e *Engine) handleWatcher(ctx context.Context, host Host, update protocol.Update) ([]Run, error) {
	e.mu.Lock()
	if err := e.ensureLoaded(ctx, host); err != nil {
		e.mu.Unlock()
		return nil, err
	}
	if update.Message == nil || e.state.Paused {
		e.mu.Unlock()
		return nil, nil
	}
	message := update.Message
	dedupKey := fmt.Sprintf("%d/%d", message.ChatID, message.ID)
	if _, duplicate := e.state.Dedup[dedupKey]; duplicate {
		e.state.Counters.TriggerRejects++
		e.mu.Unlock()
		return nil, nil
	}
	ids := sortedWorkflowIDs(e.state.Workflows)
	selected := make([]string, 0)
	for _, id := range ids {
		workflow := e.state.Workflows[id]
		matched, _ := triggerMatches(workflow, update)
		if !matched {
			e.state.Counters.TriggerRejects++
			continue
		}
		if until := e.state.Cooldown[id]; until.After(e.clock.Now()) {
			e.state.Counters.TriggerRejects++
			continue
		}
		selected = append(selected, id)
		e.state.Counters.TriggerMatches++
		if workflow.Limits.CooldownMS > 0 {
			e.state.Cooldown[id] = e.clock.Now().Add(time.Duration(workflow.Limits.CooldownMS) * time.Millisecond)
		}
	}
	e.state.Dedup[dedupKey] = e.clock.Now()
	e.trimDedup()
	_ = e.save(ctx, host)
	e.mu.Unlock()
	runs := make([]Run, 0, len(selected))
	for _, id := range selected {
		run, err := e.trigger(ctx, host, id, SourceSummary{Kind: "message", UpdateID: update.ID, ChatID: message.ChatID, MessageID: message.ID, ActorID: message.SenderID}, message.Text)
		if err != nil {
			return runs, err
		}
		runs = append(runs, run)
	}
	return runs, nil
}

func (e *Engine) trimDedup() {
	for len(e.state.Dedup) > 256 {
		var oldestKey string
		var oldest int64
		for key, at := range e.state.Dedup {
			if oldestKey == "" || at.UnixNano() < oldest {
				oldestKey, oldest = key, at.UnixNano()
			}
		}
		delete(e.state.Dedup, oldestKey)
	}
}

func (e *Engine) cancel(ctx context.Context, host Host, runID string) (Run, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureLoaded(ctx, host); err != nil {
		return Run{}, err
	}
	run, ok := e.state.Runs[runID]
	if !ok {
		return Run{}, errors.New("run not found")
	}
	if terminalRun(run.State) {
		return cloneRun(run), errors.New("run is already terminal")
	}
	if run.ExpectedJob != nil && run.ExpectedJob.PluginID != "" && host.Jobs != nil {
		_ = host.Jobs.Delete(ctx, protocol.JobIdentityRequest{Identity: *run.ExpectedJob})
	}
	e.finish(&run, runCancelled, "owner_cancelled", "cancelled by owner")
	e.state.Counters.Cancelled++
	e.state.Runs[runID] = run
	return cloneRun(run), e.save(ctx, host)
}

func (e *Engine) decide(ctx context.Context, host Host, runID, decision string, actorID int64, token string) (Run, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureLoaded(ctx, host); err != nil {
		return Run{}, err
	}
	run, ok := e.state.Runs[runID]
	if !ok || run.State != runWaitingApproval || run.Approval == nil {
		e.state.Counters.ApprovalReplays++
		return Run{}, errors.New("approval is stale")
	}
	if decision != "approved" && decision != "rejected" {
		return Run{}, errors.New("decision is invalid")
	}
	if run.Approval.ActorID != 0 && actorID != run.Approval.ActorID {
		return Run{}, errors.New("approval actor is not authorized")
	}
	if !run.Approval.ExpiresAt.After(e.clock.Now()) {
		return e.failLocked(ctx, host, run, "approval_expired", "approval expired")
	}
	if token != "" {
		digest := sha256.Sum256([]byte(token))
		if hex.EncodeToString(digest[:]) != run.Approval.TokenHash {
			return Run{}, errors.New("approval token is invalid")
		}
	}
	if run.Approval.Used {
		e.state.Counters.ApprovalReplays++
		return Run{}, errors.New("approval was already used")
	}
	run.Approval.Used, run.Approval.Decision = true, decision
	step := run.Definition.Steps[run.Step]
	if decision == "approved" {
		run.Step = nextIndex(run.Step, step.Next)
	} else {
		run.Step = nextIndex(run.Step, step.OnFailure)
	}
	run.State, run.UpdatedAt = runRunning, e.clock.Now()
	e.state.Runs[run.ID] = run
	e.state.addAudit(e.clock.Now(), e.policy, &run, "approval", decision)
	if err := e.save(ctx, host); err != nil {
		return Run{}, err
	}
	return e.executeLocked(ctx, host, run.ID)
}

func (e *Engine) decideToken(ctx context.Context, host Host, token, decision string, actor int64) (Run, error) {
	e.mu.Lock()
	if err := e.ensureLoaded(ctx, host); err != nil {
		e.mu.Unlock()
		return Run{}, err
	}
	digest := sha256.Sum256([]byte(token))
	wanted := hex.EncodeToString(digest[:])
	runID := ""
	for id, run := range e.state.Runs {
		if run.State == runWaitingApproval && run.Approval != nil && run.Approval.TokenHash == wanted {
			runID = id
			break
		}
	}
	e.mu.Unlock()
	if runID == "" {
		return Run{}, errors.New("approval is stale")
	}
	return e.decide(ctx, host, runID, decision, actor, token)
}

func (e *Engine) retry(ctx context.Context, host Host, runID string) (Run, error) {
	e.mu.Lock()
	if err := e.ensureLoaded(ctx, host); err != nil {
		e.mu.Unlock()
		return Run{}, err
	}
	prior, ok := e.state.Runs[runID]
	e.mu.Unlock()
	if !ok || !terminalRun(prior.State) {
		return Run{}, errors.New("only a terminal run can be retried")
	}
	if prior.LastResult == string(protocol.ActionResultOutcomeUnknown) || (!prior.RetrySafe && prior.LastOperation != "") {
		return Run{}, errors.New("retry is unsafe because the previous mutation may have succeeded")
	}
	return e.trigger(ctx, host, prior.WorkflowID, prior.Source, "")
}

func (e *Engine) handleJob(ctx context.Context, host Host, invocation protocol.JobInvocation) (Run, error) {
	var payload struct {
		RunID      string `json:"run_id"`
		WorkflowID string `json:"workflow_id"`
		Revision   uint64 `json:"revision"`
		Step       int    `json:"step"`
	}
	if err := strictDecode(invocation.Payload, &payload); err != nil {
		return Run{}, fmt.Errorf("invalid minimal job payload: %w", err)
	}
	if invocation.Handler == "workflow_schedule" {
		return e.trigger(ctx, host, payload.WorkflowID, SourceSummary{Kind: "schedule"}, "")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureLoaded(ctx, host); err != nil {
		return Run{}, err
	}
	run, ok := e.state.Runs[payload.RunID]
	if !ok || run.Revision != payload.Revision {
		e.state.Counters.StaleJobs++
		return Run{}, errors.New("stale job delivery")
	}
	switch invocation.Handler {
	case "resume_wait":
		if run.State != runWaitingJob || run.ExpectedJob == nil || run.ExpectedJob.ID != invocation.Identity.ID || run.Step != payload.Step {
			e.state.Counters.StaleJobs++
			return Run{}, errors.New("duplicate or stale wait delivery")
		}
		run.State, run.ExpectedJob = runRunning, nil
		run.Step = nextIndex(run.Step, run.Definition.Steps[run.Step].Next)
		run.UpdatedAt = e.clock.Now()
		e.state.Runs[run.ID] = run
		e.state.Counters.JobResumes++
		if err := e.save(ctx, host); err != nil {
			return Run{}, err
		}
		return e.executeLocked(ctx, host, run.ID)
	case "approval_expiry":
		if run.State != runWaitingApproval {
			e.state.Counters.StaleJobs++
			return cloneRun(run), nil
		}
		return e.failLocked(ctx, host, run, "approval_expired", "approval expired")
	case "cleanup":
		e.cleanupLocked()
		return Run{}, e.save(ctx, host)
	case "chaos_failure":
		e.state.Counters.Chaos++
		e.state.addAudit(e.clock.Now(), e.policy, &run, "chaos", "intentional job failure")
		_ = e.save(ctx, host)
		return cloneRun(run), errors.New("intentional Autopilot chaos job failure")
	default:
		return Run{}, errors.New("unknown job handler")
	}
}

func (e *Engine) cleanupLocked() {
	now := e.clock.Now()
	for key, at := range e.state.Dedup {
		if now.Sub(at) > 24*time.Hour {
			delete(e.state.Dedup, key)
		}
	}
	e.state.enforceRetention(e.policy)
	e.state.Counters.Cleanup++
}

func (e *Engine) importJSON(ctx context.Context, host Host, raw string) error {
	workflow, err := ParseWorkflow([]byte(raw), e.policy)
	if err != nil {
		return err
	}
	return e.installWorkflow(ctx, host, workflow)
}
func (e *Engine) exportJSON(ctx context.Context, host Host, id string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureLoaded(ctx, host); err != nil {
		return "", err
	}
	workflow, ok := e.state.Workflows[id]
	if !ok {
		return "", errors.New("workflow not found")
	}
	raw, err := workflow.Canonical()
	return string(raw), err
}
func (e *Engine) setPaused(ctx context.Context, host Host, paused bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureLoaded(ctx, host); err != nil {
		return err
	}
	e.state.Paused = paused
	return e.save(ctx, host)
}

func (e *Engine) ensureCleanupJob(ctx context.Context, host Host) error {
	if host.Jobs == nil {
		return errors.New("jobs service is unavailable")
	}
	spec := baseJobSpec("ap-cleanup", "cleanup", "autopilot.cleanup/v1", []byte(`{}`))
	spec.Description = "Autopilot bounded retention cleanup"
	spec.Schedule = protocol.JobSchedule{Kind: protocol.JobScheduleFixedDelay, Interval: &protocol.IntervalSchedule{IntervalMS: int64((24 * time.Hour) / time.Millisecond)}}
	_, err := host.Jobs.Create(ctx, spec)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return err
	}
	return nil
}
func (e *Engine) deleteWorkflow(ctx context.Context, host Host, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureLoaded(ctx, host); err != nil {
		return err
	}
	if _, ok := e.state.Workflows[id]; !ok {
		return errors.New("workflow not found")
	}
	for _, run := range e.state.Runs {
		if run.WorkflowID == id && !terminalRun(run.State) {
			return errors.New("workflow has active runs")
		}
	}
	delete(e.state.Workflows, id)
	return e.save(ctx, host)
}
func (e *Engine) setEnabled(ctx context.Context, host Host, id string, enabled bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureLoaded(ctx, host); err != nil {
		return err
	}
	workflow, ok := e.state.Workflows[id]
	if !ok {
		return errors.New("workflow not found")
	}
	workflow.Enabled, workflow.Revision, workflow.UpdatedAt = enabled, workflow.Revision+1, e.clock.Now()
	e.state.Workflows[id] = workflow
	return e.save(ctx, host)
}

func (e *Engine) status(ctx context.Context, host Host) (persistedState, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureLoaded(ctx, host); err != nil {
		return persistedState{}, err
	}
	raw, _ := json.Marshal(e.state)
	var copy persistedState
	_ = json.Unmarshal(raw, &copy)
	return copy, nil
}
func parseApprovalCallback(data string) (string, string, bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != "ap" || (parts[2] != "a" && parts[2] != "r") {
		return "", "", false
	}
	decision := "approved"
	if parts[2] == "r" {
		decision = "rejected"
	}
	return parts[1], decision, true
}
