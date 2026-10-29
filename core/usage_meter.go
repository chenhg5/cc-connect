package core

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// TokenUsage uses disjoint billing buckets. Reasoning is a share of output.
type TokenUsage struct {
	InputTokens              int `json:"input"`
	OutputTokens             int `json:"output"`
	CacheReadInputTokens     int `json:"input_cache_read_tokens"`
	CacheCreationInputTokens int `json:"input_cache_creation_tokens"`
	ReasoningOutputTokens    int `json:"output_reasoning_tokens"`
}

func (u TokenUsage) Empty() bool {
	return u.InputTokens+u.OutputTokens+u.CacheReadInputTokens+u.CacheCreationInputTokens == 0
}

func (u *TokenUsage) Add(v TokenUsage) {
	u.InputTokens += v.InputTokens
	u.OutputTokens += v.OutputTokens
	u.CacheReadInputTokens += v.CacheReadInputTokens
	u.CacheCreationInputTokens += v.CacheCreationInputTokens
	u.ReasoningOutputTokens += v.ReasoningOutputTokens
}

func (u TokenUsage) Delta(previous TokenUsage) TokenUsage {
	return TokenUsage{
		InputTokens:              max(u.InputTokens-previous.InputTokens, 0),
		OutputTokens:             max(u.OutputTokens-previous.OutputTokens, 0),
		CacheReadInputTokens:     max(u.CacheReadInputTokens-previous.CacheReadInputTokens, 0),
		CacheCreationInputTokens: max(u.CacheCreationInputTokens-previous.CacheCreationInputTokens, 0),
		ReasoningOutputTokens:    max(u.ReasoningOutputTokens-previous.ReasoningOutputTokens, 0),
	}
}

func (u TokenUsage) Apply(event *Event) {
	event.InputTokens = u.InputTokens
	event.OutputTokens = u.OutputTokens
	event.CacheReadInputTokens = u.CacheReadInputTokens
	event.CacheCreationInputTokens = u.CacheCreationInputTokens
	event.ReasoningOutputTokens = u.ReasoningOutputTokens
}

// UsageMeter keeps a high-water mark per request or cumulative counter, so
// duplicate/replayed notifications and resumed processes do not bill twice.
// An empty path keeps the meter in memory (for ephemeral sessions).
type UsageMeter struct {
	mu       sync.Mutex
	path     string
	reported map[string]TokenUsage
	sources  map[string]string
}

type usageMeterState struct {
	Reported map[string]TokenUsage `json:"reported"`
	Sources  map[string]string     `json:"sources,omitempty"`
}

func NewUsageMeter(path string) (*UsageMeter, bool) {
	m := &UsageMeter{path: path, reported: map[string]TokenUsage{}, sources: map[string]string{}}
	if path == "" {
		return m, false
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return m, false
	}
	if err == nil {
		var state usageMeterState
		err = json.Unmarshal(data, &state)
		if err == nil {
			m.reported, m.sources = state.Reported, state.Sources
		}
	}
	if err != nil {
		slog.Warn("usage meter state unavailable", "path", path, "error", err)
		return m, false
	}
	if m.reported == nil {
		m.reported = map[string]TokenUsage{}
	}
	if m.sources == nil {
		m.sources = map[string]string{}
	}
	return m, true
}

// TrackSource remembers a native child transcript across process restarts.
func (m *UsageMeter) TrackSource(id, path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sources[id] = path
}

func (m *UsageMeter) Sources() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := make(map[string]string, len(m.sources))
	for id, path := range m.sources {
		copy[id] = path
	}
	return copy
}

// Observe reports only growth. Persist after collecting a complete batch.
func (m *UsageMeter) Observe(key string, total TokenUsage) TokenUsage {
	m.mu.Lock()
	defer m.mu.Unlock()
	previous := m.reported[key]
	delta := total.Delta(previous)
	previous.Add(delta)
	m.reported[key] = previous
	return delta
}

func (m *UsageMeter) Save() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.path == "" {
		return
	}
	data, err := json.Marshal(usageMeterState{Reported: m.reported, Sources: m.sources})
	if err == nil {
		err = os.MkdirAll(filepath.Dir(m.path), 0o700)
	}
	if err == nil {
		var file *os.File
		file, err = os.CreateTemp(filepath.Dir(m.path), ".usage-*")
		if err == nil {
			defer func() { _ = os.Remove(file.Name()) }()
			_, err = file.Write(data)
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil {
				err = os.Rename(file.Name(), m.path)
			}
		}
	}
	if err != nil {
		slog.Warn("could not persist usage meter", "path", m.path, "error", err)
	}
}

// BillableUsage includes the separate child counters. Event token fields keep
// describing the parent so a child cannot inflate the parent's context size.
func (event Event) BillableUsage() TokenUsage {
	usage := TokenUsage{InputTokens: event.InputTokens, OutputTokens: event.OutputTokens,
		CacheReadInputTokens: event.CacheReadInputTokens, CacheCreationInputTokens: event.CacheCreationInputTokens,
		ReasoningOutputTokens: event.ReasoningOutputTokens}
	if child := event.Sidechain; child != nil {
		usage.Add(TokenUsage{InputTokens: child.InputTokens, OutputTokens: child.OutputTokens,
			CacheReadInputTokens: child.CacheReadInputTokens, CacheCreationInputTokens: child.CacheCreationInputTokens,
			ReasoningOutputTokens: child.ReasoningOutputTokens})
	}
	return usage
}
