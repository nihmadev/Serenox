package pluginpackage

import "errors"

var (
	ErrInvalidPackage      = errors.New("invalid Serenox plugin package")
	ErrLimitExceeded       = errors.New("plugin package limit exceeded")
	ErrIntegrity           = errors.New("plugin package integrity check failed")
	ErrSignature           = errors.New("plugin package signature check failed")
	ErrUntrusted           = errors.New("plugin signer is not trusted")
	ErrIncompatible        = errors.New("plugin package is incompatible")
	ErrAlreadyInstalled    = errors.New("plugin version is already installed with different contents")
	ErrNotInstalled        = errors.New("plugin is not installed")
	ErrNoRollback          = errors.New("no plugin version is available for rollback")
	ErrInstallLockTimeout  = errors.New("timed out waiting for plugin installation lock")
	ErrUnsafeArchivePath   = errors.New("unsafe plugin archive path")
	ErrUnsupportedFileType = errors.New("unsupported plugin archive file type")
)
