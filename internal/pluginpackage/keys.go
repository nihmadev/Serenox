package pluginpackage

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"sort"
)

const signingDomain = "serenox-plugin-package-v1\x00"

type trustFile struct {
	Mode TrustMode      `json:"mode"`
	Keys []trustFileKey `json:"keys,omitempty"`
}

type trustFileKey struct {
	ID        string `json:"id"`
	PublicKey string `json:"public_key"`
}

func KeyID(key ed25519.PublicKey) string {
	digest := sha256.Sum256(key)
	return "ed25519:" + hex.EncodeToString(digest[:16])
}

// LoadPrivateKey accepts a raw/base64 Ed25519 seed or private key, or a PEM
// encoded PKCS#8 private key.
func LoadPrivateKey(filename string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	trimmed := bytes.TrimSpace(data)
	if block, _ := pem.Decode(trimmed); block != nil {
		parsed, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if parseErr != nil {
			return nil, fmt.Errorf("parse PKCS#8 private key: %w", parseErr)
		}
		key, ok := parsed.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("private key is not Ed25519")
		}
		return append(ed25519.PrivateKey(nil), key...), nil
	}
	if decoded, decodeErr := base64.StdEncoding.DecodeString(string(trimmed)); decodeErr == nil {
		trimmed = decoded
	}
	switch len(trimmed) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(trimmed), nil
	case ed25519.PrivateKeySize:
		return append(ed25519.PrivateKey(nil), trimmed...), nil
	default:
		return nil, fmt.Errorf("private key must be a %d-byte seed or %d-byte Ed25519 key", ed25519.SeedSize, ed25519.PrivateKeySize)
	}
}

func LoadTrustPolicy(filename string) (TrustPolicy, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return TrustPolicy{}, fmt.Errorf("read trust policy: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document trustFile
	if err := decoder.Decode(&document); err != nil {
		return TrustPolicy{}, fmt.Errorf("decode trust policy: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return TrustPolicy{}, fmt.Errorf("decode trust policy: %w", err)
	}
	if document.Mode == "" {
		document.Mode = TrustRequireTrusted
	}
	if err := validateTrustMode(document.Mode); err != nil {
		return TrustPolicy{}, err
	}
	policy := TrustPolicy{Mode: document.Mode, TrustedKeys: make(map[string]ed25519.PublicKey, len(document.Keys))}
	for index, record := range document.Keys {
		if record.ID == "" {
			return TrustPolicy{}, fmt.Errorf("trust key %d has an empty id", index)
		}
		if _, exists := policy.TrustedKeys[record.ID]; exists {
			return TrustPolicy{}, fmt.Errorf("duplicate trust key id %q", record.ID)
		}
		decoded, err := base64.StdEncoding.DecodeString(record.PublicKey)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return TrustPolicy{}, fmt.Errorf("trust key %q is not a base64 Ed25519 public key", record.ID)
		}
		policy.TrustedKeys[record.ID] = append(ed25519.PublicKey(nil), decoded...)
	}
	return policy, nil
}

// MarshalTrustPolicy emits a deterministic trust file suitable for the CLI.
func MarshalTrustPolicy(policy TrustPolicy) ([]byte, error) {
	if policy.Mode == "" {
		policy.Mode = TrustRequireTrusted
	}
	if err := validateTrustMode(policy.Mode); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(policy.TrustedKeys))
	for id := range policy.TrustedKeys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	document := trustFile{Mode: policy.Mode, Keys: make([]trustFileKey, 0, len(ids))}
	for _, id := range ids {
		key := policy.TrustedKeys[id]
		if len(key) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("trusted key %q has invalid length", id)
		}
		document.Keys = append(document.Keys, trustFileKey{ID: id, PublicKey: base64.StdEncoding.EncodeToString(key)})
	}
	return json.MarshalIndent(document, "", "  ")
}

func validateTrustMode(mode TrustMode) error {
	switch mode {
	case TrustAllowUnsigned, TrustRequireSigned, TrustRequireTrusted:
		return nil
	default:
		return fmt.Errorf("unknown trust mode %q", mode)
	}
}

func manifestDigest(manifest []byte) [sha256.Size]byte {
	hash := sha256.New()
	_, _ = hash.Write([]byte(signingDomain))
	_, _ = hash.Write(manifest)
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func signatureMessage(digest [sha256.Size]byte) []byte {
	message := make([]byte, 0, len(signingDomain)+len("signature\x00")+len(digest))
	message = append(message, signingDomain...)
	message = append(message, "signature\x00"...)
	message = append(message, digest[:]...)
	return message
}
