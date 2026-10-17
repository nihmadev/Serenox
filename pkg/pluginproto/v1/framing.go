package v1

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var (
	ErrEmptyFrame     = errors.New("empty plugin protocol frame")
	ErrFrameTooLarge  = errors.New("plugin protocol frame exceeds configured limit")
	ErrMalformedFrame = errors.New("malformed plugin protocol JSON frame")
)

// WriteFrame writes one uint32 big-endian length-prefixed JSON value. Callers
// sharing a writer must serialize calls to WriteFrame.
func WriteFrame(w io.Writer, value any, maxSize uint32) error {
	if maxSize == 0 {
		return fmt.Errorf("%w: maximum is zero", ErrFrameTooLarge)
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMalformedFrame, err)
	}
	if len(payload) == 0 {
		return ErrEmptyFrame
	}
	if uint64(len(payload)) > uint64(maxSize) {
		return fmt.Errorf("%w: size=%d max=%d", ErrFrameTooLarge, len(payload), maxSize)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeAll(w, header[:]); err != nil {
		return fmt.Errorf("write frame header: %w", err)
	}
	if err := writeAll(w, payload); err != nil {
		return fmt.Errorf("write frame payload: %w", err)
	}
	return nil
}

// ReadFrame reads exactly one frame. An oversized length is rejected before
// allocating or reading the advertised payload; the connection must be closed
// after that terminal protocol error.
func ReadFrame(r io.Reader, dst any, maxSize uint32) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return fmt.Errorf("read frame header: %w", err)
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 {
		return ErrEmptyFrame
	}
	if maxSize == 0 || size > maxSize {
		return fmt.Errorf("%w: size=%d max=%d", ErrFrameTooLarge, size, maxSize)
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(r, payload); err != nil {
		return fmt.Errorf("read frame payload: %w", err)
	}
	if !json.Valid(payload) {
		return ErrMalformedFrame
	}
	if err := json.Unmarshal(payload, dst); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformedFrame, err)
	}
	return nil
}

func writeAll(w io.Writer, payload []byte) error {
	for len(payload) > 0 {
		n, err := w.Write(payload)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		payload = payload[n:]
	}
	return nil
}
