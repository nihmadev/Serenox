package jobs

import (
	"container/heap"
	"context"
	"sync/atomic"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func (s *Scheduler) SetOwnerState(ctx context.Context, pluginID string, accountID int64, state protocol.JobState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for jobKey, record := range s.records {
		identity := record.Spec.Identity
		if identity.PluginID != pluginID || (accountID != 0 && identity.AccountID != accountID) || record.Status.State == protocol.JobStateCancelled {
			continue
		}
		previousState := record.Status.State
		record.Status.State = state
		record.Status.Revision++
		record.Spec.Revision = record.Status.Revision
		if state == protocol.JobStateEnabled && (record.Status.NextRunUnixMS == 0 || previousState == protocol.JobStateInterrupted) {
			next, err := initialRun(record.Spec, s.config.Clock.Now())
			if err != nil {
				return err
			}
			record.Status.NextRunUnixMS = next.UnixMilli()
			heap.Push(&s.queue, &item{key: jobKey, at: next})
		}
		if record.Spec.Persistent {
			if err := s.store.Update(ctx, record, record.Status.Revision-1); err != nil {
				return err
			}
		}
		s.records[jobKey] = record
	}
	s.signal()
	return nil
}

func (s *Scheduler) Trigger(ctx context.Context, identity protocol.JobIdentity, revision uint64) (protocol.JobTriggerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key(identity)]
	if !ok {
		return protocol.JobTriggerResponse{}, ErrNotFound
	}
	if revision != 0 && revision != record.Status.Revision {
		return protocol.JobTriggerResponse{}, ErrRevision
	}
	now := s.config.Clock.Now()
	if err := s.fireLocked(ctx, record, now, now); err != nil {
		return protocol.JobTriggerResponse{}, err
	}
	return protocol.JobTriggerResponse{Accepted: true, Queued: true, Status: s.records[key(identity)].Status}, nil
}
func (s *Scheduler) Get(ctx context.Context, identity protocol.JobIdentity) (protocol.JobStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key(identity)]
	if !ok {
		var err error
		record, err = s.store.Get(ctx, identity)
		if err != nil {
			return protocol.JobStatus{}, err
		}
	}
	return record.Status, nil
}
func (s *Scheduler) List(ctx context.Context, pluginID string, accountID int64, limit int) ([]protocol.JobStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.store.List(ctx, pluginID, accountID, limit)
	if err != nil {
		return nil, err
	}
	result := make([]protocol.JobStatus, len(records))
	for i := range records {
		if current, ok := s.records[key(records[i].Spec.Identity)]; ok {
			result[i] = current.Status
		} else {
			result[i] = records[i].Status
		}
	}
	return result, nil
}
func (s *Scheduler) Inspect(ctx context.Context, identity protocol.JobIdentity) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record, ok := s.records[key(identity)]; ok {
		return cloneRecord(record), nil
	}
	return s.store.Get(ctx, identity)
}

func (s *Scheduler) Delete(ctx context.Context, identity protocol.JobIdentity, revision uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key(identity)]
	if !ok {
		return ErrNotFound
	}
	if revision != 0 && revision != record.Status.Revision {
		return ErrRevision
	}
	if record.Spec.Persistent {
		if err := s.store.Delete(ctx, identity, revision); err != nil {
			return err
		}
	}
	delete(s.records, key(identity))
	s.signal()
	return nil
}

func (s *Scheduler) Clear(ctx context.Context, identity protocol.JobIdentity, revision uint64) (protocol.JobStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key(identity)]
	if !ok {
		return protocol.JobStatus{}, ErrNotFound
	}
	if revision != 0 && revision != record.Status.Revision {
		return protocol.JobStatus{}, ErrRevision
	}
	record.Status.LastRun = protocol.JobExecutionAttempt{}
	record.History = nil
	if record.Spec.Persistent {
		if err := s.store.Update(ctx, record, record.Status.Revision); err != nil {
			return protocol.JobStatus{}, err
		}
	}
	s.records[key(identity)] = record
	return record.Status, nil
}

func (s *Scheduler) Health() protocol.SchedulerHealth {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := 0
	for _, record := range s.records {
		active += record.Status.Active
	}
	return protocol.SchedulerHealth{Registered: len(s.records), Maximum: s.config.Maximum, Due: len(s.queue), Active: active, LoopAlive: s.loopAlive.Load(), StoreFailures: atomic.LoadUint64(&s.storeFailures), RestoreFailures: atomic.LoadUint64(&s.restoreFailures), Interrupted: atomic.LoadUint64(&s.interrupted)}
}
