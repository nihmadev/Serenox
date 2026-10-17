package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/nihmadev/Serenox/internal/storage"
)

const fabricNamespace = "core.event_fabric.v1"

// StorageStore keeps each accepted event and its bounded fan-out in one JSON
// value. Therefore acceptance and delivery creation share one file-store commit.
type StorageStore struct {
	mu    sync.Mutex
	store storage.Store
}

func NewStorageStore(store storage.Store) (*StorageStore, error) {
	if store == nil {
		return nil, fmt.Errorf("eventbus: nil storage store")
	}
	return &StorageStore{store: store}, nil
}

func (s *StorageStore) Put(ctx context.Context, record Record, expected uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, found, err := s.get(ctx, record.Envelope.ID)
	if err != nil {
		return err
	}
	if (!found && expected != 0) || (found && current.Revision != expected) {
		return ErrStaleRevision
	}
	record.Revision = expected + 1
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return s.store.Set(ctx, fabricNamespace, record.Envelope.ID, data)
}

func (s *StorageStore) Get(ctx context.Context, id string) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.get(ctx, id)
}

func (s *StorageStore) get(ctx context.Context, id string) (Record, bool, error) {
	raw, found, err := s.store.Get(ctx, fabricNamespace, id)
	if err != nil || !found {
		return Record{}, found, err
	}
	var record Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return Record{}, false, fmt.Errorf("eventbus: corrupt record %s: %w", id, err)
	}
	if err := validateRecord(record); err != nil {
		return Record{}, false, fmt.Errorf("eventbus: corrupt record %s: %w", id, err)
	}
	return record, true, nil
}

func (s *StorageStore) List(ctx context.Context, limit int) ([]Record, error) {
	if limit < 1 {
		return nil, fmt.Errorf("eventbus: list limit must be positive")
	}
	entries, err := s.store.List(ctx, fabricNamespace)
	if err != nil {
		return nil, err
	}
	if len(entries) > limit {
		return nil, ErrQuota
	}
	result := make([]Record, 0, len(entries))
	for _, entry := range entries {
		var record Record
		if err := json.Unmarshal(entry.Value, &record); err != nil {
			return nil, fmt.Errorf("eventbus: corrupt record %s: %w", entry.Key, err)
		}
		if err := validateRecord(record); err != nil {
			return nil, fmt.Errorf("eventbus: corrupt record %s: %w", entry.Key, err)
		}
		result = append(result, record)
	}
	return result, nil
}

func (s *StorageStore) Delete(ctx context.Context, id string, expected uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, found, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	if record.Revision != expected {
		return ErrStaleRevision
	}
	return s.store.Delete(ctx, fabricNamespace, id)
}

func validateRecord(record Record) error {
	if record.SchemaVersion != 1 || record.Revision == 0 || record.ExpiresUnixMS <= record.Envelope.AcceptedUnixMS {
		return fmt.Errorf("invalid record metadata")
	}
	if err := record.Envelope.Validate(); err != nil {
		return err
	}
	if len(record.Deliveries) > recordFanoutLimit() {
		return fmt.Errorf("delivery fan-out exceeds bound")
	}
	for _, delivery := range record.Deliveries {
		if delivery.ID == "" || delivery.PluginID == "" || delivery.Revision == 0 || delivery.Attempt < 0 || len(delivery.LastError) > 1024 {
			return fmt.Errorf("invalid delivery")
		}
		if err := delivery.Subscription.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func recordFanoutLimit() int { return 32 }
