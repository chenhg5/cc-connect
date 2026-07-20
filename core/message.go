package core

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// UnauthorizedAccessMessage is safe to show to an inbound sender when the
// platform boundary rejects their identity. Keep it user-facing: do not mention
// allow_from, user IDs, or chat IDs.
const UnauthorizedAccessMessage = "角色未授权，请联系管理员添加权限。"

// MergeEnv returns base env with entries from extra overriding same-key entries.
// This prevents duplicate keys (e.g. two PATH entries) which cause the override
// to be silently ignored on Linux (getenv returns the first match).
func MergeEnv(base, extra []string) []string {
	keys := make(map[string]bool, len(extra))
	for _, e := range extra {
		if k, _, ok := strings.Cut(e, "="); ok {
			keys[k] = true
		}
	}
	merged := make([]string, 0, len(base)+len(extra))
	for _, e := range base {
		if k, _, ok := strings.Cut(e, "="); ok && keys[k] {
			continue
		}
		merged = append(merged, e)
	}
	return append(merged, extra...)
}

// CheckAllowFrom logs a security warning at startup when allow_from is not
// configured (defaults to permit-all). Platforms should call this during init.
func CheckAllowFrom(platform, allowFrom string) {
	if strings.TrimSpace(allowFrom) == "" {
		slog.Warn("allow_from is not set — all users are permitted. "+
			"Set allow_from in config to restrict access.",
			"platform", platform)
	}
}

// RedactToken replaces a secret token in text with [REDACTED] to prevent
// token leakage in logs or error messages.
func RedactToken(text, token string) string {
	if token == "" || text == "" {
		return text
	}
	return strings.ReplaceAll(text, token, "[REDACTED]")
}

// AllowList checks whether a user ID is permitted based on a comma-separated
// allow_from string. Returns true if allowFrom is empty or "*" (allow all),
// or if the userID is in the list. Comparison is case-insensitive.
func AllowList(allowFrom, userID string) bool {
	allowFrom = strings.TrimSpace(allowFrom)
	if allowFrom == "" || allowFrom == "*" {
		return true
	}
	for _, id := range strings.Split(allowFrom, ",") {
		if strings.EqualFold(strings.TrimSpace(id), userID) {
			return true
		}
	}
	return false
}

// ImageAttachment represents an image sent by the user.
type ImageAttachment struct {
	MimeType string // e.g. "image/png", "image/jpeg"
	Data     []byte // raw image bytes
	FileName string // original filename (optional)
}

// FileAttachment represents a file (PDF, doc, spreadsheet, etc.) sent by the user.
type FileAttachment struct {
	MimeType string // e.g. "application/pdf", "text/plain"
	Data     []byte // raw file bytes
	FileName string // original filename
}

// SaveFilesToDisk saves file attachments to disk and returns the list of
// absolute file paths. Agents can reference these paths in their prompts so
// the CLI can read them with built-in tools.
//
// Layout:
//
//	messageID == "": <workDir>/.cc-connect/attachments/<sanitized_name>
//	messageID != "": <workDir>/.cc-connect/attachments/<messageID>/<sanitized_name>
//
// Scoping files to a per-message subdirectory (issue #1552) prevents the
// silent-overwrite data loss that occurred when two different messages
// happened to carry attachments with the same filename: under the old flat
// layout, the second os.WriteFile truncated the first file while the
// returned paths slice still referenced the (now overwritten) target by
// name — leaving callers with N path entries pointing at 1 surviving file.
// With messageID-scoped subdirs, two uploads with the same filename land
// in different directories and both survive. Duplicate names within one message
// receive a numeric suffix rather than replacing an earlier attachment.
//
// workDir may be absolute or relative; the returned paths are always
// absolute. When workDir is relative, filepath.Abs resolves it against
// the cc-connect process's current working directory, so callers running
// from different cwd contexts (especially those where the agent's
// "workDir" is itself relative to the user's home, like "~/project") still
// get paths the agent can actually open. An empty workDir falls back to
// the process cwd, which is a reasonable last-resort default for
// misconfigured deploys.
//
// The attachment FileName is treated as untrusted user input (it comes
// from the IM/HTTP upload metadata) and is sanitized to a basename before
// being joined into the attachments directory. Without this,
// FileName="../../escape.txt" was written outside the intended attachments
// directory.
//
// messageID is also treated as untrusted: it is sanitized to a basename
// (same rules as FileName) before being joined into the attachments dir.
// Real platform message IDs are alphanumeric / dash / underscore, but a
// hostile or buggy platform could send anything.
//
// Fallback (messageID == ""):
//
//	Used by older callers that have not been updated to thread messageID
//	through. Each file is written to a unique tempfile in the attachments
//	dir, then atomically linked (os.Link) to the final sanitized name. If
//	the final name already exists, the link fails loudly (no silent
//	overwrite) and the file is skipped. This protects against within-call
//	collisions but does not protect against concurrent calls racing on the
//	same name — that case is fixed only when messageID is provided.
func SaveFilesToDisk(workDir, messageID string, files []FileAttachment) []string {
	if len(files) == 0 {
		return nil
	}
	// Absolutize workDir so the returned paths are usable no matter where the
	// process is invoked from. See issue #1459: when workDir is relative
	// (e.g. ".cc-connect" or "project/sub"), the agent's prompt referenced
	// ".cc-connect/attachments/<file>" while the file actually landed at
	// workDir/.cc-connect/attachments/<file> — a path mismatch that lost
	// every attachment.
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		// Fall back to the original input. filepath.Join on a relative
		// workDir is still better than failing the whole save, and
		// AppendFileRefs below defensively absolutizes anyway.
		absWorkDir = workDir
		slog.Warn("SaveFilesToDisk: filepath.Abs failed, using raw workDir", "workDir", workDir, "error", err)
	}
	attachDir := filepath.Join(absWorkDir, ".cc-connect", "attachments")
	scoped := false
	if messageID != "" {
		safeMid := sanitizeAttachmentFileName(messageID)
		if safeMid == "" {
			slog.Warn("SaveFilesToDisk: messageID sanitized to empty, falling back to flat layout", "messageID", messageID)
		} else {
			attachDir = filepath.Join(attachDir, safeMid)
			scoped = true
		}
	}
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		slog.Warn("SaveFilesToDisk: mkdir failed", "dir", attachDir, "error", err)
	}

	var paths []string
	for i, f := range files {
		fname := sanitizeAttachmentFileName(f.FileName)
		if fname == "" {
			fname = fmt.Sprintf("file_%d_%d", time.Now().UnixNano(), i)
		}
		fpath := filepath.Join(attachDir, fname)
		if scoped {
			var ok bool
			fpath, ok = writeUniqueNoOverwrite(attachDir, fname, f.Data)
			if !ok {
				continue
			}
		} else {
			// Fallback: write to a unique tempfile, then atomic-link to
			// the final name. If the final name already exists, refuse to
			// overwrite — fail loudly rather than silently truncating the
			// previous upload.
			if !writeAtomicNoOverwrite(attachDir, fname, f.Data) {
				continue
			}
		}
		paths = append(paths, fpath)
		slog.Debug("SaveFilesToDisk: file saved", "path", fpath, "name", f.FileName, "mime", f.MimeType, "size", len(f.Data))
	}
	return paths
}

// writeUniqueNoOverwrite creates a new file without replacing an existing name.
// A numeric suffix is added when the sanitized name is already present.
func writeUniqueNoOverwrite(dir, name string, data []byte) (string, bool) {
	for i := 0; ; i++ {
		candidate := name
		if i > 0 {
			ext := filepath.Ext(name)
			stem := strings.TrimSuffix(name, ext)
			candidate = fmt.Sprintf("%s_%d%s", stem, i, ext)
		}
		path := filepath.Join(dir, candidate)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			slog.Error("SaveFilesToDisk: create failed", "path", path, "error", err)
			return "", false
		}
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			_ = os.Remove(path)
			slog.Error("SaveFilesToDisk: write failed", "path", path, "error", err)
			return "", false
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(path)
			slog.Error("SaveFilesToDisk: close failed", "path", path, "error", err)
			return "", false
		}
		return path, true
	}
}

// writeAtomicNoOverwrite writes data to a unique tempfile in dir, then
// hard-links it to dir/name. If dir/name already exists, the link step
// fails and the function returns false without writing — no silent
// overwrite. Returns true on success.
func writeAtomicNoOverwrite(dir, name string, data []byte) bool {
	tmp, err := os.CreateTemp(dir, ".tmp_*")
	if err != nil {
		slog.Error("SaveFilesToDisk: CreateTemp failed", "dir", dir, "error", err)
		return false
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		if cerr := tmp.Close(); cerr != nil {
			slog.Warn("SaveFilesToDisk: tempfile close failed during write-failure cleanup", "tmp", tmpName, "error", cerr)
		}
		if rerr := os.Remove(tmpName); rerr != nil {
			slog.Warn("SaveFilesToDisk: tempfile remove failed during write-failure cleanup", "tmp", tmpName, "error", rerr)
		}
		slog.Error("SaveFilesToDisk: tempfile write failed", "tmp", tmpName, "error", err)
		return false
	}
	if err := tmp.Close(); err != nil {
		if rerr := os.Remove(tmpName); rerr != nil {
			slog.Warn("SaveFilesToDisk: tempfile remove failed during close-failure cleanup", "tmp", tmpName, "error", rerr)
		}
		slog.Error("SaveFilesToDisk: tempfile close failed", "tmp", tmpName, "error", err)
		return false
	}
	target := filepath.Join(dir, name)
	if err := os.Link(tmpName, target); err != nil {
		if rerr := os.Remove(tmpName); rerr != nil {
			slog.Warn("SaveFilesToDisk: tempfile remove failed during link-failure cleanup", "tmp", tmpName, "error", rerr)
		}
		slog.Error("SaveFilesToDisk: refusing to overwrite existing file",
			"path", target, "error", err)
		return false
	}
	if err := os.Remove(tmpName); err != nil {
		slog.Warn("SaveFilesToDisk: tempfile remove failed after successful link", "tmp", tmpName, "error", err)
	}
	return true
}

// sanitizeAttachmentFileName reduces a user-supplied attachment filename to a
// safe basename suitable for joining into an attachment directory. It strips
// any directory components (both `/` and `\`, the latter so Linux strips
// Windows-style paths too) and rejects parent / current-directory references.
// Returns "" when the input cannot produce a safe basename, so callers can
// fall back to a generated name.
func sanitizeAttachmentFileName(name string) string {
	// Normalize backslashes to forward slashes so filepath.Base on any OS
	// strips Windows-style separators in attacker-supplied paths too.
	name = filepath.ToSlash(name)
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." {
		return ""
	}
	return name
}

// AppendFileRefs appends file path references to a prompt string.
//
// File paths are defensively absolutized so the prompt handed to the agent
// always points at a real on-disk location, even when a caller passed a
// relative path by mistake. This guards against the issue #1459 class of
// bugs where the prompt referenced ".cc-connect/attachments/<file>" while
// the file actually landed at the absolute version of workDir. Absolute
// inputs are passed through unchanged. An unresolvable relative path falls
// back to the raw input rather than dropping the reference.
func AppendFileRefs(prompt string, filePaths []string) string {
	if len(filePaths) == 0 {
		return prompt
	}
	if prompt == "" {
		prompt = "Please analyze the attached file(s)."
	}
	abs := make([]string, len(filePaths))
	for i, p := range filePaths {
		if filepath.IsAbs(p) {
			abs[i] = p
			continue
		}
		if a, err := filepath.Abs(p); err == nil {
			abs[i] = a
		} else {
			abs[i] = p
		}
	}
	return prompt + "\n\n(Files saved locally, please read them: " + strings.Join(abs, ", ") + ")"
}

// AudioAttachment represents a voice/audio message sent by the user.
type AudioAttachment struct {
	MimeType string // e.g. "audio/amr", "audio/ogg", "audio/mp4"
	Data     []byte // raw audio bytes
	Format   string // short format hint: "amr", "ogg", "m4a", "mp3", "wav", etc.
	Duration int    // duration in seconds (if known)
}

// LocationAttachment represents a geographical location sent by the user.
type LocationAttachment struct {
	Latitude             float64 // latitude coordinate
	Longitude            float64 // longitude coordinate
	HorizontalAccuracy   float64 // accuracy radius in meters (optional)
	LivePeriod           int     // time period for live location updates in seconds (optional)
	Heading              int     // direction of movement in degrees (optional)
	ProximityAlertRadius int     // maximum distance for proximity alerts in meters (optional)
}

// Message represents a unified incoming message from any platform.
type Message struct {
	SessionKey   string // unique key for user context, e.g. "feishu:{chatID}:{userID}"
	Platform     string
	MessageID    string // platform message ID for tracing
	Recalled     bool   // true for platform message recall/delete events targeting MessageID
	ChannelID    string
	UserID       string
	UserName     string
	ChatName     string // human-readable chat/group name (optional)
	Content      string
	Images       []ImageAttachment   // attached images (if any)
	Files        []FileAttachment    // attached files (if any)
	Audio        *AudioAttachment    // voice message (if any)
	Location     *LocationAttachment // geographical location (if any)
	ExtraContent string              // platform-enriched content (e.g. location text, reply quote) prepended for the agent
	OnAccepted   func()              // called once when the engine accepts this message for an agent turn
	ChannelKey   string              // platform-provided channel identifier for workspace binding (optional)
	// LegacyChannelKey is the platform-provided channel identifier used by an
	// older workspace-binding scope. When both keys are set, multi-workspace
	// routing atomically migrates the legacy binding to ChannelKey.
	LegacyChannelKey string
	ReplyCtx         any    // platform-specific context needed for replying
	FromVoice        bool   // true if message originated from voice transcription
	ModeOverride     string // if set, temporarily override agent permission mode for this message
	// IsPermissionResponse is set by inline-button / card-action paths in
	// platforms when a synthesized message is forwarded as a permission
	// decision (e.g. Telegram handleCallbackQuery for perm:allow/deny,
	// Feishu onCardAction, QQBot interaction button, bridge card_action).
	// The engine uses this flag to drop STALE callbacks silently when no
	// matching pending request exists, instead of letting the literal
	// "allow"/"deny" string reach the agent prompt stream. Plain text
	// "allow"/"deny" typed by a real user must NOT set this flag — they
	// continue to flow through the regular message handler.
	IsPermissionResponse bool
	// UserMessageTimeMs is the platform message creation time in Unix milliseconds
	// when known (e.g. Feishu im.message.message_received create_time). Used to
	// drop late redeliveries that reuse a new message_id but an older create_time
	// than a message already processed. Zero means unset (no ordering hint).
	UserMessageTimeMs int64
}

// EventType distinguishes different kinds of agent output.
type EventType string

const (
	EventText              EventType = "text"               // intermediate or final text
	EventToolUse           EventType = "tool_use"           // tool invocation info
	EventToolResult        EventType = "tool_result"        // tool execution result
	EventResult            EventType = "result"             // final aggregated result
	EventError             EventType = "error"              // error occurred
	EventPermissionRequest EventType = "permission_request" // agent requests permission via stdio protocol
	EventThinking          EventType = "thinking"           // thinking/processing status
)

// UserQuestion represents a structured question from AskUserQuestion.
type UserQuestion struct {
	Question    string               `json:"question"`
	Header      string               `json:"header"`
	Options     []UserQuestionOption `json:"options"`
	MultiSelect bool                 `json:"multiSelect"`
}

// UserQuestionOption is one choice in a UserQuestion.
type UserQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Event represents a single piece of agent output streamed back to the engine.
type Event struct {
	Type                     EventType
	Content                  string
	ToolName                 string         // populated for EventToolUse, EventPermissionRequest
	ToolInput                string         // human-readable summary of tool input
	ToolInputRaw             map[string]any // raw tool input (for EventPermissionRequest, used in allow response)
	ToolResult               string         // populated for EventToolResult
	ToolStatus               string         // optional status for EventToolResult (e.g. completed/failed)
	ToolExitCode             *int           // optional exit code for EventToolResult
	ToolSuccess              *bool          // optional success flag for EventToolResult
	SessionID                string         // agent-managed session ID for conversation continuity
	RequestID                string         // unique request ID for EventPermissionRequest
	Questions                []UserQuestion // populated when ToolName == "AskUserQuestion"
	Done                     bool
	Error                    error
	InputTokens              int // token usage from agent result events
	OutputTokens             int
	CacheCreationInputTokens int            // cache-write tokens (new content written to cache)
	CacheReadInputTokens     int            // cache-read tokens (prior context retrieved from cache)
	Metadata                 map[string]any // optional metadata from agent (e.g. compaction_continue)
	Synthetic                bool           // true if this is a synthetic/generated message (not from real user)
}

// HistoryEntry is one turn in a conversation.
type HistoryEntry struct {
	Role      string    `json:"role"` // "user" or "assistant"
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// AgentSessionInfo describes one session as reported by the agent backend.
type AgentSessionInfo struct {
	ID           string
	Summary      string
	MessageCount int
	ModifiedAt   time.Time
	GitBranch    string
}
