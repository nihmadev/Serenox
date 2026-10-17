package pluginpackage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func (m *Manager) acquire(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(m.root, 0o700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(m.root, lockFilename)
	deadline := time.NewTimer(m.lockWait)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		tokenBytes := make([]byte, 16)
		if _, err := rand.Read(tokenBytes); err != nil {
			return nil, fmt.Errorf("generate installation lock token: %w", err)
		}
		lock := installLock{PID: os.Getpid(), Token: hex.EncodeToString(tokenBytes), CreatedAt: m.now().UTC()}
		data, _ := json.Marshal(lock)
		file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, writeErr := file.Write(data); writeErr != nil {
				_ = file.Close()
				_ = os.Remove(lockPath)
				return nil, writeErr
			}
			if syncErr := file.Sync(); syncErr != nil {
				_ = file.Close()
				_ = os.Remove(lockPath)
				return nil, syncErr
			}
			if closeErr := file.Close(); closeErr != nil {
				_ = os.Remove(lockPath)
				return nil, closeErr
			}
			return func() {
				current, readErr := os.ReadFile(lockPath)
				if readErr == nil && bytes.Equal(current, data) {
					_ = os.Remove(lockPath)
				}
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && m.now().Sub(info.ModTime()) > m.staleLockAge {
			staleName := fmt.Sprintf("%s.stale-%d", lockPath, m.now().UnixNano())
			if renameErr := os.Rename(lockPath, staleName); renameErr == nil {
				_ = os.Remove(staleName)
				continue
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, ErrInstallLockTimeout
		case <-ticker.C:
		}
	}
}
