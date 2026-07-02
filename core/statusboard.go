package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbot "github.com/go-telegram/bot"
)

// globalStatusBoard is the process-wide StatusBoard instance, set once at
// startup by SetStatusBoard. Engine hook points (BeginTurn/EndTurn/relay
// dispatch) read this directly rather than threading a StatusBoard
// reference through every Engine constructor — mirrors the existing
// globalAPIServer pattern in cmd/cc-connect/main.go. nil until set;
// every call site nil-checks before using it.
var globalStatusBoard *StatusBoard

// SetStatusBoard installs the process-wide StatusBoard instance.
func SetStatusBoard(sb *StatusBoard) {
	globalStatusBoard = sb
}

// StatusBoard maintains a single pinned Telegram message showing live
// status for every seat, refreshed on real state transitions (turn
// start/end, relay dispatch, hung detection) — not on a fixed timer.
//
// It is deliberately NOT wired through the [[projects]] seat/engine
// machinery: no persona, no LLM calls, no session. It posts/edits with
// its own dedicated bot token via direct Bot API calls, so the
// General-channel split between advisory and execution seats
// ("议事堂政策", see BOSS_HANDBOOK) does not apply to it — it isn't a
// conversational seat at all, it's a system component.
type StatusBoard struct {
	mu          sync.Mutex
	enabled     bool
	bot         *tgbot.Bot
	chatID      int64
	storePath   string
	minInterval time.Duration
	lastEditAt  time.Time
	lastText    string
	messageID   int
	pinned      bool
	seats       map[string]*seatStatus
}

type seatStatus struct {
	State     string // "working" | "idle" | "assigned" | "hung"
	Detail    string // task summary, or "from <seat>" for a relay assignment
	UpdatedAt time.Time
}

type statusBoardPersist struct {
	ChatID    int64 `json:"chat_id"`
	MessageID int   `json:"message_id"`
}

// NewStatusBoard constructs a StatusBoard. If enabled is false, every
// public method becomes a cheap no-op — safe to wire into hot paths
// unconditionally without an enabled-check at every call site.
func NewStatusBoard(enabled bool, botToken, chatID, dataDir string) *StatusBoard {
	sb := &StatusBoard{
		enabled:     enabled,
		minInterval: 3 * time.Second,
		seats:       make(map[string]*seatStatus),
	}
	if !enabled {
		return sb
	}
	if id, err := strconv.ParseInt(chatID, 10, 64); err == nil {
		sb.chatID = id
	} else {
		slog.Error("statusboard: invalid chat_id, disabling", "chat_id", chatID, "error", err)
		sb.enabled = false
		return sb
	}
	b, err := tgbot.New(botToken)
	if err != nil {
		slog.Error("statusboard: failed to create bot client, disabling", "error", err)
		sb.enabled = false
		return sb
	}
	sb.bot = b
	if dataDir != "" {
		sb.storePath = filepath.Join(dataDir, "statusboard.json")
		sb.load()
	}
	return sb
}

func (sb *StatusBoard) load() {
	data, err := os.ReadFile(sb.storePath)
	if err != nil {
		return // no persisted board yet — first render will post a new message
	}
	var p statusBoardPersist
	if err := json.Unmarshal(data, &p); err != nil {
		slog.Warn("statusboard: failed to parse persisted state, ignoring", "error", err)
		return
	}
	if p.ChatID == sb.chatID {
		sb.messageID = p.MessageID
	}
}

func (sb *StatusBoard) persist() {
	if sb.storePath == "" {
		return
	}
	data, err := json.Marshal(statusBoardPersist{ChatID: sb.chatID, MessageID: sb.messageID})
	if err != nil {
		return
	}
	if err := os.WriteFile(sb.storePath, data, 0o644); err != nil {
		slog.Warn("statusboard: failed to persist message id", "error", err)
	}
}

// OnTurnStart marks a seat as actively working on a task.
func (sb *StatusBoard) OnTurnStart(project, taskSummary string) {
	sb.transition(project, "working", taskSummary)
}

// OnTurnEnd marks a seat as idle again.
func (sb *StatusBoard) OnTurnEnd(project string) {
	sb.transition(project, "idle", "")
}

// OnRelayDispatched marks the target seat as having just been handed a task.
func (sb *StatusBoard) OnRelayDispatched(fromProject, toProject, taskSummary string) {
	detail := taskSummary
	if fromProject != "" {
		detail = fmt.Sprintf("from %s: %s", fromProject, taskSummary)
	}
	sb.transition(toProject, "assigned", detail)
}

// OnHungDetected marks a seat as possibly stuck. lastOutputAgo is a
// human-readable duration string (e.g. "112s") shown in the board.
func (sb *StatusBoard) OnHungDetected(project, lastOutputAgo string) {
	sb.transition(project, "hung", "last output "+lastOutputAgo+" ago")
}

// recoverIfWasHung clears a "hung" marking once the poller observes the
// seat is idle/working again. It's a no-op for seats that weren't hung,
// so calling it on every poll tick for every seat is cheap.
func (sb *StatusBoard) recoverIfWasHung(project, newState string) {
	sb.mu.Lock()
	prev, ok := sb.seats[project]
	wasHung := ok && prev.State == "hung"
	sb.mu.Unlock()
	if wasHung {
		sb.transition(project, newState, "")
	}
}

func (sb *StatusBoard) transition(project, state, detail string) {
	if !sb.enabled || project == "" {
		return
	}
	sb.mu.Lock()
	prev, ok := sb.seats[project]
	if ok && prev.State == state && prev.Detail == detail {
		sb.mu.Unlock()
		return // no real change — don't even attempt a render
	}
	sb.seats[project] = &seatStatus{State: state, Detail: detail, UpdatedAt: time.Now()}
	sb.mu.Unlock()
	sb.render(context.Background())
}

// render rebuilds the board text and edits (or posts) the Telegram
// message only if the text actually changed and the minimum edit
// interval has elapsed. Safe to call frequently — most calls are no-ops.
func (sb *StatusBoard) render(ctx context.Context) {
	sb.mu.Lock()
	if !sb.enabled || sb.bot == nil {
		sb.mu.Unlock()
		return
	}
	if time.Since(sb.lastEditAt) < sb.minInterval {
		sb.mu.Unlock()
		return // coalesce bursts; the next transition after the window will catch up
	}
	text := sb.renderText()
	if text == sb.lastText {
		sb.mu.Unlock()
		return
	}
	sb.lastText = text
	sb.lastEditAt = time.Now()
	messageID := sb.messageID
	chatID := sb.chatID
	sb.mu.Unlock()

	if messageID == 0 {
		sb.post(ctx, chatID, text)
		return
	}
	_, err := sb.bot.EditMessageText(ctx, &tgbot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: messageID,
		Text:      text,
	})
	if err != nil {
		slog.Warn("statusboard: edit failed, posting new message", "error", err)
		sb.mu.Lock()
		sb.messageID = 0
		sb.mu.Unlock()
		sb.post(ctx, chatID, text)
	}
}

func (sb *StatusBoard) post(ctx context.Context, chatID int64, text string) {
	sent, err := sb.bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	})
	if err != nil {
		slog.Error("statusboard: failed to post board message", "error", err)
		return
	}
	sb.mu.Lock()
	sb.messageID = sent.ID
	pinned := sb.pinned
	sb.mu.Unlock()
	sb.persist()

	if !pinned {
		_, err := sb.bot.PinChatMessage(ctx, &tgbot.PinChatMessageParams{
			ChatID:              chatID,
			MessageID:           sent.ID,
			DisableNotification: true,
		})
		if err != nil {
			slog.Warn("statusboard: failed to pin board message", "error", err)
		} else {
			sb.mu.Lock()
			sb.pinned = true
			sb.mu.Unlock()
		}
	}
}

var statusEmoji = map[string]string{
	"working":  "\U0001F7E2", // green circle
	"idle":     "⚪",     // white circle
	"assigned": "\U0001F535", // blue circle
	"hung":     "⚠️", // warning
}

func (sb *StatusBoard) renderText() string {
	sb.mu.Lock()
	names := make([]string, 0, len(sb.seats))
	snapshot := make(map[string]*seatStatus, len(sb.seats))
	for name, st := range sb.seats {
		names = append(names, name)
		snapshot[name] = st
	}
	sb.mu.Unlock()

	sort.Strings(names)
	var b strings.Builder
	b.WriteString("Nexus Fleet Status\n")
	for _, name := range names {
		st := snapshot[name]
		emoji := statusEmoji[st.State]
		if emoji == "" {
			emoji = "?"
		}
		line := fmt.Sprintf("%s %s — %s", emoji, name, st.State)
		if st.Detail != "" {
			line += ": " + st.Detail
		}
		b.WriteString(line + "\n")
	}
	b.WriteString(fmt.Sprintf("updated %s", time.Now().Format("15:04:05")))
	return b.String()
}
