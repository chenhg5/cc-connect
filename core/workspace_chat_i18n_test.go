package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

type workspaceChatI18nPlatform struct {
	replies chan string
}

func newWorkspaceChatI18nPlatform() *workspaceChatI18nPlatform {
	return &workspaceChatI18nPlatform{replies: make(chan string, 16)}
}

func (p *workspaceChatI18nPlatform) Name() string               { return "wecom" }
func (p *workspaceChatI18nPlatform) Start(MessageHandler) error { return nil }
func (p *workspaceChatI18nPlatform) Stop() error                { return nil }
func (p *workspaceChatI18nPlatform) Send(_ context.Context, _ any, content string) error {
	p.replies <- content
	return nil
}
func (p *workspaceChatI18nPlatform) Reply(_ context.Context, _ any, content string) error {
	p.replies <- content
	return nil
}

func (p *workspaceChatI18nPlatform) nextReply(t *testing.T) string {
	t.Helper()
	select {
	case reply := <-p.replies:
		return reply
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for workspace chat reply")
		return ""
	}
}

func TestWorkspaceChatI18n_AllMessagesCoverSupportedLanguages(t *testing.T) {
	languages := []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish}
	for key, translations := range messages {
		if !strings.HasPrefix(string(key), "workspace_chat_") {
			continue
		}
		for _, language := range languages {
			if strings.TrimSpace(translations[language]) == "" {
				t.Errorf("message %q is missing %s", key, language)
			}
		}
	}
}

func TestWorkspaceChatI18n_WeComCommandsUseSelectedLanguage(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	platform := newWorkspaceChatI18nPlatform()

	fixture.service.engine.i18n.SetLang(LangSpanish)
	if !fixture.service.HandleIncoming(platform, &Message{
		Scope: ConversationScopeDirect, UserID: "user-1", Content: "/projects",
	}) {
		t.Fatal("workspace chat did not consume /projects")
	}
	projects := platform.nextReply(t)
	if !strings.Contains(projects, "Proyectos de Codex App (página 1)") ||
		!strings.Contains(projects, "Usa /project N para seleccionar un proyecto de esta página.") {
		t.Fatalf("Spanish /projects reply = %q", projects)
	}
	if strings.Contains(projects, "项目") {
		t.Fatalf("Spanish /projects reply contains hard-coded Chinese: %q", projects)
	}

	fixture.service.engine.i18n.SetLang(LangJapanese)
	if !fixture.service.HandleIncoming(platform, &Message{
		Scope: ConversationScopeDirect, UserID: "user-1", Content: "/new named",
	}) {
		t.Fatal("workspace chat did not consume /new")
	}
	if got := platform.nextReply(t); got != "/new は名前やその他の引数を受け付けません。" {
		t.Fatalf("Japanese /new reply = %q", got)
	}
}

func TestWorkspaceChatI18n_InteractionDeliveryUsesSelectedLanguage(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	platform := newWorkspaceChatI18nPlatform()
	fixture.service.engine.i18n.SetLang(LangSpanish)

	conversation := ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA}
	actor := fixture.service.actor(fixture.workspaceA, conversation)
	actor.mu.Lock()
	actor.deliveries["turn-1"] = &workspaceChatDeliveryTarget{
		clientID: "wecom:user:user-1", requestID: "request-1", platform: platform, destination: "user-1",
	}
	actor.mu.Unlock()

	fixture.service.deliverInteraction(actor, NativeInteraction{ID: "interaction-1", Kind: "item/commandExecution/requestApproval", TurnID: "turn-1"})
	reply := platform.nextReply(t)
	if !strings.Contains(reply, "Codex requiere una respuesta (item/commandExecution/requestApproval)") ||
		!strings.Contains(reply, "ID de solicitud: interaction-1") {
		t.Fatalf("Spanish interaction delivery = %q", reply)
	}
}
