package v1

import (
	"encoding/json"
	"fmt"
)

func (r StorageRequest) Validate() error {
	switch r.Operation {
	case StorageGet, StorageSet, StorageDelete, StorageList:
		if err := validString("storage.namespace", r.Namespace, true); err != nil {
			return err
		}
		if r.Operation != StorageList {
			if err := validString("storage.key", r.Key, true); err != nil {
				return err
			}
		}
	case StorageBatch:
		if len(r.Batch) == 0 || len(r.Batch) > MaxStorageBatchItems {
			return invalid("storage.batch", "must contain 1..%d items", MaxStorageBatchItems)
		}
		for i, item := range r.Batch {
			if item.Operation != StorageSet && item.Operation != StorageDelete {
				return invalid(fmt.Sprintf("storage.batch[%d].operation", i), "must be set or delete")
			}
			if err := validString(fmt.Sprintf("storage.batch[%d].namespace", i), item.Namespace, true); err != nil {
				return err
			}
			if err := validString(fmt.Sprintf("storage.batch[%d].key", i), item.Key, true); err != nil {
				return err
			}
			if item.Operation == StorageSet && (len(item.Value) > MaxStorageValueBytes || !validJSONValue(item.Value)) {
				return invalid(fmt.Sprintf("storage.batch[%d].value", i), "must be one JSON value")
			}
		}
	default:
		return invalid("storage.operation", "unknown operation %q", r.Operation)
	}
	if r.Operation == StorageSet && (len(r.Value) > MaxStorageValueBytes || !validJSONValue(r.Value)) {
		return invalid("storage.value", "must be one JSON value")
	}
	return nil
}

func (r StorageResult) Validate() error {
	if len(r.Entries) > MaxCollectionItems {
		return invalid("storage.entries", "exceed limit %d", MaxCollectionItems)
	}
	for i, entry := range r.Entries {
		if err := validString(fmt.Sprintf("storage.entries[%d].key", i), entry.Key, true); err != nil {
			return err
		}
		if !validJSONValue(entry.Value) {
			return invalid(fmt.Sprintf("storage.entries[%d].value", i), "must be one JSON value")
		}
	}
	if len(r.Value) > MaxStorageValueBytes || (len(r.Value) != 0 && !validJSONValue(r.Value)) {
		return invalid("storage.value", "must be one JSON value")
	}
	return nil
}

func validJSONValue(value json.RawMessage) bool {
	if len(value) == 0 || !json.Valid(value) {
		return false
	}
	var decoded any
	return json.Unmarshal(value, &decoded) == nil
}
