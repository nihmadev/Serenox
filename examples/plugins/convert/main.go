// Command convert implements `.convert <source>-<target>` for attached images.
// It intentionally uses only the transport-neutral Serenox media API and the
// Go standard library image codecs.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
	serenox "github.com/nihmadev/Serenox/sdk/go"
)

const (
	pluginID      = "example.convert"
	pluginVersion = "1.0.5"
	maxPixels     = int64(25_000_000)
)

type mediaProvider func(context.Context) *serenox.MediaClient

func main() {
	if err := serenox.Run(context.Background(), newHandler(nil), serenox.Config{Workers: 1, QueueSize: 8}); err != nil && !errors.Is(err, context.Canceled) {
		panic(err)
	}
}

func newHandler(provider mediaProvider) serenox.Handler {
	if provider == nil {
		provider = func(ctx context.Context) *serenox.MediaClient {
			return serenox.MediaClientFromContext(ctx)
		}
	}
	return serenox.HandlerFuncs{
		MetadataFunc: func(context.Context) (protocol.PluginMetadata, error) {
			return protocol.PluginMetadata{
				ID: pluginID, Name: "Convert", Version: pluginVersion,
				Description: "Convert attached PNG/JPEG images with .convert source-target",
				Author:      "Serenox", ProtocolVersion: protocol.Version,
				Capabilities: []protocol.Capability{
					protocol.CapabilityCommands,
					protocol.CapabilityMediaRead,
					protocol.CapabilityMediaWrite,
					protocol.CapabilityMediaAttach,
				},
				Commands: []protocol.CommandSpec{{
					Name: "convert", Description: "Convert an attached image, for example png-jpg", OwnerOnly: true,
				}},
			}, nil
		},
		DispatchFunc: func(ctx context.Context, request protocol.DispatchRequest) (protocol.DispatchResponse, error) {
			return dispatch(ctx, request, provider(ctx))
		},
	}
}

func dispatch(ctx context.Context, request protocol.DispatchRequest, media *serenox.MediaClient) (protocol.DispatchResponse, error) {
	if request.Command == nil || request.Command.Name != "convert" {
		return protocol.DispatchResponse{}, nil
	}
	message := request.Command.Message
	source, target, err := parseFormatPair(request.Command.RawArgs)
	if err != nil {
		return textReply(request.Update.ID, message, "Usage: .convert png-jpg (supported formats: png, jpg, jpeg)"), nil
	}
	if message.Media == nil || message.Media.Handle == "" {
		return textReply(request.Update.ID, message, "Reply to or caption an image before using .convert."), nil
	}
	if media == nil {
		return textReply(request.Update.ID, message, "Conversion is unavailable: the daemon has not enabled the host media API yet."), nil
	}

	input, err := media.Inspect(ctx, message.Media.Handle)
	if err != nil {
		return textReply(request.Update.ID, message, "Conversion failed while reading the source media: "+err.Error()), nil
	}
	if !isImageMedia(input) {
		return textReply(request.Update.ID, message, "Only image media can be converted by this plugin."), nil
	}

	inputFile, err := os.CreateTemp("", "serenox-convert-input-")
	if err != nil {
		return protocol.DispatchResponse{}, fmt.Errorf("create input temporary file: %w", err)
	}
	inputPath := inputFile.Name()
	defer os.Remove(inputPath) // best-effort cleanup of plugin-owned temporary data
	if _, err := media.Download(ctx, message.Media.Handle, inputFile); err != nil {
		_ = inputFile.Close()
		return textReply(request.Update.ID, message, "Conversion failed while downloading the source media: "+err.Error()), nil
	}
	if err := inputFile.Close(); err != nil {
		return protocol.DispatchResponse{}, fmt.Errorf("close input temporary file: %w", err)
	}

	outputFile, err := os.CreateTemp("", "serenox-convert-output-")
	if err != nil {
		return protocol.DispatchResponse{}, fmt.Errorf("create output temporary file: %w", err)
	}
	outputPath := outputFile.Name()
	defer os.Remove(outputPath)
	if err := convertFile(ctx, inputPath, outputFile, source, target); err != nil {
		_ = outputFile.Close()
		return textReply(request.Update.ID, message, "Conversion failed: "+err.Error()), nil
	}
	if err := outputFile.Close(); err != nil {
		return protocol.DispatchResponse{}, fmt.Errorf("close output temporary file: %w", err)
	}

	output, err := os.Open(outputPath)
	if err != nil {
		return protocol.DispatchResponse{}, fmt.Errorf("open converted media: %w", err)
	}
	defer output.Close()
	info, err := output.Stat()
	if err != nil {
		return protocol.DispatchResponse{}, fmt.Errorf("stat converted media: %w", err)
	}
	checksum, err := fileChecksum(output)
	if err != nil {
		return protocol.DispatchResponse{}, fmt.Errorf("checksum converted media: %w", err)
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		return protocol.DispatchResponse{}, fmt.Errorf("rewind converted media: %w", err)
	}
	uploaded, err := media.Upload(ctx, "document", outputName(input.FileName, target), targetMIME(target), info.Size(), checksum, output)
	if err != nil {
		return textReply(request.Update.ID, message, "Conversion failed while uploading the result: "+err.Error()), nil
	}

	return protocol.DispatchResponse{Actions: []protocol.Action{{
		ID:   "convert-" + request.Update.ID,
		Kind: protocol.ActionReply, ChatID: message.ChatID, ReplyToMessageID: message.ID,
		Media: &protocol.MediaAttachment{
			Handle: uploaded.Handle, Kind: uploadedKind(target), MIMEType: targetMIME(target),
			FileName: outputName(input.FileName, target), Size: uploaded.Size, Checksum: uploaded.Checksum,
			Caption: fmt.Sprintf("Converted %s to %s", source, target),
		},
	}}}, nil
}

func convertFile(ctx context.Context, inputPath string, output io.Writer, source, target string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	input, err := os.Open(inputPath)
	if err != nil {
		return err
	}
	defer input.Close()
	config, format, err := image.DecodeConfig(input)
	if err != nil {
		return fmt.Errorf("decode %s image: %w", source, err)
	}
	if int64(config.Width) <= 0 || int64(config.Height) <= 0 || int64(config.Width)*int64(config.Height) > maxPixels {
		return fmt.Errorf("image dimensions exceed %d pixels", maxPixels)
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return err
	}
	decoded, decodedFormat, err := image.Decode(input)
	if err != nil {
		return fmt.Errorf("decode %s image: %w", format, err)
	}
	if decodedFormat == "" {
		return errors.New("image format is unknown")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	switch target {
	case "png":
		return png.Encode(output, decoded)
	case "jpg":
		return jpeg.Encode(output, decoded, &jpeg.Options{Quality: 90})
	default:
		return fmt.Errorf("unsupported target format %q", target)
	}
}

func parseFormatPair(value string) (string, string, error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(value)), "-")
	if len(parts) != 2 {
		return "", "", errors.New("format must be source-target")
	}
	source, target := normalizeFormat(parts[0]), normalizeFormat(parts[1])
	if !supportedFormat(source) || !supportedFormat(target) || source == target {
		return "", "", errors.New("unsupported or identical format")
	}
	return source, target, nil
}

func normalizeFormat(value string) string {
	if value == "jpeg" {
		return "jpg"
	}
	return value
}

func supportedFormat(value string) bool { return value == "png" || value == "jpg" }

func targetMIME(value string) string {
	if value == "png" {
		return "image/png"
	}
	return "image/jpeg"
}

func uploadedKind(value string) string {
	// Keep the converted result as a document so Telegram preserves the
	// explicit filename and extension instead of treating JPEG as a photo.
	return "document"
}

func outputName(input, target string) string {
	base := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
	if base == "" || base == "." {
		base = "converted"
	}
	return base + "." + target
}

func isImageMedia(media protocol.Media) bool {
	return media.Kind == "photo" || strings.HasPrefix(strings.ToLower(media.MIMEType), "image/")
}

func fileChecksum(file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func textReply(id string, message protocol.Message, text string) protocol.DispatchResponse {
	return protocol.DispatchResponse{Actions: []protocol.Action{{
		ID: "convert-info-" + id, Kind: protocol.ActionReply,
		ChatID: message.ChatID, ReplyToMessageID: message.ID, Text: text,
	}}}
}
