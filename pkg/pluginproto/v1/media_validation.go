package v1

import (
	"encoding/hex"
)

func (m Media) Validate() error {
	if err := validIdentifier("media.kind", m.Kind); err != nil {
		return err
	}
	if m.Handle == "" && m.FileID == "" {
		return invalid("media.handle", "is required")
	}
	if m.Size < 0 || m.Size > MediaMaxSizeBytes {
		return invalid("media.size", "must be between 0 and %d", MediaMaxSizeBytes)
	}
	for field, value := range map[string]string{"media.handle": m.Handle, "media.file_id": m.FileID, "media.file_name": m.FileName, "media.mime_type": m.MIMEType, "media.checksum": m.Checksum} {
		if err := validString(field, value, false); err != nil {
			return err
		}
	}
	if m.Checksum != "" && (len(m.Checksum) != 64 || !isHex(m.Checksum)) {
		return invalid("media.checksum", "must be a SHA-256 hex digest")
	}
	return nil
}

func (a MediaAttachment) Validate() error {
	if err := validString("action.media.handle", a.Handle, true); err != nil {
		return err
	}
	if err := validIdentifier("action.media.kind", a.Kind); err != nil {
		return err
	}
	if !SupportedMediaKind(a.Kind) {
		return invalid("action.media.kind", "unsupported media kind %q", a.Kind)
	}
	if a.Size < 0 || a.Size > MediaMaxSizeBytes {
		return invalid("action.media.size", "is outside media limit")
	}
	for field, value := range map[string]string{"action.media.mime_type": a.MIMEType, "action.media.file_name": a.FileName, "action.media.checksum": a.Checksum, "action.media.caption": a.Caption, "action.media.parse_mode": a.ParseMode} {
		if err := validString(field, value, false); err != nil {
			return err
		}
	}
	if a.Checksum != "" && (len(a.Checksum) != 64 || !isHex(a.Checksum)) {
		return invalid("action.media.checksum", "must be a SHA-256 hex digest")
	}
	if a.Attributes != nil && (a.Attributes.Width < 0 || a.Attributes.Height < 0 || a.Attributes.DurationSeconds < 0) {
		return invalid("action.media.attributes", "values must not be negative")
	}
	return nil
}

func (r MediaRequest) Validate() error {
	switch r.Operation {
	case MediaInspect, MediaDownload, MediaDelete:
		if err := validString("media.handle", r.Handle, true); err != nil {
			return err
		}
	case MediaUpload:
		if err := validIdentifier("media.kind", r.Kind); err != nil {
			return err
		}
		if !SupportedMediaKind(r.Kind) {
			return invalid("media.kind", "unsupported media kind %q", r.Kind)
		}
		if r.Size <= 0 || r.Size > MediaMaxSizeBytes {
			return invalid("media.size", "must be between 1 and %d", MediaMaxSizeBytes)
		}
	case MediaAttach:
		if err := validString("media.handle", r.Handle, true); err != nil {
			return err
		}
		if r.PeerID == 0 {
			return invalid("media.peer_id", "is required")
		}
	default:
		return invalid("media.operation", "unknown operation %q", r.Operation)
	}
	if r.Size < 0 || r.Size > MediaMaxSizeBytes {
		return invalid("media.size", "is outside media limit")
	}
	if r.Offset < 0 || r.Offset > MediaMaxSizeBytes {
		return invalid("media.offset", "is outside media limit")
	}
	if len(r.Data) > MediaChunkSizeBytes {
		return invalid("media.data", "exceeds media chunk limit")
	}
	if len(r.Data) != 0 && r.Operation != MediaUpload {
		return invalid("media.data", "is only valid for upload")
	}
	if r.Checksum != "" && (len(r.Checksum) != 64 || !isHex(r.Checksum)) {
		return invalid("media.checksum", "must be a SHA-256 hex digest")
	}
	return nil
}

func SupportedMediaKind(kind string) bool {
	switch kind {
	case "photo", "video", "audio", "voice", "document", "sticker", "animation":
		return true
	default:
		return false
	}
}

func (r MediaResult) Validate() error {
	if r.Media != nil {
		if err := r.Media.Validate(); err != nil {
			return err
		}
	}
	if r.ChunkSize < 0 || r.ChunkSize > MediaChunkSizeBytes {
		return invalid("media.chunk_size", "is outside limit")
	}
	if r.MaxSize < 0 || r.MaxSize > MediaMaxSizeBytes {
		return invalid("media.max_size", "is outside limit")
	}
	if len(r.Data) > MediaChunkSizeBytes {
		return invalid("media.data", "exceeds media chunk limit")
	}
	return nil
}

func isHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}
