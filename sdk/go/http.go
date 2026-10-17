package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type httpRPCControl interface {
	Call(context.Context, protocol.Method, any, any) error
}

// HTTPClient is the typed, core-owned outbound HTTP surface. It intentionally
// exposes no net/http client, transport, cookie jar, or socket.
type HTTPClient struct{ control httpRPCControl }

type HTTPResponse struct{ protocol.HTTPResponse }

func newHTTPClient(control httpRPCControl) *HTTPClient { return &HTTPClient{control: control} }

func (c *HTTPClient) Request(ctx context.Context, request protocol.HTTPRequest) (HTTPResponse, error) {
	if c == nil || c.control == nil {
		return HTTPResponse{}, fmt.Errorf("HTTP service is unavailable")
	}
	if err := request.Validate(); err != nil {
		return HTTPResponse{}, err
	}
	var response protocol.HTTPResponse
	if err := c.control.Call(ctx, protocol.MethodHostHTTP, request, &response); err != nil {
		return HTTPResponse{response}, err
	}
	if err := response.Validate(); err != nil {
		return HTTPResponse{response}, err
	}
	return HTTPResponse{response}, nil
}

func (r HTTPResponse) Header(name string) (string, bool) {
	for _, header := range r.Headers {
		if equalFold(header.Name, name) {
			return header.Value, true
		}
	}
	return "", false
}

func (r HTTPResponse) JSON(maxBytes int64, target any) error {
	if maxBytes <= 0 {
		return fmt.Errorf("JSON maximum must be positive")
	}
	if int64(len(r.Body)) > maxBytes || r.Truncated || r.Rejected {
		return fmt.Errorf("response exceeds JSON maximum")
	}
	decoder := json.NewDecoder(bytes.NewReader(r.Body))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode HTTP JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode HTTP JSON: trailing data")
		}
		return fmt.Errorf("decode HTTP JSON: trailing data: %w", err)
	}
	return nil
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x >= 'A' && x <= 'Z' {
			x += 'a' - 'A'
		}
		if y >= 'A' && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
