package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
	serenox "github.com/nihmadev/Serenox/sdk/go"
)

const pluginID, pluginVersion = "example.autopilot", "1.0.0"

type processExit struct{}

func (processExit) Exit(code int) { os.Exit(code) }

type autopilotHandler struct {
	engine  *Engine
	exit    ExitService
	started time.Time
}

func newAutopilotHandler(exit ExitService) *autopilotHandler {
	if exit == nil {
		exit = processExit{}
	}
	return &autopilotHandler{engine: newEngine(nil, nil), exit: exit}
}

func (h *autopilotHandler) Metadata(context.Context) (protocol.PluginMetadata, error) {
	job := func(name, description, payload string) protocol.JobHandlerSpec {
		return protocol.JobHandlerSpec{Name: name, Description: description, Persistent: true, MaxRuntimeMS: 5000, Overlap: protocol.JobOverlapSkipIfRunning, RequiredCapabilities: []protocol.Capability{protocol.CapabilityStorage}, RequiredPermissions: []string{"storage.read", "storage.write", "jobs.schedule"}, PayloadType: payload, MaxPayloadBytes: 512}
	}
	return protocol.PluginMetadata{ID: pluginID, Name: "Autopilot", Version: pluginVersion, Description: "Bounded durable workflow and recovery demonstration", Author: "Serenox", ProtocolVersion: protocol.Version,
		Capabilities:     []protocol.Capability{protocol.CapabilityCommands, protocol.CapabilityWatchers, protocol.CapabilityInline, protocol.CapabilityStorage, protocol.CapabilityJobs, protocol.CapabilityHTTP, protocol.CapabilityControlBot, protocol.CapabilityControlBotCallbacks},
		Commands:         []protocol.CommandSpec{{Name: "autopilot", Description: "Manage Autopilot", OwnerOnly: true}, {Name: "flow", Description: "Manage bounded workflows and runs", OwnerOnly: true}},
		Watchers:         []protocol.WatcherSpec{{ID: "workflow-messages", Description: "Bounded dynamic workflow matcher", Filter: protocol.WatcherFilter{Kinds: []protocol.UpdateKind{protocol.UpdateMessage}, Incoming: true, Outgoing: true}}},
		CallbackHandlers: []protocol.CallbackSpec{{Name: "callback", Description: "Owner-bound Autopilot approvals", DataPrefix: "ap:", OwnerOnly: true, TTLMS: int64((10 * time.Minute) / time.Millisecond)}},
		ControlCommands:  []protocol.ControlCommand{{Name: "autopilot", Description: "Autopilot owner dashboard", Surfaces: []protocol.ControlSurface{protocol.ControlSurfacePrivate}, ActorPolicy: protocol.ControlActorOwnerOnly, MaxArgumentBytes: 128, MaxStateBytes: 1024, ResponseMode: protocol.ControlResponseBotMessage, HandlerID: "autopilot-control"}},
		Jobs:             []protocol.JobHandlerSpec{job("workflow_schedule", "Start a scheduled workflow", "autopilot.schedule/v1"), job("resume_wait", "Resume a waiting run", "autopilot.resume/v1"), job("approval_expiry", "Expire an owner approval", "autopilot.approval-expiry/v1"), job("cleanup", "Bounded retention cleanup", "autopilot.cleanup/v1"), job("chaos_failure", "Intentional scoped chaos failure", "autopilot.chaos/v1")},
		Config:           []protocol.ConfigSpec{{Key: "chaos_enabled", Type: protocol.ConfigBoolean, Description: "Allow confirmed process-scoped chaos demonstrations", Default: json.RawMessage(`false`)}, {Key: "control_bot_enabled", Type: protocol.ConfigBoolean, Description: "Use an attached control bot for approval UI", Default: json.RawMessage(`false`)}, {Key: "http_enabled", Type: protocol.ConfigBoolean, Description: "Allow the signed connector catalog", Default: json.RawMessage(`false`)}}}, nil
}

func (h *autopilotHandler) Configure(_ context.Context, values []protocol.ConfigValue) error {
	settings := map[string]bool{"chaos_enabled": false, "control_bot_enabled": false, "http_enabled": false}
	for _, value := range values {
		if _, ok := settings[value.Key]; !ok {
			continue
		}
		var enabled bool
		if err := json.Unmarshal(value.Value, &enabled); err != nil {
			return fmt.Errorf("%s must be boolean", value.Key)
		}
		settings[value.Key] = enabled
	}
	h.engine.configure(settings["chaos_enabled"], settings["control_bot_enabled"], settings["http_enabled"])
	return nil
}
func (h *autopilotHandler) Start(context.Context) error { h.started = time.Now().UTC(); return nil }
func (h *autopilotHandler) Stop(context.Context) error  { return nil }
func (h *autopilotHandler) Health(context.Context, protocol.HealthRequest) (protocol.HealthResponse, error) {
	return protocol.HealthResponse{Ready: true, Status: "ready; durable reconciliation occurs on first dispatch", UptimeMS: time.Since(h.started).Milliseconds()}, nil
}

func (h *autopilotHandler) Dispatch(ctx context.Context, request protocol.DispatchRequest) (protocol.DispatchResponse, error) {
	host, err := hostFromContext(ctx, h.exit)
	if err != nil {
		return protocol.DispatchResponse{}, err
	}
	if request.Update.Control != nil {
		return h.control(ctx, host, request.Update)
	}
	if request.Command != nil {
		return h.command(ctx, host, request)
	}
	_, err = h.engine.handleWatcher(ctx, host, request.Update)
	return protocol.DispatchResponse{}, err
}

func (h *autopilotHandler) Job(ctx context.Context, invocation protocol.JobInvocation) (protocol.DispatchResponse, error) {
	host, err := hostFromContext(ctx, h.exit)
	if err != nil {
		return protocol.DispatchResponse{}, err
	}
	_, err = h.engine.handleJob(ctx, host, invocation)
	return protocol.DispatchResponse{}, err
}

func (h *autopilotHandler) command(ctx context.Context, host Host, request protocol.DispatchRequest) (protocol.DispatchResponse, error) {
	command := request.Command
	var text string
	var err error
	if command.Name == "autopilot" {
		text, err = h.autopilotCommand(ctx, host, command.RawArgs, command.Message)
	} else {
		text, err = h.flowCommand(ctx, host, command.RawArgs, command.Message)
	}
	if err != nil {
		text = "Autopilot: " + safeError(err)
	}
	return reply(request.Update.ID, command.Message, text), nil
}

func reply(updateID string, message protocol.Message, text string) protocol.DispatchResponse {
	if len(text) > 4096 {
		text = text[:4093] + "..."
	}
	return protocol.DispatchResponse{Actions: []protocol.Action{{ID: "autopilot-reply-" + updateID, Kind: protocol.ActionReply, ChatID: message.ChatID, ReplyToMessageID: message.ID, Text: text}}}
}

func (h *autopilotHandler) autopilotCommand(ctx context.Context, host Host, raw string, message protocol.Message) (string, error) {
	args := strings.Fields(raw)
	if len(args) == 0 || args[0] == "help" {
		return helpText, nil
	}
	switch args[0] {
	case "status":
		return h.renderStatus(ctx, host)
	case "pause":
		if err := h.engine.setPaused(ctx, host, true); err != nil {
			return "", err
		}
		return "Autopilot paused. Waiting jobs remain durable; new runs are rejected.", nil
	case "resume":
		if err := h.engine.setPaused(ctx, host, false); err != nil {
			return "", err
		}
		return "Autopilot resumed.", nil
	case "cleanup":
		h.engine.mu.Lock()
		if err := h.engine.ensureLoaded(ctx, host); err != nil {
			h.engine.mu.Unlock()
			return "", err
		}
		h.engine.cleanupLocked()
		err := h.engine.save(ctx, host)
		h.engine.mu.Unlock()
		return "Bounded retention cleanup completed.", err
	case "demo":
		return h.demoCommand(ctx, host, args[1:], message)
	case "chaos":
		return h.chaosCommand(ctx, host, args[1:], message)
	default:
		return "", errors.New("usage: .autopilot help|status|pause|resume|demo|cleanup|chaos")
	}
}

func (h *autopilotHandler) demoCommand(ctx context.Context, host Host, args []string, message protocol.Message) (string, error) {
	if len(args) != 1 {
		return "", errors.New("usage: .autopilot demo install|run")
	}
	if args[0] == "install" {
		installed := 0
		for _, workflow := range demoWorkflows(h.engine.clock.Now()) {
			state, _ := h.engine.status(ctx, host)
			if _, exists := state.Workflows[workflow.ID]; exists {
				continue
			}
			if err := h.engine.installWorkflow(ctx, host, workflow); err != nil {
				return "", err
			}
			installed++
		}
		if err := h.engine.ensureCleanupJob(ctx, host); err != nil {
			return "", err
		}
		return fmt.Sprintf("Demo ready: %d new workflows, %d already present.", installed, 4-installed), nil
	}
	if args[0] == "run" {
		run, err := h.engine.trigger(ctx, host, "demo-reminder", SourceSummary{Kind: "manual", ChatID: message.ChatID, MessageID: message.ID, ActorID: message.SenderID}, "")
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Run %s is %s at step %d.", run.ID, run.State, run.Step), nil
	}
	return "", errors.New("usage: .autopilot demo install|run")
}

func (h *autopilotHandler) flowCommand(ctx context.Context, host Host, raw string, message protocol.Message) (string, error) {
	verb, rest, _ := strings.Cut(strings.TrimSpace(raw), " ")
	state, err := h.engine.status(ctx, host)
	if err != nil {
		return "", err
	}
	switch verb {
	case "list":
		ids := sortedWorkflowIDs(state.Workflows)
		if len(ids) == 0 {
			return "No workflows. Use .autopilot demo install.", nil
		}
		lines := []string{"Workflows:"}
		for _, id := range ids {
			workflow := state.Workflows[id]
			lines = append(lines, fmt.Sprintf("- %s r%d enabled=%t trigger=%s", id, workflow.Revision, workflow.Enabled, workflow.Trigger.Kind))
		}
		return strings.Join(lines, "\n"), nil
	case "show":
		workflow, ok := state.Workflows[strings.TrimSpace(rest)]
		if !ok {
			return "", errors.New("workflow not found")
		}
		return fmt.Sprintf("%s — %s\nrevision=%d enabled=%t trigger=%s steps=%d", workflow.ID, workflow.Name, workflow.Revision, workflow.Enabled, workflow.Trigger.Kind, len(workflow.Steps)), nil
	case "enable", "disable":
		id := strings.TrimSpace(rest)
		err := h.engine.setEnabled(ctx, host, id, verb == "enable")
		return "Workflow " + id + " updated.", err
	case "trigger":
		id := strings.TrimSpace(rest)
		run, err := h.engine.trigger(ctx, host, id, SourceSummary{Kind: "manual", ChatID: message.ChatID, MessageID: message.ID, ActorID: message.SenderID}, "")
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Run %s: %s.", run.ID, run.State), nil
	case "cancel":
		run, err := h.engine.cancel(ctx, host, strings.TrimSpace(rest))
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Run %s: %s.", run.ID, run.State), nil
	case "retry":
		run, err := h.engine.retry(ctx, host, strings.TrimSpace(rest))
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("New run %s: %s.", run.ID, run.State), nil
	case "approve", "reject":
		run, err := h.engine.decide(ctx, host, strings.TrimSpace(rest), map[bool]string{true: "approved", false: "rejected"}[verb == "approve"], message.SenderID, "")
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Run %s: %s.", run.ID, run.State), nil
	case "runs":
		return renderRuns(state, strings.TrimSpace(rest)), nil
	case "audit":
		return renderAudit(state, strings.TrimSpace(rest)), nil
	case "export":
		return h.engine.exportJSON(ctx, host, strings.TrimSpace(rest))
	case "import":
		if strings.TrimSpace(rest) == "" {
			return "", errors.New("usage: .flow import <strict-json-definition>")
		}
		if err := h.engine.importJSON(ctx, host, rest); err != nil {
			return "", err
		}
		return "Workflow imported.", nil
	case "delete":
		fields := strings.Fields(rest)
		if len(fields) != 2 || fields[1] != "confirm" {
			return "", errors.New("usage: .flow delete <workflow-id> confirm")
		}
		if err := h.engine.deleteWorkflow(ctx, host, fields[0]); err != nil {
			return "", err
		}
		return "Workflow deleted.", nil
	default:
		return "", errors.New("usage: .flow list|show|enable|disable|trigger|cancel|retry|approve|reject|runs|audit|export|import|delete")
	}
}

func renderRuns(state persistedState, workflowID string) string {
	ids := make([]string, 0)
	for id := range state.Runs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	lines := []string{"Recent runs:"}
	for index := len(ids) - 1; index >= 0 && len(lines) <= 12; index-- {
		run := state.Runs[ids[index]]
		if workflowID != "" && run.WorkflowID != workflowID {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s %s %s step=%d transitions=%d", run.ID, run.WorkflowID, run.State, run.Step, run.Transitions))
	}
	if len(lines) == 1 {
		return "No matching runs."
	}
	return strings.Join(lines, "\n")
}
func renderAudit(state persistedState, runID string) string {
	lines := []string{"Recent audit:"}
	for index := len(state.Audit) - 1; index >= 0 && len(lines) <= 16; index-- {
		event := state.Audit[index]
		if runID != "" && event.RunID != runID {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s %s %s", event.At.Format(time.RFC3339), event.Category, event.Summary))
	}
	if len(lines) == 1 {
		return "No matching audit events."
	}
	return strings.Join(lines, "\n")
}

func (h *autopilotHandler) renderStatus(ctx context.Context, host Host) (string, error) {
	state, err := h.engine.status(ctx, host)
	if err != nil {
		return "", err
	}
	active, waiting := 0, 0
	for _, run := range state.Runs {
		if !terminalRun(run.State) {
			active++
		}
		if run.State == runWaitingJob || run.State == runWaitingApproval {
			waiting++
		}
	}
	return fmt.Sprintf("Autopilot %s\nworkflows=%d/%d runs=%d/%d active=%d waiting=%d audit=%d/%d\ncompleted=%d failed=%d interrupted=%d", map[bool]string{true: "paused", false: "running"}[state.Paused], len(state.Workflows), h.engine.policy.MaxWorkflows, len(state.Runs), h.engine.policy.MaxRuns, active, waiting, len(state.Audit), h.engine.policy.MaxAudit, state.Counters.Completed, state.Counters.Failed, state.Counters.Interrupted), nil
}

func (h *autopilotHandler) control(ctx context.Context, host Host, update protocol.Update) (protocol.DispatchResponse, error) {
	control := update.Control
	if update.Kind == protocol.UpdateControlCallback {
		token, decision, ok := parseApprovalCallback(control.CallbackData)
		if !ok {
			return protocol.DispatchResponse{}, errors.New("unknown or expired Autopilot control")
		}
		run, err := h.engine.decideToken(ctx, host, token, decision, control.Actor.ID)
		if err != nil {
			return protocol.DispatchResponse{}, err
		}
		return protocol.DispatchResponse{Actions: []protocol.Action{{ID: "ap-control-" + update.ID, Kind: protocol.ActionBotSend, ResponseChannel: protocol.ResponseChannelControlBot, ChatID: control.ChatID, Text: fmt.Sprintf("Run %s: %s", run.ID, run.State)}}}, nil
	}
	status, err := h.renderStatus(ctx, host)
	if err != nil {
		return protocol.DispatchResponse{}, err
	}
	return protocol.DispatchResponse{Actions: []protocol.Action{{ID: "ap-dashboard-" + update.ID, Kind: protocol.ActionBotSend, ResponseChannel: protocol.ResponseChannelControlBot, ChatID: control.ChatID, Text: status}}}, nil
}

func (h *autopilotHandler) chaosCommand(ctx context.Context, host Host, args []string, message protocol.Message) (string, error) {
	if len(args) == 1 && args[0] == "status" {
		return "Chaos is " + map[bool]string{true: "enabled", false: "disabled"}[h.engine.chaosEnabled] + "; it is scoped to example.autopilot.", nil
	}
	if len(args) != 2 || args[1] != "confirm" {
		return "", errors.New("usage: .autopilot chaos status|fail-job confirm|interrupt-run confirm|exit-process confirm")
	}
	h.engine.mu.Lock()
	defer h.engine.mu.Unlock()
	if err := h.engine.ensureLoaded(ctx, host); err != nil {
		return "", err
	}
	if !h.engine.chaosEnabled {
		return "", errors.New("chaos is disabled by configuration")
	}
	run, err := h.engine.prepareChaosRunLocked()
	if err != nil {
		return "", err
	}
	run.Source = SourceSummary{Kind: "chaos", ChatID: message.ChatID, ActorID: message.SenderID}
	h.engine.state.Runs[run.ID] = run
	h.engine.state.Counters.Chaos++
	h.engine.state.addAudit(h.engine.clock.Now(), h.engine.policy, &run, "chaos", args[0])
	if err := h.engine.save(ctx, host); err != nil {
		return "", err
	}
	switch args[0] {
	case "interrupt-run":
		run.State = runInterrupted
		run.FinishedAt = h.engine.clock.Now()
		h.engine.state.Runs[run.ID] = run
		return "Prepared run " + run.ID + " marked interrupted.", h.engine.save(ctx, host)
	case "fail-job":
		if host.Jobs == nil {
			return "", errors.New("jobs service unavailable")
		}
		payload, _ := json.Marshal(map[string]any{"run_id": run.ID, "revision": run.Revision})
		spec := baseJobSpec("ap-c-"+run.ID, "chaos_failure", "autopilot.chaos/v1", payload)
		spec.Schedule = protocol.JobSchedule{Kind: protocol.JobScheduleOneShot, OneShot: &protocol.OneShotSchedule{RunAtUnixMS: h.engine.clock.Now().Add(time.Second).UnixMilli()}}
		_, err = host.Jobs.Create(ctx, spec)
		return "Intentional failing job scheduled for run " + run.ID + ".", err
	case "exit-process":
		if host.Exit == nil {
			return "", errors.New("exit service unavailable")
		}
		host.Exit.Exit(23)
		return "Exit injected.", nil
	default:
		return "", errors.New("unknown chaos operation")
	}
}

func (e *Engine) prepareChaosRunLocked() (Run, error) {
	workflow := Workflow{Version: definitionVersion, ID: "chaos-recovery", Name: "Chaos recovery", Enabled: true, Revision: 1, CreatedAt: e.clock.Now(), UpdatedAt: e.clock.Now(), Trigger: Trigger{Kind: "manual"}, Steps: []Step{{ID: "finish", Kind: "complete", Text: "manual recovery required"}}}
	digest, _ := workflow.Digest()
	id, err := e.ids.New("r-")
	if err != nil {
		return Run{}, err
	}
	now := e.clock.Now()
	return Run{ID: id, WorkflowID: workflow.ID, Revision: 1, Digest: digest, State: runRunning, Context: map[string]json.RawMessage{}, CreatedAt: now, UpdatedAt: now, StartedAt: now, Definition: workflow}, nil
}

const helpText = "Autopilot commands:\n.autopilot status|pause|resume|demo install|demo run|cleanup\n.autopilot chaos status|fail-job confirm|interrupt-run confirm|exit-process confirm\n.flow list|show <id>|enable <id>|disable <id>|trigger <id>\n.flow cancel <run>|retry <run>|approve <run>|reject <run>|runs [workflow]|audit [run]\n.flow export <id>|import <strict-json>|delete <id> confirm"

var _ serenox.Handler = (*autopilotHandler)(nil)
var _ serenox.JobHandler = (*autopilotHandler)(nil)
