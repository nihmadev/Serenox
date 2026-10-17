package telegram

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

var (
	ErrCursorInvalid = errors.New("read cursor is invalid")
	ErrCursorExpired = errors.New("read cursor has expired")
	ErrCursorScope   = errors.New("read cursor scope mismatch")
)

type CursorScope struct {
	PluginID  string
	AccountID int64
	Operation string
	PeerID    int64
	Query     string
	Direction string
}

type readCursor struct {
	Version   int    `json:"v"`
	Plugin    string `json:"p"`
	Account   int64  `json:"a"`
	Operation string `json:"o"`
	Peer      int64  `json:"i,omitempty"`
	QueryHash string `json:"q,omitempty"`
	Direction string `json:"d,omitempty"`
	Position  int64  `json:"n"`
	Page      int    `json:"r,omitempty"`
	Expires   int64  `json:"e"`
	MAC       string `json:"m"`
}

type CursorCodec struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

func NewCursorCodec(secret []byte, ttl time.Duration) (*CursorCodec, error) {
	if len(secret) < 32 {
		return nil, errors.New("cursor secret must contain at least 32 bytes")
	}
	if ttl <= 0 || ttl > 24*time.Hour {
		return nil, errors.New("cursor TTL is outside its bound")
	}
	return &CursorCodec{secret: append([]byte(nil), secret...), ttl: ttl, now: time.Now}, nil
}

func (c *CursorCodec) Encode(scope CursorScope, position int64) (string, error) {
	return c.EncodePage(scope, position, 0)
}

func (c *CursorCodec) EncodePage(scope CursorScope, position int64, page int) (string, error) {
	if c == nil || strings.TrimSpace(scope.PluginID) == "" || scope.AccountID == 0 || scope.Operation == "" || position < 0 {
		return "", ErrCursorInvalid
	}
	if page < 0 || page >= pluginv1.MaxClientPages {
		return "", ErrCursorInvalid
	}
	if pluginv1.ClientCapability(pluginv1.ClientReadOperation(scope.Operation)) == "" && pluginv1.ModerationPermission(pluginv1.ModerationOperation(scope.Operation)) == "" {
		return "", ErrCursorInvalid
	}
	value := readCursor{Version: 1, Plugin: scope.PluginID, Account: scope.AccountID, Operation: string(scope.Operation), Peer: scope.PeerID, QueryHash: cursorQueryHash(scope.Query), Direction: scope.Direction, Position: position, Page: page, Expires: c.now().Add(c.ttl).Unix()}
	value.MAC = c.sign(value)
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	if len(encoded) > pluginv1.MaxClientCursorBytes {
		return "", fmt.Errorf("%w: encoded cursor exceeds %d bytes", ErrCursorInvalid, pluginv1.MaxClientCursorBytes)
	}
	return encoded, nil
}

func (c *CursorCodec) Decode(scope CursorScope, encoded string) (int64, error) {
	position, _, err := c.DecodeState(scope, encoded)
	return position, err
}

func (c *CursorCodec) DecodeState(scope CursorScope, encoded string) (int64, int, error) {
	if c == nil || len(encoded) == 0 || len(encoded) > pluginv1.MaxClientCursorBytes {
		return 0, 0, ErrCursorInvalid
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return 0, 0, ErrCursorInvalid
	}
	var value readCursor
	if json.Unmarshal(data, &value) != nil || value.Version != 1 || value.Position < 0 || value.Page < 0 || value.Page >= pluginv1.MaxClientPages || value.Expires <= 0 || value.MAC == "" {
		return 0, 0, ErrCursorInvalid
	}
	if value.Expires < c.now().Unix() {
		return 0, 0, ErrCursorExpired
	}
	if !hmac.Equal([]byte(value.MAC), []byte(c.sign(value))) {
		return 0, 0, ErrCursorInvalid
	}
	if value.Plugin != scope.PluginID || value.Account != scope.AccountID || value.Operation != string(scope.Operation) || value.Peer != scope.PeerID || value.Direction != scope.Direction || value.QueryHash != cursorQueryHash(scope.Query) {
		return 0, 0, ErrCursorScope
	}
	return value.Position, value.Page, nil
}

func (c *CursorCodec) sign(value readCursor) string {
	value.MAC = ""
	data, _ := json.Marshal(value)
	hash := hmac.New(sha256.New, c.secret)
	_, _ = hash.Write(data)
	return base64.RawURLEncoding.EncodeToString(hash.Sum(nil))
}

func cursorQueryHash(query string) string {
	hash := sha256.Sum256([]byte(query))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}
