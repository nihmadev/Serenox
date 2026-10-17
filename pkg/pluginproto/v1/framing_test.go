package v1

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	t.Parallel()
	want := Request{
		Protocol: Protocol, RequestID: 7, Kind: KindRequest,
		DeadlineUnixMS: 1000, Method: MethodHealth,
		Payload: []byte(`{"detailed":true}`),
	}
	var wire bytes.Buffer
	if err := WriteFrame(&wire, want, DefaultMaxFrameSize); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	if got := binary.BigEndian.Uint32(wire.Bytes()[:4]); got != uint32(wire.Len()-4) {
		t.Fatalf("length prefix = %d, payload = %d", got, wire.Len()-4)
	}
	var got Request
	if err := ReadFrame(&wire, &got, DefaultMaxFrameSize); err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if got.RequestID != want.RequestID || got.Method != want.Method || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("round trip mismatch: %#v", got)
	}
}

func TestReadFrameRejectsOversizeBeforePayload(t *testing.T) {
	t.Parallel()
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], 4096)
	r := bytes.NewReader(header[:])
	var dst any
	err := ReadFrame(r, &dst, 128)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("error = %v, want ErrFrameTooLarge", err)
	}
	if r.Len() != 0 {
		t.Fatalf("reader consumed unexpected payload bytes")
	}
}

func TestReadFrameRejectsMalformedAndEmpty(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		wire []byte
		want error
	}{
		{name: "empty", wire: []byte{0, 0, 0, 0}, want: ErrEmptyFrame},
		{name: "malformed", wire: append([]byte{0, 0, 0, 1}, '{'), want: ErrMalformedFrame},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var dst any
			if err := ReadFrame(bytes.NewReader(test.wire), &dst, 32); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestWriteFrameRejectsOversizeWithoutWriting(t *testing.T) {
	t.Parallel()
	var wire bytes.Buffer
	err := WriteFrame(&wire, map[string]string{"large": "0123456789"}, 4)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("error = %v, want ErrFrameTooLarge", err)
	}
	if wire.Len() != 0 {
		t.Fatalf("wrote %d bytes for rejected frame", wire.Len())
	}
}
