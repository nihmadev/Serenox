package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

const stateSchemaVersion = 1

type RunState string

const (
	runPending         RunState = "pending"
	runRunning         RunState = "running"
	runWaitingJob      RunState = "waiting_job"
	runWaitingApproval RunState = "waiting_approval"
	runCompleted       RunState = "completed"
	runFailed          RunState = "failed"
	runCancelled       RunState = "cancelled"
	runInterrupted     RunState = "interrupted"
)

type Run struct {
	ID            string                     `json:"id"`
	WorkflowID    string                     `json:"workflow_id"`
	Revision      uint64                     `json:"revision"`
	Digest        string                     `json:"digest"`
	State         RunState                   `json:"state"`
	Step          int                        `json:"step"`
	Transitions   int                        `json:"transitions"`
	Context       map[string]json.RawMessage `json:"context,omitempty"`
	Source        SourceSummary              `json:"source"`
	CreatedAt     time.Time                  `json:"created_at"`
	UpdatedAt     time.Time                  `json:"updated_at"`
	StartedAt     time.Time                  `json:"started_at,omitempty"`
	FinishedAt    time.Time                  `json:"finished_at,omitempty"`
	ExpectedJob   *protocol.JobIdentity      `json:"expected_job,omitempty"`
	Approval      *Approval                  `json:"approval,omitempty"`
	LastErrorCode string                     `json:"last_error_code,omitempty"`
	LastError     string                     `json:"last_error,omitempty"`
	LastOperation string                     `json:"last_operation,omitempty"`
	LastResult    string                     `json:"last_result,omitempty"`
	RetrySafe     bool                       `json:"retry_safe,omitempty"`
	Definition    Workflow                   `json:"definition"`
}

type SourceSummary struct {
	Kind, UpdateID             string
	ChatID, MessageID, ActorID int64
}

func (s SourceSummary) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Kind      string `json:"kind"`
		UpdateID  string `json:"update_id,omitempty"`
		ChatID    int64  `json:"chat_id,omitempty"`
		MessageID int64  `json:"message_id,omitempty"`
		ActorID   int64  `json:"actor_id,omitempty"`
	}{s.Kind, s.UpdateID, s.ChatID, s.MessageID, s.ActorID})
}

func (s *SourceSummary) UnmarshalJSON(data []byte) error {
	var value struct {
		Kind      string `json:"kind"`
		UpdateID  string `json:"update_id,omitempty"`
		ChatID    int64  `json:"chat_id,omitempty"`
		MessageID int64  `json:"message_id,omitempty"`
		ActorID   int64  `json:"actor_id,omitempty"`
	}
	if err := strictDecode(data, &value); err != nil {
		return err
	}
	s.Kind, s.UpdateID, s.ChatID, s.MessageID, s.ActorID = value.Kind, value.UpdateID, value.ChatID, value.MessageID, value.ActorID
	return nil
}

type Approval struct {
	TokenHash string    `json:"token_hash"`
	ActorID   int64     `json:"actor_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Decision  string    `json:"decision,omitempty"`
	Used      bool      `json:"used,omitempty"`
}

type AuditEvent struct {
	At         time.Time `json:"at"`
	RunID      string    `json:"run_id"`
	WorkflowID string    `json:"workflow_id"`
	Category   string    `json:"category"`
	Summary    string    `json:"summary"`
}

type Counters struct {
	RunsCreated, Transitions, TriggerMatches, TriggerRejects uint64
	Completed, Failed, Cancelled, Interrupted                uint64
	JobResumes, StaleJobs, Approvals, ApprovalReplays        uint64
	ActionSuccess, ActionFailure, ActionUnknown              uint64
	HTTPRead, HTTPWrite, Recovery, Cleanup, Chaos            uint64
}

type persistedState struct {
	Schema    int                  `json:"schema"`
	Paused    bool                 `json:"paused"`
	Workflows map[string]Workflow  `json:"workflows"`
	Runs      map[string]Run       `json:"runs"`
	Audit     []AuditEvent         `json:"audit"`
	Dedup     map[string]time.Time `json:"dedup,omitempty"`
	Cooldown  map[string]time.Time `json:"cooldown,omitempty"`
	Counters  Counters             `json:"counters"`
}

func emptyState() persistedState {
	return persistedState{Schema: stateSchemaVersion, Workflows: map[string]Workflow{}, Runs: map[string]Run{}, Dedup: map[string]time.Time{}, Cooldown: map[string]time.Time{}}
}

func decodeState(data []byte, policy Policy) (persistedState, error) {
	if len(data) == 0 || len(data) > 1<<20 {
		return persistedState{}, fmt.Errorf("autopilot state is empty or oversized")
	}
	var state persistedState
	if err := strictDecode(data, &state); err != nil {
		return persistedState{}, fmt.Errorf("decode autopilot state: %w", err)
	}
	if err := state.validate(policy); err != nil {
		return persistedState{}, err
	}
	return state, nil
}

func (s *persistedState) validate(policy Policy) error {
	if s.Schema != stateSchemaVersion {
		return fmt.Errorf("unsupported autopilot state schema %d; restore a compatible package or export state", s.Schema)
	}
	if s.Workflows == nil || s.Runs == nil {
		return fmt.Errorf("autopilot state indexes are missing")
	}
	if len(s.Workflows) > policy.MaxWorkflows || len(s.Runs) > policy.MaxRuns || len(s.Audit) > policy.MaxAudit {
		return fmt.Errorf("autopilot state exceeds bounded cardinality")
	}
	if len(s.Dedup) > 256 || len(s.Cooldown) > policy.MaxWorkflows {
		return fmt.Errorf("autopilot derived indexes exceed bounds")
	}
	for id, workflow := range s.Workflows {
		if id != workflow.ID {
			return fmt.Errorf("workflow index %q is inconsistent", id)
		}
		if err := workflow.Validate(policy); err != nil {
			return fmt.Errorf("workflow %s: %w", id, err)
		}
	}
	for id, run := range s.Runs {
		if id != run.ID {
			return fmt.Errorf("run index %q is inconsistent", id)
		}
		if err := run.validate(policy); err != nil {
			return fmt.Errorf("run %s: %w", id, err)
		}
	}
	return nil
}

func (r Run) validate(policy Policy) error {
	if !identifierPattern.MatchString(r.ID) || r.WorkflowID == "" || r.Revision == 0 || r.Digest == "" {
		return fmt.Errorf("identity is invalid")
	}
	if r.Step < 0 || r.Step >= len(r.Definition.Steps) || r.Transitions < 0 || r.Transitions > policy.MaxTransitions {
		return fmt.Errorf("step or transition count is invalid")
	}
	if !validRunState(r.State) {
		return fmt.Errorf("state %q is invalid", r.State)
	}
	if r.State == runWaitingJob && r.ExpectedJob == nil {
		return fmt.Errorf("waiting job identity is missing")
	}
	if r.State == runWaitingApproval && r.Approval == nil {
		return fmt.Errorf("waiting approval identity is missing")
	}
	encoded, _ := json.Marshal(r.Context)
	if len(encoded) > policy.MaxContextBytes {
		return fmt.Errorf("context exceeds %d bytes", policy.MaxContextBytes)
	}
	return nil
}

func validRunState(value RunState) bool {
	switch value {
	case runPending, runRunning, runWaitingJob, runWaitingApproval, runCompleted, runFailed, runCancelled, runInterrupted:
		return true
	default:
		return false
	}
}

func terminalRun(value RunState) bool {
	return value == runCompleted || value == runFailed || value == runCancelled || value == runInterrupted
}

func (s *persistedState) addAudit(now time.Time, policy Policy, run *Run, category, summary string) {
	if len(summary) > 160 {
		summary = summary[:160]
	}
	event := AuditEvent{At: now.UTC(), Category: category, Summary: summary}
	if run != nil {
		event.RunID, event.WorkflowID = run.ID, run.WorkflowID
	}
	s.Audit = append(s.Audit, event)
	if len(s.Audit) > policy.MaxAudit {
		s.Audit = append([]AuditEvent(nil), s.Audit[len(s.Audit)-policy.MaxAudit:]...)
	}
}

func (s *persistedState) enforceRetention(policy Policy) {
	if len(s.Runs) <= policy.MaxRuns {
		return
	}
	type item struct {
		id string
		at time.Time
	}
	items := make([]item, 0, len(s.Runs))
	for id, run := range s.Runs {
		if terminalRun(run.State) {
			items = append(items, item{id, run.UpdatedAt})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].at.Before(items[j].at) })
	for _, candidate := range items {
		if len(s.Runs) <= policy.MaxRuns {
			break
		}
		delete(s.Runs, candidate.id)
		s.Counters.Cleanup++
	}
}

func cloneRun(value Run) Run {
	copy := value
	copy.Context = map[string]json.RawMessage{}
	for key, raw := range value.Context {
		copy.Context[key] = append(json.RawMessage(nil), raw...)
	}
	if value.ExpectedJob != nil {
		identity := *value.ExpectedJob
		copy.ExpectedJob = &identity
	}
	if value.Approval != nil {
		approval := *value.Approval
		copy.Approval = &approval
	}
	copy.Definition.Steps = append([]Step(nil), value.Definition.Steps...)
	return copy
}
