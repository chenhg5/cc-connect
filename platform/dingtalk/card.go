package dingtalk

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// aiCard implements core.StreamingCard for DingTalk AI Card streaming.
type aiCard struct {
	cardInstanceId string
	outTrackId     string
	templateKey    string // 卡片模板变量名，默认 "content"
	platform       *Platform

	mu              sync.Mutex
	state           string // "processing" | "finished" | "failed"
	lastSentContent string
	lastSentAt      time.Time

	// 节流控制（single-flight + latest-wins 语义）
	throttleMs     int
	pendingContent string
	timer          *time.Timer
	inFlight       bool
	done           chan struct{} // closed when finalized or failed
}

// Ensure aiCard implements core.StreamingCard
var _ core.StreamingCard = (*aiCard)(nil)

// generateOutTrackID generates a unique outTrackId for AI Card.
func generateOutTrackID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("card_%d_%s", time.Now().UnixMilli(), hex.EncodeToString(b))
}

// generateGUID generates a UUID-like string for API requests.
func generateGUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	// Set version (4) and variant bits
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// createAICard creates a new AI Card instance and delivers it to the conversation.
func (p *Platform) createAICard(ctx context.Context, rc replyContext) (*aiCard, error) {
	token, err := p.getAccessToken()
	if err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}

	outTrackId := generateOutTrackID()
	isGroup := rc.isGroup

	// Build openSpaceId based on conversation type
	var openSpaceId string
	if isGroup {
		openSpaceId = fmt.Sprintf("dtv1.card//IM_GROUP.%s", rc.conversationId)
	} else {
		openSpaceId = fmt.Sprintf("dtv1.card//IM_ROBOT.%s", p.robotCode)
	}

	// Build card data
	cardParamMap := map[string]string{
		"config":         `{"autoLayout":true,"enableForward":true}`,
		p.cardTemplateKey: "",
	}

	payload := map[string]any{
		"cardTemplateId": p.cardTemplateID,
		"outTrackId":     outTrackId,
		"cardData": map[string]any{
			"cardParamMap": cardParamMap,
		},
		"callbackType":            "STREAM",
		"imGroupOpenSpaceModel":   map[string]any{"supportForward": true},
		"imRobotOpenSpaceModel":   map[string]any{"supportForward": true},
		"openSpaceId":             openSpaceId,
		"userIdType":              1,
	}

	// Set delivery model based on conversation type
	if isGroup {
		payload["imGroupOpenDeliverModel"] = map[string]any{
			"robotCode": p.robotCode,
		}
	} else {
		payload["imRobotOpenDeliverModel"] = map[string]any{
			"spaceType": "IM_ROBOT",
			"robotCode": p.robotCode,
		}
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		"https://api.dingtalk.com/v1.0/card/instances/createAndDeliver",
		bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", token)

	slog.Debug("dingtalk: creating AI card", "outTrackId", outTrackId, "isGroup", isGroup)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	slog.Debug("dingtalk: createAndDeliver response",
		"status", resp.StatusCode,
		"body", string(respBody))

	if resp.StatusCode != http.StatusOK {
		slog.Error("dingtalk: create AI card failed",
			"status", resp.StatusCode,
			"body", string(respBody))
		// Check if we should trigger degrade
		if resp.StatusCode == 403 || resp.StatusCode == 429 || resp.StatusCode >= 500 {
			p.activateCardDegrade(fmt.Sprintf("card.create:%d", resp.StatusCode))
		}
		return nil, fmt.Errorf("create AI card: status=%d, body=%s", resp.StatusCode, string(respBody))
	}

	// Parse response to get cardInstanceId
	var result struct {
		Result struct {
			CardInstanceId  string `json:"cardInstanceId"`
			OutTrackId      string `json:"outTrackId"`
			ProcessQueryKey string `json:"processQueryKey"`
		} `json:"result"`
		CardInstanceId string `json:"cardInstanceId"`
		OutTrackId     string `json:"outTrackId"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		slog.Warn("dingtalk: failed to parse createAndDeliver response", "error", err, "body", string(respBody))
	}

	cardInstanceId := result.Result.CardInstanceId
	if cardInstanceId == "" {
		cardInstanceId = result.CardInstanceId
	}
	if cardInstanceId == "" {
		cardInstanceId = outTrackId
	}

	resolvedOutTrackId := result.Result.OutTrackId
	if resolvedOutTrackId == "" {
		resolvedOutTrackId = result.OutTrackId
	}
	if resolvedOutTrackId == "" {
		resolvedOutTrackId = outTrackId
	}

	// Check deliverResults for actual delivery success
	var deliverCheck struct {
		Result struct {
			DeliverResults []struct {
				Success  bool   `json:"success"`
				ErrorMsg string `json:"errorMsg"`
			} `json:"deliverResults"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBody, &deliverCheck); err == nil {
		for _, dr := range deliverCheck.Result.DeliverResults {
			if !dr.Success {
				slog.Warn("dingtalk: AI card delivery failed",
					"errorMsg", dr.ErrorMsg,
					"outTrackId", outTrackId,
					"isGroup", isGroup)
				return nil, fmt.Errorf("AI card delivery failed: %s", dr.ErrorMsg)
			}
		}
	}

	slog.Info("dingtalk: AI card created",
		"cardInstanceId", cardInstanceId,
		"outTrackId", resolvedOutTrackId)

	card := &aiCard{
		cardInstanceId: cardInstanceId,
		outTrackId:     resolvedOutTrackId,
		templateKey:    p.cardTemplateKey,
		platform:       p,
		state:          "processing",
		throttleMs:     p.cardThrottleMs,
		done:           make(chan struct{}),
	}

	return card, nil
}

// Update replaces the card content with the given markdown.
// Implements throttling using single-flight + latest-wins semantics.
func (c *aiCard) Update(ctx context.Context, content string) error {
	c.mu.Lock()

	// If already finished or failed, skip
	if c.state == "finished" || c.state == "failed" {
		c.mu.Unlock()
		return nil
	}

	c.pendingContent = content

	// If there's an in-flight request, schedule a timer
	if c.inFlight {
		c.scheduleFlushLocked()
		c.mu.Unlock()
		return nil
	}

	// If enough time has passed since last send, flush immediately
	if c.timer == nil && time.Since(c.lastSentAt) >= time.Duration(c.throttleMs)*time.Millisecond {
		c.mu.Unlock()
		c.flush(ctx)
		return nil
	}

	// Otherwise, schedule a timer
	c.scheduleFlushLocked()
	c.mu.Unlock()
	return nil
}

// scheduleFlushLocked schedules a flush after throttleMs. Must be called with mu held.
func (c *aiCard) scheduleFlushLocked() {
	if c.timer != nil {
		return
	}
	delay := time.Duration(c.throttleMs)*time.Millisecond - time.Since(c.lastSentAt)
	if delay < 0 {
		delay = 0
	}
	c.timer = time.AfterFunc(delay, func() {
		c.mu.Lock()
		c.timer = nil
		c.mu.Unlock()
		c.flush(context.Background())
	})
}

// flush sends the pending content to the DingTalk streaming API.
func (c *aiCard) flush(ctx context.Context) {
	c.mu.Lock()
	if c.state == "finished" || c.state == "failed" {
		c.mu.Unlock()
		return
	}
	if c.pendingContent == "" {
		c.mu.Unlock()
		return
	}
	if c.inFlight {
		c.mu.Unlock()
		return
	}

	content := c.pendingContent
	c.pendingContent = ""
	c.inFlight = true
	c.mu.Unlock()

	err := c.doStream(ctx, content, false)

	c.mu.Lock()
	c.inFlight = false
	if err != nil {
		slog.Error("dingtalk: AI card stream update failed", "error", err)
		// Check pending content that arrived during in-flight
		if c.pendingContent != "" {
			c.scheduleFlushLocked()
		}
	} else {
		c.lastSentContent = content
		c.lastSentAt = time.Now()
		// Check if new content arrived during in-flight
		if c.pendingContent != "" {
			c.scheduleFlushLocked()
		}
	}
	c.mu.Unlock()
}

// doStream sends content to the DingTalk streaming API.
func (c *aiCard) doStream(ctx context.Context, content string, isFinalize bool) error {
	token, err := c.platform.getAccessToken()
	if err != nil {
		return fmt.Errorf("get access token: %w", err)
	}

	payload := map[string]any{
		"outTrackId": c.outTrackId,
		"key":        c.templateKey,
		"content":    content,
		"isFull":     true,
		"isFinalize": isFinalize,
		"isError":    false,
		"guid":       generateGUID(),
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPut,
		"https://api.dingtalk.com/v1.0/card/streaming",
		bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", token)

	slog.Debug("dingtalk: streaming AI card",
		"outTrackId", c.outTrackId,
		"contentLen", len(content),
		"isFinalize", isFinalize)

	resp, err := c.platform.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	slog.Debug("dingtalk: streaming response",
		"status", resp.StatusCode,
		"body", string(respBody),
		"isFinalize", isFinalize)

	if resp.StatusCode != http.StatusOK {
		slog.Error("dingtalk: stream AI card failed",
			"status", resp.StatusCode,
			"body", string(respBody))
		// Check if we should trigger degrade
		if resp.StatusCode == 403 || resp.StatusCode == 429 || resp.StatusCode >= 500 {
			c.platform.activateCardDegrade(fmt.Sprintf("card.stream:%d", resp.StatusCode))
			c.mu.Lock()
			c.state = "failed"
			close(c.done)
			c.mu.Unlock()
		}
		return fmt.Errorf("stream AI card: status=%d, body=%s", resp.StatusCode, string(respBody))
	}

	slog.Debug("dingtalk: AI card streamed successfully", "isFinalize", isFinalize)
	return nil
}

// Finalize sends the final content and marks the card as complete.
func (c *aiCard) Finalize(ctx context.Context, content string) error {
	c.mu.Lock()

	// Stop any pending timer
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}

	// If already finished or failed, skip
	if c.state == "finished" || c.state == "failed" {
		c.mu.Unlock()
		return nil
	}

	// Wait for in-flight to complete
	for c.inFlight {
		c.mu.Unlock()
		select {
		case <-c.done:
			return nil
		case <-time.After(100 * time.Millisecond):
		}
		c.mu.Lock()
	}

	c.inFlight = true
	c.mu.Unlock()

	err := c.doStream(ctx, content, true)

	c.mu.Lock()
	c.inFlight = false
	if err != nil {
		c.state = "failed"
	} else {
		c.state = "finished"
		c.lastSentContent = content
		c.lastSentAt = time.Now()
	}
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	c.mu.Unlock()

	return err
}

// Failed returns true if the card has entered a failed state.
func (c *aiCard) Failed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state == "failed"
}

// isCardDegraded returns true if card API is temporarily degraded.
func (p *Platform) isCardDegraded() bool {
	p.degradeMu.Lock()
	defer p.degradeMu.Unlock()
	return time.Now().Before(p.degradeUntil)
}

// activateCardDegrade activates card API degradation for 30 minutes.
func (p *Platform) activateCardDegrade(reason string) {
	p.degradeMu.Lock()
	defer p.degradeMu.Unlock()
	p.degradeUntil = time.Now().Add(30 * time.Minute)
	slog.Warn("dingtalk: AI card API degraded",
		"reason", reason,
		"until", p.degradeUntil.Format(time.RFC3339))
}
