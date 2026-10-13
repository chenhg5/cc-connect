package feishu

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Query cards use full schema 2.0 updates. Unlike separate preview/progress
// cards, the same message handle also owns permission buttons and pagination.
type queryCard struct {
	budget            bool
	detailCache       []string
	detailCacheRecord core.ExecutionBlock
	detailCacheBudget string
	pinned            atomic.Bool
	taskKey           string
	mu                sync.Mutex
	p                 *Platform
	rc                replyContext
	handle            any
	id                string
	created           time.Time
	lastWrite         time.Time
	content, notice   string
	summary           string
	native            bool
	blocks            []core.ExecutionBlock
	records           map[string]core.ExecutionBlock
	view              executionView
	decisionUser      string
	decisionDraft     string
	decisionNotice    string
	notify            func(context.Context, core.DecisionNotification) error
	notification      *core.DecisionNotification
	prompt            *core.Card
	status            core.CardStatus
	done              bool
	answer            bool
	page              int
	unpaged           bool // new continuations never require callback navigation
	part              int
	lastRendered      string
}

var _ core.InteractiveStreamingCard = (*queryCard)(nil)
var _ core.FinalAnswerCard = (*queryCard)(nil)

// Leave room for UTF-8, JSON escapes, notices and approval controls within
// the same conservative payload budget used by Feishu's rich cards.
const queryPageRunes = 3000

func (p *interactivePlatform) SingleQueryCardEnabled() bool { return p.singleQueryCard }

func (p *interactivePlatform) CreateStreamingCard(ctx context.Context, rctx any) (core.StreamingCard, error) {
	if !p.singleQueryCard {
		return nil, nil
	}
	rc, ok := rctx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("%s: invalid query reply context", p.tag())
	}
	return p.Platform.createQueryCardStream(ctx, rc)
}

func (p *Platform) createQueryCard(ctx context.Context, rc replyContext, content string, answer bool) (*queryCard, error) {
	c := &queryCard{p: p, rc: rc, id: rand.Text(), created: time.Now(), status: core.CardStatusWorking, content: content, answer: answer, done: answer}
	if answer {
		c.status = core.CardStatusDone
	}
	return p.sendQueryCard(ctx, c)
}

func (p *Platform) sendQueryCard(ctx context.Context, c *queryCard) (*queryCard, error) {
	card, err := c.renderLocked()
	if err != nil {
		return nil, err
	}
	c.handle, err = p.SendPreviewStart(ctx, c.rc, card)
	if err != nil {
		return nil, err
	}
	c.lastWrite = time.Now()
	c.pinned.Store(!c.done)
	c.lastRendered = card
	c.mu.Lock()
	p.queryCardsMu.Lock()
	if p.queryCards == nil {
		p.queryCards = make(map[string]*queryCard)
	}
	for id, old := range p.queryCards {
		if !old.pinned.Load() && time.Since(old.created) > 24*time.Hour {
			delete(p.queryCards, id)
		}
	}
	if len(p.queryCards) >= 256 {
		oldest := ""
		for id, old := range p.queryCards {
			if !old.pinned.Load() && (oldest == "" || old.created.Before(p.queryCards[oldest].created)) {
				oldest = id
			}
		}
		delete(p.queryCards, oldest)
	}
	p.queryCards[c.id] = c
	p.queryCardsMu.Unlock()
	if err := c.persistLocked(false); err != nil {
		slog.Warn("query card persistence failed", "error", err)
	}
	c.mu.Unlock()
	p.pruneQueryCardSnapshots()
	return c, nil
}

func (c *queryCard) SetProgressSummary(summary string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.done {
		c.summary = summary
	}
}
func (c *queryCard) Failed() bool { return false } // update errors never spawn another message
func (c *queryCard) Update(ctx context.Context, content string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}
	c.content = content
	if time.Since(c.lastWrite) < 500*time.Millisecond {
		return nil
	}
	return c.writeLocked(ctx)
}
func (c *queryCard) SetPrompt(ctx context.Context, card *core.Card) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}
	c.prompt = card
	return c.writeLocked(ctx)
}
func (c *queryCard) SetNotice(ctx context.Context, notice string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}
	c.notice = notice
	return c.writeLocked(ctx)
}
func (c *queryCard) Finalize(ctx context.Context, content string) error {
	return c.Finish(ctx, content, core.CardStatusDone)
}
func (c *queryCard) SeparateFinalAnswer() bool { return c.p.separateFinalCard }

func (c *queryCard) FinalizeWithAnswer(ctx context.Context, progress, answer string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}
	c.content, c.prompt, c.done, c.status = progress, nil, true, core.CardStatusDone
	progressErr := c.writeLocked(ctx)
	if answer == "" {
		return progressErr
	}
	// A failed process update must not prevent delivery of the conclusion.
	if _, err := c.p.createQueryCard(ctx, c.rc, answer, true); err != nil {
		// Keep the answer accessible even if Feishu rejects the second message.
		// Put it first, independently of the process card's selected page.
		c.answer, c.summary = true, ""
		c.content = answer
		if progress != "" {
			c.content += "\n\n---\n\n" + progress
		}
		c.page = 0
		return errors.Join(progressErr, fmt.Errorf("create final answer card: %w", err), c.writeLocked(ctx))
	}
	return progressErr
}
func (c *queryCard) Finish(ctx context.Context, content string, status core.CardStatus) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}
	if content != "" {
		c.content = content
	}
	c.status = status
	c.prompt = nil
	c.done = true
	return c.writeLocked(ctx)
}
func (c *queryCard) writeLocked(ctx context.Context) error {
	c.pinned.Store(!c.done)
	card, err := c.renderLocked()
	if err != nil {
		return err
	}
	if card == c.lastRendered {
		return c.persistLocked(false)
	}
	err = c.p.withTransientRetry(ctx, "update query card", func() error {
		// Save the full body and reserve the next entity sequence before sending.
		// A crash after Feishu accepts the update must not reuse that sequence.
		if err := c.persistLocked(true); err != nil {
			return fmt.Errorf("persist query card before update: %w", err)
		}
		return c.p.UpdateMessage(ctx, c.handle, card)
	})
	if err == nil {
		c.lastWrite = time.Now()
		c.lastRendered = card
	} else {
		slog.Warn("query card update failed", "error", err)
	}
	return err
}

func (c *queryCard) renderLocked() (string, error) {
	if c.native {
		return c.renderExecutionLocked()
	}
	i18n := c.p.cardI18n()
	title, color := i18n.T(core.MsgStarting), "blue"
	if c.done {
		title, color = i18n.T(core.MsgQueryDone), "green"
		if c.status == core.CardStatusStopped {
			title, color = i18n.T(core.MsgExecStopped), "grey"
		}
		if c.status == core.CardStatusError {
			title, color = i18n.T(core.MsgQueryFailed), "red"
		}
	}
	if c.answer {
		title = i18n.T(core.MsgQueryAnswer)
	} else if c.p.separateFinalCard {
		title = i18n.T(core.MsgQueryProcess) + " · " + title
	}
	if c.part > 1 {
		title = fmt.Sprintf("%s · %d", title, c.part)
	}
	if c.prompt != nil && c.prompt.Header != nil {
		title, color = c.prompt.Header.Title, c.prompt.Header.Color
	}
	pages := []string{c.content}
	if !c.unpaged {
		pages = core.SplitMessageCodeFenceAware(c.content, queryPageRunes)
	}
	if len(pages) == 0 {
		pages = []string{""}
	}
	page := c.page
	if page >= len(pages) {
		page = len(pages) - 1
	}
	if page < 0 {
		page = 0
	}
	// Keep the reader on the selected page while content grows. Showing only
	// the last page during a turn made earlier output disappear without any
	// way to get back to it until completion.
	c.page = page
	var elements []map[string]any
	if c.native {
		elements = c.executionSummaryElements(c.blocks)
	}
	if !c.native && pages[page] != "" {
		detail := map[string]any{"tag": "markdown", "content": sanitizeMarkdownURLs(preprocessFeishuMarkdown(pages[page]))}
		if !c.answer && (c.summary != "" || c.p.separateFinalCard) {
			summary := c.summary
			if summary == "" {
				summary = i18n.T(core.MsgProgressFoldHint)
			}
			elements = append(elements, map[string]any{"tag": "markdown", "content": sanitizeMarkdownURLs(preprocessFeishuMarkdown(summary))})
			panel := buildProgressPanel(i18n.T(core.MsgProgressExpand), false, []map[string]any{detail})
			panel["element_id"] = "execution_details"
			elements = append(elements, panel)
		} else {
			elements = append(elements, detail)
		}
	}
	if c.notice != "" {
		elements = append(elements, map[string]any{"tag": "markdown", "content": c.notice, "text_size": "notation"})
	}
	if c.prompt != nil {
		elements = append(elements, map[string]any{"tag": "hr"})
		for _, element := range c.prompt.Elements {
			switch e := element.(type) {
			case core.CardMarkdown:
				elements = append(elements, map[string]any{"tag": "markdown", "content": e.Content})
			case core.CardNote:
				elements = append(elements, map[string]any{"tag": "markdown", "content": e.Text, "text_size": "notation"})
			case core.CardDivider:
				elements = append(elements, map[string]any{"tag": "hr"})
			case core.CardActions:
				for _, b := range e.Buttons {
					elements = append(elements, queryButton(b, c.rc.sessionKey))
				}
			}
		}
	}
	if c.prompt != nil && c.prompt.DecisionToken != "" {
		elements = append(elements, questionInput(c.prompt.DecisionToken, c.rc.sessionKey, i18n.T(core.MsgDecisionAnswer), i18n.T(core.MsgDecisionSubmit)))
	}
	if len(pages) > 1 {
		elements = append(elements, map[string]any{"tag": "markdown", "content": fmt.Sprintf("%d / %d", page+1, len(pages)), "text_size": "notation"})
		if page > 0 {
			elements = append(elements, queryButton(core.DefaultBtn(i18n.T(core.MsgCardPrev), fmt.Sprintf("query_page:%s:%d", c.id, page-1)), c.rc.sessionKey))
		}
		if page+1 < len(pages) {
			elements = append(elements, queryButton(core.DefaultBtn(i18n.T(core.MsgCardNext), fmt.Sprintf("query_page:%s:%d", c.id, page+1)), c.rc.sessionKey))
			elements = append(elements, queryButton(core.DefaultBtn(i18n.T(core.MsgQueryLatestPage), fmt.Sprintf("query_page:%s:latest", c.id)), c.rc.sessionKey))
		}
	}
	if len(elements) == 0 {
		elements = append(elements, map[string]any{"tag": "markdown", "content": title})
	}
	payload := map[string]any{"schema": "2.0", "config": map[string]any{"update_multi": true, "enable_forward": false}, "header": map[string]any{"title": plainText(title), "template": color}, "body": map[string]any{"elements": elements}}
	data, err := json.Marshal(payload)
	if err == nil && c.native {
		err = c.validateNativeCard(payload, data)
	} else if err == nil && len(data) > maxRichCardJSONBytes {
		return "", fmt.Errorf("query card exceeds payload budget: %d bytes", len(data))
	}
	return string(data), err
}
func queryButton(b core.CardButton, sessionKey string) map[string]any {
	value := map[string]string{"action": b.Value, "session_key": sessionKey}
	for k, v := range b.Extra {
		// The query event loop renders acknowledgement text itself. Do not
		// repeat the entire approval body in every button's callback value.
		if b.Extra["query_card"] == "true" && k != "query_card" && k != "permission_token" {
			continue
		}
		value[k] = v
	}
	typ := b.Type
	if typ == "" {
		typ = "default"
	}
	return map[string]any{"tag": "button", "text": plainText(b.Text), "type": typ, "behaviors": []map[string]any{{"type": "callback", "value": value}}}
}

var errQueryCardExpired = errors.New("query card pagination state unavailable")
var errQueryCardBusy = errors.New("query card update in progress")

func (p *Platform) pageQueryCard(action, chatID, messageID string) error {
	parts := strings.Split(action, ":")
	if len(parts) != 3 {
		return errQueryCardExpired
	}
	page := 0
	latest := parts[2] == "latest"
	if !latest {
		var err error
		page, err = strconv.Atoi(parts[2])
		if err != nil || page < 0 {
			return errQueryCardExpired
		}
	}
	c, err := p.loadQueryCard(parts[1])
	if err != nil {
		return err
	}
	// A running turn may be doing a slow network update. A callback must
	// return within Feishu's deadline, including time waiting for this lock.
	if !c.mu.TryLock() {
		return errQueryCardBusy
	}
	defer c.mu.Unlock()
	h, ok := c.handle.(*feishuPreviewHandle)
	if !ok || c.rc.chatID != chatID || h.messageID != messageID || time.Since(c.created) > 24*time.Hour {
		return errQueryCardExpired
	}
	pages := core.SplitMessageCodeFenceAware(c.content, queryPageRunes)
	if latest {
		page = len(pages) - 1
	}
	if page < 0 || page >= len(pages) {
		return errQueryCardExpired
	}
	c.page = page
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.writeLocked(ctx); err != nil {
		slog.Warn("query card page failed", "error", err)
		return err
	}
	return nil
}
