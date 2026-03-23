package core

import (
	"context"
	"strings"
	"unicode/utf8"
)

const (
	progressStyleLegacy  = "legacy"
	progressStyleCompact = "compact"
	progressStyleCard    = "card"

	// Keep a margin below platform hard limit for markdown wrappers/code fences.
	compactProgressMaxChars = maxPlatformMessageLen - 200
)

// compactProgressWriter coalesces intermediate progress (thinking/tool-use)
// into one editable message for platforms that support message updates.
type compactProgressWriter struct {
	ctx      context.Context
	platform Platform
	replyCtx any

	starter PreviewStarter
	updater MessageUpdater
	handle  any

	enabled bool
	failed  bool

	content  string
	lastSent string
}

func normalizeProgressStyle(style string) string {
	switch strings.ToLower(strings.TrimSpace(style)) {
	case "", progressStyleLegacy:
		return progressStyleLegacy
	case progressStyleCompact:
		return progressStyleCompact
	case progressStyleCard:
		return progressStyleCard
	default:
		return progressStyleLegacy
	}
}

func progressStyleForPlatform(p Platform) string {
	ps := progressStyleLegacy
	if sp, ok := p.(ProgressStyleProvider); ok {
		ps = normalizeProgressStyle(sp.ProgressStyle())
	}
	return ps
}

func newCompactProgressWriter(ctx context.Context, p Platform, replyCtx any) *compactProgressWriter {
	w := &compactProgressWriter{
		ctx:      ctx,
		platform: p,
		replyCtx: replyCtx,
	}
	if progressStyleForPlatform(p) != progressStyleCompact {
		return w
	}
	updater, ok := p.(MessageUpdater)
	if !ok {
		return w
	}
	w.enabled = true
	w.updater = updater
	if starter, ok := p.(PreviewStarter); ok {
		w.starter = starter
	}
	return w
}

// Append appends one progress item and updates the in-place message.
// Returns true when compact rendering handled this item; false means caller
// should fallback to legacy per-event send.
func (w *compactProgressWriter) Append(item string) bool {
	if !w.enabled || w.failed {
		return false
	}
	item = strings.TrimSpace(item)
	if item == "" {
		return true
	}

	if w.content == "" {
		w.content = item
	} else {
		w.content += "\n\n" + item
	}
	w.content = trimCompactProgressText(w.content, compactProgressMaxChars)

	if w.content == w.lastSent {
		return true
	}

	if w.handle == nil {
		if w.starter != nil {
			handle, err := w.starter.SendPreviewStart(w.ctx, w.replyCtx, w.content)
			if err != nil || handle == nil {
				w.failed = true
				return false
			}
			w.handle = handle
			w.lastSent = w.content
			return true
		}
		if err := w.platform.Send(w.ctx, w.replyCtx, w.content); err != nil {
			w.failed = true
			return false
		}
		w.handle = w.replyCtx
		w.lastSent = w.content
		return true
	}

	if err := w.updater.UpdateMessage(w.ctx, w.handle, w.content); err != nil {
		w.failed = true
		return false
	}
	w.lastSent = w.content
	return true
}

func trimCompactProgressText(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return s
	}
	s = strings.TrimPrefix(s, "…\n")
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	rs := []rune(s)
	tail := strings.TrimLeft(string(rs[len(rs)-maxRunes:]), "\n")
	return "…\n" + tail
}
