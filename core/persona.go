package core

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// PersonaClass selects which archive-first preamble variant a seat receives.
type PersonaClass string

const (
	PersonaClassWrite     PersonaClass = "write"
	PersonaClassRead      PersonaClass = "read"
	PersonaClassSecretary PersonaClass = "secretary"
)

// archiveFirstFallback is injected when a seat's preamble file is missing or
// unreadable. Nexus is live production — a broken preamble file must not
// stop a seat from starting, but the seat must never run without at least
// this one-line truth (L-0216 P1, fail-loud-not-fail-stop per L-0215).
const archiveFirstFallback = "你是无状态的壳。F:\\nexus\\docs\\archive\\ 是 Nexus 唯一的持久记忆与心脏。"

// ResolvePersonaClass determines which archive-first preamble variant a seat
// gets. secretary-seat is the sole read-side seat with archive write
// authority (L-0216 Query). Everything else follows the workspace_pattern
// split already used for v1.2 execution-seat classification (L-0123): seats
// with a workspace pattern are "write" (execution seats), the rest are "read".
func ResolvePersonaClass(projectName string, hasWorkspacePattern bool) PersonaClass {
	if projectName == "secretary-seat" {
		return PersonaClassSecretary
	}
	if hasWorkspacePattern {
		return PersonaClassWrite
	}
	return PersonaClassRead
}

// ComposePersona prepends the archive-first preamble (selected by class) to
// personaContent. Missing/unreadable preamble files fall back to a hardcoded
// one-line truth plus a WARN log rather than failing the spawn.
func ComposePersona(personasDir string, class PersonaClass, personaContent string) string {
	preamble := archiveFirstFallback
	if personasDir != "" {
		path := filepath.Join(personasDir, "_preamble", "archive-first."+string(class)+".md")
		if data, err := os.ReadFile(path); err == nil {
			preamble = strings.TrimSpace(string(data))
		} else {
			slog.Warn("archive-first preamble missing — injecting hardcoded fallback",
				"path", path, "class", class, "err", err)
		}
	}
	if personaContent == "" {
		return preamble
	}
	return preamble + "\n\n---\n\n" + personaContent
}

// Markers bounding the archive-first block that gets synced into Codex's
// AGENTS.md, since Codex has no native persona/system-prompt injection path
// (L-0131) and reads project memory from that file instead.
const (
	ArchiveFirstMarkerStart = "<!-- cc-managed:archive-first:start -->"
	ArchiveFirstMarkerEnd   = "<!-- cc-managed:archive-first:end -->"
)

// SyncManagedBlock writes content into filePath bounded by startMarker/
// endMarker, replacing any existing bounded block in place and preserving
// the rest of the file. Creates the file (and parent dirs) if missing.
func SyncManagedBlock(filePath, startMarker, endMarker, content string) error {
	existing, _ := os.ReadFile(filePath)
	text := string(existing)
	block := startMarker + "\n" + content + "\n" + endMarker

	startIdx := strings.Index(text, startMarker)
	endIdx := strings.Index(text, endMarker)

	var updated string
	switch {
	case startIdx >= 0 && endIdx > startIdx:
		updated = text[:startIdx] + block + text[endIdx+len(endMarker):]
	case text == "":
		updated = block + "\n"
	default:
		updated = strings.TrimRight(text, "\n") + "\n\n" + block + "\n"
	}

	if dir := filepath.Dir(filePath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(filePath, []byte(updated), 0o644)
}
