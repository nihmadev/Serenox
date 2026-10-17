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
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPackIsDeterministicAndSignedPackageVerifies(t *testing.T) {
	source, manifestFile := createSource(t, "1.2.3", []byte("hello executable\n"), runtime.GOOS, runtime.GOARCH)
	first := filepath.Join(t.TempDir(), "hello.srnx")
	second := filepath.Join(t.TempDir(), "hello.not-an-extension")
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	options := PackOptions{PrivateKey: privateKey}

	packed, err := Pack(context.Background(), source, manifestFile, first, options)
	if err != nil {
		t.Fatalf("Pack(first): %v", err)
	}
	if _, err := Pack(context.Background(), source, manifestFile, second, options); err != nil {
		t.Fatalf("Pack(second): %v", err)
	}
	firstBytes, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("same source and key did not produce byte-identical packages")
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	verified, err := Verify(context.Background(), second, VerifyOptions{
		Limits: DefaultLimits(), Trust: TrustPolicy{
			Mode: TrustRequireTrusted, TrustedKeys: map[string]ed25519.PublicKey{KeyID(publicKey): publicKey},
		},
		OS: runtime.GOOS, Arch: runtime.GOARCH, CoreVersion: DefaultCoreVersion,
		ProtocolVersion: "1.0", CheckPlatform: true, CheckVersions: true,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !verified.Signed || !verified.Trusted || verified.ManifestDigest != packed.ManifestDigest {
		t.Fatalf("unexpected verification: %+v", verified)
	}
	if verified.Entrypoint.Path != "bin/hello" {
		t.Fatalf("entrypoint = %q", verified.Entrypoint.Path)
	}
}

func TestVerifyRejectsTamperedPayloadAndSignature(t *testing.T) {
	source, manifestFile := createSource(t, "1.0.0", []byte("original"), runtime.GOOS, runtime.GOARCH)
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x23}, ed25519.SeedSize))
	original := filepath.Join(t.TempDir(), "original.srnx")
	if _, err := Pack(context.Background(), source, manifestFile, original, PackOptions{PrivateKey: privateKey}); err != nil {
		t.Fatal(err)
	}
	tamperedPayload := filepath.Join(t.TempDir(), "payload.srnx")
	rewriteZIP(t, original, tamperedPayload, func(name string, data []byte) []byte {
		if name == "bin/hello" {
			return []byte("tampered")
		}
		return data
	})
	if _, err := Verify(context.Background(), tamperedPayload, VerifyOptions{Limits: DefaultLimits(), Trust: TrustPolicy{Mode: TrustAllowUnsigned}}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tampered payload error = %v, want integrity error", err)
	}

	tamperedSignature := filepath.Join(t.TempDir(), "signature.srnx")
	rewriteZIP(t, original, tamperedSignature, func(name string, data []byte) []byte {
		if name != SignatureName {
			return data
		}
		var signature Signature
		if err := json.Unmarshal(data, &signature); err != nil {
			t.Fatal(err)
		}
		decoded, err := base64.StdEncoding.DecodeString(signature.Value)
		if err != nil {
			t.Fatal(err)
		}
		decoded[0] ^= 0xff
		signature.Value = base64.StdEncoding.EncodeToString(decoded)
		modified, err := json.Marshal(signature)
		if err != nil {
			t.Fatal(err)
		}
		return modified
	})
	if _, err := Verify(context.Background(), tamperedSignature, VerifyOptions{Limits: DefaultLimits(), Trust: TrustPolicy{Mode: TrustRequireSigned}}); !errors.Is(err, ErrSignature) {
		t.Fatalf("tampered signature error = %v, want signature error", err)
	}
}

func TestVerifyRejectsTraversalSymlinkDuplicateAndZipBombMetadata(t *testing.T) {
	tests := []struct {
		name    string
		entries func(*testing.T) []rawEntry
		want    error
	}{
		{
			name: "traversal",
			entries: func(t *testing.T) []rawEntry {
				return []rawEntry{{name: "../escape", data: []byte("x"), mode: 0o644}}
			},
			want: ErrUnsafeArchivePath,
		},
		{
			name: "symlink",
			entries: func(t *testing.T) []rawEntry {
				return []rawEntry{{name: "link", data: []byte("target"), mode: os.ModeSymlink | 0o777}}
			},
			want: ErrUnsupportedFileType,
		},
		{
			name: "duplicate",
			entries: func(t *testing.T) []rawEntry {
				return []rawEntry{{name: "same", data: []byte("a"), mode: 0o644}, {name: "same", data: []byte("b"), mode: 0o644}}
			},
			want: ErrInvalidPackage,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "bad.srnx")
			writeRawZIP(t, filename, test.entries(t), nil)
			_, err := Verify(context.Background(), filename, VerifyOptions{Limits: DefaultLimits(), Trust: TrustPolicy{Mode: TrustAllowUnsigned}})
			if !errors.Is(err, test.want) {
				t.Fatalf("Verify error = %v, want %v", err, test.want)
			}
		})
	}

	t.Run("oversized declared entry", func(t *testing.T) {
		filename := filepath.Join(t.TempDir(), "bomb.srnx")
		file, err := os.Create(filename)
		if err != nil {
			t.Fatal(err)
		}
		writer := zip.NewWriter(file)
		header := zip.FileHeader{Name: "bomb", Method: zip.Store, CRC32: 0, CompressedSize64: 0, UncompressedSize64: uint64(DefaultLimits().MaxFileBytes + 1)}
		header.SetMode(0o644)
		if _, err := writer.CreateRaw(&header); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = Verify(context.Background(), filename, VerifyOptions{Limits: DefaultLimits(), Trust: TrustPolicy{Mode: TrustAllowUnsigned}})
		if !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("Verify error = %v, want limit error", err)
		}
	})
}

func TestTrustPolicyRejectsUnknownSignerAndUnsignedPackage(t *testing.T) {
	source, manifestFile := createSource(t, "1.0.0", []byte("signed"), runtime.GOOS, runtime.GOARCH)
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	signed := filepath.Join(t.TempDir(), "signed.srnx")
	if _, err := Pack(context.Background(), source, manifestFile, signed, PackOptions{PrivateKey: privateKey}); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), signed, VerifyOptions{Limits: DefaultLimits(), Trust: TrustPolicy{Mode: TrustRequireTrusted}}); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("untrusted error = %v", err)
	}
	unsigned := filepath.Join(t.TempDir(), "unsigned.srnx")
	if _, err := Pack(context.Background(), source, manifestFile, unsigned, PackOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), unsigned, VerifyOptions{Limits: DefaultLimits(), Trust: TrustPolicy{Mode: TrustRequireSigned}}); !errors.Is(err, ErrSignature) {
		t.Fatalf("unsigned error = %v", err)
	}
}

func TestInstallRejectsWrongPlatformBeforeExtraction(t *testing.T) {
	source, manifestFile := createSource(t, "1.0.0", []byte("foreign"), "plan9", "mips")
	archive := filepath.Join(t.TempDir(), "foreign.srnx")
	if _, err := Pack(context.Background(), source, manifestFile, archive, PackOptions{}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	manager, err := NewManager(ManagerConfig{Root: root, Verify: VerifyOptions{
		Limits: DefaultLimits(), Trust: TrustPolicy{Mode: TrustAllowUnsigned}, OS: runtime.GOOS, Arch: runtime.GOARCH,
		CoreVersion: DefaultCoreVersion, ProtocolVersion: "1.0",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background(), archive); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("Install error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, pluginsDirectory, "example.hello")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plugin directory was created after incompatible verification: %v", err)
	}
}

type rawEntry struct {
	name string
	data []byte
	mode os.FileMode
}

func createSource(t *testing.T, version string, executable []byte, targetOS, targetArch string) (string, string) {
	t.Helper()
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "bin", "hello"), executable, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		Format: FormatV1, ID: "example.hello", Name: "Hello", Version: version,
		Category:     CategoryGoExecutable,
		Protocol:     VersionRange{Min: "1.0", MaxExclusive: "2.0"},
		Core:         VersionRange{Min: "0.1.0", MaxExclusive: "1.0.0"},
		Entrypoints:  []Entrypoint{{OS: targetOS, Arch: targetArch, Path: "bin/hello"}},
		Capabilities: []string{"commands"}, Permissions: []string{"telegram.reply"},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestFilename := filepath.Join(source, ManifestName)
	if err := os.WriteFile(manifestFilename, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return source, manifestFilename
}

func rewriteZIP(t *testing.T, source, destination string, modify func(string, []byte) []byte) {
	t.Helper()
	reader, err := zip.OpenReader(source)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	entries := make([]rawEntry, 0, len(reader.File))
	for _, entry := range reader.File {
		opened, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(opened)
		_ = opened.Close()
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, rawEntry{name: entry.Name, data: modify(entry.Name, data), mode: entry.Mode()})
	}
	writeRawZIP(t, destination, entries, nil)
}

func writeRawZIP(t *testing.T, filename string, entries []rawEntry, extra func(*zip.Writer)) {
	t.Helper()
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, entry := range entries {
		header := zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetMode(entry.mode)
		output, err := writer.CreateHeader(&header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := output.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if extra != nil {
		extra(writer)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func payloadMetadata(name string, data []byte, mode uint32) FileMetadata {
	digest := sha256.Sum256(data)
	return FileMetadata{Path: name, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Mode: mode}
}
