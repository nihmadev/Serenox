// Package media owns the bounded, Telegram-independent media store used by
// plugin runtimes. Handles are random capabilities; filesystem names and paths
// never cross the plugin boundary.
package media

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

var (
	ErrInvalidHandle = errors.New("media handle is invalid or stale")
	ErrOwnership     = errors.New("media ownership check failed")
	ErrQuota         = errors.New("media plugin quota exceeded")
	ErrConcurrent    = errors.New("media concurrent transfer limit exceeded")
	ErrUnsupported   = errors.New("media kind is unsupported")
)

type Limits struct {
	MaxSize         int64
	ChunkSize       int
	MaxConcurrent   int
	QuotaBytes      int64
	TransferTimeout time.Duration
	IdleTimeout     time.Duration
}

func (l Limits) normalized() (Limits, error) {
	if l.MaxSize == 0 {
		l.MaxSize = protocol.MediaMaxSizeBytes
	}
	if l.ChunkSize == 0 {
		l.ChunkSize = protocol.MediaChunkSizeBytes
	}
	if l.MaxConcurrent == 0 {
		l.MaxConcurrent = protocol.MediaMaxConcurrentTransfers
	}
	if l.QuotaBytes == 0 {
		l.QuotaBytes = protocol.MediaPluginQuotaBytes
	}
	if l.TransferTimeout == 0 {
		l.TransferTimeout = protocol.MediaMaxTransferSeconds * time.Second
	}
	if l.IdleTimeout == 0 {
		l.IdleTimeout = protocol.MediaIdleTimeoutSeconds * time.Second
	}
	if l.MaxSize <= 0 || l.MaxSize > protocol.MediaMaxSizeBytes || l.ChunkSize <= 0 || l.ChunkSize > protocol.MediaChunkSizeBytes || l.MaxConcurrent < 1 || l.MaxConcurrent > 64 || l.QuotaBytes < l.MaxSize || l.TransferTimeout <= 0 || l.IdleTimeout <= 0 {
		return Limits{}, errors.New("invalid media limits")
	}
	return l, nil
}

type Owner = protocol.MediaOwner

type Source interface {
	Open(context.Context, protocol.Media) (io.ReadCloser, error)
}

type entry struct {
	media protocol.Media
	path  string
	owner Owner
	bytes int64
}

type Service struct {
	root    string
	limits  Limits
	mu      sync.Mutex
	entries map[string]entry
	active  map[string]int
	quota   map[string]int64
	closed  bool
	source  Source
}

func New(root string, limits Limits, source Source) (*Service, error) {
	if strings.TrimSpace(root) == "" || filepath.IsAbs(root) == false {
		return nil, errors.New("media root must be an absolute path")
	}
	limits, err := limits.normalized()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create media root: %w", err)
	}
	return &Service{root: root, limits: limits, source: source, entries: make(map[string]entry), active: make(map[string]int), quota: make(map[string]int64)}, nil
}

func (s *Service) Limits() Limits { return s.limits }

func (s *Service) Inspect(_ context.Context, owner Owner, handle string) (protocol.Media, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[handle]
	if !ok {
		return protocol.Media{}, ErrInvalidHandle
	}
	if !sameOwner(e.owner, owner) {
		return protocol.Media{}, ErrOwnership
	}
	return e.media, nil
}

func (s *Service) Register(owner Owner, media protocol.Media, path string) (protocol.Media, error) {
	if media.Kind == "" || media.Size < 0 || media.Size > s.limits.MaxSize {
		return protocol.Media{}, protocol.ErrInvalidDTO
	}
	clean, err := s.controlledPath(path)
	if err != nil {
		return protocol.Media{}, err
	}
	info, err := os.Stat(clean)
	if err != nil {
		return protocol.Media{}, err
	}
	if info.Size() != media.Size || info.Size() > s.limits.MaxSize {
		return protocol.Media{}, protocol.ErrInvalidDTO
	}
	handle, err := newHandle()
	if err != nil {
		return protocol.Media{}, err
	}
	media.Handle, media.FileID = handle, ""
	media.Source = owner
	if err := media.Validate(); err != nil {
		return protocol.Media{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return protocol.Media{}, errors.New("media service is closed")
	}
	if s.quota[owner.PluginID]+info.Size() > s.limits.QuotaBytes {
		return protocol.Media{}, ErrQuota
	}
	s.entries[handle] = entry{media: media, path: clean, owner: owner, bytes: info.Size()}
	s.quota[owner.PluginID] += info.Size()
	return media, nil
}

func (s *Service) Download(ctx context.Context, owner Owner, handle string, dst io.Writer) (int64, error) {
	if dst == nil {
		return 0, errors.New("download destination is nil")
	}
	e, release, err := s.acquire(owner, handle)
	if err != nil {
		return 0, err
	}
	defer release()
	file, err := os.Open(e.path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	return protocol.CopyMediaStream(ctx, dst, file, e.media.Size)
}

// DownloadChunk reads one bounded chunk without exposing the backing path.
// It is used by the supervised JSON bridge; the socket transport can continue
// to use Download for a streaming data plane.
func (s *Service) DownloadChunk(ctx context.Context, owner Owner, handle string, offset int64, dst io.Writer) (n int64, complete bool, err error) {
	if dst == nil || offset < 0 {
		return 0, false, protocol.ErrInvalidDTO
	}
	e, release, err := s.acquire(owner, handle)
	if err != nil {
		return 0, false, err
	}
	defer release()
	if offset > e.media.Size {
		return 0, false, protocol.ErrInvalidDTO
	}
	file, err := os.Open(e.path)
	if err != nil {
		return 0, false, err
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return 0, false, err
	}
	remaining := e.media.Size - offset
	if remaining > int64(s.limits.ChunkSize) {
		remaining = int64(s.limits.ChunkSize)
	}
	n, err = io.CopyN(dst, file, remaining)
	if errors.Is(err, io.EOF) && n == remaining {
		err = nil
	}
	return n, offset+n >= e.media.Size, err
}

func (s *Service) Upload(ctx context.Context, owner Owner, request protocol.MediaRequest, src io.Reader) (protocol.Media, error) {
	if err := request.Validate(); err != nil {
		return protocol.Media{}, err
	}
	if src == nil {
		return protocol.Media{}, errors.New("upload source is nil")
	}
	if err := s.reserve(owner, request.Size); err != nil {
		return protocol.Media{}, err
	}
	committed := false
	defer func() {
		s.releaseActive(owner.PluginID)
		if !committed {
			s.releaseQuota(owner.PluginID, request.Size)
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, s.limits.TransferTimeout)
	defer cancel()
	tmp, err := os.CreateTemp(s.root, ".upload-")
	if err != nil {
		return protocol.Media{}, err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	h := sha256.New()
	written := int64(0)
	buf := make([]byte, s.limits.ChunkSize)
	for written < request.Size {
		select {
		case <-ctx.Done():
			return protocol.Media{}, ctx.Err()
		default:
		}
		want := int64(len(buf))
		if remaining := request.Size - written; remaining < want {
			want = remaining
		}
		n, readErr := src.Read(buf[:want])
		if n > 0 {
			if _, err = tmp.Write(buf[:n]); err != nil {
				return protocol.Media{}, err
			}
			_, _ = h.Write(buf[:n])
			written += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return protocol.Media{}, readErr
		}
	}
	if written != request.Size {
		return protocol.Media{}, protocol.ErrInvalidDTO
	}
	if request.Checksum != "" && !strings.EqualFold(request.Checksum, hex.EncodeToString(h.Sum(nil))) {
		return protocol.Media{}, errors.New("media checksum mismatch")
	}
	if err = tmp.Sync(); err != nil {
		return protocol.Media{}, err
	}
	if err = tmp.Close(); err != nil {
		return protocol.Media{}, err
	}
	handle, err := newHandle()
	if err != nil {
		return protocol.Media{}, err
	}
	destination := filepath.Join(s.root, handle)
	if err = os.Rename(tmpName, destination); err != nil {
		return protocol.Media{}, err
	}
	media := protocol.Media{Kind: request.Kind, Handle: handle, FileName: request.FileName, MIMEType: request.MIMEType, Size: written, Checksum: hex.EncodeToString(h.Sum(nil)), Source: owner}
	s.mu.Lock()
	s.entries[handle] = entry{media: media, path: destination, owner: owner, bytes: written}
	s.mu.Unlock()
	committed = true
	return media, nil
}

func (s *Service) Delete(_ context.Context, owner Owner, handle string) error {
	s.mu.Lock()
	e, ok := s.entries[handle]
	if !ok {
		s.mu.Unlock()
		return ErrInvalidHandle
	}
	if !sameOwner(e.owner, owner) {
		s.mu.Unlock()
		return ErrOwnership
	}
	delete(s.entries, handle)
	s.quota[owner.PluginID] -= e.bytes
	s.mu.Unlock()
	return os.Remove(e.path)
}

func (s *Service) Cleanup() error {
	s.mu.Lock()
	entries := make([]entry, 0, len(s.entries))
	for handle, e := range s.entries {
		entries = append(entries, e)
		delete(s.entries, handle)
	}
	s.closed = true
	s.mu.Unlock()
	var first error
	for _, e := range entries {
		if err := os.Remove(e.path); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
	}
	matches, _ := filepath.Glob(filepath.Join(s.root, ".upload-*"))
	for _, path := range matches {
		_ = os.Remove(path)
	}
	return first
}

func (s *Service) controlledPath(path string) (string, error) {
	clean, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	root, _ := filepath.Abs(s.root)
	if clean != root && !strings.HasPrefix(clean, root+string(filepath.Separator)) {
		return "", ErrOwnership
	}
	return clean, nil
}
func sameOwner(a, b Owner) bool {
	return a.PluginID == b.PluginID && a.AccountID == b.AccountID && (b.PeerID == 0 || a.PeerID == b.PeerID)
}
func (s *Service) reserve(owner Owner, bytes int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrInvalidHandle
	}
	if bytes <= 0 || bytes > s.limits.MaxSize || s.quota[owner.PluginID]+bytes > s.limits.QuotaBytes {
		return ErrQuota
	}
	if s.active[owner.PluginID] >= s.limits.MaxConcurrent {
		return ErrConcurrent
	}
	s.active[owner.PluginID]++
	s.quota[owner.PluginID] += bytes
	return nil
}
func (s *Service) releaseQuota(plugin string, bytes int64) {
	s.mu.Lock()
	s.quota[plugin] -= bytes
	s.mu.Unlock()
}
func (s *Service) releaseActive(plugin string) { s.mu.Lock(); s.active[plugin]--; s.mu.Unlock() }
func (s *Service) acquire(owner Owner, handle string) (entry, func(), error) {
	s.mu.Lock()
	e, ok := s.entries[handle]
	if !ok {
		s.mu.Unlock()
		return entry{}, nil, ErrInvalidHandle
	}
	if !sameOwner(e.owner, owner) {
		s.mu.Unlock()
		return entry{}, nil, ErrOwnership
	}
	if s.active[owner.PluginID] >= s.limits.MaxConcurrent {
		s.mu.Unlock()
		return entry{}, nil, ErrConcurrent
	}
	s.active[owner.PluginID]++
	s.mu.Unlock()
	return e, func() { s.releaseActive(owner.PluginID) }, nil
}
func newHandle() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "m1_" + hex.EncodeToString(b[:]), nil
}
