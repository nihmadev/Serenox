package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type storageRPCStub struct {
	request protocol.StorageRequest
	result  protocol.StorageResult
	err     error
}

func (s *storageRPCStub) Call(_ context.Context, method protocol.Method, input, output any) error {
	if method != protocol.MethodHostStorage {
		return errors.New("unexpected method")
	}
	s.request = input.(protocol.StorageRequest)
	if s.err != nil {
		return s.err
	}
	*(output.(*protocol.StorageResult)) = s.result
	return nil
}

func TestStorageClientCommittedOperations(t *testing.T) {
	rpc := &storageRPCStub{result: protocol.StorageResult{Committed: true}}
	client := newStorageClient(rpc)
	if err := client.Set(context.Background(), "autopilot", "state", json.RawMessage(`{"schema":1}`)); err != nil {
		t.Fatal(err)
	}
	if rpc.request.Operation != protocol.StorageSet || rpc.request.Namespace != "autopilot" {
		t.Fatalf("unexpected request: %#v", rpc.request)
	}
	if err := client.Batch(context.Background(), []protocol.StorageMutation{{Operation: protocol.StorageDelete, Namespace: "autopilot", Key: "old"}}); err != nil {
		t.Fatal(err)
	}
	if rpc.request.Operation != protocol.StorageBatch {
		t.Fatalf("operation = %q", rpc.request.Operation)
	}
}

func TestStorageClientGetCopiesAndRequiresCommit(t *testing.T) {
	rpc := &storageRPCStub{result: protocol.StorageResult{Found: true, Value: json.RawMessage(`{"ok":true}`)}}
	client := newStorageClient(rpc)
	value, found, err := client.Get(context.Background(), "n", "k")
	if err != nil || !found || string(value) != `{"ok":true}` {
		t.Fatalf("get = %s, %v, %v", value, found, err)
	}
	rpc.result = protocol.StorageResult{}
	if err := client.Delete(context.Background(), "n", "k"); err == nil {
		t.Fatal("expected missing commit acknowledgement")
	}
}
