package media

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

// Source is the small adapter needed to materialize an incoming Telegram
// attachment. The media package deliberately knows nothing about gotd.
type TelegramSource interface {
	InspectMedia(context.Context, string) (protocol.Media, error)
	DownloadMedia(context.Context, string, io.Writer) (protocol.Media, error)
}

type uploadTransfer struct {
	owner   Owner
	request protocol.MediaRequest
	path    string
	next    int64
}

// Gateway translates bounded host.media calls into the daemon-owned store.
// The public handles remain opaque and ownership is checked on every call.
type Gateway struct {
	store  *Service
	root   string
	source TelegramSource

	mu       sync.Mutex
	aliases  map[string]string
	uploads  map[string]uploadTransfer
	finished map[string]struct {
		media protocol.Media
		owner Owner
	}
}

func NewGateway(root string, source TelegramSource, limits Limits) (*Gateway, error) {
	mediaRoot := filepath.Join(root, "media")
	store, err := New(mediaRoot, limits, nil)
	if err != nil {
		return nil, err
	}
	return &Gateway{store: store, root: mediaRoot, source: source,
		aliases: make(map[string]string), uploads: make(map[string]uploadTransfer),
		finished: make(map[string]struct {
			media protocol.Media
			owner Owner
		}),
	}, nil
}

func (g *Gateway) Close() error { return g.store.Cleanup() }

func (g *Gateway) Media(ctx context.Context, pluginID string, accountID int64, request protocol.MediaRequest) (protocol.MediaResult, error) {
	owner := Owner{PluginID: pluginID, AccountID: accountID}
	if err := request.Validate(); err != nil {
		return protocol.MediaResult{}, err
	}
	switch request.Operation {
	case protocol.MediaInspect:
		media, err := g.resolve(ctx, owner, request.Handle)
		if err != nil {
			return protocol.MediaResult{}, err
		}
		return protocol.MediaResult{Media: &media, ChunkSize: g.store.Limits().ChunkSize, MaxSize: g.store.Limits().MaxSize}, nil
	case protocol.MediaDownload:
		return g.download(ctx, owner, request)
	case protocol.MediaUpload:
		return g.upload(ctx, owner, request)
	default:
		return protocol.MediaResult{}, ErrUnsupported
	}
}

func (g *Gateway) resolve(ctx context.Context, owner Owner, handle string) (protocol.Media, error) {
	if finished, ok := g.finishedFor(owner, handle); ok {
		return finished, nil
	}
	if media, err := g.store.Inspect(ctx, owner, handle); err == nil {
		return media, nil
	}
	key := owner.PluginID + ":" + strconv.FormatInt(owner.AccountID, 10) + ":" + handle
	g.mu.Lock()
	stored := g.aliases[key]
	g.mu.Unlock()
	if stored != "" {
		media, err := g.store.Inspect(ctx, owner, stored)
		if err != nil {
			return protocol.Media{}, fmt.Errorf("%w: stored media alias %q: %v", ErrInvalidHandle, stored, err)
		}
		return media, nil
	}
	if g.source == nil {
		return protocol.Media{}, fmt.Errorf("%w: Telegram media source is unavailable", ErrInvalidHandle)
	}
	metadata, err := g.source.InspectMedia(ctx, handle)
	if err != nil {
		return protocol.Media{}, fmt.Errorf("%w: Telegram source inspect failed for %q: %v", ErrInvalidHandle, handle, err)
	}
	tmp, err := os.CreateTemp(g.root, ".source-")
	if err != nil {
		return protocol.Media{}, err
	}
	tmpPath := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(tmpPath) }()
	metadata, err = g.source.DownloadMedia(ctx, handle, tmp)
	if err != nil {
		return protocol.Media{}, err
	}
	if err := tmp.Close(); err != nil {
		return protocol.Media{}, err
	}
	registered, err := g.store.Register(owner, metadata, tmpPath)
	if err != nil {
		return protocol.Media{}, err
	}
	// Register owns the path; do not remove it after this point.
	tmpPath = ""
	g.mu.Lock()
	g.aliases[key] = registered.Handle
	g.mu.Unlock()
	return registered, nil
}

func (g *Gateway) download(ctx context.Context, owner Owner, request protocol.MediaRequest) (protocol.MediaResult, error) {
	media, err := g.resolve(ctx, owner, request.Handle)
	if err != nil {
		return protocol.MediaResult{}, err
	}
	var data []byte
	var complete bool
	var n int64
	n, complete, err = g.store.DownloadChunk(ctx, owner, media.Handle, request.Offset, sliceWriter{buffer: &data})
	if err != nil {
		return protocol.MediaResult{}, err
	}
	_ = n
	return protocol.MediaResult{Data: data, Complete: complete, ChunkSize: g.store.Limits().ChunkSize, MaxSize: g.store.Limits().MaxSize}, nil
}

type sliceWriter struct{ buffer *[]byte }

func (w sliceWriter) Write(p []byte) (int, error) {
	*w.buffer = append(*w.buffer, p...)
	return len(p), nil
}

func (g *Gateway) upload(ctx context.Context, owner Owner, request protocol.MediaRequest) (protocol.MediaResult, error) {
	g.mu.Lock()
	id := request.Handle
	if id == "" {
		id = "u_" + strconv.FormatInt(nextTransferID(), 10)
	}
	transfer, exists := g.uploads[id]
	if !exists {
		if request.Offset != 0 {
			g.mu.Unlock()
			return protocol.MediaResult{}, protocol.ErrInvalidDTO
		}
		file, err := os.CreateTemp(g.root, ".upload-")
		if err != nil {
			g.mu.Unlock()
			return protocol.MediaResult{}, err
		}
		transfer = uploadTransfer{owner: owner, request: request, path: file.Name()}
		_ = file.Close()
		g.uploads[id] = transfer
	}
	if transfer.owner != owner || request.Offset != transfer.next || request.Size != transfer.request.Size || request.Kind != transfer.request.Kind {
		g.mu.Unlock()
		return protocol.MediaResult{}, ErrOwnership
	}
	path := transfer.path
	transfer.next += int64(len(request.Data))
	if transfer.next > transfer.request.Size {
		g.mu.Unlock()
		return protocol.MediaResult{}, protocol.ErrInvalidDTO
	}
	transfer.request.Checksum = request.Checksum
	g.uploads[id] = transfer
	g.mu.Unlock()
	if len(request.Data) > 0 {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return protocol.MediaResult{}, err
		}
		_, writeErr := file.Write(request.Data)
		_ = file.Close()
		if writeErr != nil {
			return protocol.MediaResult{}, writeErr
		}
	}
	if !request.Complete && transfer.next < transfer.request.Size {
		return protocol.MediaResult{TransferID: id, ChunkSize: g.store.Limits().ChunkSize, MaxSize: g.store.Limits().MaxSize}, nil
	}
	if transfer.next != transfer.request.Size {
		return protocol.MediaResult{}, protocol.ErrInvalidDTO
	}
	file, err := os.Open(path)
	if err != nil {
		return protocol.MediaResult{}, err
	}
	media, err := g.store.Upload(ctx, owner, transfer.request, file)
	_ = file.Close()
	_ = os.Remove(path)
	g.mu.Lock()
	delete(g.uploads, id)
	if err == nil {
		g.finished[id] = struct {
			media protocol.Media
			owner Owner
		}{media: media, owner: owner}
	}
	g.mu.Unlock()
	if err != nil {
		return protocol.MediaResult{}, err
	}
	return protocol.MediaResult{TransferID: id, Media: &media, Complete: true, ChunkSize: g.store.Limits().ChunkSize, MaxSize: g.store.Limits().MaxSize}, nil
}

func (g *Gateway) finishedFor(owner Owner, handle string) (protocol.Media, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	value, ok := g.finished[handle]
	if !ok || value.owner != owner {
		return protocol.Media{}, false
	}
	return value.media, true
}

var transferCounter int64

func nextTransferID() int64 { return atomic.AddInt64(&transferCounter, 1) }

// DownloadAny is used by the Telegram action adapter after a plugin has
// uploaded a converted attachment.
func (g *Gateway) DownloadAny(ctx context.Context, handle string, dst io.Writer) error {
	g.mu.Lock()
	for _, value := range g.finished {
		if value.media.Handle == handle {
			g.mu.Unlock()
			_, err := g.store.Download(ctx, value.owner, handle, dst)
			return err
		}
	}
	g.mu.Unlock()
	return ErrInvalidHandle
}
