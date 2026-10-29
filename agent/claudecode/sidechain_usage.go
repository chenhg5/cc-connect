package claudecode

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/chenhg5/cc-connect/core"
)

// Subagent ("sidechain") usage metering.
//
// Claude Code bills a subagent -- an Agent/Task tool call, or one of the
// agents a dynamic workflow fans out -- to the session, but reports it on
// neither of the channels a turn's usage is taken from: stream-json carries no
// assistant event for it, and the `result` event's `usage` covers the main
// loop only. Measured on claude 2.1.280 with one Agent tool call: result.usage
// was in=18 out=219 cache_write=13901 cache_read=40872, while the session's
// modelUsage (which does include the subagent) was 28/266/31497/40872; the
// difference was exactly the subagent's own transcript. A workflow that fans
// out a hundred agents was therefore metered as the one main-loop call that
// launched it.
//
// Each subagent writes its own transcript next to the session's:
//
//	<project>/<sid>/subagents/agent-<id>.jsonl                  (Agent tool)
//	<project>/<sid>/subagents/workflows/<run>/agent-<id>.jsonl  (workflows)
//
// where every API call is an assistant line with `isSidechain: true`, a
// `requestId` and the call's usage. sidechainMeter reads what those files
// gained since the previous turn and reports it once.
//
// A call is reported once per requestId (message id when there is none).
// Claude Code writes one line per content block of a response. Against the
// Anthropic API every line carries the response's final usage and its
// stop_reason; behind an Anthropic-compatible gateway (measured: qwen3.8-max
// via ai-gateway, which also drops requestId) the lines before the last are
// provisional -- stop_reason null, input_tokens the whole prompt, output 0,
// no cache split -- and only the line that carries stop_reason has the real
// figures. Only lines with a stop_reason are counted, and a request whose
// final line grows after it was reported contributes only the growth.
//
// A background workflow can still be running when the turn that launched it
// ends; its agents' calls are picked up by the next turn instead (the
// workflow's completion notification starts one), so they still land in the
// same session.

// sidechainMeter tracks how far each subagent transcript of one session has
// been reported. Safe for concurrent use.
//
// The state is saved under cc-connect's data directory after every report
// that changes it. A session resumed by a new claude process (idle reaping,
// a cc-connect restart) picks up where the previous one stopped; without the
// file, whatever the subagents wrote after the previous process's last
// report -- a background workflow that outlived it -- could only be skipped,
// never billed.
type sidechainMeter struct {
	mu       sync.Mutex
	offsets  map[string]int64            // transcript path -> bytes consumed
	reported map[string]sidechainRequest // requestId -> usage already reported
	// statePath is where the state is saved; empty: kept in memory only.
	statePath string
}

// sidechainState is the saved form of a meter.
type sidechainState struct {
	Offsets  map[string]int64    `json:"offsets"`
	Reported map[string][4]int64 `json:"reported"` // input, output, cache write, cache read
}

// sidechainStatePath is where the meter of session sessionID is saved, or ""
// when either is unknown.
func sidechainStatePath(dataDir, sessionID string) string {
	if dataDir == "" || sessionID == "" || sessionID == core.ContinueSession {
		return ""
	}
	return filepath.Join(dataDir, "sidechain-meter", sessionID+".json")
}

// persistTo saves the meter at path from now on, first folding in what was
// saved there before; whether a saved state was found.
func (m *sidechainMeter) persistTo(path string) bool {
	if m == nil || path == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if path == m.statePath {
		return true
	}
	m.statePath = path
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var state sidechainState
	if err := json.Unmarshal(data, &state); err != nil {
		slog.Warn("claudeSession: saved subagent meter unreadable; starting over", "path", path, "error", err)
		return false
	}
	for file, offset := range state.Offsets {
		m.offsets[file] = max(m.offsets[file], offset)
	}
	for id, usage := range state.Reported {
		if _, ok := m.reported[id]; !ok {
			m.reported[id] = sidechainRequest{
				input: int(usage[0]), output: int(usage[1]), cacheCreate: int(usage[2]), cacheRead: int(usage[3]),
			}
		}
	}
	return true
}

func (m *sidechainMeter) saveLocked() {
	if m.statePath == "" {
		return
	}
	state := sidechainState{Offsets: m.offsets, Reported: make(map[string][4]int64, len(m.reported))}
	for id, r := range m.reported {
		state.Reported[id] = [4]int64{int64(r.input), int64(r.output), int64(r.cacheCreate), int64(r.cacheRead)}
	}
	data, err := json.Marshal(state)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(m.statePath), 0o700)
	}
	if err == nil {
		// Through a rename, so a crash mid-write leaves the previous state.
		tmp := m.statePath + ".tmp"
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			err = os.Rename(tmp, m.statePath)
		}
	}
	if err != nil {
		slog.Warn("claudeSession: could not save the subagent meter", "path", m.statePath, "error", err)
	}
}

type sidechainRequest struct {
	model                                 string
	input, output, cacheCreate, cacheRead int
}

func newSidechainMeter() *sidechainMeter {
	return &sidechainMeter{offsets: map[string]int64{}, reported: map[string]sidechainRequest{}}
}

// subagentsDir is where Claude Code keeps the subagent transcripts of the
// session whose main transcript is transcript.
func subagentsDir(transcript string) string {
	if transcript == "" {
		return ""
	}
	return filepath.Join(strings.TrimSuffix(transcript, ".jsonl"), "subagents")
}

// baseline marks everything already on disk under dir as reported. For a
// resumed session with no saved meter -- one started before the meter was
// saved -- the turns that produced those calls were metered by the process
// before it, and billing them again would count them twice.
func (m *sidechainMeter) baseline(dir string) {
	if m == nil || dir == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.collectLocked(dir)
}

// collect returns the subagent usage written under dir since the last call,
// or nil when there is none.
func (m *sidechainMeter) collect(dir string) *core.SidechainUsage {
	if m == nil || dir == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.collectLocked(dir)
}

func (m *sidechainMeter) collectLocked(dir string) *core.SidechainUsage {
	var files []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Missing until the session's first subagent; unreadable entries
			// are skipped rather than failing the turn's metering.
			return nil
		}
		name := d.Name()
		if !d.IsDir() && strings.HasPrefix(name, "agent-") && strings.HasSuffix(name, ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)

	var total core.SidechainUsage
	byModel := map[string]core.TokenUsage{}
	models := map[string]bool{}
	for _, path := range files {
		for _, req := range m.readNewLocked(path) {
			prev, seen := m.reported[req.id]
			delta := sidechainRequest{
				model:       req.model,
				input:       max(req.input-prev.input, 0),
				output:      max(req.output-prev.output, 0),
				cacheCreate: max(req.cacheCreate-prev.cacheCreate, 0),
				cacheRead:   max(req.cacheRead-prev.cacheRead, 0),
			}
			m.reported[req.id] = sidechainRequest{
				model:       req.model,
				input:       max(req.input, prev.input),
				output:      max(req.output, prev.output),
				cacheCreate: max(req.cacheCreate, prev.cacheCreate),
				cacheRead:   max(req.cacheRead, prev.cacheRead),
			}
			if !seen {
				total.Requests++
			}
			total.InputTokens += delta.input
			total.OutputTokens += delta.output
			total.CacheCreationInputTokens += delta.cacheCreate
			total.CacheReadInputTokens += delta.cacheRead
			usage := byModel[req.model]
			usage.Add(core.TokenUsage{InputTokens: delta.input, OutputTokens: delta.output,
				CacheReadInputTokens: delta.cacheRead, CacheCreationInputTokens: delta.cacheCreate})
			byModel[req.model] = usage
			if req.model != "" {
				models[req.model] = true
			}
		}
	}
	if total.Requests == 0 && total.InputTokens+total.OutputTokens+total.CacheCreationInputTokens+total.CacheReadInputTokens == 0 {
		return nil
	}
	m.saveLocked()
	for model := range models {
		total.Models = append(total.Models, model)
	}
	sort.Strings(total.Models)
	total.ByModel = byModel
	return &total
}

type sidechainLine struct {
	id                                    string
	model                                 string
	input, output, cacheCreate, cacheRead int
}

// readNewLocked parses the complete lines appended to path since the last
// read. A trailing partial line is left for the next read: the subagent may
// be in the middle of writing it.
func (m *sidechainMeter) readNewLocked(path string) []sidechainLine {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	offset := m.offsets[path]
	if info, err := f.Stat(); err == nil && info.Size() < offset {
		// Truncated or replaced: start over. Requests already reported stay
		// deduplicated by requestId.
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		slog.Debug("claudeSession: subagent transcript unreadable", "path", path, "error", err)
		return nil
	}
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return nil
	}
	m.offsets[path] = offset + int64(end) + 1

	var out []sidechainLine
	for _, line := range bytes.Split(data[:end], []byte{'\n'}) {
		if parsed, ok := parseSidechainLine(line); ok {
			out = append(out, parsed)
		}
	}
	return out
}

func parseSidechainLine(line []byte) (sidechainLine, bool) {
	if !bytes.Contains(line, []byte(`"usage"`)) {
		return sidechainLine{}, false
	}
	var entry struct {
		Type      string `json:"type"`
		RequestID string `json:"requestId"`
		Message   struct {
			ID         string         `json:"id"`
			Model      string         `json:"model"`
			StopReason *string        `json:"stop_reason"`
			Usage      map[string]any `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &entry); err != nil || entry.Type != "assistant" ||
		entry.Message.Usage == nil || entry.Message.StopReason == nil {
		return sidechainLine{}, false
	}
	id := entry.RequestID
	if id == "" {
		id = entry.Message.ID
	}
	if id == "" {
		return sidechainLine{}, false
	}
	input, output, cacheCreate, cacheRead := parseClaudeUsage(entry.Message.Usage)
	return sidechainLine{
		id:          id,
		model:       entry.Message.Model,
		input:       input,
		output:      output,
		cacheCreate: cacheCreate,
		cacheRead:   cacheRead,
	}, true
}
