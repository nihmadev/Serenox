package telegram

import (
	"container/list"
	"errors"
	"fmt"
	"sync"

	"github.com/gotd/td/tg"
)

const channelChatIDOffset int64 = 1_000_000_000_000

var ErrPeerNotResolved = errors.New("Telegram peer is not resolved; receive an update containing its access hash before sending an action")

type peerEntry struct {
	chatID int64
	peer   tg.InputPeerClass
}

// peerBook is a bounded LRU of access hashes learned from update entities.
// It never fabricates an access hash for a user or channel.
type peerBook struct {
	mu       sync.Mutex
	capacity int
	selfID   int64
	items    map[int64]*list.Element
	order    *list.List
}

func newPeerBook(capacity int) *peerBook {
	return &peerBook{capacity: capacity, items: make(map[int64]*list.Element, capacity), order: list.New()}
}

func (b *peerBook) setSelf(userID int64) {
	b.mu.Lock()
	b.selfID = userID
	b.mu.Unlock()
}

func (b *peerBook) learn(entities tg.Entities) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, user := range entities.Users {
		if user == nil || id == 0 {
			continue
		}
		if user.Self || id == b.selfID {
			b.putLocked(id, &tg.InputPeerSelf{})
			continue
		}
		accessHash, ok := user.GetAccessHash()
		if !ok {
			continue
		}
		b.putLocked(id, &tg.InputPeerUser{UserID: id, AccessHash: accessHash})
	}
	for id := range entities.Chats {
		if id != 0 {
			b.putLocked(-id, &tg.InputPeerChat{ChatID: id})
		}
	}
	for id, channel := range entities.Channels {
		if channel == nil || id <= 0 {
			continue
		}
		accessHash, ok := channel.GetAccessHash()
		if !ok {
			continue
		}
		b.putLocked(channelChatID(id), &tg.InputPeerChannel{ChannelID: id, AccessHash: accessHash})
	}
}

func (b *peerBook) putLocked(chatID int64, peer tg.InputPeerClass) {
	if element, ok := b.items[chatID]; ok {
		element.Value.(*peerEntry).peer = peer
		b.order.MoveToFront(element)
		return
	}
	element := b.order.PushFront(&peerEntry{chatID: chatID, peer: peer})
	b.items[chatID] = element
	if b.order.Len() <= b.capacity {
		return
	}
	oldest := b.order.Back()
	delete(b.items, oldest.Value.(*peerEntry).chatID)
	b.order.Remove(oldest)
}

func (b *peerBook) resolve(chatID int64) (tg.InputPeerClass, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if chatID == b.selfID && chatID != 0 {
		return &tg.InputPeerSelf{}, nil
	}
	if element, ok := b.items[chatID]; ok {
		b.order.MoveToFront(element)
		return cloneInputPeer(element.Value.(*peerEntry).peer), nil
	}
	if chatID < 0 && chatID > -channelChatIDOffset {
		return &tg.InputPeerChat{ChatID: -chatID}, nil
	}
	return nil, fmt.Errorf("%w: chat_id=%d", ErrPeerNotResolved, chatID)
}

func cloneInputPeer(peer tg.InputPeerClass) tg.InputPeerClass {
	switch value := peer.(type) {
	case *tg.InputPeerSelf:
		return &tg.InputPeerSelf{}
	case *tg.InputPeerUser:
		copy := *value
		return &copy
	case *tg.InputPeerChat:
		copy := *value
		return &copy
	case *tg.InputPeerChannel:
		copy := *value
		return &copy
	default:
		return peer
	}
}

func channelChatID(channelID int64) int64 { return -(channelChatIDOffset + channelID) }
