package telegram

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type RecordedOptions struct {
	MaxUpdates int
	MaxLine    int
	MaxActions int
}

func (o RecordedOptions) normalized() (RecordedOptions, error) {
	if o.MaxUpdates == 0 {
		o.MaxUpdates = 10_000
	}
	if o.MaxLine == 0 {
		o.MaxLine = 1 << 20
	}
	if o.MaxActions == 0 {
		o.MaxActions = 10_000
	}
	if o.MaxUpdates < 1 || o.MaxLine < 1 || o.MaxActions < 1 {
		return RecordedOptions{}, errors.New("invalid recorded transport limits")
	}
	return o, nil
}

// RecordedTransport replays newline-delimited Update DTOs and records actions.
// It is deterministic and never starts a background goroutine.
type RecordedTransport struct {
	options RecordedOptions
	updates []pluginv1.Update
	mu      sync.Mutex
	actions []pluginv1.Action
	closed  bool
}

func LoadRecorded(path string, options RecordedOptions) (*RecordedTransport, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open recorded updates: %w", err)
	}
	defer file.Close()
	return ReadRecorded(file, options)
}

func ReadRecorded(reader io.Reader, options RecordedOptions) (*RecordedTransport, error) {
	normalized, err := options.normalized()
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), normalized.MaxLine)
	updates := make([]pluginv1.Update, 0, min(normalized.MaxUpdates, 256))
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if len(updates) >= normalized.MaxUpdates {
			return nil, errors.New("recorded update limit exceeded")
		}
		var update pluginv1.Update
		if err := json.Unmarshal(line, &update); err != nil {
			return nil, fmt.Errorf("decode recorded update %d: %w", len(updates)+1, err)
		}
		if err := update.Validate(); err != nil {
			return nil, fmt.Errorf("validate recorded update %d: %w", len(updates)+1, err)
		}
		updates = append(updates, update)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan recorded updates: %w", err)
	}
	return &RecordedTransport{options: normalized, updates: updates}, nil
}

func (t *RecordedTransport) Run(ctx context.Context, sink Sink) error {
	if sink == nil {
		return errors.New("nil recorded update sink")
	}
	for _, update := range t.updates {
		t.mu.Lock()
		closed := t.closed
		t.mu.Unlock()
		if closed {
			return ErrClosed
		}
		if err := sink.HandleUpdate(ctx, update); err != nil {
			return normalizeRunError(err)
		}
	}
	return nil
}

func (t *RecordedTransport) Apply(_ context.Context, action pluginv1.Action) (pluginv1.ActionResult, error) {
	if err := action.Validate(); err != nil {
		return pluginv1.ActionResult{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return pluginv1.ActionResult{}, ErrClosed
	}
	if len(t.actions) >= t.options.MaxActions {
		return pluginv1.ActionResult{}, ErrActionLimit
	}
	t.actions = append(t.actions, action)
	return pluginv1.ActionResult{ActionID: action.ID, Success: true}, nil
}

func (t *RecordedTransport) Actions() []pluginv1.Action {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]pluginv1.Action(nil), t.actions...)
}

func (t *RecordedTransport) Close(context.Context) error {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	return nil
}
