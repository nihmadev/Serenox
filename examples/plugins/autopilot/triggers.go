package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func triggerMatches(workflow Workflow, update protocol.Update) (bool, string) {
	if !workflow.Enabled || workflow.Trigger.Kind != "message" || update.Message == nil {
		return false, "disabled_or_not_message"
	}
	message := update.Message
	if strings.HasPrefix(strings.TrimSpace(message.Text), ".") {
		return false, "command"
	}
	if workflow.Trigger.Direction == "incoming" && message.Outgoing || workflow.Trigger.Direction == "outgoing" && !message.Outgoing {
		return false, "direction"
	}
	if !containsInt64(workflow.Trigger.ChatIDs, message.ChatID) || !containsInt64(workflow.Trigger.SenderIDs, message.SenderID) {
		return false, "scope"
	}
	match := workflow.Trigger.Match
	if match == nil {
		return false, "matcher"
	}
	if match.Exact != "" && message.Text != match.Exact {
		return false, "exact"
	}
	if match.Prefix != "" && !strings.HasPrefix(message.Text, match.Prefix) {
		return false, "prefix"
	}
	if match.Suffix != "" && !strings.HasSuffix(message.Text, match.Suffix) {
		return false, "suffix"
	}
	if match.Contains != "" && !strings.Contains(message.Text, match.Contains) {
		return false, "contains"
	}
	if match.Regex != "" {
		compiled, err := regexp.Compile(match.Regex)
		if err != nil || !compiled.MatchString(message.Text) {
			return false, "regex"
		}
	}
	return true, "matched"
}

func containsInt64(values []int64, candidate int64) bool {
	if len(values) == 0 {
		return true
	}
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func workflowJobSpec(workflow Workflow) (protocol.JobSpec, bool, error) {
	trigger := workflow.Trigger
	spec := baseJobSpec("ap-sched-"+workflow.ID, "workflow_schedule", "autopilot.schedule/v1", []byte(fmt.Sprintf(`{"workflow_id":%q,"revision":%d}`, workflow.ID, workflow.Revision)))
	spec.Description = "Autopilot scheduled workflow trigger"
	switch trigger.Kind {
	case "one_shot":
		if trigger.RunAt == nil {
			return protocol.JobSpec{}, false, fmt.Errorf("missing run_at")
		}
		spec.Schedule = protocol.JobSchedule{Kind: protocol.JobScheduleOneShot, OneShot: &protocol.OneShotSchedule{RunAtUnixMS: trigger.RunAt.UnixMilli()}}
		spec.Timezone = protocol.JobTimezone{Name: "UTC", DST: protocol.JobDSTReject}
	case "interval":
		spec.Schedule = protocol.JobSchedule{Kind: protocol.JobScheduleFixedDelay, Interval: &protocol.IntervalSchedule{IntervalMS: trigger.IntervalMS}}
		spec.Timezone = protocol.JobTimezone{Name: "UTC", DST: protocol.JobDSTReject}
	case "calendar":
		calendar := trigger.Calendar
		if calendar == nil {
			return protocol.JobSpec{}, false, fmt.Errorf("missing calendar")
		}
		spec.Schedule = protocol.JobSchedule{Kind: protocol.JobScheduleCalendar, Calendar: &protocol.CalendarSchedule{Minutes: []int{calendar.Minute}, Hours: []int{calendar.Hour}}}
		spec.Timezone = protocol.JobTimezone{Name: calendar.Timezone, DST: protocol.JobDSTReject}
	default:
		return protocol.JobSpec{}, false, nil
	}
	return spec, true, spec.Validate()
}

func baseJobSpec(id, handler, payloadType string, payload []byte) protocol.JobSpec {
	return protocol.JobSpec{Identity: protocol.JobIdentity{PluginID: pluginID, AccountID: 1, ID: id}, Handler: handler,
		Persistent: true, DeadlineMS: int64((24 * time.Hour) / time.Millisecond), MaxRuntimeMS: 5000,
		Misfire: protocol.JobMisfireFireOnce, Overlap: protocol.JobOverlapSkipIfRunning,
		Timezone: protocol.JobTimezone{Name: "UTC", DST: protocol.JobDSTReject}, Payload: payload, PayloadType: payloadType, Revision: 1}
}
