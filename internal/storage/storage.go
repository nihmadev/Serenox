// Package storage provides bounded, namespaced JSON persistence.
package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const schemaVersion = 1

var (
	ErrClosed       = errors.New("storage: store is closed")
	ErrInvalidName  = errors.New("storage: invalid namespace or key")
	ErrInvalidValue = errors.New("storage: value is not valid JSON")
	ErrLimit        = errors.New("storage: configured limit exceeded")
)

// Options contains hard storage bounds. Zero fields use conservative defaults.
type Options struct {
	MaxNamespaces       int
	MaxKeysPerNamespace int
	MaxTotalEntries     int
	MaxNamespaceBytes   int
	MaxKeyBytes         int
	MaxValueBytes       int
	MaxFileBytes        int64
}

// DefaultOptions returns production defaults which are intentionally finite.
func DefaultOptions() Options {
	return Options{
		MaxNamespaces:       256,
		MaxKeysPerNamespace: 2048,
		MaxTotalEntries:     64 * 1024,
		MaxNamespaceBytes:   128,
		MaxKeyBytes:         256,
		MaxValueBytes:       1024 * 1024,
		MaxFileBytes:        32 * 1024 * 1024,
	}
}

func (o Options) normalized() (Options, error) {
	d := DefaultOptions()
	if o.MaxNamespaces == 0 {
		o.MaxNamespaces = d.MaxNamespaces
	}
	if o.MaxKeysPerNamespace == 0 {
		o.MaxKeysPerNamespace = d.MaxKeysPerNamespace
	}
	if o.MaxTotalEntries == 0 {
		o.MaxTotalEntries = d.MaxTotalEntries
	}
	if o.MaxNamespaceBytes == 0 {
		o.MaxNamespaceBytes = d.MaxNamespaceBytes
	}
	if o.MaxKeyBytes == 0 {
		o.MaxKeyBytes = d.MaxKeyBytes
	}
	if o.MaxValueBytes == 0 {
		o.MaxValueBytes = d.MaxValueBytes
	}
	if o.MaxFileBytes == 0 {
		o.MaxFileBytes = d.MaxFileBytes
	}
	if o.MaxNamespaces < 1 || o.MaxKeysPerNamespace < 1 || o.MaxTotalEntries < 1 ||
		o.MaxNamespaceBytes < 1 || o.MaxKeyBytes < 1 || o.MaxValueBytes < 1 || o.MaxFileBytes < 1 {
		return Options{}, fmt.Errorf("%w: all bounds must be positive", ErrLimit)
	}
	return o, nil
}

// Validate reports whether all explicitly configured bounds are usable.
func (o Options) Validate() error {
	_, err := o.normalized()
	return err
}

// Entry is an immutable copy of a stored JSON value.
type Entry struct {
	Key   string
	Value json.RawMessage
}

type OperationType uint8

const (
	OperationSet OperationType = iota + 1
	OperationDelete
)

// Operation is one mutation in an atomic Batch.
type Operation struct {
	Type      OperationType
	Namespace string
	Key       string
	Value     json.RawMessage
}

// Stats is a point-in-time, bounded-cardinality store snapshot.
type Stats struct {
	Namespaces int
	Entries    int
	ValueBytes int64
	FileBytes  int64
}

// Store is the storage contract exposed to modules and plugin bridges.
type Store interface {
	Get(context.Context, string, string) (json.RawMessage, bool, error)
	Set(context.Context, string, string, json.RawMessage) error
	Delete(context.Context, string, string) error
	List(context.Context, string) ([]Entry, error)
	Batch(context.Context, []Operation) error
	Stats() Stats
	Close() error
}

type diskState struct {
	SchemaVersion int                                   `json:"schema_version"`
	Namespaces    map[string]map[string]json.RawMessage `json:"namespaces"`
}

type core struct {
	mu       sync.RWMutex
	opts     Options
	state    diskState
	closed   bool
	persist  func([]byte) error
	fileSize int64
}

// FileStore atomically replaces a mode-0600 JSON snapshot on every committed batch.
type FileStore struct{ c *core }

// MemoryStore implements the same bounded semantics without persistence.
type MemoryStore struct{ c *core }

// NewMemory constructs an empty bounded in-memory store.
func NewMemory(opts Options) (*MemoryStore, error) {
	c, err := newCore(opts)
	if err != nil {
		return nil, err
	}
	c.persist = func([]byte) error { return nil }
	return &MemoryStore{c: c}, nil
}

// NewFile opens path without reading or modifying legacy Hikka state.
func NewFile(path string, opts Options) (*FileStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("storage: empty path")
	}
	c, err := newCore(opts)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("storage: create directory: %w", err)
	}
	if err := loadFile(abs, c); err != nil {
		return nil, err
	}
	c.persist = func(data []byte) error { return writeAtomic(abs, data) }
	return &FileStore{c: c}, nil
}

func newCore(opts Options) (*core, error) {
	opts, err := opts.normalized()
	if err != nil {
		return nil, err
	}
	return &core{
		opts:  opts,
		state: diskState{SchemaVersion: schemaVersion, Namespaces: make(map[string]map[string]json.RawMessage)},
	}, nil
}

func loadFile(path string, c *core) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("storage: open: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("storage: stat: %w", err)
	}
	if info.Size() > c.opts.MaxFileBytes {
		return fmt.Errorf("%w: file is %d bytes", ErrLimit, info.Size())
	}
	data, err := io.ReadAll(io.LimitReader(f, c.opts.MaxFileBytes+1))
	if err != nil {
		return fmt.Errorf("storage: read: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var state diskState
	if err := dec.Decode(&state); err != nil {
		return fmt.Errorf("storage: decode: %w", err)
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("storage: trailing JSON data")
	}
	if state.SchemaVersion != schemaVersion {
		return fmt.Errorf("storage: unsupported schema version %d", state.SchemaVersion)
	}
	if state.Namespaces == nil {
		state.Namespaces = make(map[string]map[string]json.RawMessage)
	}
	if err := validateState(state, c.opts); err != nil {
		return err
	}
	c.state = cloneState(state)
	c.fileSize = info.Size()
	return nil
}

func writeAtomic(path string, data []byte) (retErr error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("storage: create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("storage: chmod temporary file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("storage: write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("storage: sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("storage: close temporary file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("storage: replace snapshot: %w", err)
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("storage: open parent directory: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("storage: sync parent directory: %w", err)
	}
	return nil
}

func (c *core) get(ctx context.Context, namespace, key string) (json.RawMessage, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := validateName(namespace, c.opts.MaxNamespaceBytes); err != nil {
		return nil, false, err
	}
	if err := validateName(key, c.opts.MaxKeyBytes); err != nil {
		return nil, false, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		return nil, false, ErrClosed
	}
	v, ok := c.state.Namespaces[namespace][key]
	if !ok {
		return nil, false, nil
	}
	return bytes.Clone(v), true, nil
}

func (c *core) list(ctx context.Context, namespace string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateName(namespace, c.opts.MaxNamespaceBytes); err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		return nil, ErrClosed
	}
	ns := c.state.Namespaces[namespace]
	entries := make([]Entry, 0, len(ns))
	for k, v := range ns {
		entries = append(entries, Entry{Key: k, Value: bytes.Clone(v)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return entries, nil
}

func (c *core) batch(ctx context.Context, operations []Operation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(operations) == 0 {
		return nil
	}
	if len(operations) > c.opts.MaxTotalEntries {
		return fmt.Errorf("%w: batch operations", ErrLimit)
	}
	for i := range operations {
		op := &operations[i]
		if op.Type != OperationSet && op.Type != OperationDelete {
			return fmt.Errorf("storage: operation %d has invalid type", i)
		}
		if err := validateName(op.Namespace, c.opts.MaxNamespaceBytes); err != nil {
			return fmt.Errorf("storage: operation %d namespace: %w", i, err)
		}
		if err := validateName(op.Key, c.opts.MaxKeyBytes); err != nil {
			return fmt.Errorf("storage: operation %d key: %w", i, err)
		}
		if op.Type == OperationSet {
			if len(op.Value) > c.opts.MaxValueBytes {
				return fmt.Errorf("%w: operation %d value", ErrLimit, i)
			}
			if !json.Valid(op.Value) {
				return fmt.Errorf("%w: operation %d", ErrInvalidValue, i)
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	next := cloneState(c.state)
	for _, op := range operations {
		switch op.Type {
		case OperationSet:
			ns, ok := next.Namespaces[op.Namespace]
			if !ok {
				ns = make(map[string]json.RawMessage)
				next.Namespaces[op.Namespace] = ns
			}
			ns[op.Key] = bytes.Clone(op.Value)
		case OperationDelete:
			if ns := next.Namespaces[op.Namespace]; ns != nil {
				delete(ns, op.Key)
				if len(ns) == 0 {
					delete(next.Namespaces, op.Namespace)
				}
			}
		}
	}
	if err := validateState(next, c.opts); err != nil {
		return err
	}
	data, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("storage: encode snapshot: %w", err)
	}
	if int64(len(data)) > c.opts.MaxFileBytes {
		return fmt.Errorf("%w: encoded file is %d bytes", ErrLimit, len(data))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.persist(data); err != nil {
		return err
	}
	c.state = next
	c.fileSize = int64(len(data))
	return nil
}

func validateName(s string, max int) error {
	if s == "" || len(s) > max || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return ErrInvalidName
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return ErrInvalidName
		}
	}
	return nil
}

func validateState(s diskState, opts Options) error {
	if len(s.Namespaces) > opts.MaxNamespaces {
		return fmt.Errorf("%w: namespaces", ErrLimit)
	}
	total := 0
	for namespace, values := range s.Namespaces {
		if err := validateName(namespace, opts.MaxNamespaceBytes); err != nil {
			return err
		}
		if len(values) > opts.MaxKeysPerNamespace {
			return fmt.Errorf("%w: keys in namespace %q", ErrLimit, namespace)
		}
		total += len(values)
		for key, value := range values {
			if err := validateName(key, opts.MaxKeyBytes); err != nil {
				return err
			}
			if len(value) > opts.MaxValueBytes {
				return fmt.Errorf("%w: value %q/%q", ErrLimit, namespace, key)
			}
			if !json.Valid(value) {
				return fmt.Errorf("%w: %q/%q", ErrInvalidValue, namespace, key)
			}
		}
	}
	if total > opts.MaxTotalEntries {
		return fmt.Errorf("%w: total entries", ErrLimit)
	}
	return nil
}

func cloneState(src diskState) diskState {
	dst := diskState{SchemaVersion: src.SchemaVersion, Namespaces: make(map[string]map[string]json.RawMessage, len(src.Namespaces))}
	for name, values := range src.Namespaces {
		copyValues := make(map[string]json.RawMessage, len(values))
		for key, value := range values {
			copyValues[key] = bytes.Clone(value)
		}
		dst.Namespaces[name] = copyValues
	}
	return dst
}

func (c *core) stats() Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := Stats{Namespaces: len(c.state.Namespaces), FileBytes: c.fileSize}
	for _, ns := range c.state.Namespaces {
		s.Entries += len(ns)
		for _, v := range ns {
			s.ValueBytes += int64(len(v))
		}
	}
	return s
}

func (c *core) close() error { c.mu.Lock(); defer c.mu.Unlock(); c.closed = true; return nil }

func (s *FileStore) Get(c context.Context, n, k string) (json.RawMessage, bool, error) {
	return s.c.get(c, n, k)
}
func (s *FileStore) Set(c context.Context, n, k string, v json.RawMessage) error {
	return s.c.batch(c, []Operation{{Type: OperationSet, Namespace: n, Key: k, Value: v}})
}
func (s *FileStore) Delete(c context.Context, n, k string) error {
	return s.c.batch(c, []Operation{{Type: OperationDelete, Namespace: n, Key: k}})
}
func (s *FileStore) List(c context.Context, n string) ([]Entry, error) { return s.c.list(c, n) }
func (s *FileStore) Batch(c context.Context, o []Operation) error      { return s.c.batch(c, o) }
func (s *FileStore) Stats() Stats                                      { return s.c.stats() }
func (s *FileStore) Close() error                                      { return s.c.close() }

func (s *MemoryStore) Get(c context.Context, n, k string) (json.RawMessage, bool, error) {
	return s.c.get(c, n, k)
}
func (s *MemoryStore) Set(c context.Context, n, k string, v json.RawMessage) error {
	return s.c.batch(c, []Operation{{Type: OperationSet, Namespace: n, Key: k, Value: v}})
}
func (s *MemoryStore) Delete(c context.Context, n, k string) error {
	return s.c.batch(c, []Operation{{Type: OperationDelete, Namespace: n, Key: k}})
}
func (s *MemoryStore) List(c context.Context, n string) ([]Entry, error) { return s.c.list(c, n) }
func (s *MemoryStore) Batch(c context.Context, o []Operation) error      { return s.c.batch(c, o) }
func (s *MemoryStore) Stats() Stats                                      { return s.c.stats() }
func (s *MemoryStore) Close() error                                      { return s.c.close() }

// SetJSON encodes v and commits it as one JSON value.
func SetJSON(ctx context.Context, store Store, namespace, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("storage: encode value: %w", err)
	}
	return store.Set(ctx, namespace, key, raw)
}

// DecodeJSON decodes a value previously returned by Get.
func DecodeJSON[T any](raw json.RawMessage) (T, error) {
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, fmt.Errorf("storage: decode value: %w", err)
	}
	return value, nil
}
