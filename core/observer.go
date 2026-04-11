package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ObserverTarget is an optional interface that platforms can implement to receive
// terminal observation messages. Currently only Slack implements this.
// Other platforms can implement it in the future without changes to core.
type ObserverTarget interface {
	SendObservation(ctx context.Context, channelID, text string) error
}

// observation represents a parsed user or assistant message from a JSONL session log.
type observation struct {
	role      string // "user" or "assistant"
	text      string
	sessionID string
}

// parseObservationLine parses a single JSONL line from a Claude Code session log.
// Returns nil if the line should be skipped (non-message type, sdk-cli entrypoint, etc).
func parseObservationLine(line []byte) *observation {
	var raw map[string]any
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil
	}

	eventType, _ := raw["type"].(string)
	if eventType != "user" && eventType != "assistant" {
		return nil
	}

	// Skip cc-connect's own sessions
	if ep, _ := raw["entrypoint"].(string); ep == "sdk-cli" {
		return nil
	}

	sessionID, _ := raw["sessionId"].(string)
	msg, _ := raw["message"].(map[string]any)
	if msg == nil {
		return nil
	}

	var text string
	content := msg["content"]
	switch c := content.(type) {
	case string:
		text = c
	case []any:
		// Extract text blocks, skip tool_use/thinking
		var parts []string
		for _, block := range c {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if bt, _ := b["type"].(string); bt == "text" {
				if t, _ := b["text"].(string); t != "" {
					parts = append(parts, t)
				}
			}
		}
		text = strings.Join(parts, "\n")
	}

	return &observation{
		role:      eventType,
		text:      text,
		sessionID: sessionID,
	}
}

// startObserver launches the terminal session observer if configured.
// Called from Engine.Start() after platforms are ready.
func (e *Engine) startObserver() {
	if !e.observeEnabled || e.observeProjectDir == "" {
		return
	}

	target := e.findObserverTarget()
	if target == nil {
		slog.Warn("observe: no platform supports observation; --observe ignored")
		return
	}

	channelID := extractChannelID(e.observeSessionKey)
	if channelID == "" {
		slog.Warn("observe: could not extract channel ID from session key", "key", e.observeSessionKey)
		return
	}

	ctx, cancel := context.WithCancel(e.ctx)
	e.observeCancel = cancel

	obs := newSessionObserver(e.observeProjectDir, target, channelID)
	go obs.run(ctx)

	slog.Info("observe: watching terminal sessions", "dir", e.observeProjectDir, "channel", channelID)
}

// sessionObserver watches Claude Code JSONL session logs and forwards
// user/assistant messages to an ObserverTarget (e.g. Slack).
type sessionObserver struct {
	projectDir string
	target     ObserverTarget
	channelID  string
	offsets    map[string]int64 // file path -> last read offset
	mu         sync.Mutex
}

func newSessionObserver(projectDir string, target ObserverTarget, channelID string) *sessionObserver {
	return &sessionObserver{
		projectDir: projectDir,
		target:     target,
		channelID:  channelID,
		offsets:    make(map[string]int64),
	}
}

// run starts the observation loop. It scans for JSONL files, seeks to end
// (for existing files), then polls for new content every 2 seconds.
// Blocks until ctx is cancelled.
func (o *sessionObserver) run(ctx context.Context) {
	// Initial scan: record current end-of-file offsets so we only see NEW content
	o.initOffsets()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.poll(ctx)
		}
	}
}

// initOffsets scans existing JSONL files and records their current size
// so we don't replay historical content on startup.
func (o *sessionObserver) initOffsets() {
	files, err := filepath.Glob(filepath.Join(o.projectDir, "*.jsonl"))
	if err != nil {
		slog.Warn("observe: glob failed", "dir", o.projectDir, "error", err)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		o.offsets[f] = info.Size()
	}
}

// poll checks all JSONL files for new content since last read.
func (o *sessionObserver) poll(ctx context.Context) {
	files, err := filepath.Glob(filepath.Join(o.projectDir, "*.jsonl"))
	if err != nil {
		slog.Warn("observe: glob failed", "dir", o.projectDir, "error", err)
		return
	}
	for _, f := range files {
		o.mu.Lock()
		offset, known := o.offsets[f]
		if !known {
			// New file appeared — start from beginning
			offset = 0
			o.offsets[f] = 0
		}
		o.mu.Unlock()

		newOffset := o.tailFile(ctx, f, offset)
		o.mu.Lock()
		o.offsets[f] = newOffset
		o.mu.Unlock()
	}
}

// tailFile reads new lines from a JSONL file starting at offset.
// Returns the new offset after reading.
func (o *sessionObserver) tailFile(ctx context.Context, path string, offset int64) int64 {
	f, err := os.Open(path)
	if err != nil {
		return offset
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() <= offset {
		return offset
	}

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()

		obs := parseObservationLine(line)
		if obs == nil || obs.text == "" {
			continue
		}

		o.forward(ctx, obs)
	}

	// Use file size as new offset when we've read to EOF cleanly.
	// This avoids offset drift from line-length calculation (e.g. \r\n vs \n).
	if scanner.Err() == nil {
		return info.Size()
	}
	return offset
}

// forward sends a parsed observation to the target platform.
func (o *sessionObserver) forward(ctx context.Context, obs *observation) {
	var msg string
	switch obs.role {
	case "user":
		msg = fmt.Sprintf("user: %s", obs.text)
	case "assistant":
		msg = fmt.Sprintf("\nClaude: %s", obs.text)
	default:
		return
	}

	// Slack has a 4000 char limit per message; truncate if needed
	const maxLen = 3900
	if len(msg) > maxLen {
		truncated := msg[:maxLen]
		// Ensure we don't cut mid-rune
		for len(truncated) > 0 && !utf8.ValidString(truncated) {
			truncated = truncated[:len(truncated)-1]
		}
		msg = truncated + "\n... (truncated)"
	}

	if err := o.target.SendObservation(ctx, o.channelID, msg); err != nil {
		slog.Warn("observe: forward failed", "error", err)
	}
}
