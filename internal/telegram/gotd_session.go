package telegram

import (
	"context"
	"errors"
	"os"

	"github.com/gotd/td/session"
)

// gotdSessionStorage adapts the core-owned, size-bounded SessionStore to
// gotd's session.Storage contract. A missing file is a normal first-run state.
type gotdSessionStorage struct {
	store SessionStore
}

func (s gotdSessionStorage) LoadSession(ctx context.Context) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("nil Telegram session context")
	}
	if s.store == nil {
		return nil, errors.New("nil Telegram session store")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	payload, err := s.store.Load()
	if errors.Is(err, os.ErrNotExist) {
		return nil, session.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 {
		return nil, session.ErrNotFound
	}
	return payload, nil
}

func (s gotdSessionStorage) StoreSession(ctx context.Context, payload []byte) error {
	if ctx == nil {
		return errors.New("nil Telegram session context")
	}
	if s.store == nil {
		return errors.New("nil Telegram session store")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.store.Save(payload)
}

var _ session.Storage = gotdSessionStorage{}
