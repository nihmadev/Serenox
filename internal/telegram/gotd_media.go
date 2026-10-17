package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func (t *LiveTransport) InspectMedia(ctx context.Context, handle string) (pluginv1.Media, error) {
	media, _, err := t.rawMedia(ctx, handle)
	return media, err
}

func (t *LiveTransport) DownloadMedia(ctx context.Context, handle string, dst io.Writer) (pluginv1.Media, error) {
	media, raw, err := t.rawMedia(ctx, handle)
	if err != nil {
		return pluginv1.Media{}, err
	}
	if dst == nil {
		return pluginv1.Media{}, errors.New("media destination is nil")
	}
	var location tg.InputFileLocationClass
	switch value := raw.(type) {
	case *tg.MessageMediaPhoto:
		photo, ok := value.Photo.(*tg.Photo)
		if !ok || photo == nil {
			return pluginv1.Media{}, errors.New("Telegram photo payload is unavailable")
		}
		sizeType := largestPhotoType(photo.Sizes)
		if sizeType == "" {
			return pluginv1.Media{}, errors.New("Telegram photo has no downloadable size")
		}
		location = &tg.InputPhotoFileLocation{ID: photo.ID, AccessHash: photo.AccessHash, FileReference: photo.FileReference, ThumbSize: sizeType}
	case *tg.MessageMediaDocument:
		document, ok := value.Document.(*tg.Document)
		if !ok || document == nil {
			return pluginv1.Media{}, errors.New("Telegram document payload is unavailable")
		}
		location = document.AsInputDocumentFileLocation()
	default:
		return pluginv1.Media{}, fmt.Errorf("Telegram media kind %q is not downloadable", media.Kind)
	}
	hash := sha256.New()
	counted := &countingWriter{dst: io.MultiWriter(dst, hash)}
	if _, err := downloader.NewDownloader().Download(t.client.API(), location).Stream(ctx, counted); err != nil {
		return pluginv1.Media{}, fmt.Errorf("download Telegram media: %w", err)
	}
	media.Size = counted.n
	media.Checksum = hex.EncodeToString(hash.Sum(nil))
	return media, nil
}

type countingWriter struct {
	dst io.Writer
	n   int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	w.n += int64(n)
	return n, err
}

func (t *LiveTransport) rawMedia(ctx context.Context, handle string) (pluginv1.Media, tg.MessageMediaClass, error) {
	if err := t.ensureReadable(); err != nil {
		return pluginv1.Media{}, nil, err
	}
	chatID, messageID, err := parseMediaHandle(handle)
	if err != nil {
		return pluginv1.Media{}, nil, err
	}
	peer, err := t.peers.resolve(chatID)
	if err != nil {
		return pluginv1.Media{}, nil, err
	}
	history, ok := t.api.(interface {
		MessagesGetHistory(context.Context, *tg.MessagesGetHistoryRequest) (tg.MessagesMessagesClass, error)
	})
	if !ok {
		return pluginv1.Media{}, nil, errors.New("Telegram media reads are unavailable")
	}
	result, err := history.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, OffsetID: messageID + 1, AddOffset: -1, Limit: 3})
	if err != nil {
		return pluginv1.Media{}, nil, fmt.Errorf("read Telegram media message: %w", err)
	}
	messages, entities := messagesAndEntities(result)
	t.peers.learn(entities)
	for _, item := range messages {
		message, ok := item.(*tg.Message)
		if !ok || message == nil || message.ID != messageID {
			continue
		}
		media := convertMedia(message.Media, handle)
		if media == nil {
			return pluginv1.Media{}, nil, errors.New("Telegram message has no downloadable media")
		}
		return *media, message.Media, nil
	}
	return pluginv1.Media{}, nil, errors.New("Telegram media message was not found")
}

func parseMediaHandle(handle string) (int64, int, error) {
	parts := strings.Split(handle, "_")
	if len(parts) != 3 || parts[0] != "tg" {
		return 0, 0, errors.New("invalid Telegram media handle")
	}
	chatID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || chatID == 0 {
		return 0, 0, errors.New("invalid Telegram media chat ID")
	}
	messageID64, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || messageID64 <= 0 || messageID64 > math.MaxInt32 {
		return 0, 0, errors.New("invalid Telegram media message ID")
	}
	return chatID, int(messageID64), nil
}

func largestPhotoType(sizes []tg.PhotoSizeClass) string {
	bestType := ""
	bestArea := -1
	for _, item := range sizes {
		switch value := item.(type) {
		case *tg.PhotoSize:
			area := value.W * value.H
			if area >= bestArea {
				bestArea, bestType = area, value.Type
			}
		case *tg.PhotoSizeProgressive:
			area := value.W * value.H
			if area >= bestArea {
				bestArea, bestType = area, value.Type
			}
		}
	}
	return bestType
}
