package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
	serenox "github.com/nihmadev/Serenox/sdk/go"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Advance(value time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(value)
	c.mu.Unlock()
}

type fakeIDs struct {
	mu   sync.Mutex
	next int
}

func (i *fakeIDs) New(prefix string) (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.next++
	return prefix + "00000000000000000" + string(rune('a'+i.next)), nil
}

type fakeStore struct {
	mu             sync.Mutex
	raw            json.RawMessage
	found          bool
	getErr, setErr error
	sets           int
}

func (s *fakeStore) Get(context.Context, string, string) (json.RawMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append(json.RawMessage(nil), s.raw...), s.found, s.getErr
}
func (s *fakeStore) Set(_ context.Context, _ string, _ string, value json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setErr != nil {
		return s.setErr
	}
	s.raw = append(json.RawMessage(nil), value...)
	s.found = true
	s.sets++
	return nil
}

type fakeActions struct {
	mu      sync.Mutex
	actions []protocol.Action
	results []protocol.ActionResult
	err     error
}

func (a *fakeActions) Apply(_ context.Context, action protocol.Action) (protocol.ActionResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.actions = append(a.actions, action)
	if a.err != nil {
		return protocol.ActionResult{}, a.err
	}
	if len(a.results) > 0 {
		result := a.results[0]
		a.results = a.results[1:]
		return result, nil
	}
	return protocol.ActionResult{ActionID: action.ID, Operation: action.OperationName(), Status: protocol.ActionResultSuccess, Success: true, MessageID: int64(len(a.actions) + 100)}, nil
}

type fakeJobs struct {
	mu      sync.Mutex
	specs   []protocol.JobSpec
	deleted []protocol.JobIdentity
	err     error
}

func (j *fakeJobs) Create(_ context.Context, spec protocol.JobSpec) (protocol.JobStatus, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.err != nil {
		return protocol.JobStatus{}, j.err
	}
	spec.Identity.PluginID = pluginID
	spec.Identity.AccountID = 42
	j.specs = append(j.specs, spec)
	return protocol.JobStatus{Identity: spec.Identity, State: protocol.JobStateEnabled, Revision: 1}, nil
}
func (j *fakeJobs) Get(_ context.Context, request protocol.JobGetRequest) (protocol.JobStatus, error) {
	return protocol.JobStatus{Identity: request.Identity, State: protocol.JobStateEnabled}, nil
}
func (j *fakeJobs) Delete(_ context.Context, request protocol.JobIdentityRequest) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.deleted = append(j.deleted, request.Identity)
	return nil
}

type fakeHTTP struct {
	response serenox.HTTPResponse
	err      error
	requests []protocol.HTTPRequest
}

func (h *fakeHTTP) Request(_ context.Context, request protocol.HTTPRequest) (serenox.HTTPResponse, error) {
	h.requests = append(h.requests, request)
	return h.response, h.err
}

type fakeExit struct {
	mu   sync.Mutex
	code int
}

func (e *fakeExit) Exit(code int) { e.mu.Lock(); e.code = code; e.mu.Unlock() }

func testHost() (Host, *fakeStore, *fakeActions, *fakeJobs) {
	store := &fakeStore{}
	actions := &fakeActions{}
	jobs := &fakeJobs{}
	return Host{Store: store, Actions: actions, Jobs: jobs, HTTP: &fakeHTTP{response: serenox.HTTPResponse{HTTPResponse: protocol.HTTPResponse{Status: 200}}}, Exit: &fakeExit{}}, store, actions, jobs
}
func testEngine() (*Engine, *fakeClock) {
	clock := &fakeClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	return newEngine(clock, &fakeIDs{}), clock
}

var errCommit = errors.New("commit failed")
