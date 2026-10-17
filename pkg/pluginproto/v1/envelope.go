package v1

import (
	"encoding/json"
	"fmt"
)

// Request is a host-to-plugin RPC envelope.
type Request struct {
	Protocol       string          `json:"protocol"`
	RequestID      uint64          `json:"request_id"`
	Kind           EnvelopeKind    `json:"kind"`
	DeadlineUnixMS int64           `json:"deadline_unix_ms"`
	Method         Method          `json:"method"`
	Payload        json.RawMessage `json:"payload,omitempty"`
}

// Response is a plugin-to-host RPC envelope.
type Response struct {
	Protocol       string          `json:"protocol"`
	RequestID      uint64          `json:"request_id"`
	Kind           EnvelopeKind    `json:"kind"`
	DeadlineUnixMS int64           `json:"deadline_unix_ms"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          *Error          `json:"error,omitempty"`
}

// Error is the stable wire error representation.
type Error struct {
	Code             ErrorCode         `json:"code"`
	Message          string            `json:"message"`
	Operation        string            `json:"operation,omitempty"`
	Retryable        bool              `json:"retryable,omitempty"`
	FloodWaitSeconds int64             `json:"flood_wait_seconds,omitempty"`
	Field            string            `json:"field,omitempty"`
	Details          map[string]string `json:"details,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Field != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.Field)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}
