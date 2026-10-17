package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"strings"
	"testing"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
	serenox "github.com/nihmadev/Serenox/sdk/go"
)

type memoryMediaControl struct {
	input    protocol.Media
	request  protocol.MediaRequest
	uploaded []byte
}

func (m *memoryMediaControl) Media(_ context.Context, request protocol.MediaRequest) (protocol.MediaResult, error) {
	switch request.Operation {
	case protocol.MediaInspect:
		if request.Handle == m.input.Handle {
			return protocol.MediaResult{Media: &m.input}, nil
		}
		if request.Handle == "upload-1" {
			media := protocol.Media{
				Kind: "document", Handle: "m1_output", FileName: m.request.FileName,
				MIMEType: m.request.MIMEType, Size: int64(len(m.uploaded)), Checksum: checksum(m.uploaded),
			}
			return protocol.MediaResult{Media: &media}, nil
		}
	case protocol.MediaUpload:
		m.request = request
		return protocol.MediaResult{TransferID: "upload-1"}, nil
	}
	return protocol.MediaResult{}, nil
}

type memoryMediaData struct {
	control *memoryMediaControl
}

func (m *memoryMediaData) OpenDownload(_ context.Context, handle, _ string) (io.ReadCloser, error) {
	if handle != m.control.input.Handle {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(m.controlInput())), nil
}

func (m *memoryMediaData) controlInput() []byte {
	return pngBytes()
}

func (m *memoryMediaData) OpenUpload(_ context.Context, transferID, _ string, _ int64) (io.WriteCloser, error) {
	if transferID != "upload-1" {
		return nil, io.ErrClosedPipe
	}
	return &memoryWriteCloser{buffer: &m.control.uploaded}, nil
}

type memoryWriteCloser struct {
	buffer *[]byte
	closed bool
}

func (m *memoryWriteCloser) Write(p []byte) (int, error) {
	if m.closed {
		return 0, io.ErrClosedPipe
	}
	*m.buffer = append(*m.buffer, p...)
	return len(p), nil
}

func (m *memoryWriteCloser) Close() error {
	m.closed = true
	return nil
}

func newMemoryClient() (*serenox.MediaClient, *memoryMediaControl, error) {
	control := &memoryMediaControl{input: protocol.Media{
		Kind: "photo", Handle: "m1_input", FileName: "source.png", MIMEType: "image/png", Size: int64(len(pngBytes())),
	}}
	client, err := serenox.NewMediaClient(control, &memoryMediaData{control: control}, nil)
	return client, control, err
}

func TestConvertMetadata(t *testing.T) {
	metadata, err := newHandler(nil).Metadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := metadata.Validate(); err != nil {
		t.Fatal(err)
	}
	if metadata.ID != pluginID || len(metadata.Commands) != 1 {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestConvertPNGToJPG(t *testing.T) {
	client, control, err := newMemoryClient()
	if err != nil {
		t.Fatal(err)
	}
	handler := newHandler(func(context.Context) *serenox.MediaClient { return client })
	message := protocol.Message{ID: 11, ChatID: 42, Media: &control.input}
	response, err := handler.Dispatch(context.Background(), protocol.DispatchRequest{
		Update:  protocol.Update{ID: "convert-1", Kind: protocol.UpdateMessage, Message: &message},
		Command: &protocol.Command{Name: "convert", RawArgs: "png-jpg", Args: []string{"png-jpg"}, Message: message},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(response.Actions) != 1 || response.Actions[0].Media == nil {
		t.Fatalf("response = %#v", response)
	}
	if response.Actions[0].Media.MIMEType != "image/jpeg" || !strings.HasSuffix(response.Actions[0].Media.FileName, ".jpg") {
		t.Fatalf("media attachment = %#v", response.Actions[0].Media)
	}
	if _, err := jpeg.Decode(bytes.NewReader(control.uploaded)); err != nil {
		t.Fatalf("uploaded result is not JPEG: %v", err)
	}
}

func TestConvertRejectsMissingMedia(t *testing.T) {
	handler := newHandler(func(context.Context) *serenox.MediaClient { return nil })
	message := protocol.Message{ID: 11, ChatID: 42}
	response, err := handler.Dispatch(context.Background(), protocol.DispatchRequest{
		Update:  protocol.Update{ID: "convert-2", Kind: protocol.UpdateMessage, Message: &message},
		Command: &protocol.Command{Name: "convert", RawArgs: "png-jpg", Message: message},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Actions) != 1 || !strings.Contains(response.Actions[0].Text, "image") {
		t.Fatalf("response = %#v", response)
	}
}

func TestConvertReportsUnavailableMediaAPI(t *testing.T) {
	handler := newHandler(func(context.Context) *serenox.MediaClient { return nil })
	media := protocol.Media{Kind: "photo", Handle: "m1_input", FileName: "source.png", MIMEType: "image/png"}
	message := protocol.Message{ID: 11, ChatID: 42, Media: &media}
	response, err := handler.Dispatch(context.Background(), protocol.DispatchRequest{
		Update:  protocol.Update{ID: "convert-3", Kind: protocol.UpdateMessage, Message: &message},
		Command: &protocol.Command{Name: "convert", RawArgs: "png-jpg", Message: message},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Actions) != 1 || !strings.Contains(response.Actions[0].Text, "host media API") {
		t.Fatalf("response = %#v", response)
	}
}

func TestParseFormatPair(t *testing.T) {
	if source, target, err := parseFormatPair("JPEG-png"); err != nil || source != "jpg" || target != "png" {
		t.Fatalf("parse = %q, %q, %v", source, target, err)
	}
	if _, _, err := parseFormatPair("png-gif"); err == nil {
		t.Fatal("unsupported format was accepted")
	}
}

func pngBytes() []byte {
	var buffer bytes.Buffer
	_ = png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	return buffer.Bytes()
}

func checksum(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
