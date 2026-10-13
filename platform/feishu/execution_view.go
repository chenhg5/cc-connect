package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// The live record and the reader's snapshot have independent lifetimes.
// Only this view's current page is serialized into a Feishu message.
type executionView struct {
	Key         string              `json:"key,omitempty"`
	Snapshot    core.ExecutionBlock `json:"snapshot,omitempty"`
	Page        int                 `json:"page,omitempty"`
	SummaryPage int                 `json:"summary_page,omitempty"`
	Revision    uint64              `json:"revision,omitempty"`
}

func executionClip(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
func (c *queryCard) executionAction(label, verb, arg string) map[string]any {
	button := queryButton(core.DefaultBtn(label, fmt.Sprintf("exec:%s:%s:%s:%d", c.id, verb, arg, c.view.Revision)), c.rc.sessionKey)
	button["size"], button["type"] = "small", "text"
	if verb == "back" || verb == "refresh" {
		button["type"] = "primary_text"
	}
	return button
}
func (c *queryCard) executionRow(b core.ExecutionBlock, text string) map[string]any {
	action := fmt.Sprintf("exec:%s:open:%s:%d", c.id, b.Key, c.view.Revision)
	return map[string]any{"tag": "interactive_container", "padding": "0px 4px", "vertical_spacing": "0px", "behaviors": []map[string]any{{"type": "callback", "value": map[string]string{"action": action}}}, "elements": []map[string]any{{"tag": "markdown", "content": text, "text_size": "notation"}}}
}
func (c *queryCard) executionSummaryElements(blocks []core.ExecutionBlock) []map[string]any {
	var elements []map[string]any
	for _, b := range blocks {
		if b.Tool == "" {
			if strings.TrimSpace(b.Text) != "" {
				margin := "0px 0px 4px 0px"
				if len(elements) > 0 {
					margin = "12px 0px 4px 0px"
				}
				elements = append(elements, map[string]any{"tag": "markdown", "content": sanitizeMarkdownURLs(preprocessFeishuMarkdown(b.Text)), "margin": margin})
			}
			continue
		}
		elements = append(elements, map[string]any{"tag": "markdown", "content": c.executionCommandLine(b), "text_size": "notation", "margin": "0px"})
	}
	return elements
}
func (c *queryCard) executionStatus(b core.ExecutionBlock) string {
	i := c.p.cardI18n()
	status := "◷ " + i.T(core.MsgExecRunning)
	if b.Done {
		status = "✓ " + i.T(core.MsgExecDone)
	}
	if b.Status == "failed" || b.Status == "error" {
		status = "✗ " + i.T(core.MsgExecFailed)
	}
	if b.Status == "interrupted" {
		status = "⏹ " + i.T(core.MsgExecStopped)
	}
	if b.DurationMS > 0 {
		status += fmt.Sprintf(" · %.1fs", float64(b.DurationMS)/1000)
	}
	if b.ExitCode != nil {
		status += " · " + i.Tf(core.MsgExecExitCode, *b.ExitCode)
	}
	return status
}

func (c *queryCard) executionControls() []map[string]any {
	var out []map[string]any
	if c.prompt != nil {
		for _, e := range c.prompt.Elements {
			switch v := e.(type) {
			case core.CardMarkdown:
				out = append(out, map[string]any{"tag": "markdown", "content": v.Content})
			case core.CardNote:
				out = append(out, map[string]any{"tag": "markdown", "content": v.Text, "text_size": "notation"})
			case core.CardDivider:
				out = append(out, map[string]any{"tag": "hr"})
			case core.CardActions:
				for _, b := range v.Buttons {
					button := queryButton(b, c.rc.sessionKey)
					if button["type"] == "primary" {
						button["type"] = "primary_filled"
					}
					button["behaviors"].([]map[string]any)[0]["value"].(map[string]string)["execution_card"] = c.id
					out = append(out, button)
				}
			}
		}
		if c.decisionNotice != "" {
			out = append(out, map[string]any{"tag": "markdown", "content": c.decisionNotice, "text_size": "notation"})
			if c.notification != nil {
				out = append(out, c.executionAction(c.p.cardI18n().T(core.MsgExecRetryReminder), "remind", ""))
			}
		}
		out = []map[string]any{executionDecisionPanel(out)}
	}
	if c.notice != "" {
		out = append(out, map[string]any{"tag": "markdown", "content": c.notice, "text_size": "notation"})
	}
	return out
}

func (c *queryCard) executionPayload(body []map[string]any) (string, error) {
	i := c.p.cardI18n()
	title, color, icon := i.T(core.MsgProgressResponse), "grey", "⏳"
	for _, b := range c.blocks {
		if b.Tool != "" && !b.Done {
			title, color, icon = i.T(core.MsgProgressTool), "blue", "🧰"
			break
		}
		if b.Tool == "" && !b.Done && strings.TrimSpace(b.Text) != "" {
			title, color, icon = i.T(core.MsgProgressResponding), "blue", "✍️"
		}
	}
	if c.done {
		title, color, icon = i.T(core.MsgQueryDone), "green", "✓"
	}
	if c.status == core.CardStatusThinking && c.done {
		title, color = i.T(core.MsgExecReadArchive), "grey"
	}
	if c.status == core.CardStatusStopped {
		title, color, icon = i.T(core.MsgExecStopped), "grey", "⏹"
	}
	if c.status == core.CardStatusError {
		title, color, icon = i.T(core.MsgQueryFailed), "red", "✗"
	}
	if c.done {
		for _, b := range c.blocks {
			if b.Status == "interrupted" {
				title, color, icon = i.T(core.MsgExecStopped), "grey", "⏹"
				break
			}
		}
	}
	if c.prompt != nil && c.prompt.Header != nil {
		title, color = c.prompt.Header.Title, c.prompt.Header.Color
		icon = "⏳"
	}
	if c.budget {
		for _, key := range []core.MsgKey{core.MsgStarting, core.MsgQueryDone, core.MsgQueryFailed, core.MsgExecStopped, core.MsgExecReadArchive, core.MsgProgressResponse, core.MsgProgressTool, core.MsgProgressResponding} {
			if v := i.T(key); len(v) > len(title) {
				title = v
			}
		}
		color = "orange"
		// Use a stable maximum-width icon while measuring pages. Switching
		// from running to done must not move the reader's detail-page boundary.
		icon = "✍️"
	}
	caption := executionCaption(icon+" "+executionInlineText(title), color)
	caption["text_align"] = "left"
	elements := c.executionControls()
	elements = append(elements, body...)
	elements = append(elements, caption)
	elements = executionActionRows(elements)
	if c.prompt != nil && c.prompt.DecisionToken != "" {
		elements = c.executionQuestionForm(elements)
	}
	config := map[string]any{"update_multi": true, "enable_forward": false, "width_mode": "default"}
	padding := "8px 12px 8px 12px"
	if len(elements) == 1 && c.prompt == nil && c.notice == "" && len(body) == 0 {
		// Before the first output arrives, show a small acknowledgement instead
		// of a full-width empty execution card. Output grows this same message.
		config["width_mode"] = "compact"
		padding = "4px 12px 4px 12px"
	}
	payload := map[string]any{"schema": "2.0", "config": config, "body": map[string]any{"elements": elements, "vertical_spacing": "4px", "padding": padding}}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(data), c.validateNativeCard(payload, data)
}

// Navigation submits the form as a draft, not an answer. Required validation is
// performed only by question_submit. A stable form/input ID preserves client
// drafts; automatic live replacement is suspended while that question is open.
func (c *queryCard) executionQuestionForm(elements []map[string]any) []map[string]any {
	next := 0
	c.executionDraftControls(elements, &next)
	i := c.p.cardI18n()
	form := questionInput(c.prompt.DecisionToken, c.rc.sessionKey, i.T(core.MsgDecisionAnswer), i.T(core.MsgDecisionSubmit))
	fields := form["elements"].([]map[string]any)
	fields[1]["behaviors"].([]map[string]any)[0]["value"].(map[string]string)["execution_card"] = c.id
	fields[0]["required"] = false
	fields[0]["default_value"] = c.decisionDraft
	// CardKit permits only 20 characters. Hash the opaque request token so the
	// ID stays stable during navigation while satisfying the native contract.
	formID := sha256.Sum256([]byte(c.prompt.DecisionToken))
	form["element_id"] = fmt.Sprintf("d_%x", formID[:9])
	form["vertical_spacing"] = "4px"
	placed := false
	for _, e := range elements {
		if e["element_id"] == "execution_decision" {
			column := e["columns"].([]map[string]any)[0]
			column["elements"] = append(column["elements"].([]map[string]any), fields...)
			placed = true
			break
		}
	}
	if !placed {
		elements = append(elements, fields...)
	}
	form["elements"] = elements
	return []map[string]any{form}
}
func (c *queryCard) executionPageControls(verb string, page, total int) []map[string]any {
	out := []map[string]any{{"tag": "markdown", "content": fmt.Sprintf("%d / %d", page+1, total), "text_size": "notation"}}
	if page > 0 {
		out = append(out, c.executionAction(c.p.cardI18n().T(core.MsgCardPrev), verb, strconv.Itoa(page-1)))
	}
	if page+1 < total {
		out = append(out, c.executionAction(c.p.cardI18n().T(core.MsgCardNext), verb, strconv.Itoa(page+1)))
	}
	return out
}
func (c *queryCard) executionPagingReserve() []map[string]any {
	return c.executionPageControls("dpage", 999999997, 999999999)
}

func (c *queryCard) renderExecutionLocked() (string, error) {
	if c.view.Key != "" {
		return c.renderExecutionDetail()
	}
	elements := c.executionSummaryElements(c.blocks)
	if full, err := c.executionPayload(elements); err == nil {
		return full, nil
	} else if !errors.Is(err, errNativeCardCapacity) {
		return "", err
	}
	// The group exceeds an empty card or controls consumed its remaining space.
	// Keep its original text in the record archive and show an explicit entry.
	elements = nil
	for _, b := range c.blocks {
		if b.Tool == "" && len([]rune(b.Text)) > 600 {
			elements = append(elements, c.executionRow(b, sanitizeMarkdownURLs(preprocessFeishuMarkdown(executionClip(b.Text, 600)))+"\n"+c.p.cardI18n().T(core.MsgExecFullExplanation)))
		} else {
			elements = append(elements, c.executionSummaryElements([]core.ExecutionBlock{b})...)
		}
	}
	pages := [][]map[string]any{{}}
	for _, el := range elements {
		last := len(pages) - 1
		next := append(append([]map[string]any(nil), pages[last]...), el)
		if _, err := c.executionPayload(append(append([]map[string]any(nil), next...), c.executionPagingReserve()...)); err != nil {
			if !errors.Is(err, errNativeCardCapacity) {
				return "", err
			}
			if len(pages[last]) == 0 {
				return "", fmt.Errorf("%w: pending controls or summary entry", errNativeCardCapacity)
			}
			pages = append(pages, []map[string]any{el})
		} else {
			pages[last] = next
		}
	}
	c.view.SummaryPage = min(max(c.view.SummaryPage, 0), len(pages)-1)
	page := c.view.SummaryPage
	return c.executionPayload(append(pages[page], c.executionPageControls("spage", page, len(pages))...))
}

func (c *queryCard) executionDetailControls() []map[string]any {
	i := c.p.cardI18n()
	out := []map[string]any{c.executionAction(i.T(core.MsgExecBack), "back", "")}
	var prev, next string
	for n, b := range c.blocks {
		if b.Key != c.view.Key {
			continue
		}
		for j := n - 1; j >= 0; j-- {
			if c.blocks[j].Tool != "" {
				prev = c.blocks[j].Key
				break
			}
		}
		for j := n + 1; j < len(c.blocks); j++ {
			if c.blocks[j].Tool != "" {
				next = c.blocks[j].Key
				break
			}
		}
	}
	if prev != "" {
		out = append(out, c.executionAction(i.T(core.MsgExecPrevious), "open", prev))
	}
	if next != "" {
		out = append(out, c.executionAction(i.T(core.MsgExecNext), "open", next))
	}
	label := i.T(core.MsgExecRefreshCurrent)
	if live, ok := c.records[c.view.Key]; ok && !sameExecutionRecord(live, c.view.Snapshot) {
		label = i.T(core.MsgExecRefresh)
	}
	out = append(out, c.executionAction(label, "refresh", ""))
	return out
}
func (c *queryCard) executionDetailBody(text string) map[string]any {
	if c.view.Snapshot.Tool != "" {
		text = literalCode(text)
	} else {
		text = sanitizeMarkdownURLs(preprocessFeishuMarkdown(text))
	}
	return map[string]any{"tag": "markdown", "content": text}
}
func (c *queryCard) executionDetailPages() ([]string, error) {
	oldRevision := c.view.Revision
	c.view.Revision = ^uint64(0)
	c.budget = true
	defer func() { c.view.Revision = oldRevision; c.budget = false }()

	controls := c.executionDetailControls()
	controls[len(controls)-1] = c.executionAction(c.p.cardI18n().T(core.MsgExecRefresh), "refresh", "")
	controls = append(controls, executionCaption(c.executionStatus(c.view.Snapshot), "grey"))
	controls = append(controls, c.executionPagingReserve()...)
	budget, err := c.executionPayload(controls)
	if err != nil {
		return nil, err
	}
	if c.detailCache != nil && budget == c.detailCacheBudget && sameExecutionRecord(c.detailCacheRecord, c.view.Snapshot) {
		return c.detailCache, nil
	}
	raw := []rune(c.executionDetailText())
	pages := []string{}
	for len(raw) > 0 {
		low, high := 0, min(len(raw), nativeCardMaxBytes)
		for low < high {
			n := (low + high + 1) / 2
			_, err := c.executionPayload(append(append([]map[string]any(nil), controls...), c.executionDetailBody(string(raw[:n]))))
			if err == nil {
				low = n
			} else if errors.Is(err, errNativeCardCapacity) {
				high = n - 1
			} else {
				return nil, err
			}
		}
		if low == 0 {
			return nil, fmt.Errorf("%w: detail controls", errNativeCardCapacity)
		}
		pages = append(pages, string(raw[:low]))
		raw = raw[low:]
	}
	if len(pages) == 0 {
		pages = []string{c.p.cardI18n().T(core.MsgExecNoOutput)}
	}
	c.detailCache, c.detailCacheRecord, c.detailCacheBudget = pages, c.view.Snapshot, budget
	return pages, nil
}
func (c *queryCard) renderExecutionDetail() (string, error) {
	pages, err := c.executionDetailPages()
	if err != nil {
		return "", err
	}
	c.view.Page = min(max(c.view.Page, 0), len(pages)-1)
	elements := c.executionDetailControls()
	elements = append(elements, executionCaption(c.executionStatus(c.view.Snapshot), "grey"))
	elements = append(elements, c.executionDetailBody(pages[c.view.Page]))
	if len(pages) > 1 {
		elements = append(elements, c.executionPageControls("dpage", c.view.Page, len(pages))...)
	}
	return c.executionPayload(elements)
}

// Read-only callbacks are served by the platform, independently of agent work.
func (p *Platform) navigateExecution(action, chat, message, user string, form map[string]any) error {
	a := strings.Split(action, ":")
	if len(a) != 5 {
		return errQueryCardExpired
	}
	c, err := p.loadQueryCard(a[1])
	if err != nil {
		return err
	}
	// onCardAction acknowledges independently of this wait. Streaming updates
	// must serialize with navigation, not discard a click when HTTP is in flight.
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.handle.(*feishuPreviewHandle)
	if !ok || c.rc.chatID != chat || h.messageID != message || !c.native {
		return errQueryCardExpired
	}
	revision, err := strconv.ParseUint(a[4], 10, 64)
	if err != nil || revision != c.view.Revision {
		return errQueryCardBusy
	}
	previous := c.view
	if c.prompt != nil && c.prompt.DecisionToken != "" {
		if c.decisionUser != "" && c.decisionUser != user {
			return errQueryCardExpired
		}
		if draft, ok := form["answer"].(string); ok {
			c.decisionDraft = draft
		}
	}
	switch a[2] {
	case "open":
		b, ok := c.records[a[3]]
		if !ok {
			return errQueryCardExpired
		}
		c.view.Key, c.view.Snapshot, c.view.Page = b.Key, b, 0
	case "back":
		c.view.Key = ""
		c.view.Page = 0
	case "refresh":
		b, ok := c.records[c.view.Key]
		if !ok {
			return errQueryCardExpired
		}
		c.view.Snapshot = b
	case "spage", "dpage":
		n, err := strconv.Atoi(a[3])
		if err != nil || n < 0 {
			return errQueryCardExpired
		}
		if a[2] == "spage" {
			c.view.SummaryPage = n
		} else {
			c.view.Page = n
		}
	case "remind":
		if c.notification == nil || c.decisionUser != user || c.prompt == nil {
			return errQueryCardExpired
		}
	default:
		return errQueryCardExpired
	}
	c.view.Revision++
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if a[2] == "remind" {
		c.notifyDecisionLocked(ctx)
	}
	if err := c.writeLocked(ctx); err != nil {
		c.view = previous
		_ = c.persistLocked(false)
		return err
	}
	return nil
}

func sameExecutionRecord(a, b core.ExecutionBlock) bool {
	if (a.ExitCode == nil) != (b.ExitCode == nil) {
		return false
	}
	if a.ExitCode != nil && *a.ExitCode != *b.ExitCode {
		return false
	}
	a.ExitCode = nil
	b.ExitCode = nil
	return a == b
}
