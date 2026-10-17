package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func renderTemplate(input string, run Run, policy Policy) (string, error) {
	var output bytes.Buffer
	for offset := 0; offset < len(input); {
		open := strings.Index(input[offset:], "{{")
		closeOnly := strings.Index(input[offset:], "}}")
		if closeOnly >= 0 && (open < 0 || closeOnly < open) {
			return "", fmt.Errorf("malformed template closing braces")
		}
		if open < 0 {
			output.WriteString(input[offset:])
			break
		}
		open += offset
		output.WriteString(input[offset:open])
		close := strings.Index(input[open+2:], "}}")
		if close < 0 {
			return "", fmt.Errorf("unterminated template variable")
		}
		close += open + 2
		name := strings.TrimSpace(input[open+2 : close])
		if strings.Contains(name, "{{") || name == "" {
			return "", fmt.Errorf("malformed template variable")
		}
		value, err := templateValue(name, run)
		if err != nil {
			return "", err
		}
		output.WriteString(value)
		if output.Len() > policy.MaxRenderedBytes {
			return "", fmt.Errorf("rendered text exceeds %d bytes", policy.MaxRenderedBytes)
		}
		offset = close + 2
	}
	if output.Len() > policy.MaxRenderedBytes {
		return "", fmt.Errorf("rendered text exceeds %d bytes", policy.MaxRenderedBytes)
	}
	return output.String(), nil
}

func templateValue(name string, run Run) (string, error) {
	switch name {
	case "workflow.id":
		return run.WorkflowID, nil
	case "run.id":
		return run.ID, nil
	case "message.chat_id":
		return strconv.FormatInt(run.Source.ChatID, 10), nil
	case "message.text":
		return scalarString(run.Context["message.text"], name)
	case "http.status":
		return scalarString(run.Context["http.status"], name)
	case "approval.decision":
		if run.Approval == nil {
			return "", nil
		}
		return run.Approval.Decision, nil
	}
	if strings.HasPrefix(name, "vars.") && identifierPattern.MatchString(strings.TrimPrefix(name, "vars.")) {
		return scalarString(run.Context[name], name)
	}
	return "", fmt.Errorf("unknown template variable %q", name)
}

func scalarString(raw json.RawMessage, name string) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("template variable %q is invalid", name)
	}
	switch typed := value.(type) {
	case nil:
		return "", nil
	case string:
		return typed, nil
	case bool:
		return strconv.FormatBool(typed), nil
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("template variable %q is not scalar", name)
	}
}
