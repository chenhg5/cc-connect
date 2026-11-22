package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type approvalFinalTestPlatform struct {
	stubRichCardSilentPlatform
	enabled        bool
	sendErr        error
	plainSendCalls int
	richCards      []string
}

func (p *approvalFinalTestPlatform) FinalReplyAfterApproval() bool { return p.enabled }
func (p *approvalFinalTestPlatform) Send(ctx context.Context, reply any, content string) error {
	p.plainSendCalls++
	if p.sendErr != nil {
		return p.sendErr
	}
	return p.stubPlatformEngine.Send(ctx, reply, content)
}
func (p *approvalFinalTestPlatform) SendRichCard(ctx context.Context, reply any, content string) error {
	if p.sendErr != nil {
		return p.sendErr
	}
	p.richCards = append(p.richCards, content)
	return p.stubPlatformEngine.Send(ctx, reply, content)
}

func TestRichFinal_ApprovalMovesAnswerToLatestWithoutLosingPreviewOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		approval, enabled, fail, silent bool
		latest                          bool
	}{
		{"approval", true, true, false, false, true},
		{"ordinary turn", false, true, false, false, false},
		{"opt out", true, false, false, false, false},
		{"send failure", true, true, true, false, false},
		{"silent reply", true, true, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &approvalFinalTestPlatform{enabled: tc.enabled}
			p.n = "test"
			if tc.fail {
				p.sendErr = errors.New("send unavailable")
			}
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			e.display.CardMode = "rich"
			session := e.sessions.GetOrCreateActive("test:user")
			state := &interactiveState{agentSession: newControllableSession("thread"), platform: p, replyCtx: "reply"}
			events := make(chan Event, 8)
			events <- Event{Type: EventText, Content: "I am checking the project files before answering."}
			if tc.approval {
				events <- Event{Type: EventPermissionRequest, RequestID: "approval", ToolName: "Bash", ToolInput: "ls"}
			}
			answer := "FINAL ANSWER"
			if tc.silent {
				answer = "NO_REPLY"
			}
			events <- Event{Type: EventResult, Content: answer, Done: true}
			e.processInteractiveEvents(state, session, e.sessions, "test:user", "msg", time.Now(), nil, nil, "reply", 0, events)
			starts, _, updates, _ := p.snapshot()
			if len(starts) == 0 {
				t.Fatal("missing preview before approval")
			}
			sent := strings.Join(p.getSent(), "\n")
			if strings.Contains(sent, "FINAL ANSWER") != tc.latest {
				t.Fatalf("latest answer = %q, want latest %v", sent, tc.latest)
			}
			if tc.latest && (len(p.richCards) != 1 || !strings.Contains(p.richCards[0], "rich:status=done") || p.plainSendCalls != 0) {
				t.Fatalf("latest reply did not use rich-card sender: cards=%v plain sends=%d", p.richCards, p.plainSendCalls)
			}
			if tc.silent {
				return
			}
			if len(updates) == 0 {
				t.Fatal("preview not completed")
			}
			last := updates[len(updates)-1]
			if tc.latest {
				if !strings.Contains(last, e.i18n.T(MsgFinalReplyLatest)) || strings.Contains(last, "FINAL ANSWER") {
					t.Fatalf("preview not compacted: %s", last)
				}
			} else if !strings.Contains(last, "FINAL ANSWER") {
				t.Fatalf("in-place answer lost: %s", last)
			}
		})
	}
}

func TestResolvedPermissionCard_CollapsesEveryDecisionAndPreservesDetails(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, nil, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	pending := &pendingPermission{ToolName: "Bash", InputPreview: "ls", ToolInput: map[string]any{"reason": "Inspect files", "cwd": "/project"}}
	for _, decision := range []string{"allow", "allow_session", "deny", "cancel", ""} {
		t.Run(decision, func(t *testing.T) {
			card := e.sharedResolvedPermissionCard(pending, decision)
			rendered := e.renderCardForPlatform(&stubPlatformEngine{n: "test"}, card)
			if rendered.CollapsedDetailsTitle == "" || len(sharedPermissionButtons(rendered)) != 0 {
				t.Fatal("resolved details or buttons incorrect")
			}
			for _, want := range []string{"`ls`", "Inspect files", "/project"} {
				if !strings.Contains(rendered.RenderText(), want) {
					t.Fatalf("lost %q: %s", want, rendered.RenderText())
				}
			}
		})
	}
	if card := e.sharedPermissionCardBuilder(pending).Build(); card.CollapsedDetailsTitle != "" {
		t.Fatal("pending details must remain open")
	}
}
