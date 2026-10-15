package antigravity

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	_ "modernc.org/sqlite"

	"github.com/chenhg5/cc-connect/core"
)

var _ core.SessionHandoffReader = (*Agent)(nil)

// agy (1.3.5) keeps each conversation in
// ~/.gemini/antigravity-cli/conversations/<conversation id>.db. The steps
// table holds one gemini_coder.Step protobuf per row in step_payload. agy
// ships no .proto files; "SPEC" below means the descriptors embedded in the
// agy 1.3.5 binary (third_party/gemini_coder/proto/trajectory.proto,
// third_party/jetski/cortex_pb/cortex.proto,
// third_party/jetski/codeium_common_pb/codeium_common.proto).
const (
	// steps.step_type values (exa.cortex_pb.CortexStepType).
	stepTypeUserInput       = 14 // SPEC: CortexStepType CORTEX_STEP_TYPE_USER_INPUT
	stepTypePlannerResponse = 15 // SPEC: CortexStepType CORTEX_STEP_TYPE_PLANNER_RESPONSE
	stepTypeCheckpoint      = 23 // SPEC: CortexStepType CORTEX_STEP_TYPE_CHECKPOINT

	// gemini_coder.Step
	stepFieldMetadata        = 5  // SPEC: gemini_coder.Step.metadata
	stepFieldUserInput       = 19 // SPEC: gemini_coder.Step.user_input
	stepFieldPlannerResponse = 20 // SPEC: gemini_coder.Step.planner_response
	stepFieldCheckpoint      = 30 // SPEC: gemini_coder.Step.checkpoint

	// exa.cortex_pb.CortexStepMetadata
	metaFieldCreatedAt            = 1 // SPEC: CortexStepMetadata.created_at
	metaFieldFinishedGeneratingAt = 7 // SPEC: CortexStepMetadata.finished_generating_at
	metaFieldCompletedAt          = 8 // SPEC: CortexStepMetadata.completed_at
	metaFieldModelUsage           = 9 // SPEC: CortexStepMetadata.model_usage

	// exa.codeium_common_pb.ModelUsageStats
	usageFieldInputTokens     = 2 // SPEC: ModelUsageStats.input_tokens
	usageFieldCacheReadTokens = 5 // SPEC: ModelUsageStats.cache_read_tokens

	// exa.cortex_pb.CortexStepUserInput
	userFieldQuery        = 1 // SPEC: CortexStepUserInput.query
	userFieldUserResponse = 2 // SPEC: CortexStepUserInput.user_response
	userFieldItems        = 3 // SPEC: CortexStepUserInput.items (TextOrScopeItem)
	itemFieldText         = 1 // SPEC: TextOrScopeItem.text

	// exa.cortex_pb.CortexStepPlannerResponse
	plannerFieldResponse         = 1 // SPEC: CortexStepPlannerResponse.response
	plannerFieldModifiedResponse = 8 // SPEC: CortexStepPlannerResponse.modified_response

	// exa.cortex_pb.CortexStepCheckpoint
	checkpointFieldSessionSummary = 5 // SPEC: CortexStepCheckpoint.session_summary
	checkpointFieldIntentOnly     = 9 // SPEC: CortexStepCheckpoint.intent_only

	// google.protobuf.Timestamp
	tsFieldSeconds = 1 // SPEC: google.protobuf.Timestamp.seconds
	tsFieldNanos   = 2 // SPEC: google.protobuf.Timestamp.nanos
)

// agy names conversations with UUIDs (data: every file in conversations/).
var conversationIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// conversationDBPath returns where agy keeps a conversation. The permission
// bridge's --gemini_dir overlay links ~/.gemini/antigravity-cli back to the
// real directory, so every mode writes here.
func conversationDBPath(conversationID string) (string, error) {
	if !conversationIDPattern.MatchString(conversationID) {
		return "", fmt.Errorf("antigravity: %q is not a conversation id", conversationID)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "conversations", conversationID+".db"), nil
}

// ReadSessionHandoff reads what a fresh context needs from an agy
// conversation database (maomao fork): the last model reply's time and
// prompt size, the newest checkpoint summary and the dialogue text after it.
func (a *Agent) ReadSessionHandoff(sessionID string) (*core.SessionHandoff, error) {
	path, err := conversationDBPath(sessionID)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("antigravity: conversation %s: %w", sessionID, err)
	}
	h, err := readHandoffDB(path)
	if err != nil {
		return nil, fmt.Errorf("antigravity: read conversation %s: %w", sessionID, err)
	}
	h.Path = path
	return h, nil
}

func readHandoffDB(path string) (*core.SessionHandoff, error) {
	// busy_timeout: review request (coordinator, 2026-10-15) suggested 1000 ms.
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(1000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT step_type, step_payload FROM steps WHERE step_type IN (?, ?, ?) ORDER BY idx`,
		stepTypeUserInput, stepTypePlannerResponse, stepTypeCheckpoint)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var p handoffParser
	for rows.Next() {
		var typ int
		var payload []byte
		if err := rows.Scan(&typ, &payload); err != nil {
			return nil, err
		}
		p.add(typ, payload)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &p.h, nil
}

type handoffParser struct {
	h       core.SessionHandoff
	inReply bool // the last Tail entry is the running turn's reply
}

func (p *handoffParser) add(typ int, payload []byte) {
	h := &p.h
	meta := pbBytes(payload, stepFieldMetadata)
	switch typ {
	case stepTypeUserInput:
		text := userInputText(pbBytes(payload, stepFieldUserInput))
		if text == "" {
			return
		}
		p.inReply = false
		h.Tail = append(h.Tail, core.HistoryEntry{Role: "user", Content: text, Timestamp: pbTime(meta, metaFieldCreatedAt)})
	case stepTypePlannerResponse:
		ts := stepEndTime(meta)
		usage := pbBytes(meta, metaFieldModelUsage)
		// input_tokens counts only the uncached part of the prompt; cached
		// tokens are reported apart (data: a conversation's last reply had
		// input 2330 and cache_read 37123). cache_write_tokens never appeared
		// in local samples, so it is not counted.
		if in, cached := pbUint(usage, usageFieldInputTokens), pbUint(usage, usageFieldCacheReadTokens); in+cached > 0 {
			h.LastActivity = ts
			h.ContextTokens = int(in + cached)
		}
		pr := pbBytes(payload, stepFieldPlannerResponse)
		text := strings.TrimSpace(pbString(pr, plannerFieldModifiedResponse))
		if text == "" {
			text = strings.TrimSpace(pbString(pr, plannerFieldResponse))
		}
		if text == "" {
			return // a step that only calls tools
		}
		// cc-connect shows every response text of a turn (text before a tool
		// call too), so the channel saw them joined.
		if n := len(h.Tail); p.inReply && n > 0 {
			h.Tail[n-1].Content += "\n" + text
			h.Tail[n-1].Timestamp = ts
			return
		}
		h.Tail = append(h.Tail, core.HistoryEntry{Role: "assistant", Content: text, Timestamp: ts})
		p.inReply = true
	case stepTypeCheckpoint:
		// Not seen in any local conversation yet (agy 1.3.5); read from the
		// schema: a checkpoint summarizes the steps before it for the model.
		cp := pbBytes(payload, stepFieldCheckpoint)
		summary := strings.TrimSpace(pbString(cp, checkpointFieldSessionSummary))
		if summary == "" || pbUint(cp, checkpointFieldIntentOnly) != 0 {
			return
		}
		h.CompactSummary = summary
		h.CompactedAt = pbTime(meta, metaFieldCreatedAt)
		h.Tail, p.inReply = nil, false
	}
}

func userInputText(ui []byte) string {
	if t := strings.TrimSpace(pbString(ui, userFieldUserResponse)); t != "" {
		return t
	}
	var parts []string
	for _, item := range pbAll(ui, userFieldItems) {
		if t := strings.TrimSpace(pbString(item, itemFieldText)); t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "")
	}
	return strings.TrimSpace(pbString(ui, userFieldQuery))
}

func stepEndTime(meta []byte) time.Time {
	for _, f := range []protowire.Number{metaFieldCompletedAt, metaFieldFinishedGeneratingAt, metaFieldCreatedAt} {
		if t := pbTime(meta, f); !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

// ---- minimal protobuf wire reading ----

// pbAll returns the bytes of every length-delimited field num in msg.
func pbAll(msg []byte, num protowire.Number) [][]byte {
	var out [][]byte
	for len(msg) > 0 {
		n, typ, l := protowire.ConsumeTag(msg)
		if l < 0 {
			return out
		}
		msg = msg[l:]
		if n == num && typ == protowire.BytesType {
			v, m := protowire.ConsumeBytes(msg)
			if m < 0 {
				return out
			}
			out = append(out, v)
			msg = msg[m:]
			continue
		}
		m := protowire.ConsumeFieldValue(n, typ, msg)
		if m < 0 {
			return out
		}
		msg = msg[m:]
	}
	return out
}

// pbBytes returns the last occurrence of a length-delimited field.
func pbBytes(msg []byte, num protowire.Number) []byte {
	all := pbAll(msg, num)
	if len(all) == 0 {
		return nil
	}
	return all[len(all)-1]
}

func pbString(msg []byte, num protowire.Number) string { return string(pbBytes(msg, num)) }

// pbUint returns the last varint value of field num (0 when absent).
func pbUint(msg []byte, num protowire.Number) uint64 {
	var v uint64
	for len(msg) > 0 {
		n, typ, l := protowire.ConsumeTag(msg)
		if l < 0 {
			return v
		}
		msg = msg[l:]
		if n == num && typ == protowire.VarintType {
			x, m := protowire.ConsumeVarint(msg)
			if m < 0 {
				return v
			}
			v = x
			msg = msg[m:]
			continue
		}
		m := protowire.ConsumeFieldValue(n, typ, msg)
		if m < 0 {
			return v
		}
		msg = msg[m:]
	}
	return v
}

func pbTime(msg []byte, num protowire.Number) time.Time {
	ts := pbBytes(msg, num)
	if ts == nil {
		return time.Time{}
	}
	sec, nanos := pbUint(ts, tsFieldSeconds), pbUint(ts, tsFieldNanos)
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(int64(sec), int64(nanos))
}
