// Package bootstrap translates validated runtime configuration into concrete
// adapters while keeping configuration and transport packages independent.
package bootstrap

import (
	"errors"

	"github.com/nihmadev/Serenox/internal/config"
	"github.com/nihmadev/Serenox/internal/telegram"
)

// TelegramSessionMaxBytes is a hard upper bound for the opaque gotd session.
// Normal sessions are much smaller; the limit primarily protects startup from
// an accidentally replaced or corrupt file.
const TelegramSessionMaxBytes = 4 << 20

var ErrTelegramCredentialsRequired = errors.New("Telegram API credentials are required; set telegram.api_id and telegram.api_hash or SERENOX_TELEGRAM_API_ID and SERENOX_TELEGRAM_API_HASH")

// TelegramLiveConfig builds a native live-client configuration. Runtime must
// already have passed config validation; no credential value is included in
// returned errors.
func TelegramLiveConfig(runtime config.Runtime) (telegram.LiveConfig, error) {
	if runtime.Telegram.APIID <= 0 || runtime.Telegram.APIHash == "" {
		return telegram.LiveConfig{}, ErrTelegramCredentialsRequired
	}
	session, err := telegram.NewFileSession(runtime.TelegramSessionPath(), TelegramSessionMaxBytes)
	if err != nil {
		return telegram.LiveConfig{}, err
	}
	return telegram.LiveConfig{
		AppID:   runtime.Telegram.APIID,
		AppHash: runtime.Telegram.APIHash,
		Session: session,
		Device: telegram.DeviceInfo{
			DeviceModel:    runtime.Telegram.DeviceModel,
			SystemVersion:  runtime.Telegram.SystemVersion,
			AppVersion:     runtime.Telegram.AppVersion,
			SystemLangCode: runtime.Telegram.SystemLangCode,
			LangCode:       runtime.Telegram.LangCode,
		},
	}, nil
}
