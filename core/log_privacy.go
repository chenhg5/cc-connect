package core

import (
	"fmt"
	"log/slog"
	"strings"
)

// OperationalLogAttr is the field policy for persisted diagnostics at every
// log level. SDKs can interpolate complete payloads into msg, so source function
// and line identify events instead of retaining arbitrary message text.
func OperationalLogAttr(groups []string, attr slog.Attr) slog.Attr {
	if len(groups) == 0 {
		switch attr.Key {
		case slog.TimeKey, slog.LevelKey:
			return attr
		case slog.SourceKey:
			if source, ok := attr.Value.Any().(*slog.Source); ok {
				return slog.String(slog.SourceKey, fmt.Sprintf("%s:%d", source.Function, source.Line))
			}
		}
	}
	switch attr.Key {
	case "request_id":
		value := attr.Value.String()
		if len(value) == 16 && strings.Trim(value, "0123456789abcdefABCDEF") == "" {
			return attr
		}
	case "count", "content_len", "elapsed_ms", "bytes", "attempt", "attempts", "status":
		if attr.Value.Kind() == slog.KindInt64 || attr.Value.Kind() == slog.KindUint64 || attr.Value.Kind() == slog.KindDuration {
			return attr
		}
	}
	return slog.String(attr.Key, "[redacted]")
}
