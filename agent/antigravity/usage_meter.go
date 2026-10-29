package antigravity

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/chenhg5/cc-connect/core"
)

func (as *antigravitySession) prepareUsageMeter(sessionID string) {
	if as.usageMeter != nil && as.meterSession == sessionID {
		return
	}
	state := ""
	if home, err := os.UserHomeDir(); err == nil && sessionID != "" && sessionID != core.ContinueSession {
		state = filepath.Join(home, ".gemini", "antigravity-cli", "cc-connect-usage", sessionID+".json")
	}
	var restored bool
	as.usageMeter, restored = core.NewUsageMeter(state)
	as.usageBaselineKnown = !as.resuming || restored
	as.meterSession = sessionID
}

func (as *antigravitySession) turnUsageEvent(sessionID string, usage agyUsage) core.Event {
	as.usageMu.Lock()
	defer as.usageMu.Unlock()
	as.prepareUsageMeter(sessionID)
	// Difference the original counters first: cache starts being used on a
	// later turn and must not be subtracted from an earlier turn's input.
	delta := as.usageMeter.Observe(sessionID, core.TokenUsage{InputTokens: usage.InputTokens,
		OutputTokens: usage.OutputTokens, CacheReadInputTokens: usage.CacheReadTokens,
		ReasoningOutputTokens: usage.ThinkingTokens})
	as.usageMeter.Save()
	event := (agyUsage{InputTokens: delta.InputTokens, OutputTokens: delta.OutputTokens,
		CacheReadTokens: delta.CacheReadInputTokens, ThinkingTokens: delta.ReasoningOutputTokens}).resultEvent(sessionID)
	if len(as.turnSteps) > 0 {
		var total core.TokenUsage
		for _, step := range as.turnSteps {
			mapped := step.resultEvent(sessionID)
			total.Add(core.TokenUsage{InputTokens: mapped.InputTokens, OutputTokens: mapped.OutputTokens,
				CacheReadInputTokens: mapped.CacheReadInputTokens, ReasoningOutputTokens: mapped.ReasoningOutputTokens})
		}
		total.Apply(&event)
		as.turnSteps = nil
	} else if !as.usageBaselineKnown {
		// No old state and no per-step reading: the historical part of a
		// resumed aggregate is unknown. Never bill it to this new turn.
		slog.Warn("antigravity: resumed usage baseline missing; cumulative history excluded", "conversation_id", sessionID)
		(core.TokenUsage{}).Apply(&event)
		event.UsageIncomplete = true
	}
	as.usageBaselineKnown = true
	event.Sidechain = as.collectSubagentLogs()
	event.UsageIncomplete = event.UsageIncomplete || as.childUsageIncomplete
	return event
}

func (as *antigravitySession) trackSubagentLogs(sessionID string, info *agySubagentInfo) {
	as.usageMu.Lock()
	defer as.usageMu.Unlock()
	as.prepareUsageMeter(sessionID)
	as.trackSubagents(info)
	as.usageMeter.Save()
}

func (as *antigravitySession) trackSubagents(info *agySubagentInfo) {
	for _, child := range info.Subagents {
		if child.ConversationID == "" || child.ConversationID == as.meterSession {
			continue
		}
		if child.LogURI == "" {
			as.usageMeter.TrackSource(child.ConversationID, "")
			continue
		}
		uri, err := url.Parse(child.LogURI)
		if err != nil || (uri.Scheme != "file" && uri.Scheme != "") || (uri.Host != "" && uri.Host != "localhost") {
			slog.Warn("antigravity: child usage log is not a local file", "conversation_id", child.ConversationID)
			as.usageMeter.TrackSource(child.ConversationID, "")
			continue
		}
		path := uri.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(as.workDir, path)
		}
		as.usageMeter.TrackSource(child.ConversationID, path)
	}
}

// Native transcripts repeat a message as its content grows. Meter the final
// snapshot per message id, never each content-block line. Only transcripts
// explicitly named by this session's subagent_info belong to this family.
func (as *antigravitySession) collectSubagentLogs() *core.SidechainUsage {
	var total core.TokenUsage
	byModel := map[string]core.TokenUsage{}
	visited := map[string]bool{}
	as.childUsageIncomplete = false
	for {
		changed := false
		for id, path := range as.usageMeter.Sources() {
			if visited[id] {
				continue
			}
			visited[id], changed = true, true
			requests, err := as.readUsageTranscript(path)
			if err != nil {
				slog.Warn("antigravity: child usage log unavailable", "conversation_id", id, "error", err)
				as.childUsageIncomplete = true
				continue
			}
			if len(requests) == 0 {
				as.childUsageIncomplete = true
			}
			for request, usage := range requests {
				delta := as.usageMeter.Observe("child:"+id+":"+request, usage.tokens)
				total.Add(delta)
				if !delta.Empty() {
					amount := byModel[usage.model]
					amount.Add(delta)
					byModel[usage.model] = amount
				}
			}
		}
		if !changed {
			break
		}
	}
	as.usageMeter.Save()
	if total.Empty() {
		return nil
	}
	return &core.SidechainUsage{InputTokens: total.InputTokens, OutputTokens: total.OutputTokens,
		CacheReadInputTokens: total.CacheReadInputTokens, CacheCreationInputTokens: total.CacheCreationInputTokens, ReasoningOutputTokens: total.ReasoningOutputTokens, ByModel: byModel}
}

type agyTranscriptUsage struct {
	tokens core.TokenUsage
	model  string
}

func (as *antigravitySession) readUsageTranscript(path string) (map[string]agyTranscriptUsage, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 32*1024*1024)
	requests := map[string]agyTranscriptUsage{}
	steps := map[string]agyTranscriptUsage{}
	for scanner.Scan() {
		var line struct {
			ID, Type, Model string
			Tokens          *struct{ Input, Output, Cached, Thoughts int }
			Message         struct {
				ID, Model  string
				StopReason *string `json:"stop_reason"`
				Usage      *struct {
					Input      int `json:"input_tokens"`
					Output     int `json:"output_tokens"`
					CacheRead  int `json:"cache_read_input_tokens"`
					CacheWrite int `json:"cache_creation_input_tokens"`
				}
			}
			StepUpdate agyStepUpdate `json:"step_update"`
		}
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}
		if line.StepUpdate.SubagentInfo != nil {
			as.trackSubagents(line.StepUpdate.SubagentInfo)
		}
		if line.StepUpdate.Usage != nil && line.StepUpdate.State == "DONE" {
			event := line.StepUpdate.Usage.resultEvent("")
			steps[strconv.Itoa(line.StepUpdate.StepIndex)] = agyTranscriptUsage{tokens: core.TokenUsage{
				InputTokens: event.InputTokens, OutputTokens: event.OutputTokens, CacheReadInputTokens: event.CacheReadInputTokens,
				ReasoningOutputTokens: event.ReasoningOutputTokens}, model: line.Model}
		}
		if line.Type == "gemini" && line.ID != "" && line.Tokens != nil {
			tokens := line.Tokens
			requests[line.ID] = agyTranscriptUsage{tokens: core.TokenUsage{InputTokens: max(tokens.Input-tokens.Cached, 0),
				OutputTokens: tokens.Output + tokens.Thoughts, CacheReadInputTokens: tokens.Cached, ReasoningOutputTokens: tokens.Thoughts}, model: line.Model}
		}
		if line.Type == "assistant" && line.Message.ID != "" && line.Message.StopReason != nil && line.Message.Usage != nil {
			usage := line.Message.Usage
			requests[line.Message.ID] = agyTranscriptUsage{tokens: core.TokenUsage{InputTokens: usage.Input, OutputTokens: usage.Output,
				CacheReadInputTokens: usage.CacheRead, CacheCreationInputTokens: usage.CacheWrite}, model: line.Message.Model}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	// An export can carry both the native records and their stream view.
	// They describe the same requests, so choose one channel per transcript.
	if len(requests) > 0 {
		return requests, nil
	}
	return steps, nil
}
