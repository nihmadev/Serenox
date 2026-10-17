package v1

// This file defines the binary half of the media protocol. It is deliberately
// independent from the JSON framing code: binary content must never be placed
// in a control-plane envelope.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"hash"
	"io"
)

var (
	ErrMediaStreamMagic      = errors.New("invalid media stream magic")
	ErrMediaStreamAuth       = errors.New("media stream authentication failed")
	ErrMediaStreamSequence   = errors.New("media stream sequence is not contiguous")
	ErrMediaStreamSize       = errors.New("media stream size is invalid")
	ErrMediaStreamChunk      = errors.New("media stream chunk is invalid")
	ErrMediaStreamComplete   = errors.New("media stream is incomplete")
	ErrMediaStreamIncomplete = ErrMediaStreamComplete
	ErrMediaStreamChecksum   = errors.New("media stream checksum mismatch")
)

const (
	mediaStreamVersion    byte = 1
	mediaStreamData       byte = 1
	mediaStreamFinal      byte = 2
	mediaStreamAbort      byte = 3
	mediaStreamHeaderSize      = 4 + 1 + 1 + 2 + 8 + 16 + 8 + 8 + 4
	mediaStreamAuthSize        = sha256.Size
)

// MediaStreamHeader is metadata for one authenticated transfer. TransferID is
// opaque and is only used to associate chunks with the control request.
type MediaStreamHeader struct {
	RequestID    uint64
	TransferID   [16]byte
	Sequence     uint64
	DeclaredSize uint64
	PayloadSize  uint32
	Final        bool
}

type MediaStreamWriter struct {
	w       io.Writer
	authKey []byte
	header  MediaStreamHeader
	written uint64
	closed  bool
	hash    hash.Hash
}

func NewMediaStreamWriter(w io.Writer, authKey []byte, requestID uint64, transferID [16]byte, declaredSize uint64) (*MediaStreamWriter, error) {
	if w == nil || len(authKey) == 0 || requestID == 0 || declaredSize > uint64(MediaMaxSizeBytes) {
		return nil, ErrMediaStreamSize
	}
	return &MediaStreamWriter{w: w, authKey: append([]byte(nil), authKey...), header: MediaStreamHeader{RequestID: requestID, TransferID: transferID, DeclaredSize: declaredSize}, hash: sha256.New()}, nil
}

func (w *MediaStreamWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, ErrMediaStreamComplete
	}
	if len(p) == 0 {
		return 0, nil
	}
	if len(p) > MediaChunkSizeBytes || w.written+uint64(len(p)) > w.header.DeclaredSize {
		return 0, ErrMediaStreamChunk
	}
	if err := writeMediaChunk(w.w, w.authKey, w.header, p, mediaStreamData); err != nil {
		return 0, err
	}
	_, _ = w.hash.Write(p)
	w.header.Sequence++
	w.written += uint64(len(p))
	return len(p), nil
}

func (w *MediaStreamWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if w.written != w.header.DeclaredSize {
		return ErrMediaStreamIncomplete
	}
	final := w.header
	final.Final = true
	return writeMediaChunk(w.w, w.authKey, final, w.hash.Sum(nil), mediaStreamFinal)
}

func (w *MediaStreamWriter) Abort() error {
	if w.closed {
		return nil
	}
	w.closed = true
	return writeMediaChunk(w.w, w.authKey, w.header, nil, mediaStreamAbort)
}

type MediaStreamReader struct {
	r         io.Reader
	authKey   []byte
	expected  MediaStreamHeader
	written   uint64
	sequence  uint64
	complete  bool
	hash      hash.Hash
	finalSeen bool
	finalSum  []byte
}

func NewMediaStreamReader(r io.Reader, authKey []byte, requestID uint64, transferID [16]byte, declaredSize uint64) (*MediaStreamReader, error) {
	if r == nil || len(authKey) == 0 || requestID == 0 || declaredSize > uint64(MediaMaxSizeBytes) {
		return nil, ErrMediaStreamSize
	}
	return &MediaStreamReader{r: r, authKey: append([]byte(nil), authKey...), expected: MediaStreamHeader{RequestID: requestID, TransferID: transferID, DeclaredSize: declaredSize}, hash: sha256.New()}, nil
}

func (r *MediaStreamReader) Read(p []byte) (int, error) {
	if r.complete {
		return 0, io.EOF
	}
	if len(p) < MediaChunkSizeBytes {
		return 0, ErrMediaStreamChunk
	}
	frame, payload, err := readMediaChunk(r.r, r.authKey)
	if err != nil {
		return 0, err
	}
	if frame.RequestID != r.expected.RequestID || frame.TransferID != r.expected.TransferID || frame.Sequence != r.sequence || frame.DeclaredSize != r.expected.DeclaredSize || frame.PayloadSize > MediaChunkSizeBytes {
		return 0, ErrMediaStreamSequence
	}
	if frame.Final {
		if len(payload) != sha256.Size || r.written != r.expected.DeclaredSize {
			return 0, ErrMediaStreamIncomplete
		}
		r.finalSeen, r.finalSum = true, append([]byte(nil), payload...)
		return 0, io.EOF
	}
	if len(payload) == 0 {
		return 0, ErrMediaStreamSequence
	}
	if r.written+uint64(len(payload)) > r.expected.DeclaredSize {
		return 0, ErrMediaStreamSize
	}
	n := copy(p, payload)
	if n != len(payload) {
		return 0, ErrMediaStreamChunk
	}
	_, _ = r.hash.Write(payload)
	r.sequence++
	r.written += uint64(n)
	return n, nil
}

func (r *MediaStreamReader) Complete() error {
	if r.complete {
		return nil
	}
	var payload []byte
	if r.finalSeen {
		payload = r.finalSum
	} else {
		frame, framePayload, err := readMediaChunk(r.r, r.authKey)
		if err != nil {
			return err
		}
		if frame.RequestID != r.expected.RequestID || frame.TransferID != r.expected.TransferID || frame.Sequence != r.sequence || !frame.Final {
			return ErrMediaStreamIncomplete
		}
		payload = framePayload
	}
	if len(payload) != sha256.Size || r.written != r.expected.DeclaredSize {
		return ErrMediaStreamIncomplete
	}
	if subtle.ConstantTimeCompare(payload, r.hash.Sum(nil)) != 1 {
		return ErrMediaStreamChecksum
	}
	r.complete = true
	return nil
}

// CopyMediaStream is a bounded, cancellable copy helper for SDKs and hosts.
func CopyMediaStream(ctx context.Context, dst io.Writer, src io.Reader, limit int64) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit < 0 || limit > MediaMaxSizeBytes {
		return 0, ErrMediaStreamSize
	}
	var total int64
	buf := make([]byte, MediaChunkSizeBytes)
	for total < limit {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
		want := int64(len(buf))
		if remaining := limit - total; remaining < want {
			want = remaining
		}
		n, err := src.Read(buf[:want])
		if n == 0 && err == nil {
			return total, io.ErrNoProgress
		}
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return total, werr
			}
			total += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return total, err
		}
	}
	if total != limit {
		return total, ErrMediaStreamIncomplete
	}
	return total, nil
}

func writeMediaChunk(w io.Writer, key []byte, h MediaStreamHeader, payload []byte, kind byte) error {
	if len(payload) > MediaChunkSizeBytes {
		return ErrMediaStreamChunk
	}
	buf := make([]byte, mediaStreamHeaderSize)
	copy(buf[:4], []byte("SRXM"))
	buf[4] = mediaStreamVersion
	buf[5] = kind
	binary.BigEndian.PutUint16(buf[6:8], 0)
	binary.BigEndian.PutUint64(buf[8:16], h.RequestID)
	copy(buf[16:32], h.TransferID[:])
	binary.BigEndian.PutUint64(buf[32:40], h.Sequence)
	binary.BigEndian.PutUint64(buf[40:48], h.DeclaredSize)
	binary.BigEndian.PutUint32(buf[48:52], uint32(len(payload)))
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(buf)
	_, _ = mac.Write(payload)
	tag := mac.Sum(nil)
	if err := writeAll(w, buf); err != nil {
		return err
	}
	if err := writeAll(w, tag); err != nil {
		return err
	}
	return writeAll(w, payload)
}

func readMediaChunk(r io.Reader, key []byte) (MediaStreamHeader, []byte, error) {
	buf := make([]byte, mediaStreamHeaderSize)
	if _, err := io.ReadFull(r, buf); err != nil {
		return MediaStreamHeader{}, nil, err
	}
	if string(buf[:4]) != "SRXM" {
		return MediaStreamHeader{}, nil, ErrMediaStreamMagic
	}
	if buf[4] != mediaStreamVersion {
		return MediaStreamHeader{}, nil, ErrMediaStreamMagic
	}
	kind := buf[5]
	if kind != mediaStreamData && kind != mediaStreamFinal && kind != mediaStreamAbort {
		return MediaStreamHeader{}, nil, ErrMediaStreamChunk
	}
	size := binary.BigEndian.Uint32(buf[48:52])
	if size > MediaChunkSizeBytes {
		return MediaStreamHeader{}, nil, ErrMediaStreamSize
	}
	tag := make([]byte, mediaStreamAuthSize)
	if _, err := io.ReadFull(r, tag); err != nil {
		return MediaStreamHeader{}, nil, err
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(r, payload); err != nil {
		return MediaStreamHeader{}, nil, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(buf)
	_, _ = mac.Write(payload)
	if subtle.ConstantTimeCompare(tag, mac.Sum(nil)) != 1 {
		return MediaStreamHeader{}, nil, ErrMediaStreamAuth
	}
	var transferID [16]byte
	copy(transferID[:], buf[16:32])
	return MediaStreamHeader{RequestID: binary.BigEndian.Uint64(buf[8:16]), TransferID: transferID, Sequence: binary.BigEndian.Uint64(buf[32:40]), DeclaredSize: binary.BigEndian.Uint64(buf[40:48]), PayloadSize: size, Final: kind == mediaStreamFinal}, payload, nil
}
