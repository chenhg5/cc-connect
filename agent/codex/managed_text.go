package codex

import (
	"encoding/json"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

type managedTextStream struct {
	turnID, phase, delivery string
	text                    strings.Builder
}

func (s *managedSession) startAgentText(turnID, itemID string, item map[string]any) {
	if turnID == "" || itemID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seenItems[itemID] || s.completed[turnID] {
		return
	}
	if s.textStreams == nil {
		s.textStreams = map[string]*managedTextStream{}
	}
	key := turnID + "/" + itemID
	stream := s.textStreams[key]
	if stream == nil {
		stream = &managedTextStream{turnID: turnID}
		s.textStreams[key] = stream
	}
	stream.phase, _ = item["phase"].(string)
	stream.delivery, _ = item["delivery"].(string)
}

func (s *managedSession) handleAgentTextDelta(raw json.RawMessage) error {
	var p struct {
		TurnID string `json:"turnId"`
		ItemID string `json:"itemId"`
		Delta  string `json:"delta"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if p.TurnID == "" || p.ItemID == "" || p.Delta == "" {
		return nil
	}
	s.mu.Lock()
	if s.seenItems[p.ItemID] || s.completed[p.TurnID] {
		s.mu.Unlock()
		return nil
	}
	if s.textStreams == nil {
		s.textStreams = map[string]*managedTextStream{}
	}
	key := p.TurnID + "/" + p.ItemID
	stream := s.textStreams[key]
	if stream == nil {
		stream = &managedTextStream{turnID: p.TurnID}
		s.textStreams[key] = stream
	}
	stream.text.WriteString(p.Delta)
	phase, delivery := stream.phase, stream.delivery
	s.mu.Unlock()
	s.emit(core.Event{Type: core.EventText, TurnID: p.TurnID, ItemID: p.ItemID, Content: p.Delta, Metadata: map[string]any{"phase": phase, "delivery": delivery, "text_delta": true}})
	return nil
}

// Completion is authoritative. Emit only the missing suffix, or replace the
// item's preview when attach missed a prefix or the service corrected its text.
func (s *managedSession) finishAgentText(turnID, itemID, full string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := turnID + "/" + itemID
	stream := s.textStreams[key]
	delete(s.textStreams, key)
	if stream == nil {
		return full, false
	}
	preview := stream.text.String()
	if strings.HasPrefix(full, preview) {
		return strings.TrimPrefix(full, preview), false
	}
	return full, true
}
