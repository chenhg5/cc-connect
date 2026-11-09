package feishu

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

// recordingLarkLogger captures calls after the adapter filters and sanitizes them.
type recordingLarkLogger struct {
	infoCalls  int
	warnCalls  int
	errorCalls int
	debugCalls int
	debugArgs  [][]interface{}
}

func (l *recordingLarkLogger) Debug(_ context.Context, args ...interface{}) {
	l.debugCalls++
	cp := make([]interface{}, len(args))
	copy(cp, args)
	l.debugArgs = append(l.debugArgs, cp)
}

// Info records an SDK information event.
func (l *recordingLarkLogger) Info(context.Context, ...interface{}) { l.infoCalls++ }

// Warn records an SDK warning.
func (l *recordingLarkLogger) Warn(context.Context, ...interface{}) { l.warnCalls++ }

// Error records an SDK error.
func (l *recordingLarkLogger) Error(context.Context, ...interface{}) { l.errorCalls++ }

var _ larkcore.Logger = (*recordingLarkLogger)(nil)

// TestSanitizingLogger_DropsHeartbeatDebugLogs keeps heartbeat noise suppressed even with debug enabled.
func TestSanitizingLogger_DropsHeartbeatDebugLogs(t *testing.T) {
	setSDKTestLogLevel(t, slog.LevelDebug)
	inner := &recordingLarkLogger{}
	logger := &sanitizingLogger{inner: inner}

	logger.Debug(context.Background(), "ping success", "conn_id=123")
	logger.Debug(context.Background(), "receive pong", "conn_id=123")

	if inner.debugCalls != 0 {
		t.Fatalf("debug calls = %d, want 0 for heartbeat noise", inner.debugCalls)
	}
}

// TestSanitizingLogger_KeepOtherDebugAndMaskSecrets preserves explicit debug logging and URL redaction.
func TestSanitizingLogger_KeepOtherDebugAndMaskSecrets(t *testing.T) {
	setSDKTestLogLevel(t, slog.LevelDebug)
	inner := &recordingLarkLogger{}
	logger := &sanitizingLogger{inner: inner}

	logger.Debug(context.Background(), "websocket message", "https://x.test/ws?token=abc&conn_id=123")

	if inner.debugCalls != 1 {
		t.Fatalf("debug calls = %d, want 1", inner.debugCalls)
	}
	if len(inner.debugArgs) != 1 || len(inner.debugArgs[0]) < 2 {
		t.Fatalf("debug args = %#v, want one call with args", inner.debugArgs)
	}

	urlArg, ok := inner.debugArgs[0][1].(string)
	if !ok {
		t.Fatalf("second arg type = %T, want string", inner.debugArgs[0][1])
	}
	if strings.Contains(urlArg, "token=abc") || strings.Contains(urlArg, "conn_id=123") {
		t.Fatalf("url arg not masked: %q", urlArg)
	}
}

// setSDKTestLogLevel configures the application log threshold for one serial test.
func setSDKTestLogLevel(t *testing.T, level slog.Level) {
	t.Helper()
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(previous) })
}

// TestSanitizingLogger_RespectsApplicationLogLevel prevents SDK payloads from bypassing the configured threshold.
func TestSanitizingLogger_RespectsApplicationLogLevel(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		t.Run(level.String(), func(t *testing.T) {
			setSDKTestLogLevel(t, level)
			inner := &recordingLarkLogger{}
			logger := &sanitizingLogger{inner: inner}
			ctx := context.Background()
			logger.Debug(ctx, `receive message payload:{"content":"private-test-message"}`)
			logger.Info(ctx, "connected")
			logger.Warn(ctx, "reconnecting")
			logger.Error(ctx, "connection failed")

			for _, event := range []struct {
				level slog.Level
				calls int
			}{
				{slog.LevelDebug, inner.debugCalls},
				{slog.LevelInfo, inner.infoCalls},
				{slog.LevelWarn, inner.warnCalls},
				{slog.LevelError, inner.errorCalls},
			} {
				want := 0
				if event.level >= level {
					want = 1
				}
				if event.calls != want {
					t.Errorf("%s calls = %d, want %d at %s threshold", event.level, event.calls, want, level)
				}
			}
		})
	}
}
