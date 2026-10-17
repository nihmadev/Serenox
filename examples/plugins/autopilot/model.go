package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

const definitionVersion = "autopilot.workflow/v1"

type Policy struct {
	MaxWorkflows, MaxSteps, MaxTransitions, MaxRuns, MaxAudit int
	MaxNameBytes, MaxDescriptionBytes, MaxDefinitionBytes     int
	MaxContextBytes, MaxRenderedBytes, MaxRegexBytes          int
}

func DefaultPolicy() Policy {
	return Policy{MaxWorkflows: 32, MaxSteps: 16, MaxTransitions: 32, MaxRuns: 64, MaxAudit: 256,
		MaxNameBytes: 64, MaxDescriptionBytes: 256, MaxDefinitionBytes: 32 << 10,
		MaxContextBytes: 16 << 10, MaxRenderedBytes: 4096, MaxRegexBytes: 256}
}

type Workflow struct {
	Version     string    `json:"version"`
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Enabled     bool      `json:"enabled"`
	Revision    uint64    `json:"revision"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Trigger     Trigger   `json:"trigger"`
	Steps       []Step    `json:"steps"`
	Limits      RunPolicy `json:"limits,omitempty"`
}

type RunPolicy struct {
	Concurrency int   `json:"concurrency,omitempty"`
	CooldownMS  int64 `json:"cooldown_ms,omitempty"`
	Retention   int   `json:"retention,omitempty"`
}

type Trigger struct {
	Kind       string           `json:"kind"`
	Direction  string           `json:"direction,omitempty"`
	Match      *MessageMatch    `json:"match,omitempty"`
	ChatIDs    []int64          `json:"chat_ids,omitempty"`
	SenderIDs  []int64          `json:"sender_ids,omitempty"`
	RunAt      *time.Time       `json:"run_at,omitempty"`
	IntervalMS int64            `json:"interval_ms,omitempty"`
	Calendar   *CalendarTrigger `json:"calendar,omitempty"`
}

type CalendarTrigger struct {
	Timezone string `json:"timezone"`
	Hour     int    `json:"hour"`
	Minute   int    `json:"minute"`
}

type MessageMatch struct {
	Exact, Prefix, Suffix, Contains, Regex string
}

func (m MessageMatch) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Exact    string `json:"exact,omitempty"`
		Prefix   string `json:"prefix,omitempty"`
		Suffix   string `json:"suffix,omitempty"`
		Contains string `json:"contains,omitempty"`
		Regex    string `json:"regex,omitempty"`
	}{m.Exact, m.Prefix, m.Suffix, m.Contains, m.Regex})
}

func (m *MessageMatch) UnmarshalJSON(data []byte) error {
	var value struct {
		Exact    string `json:"exact,omitempty"`
		Prefix   string `json:"prefix,omitempty"`
		Suffix   string `json:"suffix,omitempty"`
		Contains string `json:"contains,omitempty"`
		Regex    string `json:"regex,omitempty"`
	}
	if err := strictDecode(data, &value); err != nil {
		return err
	}
	m.Exact, m.Prefix, m.Suffix, m.Contains, m.Regex = value.Exact, value.Prefix, value.Suffix, value.Contains, value.Regex
	return nil
}

type Step struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Next      int             `json:"next,omitempty"`
	OnFailure int             `json:"on_failure,omitempty"`
	Text      string          `json:"text,omitempty"`
	DelayMS   int64           `json:"delay_ms,omitempty"`
	Key       string          `json:"key,omitempty"`
	Value     json.RawMessage `json:"value,omitempty"`
	Condition *Condition      `json:"condition,omitempty"`
	Connector string          `json:"connector,omitempty"`
	Method    string          `json:"method,omitempty"`
	Path      string          `json:"path,omitempty"`
}

type Condition struct {
	Field string          `json:"field"`
	Op    string          `json:"op"`
	Value json.RawMessage `json:"value"`
	True  int             `json:"true"`
	False int             `json:"false"`
}

type FieldError struct{ Path, Message string }

func (e *FieldError) Error() string { return e.Path + ": " + e.Message }
func invalidField(path, format string, args ...any) error {
	return &FieldError{path, fmt.Sprintf(format, args...)}
}

func ParseWorkflow(data []byte, policy Policy) (Workflow, error) {
	if len(data) == 0 || len(data) > policy.MaxDefinitionBytes {
		return Workflow{}, invalidField("definition", "must contain 1..%d bytes", policy.MaxDefinitionBytes)
	}
	var workflow Workflow
	if err := strictDecode(data, &workflow); err != nil {
		return Workflow{}, invalidField("definition", "%v", err)
	}
	if err := workflow.Validate(policy); err != nil {
		return Workflow{}, err
	}
	return workflow, nil
}

func strictDecode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func (w Workflow) Canonical() ([]byte, error) {
	return json.Marshal(w)
}

func (w Workflow) Digest() (string, error) {
	data, err := w.Canonical()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:16]), nil
}

var identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func (w Workflow) Validate(policy Policy) error {
	if w.Version != definitionVersion {
		return invalidField("version", "must be %q", definitionVersion)
	}
	if !identifierPattern.MatchString(w.ID) {
		return invalidField("id", "must be a lower-case stable identifier")
	}
	if len(w.Name) == 0 || len(w.Name) > policy.MaxNameBytes {
		return invalidField("name", "must contain 1..%d bytes", policy.MaxNameBytes)
	}
	if len(w.Description) > policy.MaxDescriptionBytes {
		return invalidField("description", "exceeds %d bytes", policy.MaxDescriptionBytes)
	}
	if w.Revision == 0 {
		return invalidField("revision", "must be positive")
	}
	if w.CreatedAt.IsZero() || w.UpdatedAt.IsZero() {
		return invalidField("timestamps", "created_at and updated_at are required")
	}
	if w.Limits.Concurrency < 0 || w.Limits.Concurrency > 4 {
		return invalidField("limits.concurrency", "must be 0..4")
	}
	if w.Limits.CooldownMS < 0 || w.Limits.CooldownMS > 7*24*60*60*1000 {
		return invalidField("limits.cooldown_ms", "is outside bounds")
	}
	if w.Limits.Retention < 0 || w.Limits.Retention > policy.MaxRuns {
		return invalidField("limits.retention", "must be 0..%d", policy.MaxRuns)
	}
	if err := w.Trigger.validate(policy); err != nil {
		return err
	}
	if len(w.Steps) == 0 || len(w.Steps) > policy.MaxSteps {
		return invalidField("steps", "must contain 1..%d steps", policy.MaxSteps)
	}
	seen := map[string]struct{}{}
	for index := range w.Steps {
		step := w.Steps[index]
		if !identifierPattern.MatchString(step.ID) {
			return invalidField(fmt.Sprintf("steps[%d].id", index), "is invalid")
		}
		if _, exists := seen[step.ID]; exists {
			return invalidField(fmt.Sprintf("steps[%d].id", index), "is duplicated")
		}
		seen[step.ID] = struct{}{}
		if err := step.validate(index, len(w.Steps), policy); err != nil {
			return err
		}
	}
	return validateAllPathsTerminate(w.Steps, policy.MaxTransitions)
}

func (t Trigger) validate(policy Policy) error {
	switch t.Kind {
	case "manual":
		if t.Match != nil || t.RunAt != nil || t.IntervalMS != 0 || t.Calendar != nil {
			return invalidField("trigger", "manual trigger has no schedule or match")
		}
	case "message":
		if t.Direction != "incoming" && t.Direction != "outgoing" {
			return invalidField("trigger.direction", "must be incoming or outgoing")
		}
		if t.Match == nil {
			return invalidField("trigger.match", "is required")
		}
		count := 0
		for _, value := range []string{t.Match.Exact, t.Match.Prefix, t.Match.Suffix, t.Match.Contains, t.Match.Regex} {
			if value != "" {
				count++
				if len(value) > policy.MaxRegexBytes {
					return invalidField("trigger.match", "matcher exceeds %d bytes", policy.MaxRegexBytes)
				}
			}
		}
		if count == 0 || count > 3 {
			return invalidField("trigger.match", "requires 1..3 matchers")
		}
		if t.Match.Regex != "" {
			if _, err := regexp.Compile(t.Match.Regex); err != nil {
				return invalidField("trigger.match.regex", "%v", err)
			}
		}
	case "one_shot":
		if t.RunAt == nil {
			return invalidField("trigger.run_at", "is required")
		}
	case "interval":
		if t.IntervalMS < 1000 || t.IntervalMS > 365*24*60*60*1000 {
			return invalidField("trigger.interval_ms", "must be 1000ms..365d")
		}
	case "calendar":
		if t.Calendar == nil {
			return invalidField("trigger.calendar", "is required")
		}
		if _, err := time.LoadLocation(t.Calendar.Timezone); err != nil {
			return invalidField("trigger.calendar.timezone", "unknown IANA timezone")
		}
		if t.Calendar.Hour < 0 || t.Calendar.Hour > 23 || t.Calendar.Minute < 0 || t.Calendar.Minute > 59 {
			return invalidField("trigger.calendar", "hour/minute are outside bounds")
		}
	default:
		return invalidField("trigger.kind", "unsupported trigger %q", t.Kind)
	}
	if len(t.ChatIDs) > 16 || len(t.SenderIDs) > 16 {
		return invalidField("trigger.scope", "chat_ids and sender_ids are limited to 16")
	}
	return nil
}

func (s Step) validate(index, count int, policy Policy) error {
	path := fmt.Sprintf("steps[%d]", index)
	terminal := s.Kind == "complete" || s.Kind == "fail"
	for name, target := range map[string]int{"next": s.Next, "on_failure": s.OnFailure} {
		if target != 0 && (target <= index || target >= count) {
			return invalidField(path+"."+name, "must select a higher step index")
		}
	}
	switch s.Kind {
	case "notify":
		if s.Text == "" {
			return invalidField(path+".text", "is required")
		}
	case "wait":
		if s.DelayMS < 1000 || s.DelayMS > 365*24*60*60*1000 {
			return invalidField(path+".delay_ms", "must be 1000ms..365d")
		}
	case "approval":
		if s.Text == "" {
			return invalidField(path+".text", "is required")
		}
	case "http":
		if !identifierPattern.MatchString(s.Connector) || (s.Method != "GET" && s.Method != "POST") || !strings.HasPrefix(s.Path, "/") || strings.Contains(s.Path, "..") {
			return invalidField(path, "invalid connector, method, or fixed path")
		}
	case "set":
		if !identifierPattern.MatchString(s.Key) || !validContextValue(s.Value) {
			return invalidField(path, "set requires a bounded key and JSON scalar/object")
		}
	case "condition":
		if s.Condition == nil {
			return invalidField(path+".condition", "is required")
		}
		if err := s.Condition.validate(index, count); err != nil {
			return err
		}
	case "complete", "fail":
	default:
		return invalidField(path+".kind", "unsupported step %q", s.Kind)
	}
	if terminal && (s.Next != 0 || s.OnFailure != 0) {
		return invalidField(path, "terminal steps cannot branch")
	}
	if len(s.Text) > policy.MaxRenderedBytes {
		return invalidField(path+".text", "template exceeds %d bytes", policy.MaxRenderedBytes)
	}
	return nil
}

func (c Condition) validate(index, count int) error {
	if !identifierPattern.MatchString(strings.TrimPrefix(c.Field, "vars.")) {
		return invalidField(fmt.Sprintf("steps[%d].condition.field", index), "must reference vars.<name>")
	}
	if !strings.HasPrefix(c.Field, "vars.") || (c.Op != "eq" && c.Op != "neq" && c.Op != "exists") {
		return invalidField(fmt.Sprintf("steps[%d].condition", index), "supports vars fields and eq/neq/exists")
	}
	for _, target := range []int{c.True, c.False} {
		if target <= index || target >= count {
			return invalidField(fmt.Sprintf("steps[%d].condition", index), "branches must select higher indexes")
		}
	}
	return nil
}

func validContextValue(raw json.RawMessage) bool {
	if len(raw) == 0 || len(raw) > 2048 || !json.Valid(raw) {
		return false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	switch value.(type) {
	case nil, bool, string, float64, map[string]any:
		return true
	default:
		return false
	}
}

func validateAllPathsTerminate(steps []Step, budget int) error {
	for start := range steps {
		seen := map[int]bool{}
		current := start
		for transitions := 0; transitions <= budget; transitions++ {
			if current < 0 || current >= len(steps) {
				break
			}
			if seen[current] {
				return invalidField("steps", "graph contains a cycle")
			}
			seen[current] = true
			step := steps[current]
			if step.Kind == "complete" || step.Kind == "fail" {
				break
			}
			next := step.Next
			if step.Condition != nil {
				next = step.Condition.True
			}
			if next == 0 {
				next = current + 1
			}
			current = next
			if transitions == budget {
				return invalidField("steps", "path exceeds transition budget %d", budget)
			}
		}
	}
	return nil
}

func sortedWorkflowIDs(values map[string]Workflow) []string {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
