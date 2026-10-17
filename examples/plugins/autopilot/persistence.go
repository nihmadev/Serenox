package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func (e *Engine) ensureLoaded(ctx context.Context, host Host) error {
	if e.loaded {
		return e.loadErr
	}
	raw, found, err := host.Store.Get(ctx, storageNamespace, storageKey)
	if err != nil {
		e.loadErr = fmt.Errorf("load state: %w", err)
		e.loaded = true
		return e.loadErr
	}
	if found {
		e.state, err = decodeState(raw, e.policy)
		if err != nil {
			e.loadErr = err
			e.loaded = true
			return err
		}
	}
	now, recovered := e.clock.Now(), 0
	for id, run := range e.state.Runs {
		if run.State == runRunning || run.State == runPending {
			run.State, run.UpdatedAt, run.FinishedAt = runInterrupted, now, now
			run.LastErrorCode, run.LastError = "process_interrupted", "plugin process stopped while the run was active"
			e.state.Runs[id] = run
			recovered++
		}
		if run.State == runWaitingApproval && run.Approval != nil && !run.Approval.ExpiresAt.After(now) {
			run.State, run.UpdatedAt, run.FinishedAt = runFailed, now, now
			run.LastErrorCode, run.LastError = "approval_expired", "approval expired during downtime"
			e.state.Runs[id] = run
			recovered++
		}
		if run.State == runWaitingJob && e.reconcileWaitingJob(ctx, host, &run) {
			run.State, run.UpdatedAt, run.FinishedAt = runInterrupted, now, now
			run.LastErrorCode, run.LastError = "job_interrupted", "durable waiting job is absent, cancelled, or interrupted"
			e.state.Runs[id] = run
			recovered++
		}
	}
	if recovered > 0 {
		e.state.Counters.Recovery += uint64(recovered)
		e.state.Counters.Interrupted += uint64(recovered)
		e.state.addAudit(now, e.policy, nil, "recovery", fmt.Sprintf("reconciled %d interrupted or expired runs", recovered))
		if err := e.save(ctx, host); err != nil {
			e.loadErr = err
		}
	}
	e.loaded = true
	return e.loadErr
}

func (e *Engine) reconcileWaitingJob(ctx context.Context, host Host, run *Run) bool {
	if run.ExpectedJob == nil || run.ExpectedJob.PluginID == "" || host.Jobs == nil {
		return true
	}
	status, err := host.Jobs.Get(ctx, protocol.JobGetRequest{Identity: *run.ExpectedJob})
	return err != nil || status.State == protocol.JobStateCancelled || status.State == protocol.JobStateInterrupted
}

func (e *Engine) save(ctx context.Context, host Host) error {
	e.state.enforceRetention(e.policy)
	data, err := json.Marshal(e.state)
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("autopilot state exceeds host value limit")
	}
	return host.Store.Set(ctx, storageNamespace, storageKey, data)
}
