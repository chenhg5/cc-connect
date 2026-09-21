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

	// ProgressCardPayloadPrefix marks a structured payload for card-style progress.
	ProgressCardPayloadPrefix = "__cc_connect_progress_card_v1__:"

	// Keep a margin below platform hard limit for markdown wrappers/code fences.
	compactProgressMaxChars = maxPlatformMessageLen - 200

	// Bound each platform progress-card API call so a hung upstream request
	// does not block the whole turn forever.
	compactProgressAPITimeout = 15 * time.Second

	// Feishu cardkit rejects cards whose collapsible panels exceed the element
	// limit ("element exceeds the limit", code 300305) — a long agent turn
	// with dozens of tool calls and reasoning steps easily blows past it.
	// Cap per-panel entries in the streaming-card payload so the card never
	// exceeds the limit; excess entries are dropped (the panels stay a
	// faithful prefix record and the payload marks itself truncated, which
	// Feishu renders as a "仅显示最近更新。" note).
	maxThinkingPanelEntries = 20
	maxToolPanelEntries     = 15
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
	Kind     ProgressCardEntryKind `json:"kind"`
	Text     string                `json:"text"`
	Tool     string                `json:"tool,omitempty"`
	Status   string                `json:"status,omitempty"`
	ExitCode *int                  `json:"exit_code,omitempty"`
	Success  *bool                 `json:"success,omitempty"`
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
	Answer    string              `json:"answer,omitempty"` // final reply body rendered below the foldable panels
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
		if text == "" {
			continue
		}
		kind := item.Kind
		if kind == "" {
			kind = ProgressEntryInfo
		}
		cleaned = append(cleaned, ProgressCardEntry{
			Kind:     kind,
			Text:     text,
			Tool:     strings.TrimSpace(item.Tool),
			Status:   strings.TrimSpace(item.Status),
			ExitCode: item.ExitCode,
			Success:  item.Success,
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

// BuildStreamingCardPayload encodes a streaming-card turn (foldable thinking
// and tool panels plus the final answer body) into the transport string used
// by platforms that render the structured progress card (StreamingCard
// with StreamingCardPayloadSupporter). The rendered card looks exactly like
// the compact progress card — foldable "思考 (N)" / "工具 (N)" panels with the
// final answer below — instead of a raw markdown wall of intermediate
// process.
//
// stepTexts holds intermediate per-step text (opencode emits a text event per
// step); each becomes a thinking-panel entry so streaming updates grow the
// foldable panels instead of re-flowing the answer body.
func BuildStreamingCardPayload(thinking string, stepTexts []string, tools []cardToolEntry, answer string, agent string, lang Language, state ProgressCardState) string {
	cleaned := make([]ProgressCardEntry, 0, 8)
	if text := strings.TrimSpace(thinking); text != "" {
		// The engine accumulates every thinking event into stepTexts AND keeps
		// cardThinkingText as the latest one (for the markdown fallback). When
		// the latest thinking is already the last step entry, don't duplicate
		// it in the panel.
		lastStep := ""
		if len(stepTexts) > 0 {
			lastStep = strings.TrimSpace(stepTexts[len(stepTexts)-1])
		}
		if text != lastStep {
			cleaned = append(cleaned, ProgressCardEntry{Kind: ProgressEntryThinking, Text: text})
		}
	}
	for _, step := range stepTexts {
		if text := strings.TrimSpace(step); text != "" {
			// Skip consecutive duplicates (the agent may re-emit the same
			// reasoning/step text) so the panel doesn't show repeated entries.
			if len(cleaned) > 0 && cleaned[len(cleaned)-1].Kind == ProgressEntryThinking && cleaned[len(cleaned)-1].Text == text {
				continue
			}
			cleaned = append(cleaned, ProgressCardEntry{Kind: ProgressEntryThinking, Text: text})
		}
	}
	for _, t := range tools {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			continue
		}
		cleaned = append(cleaned, ProgressCardEntry{Kind: ProgressEntryToolUse, Tool: name, Text: strings.TrimSpace(t.Input)})
	}

	// Cap per-panel entries (Feishu cardkit rejects over-limit cards with
	// code 300305 "element exceeds the limit"). Thinking/step entries keep
	// the first maxThinkingPanelEntries; tool entries keep the first
	// maxToolPanelEntries. Excess entries are dropped — the foldable panels
	// stay a faithful prefix record (append-style updates assume a
	// monotonically growing head) and the card stays within the platform
	// element budget.
	truncated := false
	if len(cleaned) > 0 {
		kept := cleaned[:0]
		thinkingCount, toolCount := 0, 0
		for _, item := range cleaned {
			switch item.Kind {
			case ProgressEntryThinking:
				if thinkingCount >= maxThinkingPanelEntries {
					truncated = true
					continue
				}
				thinkingCount++
			case ProgressEntryToolUse:
				if toolCount >= maxToolPanelEntries {
					truncated = true
					continue
				}
				toolCount++
			}
			kept = append(kept, item)
		}
		cleaned = kept
	}

	// A streaming-card turn with no thinking, no step text, no tool calls and
	// no answer body must not produce a payload at all. Otherwise the transport
	// string carries an empty progress card that ParseProgressCardPayload
	// rejects (no items/answer), and the Feishu renderer then falls back to
	// displaying the raw payload prefix as markdown — the
	// "__cc_connect_progress_card_v1__:..." JSON leaked verbatim to users on
	// every simple Q&A turn. Empty turns return "" so the engine has no payload
	// to send (mirrors BuildProgressCardPayloadV2's empty guard).
	if len(cleaned) == 0 && strings.TrimSpace(answer) == "" {
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
		Answer:    strings.TrimSpace(answer),
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
		if item.Text == "" {
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
	if len(items) == 0 && len(legacy) == 0 && strings.TrimSpace(payload.Answer) == "" {
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

	// Throttle message edits to avoid platform rate limits (e.g. Discord ~5 edits/5s).
	minUpdateInterval time.Duration
	lastUpdateAt      time.Time
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

type progressStyleHintProvider interface {
	progressStyleHint() string
}

type progressCardPayloadHintProvider interface {
	supportsProgressCardPayloadHint() bool
}

func progressStyleForTarget(p Platform, replyCtx any) string {
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
func SuppressStandaloneToolResultEvent(p Platform) bool {
	_, ok := p.(ProgressStyleProvider)
	if !ok {
		return false
	}
	return progressStyleForPlatform(p) == progressStyleLegacy
}

func newCompactProgressWriter(ctx context.Context, p Platform, replyCtx any, agentName string, lang Language, transform func(string) string) *compactProgressWriter {
	w := &compactProgressWriter{
		ctx:        ctx,
		platform:   p,
		replyCtx:   replyCtx,
		transform:  transform,
		style:      progressStyleForTarget(p, replyCtx),
		state:      ProgressCardStateRunning,
		agentName:  normalizeProgressAgentLabel(agentName),
		lang:       lang,
		maxEntries: 10,
	}
	if throttler, ok := p.(ProgressUpdateThrottler); ok {
		w.minUpdateInterval = throttler.ProgressUpdateInterval()
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

// disable turns the writer into a no-op. The engine calls this when a
// StreamingCard (single aggregated card) is active for the same turn: running
// both would post two independently-updated cards to the platform (the
// streaming card plus the progress card). With the writer disabled, every
// Append/Finalize short-circuits on enabled==false and the platform sees only
// the streaming card.
func (w *compactProgressWriter) disable() {
	w.enabled = false
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
	return w.appendStructured(item, fallback, false)
}

// AppendStructuredImmediate appends an event and forces an immediate edit.
func (w *compactProgressWriter) AppendStructuredImmediate(item ProgressCardEntry, fallback string) bool {
	return w.appendStructured(item, fallback, true)
}

func (w *compactProgressWriter) appendStructured(item ProgressCardEntry, fallback string, forceUpdate bool) bool {
	if !w.enabled || w.failed {
		return false
	}
	text := strings.TrimSpace(item.Text)
	fallback = strings.TrimSpace(fallback)
	if text == "" && fallback == "" {
		return true
	}
	if text == "" {
		text = fallback
	}
	if fallback == "" {
		fallback = text
	}
	switch item.Kind {
	case ProgressEntryThinking, ProgressEntryError, ProgressEntryInfo:
		if w.transform != nil {
			text = w.transform(text)
			fallback = w.transform(fallback)
		}
	}
	kind := item.Kind
	if kind == "" {
		kind = ProgressEntryInfo
	}
	item.Kind = kind
	item.Text = text
	item.Tool = strings.TrimSpace(item.Tool)
	item.Status = strings.TrimSpace(item.Status)

	switch w.style {
	case progressStyleCard:
		w.items = append(w.items, item)
		w.entries = append(w.entries, fallback)
		truncated := false
		if w.maxEntries > 0 && len(w.items) > w.maxEntries {
			w.items = w.items[len(w.items)-w.maxEntries:]
			if len(w.entries) > w.maxEntries {
				w.entries = w.entries[len(w.entries)-w.maxEntries:]
			}
			truncated = true
		} else if w.maxEntries > 0 && len(w.entries) > w.maxEntries {
			w.entries = w.entries[len(w.entries)-w.maxEntries:]
			truncated = true
		}
		w.truncated = truncated
		if w.usePayload {
			w.content = BuildProgressCardPayloadV2(w.items, w.truncated, w.agentName, w.lang, w.state)
			if w.content == "" {
				slog.Warn("progress writer: failed to build structured payload", "platform", w.platform.Name())
				w.failed = true
				return false
			}
		} else {
			w.content = renderCardProgressMarkdownFallback(w.entries, truncated)
			w.content = trimCompactProgressText(w.content, compactProgressMaxChars)
		}
	default:
		if w.content == "" {
			w.content = fallback
		} else {
			w.content += "\n\n" + fallback
		}
		w.content = trimCompactProgressText(w.content, compactProgressMaxChars)
	}

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
			w.lastUpdateAt = time.Now()
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
		w.lastUpdateAt = time.Now()
		return true
	}

	if !forceUpdate && w.minUpdateInterval > 0 && time.Since(w.lastUpdateAt) < w.minUpdateInterval {
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
	w.lastUpdateAt = time.Now()
	return true
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
