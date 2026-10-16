package acp

import (
	"encoding/json"

	"github.com/chenhg5/cc-connect/core"
)

// GetContextUsage returns the latest usage_update / prompt usage snapshot.
// Nil until at least one of those has been observed.
func (s *acpSession) GetContextUsage() *core.ContextUsage {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if s.usage == nil {
		return nil
	}
	clone := *s.usage
	return &clone
}

// GetUsageSource reports that ACP usage comes from protocol events
// (usage_update notifications and session/prompt result.usage), never
// from an on-disk transcript.
func (s *acpSession) GetUsageSource() string { return "event" }

func (s *acpSession) maybeAbsorbUsageUpdate(params json.RawMessage) {
	kind, update, _ := peekSessionUpdate(params)
	if kind != "usage_update" {
		return
	}
	s.absorbUsageUpdate(update)
}

func (s *acpSession) absorbUsageUpdate(update json.RawMessage) {
	var body struct {
		Used *int           `json:"used"`
		Size *int           `json:"size"`
		Meta map[string]any `json:"_meta"`
	}
	if json.Unmarshal(update, &body) != nil {
		return
	}
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if s.usage == nil {
		s.usage = &core.ContextUsage{}
	}
	if body.Used != nil {
		s.usage.UsedTokens = *body.Used
	}
	if body.Size != nil {
		s.usage.ContextWindow = *body.Size
	}
	if n, ok := metaInt(body.Meta, "cognition.ai/inputTokens"); ok {
		s.usage.InputTokens = n
	}
	if n, ok := metaInt(body.Meta, "cognition.ai/outputTokens"); ok {
		s.usage.OutputTokens = n
	}
	if n, ok := metaInt(body.Meta, "cognition.ai/cachedReadTokens"); ok {
		s.usage.CachedInputTokens = n
	}
}

// absorbPromptResult merges session/prompt result.usage into the snapshot
// and returns the token counts to put on EventResult. Fields the result
// does not carry (used, context window) are left as usage_update set them.
func (s *acpSession) absorbPromptResult(raw json.RawMessage) (input, output, cacheRead int) {
	var wrap struct {
		Usage *promptUsage `json:"usage"`
	}
	if json.Unmarshal(raw, &wrap) != nil || wrap.Usage == nil {
		if cur := s.GetContextUsage(); cur != nil {
			return cur.InputTokens, cur.OutputTokens, cur.CachedInputTokens
		}
		return 0, 0, 0
	}
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if s.usage == nil {
		s.usage = &core.ContextUsage{}
	}
	u := wrap.Usage
	setInt(&s.usage.TotalTokens, u.TotalTokens)
	setInt(&s.usage.InputTokens, u.InputTokens)
	setInt(&s.usage.OutputTokens, u.OutputTokens)
	setInt(&s.usage.CachedInputTokens, u.CachedReadTokens)
	setInt(&s.usage.CacheCreationInputTokens, u.CachedWriteTokens)
	setInt(&s.usage.ReasoningOutputTokens, u.ThoughtTokens)
	// Prompt-only agents never send usage_update.used. totalTokens is the
	// end-of-turn load those agents do report, so fill UsedTokens from it
	// when nothing else has.
	if s.usage.UsedTokens == 0 && u.TotalTokens != nil {
		s.usage.UsedTokens = *u.TotalTokens
	}
	return s.usage.InputTokens, s.usage.OutputTokens, s.usage.CachedInputTokens
}

type promptUsage struct {
	TotalTokens       *int `json:"totalTokens"`
	InputTokens       *int `json:"inputTokens"`
	OutputTokens      *int `json:"outputTokens"`
	CachedReadTokens  *int `json:"cachedReadTokens"`
	CachedWriteTokens *int `json:"cachedWriteTokens"`
	ThoughtTokens     *int `json:"thoughtTokens"`
}

func setInt(dst *int, src *int) {
	if src != nil {
		*dst = *src
	}
}

func metaInt(m map[string]any, key string) (int, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}
