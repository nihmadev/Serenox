package telegram

import (
	"context"
	"errors"
	"sync"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

// Ingress provides a general bounded lane and a reserved owner-command lane.
// Classification happens in the core before enqueue; Receive prioritizes the
// reserved lane without starving ordinary updates.
type Ingress struct {
	updates  chan pluginv1.Update
	commands chan pluginv1.Update
	closed   chan struct{}
	mu       sync.RWMutex
	isClosed bool
	once     sync.Once
}

func NewIngress(updateCapacity, commandCapacity int) (*Ingress, error) {
	if updateCapacity <= 0 || commandCapacity <= 0 {
		return nil, errors.New("Telegram ingress capacities must be positive")
	}
	return &Ingress{
		updates:  make(chan pluginv1.Update, updateCapacity),
		commands: make(chan pluginv1.Update, commandCapacity), closed: make(chan struct{}),
	}, nil
}

func (i *Ingress) TryUpdate(update pluginv1.Update) error {
	if err := update.Validate(); err != nil {
		return err
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.isClosed {
		return ErrClosed
	}
	select {
	case i.updates <- update:
		return nil
	default:
		return ErrIngressFull
	}
}

func (i *Ingress) SubmitCommand(ctx context.Context, update pluginv1.Update) error {
	if err := update.Validate(); err != nil {
		return err
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.isClosed {
		return ErrClosed
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case i.commands <- update:
		return nil
	}
}

func (i *Ingress) Receive(ctx context.Context) (pluginv1.Update, error) {
	for {
		select {
		case update := <-i.commands:
			return update, nil
		default:
		}
		select {
		case <-ctx.Done():
			return pluginv1.Update{}, ctx.Err()
		case update := <-i.commands:
			return update, nil
		case update := <-i.updates:
			return update, nil
		case <-i.closed:
			// Admission has stopped. Drain both bounded lanes before telling
			// workers to exit.
			select {
			case update := <-i.commands:
				return update, nil
			default:
			}
			select {
			case update := <-i.updates:
				return update, nil
			default:
				return pluginv1.Update{}, ErrClosed
			}
		}
	}
}

func (i *Ingress) Depth() (updates, commands int) { return len(i.updates), len(i.commands) }

func (i *Ingress) Close() {
	i.once.Do(func() {
		i.mu.Lock()
		i.isClosed = true
		close(i.closed)
		i.mu.Unlock()
	})
}
