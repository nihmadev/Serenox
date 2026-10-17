package config

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const telegramCredentialsFile = "telegram.credentials.json"

// TelegramCredentials are kept separately from the user-edited config. The
// file is owner-readable only and contains the application credentials needed
// to reopen a saved Telegram session on a later process start.
type TelegramCredentials struct {
	APIID   int    `json:"api_id"`
	APIHash string `json:"api_hash"`
}

func TelegramCredentialsPath(dataDir string) string {
	return filepath.Join(dataDir, telegramCredentialsFile)
}

func LoadTelegramCredentials(dataDir string) (TelegramCredentials, error) {
	file, err := os.Open(TelegramCredentialsPath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return TelegramCredentials{}, nil
	}
	if err != nil {
		return TelegramCredentials{}, fmt.Errorf("open Telegram credentials: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil {
		return TelegramCredentials{}, fmt.Errorf("read Telegram credentials: %w", err)
	}
	if len(data) > 64*1024 {
		return TelegramCredentials{}, errors.New("Telegram credentials file exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var credentials TelegramCredentials
	if err := decoder.Decode(&credentials); err != nil {
		return TelegramCredentials{}, fmt.Errorf("decode Telegram credentials: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return TelegramCredentials{}, errors.New("Telegram credentials file contains trailing data")
	}
	if err := validateTelegramCredentials(credentials); err != nil {
		return TelegramCredentials{}, err
	}
	return credentials, nil
}

func SaveTelegramCredentials(dataDir string, credentials TelegramCredentials) error {
	if err := validateTelegramCredentials(credentials); err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create Telegram data directory: %w", err)
	}
	_ = os.Chmod(dataDir, 0o700)
	data, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Telegram credentials: %w", err)
	}
	data = append(data, '\n')
	file, err := os.CreateTemp(dataDir, ".telegram.credentials-*")
	if err != nil {
		return fmt.Errorf("create Telegram credentials file: %w", err)
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("protect Telegram credentials file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write Telegram credentials: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync Telegram credentials: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close Telegram credentials: %w", err)
	}
	if err := os.Rename(temporary, TelegramCredentialsPath(dataDir)); err != nil {
		return fmt.Errorf("install Telegram credentials: %w", err)
	}
	return nil
}

func validateTelegramCredentials(credentials TelegramCredentials) error {
	if credentials.APIID <= 0 || credentials.APIID > 2_147_483_647 {
		return errors.New("Telegram API ID must be between 1 and 2147483647")
	}
	decoded, err := hex.DecodeString(credentials.APIHash)
	if err != nil || len(decoded) != 16 {
		return errors.New("Telegram API hash must be exactly 32 hexadecimal characters")
	}
	return nil
}
