package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/chenhg5/cc-connect/core"
)

// Each descendant has its own cumulative counter. Never mix those counters
// with the parent's context or subtract one thread's total from another's.
type codexSidechainMeter struct {
	mu           sync.Mutex
	meter        *core.UsageMeter
	root, parent string
	totals       map[string]codexTokenUsage
	models       map[string]string
}

func (m *codexSidechainMeter) begin(root, parent string, baseline bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.meter != nil {
		return
	}
	m.root, m.parent = root, parent
	state := ""
	if root != "" && parent != "" {
		state = filepath.Join(root, "cc-connect-usage", parent+".json")
	}
	var restored bool
	m.meter, restored = core.NewUsageMeter(state)
	if baseline && !restored {
		// Older sessions have already billed their existing descendants.
		for id, usage := range m.snapshots() {
			m.meter.Observe(id, codexBillingUsage(usage))
		}
		m.meter.Save()
	}
}

func codexBillingUsage(usage codexTokenUsage) core.TokenUsage {
	event := core.Event{}
	applyCodexTurnUsage(&event, &codexTokenUsage{}, &usage)
	return core.TokenUsage{InputTokens: event.InputTokens, OutputTokens: event.OutputTokens,
		CacheReadInputTokens: event.CacheReadInputTokens, ReasoningOutputTokens: event.ReasoningOutputTokens}
}

func (m *codexSidechainMeter) observe(id string, usage codexTokenUsage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.totals == nil {
		m.totals = map[string]codexTokenUsage{}
	}
	previous := m.totals[id]
	m.totals[id] = *laterCodexTokenTotal(&previous, &usage)
}

func (m *codexSidechainMeter) collect() *core.SidechainUsage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.meter == nil {
		m.meter, _ = core.NewUsageMeter("")
	}
	var total core.TokenUsage
	byModel := map[string]core.TokenUsage{}
	for id, snapshot := range m.snapshots() {
		delta := m.meter.Observe(id, codexBillingUsage(snapshot))
		total.Add(delta)
		usage := byModel[m.models[id]]
		usage.Add(delta)
		byModel[m.models[id]] = usage
	}
	m.meter.Save()
	if total.Empty() {
		return nil
	}
	return &core.SidechainUsage{InputTokens: total.InputTokens, OutputTokens: total.OutputTokens,
		CacheReadInputTokens: total.CacheReadInputTokens, ReasoningOutputTokens: total.ReasoningOutputTokens, ByModel: byModel}
}

type codexRolloutIdentity struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_thread_id"`
	Source   struct {
		Subagent struct {
			ThreadSpawn struct {
				ParentID string `json:"parent_thread_id"`
			} `json:"thread_spawn"`
		} `json:"subagent"`
	} `json:"source"`
}

// Read only the first metadata line while discovering the family; unrelated
// sessions' histories and usage never participate in the snapshot.
func (m *codexSidechainMeter) snapshots() map[string]codexTokenUsage {
	result := map[string]codexTokenUsage{}
	for id, usage := range m.totals {
		result[id] = usage
	}
	if m.root == "" || m.parent == "" {
		return result
	}
	type rollout struct{ id, parent, path string }
	var files []rollout
	_ = filepath.WalkDir(filepath.Join(m.root, "sessions"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer func() { _ = file.Close() }()
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), 2*1024*1024)
		if !scanner.Scan() {
			return nil
		}
		var header struct {
			Type    string
			Payload codexRolloutIdentity
		}
		if json.Unmarshal(scanner.Bytes(), &header) != nil || header.Type != "session_meta" {
			return nil
		}
		parent := header.Payload.ParentID
		if parent == "" {
			parent = header.Payload.Source.Subagent.ThreadSpawn.ParentID
		}
		if parent != "" {
			files = append(files, rollout{header.Payload.ID, parent, path})
		}
		return nil
	})
	family := map[string]bool{m.parent: true}
	for changed := true; changed; {
		changed = false
		for _, file := range files {
			if !family[file.id] && family[file.parent] {
				family[file.id] = true
				changed = true
			}
		}
	}
	for _, file := range files {
		if file.id == m.parent || !family[file.id] {
			continue
		}
		if info, err := readTokenInfoFromRollout(file.path); err == nil && info != nil {
			if m.models == nil {
				m.models = map[string]string{}
			}
			m.models[file.id] = rolloutModel(file.path)
			usage := codexTokenUsage(info.TotalTokenUsage)
			previous := result[file.id]
			result[file.id] = *laterCodexTokenTotal(&previous, &usage)
		}
	}
	return result
}

func rolloutModel(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	start := max(info.Size()-codexRolloutTailBytes, 0)
	data := make([]byte, int(info.Size()-start))
	n, err := file.ReadAt(data, start)
	if err != nil && err != io.EOF {
		return ""
	}
	data = data[:n]
	if start > 0 {
		if end := bytes.IndexByte(data, '\n'); end >= 0 {
			data = data[end+1:]
		} else {
			return ""
		}
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), int(codexRolloutTailBytes))
	model := ""
	for scanner.Scan() {
		var line struct {
			Type    string
			Payload struct{ Model string }
		}
		if json.Unmarshal(scanner.Bytes(), &line) == nil && line.Type == "turn_context" && line.Payload.Model != "" {
			model = line.Payload.Model
		}
	}
	return model
}
