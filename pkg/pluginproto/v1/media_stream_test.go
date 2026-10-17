package v1

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"testing"
)

func TestMediaStreamAuthenticatesOrdersAndChecksums(t *testing.T) {
	var transfer [16]byte
	copy(transfer[:], "transfer-0000001")
	var wire bytes.Buffer
	writer, err := NewMediaStreamWriter(&wire, []byte("secret"), 7, transfer, 11)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("hello ")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("world")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := NewMediaStreamReader(&wire, []byte("secret"), 7, transfer, 11)
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	buf := make([]byte, MediaChunkSizeBytes)
	for {
		n, readErr := reader.Read(buf)
		if readErr != nil {
			if !errors.Is(readErr, nil) {
				break
			}
		}
		if n > 0 {
			got.Write(buf[:n])
		}
		if n == 0 {
			break
		}
	}
	if err := reader.Complete(); err != nil {
		t.Fatal(err)
	}
	if got.String() != "hello world" {
		t.Fatalf("payload = %q", got.String())
	}
	if sha256.Size != len(sha256.Sum256(got.Bytes())) {
		t.Fatal("checksum fixture is invalid")
	}
}

func TestMediaStreamRejectsWrongKeyAndSequence(t *testing.T) {
	var transfer [16]byte
	var wire bytes.Buffer
	writer, _ := NewMediaStreamWriter(&wire, []byte("secret"), 1, transfer, 1)
	_, _ = writer.Write([]byte{'x'})
	_ = writer.Close()
	reader, _ := NewMediaStreamReader(&wire, []byte("wrong"), 1, transfer, 1)
	buf := make([]byte, MediaChunkSizeBytes)
	if _, err := reader.Read(buf); !errors.Is(err, ErrMediaStreamAuth) {
		t.Fatalf("wrong key error = %v", err)
	}
}
