package media

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestServiceUploadDownloadOwnershipAndCleanup(t *testing.T) {
	root := t.TempDir()
	svc, err := New(filepath.Join(root, "media"), Limits{MaxSize: 1024, QuotaBytes: 4096}, nil)
	if err != nil {
		t.Fatal(err)
	}
	owner := Owner{PluginID: "example.convert", AccountID: 42}
	data := bytes.Repeat([]byte("x"), 257)
	media, err := svc.Upload(context.Background(), owner, protocol.MediaRequest{Operation: protocol.MediaUpload, Kind: "document", FileName: "out.bin", Size: int64(len(data))}, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if media.Handle == "" || media.FileID != "" || media.Size != int64(len(data)) {
		t.Fatalf("unexpected media metadata: %#v", media)
	}
	var got bytes.Buffer
	if _, err := svc.Download(context.Background(), owner, media.Handle, &got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, got.Bytes()) {
		t.Fatal("download did not preserve bytes")
	}
	if _, err := svc.Inspect(context.Background(), Owner{PluginID: "other", AccountID: 42}, media.Handle); !errors.Is(err, ErrOwnership) {
		t.Fatalf("cross-owner inspect = %v", err)
	}
	if err := svc.Delete(context.Background(), owner, media.Handle); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Inspect(context.Background(), owner, media.Handle); !errors.Is(err, ErrInvalidHandle) {
		t.Fatalf("stale inspect = %v", err)
	}
	if err := svc.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "media")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestServiceRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	svc, err := New(filepath.Join(root, "media"), Limits{MaxSize: 1024, QuotaBytes: 4096}, nil)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Register(Owner{PluginID: "p", AccountID: 1}, protocol.Media{Kind: "document", Size: 6}, outside)
	if !errors.Is(err, ErrOwnership) {
		t.Fatalf("path traversal error = %v", err)
	}
}
