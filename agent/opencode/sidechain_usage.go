package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
	_ "modernc.org/sqlite"
)

type opencodeSidechainMeter struct {
	path, session   string
	meter           *core.UsageMeter
	incomplete      bool
	baselinePending bool
}

func opencodeDataDir(extraEnv []string) string {
	for _, entry := range core.MergeEnv(os.Environ(), extraEnv) {
		if value, ok := strings.CutPrefix(entry, "XDG_DATA_HOME="); ok && value != "" {
			return filepath.Join(value, "opencode")
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "opencode")
}

func (m *opencodeSidechainMeter) begin(dir, session string, baseline bool) {
	if m.meter != nil || session == "" {
		return
	}
	m.path, m.session = filepath.Join(dir, "opencode.db"), session
	var restored bool
	m.meter, restored = core.NewUsageMeter(filepath.Join(dir, "cc-connect-usage", session+".json"))
	if baseline && !restored {
		m.baselinePending = true
		m.collect()
	} else {
		// Persist ownership before the first result, including an empty meter.
		m.meter.Save()
	}
}

func (m *opencodeSidechainMeter) collect() *core.SidechainUsage {
	m.incomplete = false
	if m.meter == nil {
		return nil
	}
	if _, err := os.Stat(m.path); err != nil {
		m.incomplete = true
		slog.Warn("opencode: child usage database unavailable", "error", err)
		return nil
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: m.path, RawQuery: "mode=ro"}).String())
	if err != nil {
		m.incomplete = true
		slog.Warn("opencode: open child usage database", "error", err)
		return nil
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `WITH RECURSIVE family(id) AS (
		SELECT id FROM session WHERE id = ?
		UNION SELECT s.id FROM session s JOIN family f ON s.parent_id = f.id
	) SELECT m.id,m.data FROM message m JOIN family f ON m.session_id = f.id
	WHERE m.session_id <> ?`, m.session, m.session)
	if err != nil {
		m.incomplete = true
		slog.Warn("opencode: read child usage", "error", err)
		return nil
	}
	defer func() { _ = rows.Close() }()
	var total core.TokenUsage
	byModel := map[string]core.TokenUsage{}
	requests := 0
	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			m.incomplete = true
			slog.Warn("opencode: decode child usage row", "error", err)
			continue
		}
		var message struct {
			Role       string
			ModelID    string
			ProviderID string
			Time       struct{ Completed *int64 }
			Tokens     struct {
				Input, Output, Reasoning int
				Cache                    struct{ Read, Write int }
			}
		}
		if err := json.Unmarshal([]byte(data), &message); err != nil {
			m.incomplete = true
			slog.Warn("opencode: invalid child usage message", "error", err)
			continue
		}
		if message.Role != "assistant" || message.Time.Completed == nil {
			continue
		}
		tokens := message.Tokens
		delta := m.meter.Observe(id, core.TokenUsage{InputTokens: tokens.Input, OutputTokens: tokens.Output + tokens.Reasoning,
			ReasoningOutputTokens: tokens.Reasoning, CacheReadInputTokens: tokens.Cache.Read, CacheCreationInputTokens: tokens.Cache.Write})
		if !delta.Empty() {
			requests++
		}
		total.Add(delta)
		model := message.ModelID
		if message.ProviderID != "" && model != "" {
			model = message.ProviderID + "/" + model
		}
		usage := byModel[model]
		usage.Add(delta)
		byModel[model] = usage
	}
	if err := rows.Err(); err != nil {
		m.incomplete = true
		slog.Warn("opencode: child usage query interrupted", "error", err)
	}
	if m.baselinePending {
		// An unavailable legacy baseline is retried rather than billing its
		// historical requests after the database recovers.
		if !m.incomplete {
			m.baselinePending = false
			m.meter.Save()
		}
		m.incomplete = true
		return nil
	}
	m.meter.Save()
	if total.Empty() {
		return nil
	}
	return &core.SidechainUsage{Requests: requests, InputTokens: total.InputTokens, OutputTokens: total.OutputTokens,
		CacheReadInputTokens: total.CacheReadInputTokens, CacheCreationInputTokens: total.CacheCreationInputTokens, ReasoningOutputTokens: total.ReasoningOutputTokens, ByModel: byModel}
}
