// Package pluginpackage implements the on-disk Serenox plugin package and
// installation format. A .srnx extension is only a user-facing convention;
// callers must always verify the ZIP contents before trusting or extracting it.
package pluginpackage

const (
	FormatV1            = "serenox.plugin.package/v1"
	SignatureFormatV1   = "serenox.plugin.signature/v1"
	ManifestName        = "manifest.json"
	SignatureName       = "signature.json"
	ReceiptDirectory    = ".serenox"
	DefaultCoreVersion  = "0.1.0"
	DefaultRetain       = 3
	defaultLockWait     = 30
	defaultStaleLockAge = 300
)
