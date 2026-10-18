package discord

import (
	"fmt"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// maomao fork: aux_channels = { "<main channel ID>" = "<log channel ID>" }.
//
// 用户 2026-10-19 点头的原话：「把噪音搬出频道。工具卡片、额度、定时器进一个日志子频道；
// 主频道只留判断、结果、决定。」For a main channel listed here, the machine chatter
// goes to its log channel:
//   - the progress card and the sub-agent table (SendPreviewStart with a
//     progress-card / sub-task-table payload, then the edits on that message),
//   - cc-connect's own notices the engine marks with core.SystemNoticeMarker
//     (♻️ new session, ⏰ cron/timer start, ⏳ capacity cooldown).
// The agent's replies, the streamed text preview, cron/timer results, errors
// and command replies stay in the main channel. Channels not in the table are
// untouched.

// parseAuxChannels reads the aux_channels option (an inline TOML table; the
// decoder hands it over as map[string]any).
func parseAuxChannels(v any) (map[string]string, error) {
	if v == nil {
		return nil, nil
	}
	out := map[string]string{}
	switch m := v.(type) {
	case map[string]string:
		for k, val := range m {
			out[strings.TrimSpace(k)] = strings.TrimSpace(val)
		}
	case map[string]any:
		for k, val := range m {
			s, ok := val.(string)
			if !ok {
				return nil, fmt.Errorf("discord: aux_channels[%q] must be a channel ID string, got %T", k, val)
			}
			out[strings.TrimSpace(k)] = strings.TrimSpace(s)
		}
	default:
		return nil, fmt.Errorf("discord: aux_channels must be a table of main channel ID → log channel ID, got %T", v)
	}
	for k, val := range out {
		if k == "" || val == "" {
			return nil, fmt.Errorf("discord: aux_channels has an empty channel ID (%q = %q)", k, val)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// auxChannelFor returns the log channel for channelID, if it has one.
func (p *Platform) auxChannelFor(channelID string) (string, bool) {
	if p == nil || len(p.auxChannels) == 0 {
		return "", false
	}
	aux, ok := p.auxChannels[channelID]
	return aux, ok && aux != ""
}

// isDiscordLogPayload reports whether a preview message is the progress card
// or the sub-agent table (as opposed to a streamed text preview of the reply).
func isDiscordLogPayload(content string) bool {
	return strings.HasPrefix(content, core.ProgressCardPayloadPrefix) ||
		strings.HasPrefix(content, core.SubTaskTablePayloadPrefix)
}

// previewChannelFor picks the channel a preview message is posted to: the log
// channel for a progress card / sub-agent table when the target channel has
// one, otherwise the target channel itself.
func (p *Platform) previewChannelFor(channelID, content string) string {
	if isDiscordLogPayload(content) {
		if aux, ok := p.auxChannelFor(channelID); ok {
			return aux
		}
	}
	return channelID
}

// redirectNotice moves a cc-connect notice (MarkSystemNotice) to the log
// channel of its target channel. The notice is posted as a plain message there
// (no reply reference: the message it would answer is in the main channel).
func (p *Platform) redirectNotice(rc replyContext) replyContext {
	if !rc.notice {
		return rc
	}
	aux, ok := p.auxChannelFor(rc.targetChannelID())
	if !ok {
		return rc
	}
	return replyContext{channelID: aux, notice: true}
}

// MarkSystemNotice implements core.SystemNoticeMarker.
func (p *Platform) MarkSystemNotice(replyCtx any) any {
	if rc, ok := replyCtx.(replyContext); ok {
		rc.notice = true
		return rc
	}
	return replyCtx
}

var _ core.SystemNoticeMarker = (*Platform)(nil)
