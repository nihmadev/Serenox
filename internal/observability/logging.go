package observability

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

const redacted = "[REDACTED]"

// NewJSONLogger creates structured JSON logging with conservative key-based
// secret redaction. Message bodies should be logged by ID, not as attributes.
func NewJSONLogger(output io.Writer, level slog.Leveler) *slog.Logger {
	handler := slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level})
	return slog.New(&redactingHandler{next: handler})
}

// NewHumanLogger is the terminal-friendly default. It keeps the same
// structured attributes as the JSON logger but renders one compact line per
// event, for example:
//
//	23:20:29 INFO  Serenox started  transport=live self_id=8612272319
func NewHumanLogger(output io.Writer, level slog.Leveler) *slog.Logger {
	handler := &humanHandler{output: output, level: level, mutex: new(sync.Mutex)}
	return slog.New(&redactingHandler{next: handler})
}

type humanHandler struct {
	output io.Writer
	level  slog.Leveler
	mutex  *sync.Mutex
	attrs  []slog.Attr
	group  string
}

func (h *humanHandler) Enabled(_ context.Context, level slog.Level) bool {
	return h.level == nil || level >= h.level.Level()
}

func (h *humanHandler) Handle(_ context.Context, record slog.Record) error {
	attrs := append([]slog.Attr(nil), h.attrs...)
	record.Attrs(func(attr slog.Attr) bool {
		attrs = append(attrs, attr)
		return true
	})
	var line strings.Builder
	line.WriteString(record.Time.Format("15:04:05"))
	line.WriteByte(' ')
	line.WriteString(fmt.Sprintf("%-5s", record.Level.String()))
	line.WriteByte(' ')
	line.WriteString(strings.ReplaceAll(strings.ReplaceAll(record.Message, "\n", "\\n"), "\r", "\\r"))
	for _, attr := range attrs {
		line.WriteString("  ")
		writeHumanAttr(&line, h.group, attr)
	}
	line.WriteByte('\n')
	h.mutex.Lock()
	defer h.mutex.Unlock()
	_, err := io.WriteString(h.output, line.String())
	return err
}

func (h *humanHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	result := h.clone()
	result.attrs = append(result.attrs, attrs...)
	return result
}

func (h *humanHandler) WithGroup(name string) slog.Handler {
	result := h.clone()
	if result.group == "" {
		result.group = name
	} else if name != "" {
		result.group += "." + name
	}
	return result
}

func (h *humanHandler) clone() *humanHandler {
	return &humanHandler{
		output: h.output, level: h.level, mutex: h.mutex,
		attrs: append([]slog.Attr(nil), h.attrs...), group: h.group,
	}
}

func writeHumanAttr(line *strings.Builder, prefix string, attr slog.Attr) {
	key := attr.Key
	if prefix != "" {
		key = prefix + "." + key
	}
	if key == "" {
		key = "value"
	}
	line.WriteString(key)
	line.WriteByte('=')
	writeHumanValue(line, attr.Value)
}

func writeHumanValue(line *strings.Builder, value slog.Value) {
	switch value.Kind() {
	case slog.KindString:
		line.WriteString(strconv.Quote(value.String()))
	case slog.KindGroup:
		line.WriteByte('{')
		group := value.Group()
		for index, attr := range group {
			if index > 0 {
				line.WriteByte(' ')
			}
			writeHumanAttr(line, "", attr)
		}
		line.WriteByte('}')
	case slog.KindTime:
		line.WriteString(strconv.Quote(value.Time().Format(time.RFC3339Nano)))
	case slog.KindDuration:
		line.WriteString(value.Duration().String())
	default:
		line.WriteString(value.String())
	}
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
