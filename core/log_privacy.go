package core

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
)

// Use an ephemeral secret so even low-entropy vendor IDs cannot be recovered by
// guessing from a saved log. Retain correlation within one process only.
var operationalLogRequestKey = sync.OnceValue(func() []byte { return []byte(rand.Text()) })

func operationalLogRequestID(v slog.Value) string {
	var id string
	switch v.Kind() {
	case slog.KindString:
		id = v.String()
	case slog.KindInt64:
		id = strconv.FormatInt(v.Int64(), 10)
	case slog.KindUint64:
		id = strconv.FormatUint(v.Uint64(), 10)
	default:
		return ""
	}
	// Do not retain empty IDs or expend unbounded work on untrusted input.
	if id == "" || len(id) > 256 {
		return ""
	}
	mac := hmac.New(sha256.New, operationalLogRequestKey())
	_, _ = mac.Write([]byte(id)) // hash.Hash.Write never returns an error.
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

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
	case "startup_cause":
		// Only fixed, actionable classifications may survive; callers cannot
		// use this field to inject their own error text into operational logs.
		if attr.Value.Kind() == slog.KindString {
			switch attr.Value.String() {
			case "config_not_found", "config_permission_denied", "config_parse_error", "config_read_error", "config_invalid",
				"lock_held", "lock_permission_denied", "lock_unavailable":
				return attr
			}
		}
	case "request_id":
		if id := operationalLogRequestID(attr.Value); id != "" {
			return slog.String(attr.Key, id)
		}
	case "count", "content_len", "elapsed_ms", "bytes", "attempt", "attempts", "status":
		if attr.Value.Kind() == slog.KindInt64 || attr.Value.Kind() == slog.KindUint64 || attr.Value.Kind() == slog.KindDuration {
			return attr
		}
	}
	return slog.String(attr.Key, "[redacted]")
}
