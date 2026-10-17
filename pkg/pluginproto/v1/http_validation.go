package v1

import (
	"fmt"
	"strings"
)

func (h HTTPHeader) Validate(field string) error {
	if err := validString(field+".name", h.Name, true); err != nil {
		return err
	}
	if err := validString(field+".value", h.Value, false); err != nil {
		return err
	}
	if len(h.Name) > MaxHTTPHeaderName || len(h.Value) > MaxHTTPHeaderValue || strings.ContainsAny(h.Name+h.Value, "\r\n") {
		return invalid(field, "exceeds header or control-character limit")
	}
	for _, r := range h.Name {
		if r <= 0x20 || r >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={} \t", r) {
			return invalid(field+".name", "is not a valid HTTP field name")
		}
	}
	return nil
}

func (r HTTPRequest) Validate() error {
	method := strings.ToUpper(strings.TrimSpace(r.Method))
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE":
	default:
		return invalid("http.method", "unsupported method %q", r.Method)
	}
	if method != r.Method {
		return invalid("http.method", "must be uppercase and contain no surrounding whitespace")
	}
	if err := validString("http.url", r.URL, true); err != nil {
		return err
	}
	if len(r.Headers) > MaxHTTPHeaders {
		return invalid("http.headers", "exceed %d entries", MaxHTTPHeaders)
	}
	for i, header := range r.Headers {
		if err := header.Validate(fmt.Sprintf("http.headers[%d]", i)); err != nil {
			return err
		}
		if strings.EqualFold(header.Name, "host") || strings.EqualFold(header.Name, "content-length") ||
			strings.EqualFold(header.Name, "transfer-encoding") || strings.EqualFold(header.Name, "connection") {
			return invalid(fmt.Sprintf("http.headers[%d].name", i), "framing or routing header is reserved")
		}
	}
	if len(r.Body) > MaxHTTPRequestBytes {
		return invalid("http.body", "exceeds %d bytes", MaxHTTPRequestBytes)
	}
	if r.MaxResponseBytes < 0 || r.MaxResponseBytes > MaxHTTPResponseBytes {
		return invalid("http.max_response_bytes", "must be between 0 and %d", MaxHTTPResponseBytes)
	}
	if r.TimeoutMS < 0 || r.TimeoutMS > 300_000 {
		return invalid("http.timeout_ms", "must be between 0 and 300000")
	}
	return nil
}

func (r HTTPResponse) Validate() error {
	if r.Status < 0 || r.Status > 999 {
		return invalid("http.response.status", "is outside the HTTP status range")
	}
	if len(r.Headers) > MaxHTTPHeaders {
		return invalid("http.response.headers", "exceed %d entries", MaxHTTPHeaders)
	}
	for i, header := range r.Headers {
		if err := header.Validate(fmt.Sprintf("http.response.headers[%d]", i)); err != nil {
			return err
		}
	}
	if len(r.Body) > MaxHTTPResponseBytes {
		return invalid("http.response.body", "exceeds %d bytes", MaxHTTPResponseBytes)
	}
	if err := validString("http.response.final_url", r.FinalURL, true); err != nil {
		return err
	}
	if err := validString("http.response.content_type", r.ContentType, false); err != nil {
		return err
	}
	if r.Redirects < 0 || r.Redirects > 10 || r.ElapsedMS < 0 {
		return invalid("http.response", "redirects or elapsed time is invalid")
	}
	if r.Rejected && len(r.Body) != 0 {
		return invalid("http.response.body", "must be empty for a rejected response")
	}
	return nil
}
