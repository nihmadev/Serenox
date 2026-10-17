package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nihmadev/Serenox/internal/storage"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestHostStorageIsDurableAndNamespaced(t *testing.T) {
	store, err := storage.NewMemory(storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := &App{store: store}
	ctx := context.Background()
	value := json.RawMessage(`{"count":1}`)
	if result, err := a.HandleHostStorage(ctx, "alpha", 7, pluginv1.StorageRequest{Operation: pluginv1.StorageSet, Namespace: "prefs", Key: "x", Value: value}); err != nil || !result.Committed {
		t.Fatalf("set = %#v, %v", result, err)
	}
	if result, err := a.HandleHostStorage(ctx, "alpha", 7, pluginv1.StorageRequest{Operation: pluginv1.StorageGet, Namespace: "prefs", Key: "x"}); err != nil || !result.Found || string(result.Value) != string(value) {
		t.Fatalf("get = %#v, %v", result, err)
	}
	if result, err := a.HandleHostStorage(ctx, "beta", 7, pluginv1.StorageRequest{Operation: pluginv1.StorageGet, Namespace: "prefs", Key: "x"}); err != nil || result.Found {
		t.Fatalf("cross-plugin get = %#v, %v", result, err)
	}
	batch := pluginv1.StorageRequest{Operation: pluginv1.StorageBatch, Batch: []pluginv1.StorageMutation{{Operation: pluginv1.StorageSet, Namespace: "prefs", Key: "y", Value: json.RawMessage(`2`)}, {Operation: pluginv1.StorageDelete, Namespace: "prefs", Key: "x"}}}
	if result, err := a.HandleHostStorage(ctx, "alpha", 7, batch); err != nil || !result.Committed {
		t.Fatalf("batch = %#v, %v", result, err)
	}
}
