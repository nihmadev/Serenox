package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validWorkflow() Workflow {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return Workflow{Version: definitionVersion, ID: "incident-followup", Name: "Incident follow-up", Description: "finite", Enabled: true, Revision: 1, CreatedAt: now, UpdatedAt: now, Trigger: Trigger{Kind: "manual"}, Steps: []Step{{ID: "set", Kind: "set", Key: "severity", Value: json.RawMessage(`"high"`)}, {ID: "check", Kind: "condition", Condition: &Condition{Field: "vars.severity", Op: "eq", Value: json.RawMessage(`"high"`), True: 2, False: 3}}, {ID: "done", Kind: "complete", Text: "done"}, {ID: "failed", Kind: "fail", Text: "not high"}}}
}

func TestWorkflowStrictCanonicalRoundTrip(t *testing.T) {
	workflow := validWorkflow()
	raw, err := workflow.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseWorkflow(raw, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	again, _ := parsed.Canonical()
	if string(raw) != string(again) {
		t.Fatalf("canonical mismatch\n%s\n%s", raw, again)
	}
	bad := strings.Replace(string(raw), `"name":`, `"unknown":true,"name":`, 1)
	if _, err := ParseWorkflow([]byte(bad), DefaultPolicy()); err == nil {
		t.Fatal("unknown field accepted")
	}
}
func TestWorkflowValidationPathsAndGraph(t *testing.T) {
	workflow := validWorkflow()
	workflow.Steps[1].Condition.True = 1
	if err := workflow.Validate(DefaultPolicy()); err == nil || !strings.Contains(err.Error(), "steps[1].condition") {
		t.Fatalf("error=%v", err)
	}
	workflow = validWorkflow()
	workflow.Steps = append(workflow.Steps, make([]Step, DefaultPolicy().MaxSteps)...)
	if err := workflow.Validate(DefaultPolicy()); err == nil || !strings.Contains(err.Error(), "steps") {
		t.Fatalf("error=%v", err)
	}
}
func TestWorkflowVersionsDuplicatesAndSize(t *testing.T) {
	workflow := validWorkflow()
	workflow.Version = "v2"
	if err := workflow.Validate(DefaultPolicy()); err == nil {
		t.Fatal("unknown version accepted")
	}
	workflow = validWorkflow()
	workflow.Steps[1].ID = workflow.Steps[0].ID
	if err := workflow.Validate(DefaultPolicy()); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("error=%v", err)
	}
	if _, err := ParseWorkflow(make([]byte, DefaultPolicy().MaxDefinitionBytes+1), DefaultPolicy()); err == nil {
		t.Fatal("oversized definition accepted")
	}
}

func TestTemplateAllowlistMalformedAndExpansion(t *testing.T) {
	run := Run{ID: "r-1", WorkflowID: "flow", Source: SourceSummary{ChatID: 42}, Context: map[string]json.RawMessage{"vars.name": json.RawMessage(`"Ada"`)}}
	value, err := renderTemplate("{{vars.name}}/{{workflow.id}}/{{message.chat_id}}", run, DefaultPolicy())
	if err != nil || value != "Ada/flow/42" {
		t.Fatalf("render=%q err=%v", value, err)
	}
	for _, input := range []string{"{{secret}}", "{{vars.name", "}}"} {
		if _, err := renderTemplate(input, run, DefaultPolicy()); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	policy := DefaultPolicy()
	policy.MaxRenderedBytes = 2
	if _, err := renderTemplate("{{vars.name}}", run, policy); err == nil {
		t.Fatal("expansion limit ignored")
	}
}
