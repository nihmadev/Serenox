package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func MarshalPayload(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	return b, nil
}

func DecodePayload(payload json.RawMessage, dst any) error {
	if dst == nil {
		return nil
	}
	if len(payload) == 0 {
		return fmt.Errorf("%w: missing payload", ErrInvalidDTO)
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%w: decode payload: %v", ErrInvalidDTO, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: trailing payload data", ErrInvalidDTO)
		}
		return fmt.Errorf("%w: trailing payload data: %v", ErrInvalidDTO, err)
	}
	return nil
}
