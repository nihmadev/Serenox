package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/nihmadev/Serenox/internal/storage"
	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

var (
	ErrDuplicate = errors.New("jobs: duplicate job")
	ErrNotFound  = errors.New("jobs: job not found")
	ErrRevision  = errors.New("jobs: stale revision")
	ErrCapacity  = errors.New("jobs: capacity reached")
)

type Record struct {
	Spec    protocol.JobSpec               `json:"spec"`
	Status  protocol.JobStatus             `json:"status"`
	History []protocol.JobExecutionAttempt `json:"history,omitempty"`
}
type JobStore interface {
	Create(context.Context, Record) error
	Get(context.Context, protocol.JobIdentity) (Record, error)
	Update(context.Context, Record, uint64) error
	Delete(context.Context, protocol.JobIdentity, uint64) error
	List(context.Context, string, int64, int) ([]Record, error)
}

type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Record
	maximum int
}

func NewMemoryStore(maximum int) *MemoryStore {
	if maximum <= 0 {
		maximum = protocol.MaxJobsPerPlugin
	}
	return &MemoryStore{records: make(map[string]Record), maximum: maximum}
}
func (s *MemoryStore) Create(ctx context.Context, record Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := identityKey(record.Spec.Identity)
	if _, ok := s.records[key]; ok {
		return ErrDuplicate
	}
	if len(s.records) >= s.maximum {
		return ErrCapacity
	}
	s.records[key] = cloneRecord(record)
	return nil
}
func (s *MemoryStore) Get(ctx context.Context, identity protocol.JobIdentity) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[identityKey(identity)]
	if !ok {
		return Record{}, ErrNotFound
	}
	return cloneRecord(record), nil
}
func (s *MemoryStore) Update(ctx context.Context, record Record, revision uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := identityKey(record.Spec.Identity)
	current, ok := s.records[key]
	if !ok {
		return ErrNotFound
	}
	if current.Status.Revision != revision {
		return ErrRevision
	}
	s.records[key] = cloneRecord(record)
	return nil
}
func (s *MemoryStore) Delete(ctx context.Context, identity protocol.JobIdentity, revision uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := identityKey(identity)
	current, ok := s.records[key]
	if !ok {
		return ErrNotFound
	}
	if revision != 0 && current.Status.Revision != revision {
		return ErrRevision
	}
	delete(s.records, key)
	return nil
}
func (s *MemoryStore) List(ctx context.Context, pluginID string, accountID int64, limit int) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Record, 0)
	for _, record := range s.records {
		id := record.Spec.Identity
		if pluginID != "" && id.PluginID != pluginID {
			continue
		}
		if accountID != 0 && id.AccountID != accountID {
			continue
		}
		result = append(result, cloneRecord(record))
	}
	sort.Slice(result, func(i, j int) bool {
		return identityKey(result[i].Spec.Identity) < identityKey(result[j].Spec.Identity)
	})
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

type StorageStore struct {
	mu        sync.Mutex
	store     storage.Store
	namespace string
	maximum   int
}

func NewStorageStore(store storage.Store, maximum int) (*StorageStore, error) {
	if store == nil {
		return nil, errors.New("jobs: storage is required")
	}
	if maximum <= 0 {
		maximum = protocol.MaxJobsPerPlugin
	}
	return &StorageStore{store: store, namespace: "serenox/jobs/v1", maximum: maximum}, nil
}
func (s *StorageStore) Create(ctx context.Context, record Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.getUnlocked(ctx, record.Spec.Identity); err == nil {
		return ErrDuplicate
	}
	entries, err := s.store.List(ctx, s.namespace)
	if err != nil {
		return err
	}
	if len(entries) >= s.maximum {
		return ErrCapacity
	}
	return s.write(ctx, record)
}
func (s *StorageStore) Get(ctx context.Context, identity protocol.JobIdentity) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getUnlocked(ctx, identity)
}
func (s *StorageStore) getUnlocked(ctx context.Context, identity protocol.JobIdentity) (Record, error) {
	raw, found, err := s.store.Get(ctx, s.namespace, identityKey(identity))
	if err != nil {
		return Record{}, err
	}
	if !found {
		return Record{}, ErrNotFound
	}
	var record Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return Record{}, fmt.Errorf("jobs: corrupt record: %w", err)
	}
	return record, nil
}
func (s *StorageStore) Update(ctx context.Context, record Record, revision uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.getUnlocked(ctx, record.Spec.Identity)
	if err != nil {
		return err
	}
	if current.Status.Revision != revision {
		return ErrRevision
	}
	return s.write(ctx, record)
}
func (s *StorageStore) Delete(ctx context.Context, identity protocol.JobIdentity, revision uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.getUnlocked(ctx, identity)
	if err != nil {
		return err
	}
	if revision != 0 && current.Status.Revision != revision {
		return ErrRevision
	}
	return s.store.Delete(ctx, s.namespace, identityKey(identity))
}
func (s *StorageStore) List(ctx context.Context, pluginID string, accountID int64, limit int) ([]Record, error) {
	entries, err := s.store.List(ctx, s.namespace)
	if err != nil {
		return nil, err
	}
	result := make([]Record, 0, len(entries))
	for _, entry := range entries {
		var record Record
		if err := json.Unmarshal(entry.Value, &record); err != nil {
			return nil, fmt.Errorf("jobs: corrupt record %q: %w", entry.Key, err)
		}
		id := record.Spec.Identity
		if (pluginID == "" || id.PluginID == pluginID) && (accountID == 0 || id.AccountID == accountID) {
			result = append(result, record)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return identityKey(result[i].Spec.Identity) < identityKey(result[j].Spec.Identity)
	})
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}
func (s *StorageStore) write(ctx context.Context, record Record) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return s.store.Set(ctx, s.namespace, identityKey(record.Spec.Identity), raw)
}
func identityKey(identity protocol.JobIdentity) string {
	return fmt.Sprintf("%s/%d/%s", identity.PluginID, identity.AccountID, identity.ID)
}
func cloneRecord(record Record) Record {
	copy := record
	copy.Spec.Payload = append([]byte(nil), record.Spec.Payload...)
	copy.History = append([]protocol.JobExecutionAttempt(nil), record.History...)
	return copy
}
