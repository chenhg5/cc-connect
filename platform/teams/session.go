package teams

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/chenhg5/cc-connect/core"
)

// replyContext carries everything the outbound side needs to answer an activity:
// the serviceURL + conversation to address, the inbound activity id (for threaded
// replies), and the conversation-reference accounts (bot as sender, user as
// recipient) the Bot Connector envelope expects.
type replyContext struct {
	serviceURL     string
	conversationID string
	activityID     string         // the inbound activity id, used as the reply-to-activity target
	botAccount     channelAccount // the bot (inbound recipient) — outbound `from`
	userAccount    channelAccount // the user (inbound from) — outbound `recipient`
}

// engagement tracks which conversations the bot has joined. A bot @mention
// engages a conversation; afterwards messages in it are followed without a
// re-mention. The engaged set is persisted to disk (when a path is configured)
// so engagement survives a process restart; with an empty path it is in-memory
// only (tests / standalone construction).
type engagement struct {
	mu      sync.Mutex
	engaged map[string]bool
	path    string
}

// newEngagement creates an engagement set, loading any previously persisted
// state from path. An empty path disables persistence.
func newEngagement(path string) *engagement {
	e := &engagement{engaged: make(map[string]bool), path: path}
	e.load()
	return e
}

// engagementPath locates the per-project engagement store under the cc-connect
// data dir, mirroring the sessions/<name> convention. Empty inputs disable
// persistence.
func engagementPath(dataDir, project string) string {
	if dataDir == "" || project == "" {
		return ""
	}
	return filepath.Join(dataDir, "teams", sanitizeSegment(project)+"-engaged.json")
}

// sanitizeSegment neutralizes path separators in a config-supplied value used as
// a filename segment (mirrors the weixin platform's handling).
func sanitizeSegment(s string) string {
	return strings.NewReplacer("/", "_", "\\", "_", ":", "_", "\x00", "_").Replace(s)
}

func (e *engagement) load() {
	if e.path == "" {
		return
	}
	data, err := os.ReadFile(e.path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("teams: cannot read engagement store", "path", e.path, "error", err)
		}
		return // missing/unreadable => start empty
	}
	var keys []string
	if err := json.Unmarshal(data, &keys); err != nil {
		slog.Warn("teams: ignoring corrupt engagement store", "path", e.path, "error", err)
		return
	}
	for _, k := range keys {
		e.engaged[k] = true
	}
}

func (e *engagement) engage(key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.engaged[key] {
		return // already engaged — no rewrite
	}
	e.engaged[key] = true
	e.persistLocked()
}

func (e *engagement) isEngaged(key string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.engaged[key]
}

// persistLocked writes the engaged set via core.AtomicWriteFile (temp + fsync +
// rename). The caller must hold e.mu, so writes are serialized — this runs on
// the inbound webhook path but only on a genuinely new engagement (engage()
// early-returns for already-engaged keys), so writes are rare. Failures are
// logged, not fatal — a missed persist only costs a re-mention after a restart.
func (e *engagement) persistLocked() {
	if e.path == "" {
		return
	}
	keys := make([]string, 0, len(e.engaged))
	for k := range e.engaged {
		keys = append(keys, k)
	}
	data, err := json.Marshal(keys)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(e.path), 0o755); err != nil {
		slog.Warn("teams: cannot create engagement dir", "error", err)
		return
	}
	if err := core.AtomicWriteFile(e.path, data, 0o644); err != nil {
		slog.Warn("teams: cannot persist engagement store", "error", err)
	}
}

// sessionKey derives the engine session key for an activity per session_scope:
//   - "thread" (default): the full conversation.id, which for a channel message
//     already identifies the reply thread (`19:...@thread.tacv2;messageid=<root>`);
//     a 1:1 or group id has no ";messageid=" suffix and stands alone.
//   - "channel": the channel root — conversation.id with the ";messageid=" thread
//     suffix stripped — so every thread in a channel shares one session.
//   - "user": the thread id plus the sender, so each user gets an isolated session.
func (p *Platform) sessionKey(a *activity) string {
	conv := a.Conversation.ID
	switch p.cfg.sessionScope {
	case "channel":
		root, _, _ := strings.Cut(conv, ";messageid=")
		return fmt.Sprintf("teams:%s", root)
	case "user":
		return fmt.Sprintf("teams:%s:%s", conv, a.From.ID)
	default: // "thread"
		return fmt.Sprintf("teams:%s", conv)
	}
}

// conversationFromSessionKey extracts the conversation component from a session
// key of the form "teams:<conversationID>" (thread/channel scope) or
// "teams:<conversationID>:<userID>" (user scope). Teams conversation ids
// themselves contain ":", so a user-scoped key cannot be split back into
// conversation and user unambiguously — and it does not need to be: the
// conversation-reference store (convref.go) keys on this function's raw output on
// both the capture and the ReconstructReplyCtx side, so the two agree for every
// scope without ever reversing the userID suffix.
func conversationFromSessionKey(key string) (string, error) {
	rest, ok := strings.CutPrefix(key, "teams:")
	if !ok || rest == "" {
		return "", fmt.Errorf("teams: not a teams session key: %q", key)
	}
	return rest, nil
}

// shouldHandle applies the mention-gate + engaged-thread follow model and reports
// whether the activity should be dispatched. Card actions always pass. Personal
// (1:1) chats always pass: Teams does not allow @mentioning a bot in a personal
// chat, so a mention gate there would silently drop every DM. In a channel/group,
// an @mention engages the thread; afterwards messages in that thread are followed
// without re-mentioning, EXCEPT a message that @mentions other participants but
// not the bot — that is human-to-human side chatter and is ignored (R7).
func (p *Platform) shouldHandle(a *activity, isCardAction bool) bool {
	// Belt-and-suspenders: never act on the bot's own activity. Bot Framework does
	// not redeliver a bot's outbound messages, but a multi-bot install or platform
	// echo must not be able to self-trigger a loop. A bot's channelAccount ID is the
	// "28:<appId>" form, not the bare app ID, so match on the suffix.
	if p.cfg.appID != "" && strings.HasSuffix(a.From.ID, p.cfg.appID) {
		return false
	}
	if isCardAction {
		return true
	}
	if strings.EqualFold(a.Conversation.ConversationType, "personal") {
		return true
	}
	key := a.Conversation.ID
	if a.mentionsBot(p.cfg.appID) {
		p.engaged.engage(key)
		return true
	}
	// A message that mentions someone, but not the bot, is aimed at another person.
	if a.hasMention() {
		return false
	}
	return p.engaged.isEngaged(key)
}
