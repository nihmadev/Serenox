package plugin

import (
	"context"
	"encoding/json"
	"fmt"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type storageRPCControl interface {
	Call(context.Context, protocol.Method, any, any) error
}

// StorageClient exposes durable plugin/account-namespaced JSON storage. Set,
// Delete, and Batch return only after the host reports a committed mutation.
type StorageClient struct{ control storageRPCControl }

func newStorageClient(control storageRPCControl) *StorageClient {
	return &StorageClient{control: control}
}

func (c *StorageClient) request(ctx context.Context, request protocol.StorageRequest) (protocol.StorageResult, error) {
	if c == nil || c.control == nil {
		return protocol.StorageResult{}, fmt.Errorf("storage service is unavailable")
	}
	if err := request.Validate(); err != nil {
		return protocol.StorageResult{}, err
	}
	var result protocol.StorageResult
	if err := c.control.Call(ctx, protocol.MethodHostStorage, request, &result); err != nil {
		return protocol.StorageResult{}, err
	}
	if err := result.Validate(); err != nil {
		return protocol.StorageResult{}, err
	}
	return result, nil
}

func (c *StorageClient) Get(ctx context.Context, namespace, key string) (json.RawMessage, bool, error) {
	result, err := c.request(ctx, protocol.StorageRequest{Operation: protocol.StorageGet, Namespace: namespace, Key: key})
	if err != nil {
		return nil, false, err
	}
	return append(json.RawMessage(nil), result.Value...), result.Found, nil
}

func (c *StorageClient) Set(ctx context.Context, namespace, key string, value json.RawMessage) error {
	result, err := c.request(ctx, protocol.StorageRequest{Operation: protocol.StorageSet, Namespace: namespace, Key: key, Value: value})
	if err != nil {
		return err
	}
	if !result.Committed {
		return fmt.Errorf("host did not confirm storage commit")
	}
	return nil
}

func (c *StorageClient) Delete(ctx context.Context, namespace, key string) error {
	result, err := c.request(ctx, protocol.StorageRequest{Operation: protocol.StorageDelete, Namespace: namespace, Key: key})
	if err != nil {
		return err
	}
	if !result.Committed {
		return fmt.Errorf("host did not confirm storage commit")
	}
	return nil
}

func (c *StorageClient) List(ctx context.Context, namespace string) ([]protocol.StorageEntry, error) {
	result, err := c.request(ctx, protocol.StorageRequest{Operation: protocol.StorageList, Namespace: namespace})
	if err != nil {
		return nil, err
	}
	entries := append([]protocol.StorageEntry(nil), result.Entries...)
	for index := range entries {
		entries[index].Value = append(json.RawMessage(nil), entries[index].Value...)
	}
	return entries, nil
}

func (c *StorageClient) Batch(ctx context.Context, mutations []protocol.StorageMutation) error {
	copyOfMutations := append([]protocol.StorageMutation(nil), mutations...)
	for index := range copyOfMutations {
		copyOfMutations[index].Value = append(json.RawMessage(nil), copyOfMutations[index].Value...)
	}
	result, err := c.request(ctx, protocol.StorageRequest{Operation: protocol.StorageBatch, Batch: copyOfMutations})
	if err != nil {
		return err
	}
	if !result.Committed {
		return fmt.Errorf("host did not confirm storage batch commit")
	}
	return nil
}
