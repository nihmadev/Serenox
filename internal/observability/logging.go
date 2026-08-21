package observability

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

const redacted = "[REDACTED]"

// NewJSONLogger creates structured JSON logging with conservative key-based
// secret redaction. Message bodies should be logged by ID, not as attributes.
func NewJSONLogger(output io.Writer, level slog.Leveler) *slog.Logger {
	handler := slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level})
	return slog.New(&redactingHandler{next: handler})
}

type redactingHandler struct{ next slog.Handler }

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	clean := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		clean.AddAttrs(redactAttr(attr))
		return true
	})
	return h.next.Handle(ctx, clean)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for index, attr := range attrs {
		clean[index] = redactAttr(attr)
	}
	return &redactingHandler{next: h.next.WithAttrs(clean)}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{next: h.next.WithGroup(name)}
}

func redactAttr(attr slog.Attr) slog.Attr {
	key := strings.ToLower(attr.Key)
	for _, fragment := range []string{"token", "password", "secret", "api_hash", "session", "phone"} {
		if strings.Contains(key, fragment) {
			return slog.String(attr.Key, redacted)
		}
	}
	if attr.Value.Kind() == slog.KindGroup {
		group := attr.Value.Group()
		for index := range group {
			group[index] = redactAttr(group[index])
		}
		return slog.Group(attr.Key, attrsToAny(group)...)
	}
	return attr
}

func attrsToAny(attrs []slog.Attr) []any {
	result := make([]any, len(attrs))
	for index := range attrs {
		result[index] = attrs[index]
	}
	return result
}
