package core

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// maomao fork: a sub bot's own wiki index on every fresh agent context (user 2026-10-20 「让他们和你一样的待遇，
// 不要区分」). 橘 (Claude Code) gets ~/wiki/MEMORY.md from Claude Code's auto-memory at every session start; Codex and
// Antigravity have no such loader, so cc-connect puts the bot's MEMORY.md (project option memory_index) before the
// first turn of a context started without resume (/new, cold-cache renewal, failed resume, first start).

const memoryIndexHead = "（你的记忆 wiki 目录页"

// SetMemoryIndex sets the MEMORY.md given to fresh contexts ("" = off, the default).
func (e *Engine) SetMemoryIndex(path string) {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	e.memoryIndex = path
}

// memoryBlock renders the MEMORY.md block for the first turn of a fresh context ("" otherwise).
func (e *Engine) memoryBlock(session *Session) string {
	if e.memoryIndex == "" || session == nil || !session.TakeMemoryIndex() {
		return ""
	}
	data, err := os.ReadFile(e.memoryIndex)
	if err != nil {
		slog.Warn("memory index: read failed", "path", e.memoryIndex, "error", err)
		return ""
	}
	body := strings.TrimSpace(string(data))
	if body == "" {
		return ""
	}
	shown := e.memoryIndex
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(shown, home+"/") {
		shown = "~" + shown[len(home):]
	}
	// MEMORY.md holds only entry lines (wiki lint), so the block is one paragraph: stripStaleBlocks drops it whole.
	return fmt.Sprintf("%s %s，新会话系统自动附上，不用回应）\n%s", memoryIndexHead, shown, body)
}

// TakeMemoryIndex returns and clears the "give this context its MEMORY.md" mark; SettleHandoff puts it back when
// the turn carrying it was not sent.
func (s *Session) TakeMemoryIndex() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.memCtx
	s.memCtx = false
	if m {
		s.memTaken = true
	}
	return m
}
