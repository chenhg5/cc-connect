package core

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// stubAskqClaimFallbackPlatform mints claims like Feishu does, can fail the
// native card send on demand, and captures the generic card the engine falls
// back to.
type stubAskqClaimFallbackPlatform struct {
	stubCardPlatform
	mu          sync.Mutex
	tokens      []string
	nativeCalls int
	nativeToken string
	nativeErr   error
}

func (p *stubAskqClaimFallbackPlatform) IssueAskQuestionClaim(_ context.Context, _ any, _ int, _ string, _ AskQuestionCardTexts) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	token := "tok-" + strconv.Itoa(len(p.tokens)+1)
	p.tokens = append(p.tokens, token)
	return token, nil
}

func (p *stubAskqClaimFallbackPlatform) SendAskQuestionCard(_ context.Context, _ any, _ AskQuestionCardTexts, _ UserQuestion, _ int, token string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nativeCalls++
	p.nativeToken = token
	return p.nativeErr
}

func (p *stubAskqClaimFallbackPlatform) issuedToken() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.tokens) == 0 {
		return ""
	}
	return p.tokens[0]
}

// #1778 review, must-fix 1: the engine mints the claim before choosing which
// card to render, so when the native card cannot be sent the generic fallback
// still carries the same token and its buttons stay answerable. Minting only
// inside the native send left the fallback card with no token, so every click
// was rejected as "already answered or no longer active".
func TestSendAskQuestionPrompt_NativeCardFailureFallbackCardAnswers(t *testing.T) {
	e := newTestEngine()
	p := &stubAskqClaimFallbackPlatform{
		stubCardPlatform: stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}},
		nativeErr:        errors.New("native card send failed"),
	}
	const session = "feishu:oc_chat:root:om_root"
	questions := testQuestions()

	e.sendAskQuestionPrompt(p, "ctx", questions, 0)

	token := p.issuedToken()
	if token == "" {
		t.Fatal("engine did not issue an ask-question claim before rendering")
	}
	p.mu.Lock()
	nativeCalls, nativeToken := p.nativeCalls, p.nativeToken
	p.mu.Unlock()
	if nativeCalls != 1 {
		t.Fatalf("native card send attempts = %d, want 1", nativeCalls)
	}
	if nativeToken != token {
		t.Fatalf("native card token = %q, want the engine-issued %q", nativeToken, token)
	}

	if len(p.sentCards) != 1 {
		t.Fatalf("generic fallback cards sent = %d, want 1", len(p.sentCards))
	}
	buttons := 0
	for _, elem := range p.sentCards[0].Elements {
		actions, ok := elem.(CardActions)
		if !ok {
			continue
		}
		for _, btn := range actions.Buttons {
			if !strings.HasPrefix(btn.Value, "askq:") {
				continue
			}
			buttons++
			if got := btn.Extra["askq_token"]; got != token {
				t.Errorf("fallback button %q carries askq_token %q, want the engine-issued %q", btn.Value, got, token)
			}
		}
	}
	if buttons != 3 {
		t.Fatalf("fallback card askq buttons = %d, want 3", buttons)
	}

	// The click the platform forwards for that button answers the pending
	// question in the session the question was asked in.
	rec := &recordingAgentSession{}
	state := &interactiveState{
		agentSession: rec,
		platform:     p,
		replyCtx:     "ctx",
		pending: &pendingPermission{
			RequestID: "req-1",
			ToolName:  "AskUserQuestion",
			ToolInput: map[string]any{
				"questions": []any{map[string]any{"question": "Which?"}},
			},
			Questions: questions,
			Resolved:  make(chan struct{}),
		},
	}
	e.interactiveMu.Lock()
	e.interactiveStates[session] = state
	e.interactiveMu.Unlock()

	handled := e.handlePendingPermission(p, &Message{
		SessionKey:         session,
		UserID:             "user1",
		Content:            "askq:0:1",
		ReplyCtx:           "ctx",
		CardAnswerRendered: true,
	}, "askq:0:1", "")
	if !handled {
		t.Fatal("expected the fallback card's click to be handled as an answer")
	}
	if rec.calls != 1 {
		t.Fatalf("RespondPermission calls = %d, want 1", rec.calls)
	}
	answers, ok := rec.lastResult.UpdatedInput["answers"].(map[string]any)
	if !ok {
		t.Fatal("expected answers in updatedInput")
	}
	if answers["Which database?"] != "PostgreSQL" {
		t.Fatalf("answer = %v, want the clicked option's label", answers["Which database?"])
	}
}
