package telegram

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var ErrSessionTooLarge = errors.New("Telegram session exceeds configured size")

const MaximumSessionBytes = 16 << 20

type SessionStore interface {
	Load() ([]byte, error)
	Save([]byte) error
	Delete() error
}

// FileSession stores opaque native-client session bytes with atomic rename and
// mode 0600. The live adapter, not the core protocol, understands their format.
type FileSession struct {
	path     string
	maxBytes int
	mu       sync.Mutex
}

func NewFileSession(path string, maxBytes int) (*FileSession, error) {
	if strings.TrimSpace(path) == "" || strings.IndexByte(path, 0) >= 0 || maxBytes <= 0 || maxBytes > MaximumSessionBytes {
		return nil, errors.New("invalid Telegram session configuration")
	}
	return &FileSession{path: path, maxBytes: maxBytes}, nil
}

func (s *FileSession) Load() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat Telegram session: %w", err)
	}
	if info.Size() > int64(s.maxBytes) {
		_ = file.Close()
		return nil, ErrSessionTooLarge
	}
	payload, err := io.ReadAll(io.LimitReader(file, int64(s.maxBytes)+1))
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("read Telegram session: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close Telegram session: %w", err)
	}
	if len(payload) > s.maxBytes {
		return nil, ErrSessionTooLarge
	}
	return payload, nil
}

func (s *FileSession) Save(payload []byte) error {
	if len(payload) > s.maxBytes {
		return ErrSessionTooLarge
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create Telegram session directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".session-*")
	if err != nil {
		return fmt.Errorf("create Telegram session temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	cleanup := func() { _ = os.Remove(temporaryName) }
	defer cleanup()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("chmod Telegram session: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write Telegram session: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync Telegram session: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close Telegram session: %w", err)
	}
	if err := os.Rename(temporaryName, s.path); err != nil {
		return fmt.Errorf("replace Telegram session: %w", err)
	}
	return syncDirectory(filepath.Dir(s.path))
}

func (s *FileSession) Delete() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	parent := filepath.Dir(s.path)
	if err := os.Remove(s.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, statErr := os.Stat(parent); errors.Is(statErr, os.ErrNotExist) {
				return nil
			} else if statErr != nil {
				return fmt.Errorf("stat Telegram session directory: %w", statErr)
			}
			// Syncing an existing parent also makes a retry meaningful after a
			// previous unlink succeeded but its directory sync failed.
			return syncDirectory(parent)
		}
		return fmt.Errorf("delete Telegram session: %w", err)
	}
	return syncDirectory(parent)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open session directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync session directory: %w", err)
	}
	return nil
}
