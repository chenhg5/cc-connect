package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/chenhg5/cc-connect/core"
	"io"
	"os"
	"strings"
)

// applyCodexTurnUsage fills evt's token fields with one turn's usage: the
// difference of the thread's cumulative totals at turn end and turn start (a
// turn makes many model requests; Codex's "last" usage covers only one). An
// unknown endpoint leaves the fields zero rather than guessing, and deltas
// are clamped so a counter that moved backwards never yields negative usage.
//
// Codex's input_tokens include cached_input_tokens, while EventResult follows
// the Anthropic split (InputTokens = fresh input, CacheReadInputTokens = cache
// reads); output_tokens already include reasoning_output_tokens.
func applyCodexTurnUsage(evt *core.Event, start, end *codexTokenUsage) {
	if evt == nil || start == nil || end == nil {
		return
	}
	input := max(end.InputTokens-start.InputTokens, 0)
	cached := min(max(end.CachedInputTokens-start.CachedInputTokens, 0), input)
	evt.InputTokens = input - cached
	evt.CacheReadInputTokens = cached
	evt.OutputTokens = max(end.OutputTokens-start.OutputTokens, 0)
	evt.ReasoningOutputTokens = max(end.ReasoningOutputTokens-start.ReasoningOutputTokens, 0)
}

// laterCodexTokenTotal returns whichever of two snapshots of the same
// cumulative thread counter is further along; nil means unknown.
func laterCodexTokenTotal(a, b *codexTokenUsage) *codexTokenUsage {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if b.InputTokens+b.OutputTokens > a.InputTokens+a.OutputTokens {
		return b
	}
	return a
}

// codexTurnTokens tracks the thread's cumulative token totals across the
// app-server notification stream so each turn can report its own usage.
type codexTurnTokens struct {
	total  *codexTokenUsage // latest cumulative thread total; nil until known
	active bool             // a turn is being accounted
	start  *codexTokenUsage // thread total when the turn began; nil until known
	end    *codexTokenUsage // latest thread total seen during the turn
}

// newThread records that the thread was just created, so its totals are zero.
func (t *codexTurnTokens) newThread() {
	t.total = &codexTokenUsage{}
}

// beginTurn opens a turn with the latest known thread total as its baseline.
func (t *codexTurnTokens) beginTurn() {
	t.active = true
	t.start = cloneCodexTokenUsage(t.total)
	t.end = nil
}

// observe records one thread/tokenUsage/updated notification.
func (t *codexTurnTokens) observe(total, last codexTokenUsage) {
	if !t.active {
		t.beginTurn()
	}
	if t.start == nil {
		// A resumed thread carries totals from before this session. The
		// first update of the turn is total = earlier totals + this request.
		t.start = &codexTokenUsage{
			InputTokens:           max(total.InputTokens-last.InputTokens, 0),
			CachedInputTokens:     max(total.CachedInputTokens-last.CachedInputTokens, 0),
			OutputTokens:          max(total.OutputTokens-last.OutputTokens, 0),
			ReasoningOutputTokens: max(total.ReasoningOutputTokens-last.ReasoningOutputTokens, 0),
			TotalTokens:           max(total.TotalTokens-last.TotalTokens, 0),
		}
	}
	t.total = &total
	t.end = cloneCodexTokenUsage(&total)
}

// finishTurn closes the turn and returns its baseline and end totals.
func (t *codexTurnTokens) finishTurn() (start, end *codexTokenUsage) {
	start, end = t.start, t.end
	t.active, t.start, t.end = false, nil, nil
	return start, end
}

func cloneCodexTokenUsage(usage *codexTokenUsage) *codexTokenUsage {
	if usage == nil {
		return nil
	}
	cloned := *usage
	return &cloned
}

// codexRolloutTokenInfo is the info of a rollout token_count event: the
// thread's cumulative usage and the last model request's usage.
type codexRolloutTokenInfo struct {
	TotalTokenUsage    codexSnakeTokenUsage `json:"total_token_usage"`
	LastTokenUsage     codexSnakeTokenUsage `json:"last_token_usage"`
	ModelContextWindow int                  `json:"model_context_window"`
}

func (info *codexRolloutTokenInfo) contextUsage() *core.ContextUsage {
	if info == nil {
		return nil
	}
	return contextUsageFromSnake(info.LastTokenUsage, info.ModelContextWindow)
}

func (info *codexRolloutTokenInfo) total() *codexTokenUsage {
	if info == nil {
		return nil
	}
	total := codexTokenUsage(info.TotalTokenUsage)
	return &total
}

// loadRolloutTokenInfo returns the last token_count of the thread's rollout.
// A rollout that exists but has no token_count yet yields (nil, path, nil);
// an error means the rollout could not be found or read.
func loadRolloutTokenInfo(extraEnv []string, sessionID, cachedPath string) (*codexRolloutTokenInfo, string, error) {
	path := strings.TrimSpace(cachedPath)
	if path != "" {
		info, err := readTokenInfoFromRollout(path)
		if err == nil && info != nil {
			return info, path, nil
		}
	}

	codexHome, err := resolveCodexHome(extraEnv)
	if err != nil {
		return nil, "", err
	}
	path = findSessionFileInCodexHome(codexHome, sessionID)
	if path == "" {
		return nil, "", fmt.Errorf("session file not found for %s", sessionID)
	}
	info, err := readTokenInfoFromRollout(path)
	if err != nil {
		return nil, path, err
	}
	return info, path, nil
}

func readTokenInfoFromRollout(path string) (*codexRolloutTokenInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if info, err := readTokenInfoFromRolloutTail(f); err != nil {
		return nil, err
	} else if info != nil {
		return info, nil
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return scanTokenInfoFromRollout(f)
}

func readTokenInfoFromRolloutTail(f *os.File) (*codexRolloutTokenInfo, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() <= 0 {
		return nil, nil
	}

	start := int64(0)
	if info.Size() > codexRolloutTailBytes {
		start = info.Size() - codexRolloutTailBytes
	}
	buf := make([]byte, int(info.Size()-start))
	n, err := f.ReadAt(buf, start)
	if err != nil && err != io.EOF {
		return nil, err
	}
	buf = buf[:n]
	if start > 0 {
		if idx := bytes.IndexByte(buf, '\n'); idx >= 0 {
			buf = buf[idx+1:]
		}
	}
	return parseTokenInfoFromRolloutBytes(buf), nil
}

func parseTokenInfoFromRolloutBytes(data []byte) *codexRolloutTokenInfo {
	lines := bytes.Split(data, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		if info := parseTokenInfoFromRolloutLine(line); info != nil {
			return info
		}
	}
	return nil
}

func scanTokenInfoFromRollout(r io.Reader) (*codexRolloutTokenInfo, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)

	var last *codexRolloutTokenInfo
	for scanner.Scan() {
		if info := parseTokenInfoFromRolloutLine(scanner.Bytes()); info != nil {
			last = info
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return last, nil
}

func parseTokenInfoFromRolloutLine(line []byte) *codexRolloutTokenInfo {
	var entry struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(line, &entry); err != nil {
		return nil
	}
	if entry.Type != "event_msg" {
		return nil
	}

	var payload struct {
		Type string                 `json:"type"`
		Info *codexRolloutTokenInfo `json:"info"`
	}
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return nil
	}
	if payload.Type != "token_count" {
		return nil
	}
	return payload.Info
}

func (cs *codexSession) beginBillingUsage() {
	sid := cs.CurrentSessionID()
	cs.turnTokenStart = nil
	if sid == "" {
		cs.turnTokenStart = &codexTokenUsage{}
		return
	}
	root, err := resolveCodexHome(cs.extraEnv)
	if err != nil {
		return
	}
	cs.sidechain.begin(root, sid, true)
	if info, _, err := loadRolloutTokenInfo(cs.extraEnv, sid, ""); err == nil {
		if info == nil {
			cs.turnTokenStart = &codexTokenUsage{}
		} else {
			cs.turnTokenStart = info.total()
		}
	}
}
func (cs *codexSession) finishBillingUsage(event *core.Event, raw map[string]any) {
	info, _, _ := loadRolloutTokenInfo(cs.extraEnv, cs.CurrentSessionID(), "")
	end := info.total()
	if data, err := json.Marshal(raw["usage"]); err == nil {
		var parsed codexSnakeTokenUsage
		if json.Unmarshal(data, &parsed) == nil && (parsed.InputTokens > 0 || parsed.OutputTokens > 0) {
			fallback := codexTokenUsage(parsed)
			end = laterCodexTokenTotal(end, &fallback)
		}
	}
	applyCodexTurnUsage(event, cs.turnTokenStart, end)
	cs.turnTokenStart = nil
	event.Sidechain = cs.sidechain.collect()
}
