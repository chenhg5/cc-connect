package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
)

// --- PreviewStarter / MessageUpdater (streaming preview support) ---

// dingtalkPreviewHandle is the handle returned by SendPreviewStart.
// It carries the outTrackID and conversation context so UpdateMessage
// can locate and update the same card.
type dingtalkPreviewHandle struct {
	outTrackID   string
	conversationId string
}

// --- Interface declarations ---
var _ core.CardSender = (*Platform)(nil)
var _ core.CardRefresher = (*Platform)(nil)
var _ core.PreviewStarter = (*Platform)(nil)
var _ core.MessageUpdater = (*Platform)(nil)
var _ core.ProgressStyleProvider = (*Platform)(nil)
var _ core.ProgressUpdateThrottler = (*Platform)(nil)

// --- AI Card API request/response structures ---

// createCardRequest corresponds to POST /v1.0/card/instances
type createCardRequest struct {
	CardTemplateID string                     `json:"cardTemplateId"`
	OutTrackID     string                     `json:"outTrackId"`
	CardData       *createCardRequestCardData `json:"cardData"`
	CallbackType   string                     `json:"callbackType"`
	// Space models define how the card behaves in the conversation context.
	ImGroupOpenSpaceModel *imGroupOpenSpaceModel `json:"imGroupOpenSpaceModel,omitempty"`
	ImRobotOpenSpaceModel *imRobotOpenSpaceModel `json:"imRobotOpenSpaceModel,omitempty"`
}

type createCardRequestCardData struct {
	CardParamMap map[string]string `json:"cardParamMap"`
}

// deliverCardRequest corresponds to POST /v1.0/card/instances/deliver
type deliverCardRequest struct {
	OutTrackID string `json:"outTrackId"`
	OpenSpaceID string `json:"openSpaceId"`
	// Deliver models define how the card is delivered to the conversation.
	ImGroupOpenDeliverModel *imGroupOpenDeliverModel `json:"imGroupOpenDeliverModel,omitempty"`
	ImRobotOpenDeliverModel *imRobotOpenDeliverModel `json:"imRobotOpenDeliverModel,omitempty"`
}

type imGroupOpenDeliverModel struct {
	RobotCode string `json:"robotCode"`
}

type imRobotOpenDeliverModel struct {
	SpaceType string `json:"spaceType"`
}

type imGroupOpenSpaceModel struct {
	SupportForward bool `json:"supportForward"`
}

type imRobotOpenSpaceModel struct {
	SupportForward bool `json:"supportForward"`
}

type streamingUpdateRequest struct {
	OutTrackID string `json:"outTrackId"`
	GUID       string `json:"guid"`
	Key        string `json:"key"`
	Content    string `json:"content"`
	IsFull     bool   `json:"isFull"`
	IsFinalize bool   `json:"isFinalize"`
	IsError    bool   `json:"isError"`
}

// createCardResponse is the response from the interactive card creation API.
type createCardResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

// streamingUpdateResponse is the response from the streaming update API.
type streamingUpdateResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

// --- AI Card lifecycle helpers ---

// storeMsgContext saves the latest inbound message info for a sessionKey,
// used for AI Card creation and emoji reactions.
func (p *Platform) storeMsgContext(sessionKey string, data *chatbot.BotCallbackDataModel) {
	p.lastMsgContext[sessionKey] = &msgContext{
		conversationId:   data.ConversationId,
		senderStaffId:    data.SenderStaffId,
		messageId:        data.MsgId,
		conversationType: data.ConversationType,
	}
}

// resetDoneEmojiFired clears the done-emoji flag for the given chat so the
// next response gets its own Thinking->Done cycle.
func (p *Platform) resetDoneEmojiFired(sessionKey string) {
	delete(p.doneEmojiFired, sessionKey)
}

// msgContextFor returns the stored message context matching the given
// conversationId. It iterates all stored contexts and returns the first
// one whose conversationId matches, handling both share_session_in_channel
// mode (key = "dingtalk:convId") and non-share mode (key = "dingtalk:convId:userId").
func (p *Platform) msgContextFor(conversationId string) *msgContext {
	for _, mc := range p.lastMsgContext {
		if mc.conversationId == conversationId {
			return mc
		}
	}
	return nil
}

// closeStreamingSiblings finalizes any previously-open streaming cards for
// the given chat, so lingering tool-progress cards get cleanly closed
// before the next card is created.
func (p *Platform) closeStreamingSiblings(ctx context.Context, sessionKey string) error {
	p.streamingCardsMu.Lock()
	cards := p.streamingCards[sessionKey]
	delete(p.streamingCards, sessionKey)
	p.streamingCardsMu.Unlock()

	if len(cards) == 0 {
		return nil
	}

	token, err := p.getAccessToken()
	if err != nil {
		return fmt.Errorf("dingtalk: get access token: %w", err)
	}

	for outTrackID, lastContent := range cards {
		if err := p.streamCardContent(ctx, outTrackID, token, lastContent, true); err != nil {
			slog.Debug("dingtalk: sibling close failed", "error", err, "outTrackId", outTrackID)
		}
	}
	return nil
}

// fireDoneReaction swaps 🤔Thinking -> 🥳Done on the original user message.
// Idempotent per sessionKey.
func (p *Platform) fireDoneReaction(sessionKey string) {
	if p.doneEmojiFired[sessionKey] {
		return
	}
	p.doneEmojiFired[sessionKey] = true

	mc := p.msgContextFor(sessionKey)
	if mc == nil || mc.messageId == "" || mc.conversationId == "" {
		return
	}

	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Recall thinking emoji first
		_ = p.sendEmotion(bgCtx, mc.messageId, mc.conversationId, "🤔Thinking", true)
		// Then add done emoji
		if err := p.sendEmotion(bgCtx, mc.messageId, mc.conversationId, "🥳Done", false); err != nil {
			slog.Debug("dingtalk: fireDoneReaction failed", "error", err)
		}
	}()
}

// trackStreamingCard records a card as open/streaming for sibling cleanup.
func (p *Platform) trackStreamingCard(sessionKey, outTrackID, content string) {
	p.streamingCardsMu.Lock()
	defer p.streamingCardsMu.Unlock()
	if p.streamingCards[sessionKey] == nil {
		p.streamingCards[sessionKey] = make(map[string]string)
	}
	p.streamingCards[sessionKey][outTrackID] = content
}

// untrackStreamingCard removes a card from streaming tracking.
func (p *Platform) untrackStreamingCard(sessionKey, outTrackID string) {
	p.streamingCardsMu.Lock()
	defer p.streamingCardsMu.Unlock()
	if cards, ok := p.streamingCards[sessionKey]; ok {
		delete(cards, outTrackID)
		if len(cards) == 0 {
			delete(p.streamingCards, sessionKey)
		}
	}
}

// setActiveCard records the active streaming card outTrackID for this
// conversation, so subsequent Reply/Send can update it in place.
func (p *Platform) setActiveCard(conversationID, outTrackID string) {
	p.activeCardMu.Lock()
	defer p.activeCardMu.Unlock()
	p.activeCardTrackID[conversationID] = outTrackID
}

// getActiveCard returns the active streaming card outTrackID for this
// conversation, or empty string if none exists.
func (p *Platform) getActiveCard(conversationID string) string {
	p.activeCardMu.Lock()
	defer p.activeCardMu.Unlock()
	return p.activeCardTrackID[conversationID]
}

// clearActiveCard removes the active streaming card tracking for this
// conversation.
func (p *Platform) clearActiveCard(conversationID string) {
	p.activeCardMu.Lock()
	defer p.activeCardMu.Unlock()
	delete(p.activeCardTrackID, conversationID)
}

// --- Streaming typewriter pusher ---

const (
	streamPushInterval      = 200 * time.Millisecond // interval between chunk pushes
	streamPushChunkRunes    = 8                      // runes per chunk
)

// startStreamPush launches (or reuses) a background goroutine that gradually
// pushes content to the card identified by outTrackID in a typewriter fashion.
// If a pusher already exists for this outTrackID, it just updates the target
// content and resets the idle timer.
func (p *Platform) startStreamPush(outTrackID, token, content string) {
	p.streamPushMu.Lock()
	pusher, exists := p.streamPushers[outTrackID]
	if !exists {
		ctx, cancel := context.WithCancel(context.Background())
		pusher = &streamPusher{
			platform:      p,
			outTrackID:    outTrackID,
			token:         token,
			ctx:           ctx,
			cancel:        cancel,
			targetContent: content,
		}
		p.streamPushers[outTrackID] = pusher
		p.streamPushMu.Unlock()

		go pusher.run()
		return
	}
	p.streamPushMu.Unlock()

	// Update existing pusher's target content.
	pusher.mu.Lock()
	pusher.targetContent = content
	pusher.mu.Unlock()
}

// run is the main loop of a streamPusher. It gradually pushes chunks of
// targetContent to the card at fixed intervals, stopping only when
// explicitly cancelled via stopStreamPusher (called from Reply/Send).
func (sp *streamPusher) run() {
	ticker := time.NewTicker(streamPushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sp.ctx.Done():
			return
		case <-ticker.C:
			sp.pushOnce()
		}
	}
}

// pushOnce pushes one chunk of targetContent to the card.
// When all content has been pushed, it simply waits for new content
// rather than stopping — the pusher is only stopped explicitly by
// stopStreamPusher (called when the final Reply/Send creates the
// result card).
func (sp *streamPusher) pushOnce() {
	sp.mu.Lock()
	target := sp.targetContent
	pushedUpTo := sp.pushedUpTo
	ctx := sp.ctx
	sp.mu.Unlock()

	if ctx.Err() != nil {
		return
	}

	runes := []rune(target)
	totalRunes := len(runes)
	if pushedUpTo >= totalRunes {
		// Content fully pushed — just wait for new content.
		// The pusher stays alive until explicitly stopped.
		return
	}

	// Calculate next chunk boundary.
	next := pushedUpTo + streamPushChunkRunes
	if next > totalRunes {
		next = totalRunes
	}
	partial := string(runes[:next])

	// Push partial content.
	pushCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err := sp.platform.streamCardContent(pushCtx, sp.outTrackID, sp.token, partial, false)
	if err != nil {
		slog.Debug("dingtalk: streamPusher push failed", "error", err)
		// Don't stop on error — the card may recover.
		return
	}

	sp.mu.Lock()
	// Only advance pushedUpTo if target hasn't changed underneath us.
	if sp.targetContent == target {
		sp.pushedUpTo = next
	}
	sp.lastPushAt = time.Now()
	sp.mu.Unlock()
}

// stop terminates the streamPusher and removes it from the map.
func (sp *streamPusher) stop() {
	sp.mu.Lock()
	if sp.cancel != nil {
		sp.cancel()
		sp.cancel = nil
	}
	sp.mu.Unlock()

	sp.platform.streamPushMu.Lock()
	delete(sp.platform.streamPushers, sp.outTrackID)
	sp.platform.streamPushMu.Unlock()
}

// stopSync stops the streamPusher synchronously and waits for it to finish
// the current push cycle.
func (sp *streamPusher) stopSync() {
	sp.stop()
}

// stopStreamPusher stops and removes the streamPusher for the given
// outTrackID, if one exists.
func (p *Platform) stopStreamPusher(outTrackID string) {
	p.streamPushMu.Lock()
	pusher := p.streamPushers[outTrackID]
	p.streamPushMu.Unlock()
	if pusher != nil {
		pusher.stopSync()
	}
}

// buildOpenSpaceID constructs the DingTalk open_space_id for a conversation.
// For group chats (conversationType="2"): dtv1.card//IM_GROUP.{conversation_id}
// For DMs: dtv1.card//IM_ROBOT.{sender_staff_id}
func (p *Platform) buildOpenSpaceID(mc *msgContext) (string, bool) {
	isGroup := mc.conversationType == "2"
	if isGroup {
		return fmt.Sprintf("dtv1.card//IM_GROUP.%s", mc.conversationId), true
	}
	if mc.senderStaffId == "" {
		return "", false
	}
	return fmt.Sprintf("dtv1.card//IM_ROBOT.%s", mc.senderStaffId), true
}

// --- CardSender interface ---

// ReplyCard sends a structured card as a reply to the original message.
// Implements core.CardSender.
func (p *Platform) ReplyCard(ctx context.Context, rctx any, card *core.Card) error {
	return p.sendCard(ctx, rctx, card, true)
}

// SendCard sends a structured card as a new message to the chat.
// Implements core.CardSender.
func (p *Platform) SendCard(ctx context.Context, rctx any, card *core.Card) error {
	return p.sendCard(ctx, rctx, card, false)
}

func (p *Platform) sendCard(ctx context.Context, rctx any, card *core.Card, isReply bool) error {
	if p.cardTemplateID == "" {
		return fmt.Errorf("dingtalk: card_template_id not configured")
	}

	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("dingtalk: invalid reply context type %T", rctx)
	}

	chatID := rc.conversationId

	// Close any previously-open streaming cards for this chat.
	if err := p.closeStreamingSiblings(ctx, chatID); err != nil {
		slog.Debug("dingtalk: closeStreamingSiblings failed", "error", err)
	}

	// Stop any running stream pusher for the active thinking card.
	// The thinking card stays visible; this card will be a new one.
	if outTrackID := p.getActiveCard(chatID); outTrackID != "" {
		slog.Debug("dingtalk: sendCard stopping stream pusher for thinking card",
			"outTrackId", outTrackID)
		p.stopStreamPusher(outTrackID)
		p.clearActiveCard(chatID)
		p.streamingCardsMu.Lock()
		delete(p.streamingCards, chatID)
		p.streamingCardsMu.Unlock()
	}

	mc := p.msgContextFor(chatID)
	if mc == nil {
		mc = &msgContext{
			conversationId:   rc.conversationId,
			senderStaffId:    rc.senderStaffId,
			messageId:        rc.messageId,
			conversationType: "1",
		}
	}

	// Render card content to markdown
	content := renderCardContent(card)

	outTrackID, err := p.createAndStreamCard(ctx, mc, content, !isReply)
	if err != nil {
		return fmt.Errorf("dingtalk: create card: %w", err)
	}

	if isReply {
		p.fireDoneReaction(chatID)
	} else {
		p.trackStreamingCard(chatID, outTrackID, content)
	}

	slog.Info("dingtalk: AI Card sent", "outTrackId", outTrackID, "finalized", !isReply)
	return nil
}

// --- CardRefresher interface ---

// RefreshCard updates a previously rendered card in-place via the streaming
// update API. The sessionKey is used to look up the out_track_id of the
// most recently streamed card for that conversation.
// Implements core.CardRefresher.
func (p *Platform) RefreshCard(ctx context.Context, sessionKey string, card *core.Card) error {
	p.streamingCardsMu.Lock()
	cards := p.streamingCards[sessionKey]
	p.streamingCardsMu.Unlock()

	if len(cards) == 0 {
		return fmt.Errorf("dingtalk: no tracked streaming card for session %q", sessionKey)
	}

	token, err := p.getAccessToken()
	if err != nil {
		return fmt.Errorf("dingtalk: get access token: %w", err)
	}

	content := renderCardContent(card)

	// Update all tracked cards for this session (typically just one).
	var lastErr error
	for outTrackID := range cards {
		if err := p.streamCardContent(ctx, outTrackID, token, content, true); err != nil {
			slog.Debug("dingtalk: RefreshCard stream failed", "error", err, "outTrackId", outTrackID)
			lastErr = err
		}
	}

	if lastErr != nil {
		return fmt.Errorf("dingtalk: refresh card: %w", lastErr)
	}

	// Clear tracking since we finalized.
	p.streamingCardsMu.Lock()
	delete(p.streamingCards, sessionKey)
	p.streamingCardsMu.Unlock()

	slog.Debug("dingtalk: AI Card refreshed", "sessionKey", sessionKey)
	return nil
}

// --- PreviewStarter / MessageUpdater (streaming preview support) ---

// SendPreviewStart creates a new AI Card in streaming state and returns a
// handle for subsequent UpdateMessage calls. This is the entry point for
// the engine's streamPreview system, which throttles updates (default
// 1500ms / 30 chars) to avoid flooding the platform API.
//
// If an active card already exists for this conversation (e.g. created by
// compactProgressWriter's earlier SendPreviewStart call), this method returns
// the existing handle instead of creating a duplicate card.
// Implements core.PreviewStarter.
func (p *Platform) SendPreviewStart(ctx context.Context, rctx any, content string) (any, error) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("dingtalk: invalid reply context type %T", rctx)
	}

	convID := rc.conversationId

	// Check for an existing active card — if one exists, resume or
	// restart the stream pusher to append new content with a typewriter
	// effect, and return its handle instead of creating a duplicate card.
	// This consolidates tool progress and text streaming into a single
	// AI Card.
	if outTrackID := p.getActiveCard(convID); outTrackID != "" {
		slog.Debug("dingtalk: SendPreviewStart reusing existing active card",
			"outTrackId", outTrackID)

		// Feed content into the stream pusher (append-only typewriter).
		// If the pusher has already stopped (idle timeout), startStreamPush
		// will create a new one. The new pusher inherits pushedUpTo=0,
		// so it will re-type from the beginning — but since isFull=true,
		// the card content is replaced wholesale each push, so visually
		// it types from the start. This is acceptable for the initial
		// appearance of a new tool call / thinking block.
		token, err := p.getAccessToken()
		if err == nil {
			p.startStreamPush(outTrackID, token, content)
		}

		return &dingtalkPreviewHandle{
			outTrackID:     outTrackID,
			conversationId: convID,
		}, nil
	}

	mc := p.msgContextFor(convID)
	if mc == nil {
		mc = &msgContext{
			conversationId:   convID,
			senderStaffId:    rc.senderStaffId,
			messageId:        rc.messageId,
			conversationType: "1",
		}
	}

	outTrackID, err := p.createAndStreamCard(ctx, mc, content, false)
	if err != nil {
		return nil, fmt.Errorf("dingtalk: SendPreviewStart: %w", err)
	}

	// Register as the active card for this conversation so subsequent
	// SendPreviewStart/Reply/SendCard calls update the same card.
	p.setActiveCard(convID, outTrackID)

	// Track for sibling cleanup and RefreshCard.
	p.trackStreamingCard(convID, outTrackID, content)

	// Start the stream pusher for the initial content so it displays
	// with a typewriter effect.
	token, err := p.getAccessToken()
	if err == nil {
		p.startStreamPush(outTrackID, token, content)
	}

	return &dingtalkPreviewHandle{
		outTrackID:     outTrackID,
		conversationId: convID,
	}, nil
}

// UpdateMessage edits an existing AI Card identified by the preview handle.
// It feeds the content into a background streamPusher that gradually renders
// new content in a typewriter fashion, starting from where the previous push
// left off (append-only). This provides streaming visual feedback while
// avoiding re-typing from the beginning on each update.
// Implements core.MessageUpdater.
func (p *Platform) UpdateMessage(ctx context.Context, previewHandle any, content string) error {
	h, ok := previewHandle.(*dingtalkPreviewHandle)
	if !ok {
		return fmt.Errorf("dingtalk: invalid preview handle type %T", previewHandle)
	}

	// Update tracked content for sibling cleanup.
	p.trackStreamingCard(h.conversationId, h.outTrackID, content)

	token, err := p.getAccessToken()
	if err != nil {
		return fmt.Errorf("dingtalk: get access token: %w", err)
	}

	// Feed content into the streaming pusher. The pusher is append-only:
	// it remembers pushedUpTo (how many runes have been sent) and only
	// pushes new content beyond that point, using a typewriter effect.
	p.startStreamPush(h.outTrackID, token, content)

	return nil
}

// --- ProgressStyleProvider / ProgressUpdateThrottler ---

// ProgressStyle returns "compact" so the engine's compactProgressWriter
// consolidates tool progress, thinking, and error events into a single
// card that is updated in-place via SendPreviewStart + UpdateMessage.
func (p *Platform) ProgressStyle() string {
	return "compact"
}

// ProgressUpdateInterval returns 1500ms to throttle card update frequency,
// matching the engine's default stream preview interval.
func (p *Platform) ProgressUpdateInterval() time.Duration {
	return 1500 * time.Millisecond
}

// --- AI Card API calls ---

// createAndStreamCard creates an AI Card, delivers it to the conversation,
// and streams the initial content.
// Returns the outTrackId. If finalize is true, the card is closed immediately.
//
// The DingTalk Card SDK uses a 3-step process:
//   1. Create Card    — POST /v1.0/card/instances
//   2. Deliver Card   — POST /v1.0/card/instances/deliver
//   3. Stream Content — PUT  /v1.0/card/streaming
func (p *Platform) createAndStreamCard(ctx context.Context, mc *msgContext, content string, finalize bool) (string, error) {
	token, err := p.getAccessToken()
	if err != nil {
		return "", fmt.Errorf("get access token: %w", err)
	}

	outTrackID := fmt.Sprintf("ccconnect_%d", time.Now().UnixNano())

	openSpaceID, ok := p.buildOpenSpaceID(mc)
	if !ok {
		return "", fmt.Errorf("cannot build openSpaceId: missing senderStaffId for DM")
	}

	isGroup := mc.conversationType == "2"

	// Step 1: Create the card instance.
	createReq := createCardRequest{
		CardTemplateID: p.cardTemplateID,
		OutTrackID:     outTrackID,
		CardData: &createCardRequestCardData{
			CardParamMap: map[string]string{"content": ""},
		},
		CallbackType: "STREAM",
	}
	if isGroup {
		createReq.ImGroupOpenSpaceModel = &imGroupOpenSpaceModel{SupportForward: true}
	} else {
		createReq.ImRobotOpenSpaceModel = &imRobotOpenSpaceModel{SupportForward: true}
	}

	bodyBytes, err := json.Marshal(createReq)
	if err != nil {
		return "", fmt.Errorf("marshal create request: %w", err)
	}

	createURL := "https://api.dingtalk.com/v1.0/card/instances"
	createHTTPReq, err := http.NewRequestWithContext(ctx, http.MethodPost, createURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	createHTTPReq.Header.Set("Content-Type", "application/json")
	createHTTPReq.Header.Set("x-acs-dingtalk-access-token", token)

	resp, err := p.httpClient.Do(createHTTPReq)
	if err != nil {
		return "", fmt.Errorf("do create request: %w", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("create card HTTP %d: %s", resp.StatusCode, respBody)
	}

	var createResp createCardResponse
	if err := json.Unmarshal(respBody, &createResp); err != nil {
		return "", fmt.Errorf("decode create response: %w, body: %s", err, respBody)
	}
	if createResp.ErrCode != 0 {
		return "", fmt.Errorf("create card API error %d: %s", createResp.ErrCode, createResp.ErrMsg)
	}

	// Step 2: Deliver the card to the conversation.
	deliverReq := deliverCardRequest{
		OutTrackID:  outTrackID,
		OpenSpaceID: openSpaceID,
	}
	if isGroup {
		deliverReq.ImGroupOpenDeliverModel = &imGroupOpenDeliverModel{RobotCode: p.robotCode}
	} else {
		deliverReq.ImRobotOpenDeliverModel = &imRobotOpenDeliverModel{SpaceType: "IM_ROBOT"}
	}

	bodyBytes, err = json.Marshal(deliverReq)
	if err != nil {
		return "", fmt.Errorf("marshal deliver request: %w", err)
	}

	deliverURL := "https://api.dingtalk.com/v1.0/card/instances/deliver"
	deliverHTTPReq, err := http.NewRequestWithContext(ctx, http.MethodPost, deliverURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("deliver request: %w", err)
	}
	deliverHTTPReq.Header.Set("Content-Type", "application/json")
	deliverHTTPReq.Header.Set("x-acs-dingtalk-access-token", token)

	resp, err = p.httpClient.Do(deliverHTTPReq)
	if err != nil {
		return "", fmt.Errorf("do deliver request: %w", err)
	}
	respBody, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("deliver card HTTP %d: %s", resp.StatusCode, respBody)
	}

	var deliverResp createCardResponse
	if err := json.Unmarshal(respBody, &deliverResp); err != nil {
		return "", fmt.Errorf("decode deliver response: %w, body: %s", err, respBody)
	}
	if deliverResp.ErrCode != 0 {
		return "", fmt.Errorf("deliver card API error %d: %s", deliverResp.ErrCode, deliverResp.ErrMsg)
	}

	// Step 3: Stream initial content.
	if err := p.streamCardContent(ctx, outTrackID, token, content, finalize); err != nil {
		return outTrackID, fmt.Errorf("stream initial content: %w", err)
	}

	return outTrackID, nil
}

// streamCardContent streams content to an existing AI Card via the
// streamingUpdate API. If finalize is true, the card is closed after
// the update.
func (p *Platform) streamCardContent(ctx context.Context, outTrackID, token, content string, finalize bool) error {
	const maxContentLength = 20000
	if len(content) > maxContentLength {
		content = content[:maxContentLength]
	}

	req := streamingUpdateRequest{
		OutTrackID: outTrackID,
		GUID:       fmt.Sprintf("ccconnect-%d", time.Now().UnixNano()),
		Key:        "content",
		Content:    content,
		IsFull:     true,
		IsFinalize: finalize,
		IsError:    false,
	}

	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal streaming update request: %w", err)
	}

	updateURL := "https://api.dingtalk.com/v1.0/card/streaming"
	updateReq, err := http.NewRequestWithContext(ctx, http.MethodPut, updateURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("create streaming update request: %w", err)
	}
	updateReq.Header.Set("Content-Type", "application/json")
	updateReq.Header.Set("x-acs-dingtalk-access-token", token)

	resp, err := p.httpClient.Do(updateReq)
	if err != nil {
		return fmt.Errorf("do streaming update request: %w", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("streaming update HTTP %d: %s", resp.StatusCode, respBody)
	}

	var streamResp streamingUpdateResponse
	if err := json.Unmarshal(respBody, &streamResp); err != nil {
		return fmt.Errorf("decode streaming update response: %w, body: %s", err, respBody)
	}
	if streamResp.ErrCode != 0 {
		return fmt.Errorf("streaming update API error %d: %s", streamResp.ErrCode, streamResp.ErrMsg)
	}

	return nil
}

// --- Card content rendering ---

// renderCardContent converts a core.Card into a markdown string suitable
// for DingTalk AI Card content parameter. The AI Card template renders
// the "content" parameter as markdown.
func renderCardContent(card *core.Card) string {
	if card == nil {
		return ""
	}

	var sb strings.Builder

	// Header
	if card.Header != nil && card.Header.Title != "" {
		sb.WriteString(fmt.Sprintf("# %s\n\n", card.Header.Title))
	}

	// Elements
	for _, elem := range card.Elements {
		switch e := elem.(type) {
		case core.CardMarkdown:
			sb.WriteString(e.Content)
			sb.WriteString("\n\n")
		case core.CardDivider:
			sb.WriteString("---\n\n")
		case core.CardActions:
			if len(e.Buttons) > 0 {
				for _, btn := range e.Buttons {
					sb.WriteString(fmt.Sprintf("[%s](%s) ", btn.Text, btn.Value))
				}
				sb.WriteString("\n\n")
			}
		case core.CardListItem:
			line := e.Text
			if e.BtnText != "" {
				line = fmt.Sprintf("%s — [%s](%s)", e.Text, e.BtnText, e.BtnValue)
			}
			sb.WriteString(fmt.Sprintf("- %s\n", line))
		case core.CardSelect:
			sb.WriteString(fmt.Sprintf("**%s**: ", e.Placeholder))
			opts := make([]string, 0, len(e.Options))
			for _, opt := range e.Options {
				opts = append(opts, opt.Text)
			}
			sb.WriteString(strings.Join(opts, ", "))
			sb.WriteString("\n\n")
		case core.CardNote:
			sb.WriteString(fmt.Sprintf("\n> %s\n\n", e.Text))
		}
	}

	return preprocessDingTalkMarkdown(strings.TrimSpace(sb.String()))
}
