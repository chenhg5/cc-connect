package core

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	progressStyleLegacy  = "legacy"
	progressStyleCompact = "compact"
	progressStyleCard    = "card"
	toolLayoutSplit      = "split"
	toolLayoutMerged     = "merged"

	// ProgressCardPayloadPrefix marks a structured payload for card-style progress.
	ProgressCardPayloadPrefix = "__cc_connect_progress_card_v1__:"

	// Keep a margin below platform hard limit for markdown wrappers/code fences.
	compactProgressMaxChars = maxPlatformMessageLen - 200

	// Bound each platform progress-card API call so a hung upstream request
	// does not block the whole turn forever.
	compactProgressAPITimeout = 15 * time.Second
)

type ProgressCardState string

const (
	ProgressCardStateRunning   ProgressCardState = "running"
	ProgressCardStateCompleted ProgressCardState = "completed"
	ProgressCardStateFailed    ProgressCardState = "failed"
)

type ProgressCardEntryKind string

const (
	ProgressEntryInfo       ProgressCardEntryKind = "info"
	ProgressEntryThinking   ProgressCardEntryKind = "thinking"
	ProgressEntryToolUse    ProgressCardEntryKind = "tool_use"
	ProgressEntryToolResult ProgressCardEntryKind = "tool_result"
	ProgressEntryError      ProgressCardEntryKind = "error"
)

type ProgressCardEntry struct {
	Kind       ProgressCardEntryKind `json:"kind"`
	Text       string                `json:"text,omitempty"`
	Tool       string                `json:"tool,omitempty"`
	Status     string                `json:"status,omitempty"`
	ExitCode   *int                  `json:"exit_code,omitempty"`
	Success    *bool                 `json:"success,omitempty"`
	ToolInput  string                `json:"tool_input,omitempty"`
	ToolResult string                `json:"tool_result,omitempty"`
	Running    bool                  `json:"running,omitempty"`
}

// ProgressCardPayload carries structured progress entries for platforms that
// render custom progress cards.
type ProgressCardPayload struct {
	Version   int                 `json:"version,omitempty"`
	Agent     string              `json:"agent,omitempty"`
	Lang      string              `json:"lang,omitempty"`
	State     ProgressCardState   `json:"state,omitempty"`
	Entries   []string            `json:"entries,omitempty"` // legacy fallback
	Items     []ProgressCardEntry `json:"items,omitempty"`   // ordered typed events
	Truncated bool                `json:"truncated"`
}

// BuildProgressCardPayload encodes progress entries into a transport string.
// This legacy builder keeps compatibility with old callers that only send text.
func BuildProgressCardPayload(entries []string, truncated bool) string {
	cleaned := make([]string, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry != "" {
			cleaned = append(cleaned, entry)
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	payload := ProgressCardPayload{
		Entries:   cleaned,
		Truncated: truncated,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return ProgressCardPayloadPrefix + string(b)
}

// BuildProgressCardPayloadV2 encodes ordered typed progress events.
func BuildProgressCardPayloadV2(items []ProgressCardEntry, truncated bool, agent string, lang Language, state ProgressCardState) string {
	cleaned := make([]ProgressCardEntry, 0, len(items))
	for _, item := range items {
		text := strings.TrimSpace(item.Text)
		input := strings.TrimSpace(item.ToolInput)
		result := strings.TrimSpace(item.ToolResult)
		tool := strings.TrimSpace(item.Tool)
		status := strings.TrimSpace(item.Status)
		if text == "" && input == "" && result == "" && tool == "" {
			continue
		}
		kind := item.Kind
		if kind == "" {
			kind = ProgressEntryInfo
		}
		cleaned = append(cleaned, ProgressCardEntry{
			Kind:       kind,
			Text:       text,
			Tool:       tool,
			Status:     status,
			ExitCode:   item.ExitCode,
			Success:    item.Success,
			ToolInput:  input,
			ToolResult: result,
			Running:    item.Running,
		})
	}
	if len(cleaned) == 0 {
		return ""
	}
	if state == "" {
		state = ProgressCardStateRunning
	}
	payload := ProgressCardPayload{
		Version:   2,
		Agent:     strings.TrimSpace(agent),
		Lang:      string(lang),
		State:     state,
		Items:     cleaned,
		Truncated: truncated,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return ProgressCardPayloadPrefix + string(b)
}

// ParseProgressCardPayload decodes a structured progress payload.
func ParseProgressCardPayload(content string) (*ProgressCardPayload, bool) {
	if !strings.HasPrefix(content, ProgressCardPayloadPrefix) {
		return nil, false
	}
	raw := strings.TrimPrefix(content, ProgressCardPayloadPrefix)
	var payload ProgressCardPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, false
	}
	legacy := make([]string, 0, len(payload.Entries))
	for _, entry := range payload.Entries {
		entry = strings.TrimSpace(entry)
		if entry != "" {
			legacy = append(legacy, entry)
		}
	}
	items := make([]ProgressCardEntry, 0, len(payload.Items))
	for _, item := range payload.Items {
		item.Text = strings.TrimSpace(item.Text)
		item.Tool = strings.TrimSpace(item.Tool)
		item.Status = strings.TrimSpace(item.Status)
		item.ToolInput = strings.TrimSpace(item.ToolInput)
		item.ToolResult = strings.TrimSpace(item.ToolResult)
		if item.Text == "" && item.ToolInput == "" && item.ToolResult == "" && item.Tool == "" {
			continue
		}
		if item.Kind == "" {
			item.Kind = ProgressEntryInfo
		}
		items = append(items, item)
	}
	if len(items) == 0 && len(legacy) > 0 {
		for _, entry := range legacy {
			items = append(items, ProgressCardEntry{
				Kind: inferLegacyEntryKind(entry),
				Text: entry,
			})
		}
	}
	if len(items) == 0 && len(legacy) == 0 {
		return nil, false
	}
	if payload.State == "" {
		payload.State = ProgressCardStateRunning
	}
	payload.Items = items
	payload.Entries = legacy
	if len(payload.Entries) == 0 && len(payload.Items) > 0 {
		payload.Entries = make([]string, 0, len(payload.Items))
		for _, item := range payload.Items {
			payload.Entries = append(payload.Entries, item.Text)
		}
	}
	return &payload, true
}

func inferLegacyEntryKind(entry string) ProgressCardEntryKind {
	switch {
	case strings.HasPrefix(entry, "💭"):
		return ProgressEntryThinking
	case strings.HasPrefix(entry, "🔧"), strings.Contains(entry, "**Tool #"):
		return ProgressEntryToolUse
	case strings.HasPrefix(entry, "🧾"):
		return ProgressEntryToolResult
	case strings.HasPrefix(entry, "❌"):
		return ProgressEntryError
	default:
		return ProgressEntryInfo
	}
}

// compactProgressWriter coalesces intermediate progress (thinking/tool-use)
// into one editable message for platforms that support message updates.
type compactProgressWriter struct {
	ctx       context.Context
	platform  Platform
	replyCtx  any
	transform func(string) string

	starter PreviewStarter
	updater MessageUpdater
	handle  any

	enabled    bool
	failed     bool
	style      string
	usePayload bool

	content    string
	entries    []string
	items      []ProgressCardEntry
	state      ProgressCardState
	agentName  string
	lang       Language
	truncated  bool
	lastSent   string
	maxEntries int
	toolLayout string
	showInput  bool
	showResult bool
	pending    []pendingToolBlock
}

type pendingToolBlock struct {
	itemIndex int
	toolName  string
	resolved  bool
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

func normalizeToolLayout(layout string) string {
	switch strings.ToLower(strings.TrimSpace(layout)) {
	case "", toolLayoutMerged:
		return toolLayoutMerged
	case toolLayoutSplit:
		return toolLayoutSplit
	default:
		return toolLayoutMerged
	}
}

func progressStyleForPlatform(p Platform) string {
	ps := progressStyleLegacy
	if sp, ok := p.(ProgressStyleProvider); ok {
		ps = normalizeProgressStyle(sp.ProgressStyle())
	}
	return ps
}

type progressStyleHintProvider interface {
	progressStyleHint() string
}

type progressCardPayloadHintProvider interface {
	supportsProgressCardPayloadHint() bool
}

func progressStyleFor(p Platform, replyCtx any, display DisplayCfg) string {
	if override := strings.ToLower(strings.TrimSpace(display.ProgressStyle)); override != "" {
		switch override {
		case "auto":
			// Fall through to reply context / platform defaults below.
		case progressStyleLegacy, progressStyleCompact, progressStyleCard:
			return override
		}
	}
	if hint, ok := replyCtx.(progressStyleHintProvider); ok {
		return normalizeProgressStyle(hint.progressStyleHint())
	}
	return progressStyleForPlatform(p)
}

func progressCardPayloadForTarget(p Platform, replyCtx any) bool {
	if hint, ok := replyCtx.(progressCardPayloadHintProvider); ok {
		return hint.supportsProgressCardPayloadHint()
	}
	if cap, ok := p.(ProgressCardPayloadSupport); ok {
		return cap.SupportsProgressCardPayload()
	}
	return false
}
// SuppressStandaloneToolResultEvent is true when a platform opts into progress
// styling (ProgressStyleProvider) but uses legacy mode. In that case tool_use
// lines are still shown, but a separate chat message for EventToolResult is
// skipped to avoid duplicate noise (e.g. Codex structured tool results on Feishu).
// Platforms without ProgressStyleProvider keep showing standalone tool results.
func SuppressStandaloneToolResultEvent(p Platform, display DisplayCfg) bool {
	_, ok := p.(ProgressStyleProvider)
	if !ok {
		return false
	}
	return progressStyleFor(p, nil, display) == progressStyleLegacy
}

func newCompactProgressWriter(ctx context.Context, p Platform, replyCtx any, agentName string, lang Language, display DisplayCfg, transform func(string) string) *compactProgressWriter {
	w := &compactProgressWriter{
		ctx:        ctx,
		platform:   p,
		replyCtx:   replyCtx,
		transform:  transform,
		style:      progressStyleFor(p, replyCtx, display),
		state:      ProgressCardStateRunning,
		agentName:  normalizeProgressAgentLabel(agentName),
		lang:       lang,
		maxEntries: display.ProgressMaxEntries,
		toolLayout: normalizeToolLayout(display.ToolLayout),
		showInput:  display.ToolShowInput,
		showResult: display.ToolShowResultBody,
	}
	if w.style != progressStyleCompact && w.style != progressStyleCard {
		slog.Debug("progress writer disabled: unsupported style", "platform", p.Name(), "style", w.style)
		return w
	}
	updater, ok := p.(MessageUpdater)
	if !ok {
		slog.Debug("progress writer disabled: platform has no MessageUpdater", "platform", p.Name(), "style", w.style)
		return w
	}
	w.enabled = true
	w.updater = updater
	if starter, ok := p.(PreviewStarter); ok {
		w.starter = starter
	}
	if w.style == progressStyleCard {
		if progressCardPayloadForTarget(p, replyCtx) {
			w.usePayload = true
		}
	}
	slog.Debug("progress writer enabled", "platform", p.Name(), "style", w.style, "use_payload", w.usePayload)
	return w
}

func normalizeProgressAgentLabel(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "agent":
		return "Agent"
	case "codex":
		return "Codex"
	case "claudecode", "claude-code", "cc":
		return "CC"
	case "gemini":
		return "Gemini"
	case "cursor":
		return "Cursor"
	case "qoder":
		return "Qoder"
	case "iflow":
		return "iFlow"
	case "opencode":
		return "OpenCode"
	case "pi":
		return "PI"
	default:
		n := strings.TrimSpace(name)
		if n == "" {
			return "Agent"
		}
		return strings.ToUpper(n[:1]) + n[1:]
	}
}

// Append appends one progress item and updates the in-place message.
// Returns true when compact rendering handled this item; false means caller
// should fallback to legacy per-event send.
func (w *compactProgressWriter) Append(item string) bool {
	return w.AppendEvent(ProgressEntryInfo, item, "", item)
}

// AppendEvent appends one typed progress event and updates the in-place message.
// fallback is used for compact/plain rendering when style-specific rendering is not available.
func (w *compactProgressWriter) AppendEvent(kind ProgressCardEntryKind, text string, tool string, fallback string) bool {
	return w.AppendStructured(ProgressCardEntry{
		Kind: kind,
		Text: text,
		Tool: tool,
	}, fallback)
}

// AppendStructured appends one structured progress event and updates the in-place message.
func (w *compactProgressWriter) AppendStructured(item ProgressCardEntry, fallback string) bool {
	if !w.enabled || w.failed {
		return false
	}
	item = normalizeProgressItem(item)
	fallback = strings.TrimSpace(fallback)
	if item.Text == "" && item.ToolInput == "" && item.ToolResult == "" && fallback == "" {
		return true
	}
	if fallback == "" {
		fallback = progressFallbackText(item)
	}
	if fallback == "" {
		fallback = item.Text
	}
	switch item.Kind {
	case ProgressEntryThinking, ProgressEntryError, ProgressEntryInfo:
		if w.transform != nil {
			item.Text = w.transform(item.Text)
			fallback = w.transform(fallback)
		}
	}

	if w.toolLayout == toolLayoutMerged && (item.Kind == ProgressEntryToolUse || item.Kind == ProgressEntryToolResult) {
		w.appendMerged(item, fallback)
	} else {
		w.items = append(w.items, item)
		w.entries = append(w.entries, fallback)
	}
	w.trimEntries()
	w.rebuildContent()

	if w.content == w.lastSent {
		return true
	}

	if w.handle == nil {
		if w.starter != nil {
			callCtx, cancel := w.withAPITimeout()
			handle, err := w.starter.SendPreviewStart(callCtx, w.replyCtx, w.content)
			cancel()
			if err != nil || handle == nil {
				slog.Warn("progress writer: SendPreviewStart failed", "platform", w.platform.Name(), "style", w.style, "error", err, "handle_nil", handle == nil)
				w.failed = true
				return false
			}
			w.handle = handle
			w.lastSent = w.content
			return true
		}
		callCtx, cancel := w.withAPITimeout()
		err := w.platform.Send(callCtx, w.replyCtx, w.content)
		cancel()
		if err != nil {
			slog.Warn("progress writer: initial Send failed", "platform", w.platform.Name(), "style", w.style, "error", err)
			w.failed = true
			return false
		}
		w.handle = w.replyCtx
		w.lastSent = w.content
		return true
	}

	callCtx, cancel := w.withAPITimeout()
	err := w.updater.UpdateMessage(callCtx, w.handle, w.content)
	cancel()
	if err != nil {
		slog.Warn("progress writer: UpdateMessage failed", "platform", w.platform.Name(), "style", w.style, "error", err)
		w.failed = true
		return false
	}
	w.lastSent = w.content
	return true
}

func normalizeProgressItem(item ProgressCardEntry) ProgressCardEntry {
	item.Kind = ProgressCardEntryKind(strings.TrimSpace(string(item.Kind)))
	if item.Kind == "" {
		item.Kind = ProgressEntryInfo
	}
	item.Text = strings.TrimSpace(item.Text)
	item.Tool = strings.TrimSpace(item.Tool)
	item.Status = strings.TrimSpace(item.Status)
	item.ToolInput = strings.TrimSpace(item.ToolInput)
	item.ToolResult = strings.TrimSpace(item.ToolResult)
	if item.Text == "" {
		switch item.Kind {
		case ProgressEntryToolUse:
			item.Text = item.ToolInput
		case ProgressEntryToolResult:
			item.Text = item.ToolResult
		}
	}
	return item
}

func progressFallbackText(item ProgressCardEntry) string {
	switch item.Kind {
	case ProgressEntryToolUse:
		if item.ToolInput != "" {
			return item.ToolInput
		}
		return item.Text
	case ProgressEntryToolResult:
		if item.ToolResult != "" {
			return item.ToolResult
		}
		return item.Text
	default:
		return item.Text
	}
}

func (w *compactProgressWriter) appendMerged(item ProgressCardEntry, fallback string) {
	switch item.Kind {
	case ProgressEntryToolUse:
		merged := ProgressCardEntry{
			Kind:      ProgressEntryToolUse,
			Tool:      item.Tool,
			ToolInput: item.ToolInput,
			Running:   true,
		}
		if merged.ToolInput == "" {
			merged.ToolInput = item.Text
		}
		merged.Text = merged.ToolInput
		entry := fallback
		if entry == "" {
			entry = progressFallbackText(merged)
		}
		w.items = append(w.items, merged)
		w.entries = append(w.entries, entry)
		w.pending = append(w.pending, pendingToolBlock{
			itemIndex: len(w.items) - 1,
			toolName:  merged.Tool,
		})
	case ProgressEntryToolResult:
		idx := w.findPendingToolIndex(item.Tool)
		if idx < 0 {
			w.items = append(w.items, item)
			w.entries = append(w.entries, fallback)
			return
		}
		pending := &w.pending[idx]
		target := pending.itemIndex
		if target < 0 || target >= len(w.items) {
			w.items = append(w.items, item)
			w.entries = append(w.entries, fallback)
			pending.resolved = true
			return
		}
		existing := w.items[target]
		existing.Kind = ProgressEntryToolResult
		existing.Running = false
		if existing.Tool == "" {
			existing.Tool = item.Tool
		}
		existing.Status = item.Status
		existing.ExitCode = item.ExitCode
		existing.Success = item.Success
		if item.ToolInput != "" {
			existing.ToolInput = item.ToolInput
		}
		if item.ToolResult != "" {
			existing.ToolResult = item.ToolResult
		} else if item.Text != "" {
			existing.ToolResult = item.Text
		}
		if existing.Text == "" {
			existing.Text = existing.ToolInput
		}
		w.items[target] = existing
		w.entries[target] = mergedFallbackText(existing)
		pending.resolved = true
	}
}

func mergedFallbackText(item ProgressCardEntry) string {
	parts := make([]string, 0, 2)
	if item.ToolInput != "" {
		parts = append(parts, item.ToolInput)
	}
	if item.ToolResult != "" {
		parts = append(parts, item.ToolResult)
	}
	if len(parts) == 0 {
		return item.Text
	}
	return strings.Join(parts, "\n\n")
}

func buildProgressItemsFromTimeline(events []TimelineEvent, display DisplayCfg) []ProgressCardEntry {
	collector := compactProgressWriter{
		toolLayout: normalizeToolLayout(display.ToolLayout),
		showInput:  display.ToolShowInput,
		showResult: display.ToolShowResultBody,
	}
	for _, ev := range events {
		item, fallback, ok := progressEntryFromTimelineEvent(ev, display)
		if !ok {
			continue
		}
		if collector.toolLayout == toolLayoutMerged && (item.Kind == ProgressEntryToolUse || item.Kind == ProgressEntryToolResult) {
			collector.appendMerged(item, fallback)
		} else {
			collector.items = append(collector.items, item)
			collector.entries = append(collector.entries, fallback)
		}
	}
	return collector.items
}

func progressEntryFromTimelineEvent(ev TimelineEvent, display DisplayCfg) (ProgressCardEntry, string, bool) {
	switch ev.Kind {
	case EventThinking:
		text := strings.TrimSpace(truncateIf(ev.Text, display.ThinkingMaxLen))
		if text == "" {
			return ProgressCardEntry{}, "", false
		}
		item := ProgressCardEntry{
			Kind: ProgressEntryThinking,
			Text: text,
		}
		return item, text, true
	case EventToolUse:
		if !display.ToolMessages {
			return ProgressCardEntry{}, "", false
		}
		input := strings.TrimSpace(ev.ToolInput)
		item := ProgressCardEntry{
			Kind: ProgressEntryToolUse,
			Tool: strings.TrimSpace(ev.ToolName),
		}
		if display.ToolShowInput {
			item.ToolInput = input
			item.Text = input
		}
		return item, progressFallbackText(item), true
	case EventToolResult:
		if !display.ToolMessages {
			return ProgressCardEntry{}, "", false
		}
		result := strings.TrimSpace(ev.ToolResult)
		if result == "" {
			result = strings.TrimSpace(ev.Text)
		}
		if display.ToolShowResultBody {
			result = strings.TrimSpace(truncateIf(result, display.ToolMaxLen))
		} else {
			result = ""
		}
		item := ProgressCardEntry{
			Kind:       ProgressEntryToolResult,
			Tool:       strings.TrimSpace(ev.ToolName),
			Status:     strings.TrimSpace(ev.Status),
			ExitCode:   ev.ExitCode,
			Success:    ev.Success,
			ToolResult: result,
		}
		return item, progressFallbackText(item), true
	case EventError:
		text := strings.TrimSpace(ev.Text)
		if text == "" {
			return ProgressCardEntry{}, "", false
		}
		item := ProgressCardEntry{
			Kind: ProgressEntryError,
			Text: text,
		}
		return item, text, true
	default:
		text := strings.TrimSpace(ev.Text)
		if text == "" {
			return ProgressCardEntry{}, "", false
		}
		item := ProgressCardEntry{
			Kind: ProgressEntryInfo,
			Text: text,
		}
		return item, text, true
	}
}

func (w *compactProgressWriter) findPendingToolIndex(toolName string) int {
	toolName = strings.TrimSpace(toolName)
	for i, pending := range w.pending {
		if pending.resolved {
			continue
		}
		if toolName != "" && strings.EqualFold(strings.TrimSpace(pending.toolName), toolName) {
			return i
		}
	}
	for i, pending := range w.pending {
		if !pending.resolved {
			return i
		}
	}
	return -1
}

func (w *compactProgressWriter) trimEntries() {
	truncated := false
	if w.maxEntries > 0 && len(w.items) > w.maxEntries {
		overflow := len(w.items) - w.maxEntries
		w.items = w.items[overflow:]
		if len(w.entries) > overflow {
			w.entries = w.entries[overflow:]
		} else {
			w.entries = nil
		}
		if len(w.pending) > 0 {
			next := make([]pendingToolBlock, 0, len(w.pending))
			for _, pending := range w.pending {
				if pending.resolved {
					continue
				}
				pending.itemIndex -= overflow
				if pending.itemIndex >= 0 && pending.itemIndex < len(w.items) {
					next = append(next, pending)
				}
			}
			w.pending = next
		}
		truncated = true
	} else if w.maxEntries > 0 && len(w.entries) > w.maxEntries {
		w.entries = w.entries[len(w.entries)-w.maxEntries:]
		truncated = true
	}
	w.truncated = truncated
}

func (w *compactProgressWriter) rebuildContent() {
	switch w.style {
	case progressStyleCard:
		if w.usePayload {
			w.content = BuildProgressCardPayloadV2(w.items, w.truncated, w.agentName, w.lang, w.state)
		} else {
			w.content = renderCardProgressMarkdownFallback(w.entries, w.truncated)
			w.content = trimCompactProgressText(w.content, compactProgressMaxChars)
		}
	default:
		w.content = strings.Join(w.entries, "\n\n")
		w.content = trimCompactProgressText(w.content, compactProgressMaxChars)
	}
	if w.usePayload && w.content == "" {
		slog.Warn("progress writer: failed to build structured payload", "platform", w.platform.Name())
		w.failed = true
	}
}

// Finalize updates card progress state (running/completed/failed) without
// appending a new progress entry.
func (w *compactProgressWriter) Finalize(state ProgressCardState) bool {
	if !w.enabled || w.failed || w.style != progressStyleCard || !w.usePayload || w.handle == nil {
		return false
	}
	if state == "" {
		state = ProgressCardStateCompleted
	}
	if w.state == state {
		return true
	}
	w.state = state
	w.content = BuildProgressCardPayloadV2(w.items, w.truncated, w.agentName, w.lang, w.state)
	if w.content == "" || w.content == w.lastSent {
		return w.content != ""
	}
	callCtx, cancel := w.withAPITimeout()
	err := w.updater.UpdateMessage(callCtx, w.handle, w.content)
	cancel()
	if err != nil {
		slog.Warn("progress writer: Finalize UpdateMessage failed", "platform", w.platform.Name(), "style", w.style, "error", err)
		w.failed = true
		return false
	}
	w.lastSent = w.content
	return true
}

func (w *compactProgressWriter) withAPITimeout() (context.Context, context.CancelFunc) {
	if _, hasDeadline := w.ctx.Deadline(); hasDeadline {
		return w.ctx, func() {}
	}
	return context.WithTimeout(w.ctx, compactProgressAPITimeout)
}

func renderCardProgressMarkdownFallback(entries []string, truncated bool) string {
	var b strings.Builder
	b.WriteString("⏳ **Progress**\n")
	if truncated {
		b.WriteString("_Showing latest updates only._\n")
	}
	for i, entry := range entries {
		b.WriteString("\n")
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". ")
		b.WriteString(strings.ReplaceAll(entry, "\n", "\n   "))
	}
	return b.String()
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
