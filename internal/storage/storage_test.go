package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestFileStorePersistsAtomicSnapshot(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	store, err := NewFile(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Batch(context.Background(), []Operation{
		{Type: OperationSet, Namespace: "plugin.one", Key: "enabled", Value: json.RawMessage(`true`)},
		{Type: OperationSet, Namespace: "plugin.one", Key: "count", Value: json.RawMessage(`42`)},
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewFile(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	raw, ok, err := reopened.Get(context.Background(), "plugin.one", "count")
	if err != nil || !ok || string(raw) != "42" {
		t.Fatalf("Get = %s, %v, %v", raw, ok, err)
	}
	entries, err := reopened.List(context.Background(), "plugin.one")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Key != "count" || entries[1].Key != "enabled" {
		t.Fatalf("entries = %#v", entries)
	}
	for _, match := range []string{".state.json.tmp-*", ".state.json.tmp-*"} {
		leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), match))
		if err != nil {
			t.Fatal(err)
		}
		if len(leftovers) != 0 {
			t.Fatalf("temporary files remain: %v", leftovers)
		}
	}
}

func TestBatchFailureDoesNotMutateState(t *testing.T) {
	t.Parallel()
	store, err := NewMemory(Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Set(ctx, "plugin", "key", json.RawMessage(`"old"`)); err != nil {
		t.Fatal(err)
	}
	store.c.persist = func([]byte) error { return errors.New("disk full") }
	err = store.Batch(ctx, []Operation{{Type: OperationSet, Namespace: "plugin", Key: "key", Value: json.RawMessage(`"new"`)}})
	if err == nil || err.Error() != "disk full" {
		t.Fatalf("Batch error = %v", err)
	}
	raw, ok, err := store.Get(ctx, "plugin", "key")
	if err != nil || !ok || string(raw) != `"old"` {
		t.Fatalf("state changed: %s %v %v", raw, ok, err)
	}
}

func TestBoundsAndValidation(t *testing.T) {
	t.Parallel()
	opts := DefaultOptions()
	opts.MaxNamespaces = 1
	opts.MaxKeysPerNamespace = 1
	opts.MaxTotalEntries = 1
	opts.MaxValueBytes = 4
	store, err := NewMemory(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Set(ctx, "a", "x", json.RawMessage(`null`)); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"namespace cap": store.Set(ctx, "b", "x", json.RawMessage(`null`)),
		"key cap":       store.Set(ctx, "a", "y", json.RawMessage(`null`)),
		"value cap":     store.Set(ctx, "a", "x", json.RawMessage(`12345`)),
		"invalid JSON":  store.Set(ctx, "a", "x", json.RawMessage(`{`)),
		"invalid name":  store.Set(ctx, " bad", "x", json.RawMessage(`null`)),
	} {
		if err == nil {
			t.Errorf("%s unexpectedly succeeded", name)
		}
	}
	if err := store.Delete(ctx, "a", "x"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, "b", "x", json.RawMessage(`true`)); err != nil {
		t.Fatalf("capacity not released: %v", err)
	}
}

func TestReturnedValuesAreCopies(t *testing.T) {
	t.Parallel()
	store, _ := NewMemory(Options{})
	ctx := context.Background()
	input := json.RawMessage(`{"value":1}`)
	if err := store.Set(ctx, "p", "k", input); err != nil {
		t.Fatal(err)
	}
	input[0] = '['
	raw, _, _ := store.Get(ctx, "p", "k")
	raw[0] = '['
	again, _, _ := store.Get(ctx, "p", "k")
	if string(again) != `{"value":1}` {
		t.Fatalf("stored value aliased caller: %s", again)
	}
}

func TestContextAndClose(t *testing.T) {
	t.Parallel()
	store, _ := NewMemory(Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Set(ctx, "p", "k", json.RawMessage(`1`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Set error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(context.Background(), "p", "k"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Get error = %v", err)
	}
}

func TestRejectsMalformedOrOversizeFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"schema_version":1,"namespaces":{}} trailing`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFile(bad, Options{}); err == nil {
		t.Fatal("malformed file accepted")
	}
	large := filepath.Join(dir, "large.json")
	if err := os.WriteFile(large, make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	opts.MaxFileBytes = 50
	if _, err := NewFile(large, opts); !errors.Is(err, ErrLimit) {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestConcurrentAccessRemainsWithinBounds(t *testing.T) {
	store, err := NewMemory(Options{})
	if err != nil {
		t.Fatal(err)
	}
	const goroutines, writes = 16, 100
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < writes; j++ {
				key := fmt.Sprintf("%d-%d", id, j)
				if err := store.Set(context.Background(), "plugin", key, json.RawMessage(`1`)); err != nil {
					t.Errorf("Set: %v", err)
					return
				}
				if _, _, err := store.Get(context.Background(), "plugin", key); err != nil {
					t.Errorf("Get: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	if got := store.Stats().Entries; got != goroutines*writes {
		t.Fatalf("entries = %d", got)
	}
}

func TestJSONHelpers(t *testing.T) {
	t.Parallel()
	store, _ := NewMemory(Options{})
	type value struct {
		Name string `json:"name"`
	}
	if err := SetJSON(context.Background(), store, "p", "k", value{Name: "ok"}); err != nil {
		t.Fatal(err)
	}
	raw, _, _ := store.Get(context.Background(), "p", "k")
	decoded, err := DecodeJSON[value](raw)
	if err != nil || decoded.Name != "ok" {
		t.Fatalf("DecodeJSON = %#v, %v", decoded, err)
	}
}

func BenchmarkMemoryStoreSet(b *testing.B) {
	store, _ := NewMemory(Options{})
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = store.Set(ctx, "benchmark", fmt.Sprintf("key-%d", i%128), json.RawMessage(`{"ok":true}`))
	}
}
