package v1

// HTTPHeader is a transport-neutral header pair. A slice preserves duplicate
// response fields while keeping JSON framing deterministic and bounded.
type HTTPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type HTTPRequest struct {
	Method           string       `json:"method"`
	URL              string       `json:"url"`
	Headers          []HTTPHeader `json:"headers,omitempty"`
	Body             []byte       `json:"body,omitempty"`
	MaxResponseBytes int64        `json:"max_response_bytes,omitempty"`
	TimeoutMS        int64        `json:"timeout_ms,omitempty"`
}

type HTTPResponse struct {
	Status      int          `json:"status"`
	Headers     []HTTPHeader `json:"headers,omitempty"`
	FinalURL    string       `json:"final_url"`
	Body        []byte       `json:"body,omitempty"`
	Redirects   int          `json:"redirects,omitempty"`
	ElapsedMS   int64        `json:"elapsed_ms"`
	Truncated   bool         `json:"truncated,omitempty"`
	Rejected    bool         `json:"rejected,omitempty"`
	ContentType string       `json:"content_type,omitempty"`
}

const (
	MaxHTTPHeaders     = 64
	MaxHTTPHeaderName  = 256
	MaxHTTPHeaderValue = 8 << 10
	// JSON/base64 encoding must remain below the default 1 MiB negotiated
	// frame even after envelope and header overhead are included.
	MaxHTTPRequestBytes  = 512 << 10
	MaxHTTPResponseBytes = 512 << 10
)
