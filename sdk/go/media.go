package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

// MediaControl and MediaDataPlane keep the SDK independent from the socket
// implementation. The supervisor supplies both over the authenticated local
// transport; tests and alternate transports can provide the same contract.
type MediaControl interface {
	Media(context.Context, protocol.MediaRequest) (protocol.MediaResult, error)
}
type MediaDataPlane interface {
	OpenDownload(context.Context, string, string) (io.ReadCloser, error)
	OpenUpload(context.Context, string, string, int64) (io.WriteCloser, error)
}

type MediaProgress struct {
	Bytes int64
	Total int64
}
type MediaClient struct {
	control  MediaControl
	data     MediaDataPlane
	progress func(MediaProgress)
}

func NewMediaClient(control MediaControl, data MediaDataPlane, progress func(MediaProgress)) (*MediaClient, error) {
	if control == nil {
		return nil, errors.New("media control is required")
	}
	return &MediaClient{control: control, data: data, progress: progress}, nil
}

func (c *MediaClient) Inspect(ctx context.Context, handle string) (protocol.Media, error) {
	result, err := c.control.Media(ctx, protocol.MediaRequest{Operation: protocol.MediaInspect, Handle: handle})
	if err != nil {
		return protocol.Media{}, err
	}
	if result.Media == nil {
		return protocol.Media{}, errors.New("media inspect returned no metadata")
	}
	return *result.Media, nil
}

func (c *MediaClient) Download(ctx context.Context, handle string, dst io.Writer) (protocol.Media, error) {
	meta, err := c.Inspect(ctx, handle)
	if err != nil {
		return protocol.Media{}, err
	}
	if c.data == nil {
		return c.downloadChunked(ctx, meta, dst)
	}
	stream, err := c.data.OpenDownload(ctx, handle, "")
	if err != nil {
		return protocol.Media{}, err
	}
	defer stream.Close()
	h := sha256.New()
	n, err := copyProgress(ctx, io.MultiWriter(dst, h), stream, meta.Size, c.progress)
	if err != nil {
		return protocol.Media{}, err
	}
	if n != meta.Size {
		return protocol.Media{}, fmt.Errorf("media download size %d does not match declared %d", n, meta.Size)
	}
	if meta.Checksum != "" && !equalChecksum(meta.Checksum, h) {
		return protocol.Media{}, errors.New("media download checksum mismatch")
	}
	return meta, nil
}

func (c *MediaClient) downloadChunked(ctx context.Context, meta protocol.Media, dst io.Writer) (protocol.Media, error) {
	h := sha256.New()
	var offset int64
	for offset < meta.Size {
		result, err := c.control.Media(ctx, protocol.MediaRequest{
			Operation: protocol.MediaDownload, Handle: meta.Handle, Offset: offset,
		})
		if err != nil {
			return protocol.Media{}, err
		}
		if len(result.Data) == 0 {
			return protocol.Media{}, errors.New("media download returned an empty chunk")
		}
		if int64(len(result.Data)) > meta.Size-offset {
			return protocol.Media{}, errors.New("media download exceeded declared size")
		}
		written, err := dst.Write(result.Data)
		if err != nil {
			return protocol.Media{}, err
		}
		if written != len(result.Data) {
			return protocol.Media{}, io.ErrShortWrite
		}
		_, _ = h.Write(result.Data)
		offset += int64(written)
		if c.progress != nil {
			c.progress(MediaProgress{Bytes: offset, Total: meta.Size})
		}
		if result.Complete != (offset == meta.Size) {
			return protocol.Media{}, errors.New("media download completion flag is inconsistent")
		}
	}
	if meta.Checksum != "" && !equalChecksum(meta.Checksum, h) {
		return protocol.Media{}, errors.New("media download checksum mismatch")
	}
	return meta, nil
}

// Upload consumes exactly size bytes from src. It intentionally accepts no
// []byte or path overload, so callers cannot accidentally create an unbounded
// in-memory convenience API.
func (c *MediaClient) Upload(ctx context.Context, kind, filename, mime string, size int64, checksum string, src io.Reader) (protocol.Media, error) {
	if size <= 0 || size > protocol.MediaMaxSizeBytes {
		return protocol.Media{}, errors.New("media upload size is outside the configured limit")
	}
	if c.data == nil {
		return c.uploadChunked(ctx, kind, filename, mime, size, checksum, src)
	}
	result, err := c.control.Media(ctx, protocol.MediaRequest{Operation: protocol.MediaUpload, Kind: kind, FileName: filename, MIMEType: mime, Size: size, Checksum: checksum})
	if err != nil {
		return protocol.Media{}, err
	}
	if result.TransferID == "" {
		return protocol.Media{}, errors.New("media upload returned no transfer ID")
	}
	stream, err := c.data.OpenUpload(ctx, result.TransferID, checksum, size)
	if err != nil {
		return protocol.Media{}, err
	}
	defer stream.Close()
	n, err := copyProgress(ctx, stream, src, size, c.progress)
	if err != nil {
		_ = stream.Close()
		return protocol.Media{}, err
	}
	if n != size {
		_ = stream.Close()
		return protocol.Media{}, errors.New("media upload source ended before declared size")
	}
	if err := stream.Close(); err != nil {
		return protocol.Media{}, err
	}
	result, err = c.control.Media(ctx, protocol.MediaRequest{Operation: protocol.MediaInspect, Handle: result.TransferID})
	if err != nil {
		return protocol.Media{}, err
	}
	if result.Media == nil {
		return protocol.Media{}, errors.New("media upload returned no metadata")
	}
	return *result.Media, nil
}

func (c *MediaClient) uploadChunked(ctx context.Context, kind, filename, mime string, size int64, checksum string, src io.Reader) (protocol.Media, error) {
	buf := make([]byte, protocol.MediaChunkSizeBytes)
	h := sha256.New()
	var offset int64
	var transferID string
	for offset < size {
		want := int64(len(buf))
		if size-offset < want {
			want = size - offset
		}
		read, err := io.ReadFull(src, buf[:want])
		if err != nil && !(errors.Is(err, io.EOF) && read == 0) {
			return protocol.Media{}, fmt.Errorf("read media upload: %w", err)
		}
		if read == 0 {
			return protocol.Media{}, errors.New("media upload source ended before declared size")
		}
		_, _ = h.Write(buf[:read])
		complete := offset+int64(read) == size
		result, callErr := c.control.Media(ctx, protocol.MediaRequest{
			Operation: protocol.MediaUpload, Handle: transferID, Kind: kind, FileName: filename,
			MIMEType: mime, Size: size, Checksum: checksum, Offset: offset,
			Data: append([]byte(nil), buf[:read]...), Complete: complete,
		})
		if callErr != nil {
			return protocol.Media{}, callErr
		}
		if transferID == "" {
			transferID = result.TransferID
		}
		if transferID == "" || result.TransferID != transferID {
			return protocol.Media{}, errors.New("media upload returned an invalid transfer ID")
		}
		offset += int64(read)
		if c.progress != nil {
			c.progress(MediaProgress{Bytes: offset, Total: size})
		}
	}
	result, err := c.control.Media(ctx, protocol.MediaRequest{Operation: protocol.MediaInspect, Handle: transferID})
	if err != nil {
		return protocol.Media{}, err
	}
	if result.Media == nil {
		return protocol.Media{}, errors.New("media upload returned no metadata")
	}
	if checksum != "" && !equalChecksum(checksum, h) {
		return protocol.Media{}, errors.New("media upload checksum does not match source")
	}
	return *result.Media, nil
}

func copyProgress(ctx context.Context, dst io.Writer, src io.Reader, total int64, progress func(MediaProgress)) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if total < 0 || total > protocol.MediaMaxSizeBytes {
		return 0, errors.New("media transfer total is outside limit")
	}
	buf := make([]byte, protocol.MediaChunkSizeBytes)
	var n int64
	for n < total {
		select {
		case <-ctx.Done():
			return n, ctx.Err()
		default:
		}
		want := int64(len(buf))
		if total-n < want {
			want = total - n
		}
		read, err := src.Read(buf[:want])
		if read == 0 && err == nil {
			return n, io.ErrNoProgress
		}
		if read > 0 {
			written, werr := dst.Write(buf[:read])
			n += int64(written)
			if werr != nil {
				return n, werr
			}
			if written != read {
				return n, io.ErrShortWrite
			}
			if progress != nil {
				progress(MediaProgress{Bytes: n, Total: total})
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func equalChecksum(expected string, h hash.Hash) bool {
	sum := h.Sum(nil)
	return len(sum) == sha256.Size && subtleEqual(stringsLower(expected), hex.EncodeToString(sum))
}
func stringsLower(v string) string {
	b := []byte(v)
	for i, c := range b {
		if c >= 'A' && c <= 'F' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
func subtleEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var result byte
	for i := range a {
		result |= a[i] ^ b[i]
	}
	return result == 0
}
