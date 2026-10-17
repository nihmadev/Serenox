package eventbus

import (
	"crypto/rand"
	"encoding/hex"
)

type RandomIDs struct{}

func (RandomIDs) NewEventID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return ""
	}
	return "evt_" + hex.EncodeToString(buffer)
}
