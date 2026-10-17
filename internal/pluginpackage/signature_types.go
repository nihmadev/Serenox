package pluginpackage

import "crypto/ed25519"

type Signature struct {
	Format    string `json:"format"`
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key,omitempty"`
	Digest    string `json:"digest"`
	Value     string `json:"signature"`
}

type TrustMode string

const (
	// TrustAllowUnsigned accepts unsigned packages. A present signature is
	// still verified cryptographically, even when its signer is not trusted.
	TrustAllowUnsigned TrustMode = "allow-unsigned"
	// TrustRequireSigned requires a valid signature but does not require its key
	// to be present in the local trust store.
	TrustRequireSigned TrustMode = "require-signed"
	// TrustRequireTrusted requires a valid signature by a locally trusted key.
	TrustRequireTrusted TrustMode = "require-trusted"
)

type TrustPolicy struct {
	Mode        TrustMode
	TrustedKeys map[string]ed25519.PublicKey
}
