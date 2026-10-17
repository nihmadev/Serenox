package main

import "time"

func demoWorkflows(now time.Time) []Workflow {
	base := func(id, name, description string, trigger Trigger, steps []Step) Workflow {
		return Workflow{Version: definitionVersion, ID: id, Name: name, Description: description, Enabled: true, Revision: 1, CreatedAt: now.UTC(), UpdatedAt: now.UTC(), Trigger: trigger, Steps: steps, Limits: RunPolicy{Concurrency: 1, Retention: 16}}
	}
	return []Workflow{
		base("demo-reminder", "Delayed reminder", "Notify, wait through a durable job, notify again, and complete.", Trigger{Kind: "manual"}, []Step{
			{ID: "start", Kind: "notify", Text: "Autopilot run {{run.id}} started."}, {ID: "delay", Kind: "wait", DelayMS: 1000}, {ID: "finish", Kind: "notify", Text: "Durable wait resumed for {{workflow.id}}."}, {ID: "done", Kind: "complete", Text: "reminder complete"},
		}),
		base("demo-approval", "Owner approval", "Wait for an owner decision; control bot is optional.", Trigger{Kind: "manual"}, []Step{
			{ID: "ask", Kind: "approval", Text: "Approve Autopilot run {{run.id}}?", Next: 1, OnFailure: 3}, {ID: "approved", Kind: "notify", Text: "Decision: {{approval.decision}}"}, {ID: "done", Kind: "complete", Text: "approved"}, {ID: "rejected", Kind: "fail", Text: "rejected by owner"},
		}),
		base("demo-message", "Bounded message rule", "Matches the exact incoming phrase autopilot demo.", Trigger{Kind: "message", Direction: "incoming", Match: &MessageMatch{Exact: "autopilot demo"}}, []Step{
			{ID: "record", Kind: "set", Key: "matched", Value: []byte(`true`)}, {ID: "notify", Kind: "notify", Text: "Matched bounded rule in chat {{message.chat_id}}."}, {ID: "done", Kind: "complete", Text: "message rule complete"},
		}),
		base("demo-heartbeat", "Scheduled heartbeat", "A deterministic state-only fixed-delay workflow.", Trigger{Kind: "interval", IntervalMS: 24 * 60 * 60 * 1000}, []Step{
			{ID: "record", Kind: "set", Key: "heartbeat", Value: []byte(`true`)}, {ID: "done", Kind: "complete", Text: "heartbeat recorded"},
		}),
	}
}
