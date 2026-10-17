package pluginpackage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func writeReceipt(versionDirectory string, receipt Receipt) error {
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return writeNewFile(filepath.Join(versionDirectory, ReceiptDirectory, receiptFilename), data, 0o600)
}

func readReceipt(versionDirectory string) (Receipt, error) {
	data, err := os.ReadFile(filepath.Join(versionDirectory, ReceiptDirectory, receiptFilename))
	if err != nil {
		return Receipt{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var receipt Receipt
	if err := decoder.Decode(&receipt); err != nil {
		return Receipt{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Receipt{}, err
	}
	if receipt.Format != FormatV1 || !idPattern.MatchString(receipt.ID) || strings.TrimSpace(receipt.Name) == "" || len(receipt.Name) > 128 ||
		!versionPattern.MatchString(receipt.Version) || !validManifestDigest(receipt.ManifestDigest) ||
		(receipt.Category != CategoryGoExecutable && receipt.Category != CategoryPythonSidecar) {
		return Receipt{}, fmt.Errorf("%w: invalid installation receipt", ErrInvalidPackage)
	}
	if receipt.Trusted && !receipt.Signed || receipt.Signed && receipt.SignerKeyID == "" || !receipt.Signed && receipt.SignerKeyID != "" {
		return Receipt{}, fmt.Errorf("%w: inconsistent receipt signature state", ErrInvalidPackage)
	}
	if (!platformPattern.MatchString(receipt.Entrypoint.OS) && receipt.Entrypoint.OS != "any") ||
		(!platformPattern.MatchString(receipt.Entrypoint.Arch) && receipt.Entrypoint.Arch != "any") {
		return Receipt{}, fmt.Errorf("%w: invalid receipt entrypoint platform", ErrInvalidPackage)
	}
	cleaned, pathErr := cleanArchivePath(receipt.Entrypoint.Path)
	if pathErr != nil || cleaned != receipt.Entrypoint.Path {
		return Receipt{}, fmt.Errorf("%w: invalid receipt entrypoint path", ErrInvalidPackage)
	}
	if receipt.Category == CategoryGoExecutable && (receipt.Entrypoint.OS == "any" || receipt.Entrypoint.Arch == "any") {
		return Receipt{}, fmt.Errorf("%w: non-specific Go receipt entrypoint", ErrInvalidPackage)
	}
	if receipt.Category == CategoryPythonSidecar {
		if err := validateRange("receipt.protocol", receipt.Protocol); err != nil {
			return Receipt{}, err
		}
		if err := validateRange("receipt.sdk", receipt.SDK); err != nil {
			return Receipt{}, err
		}
		if err := validateRange("receipt.sidecar", receipt.Sidecar); err != nil {
			return Receipt{}, err
		}
		if err := validatePythonEntrypoint(receipt.Entrypoint, 0); err != nil {
			return Receipt{}, err
		}
		if err := validateResourceLimits(receipt.Resources); err != nil {
			return Receipt{}, err
		}
		if err := validateDependencies(receipt.Dependencies); err != nil {
			return Receipt{}, err
		}
	}
	if err := validateReceiptTokens("capabilities", receipt.Capabilities); err != nil {
		return Receipt{}, err
	}
	if err := validateReceiptTokens("permissions", receipt.Permissions); err != nil {
		return Receipt{}, err
	}
	for _, declaration := range receipt.EventPublications {
		if err := declaration.Validate(); err != nil {
			return Receipt{}, fmt.Errorf("%w: invalid receipt event publication", ErrInvalidPackage)
		}
	}
	for _, declaration := range receipt.EventSubscriptions {
		if err := declaration.Validate(); err != nil {
			return Receipt{}, fmt.Errorf("%w: invalid receipt event subscription", ErrInvalidPackage)
		}
	}
	return receipt, nil
}

func validateReceiptTokens(field string, values []string) error {
	if len(values) > 64 || !sort.StringsAreSorted(values) {
		return fmt.Errorf("%w: invalid receipt %s", ErrInvalidPackage, field)
	}
	for index, value := range values {
		if !tokenPattern.MatchString(value) || index > 0 && values[index-1] == value {
			return fmt.Errorf("%w: invalid receipt %s", ErrInvalidPackage, field)
		}
	}
	return nil
}

func validManifestDigest(value string) bool {
	const prefix = "sha256:"
	return strings.HasPrefix(value, prefix) && hexDigestPattern.MatchString(strings.TrimPrefix(value, prefix))
}
