package pluginpackage

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type archiveHandle struct {
	file          *os.File
	reader        *zip.Reader
	entries       map[string]*zip.File
	manifestBytes []byte
	signature     []byte
}

func (h *archiveHandle) close() error { return h.file.Close() }

func Verify(ctx context.Context, filename string, options VerifyOptions) (VerifiedPackage, error) {
	verified, archive, err := openAndVerify(ctx, filename, options)
	if archive != nil {
		closeErr := archive.close()
		if err == nil && closeErr != nil {
			err = fmt.Errorf("close package: %w", closeErr)
		}
	}
	return verified, err
}

func openAndVerify(ctx context.Context, filename string, options VerifyOptions) (VerifiedPackage, *archiveHandle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	limits, err := options.Limits.normalized()
	if err != nil {
		return VerifiedPackage{}, nil, err
	}
	if options.Trust.Mode == "" {
		options.Trust.Mode = TrustAllowUnsigned
	}
	if err := validateTrustMode(options.Trust.Mode); err != nil {
		return VerifiedPackage{}, nil, err
	}
	file, err := os.Open(filename)
	if err != nil {
		return VerifiedPackage{}, nil, fmt.Errorf("open plugin package: %w", err)
	}
	fail := func(cause error) (VerifiedPackage, *archiveHandle, error) {
		_ = file.Close()
		return VerifiedPackage{}, nil, cause
	}
	stat, err := file.Stat()
	if err != nil {
		return fail(fmt.Errorf("stat plugin package: %w", err))
	}
	if !stat.Mode().IsRegular() {
		return fail(fmt.Errorf("%w: archive is not a regular file", ErrInvalidPackage))
	}
	if stat.Size() <= 0 || stat.Size() > limits.MaxArchiveBytes {
		return fail(fmt.Errorf("%w: archive size %d exceeds maximum %d", ErrLimitExceeded, stat.Size(), limits.MaxArchiveBytes))
	}
	reader, err := zip.NewReader(file, stat.Size())
	if err != nil {
		return fail(fmt.Errorf("%w: open ZIP: %v", ErrInvalidPackage, err))
	}
	if len(reader.File) == 0 || len(reader.File) > limits.MaxFiles+2 {
		return fail(fmt.Errorf("%w: archive has %d entries, maximum is %d payload files plus metadata", ErrLimitExceeded, len(reader.File), limits.MaxFiles))
	}
	archive := &archiveHandle{file: file, reader: reader, entries: make(map[string]*zip.File, len(reader.File))}
	caseFolded := make(map[string]string, len(reader.File))
	var uncompressed uint64
	for _, entry := range reader.File {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		cleaned, pathErr := cleanArchivePath(entry.Name)
		if pathErr != nil || cleaned != entry.Name {
			return fail(fmt.Errorf("%w: %q", ErrUnsafeArchivePath, entry.Name))
		}
		if entry.NonUTF8 {
			return fail(fmt.Errorf("%w: non-UTF-8 entry %q", ErrInvalidPackage, entry.Name))
		}
		if _, duplicate := archive.entries[entry.Name]; duplicate {
			return fail(fmt.Errorf("%w: duplicate path %q", ErrInvalidPackage, entry.Name))
		}
		folded := strings.ToLower(entry.Name)
		if previous, duplicate := caseFolded[folded]; duplicate {
			return fail(fmt.Errorf("%w: case-colliding paths %q and %q", ErrInvalidPackage, previous, entry.Name))
		}
		mode := entry.Mode()
		if mode&os.ModeType != 0 || entry.FileInfo().IsDir() {
			return fail(fmt.Errorf("%w: %q has mode %s", ErrUnsupportedFileType, entry.Name, mode))
		}
		entryLimit := limits.MaxFileBytes
		switch entry.Name {
		case ManifestName:
			entryLimit = limits.MaxManifestBytes
		case SignatureName:
			entryLimit = limits.MaxSignatureBytes
		}
		if entry.UncompressedSize64 > uint64(entryLimit) {
			return fail(fmt.Errorf("%w: entry %q declares %d bytes, maximum is %d", ErrLimitExceeded, entry.Name, entry.UncompressedSize64, entryLimit))
		}
		if ^uint64(0)-uncompressed < entry.UncompressedSize64 {
			return fail(fmt.Errorf("%w: uncompressed size overflow", ErrLimitExceeded))
		}
		uncompressed += entry.UncompressedSize64
		if uncompressed > uint64(limits.MaxUncompressedBytes) {
			return fail(fmt.Errorf("%w: declared uncompressed size %d exceeds maximum %d", ErrLimitExceeded, uncompressed, limits.MaxUncompressedBytes))
		}
		archive.entries[entry.Name] = entry
		caseFolded[folded] = entry.Name
	}
	manifestEntry, exists := archive.entries[ManifestName]
	if !exists {
		return fail(fmt.Errorf("%w: missing %s", ErrInvalidPackage, ManifestName))
	}
	archive.manifestBytes, err = readEntry(ctx, manifestEntry, limits.MaxManifestBytes)
	if err != nil {
		return fail(fmt.Errorf("read manifest: %w", err))
	}
	manifest, err := decodeManifest(archive.manifestBytes)
	if err != nil {
		return fail(err)
	}
	if len(manifest.Files) > limits.MaxFiles {
		return fail(fmt.Errorf("%w: manifest declares %d files, maximum is %d", ErrLimitExceeded, len(manifest.Files), limits.MaxFiles))
	}
	canonical, err := canonicalManifest(manifest)
	if err != nil {
		return fail(err)
	}
	if !bytes.Equal(canonical, archive.manifestBytes) {
		return fail(fmt.Errorf("%w: %s is not canonical JSON", ErrInvalidPackage, ManifestName))
	}
	verified := VerifiedPackage{Manifest: manifest, ArchiveSize: stat.Size(), Uncompressed: int64(uncompressed)}
	digest := manifestDigest(archive.manifestBytes)
	verified.ManifestDigest = "sha256:" + hex.EncodeToString(digest[:])
	if signatureEntry, signed := archive.entries[SignatureName]; signed {
		archive.signature, err = readEntry(ctx, signatureEntry, limits.MaxSignatureBytes)
		if err != nil {
			return fail(fmt.Errorf("read signature: %w", err))
		}
		if err := verifySignature(archive.signature, digest, options.Trust, &verified); err != nil {
			return fail(err)
		}
	} else if options.Trust.Mode == TrustRequireSigned || options.Trust.Mode == TrustRequireTrusted {
		return fail(fmt.Errorf("%w: policy %q requires %s", ErrSignature, options.Trust.Mode, SignatureName))
	}
	declared := make(map[string]FileMetadata, len(manifest.Files))
	for _, metadata := range manifest.Files {
		entry, exists := archive.entries[metadata.Path]
		if !exists {
			return fail(fmt.Errorf("%w: declared file %q is missing", ErrIntegrity, metadata.Path))
		}
		if entry.UncompressedSize64 != uint64(metadata.Size) {
			return fail(fmt.Errorf("%w: file %q size header is %d, manifest declares %d", ErrIntegrity, metadata.Path, entry.UncompressedSize64, metadata.Size))
		}
		declared[metadata.Path] = metadata
	}
	for name := range archive.entries {
		if name == ManifestName || name == SignatureName {
			continue
		}
		if _, exists := declared[name]; !exists {
			return fail(fmt.Errorf("%w: undeclared archive file %q", ErrIntegrity, name))
		}
	}
	for _, metadata := range manifest.Files {
		actualDigest, size, hashErr := hashEntry(ctx, archive.entries[metadata.Path], limits.MaxFileBytes)
		if hashErr != nil {
			return fail(fmt.Errorf("verify file %q: %w", metadata.Path, hashErr))
		}
		if size != metadata.Size || actualDigest != metadata.SHA256 {
			return fail(fmt.Errorf("%w: file %q expected size/hash %d/%s, got %d/%s", ErrIntegrity, metadata.Path, metadata.Size, metadata.SHA256, size, actualDigest))
		}
	}
	if options.CheckVersions {
		if options.CoreVersion == "" || options.ProtocolVersion == "" {
			return fail(fmt.Errorf("%w: core and protocol versions are required", ErrIncompatible))
		}
		coreOK, rangeErr := versionInRange(options.CoreVersion, manifest.Core)
		if rangeErr != nil || !coreOK {
			return fail(fmt.Errorf("%w: core %q is outside [%s,%s)", ErrIncompatible, options.CoreVersion, manifest.Core.Min, manifest.Core.MaxExclusive))
		}
		protocolOK, rangeErr := versionInRange(options.ProtocolVersion, manifest.Protocol)
		if rangeErr != nil || !protocolOK {
			return fail(fmt.Errorf("%w: protocol %q is outside [%s,%s)", ErrIncompatible, options.ProtocolVersion, manifest.Protocol.Min, manifest.Protocol.MaxExclusive))
		}
	}
	if options.CheckPlatform {
		if options.OS == "" || options.Arch == "" {
			return fail(fmt.Errorf("%w: target os and arch are required", ErrIncompatible))
		}
		entrypoint, found := selectEntrypoint(manifest, options.OS, options.Arch)
		if !found {
			return fail(fmt.Errorf("%w: no entrypoint for %s/%s", ErrIncompatible, options.OS, options.Arch))
		}
		verified.Entrypoint = entrypoint
	}
	return verified, archive, nil
}

func decodeManifest(data []byte) (Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: decode manifest: %v", ErrInvalidPackage, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Manifest{}, fmt.Errorf("%w: decode manifest: %v", ErrInvalidPackage, err)
	}
	if err := manifest.normalizeAndValidate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func canonicalManifest(manifest Manifest) ([]byte, error) {
	if err := manifest.normalizeAndValidate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	return data, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func verifySignature(data []byte, digest [sha256.Size]byte, policy TrustPolicy, result *VerifiedPackage) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var signature Signature
	if err := decoder.Decode(&signature); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrSignature, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrSignature, err)
	}
	canonical, err := json.Marshal(signature)
	if err != nil || !bytes.Equal(canonical, data) {
		return fmt.Errorf("%w: %s is not canonical JSON", ErrSignature, SignatureName)
	}
	if signature.Format != SignatureFormatV1 || signature.Algorithm != "ed25519" || signature.KeyID == "" {
		return fmt.Errorf("%w: unsupported signature declaration", ErrSignature)
	}
	expectedDigest := "sha256:" + hex.EncodeToString(digest[:])
	if signature.Digest != expectedDigest {
		return fmt.Errorf("%w: manifest digest mismatch", ErrSignature)
	}
	value, err := base64.StdEncoding.DecodeString(signature.Value)
	if err != nil || len(value) != ed25519.SignatureSize {
		return fmt.Errorf("%w: invalid Ed25519 signature encoding", ErrSignature)
	}
	var embedded ed25519.PublicKey
	if signature.PublicKey != "" {
		decoded, decodeErr := base64.StdEncoding.DecodeString(signature.PublicKey)
		if decodeErr != nil || len(decoded) != ed25519.PublicKeySize {
			return fmt.Errorf("%w: invalid embedded Ed25519 public key", ErrSignature)
		}
		embedded = decoded
	}
	trusted, isTrusted := policy.TrustedKeys[signature.KeyID]
	if isTrusted && len(trusted) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: trusted key %q has invalid length", ErrSignature, signature.KeyID)
	}
	if isTrusted && embedded != nil && !bytes.Equal(trusted, embedded) {
		return fmt.Errorf("%w: embedded key does not match trusted key %q", ErrSignature, signature.KeyID)
	}
	publicKey := embedded
	if isTrusted {
		publicKey = trusted
	}
	if publicKey == nil {
		return fmt.Errorf("%w: no public key is available for signer %q", ErrSignature, signature.KeyID)
	}
	if !ed25519.Verify(publicKey, signatureMessage(digest), value) {
		return fmt.Errorf("%w: Ed25519 verification failed", ErrSignature)
	}
	if policy.Mode == TrustRequireTrusted && !isTrusted {
		return fmt.Errorf("%w: key %q", ErrUntrusted, signature.KeyID)
	}
	result.Signed = true
	result.SignerKeyID = signature.KeyID
	result.Trusted = isTrusted
	return nil
}

func readEntry(ctx context.Context, entry *zip.File, limit int64) ([]byte, error) {
	reader, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	var output bytes.Buffer
	if entry.UncompressedSize64 < uint64(limit) {
		output.Grow(int(entry.UncompressedSize64))
	}
	written, err := copyWithContext(ctx, &output, reader, limit+1, nil)
	if err != nil {
		return nil, err
	}
	if written > limit {
		return nil, fmt.Errorf("%w: decompressed entry exceeds %d bytes", ErrLimitExceeded, limit)
	}
	if written != int64(entry.UncompressedSize64) {
		return nil, fmt.Errorf("%w: entry size header mismatch", ErrIntegrity)
	}
	return output.Bytes(), nil
}

func hashEntry(ctx context.Context, entry *zip.File, limit int64) (string, int64, error) {
	reader, err := entry.Open()
	if err != nil {
		return "", 0, err
	}
	defer reader.Close()
	hasher := sha256.New()
	written, err := copyWithContext(ctx, io.Discard, reader, limit+1, hasher)
	if err != nil {
		return "", written, err
	}
	if written > limit {
		return "", written, fmt.Errorf("%w: file expands past %d bytes", ErrLimitExceeded, limit)
	}
	if written != int64(entry.UncompressedSize64) {
		return "", written, fmt.Errorf("%w: file size header mismatch", ErrIntegrity)
	}
	return hex.EncodeToString(hasher.Sum(nil)), written, nil
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader, limit int64, additional hash.Hash) (int64, error) {
	buffer := make([]byte, 32<<10)
	limited := io.LimitReader(source, limit)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := limited.Read(buffer)
		if read > 0 {
			chunk := buffer[:read]
			if additional != nil {
				_, _ = additional.Write(chunk)
			}
			written, writeErr := destination.Write(chunk)
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

type sourceFile struct {
	absolute string
	metadata FileMetadata
}

func Pack(ctx context.Context, sourceDirectory, manifestFilename, outputFilename string, options PackOptions) (VerifiedPackage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	limits, err := options.Limits.normalized()
	if err != nil {
		return VerifiedPackage{}, err
	}
	sourceDirectory, err = filepath.Abs(sourceDirectory)
	if err != nil {
		return VerifiedPackage{}, fmt.Errorf("resolve source directory: %w", err)
	}
	if manifestFilename == "" {
		manifestFilename = filepath.Join(sourceDirectory, ManifestName)
	}
	manifestFilename, err = filepath.Abs(manifestFilename)
	if err != nil {
		return VerifiedPackage{}, fmt.Errorf("resolve manifest: %w", err)
	}
	outputFilename, err = filepath.Abs(outputFilename)
	if err != nil {
		return VerifiedPackage{}, fmt.Errorf("resolve output: %w", err)
	}
	templateData, err := os.ReadFile(manifestFilename)
	if err != nil {
		return VerifiedPackage{}, fmt.Errorf("read manifest template: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(templateData))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return VerifiedPackage{}, fmt.Errorf("decode manifest template: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return VerifiedPackage{}, fmt.Errorf("decode manifest template: %w", err)
	}
	manifest.Files = nil
	sources, err := collectSourceFiles(ctx, sourceDirectory, manifestFilename, outputFilename, limits)
	if err != nil {
		return VerifiedPackage{}, err
	}
	manifest.Files = make([]FileMetadata, len(sources))
	for index := range sources {
		manifest.Files[index] = sources[index].metadata
	}
	manifestBytes, err := canonicalManifest(manifest)
	if err != nil {
		return VerifiedPackage{}, err
	}
	if int64(len(manifestBytes)) > limits.MaxManifestBytes {
		return VerifiedPackage{}, fmt.Errorf("%w: manifest is %d bytes", ErrLimitExceeded, len(manifestBytes))
	}
	var signatureBytes []byte
	if len(options.PrivateKey) > 0 {
		if len(options.PrivateKey) != ed25519.PrivateKeySize {
			return VerifiedPackage{}, fmt.Errorf("signing key must be an Ed25519 private key")
		}
		publicKey := options.PrivateKey.Public().(ed25519.PublicKey)
		keyID := options.KeyID
		if keyID == "" {
			keyID = KeyID(publicKey)
		}
		digest := manifestDigest(manifestBytes)
		signature := Signature{
			Format: SignatureFormatV1, Algorithm: "ed25519", KeyID: keyID,
			PublicKey: base64.StdEncoding.EncodeToString(publicKey),
			Digest:    "sha256:" + hex.EncodeToString(digest[:]),
			Value:     base64.StdEncoding.EncodeToString(ed25519.Sign(options.PrivateKey, signatureMessage(digest))),
		}
		signatureBytes, err = json.Marshal(signature)
		if err != nil {
			return VerifiedPackage{}, fmt.Errorf("marshal signature: %w", err)
		}
		if int64(len(signatureBytes)) > limits.MaxSignatureBytes {
			return VerifiedPackage{}, fmt.Errorf("%w: signature is %d bytes", ErrLimitExceeded, len(signatureBytes))
		}
	}
	if err := os.MkdirAll(filepath.Dir(outputFilename), 0o755); err != nil {
		return VerifiedPackage{}, fmt.Errorf("create output directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(outputFilename), ".srnx-pack-*")
	if err != nil {
		return VerifiedPackage{}, fmt.Errorf("create temporary package: %w", err)
	}
	temporaryName := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temporaryName)
		}
	}()
	writer := zip.NewWriter(temporary)
	writeErr := writePackageArchive(ctx, writer, manifestBytes, signatureBytes, sources)
	if closeErr := writer.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if syncErr := temporary.Sync(); writeErr == nil {
		writeErr = syncErr
	}
	if closeErr := temporary.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return VerifiedPackage{}, fmt.Errorf("write package: %w", writeErr)
	}
	stat, err := os.Stat(temporaryName)
	if err != nil {
		return VerifiedPackage{}, err
	}
	if stat.Size() > limits.MaxArchiveBytes {
		return VerifiedPackage{}, fmt.Errorf("%w: packed archive is %d bytes", ErrLimitExceeded, stat.Size())
	}
	if err := os.Chmod(temporaryName, 0o644); err != nil {
		return VerifiedPackage{}, fmt.Errorf("set package permissions: %w", err)
	}
	if err := replaceFile(temporaryName, outputFilename); err != nil {
		return VerifiedPackage{}, fmt.Errorf("commit package: %w", err)
	}
	committed = true
	verifyOptions := VerifyOptions{Limits: limits, Trust: TrustPolicy{Mode: TrustAllowUnsigned}}
	return Verify(ctx, outputFilename, verifyOptions)
}

func collectSourceFiles(ctx context.Context, sourceDirectory, manifestFilename, outputFilename string, limits Limits) ([]sourceFile, error) {
	stat, err := os.Stat(sourceDirectory)
	if err != nil || !stat.IsDir() {
		return nil, fmt.Errorf("source is not a directory: %s", sourceDirectory)
	}
	var sources []sourceFile
	var total int64
	err = filepath.WalkDir(sourceDirectory, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if filename == sourceDirectory {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: source path %q is a symlink", ErrUnsupportedFileType, filename)
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: source path %q is not regular", ErrUnsupportedFileType, filename)
		}
		absolute, err := filepath.Abs(filename)
		if err != nil {
			return err
		}
		if absolute == manifestFilename || absolute == outputFilename {
			return nil
		}
		relative, err := filepath.Rel(sourceDirectory, filename)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if _, reserved := reservedRootNames[relative]; reserved || strings.HasPrefix(relative, ReceiptDirectory+"/") {
			return fmt.Errorf("%w: source uses reserved path %q", ErrInvalidPackage, relative)
		}
		if _, err := cleanArchivePath(relative); err != nil {
			return fmt.Errorf("%w: source path %q", ErrUnsafeArchivePath, relative)
		}
		if len(sources) >= limits.MaxFiles {
			return fmt.Errorf("%w: more than %d source files", ErrLimitExceeded, limits.MaxFiles)
		}
		if info.Size() > limits.MaxFileBytes {
			return fmt.Errorf("%w: source file %q is %d bytes", ErrLimitExceeded, relative, info.Size())
		}
		if total > limits.MaxUncompressedBytes-info.Size() {
			return fmt.Errorf("%w: source content exceeds %d bytes", ErrLimitExceeded, limits.MaxUncompressedBytes)
		}
		digest, size, err := hashFile(ctx, filename, limits.MaxFileBytes)
		if err != nil {
			return err
		}
		mode := uint32(0o644)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		total += size
		sources = append(sources, sourceFile{absolute: filename, metadata: FileMetadata{Path: relative, Size: size, SHA256: digest, Mode: mode}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].metadata.Path < sources[j].metadata.Path })
	return sources, nil
}

func hashFile(ctx context.Context, filename string, limit int64) (string, int64, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := copyWithContext(ctx, io.Discard, file, limit+1, hasher)
	if err != nil {
		return "", size, err
	}
	if size > limit {
		return "", size, fmt.Errorf("%w: %q exceeds %d bytes", ErrLimitExceeded, filename, limit)
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

func writePackageArchive(ctx context.Context, writer *zip.Writer, manifest, signature []byte, sources []sourceFile) error {
	if err := writeZIPBytes(writer, ManifestName, manifest, 0o644); err != nil {
		return err
	}
	if signature != nil {
		if err := writeZIPBytes(writer, SignatureName, signature, 0o644); err != nil {
			return err
		}
	}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		header := deterministicHeader(source.metadata.Path, fs.FileMode(source.metadata.Mode))
		entryWriter, err := writer.CreateHeader(&header)
		if err != nil {
			return err
		}
		file, err := os.Open(source.absolute)
		if err != nil {
			return err
		}
		hasher := sha256.New()
		size, copyErr := copyWithContext(ctx, entryWriter, file, source.metadata.Size+1, hasher)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		actual := hex.EncodeToString(hasher.Sum(nil))
		if size != source.metadata.Size || actual != source.metadata.SHA256 {
			return fmt.Errorf("%w: source file %q changed while packing", ErrIntegrity, source.metadata.Path)
		}
	}
	return nil
}

func writeZIPBytes(writer *zip.Writer, name string, data []byte, mode fs.FileMode) error {
	header := deterministicHeader(name, mode)
	entryWriter, err := writer.CreateHeader(&header)
	if err != nil {
		return err
	}
	_, err = entryWriter.Write(data)
	return err
}

func deterministicHeader(name string, mode fs.FileMode) zip.FileHeader {
	header := zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(mode)
	header.SetModTime(time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC))
	return header
}

func replaceFile(source, destination string) error {
	if err := os.Rename(source, destination); err == nil {
		return nil
	}
	// Windows cannot replace an existing file with Rename. The destination is
	// removed only after the complete temporary file has been fsynced.
	if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, destination)
}
