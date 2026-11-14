package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// ===========================================================================
// Regression: rich-card mode hid everything the agent said after the user
// answered an AskUserQuestion.
//
// The question card is sent by sendAskQuestionPrompt as an INDEPENDENT new
// message. The engine's cardMessageID (the handle of the working card) was
// left pointing at the card opened BEFORE the question, so every text chunk
// after the answer — and the final reply — was patched back onto that older
// card. The question card and the answer receipt sat below it, so the bottom
// of the chat never moved and the user saw "I answered, the agent went
// quiet" (Feishu, 2026-11-03 07:54).
//
// The legacy path never had this problem: freeze() detaches the preview
// handle, so after the answer the next chunk opens a fresh card. This test
// pins the same guarantee for card_mode = "rich".
// ===========================================================================

// stubRichCardStreamingPlatform is a cujStreamingPlatform (PreviewStarter +
// MessageUpdater) that also speaks the rich-card protocol, so the engine
// takes the `hasRichCard` branch.
type stubRichCardStreamingPlatform struct {
	cujStreamingPlatform
	handleMu sync.Mutex
	handles  []any
}

// SendPreviewStart records the handle of every card it opens. The embedded
// cujStreamingPlatform only keeps the card CONTENT, and the two are not
// interchangeable: a test that wants to know "which card was updated" needs
// the handle.
func (p *stubRichCardStreamingPlatform) SendPreviewStart(ctx context.Context, rctx any, content string) (any, error) {
	handle, err := p.cujStreamingPlatform.SendPreviewStart(ctx, rctx, content)
	p.handleMu.Lock()
	p.handles = append(p.handles, handle)
	p.handleMu.Unlock()
	return handle, err
}

func (p *stubRichCardStreamingPlatform) getPreviewHandles() []any {
	p.handleMu.Lock()
	defer p.handleMu.Unlock()
	out := make([]any, len(p.handles))
	copy(out, p.handles)
	return out
}

func (p *stubRichCardStreamingPlatform) BuildRichCard(_ CardStatus, _ string, _ []ToolStep, markdown string, _ bool, _ string) string {
	return "RICH{" + markdown + "}"
}

func newRichCardStreamingEnv(t *testing.T, afterAskQuestion string) (*cujEnv, *stubRichCardStreamingPlatform) {
	t.Helper()
	dir := t.TempDir()
	plat := &stubRichCardStreamingPlatform{
		cujStreamingPlatform: cujStreamingPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}},
	}
	agent := &cujAgent{}
	e := NewEngine("test", agent, []Platform{plat}, dir+"/sessions.json", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{
		Mode:             "full",
		CardMode:         "rich",
		ThinkingMessages: true,
		ThinkingMaxLen:   defaultThinkingMaxLen,
		ToolMaxLen:       defaultToolMaxLen,
		ToolMessages:     true,
		AfterAskQuestion: afterAskQuestion,
	})
	env := &cujEnv{
		t:       t,
		engine:  e,
		plat:    &plat.stubPlatformEngine,
		agent:   agent,
		tempDir: dir,
	}
	return env, plat
}

// sendRich drives the engine through ReceiveMessage with the rich-card
// platform visible (the embedded stubPlatformEngine alone would hide the
// PreviewStarter/RichCardSupporter capabilities from the type assertions).
func sendRich(t *testing.T, env *cujEnv, plat *stubRichCardStreamingPlatform, userID, content string) {
	t.Helper()
	env.engine.ReceiveMessage(plat, &Message{
		SessionKey: "test:" + userID,
		Platform:   "feishu",
		MessageID:  "msg-" + content[:min(8, len(content))],
		UserID:     userID,
		UserName:   userID,
		Content:    content,
		ReplyCtx:   "ctx-" + userID,
	})
}

func TestRichCard_OpensNewCardAfterPermissionResolved(t *testing.T) {
	env, plat := newRichCardStreamingEnv(t, "new_card")

	preText := "pre-permission intro one two three four five six seven"
	postText := "post-resolution reply one two three four five six seven"
	finalText := "final summary text"

	env.agent.setNextSessionEvents([]Event{
		{Type: EventText, Content: preText},
		{
			Type:      EventPermissionRequest,
			ToolName:  "AskUserQuestion",
			RequestID: "req-rich-after-permission",
			Questions: []UserQuestion{
				{
					Question: "Continue?",
					Options: []UserQuestionOption{
						{Label: "Yes", Description: "keep going"},
						{Label: "No", Description: "stop"},
					},
				},
			},
		},
		{Type: EventText, Content: postText},
		{Type: EventResult, Content: finalText, Done: true},
	}, 0)

	sendRich(t, env, plat, "jack", "please do a thing")

	// The pre-permission text opens the working card.
	env.waitFor("pre-permission card", 2*time.Second, func() bool {
		for _, u := range plat.getPreviewUpdates() {
			if strings.Contains(u.Content, preText) {
				return true
			}
		}
		return false
	})
	// The question itself is a separate message, not a card update.
	env.waitFor("question card sent", 2*time.Second, func() bool {
		for _, m := range plat.getSent() {
			if strings.Contains(m, "Continue?") {
				return true
			}
		}
		return false
	})

	opensBefore := len(plat.getPreviewOpens())

	// The user answers.
	if !env.engine.handlePendingPermission(plat, &Message{
		SessionKey: "test:jack",
		UserID:     "jack",
		Content:    "1",
		ReplyCtx:   "ctx-jack",
	}, "1", "test:jack") {
		t.Fatal("expected the answer to resolve the pending request")
	}

	// THE assertion: a NEW card must be opened for the post-answer output.
	// Before the fix the engine kept the old handle and patched the
	// post-answer text onto the pre-question card instead.
	env.waitFor("post-resolution card", 2*time.Second, func() bool {
		opens := plat.getPreviewOpens()
		if len(opens) <= opensBefore {
			return false
		}
		return strings.Contains(opens[opensBefore], postText)
	})

	// The post-answer text must not be written onto the pre-question card.
	preHandle := plat.getPreviewHandles()[0]
	for _, u := range plat.getPreviewUpdates() {
		if u.Handle == preHandle && strings.Contains(u.Content, postText) {
			t.Fatalf("post-answer text was patched onto the pre-question card %q: %q",
				preHandle, u.Content)
		}
	}

	// And the final reply must land on the NEW card, not the old one.
	env.waitFor("final reply on the new card", 2*time.Second, func() bool {
		for _, u := range plat.getPreviewUpdates() {
			if u.Handle != preHandle && strings.Contains(u.Content, finalText) {
				return true
			}
		}
		return false
	})
}

// The new card must start clean: the pre-question text lives on the old
// card, and repeating it on the new one reads like the agent said it twice.
func TestRichCard_NewCardAfterPermissionDoesNotRepeatOldText(t *testing.T) {
	env, plat := newRichCardStreamingEnv(t, "new_card")

	preText := "pre-permission intro one two three four five six seven"
	postText := "post-resolution reply one two three four five six seven"

	env.agent.setNextSessionEvents([]Event{
		{Type: EventText, Content: preText},
		{
			Type:      EventPermissionRequest,
			ToolName:  "AskUserQuestion",
			RequestID: "req-rich-clean-slate",
			Questions: []UserQuestion{
				{Question: "Continue?", Options: []UserQuestionOption{{Label: "Yes"}}},
			},
		},
		{Type: EventText, Content: postText},
		{Type: EventResult, Content: "done", Done: true},
	}, 0)

	sendRich(t, env, plat, "jack", "please do a thing")

	env.waitFor("pre-permission card", 2*time.Second, func() bool {
		for _, u := range plat.getPreviewUpdates() {
			if strings.Contains(u.Content, preText) {
				return true
			}
		}
		return false
	})
	opensBefore := len(plat.getPreviewOpens())

	env.engine.handlePendingPermission(plat, &Message{
		SessionKey: "test:jack",
		UserID:     "jack",
		Content:    "1",
		ReplyCtx:   "ctx-jack",
	}, "1", "test:jack")

	env.waitFor("post-resolution card", 2*time.Second, func() bool {
		opens := plat.getPreviewOpens()
		if len(opens) <= opensBefore {
			return false
		}
		return strings.Contains(opens[opensBefore], postText)
	})

	newCard := plat.getPreviewOpens()[opensBefore]
	if strings.Contains(newCard, preText) {
		t.Fatalf("new card repeats the pre-question text: %q", newCard)
	}
}

// The default ("same_card") keeps rich mode's one-card-per-turn design: the
// output that follows the answer is patched back onto the card that was open
// BEFORE the question. Upstream's intent (#898), and the reason the option
// exists at all.
func TestRichCard_SameCardAfterPermissionKeepsTurnOnOneCard(t *testing.T) {
	env, plat := newRichCardStreamingEnv(t, "same_card")

	preText := "pre-permission intro one two three four five six seven"
	postText := "post-resolution reply one two three four five six seven"

	env.agent.setNextSessionEvents([]Event{
		{Type: EventText, Content: preText},
		{
			Type:      EventPermissionRequest,
			ToolName:  "AskUserQuestion",
			RequestID: "req-rich-same-card",
			Questions: []UserQuestion{
				{Question: "Continue?", Options: []UserQuestionOption{{Label: "Yes"}}},
			},
		},
		{Type: EventText, Content: postText},
		{Type: EventResult, Content: "final summary text", Done: true},
	}, 0)

	sendRich(t, env, plat, "jack", "please do a thing")

	env.waitFor("pre-permission card", 2*time.Second, func() bool {
		for _, u := range plat.getPreviewUpdates() {
			if strings.Contains(u.Content, preText) {
				return true
			}
		}
		return false
	})
	opensBefore := len(plat.getPreviewOpens())
	preHandle := plat.getPreviewHandles()[0]

	env.engine.handlePendingPermission(plat, &Message{
		SessionKey: "test:jack",
		UserID:     "jack",
		Content:    "1",
		ReplyCtx:   "ctx-jack",
	}, "1", "test:jack")

	env.waitFor("post-answer text on the original card", 2*time.Second, func() bool {
		for _, u := range plat.getPreviewUpdates() {
			if u.Handle == preHandle && strings.Contains(u.Content, postText) {
				return true
			}
		}
		return false
	})

	// No second card may be opened in same_card mode.
	if got := len(plat.getPreviewOpens()); got != opensBefore {
		t.Fatalf("same_card mode opened %d cards (want %d) — the turn must stay on one card", got, opensBefore)
	}
}
