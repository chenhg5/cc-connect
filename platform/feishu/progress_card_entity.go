package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

// Progress cards keep the panels a reader opened open.
//
// A collapsible_panel opens and closes on the Feishu client only. Replacing
// the whole card (Im.Message.Patch, or a full card entity update) re-mounts it
// and every panel falls back to the expanded flag in the new JSON, so a panel
// the reader opened snapped shut on the next refresh. Progress cards are
// therefore sent as card entities and refreshed in place: each update becomes
// one batch_update that adds, changes and removes rows and retitles panels by
// element_id, and never re-sends a panel or its expanded flag. Panels start
// collapsed when they first appear.
//
// The card header cannot be changed in place (batch_update has no header
// action), so the status lives in the first body element instead.

const (
	progressStatusElementID   = "pc_status"
	progressTruncElementID    = "pc_trunc"
	progressTruncHRElementID  = "pc_trunc_hr"
	progressFooterHRElementID = "pc_foot_hr"
	progressFooterElementID   = "pc_footer"
)

// progressLane is one panel of a progress card.
type progressLane struct {
	key   string // element_id suffix and row id prefix
	label string
}

var progressLanes = []progressLane{
	{key: "r", label: "Reasoning"},
	{key: "t", label: "Tools"},
	{key: "u", label: "Updates"},
}

func progressPanelElementID(lane progressLane) string { return "pc_" + lane.key }

// progressCardView is what a payload draws, before element ids are assigned
// to rows.
type progressCardView struct {
	status    map[string]any
	summary   string
	truncated map[string]any // nil when the card is not truncated
	lanes     []progressLaneView
	footer    map[string]any // nil when there is no footer
}

type progressLaneView struct {
	title string
	rows  []map[string]any // empty when the lane is absent
}

// progressCardModel is what the card entity currently shows.
type progressCardModel struct {
	status    string
	summary   string
	truncated bool
	lanes     []progressLaneModel
	footer    string
	nextRow   int
}

type progressLaneModel struct {
	present bool
	title   string
	rows    []progressRow
}

type progressRow struct {
	id   string
	body string // the row JSON without its element_id
}

func newProgressCardView(payload *core.ProgressCardPayload) progressCardView {
	items := normalizeProgressItems(payload)
	agent := progressAgentLabel(payload.Agent)
	title, template, footer := progressStateMeta(payload.State, payload.Lang, agent)
	view := progressCardView{
		status: map[string]any{
			"tag":     "markdown",
			"content": fmt.Sprintf("<font color='%s'>**%s**</font>", template, title),
		},
		summary: title,
	}
	if payload.Truncated {
		text := "Showing latest updates only."
		if isZhLikeProgressLang(payload.Lang) {
			text = "仅显示最近更新。"
		}
		view.truncated = progressNotationElement(text)
	}
	reasoning, tools, others := splitProgressItemsByLane(items)
	for i, laneItems := range [][]core.ProgressCardEntry{reasoning, tools, others} {
		lv := progressLaneView{title: progressPanelTitle(progressLanes[i].label, len(laneItems), payload.Lang)}
		lv.rows = buildProgressPanelElements(laneItems, payload.Lang)
		view.lanes = append(view.lanes, lv)
	}
	if footer != "" {
		view.footer = progressNotationElement(footer)
	}
	return view
}

func progressNotationElement(text string) map[string]any {
	return map[string]any{
		"tag": "div",
		"text": map[string]any{
			"tag":        "plain_text",
			"content":    text,
			"text_size":  "notation",
			"text_color": "grey",
		},
	}
}

func progressElementJSON(element map[string]any) string {
	b, _ := json.Marshal(element)
	return string(b)
}

func withElementID(element map[string]any, id string) map[string]any {
	out := make(map[string]any, len(element)+1)
	for k, v := range element {
		out[k] = v
	}
	out["element_id"] = id
	return out
}

func (m *progressCardModel) newRow(lane progressLane, element map[string]any) (progressRow, map[string]any) {
	m.nextRow++
	row := progressRow{id: "pc_" + lane.key + strconv.Itoa(m.nextRow), body: progressElementJSON(element)}
	return row, withElementID(element, row.id)
}

// newPanel draws a lane panel the first time it appears: collapsed.
func (m *progressCardModel) newPanel(i int, lv progressLaneView) (progressLaneModel, map[string]any) {
	lane := progressLanes[i]
	lm := progressLaneModel{present: true, title: lv.title}
	rows := make([]map[string]any, 0, len(lv.rows))
	for _, element := range lv.rows {
		row, withID := m.newRow(lane, element)
		lm.rows = append(lm.rows, row)
		rows = append(rows, withID)
	}
	panel := withElementID(map[string]any{
		"tag":              "collapsible_panel",
		"expanded":         false,
		"background_color": "grey",
		"header": map[string]any{
			"title": map[string]any{"tag": "plain_text", "content": lv.title},
		},
		"border":           map[string]any{"color": "grey"},
		"vertical_spacing": "8px",
		"padding":          "4px 8px",
		"elements":         rows,
	}, progressPanelElementID(lane))
	return lm, panel
}

// newProgressCard draws the whole card for its first send and returns what it
// shows.
func newProgressCard(payload *core.ProgressCardPayload) (*progressCardModel, string) {
	view := newProgressCardView(payload)
	m := &progressCardModel{
		status:  progressElementJSON(view.status),
		summary: view.summary,
		lanes:   make([]progressLaneModel, len(progressLanes)),
	}
	elements := []map[string]any{withElementID(view.status, progressStatusElementID)}
	if view.truncated != nil {
		m.truncated = true
		elements = append(elements,
			withElementID(view.truncated, progressTruncElementID),
			map[string]any{"tag": "hr", "element_id": progressTruncHRElementID})
	}
	for i, lv := range view.lanes {
		if len(lv.rows) == 0 {
			continue
		}
		lm, panel := m.newPanel(i, lv)
		m.lanes[i] = lm
		elements = append(elements, panel)
	}
	if view.footer != nil {
		m.footer = progressElementJSON(view.footer)
		elements = append(elements,
			map[string]any{"tag": "hr", "element_id": progressFooterHRElementID},
			withElementID(view.footer, progressFooterElementID))
	}
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"wide_screen_mode": true,
			"update_multi":     true,
			"summary":          map[string]any{"content": view.summary},
		},
		"body": map[string]any{"elements": elements},
	}
	b, _ := json.Marshal(card)
	return m, string(b)
}

// progressCardUpdate turns the card m shows into the one payload draws: the
// batch_update actions to send and what the card shows afterwards. Panels that
// stay are never re-sent, so the client keeps them open or closed.
func progressCardUpdate(m *progressCardModel, payload *core.ProgressCardPayload) ([]map[string]any, *progressCardModel) {
	view := newProgressCardView(payload)
	next := &progressCardModel{
		status:    progressElementJSON(view.status),
		summary:   view.summary,
		truncated: view.truncated != nil,
		lanes:     make([]progressLaneModel, len(progressLanes)),
		nextRow:   m.nextRow,
	}
	var actions []map[string]any
	add := func(target, position string, elements ...map[string]any) {
		actions = append(actions, map[string]any{"action": "add_elements", "params": map[string]any{
			"type": position, "target_element_id": target, "elements": elements,
		}})
	}
	remove := func(ids ...string) {
		actions = append(actions, map[string]any{"action": "delete_elements", "params": map[string]any{"element_ids": ids}})
	}
	replace := func(id string, element map[string]any) {
		actions = append(actions, map[string]any{"action": "update_element", "params": map[string]any{
			"element_id": id, "element": withElementID(element, id),
		}})
	}

	if next.status != m.status {
		replace(progressStatusElementID, view.status)
	}
	if next.summary != m.summary {
		actions = append(actions, map[string]any{"action": "partial_update_setting", "params": map[string]any{
			"settings": map[string]any{"config": map[string]any{"summary": map[string]any{"content": next.summary}}},
		}})
	}

	anchor := progressStatusElementID
	switch {
	case next.truncated && !m.truncated:
		add(anchor, "insert_after",
			withElementID(view.truncated, progressTruncElementID),
			map[string]any{"tag": "hr", "element_id": progressTruncHRElementID})
	case !next.truncated && m.truncated:
		remove(progressTruncElementID, progressTruncHRElementID)
	}
	if next.truncated {
		anchor = progressTruncHRElementID
	}

	for i, lv := range view.lanes {
		lane := progressLanes[i]
		old := m.lanes[i]
		panelID := progressPanelElementID(lane)
		switch {
		case len(lv.rows) == 0:
			if old.present {
				remove(panelID)
			}
			continue
		case !old.present:
			lm, panel := next.newPanel(i, lv)
			next.lanes[i] = lm
			add(anchor, "insert_after", panel)
		default:
			lm := progressLaneModel{present: true, title: lv.title}
			if lv.title != old.title {
				actions = append(actions, map[string]any{"action": "partial_update_element", "params": map[string]any{
					"element_id": panelID,
					"partial_element": map[string]any{
						"header": map[string]any{"title": map[string]any{"tag": "plain_text", "content": lv.title}},
					},
				}})
			}
			lm.rows = next.updateRows(lane, old.rows, lv.rows, &actions)
			next.lanes[i] = lm
		}
		anchor = panelID
	}

	if view.footer != nil {
		next.footer = progressElementJSON(view.footer)
	}
	switch {
	case next.footer != "" && m.footer == "":
		add(anchor, "insert_after",
			map[string]any{"tag": "hr", "element_id": progressFooterHRElementID},
			withElementID(view.footer, progressFooterElementID))
	case next.footer == "" && m.footer != "":
		remove(progressFooterHRElementID, progressFooterElementID)
	case next.footer != m.footer:
		replace(progressFooterElementID, view.footer)
	}
	return actions, next
}

// updateRows turns a panel's rows from old into elements. Progress grows at
// the end, rewrites rows in place (a tool result lands on its call) and drops
// rows from the front when truncated, so it keeps old[drop:] in place for the
// drop that needs the fewest deletes and rewrites, rewrites the kept rows
// that changed and appends the rest. Every choice ends with exactly elements.
func (m *progressCardModel) updateRows(lane progressLane, old []progressRow, elements []map[string]any, actions *[]map[string]any) []progressRow {
	bodies := make([]string, len(elements))
	for i, element := range elements {
		bodies[i] = progressElementJSON(element)
	}
	drop, best := len(old), -1
	for d := 0; d <= len(old); d++ {
		kept := len(old) - d
		if kept > len(bodies) {
			continue
		}
		cost := d
		for i := 0; i < kept; i++ {
			if old[d+i].body != bodies[i] {
				cost++
			}
		}
		if best < 0 || cost < best {
			drop, best = d, cost
		}
	}
	kept := len(old) - drop
	var deleted []string
	for _, row := range old[:drop] {
		deleted = append(deleted, row.id)
	}
	if len(deleted) > 0 {
		*actions = append(*actions, map[string]any{"action": "delete_elements", "params": map[string]any{"element_ids": deleted}})
	}
	rows := append([]progressRow(nil), old[drop:]...)
	for i := 0; i < kept; i++ {
		if rows[i].body == bodies[i] {
			continue
		}
		rows[i].body = bodies[i]
		*actions = append(*actions, map[string]any{"action": "update_element", "params": map[string]any{
			"element_id": rows[i].id, "element": withElementID(elements[i], rows[i].id),
		}})
	}
	var appended []map[string]any
	for _, element := range elements[kept:] {
		row, withID := m.newRow(lane, element)
		rows = append(rows, row)
		appended = append(appended, withID)
	}
	if len(appended) > 0 {
		*actions = append(*actions, map[string]any{"action": "add_elements", "params": map[string]any{
			"type": "append", "target_element_id": progressPanelElementID(lane), "elements": appended,
		}})
	}
	return rows
}

// updateProgressCardEntity refreshes a progress card sent as a card entity in
// place. It reports false when the card was not sent that way, leaving the
// caller to replace the card.
func (p *Platform) updateProgressCardEntity(ctx context.Context, h *feishuPreviewHandle, payload *core.ProgressCardPayload) (bool, error) {
	h.progressMu.Lock()
	defer h.progressMu.Unlock()
	if h.progress == nil {
		return false, nil
	}
	actions, next := progressCardUpdate(h.progress, payload)
	if len(actions) == 0 {
		return true, nil
	}
	if err := p.batchUpdateCardEntity(ctx, h, actions); err != nil {
		// Only Feishu rejecting the actions means the card no longer matches
		// what h.progress says it shows. A network failure, timeout or rate
		// limit leaves h.progress as it was: core keeps the content and
		// retries, and that refresh diffs from the same card again. Replacing
		// the card would close the panels the reader opened.
		var cardErr *feishuCardAPIError
		if !errors.As(err, &cardErr) || isTransientError(err) || errors.Is(err, errFeishuCardRateLimited) {
			return true, err
		}
		// Keep the card right even though open panels close.
		slog.Warn(p.tag()+": progress card in-place update rejected, replacing the card", "message_id", h.messageID, "error", err)
		model, cardJSON := newProgressCard(payload)
		if err := p.updateCardEntity(ctx, h, cardJSON); err != nil {
			return true, err
		}
		h.progress = model
		return true, nil
	}
	h.progress = next
	return true, nil
}

// batchUpdateCardEntity applies actions to a card entity in one call
// (POST /open-apis/cardkit/v1/cards/{card_id}/batch_update).
func (p *Platform) batchUpdateCardEntity(ctx context.Context, h *feishuPreviewHandle, actions []map[string]any) error {
	actionsJSON, err := json.Marshal(actions)
	if err != nil {
		return fmt.Errorf("%s: batch update card entity: %w", p.tag(), err)
	}
	h.mu.Lock()
	if h.cardID == "" {
		h.mu.Unlock()
		return fmt.Errorf("%s: batch update card entity: cardID not set", p.tag())
	}
	h.sequence++
	cardID := h.cardID
	seq := h.sequence
	h.mu.Unlock()

	apiPath := fmt.Sprintf("/open-apis/cardkit/v1/cards/%s/batch_update", cardID)
	// Retries resend the same sequence and uuid, so Feishu applies the batch
	// at most once.
	body := map[string]any{
		"uuid":     fmt.Sprintf("pc-%s-%d", cardID, seq),
		"sequence": seq,
		"actions":  string(actionsJSON),
	}
	return p.withTransientRetry(ctx, "batch update card entity", func() error {
		return p.postCardEntityBatch(ctx, apiPath, body)
	})
}

func (p *Platform) postCardEntityBatch(ctx context.Context, apiPath string, body map[string]any) error {
	var apiResp *larkcore.ApiResp
	if err := p.withFreshTenantAccessTokenRetry(ctx, "batch update card entity", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
		var err error
		apiResp, err = client.Post(ctx, apiPath, body, larkcore.AccessTokenTypeTenant, options...)
		return err
	}); err != nil {
		return fmt.Errorf("%s: batch update card entity: %w", p.tag(), err)
	}
	if apiResp == nil {
		return fmt.Errorf("%s: batch update card entity: no response", p.tag())
	}
	// Feishu rejects actions with HTTP 400 and a code in the body (300301
	// duplicate element id, 300313 unknown element id, ...), so read the code
	// before the status: a rejection must reach the caller as a
	// feishuCardAPIError, or the refresh would retry the same diff forever.
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	parseErr := json.Unmarshal(apiResp.RawBody, &resp)
	if parseErr == nil && resp.Code != 0 {
		return fmt.Errorf("%s: %w", p.tag(), classifyFeishuCardAPIError("batch update card entity", resp.Code, resp.Msg))
	}
	if apiResp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: batch update card entity: HTTP status %d", p.tag(), apiResp.StatusCode)
	}
	if parseErr != nil {
		return fmt.Errorf("%s: batch update card entity: parse response: %w", p.tag(), parseErr)
	}
	return nil
}
