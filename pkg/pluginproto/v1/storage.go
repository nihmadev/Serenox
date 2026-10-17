package v1

import (
	"encoding/json"
)

type StorageOperation string

const (
	StorageGet    StorageOperation = "get"
	StorageSet    StorageOperation = "set"
	StorageDelete StorageOperation = "delete"
	StorageList   StorageOperation = "list"
	StorageBatch  StorageOperation = "batch"
)

// StorageMutation is deliberately typed. It is not an Action.Value payload.
type StorageMutation struct {
	Operation StorageOperation `json:"operation"`
	Namespace string           `json:"namespace"`
	Key       string           `json:"key"`
	Value     json.RawMessage  `json:"value,omitempty"`
}

type StorageRequest struct {
	Operation StorageOperation  `json:"operation"`
	Namespace string            `json:"namespace"`
	Key       string            `json:"key"`
	Value     json.RawMessage   `json:"value,omitempty"`
	Batch     []StorageMutation `json:"batch,omitempty"`
}

type StorageEntry struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

type StorageResult struct {
	Found     bool            `json:"found"`
	Value     json.RawMessage `json:"value,omitempty"`
	Entries   []StorageEntry  `json:"entries,omitempty"`
	Committed bool            `json:"committed,omitempty"`
}

func (m StorageMutation) Validate() error {
	if m.Operation != StorageSet && m.Operation != StorageDelete {
		return invalid("storage mutation.operation", "must be set or delete")
	}
	if err := validString("storage mutation.namespace", m.Namespace, true); err != nil {
		return err
	}
	if err := validString("storage mutation.key", m.Key, true); err != nil {
		return err
	}
	if m.Operation == StorageSet && (len(m.Value) > MaxStorageValueBytes || !validJSONValue(m.Value)) {
		return invalid("storage mutation.value", "must be one JSON value within limit")
	}
	return nil
}
