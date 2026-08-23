package core

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

type workspaceChatWeComTestPlatform struct {
	replies chan string
}

func newWorkspaceChatWeComTestPlatform() *workspaceChatWeComTestPlatform {
	return &workspaceChatWeComTestPlatform{replies: make(chan string, 64)}
}

func (p *workspaceChatWeComTestPlatform) Name() string                            { return "wecom" }
func (p *workspaceChatWeComTestPlatform) WorkspaceChatTransport() string          { return "wecom" }
func (p *workspaceChatWeComTestPlatform) Start(MessageHandler) error              { return nil }
func (p *workspaceChatWeComTestPlatform) Stop() error                             { return nil }
func (p *workspaceChatWeComTestPlatform) Send(context.Context, any, string) error { return nil }
func (p *workspaceChatWeComTestPlatform) Reply(_ context.Context, _ any, content string) error {
	p.replies <- content
	return nil
}

// workspaceChatI18nPlatform 是另一个独立测试文件中的企业微信 WS 边界替身。
func (p *workspaceChatI18nPlatform) WorkspaceChatTransport() string { return "wecom" }

type workspaceChatNameOnlyPlatform struct {
	replies int
}

func (p *workspaceChatNameOnlyPlatform) Name() string                            { return "wecom" }
func (p *workspaceChatNameOnlyPlatform) Start(MessageHandler) error              { return nil }
func (p *workspaceChatNameOnlyPlatform) Stop() error                             { return nil }
func (p *workspaceChatNameOnlyPlatform) Send(context.Context, any, string) error { return nil }
func (p *workspaceChatNameOnlyPlatform) Reply(context.Context, any, string) error {
	p.replies++
	return nil
}

func (p *workspaceChatWeComTestPlatform) nextReply(t *testing.T) string {
	t.Helper()
	select {
	case reply := <-p.replies:
		return reply
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for WeCom workspace-chat reply")
		return ""
	}
}

func sendWorkspaceChatWeCom(t *testing.T, service *WorkspaceChatService, platform *workspaceChatWeComTestPlatform, message Message) string {
	t.Helper()
	if message.Scope == "" {
		message.Scope = ConversationScopeDirect
	}
	if message.UserID == "" {
		message.UserID = "user-1"
	}
	if !service.HandleIncoming(platform, &message) {
		t.Fatalf("HandleIncoming() did not consume %q", message.Content)
	}
	return platform.nextReply(t)
}

type workspaceChatWeComNativeAgent struct {
	*workspaceChatNativeTestAgent
}

func (a *workspaceChatWeComNativeAgent) StartNativeConversation(ctx context.Context, workspace Workspace) (NativeConversationSnapshot, error) {
	snapshot, err := a.workspaceChatNativeTestAgent.StartNativeConversation(ctx, workspace)
	if err != nil {
		return NativeConversationSnapshot{}, err
	}
	snapshot.DeepLink = "codex://threads/" + snapshot.Thread.ID
	a.mu.Lock()
	a.snapshots[snapshot.Thread.ID] = cloneWorkspaceChatSnapshot(snapshot)
	a.mu.Unlock()
	return snapshot, nil
}

func newWorkspaceChatWeComFixture(t *testing.T) *workspaceChatTestFixture {
	t.Helper()
	fixture := newWorkspaceChatTestFixture(t)
	fixture.agent.mu.Lock()
	for id, snapshot := range fixture.agent.snapshots {
		snapshot.DeepLink = "codex://threads/" + id
		fixture.agent.snapshots[id] = snapshot
	}
	fixture.agent.mu.Unlock()
	repository := newWorkspaceChatMemoryRepository()
	engine := NewEngine("workspace-template", &workspaceChatWeComNativeAgent{workspaceChatNativeTestAgent: fixture.agent}, nil, "", LangEnglish)
	service, err := NewWorkspaceChatService(engine, repository, []string{"web", "wecom"})
	if err != nil {
		t.Fatalf("NewWorkspaceChatService() error = %v", err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("WorkspaceChatService.Close() error = %v", err)
		}
	})
	fixture.service = service
	fixture.repository = repository
	return fixture
}

func selectWorkspaceChatWeComThread(t *testing.T, fixture *workspaceChatTestFixture, userID string) {
	t.Helper()
	_, err := fixture.service.SelectConversation(context.Background(), "wecom:user:"+userID, fixture.workspaceA.Ref, ConversationRef{
		Kind: ConversationKindThread,
		ID:   fixture.threadA,
	})
	if err != nil {
		t.Fatalf("SelectConversation() error = %v", err)
	}
}

func TestWorkspaceChatWeComProjectAndThreadMenusRevalidateSelections(t *testing.T) {
	fixture := newWorkspaceChatWeComFixture(t)
	platform := newWorkspaceChatWeComTestPlatform()

	first := sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "hello"})
	if !strings.Contains(first, "Bind a project first") || !strings.Contains(first, "Project A") || !strings.Contains(first, "Project B") {
		t.Fatalf("unbound ordinary-message reply = %q", first)
	}
	sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/project 2"})
	threadsA := sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/threads"})
	if !strings.Contains(threadsA, "first thread") {
		t.Fatalf("workspace A /threads reply = %q", threadsA)
	}
	sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/project 1"})
	staleSwitch := sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/switch 1"})
	if !strings.Contains(staleSwitch, "does not belong to workspace") {
		t.Fatalf("cross-workspace stale /switch reply = %q", staleSwitch)
	}
	threadsB := sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/threads"})
	if !strings.Contains(threadsB, "other workspace") {
		t.Fatalf("workspace B /threads reply = %q", threadsB)
	}
	if switched := sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/switch 1"}); !strings.Contains(switched, "thread selected") {
		t.Fatalf("validated /switch reply = %q", switched)
	}
}

func TestWorkspaceChatWeComRequiresExplicitTransportCapability(t *testing.T) {
	fixture := newWorkspaceChatWeComFixture(t)
	platform := &workspaceChatNameOnlyPlatform{}
	consumed := fixture.service.HandleIncoming(platform, &Message{
		Scope: ConversationScopeDirect, UserID: "user-1", Content: "/projects",
	})
	if consumed || platform.replies != 0 {
		t.Fatalf("name-only platform entered workspace chat: consumed=%v replies=%d", consumed, platform.replies)
	}
	ws := newWorkspaceChatWeComTestPlatform()
	if !fixture.service.HandleIncoming(ws, &Message{Scope: ConversationScopeDirect, UserID: "user-1", Content: "/projects"}) {
		t.Fatal("explicit WeCom WS capability was not consumed")
	}
	_ = ws.nextReply(t)
}

func TestWorkspaceChatWeComDraftSettingsPersistAndMaterialize(t *testing.T) {
	fixture := newWorkspaceChatWeComFixture(t)
	platform := newWorkspaceChatWeComTestPlatform()
	selectWorkspaceChatWeComThread(t, fixture, "user-1")

	if reply := sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/new named"}); !strings.Contains(reply, "does not accept") {
		t.Fatalf("/new with arguments reply = %q", reply)
	}
	selection, err := fixture.service.Selection(context.Background(), "wecom:user:user-1")
	if err != nil || selection == nil || selection.Conversation.Kind != ConversationKindThread {
		t.Fatalf("selection changed after rejected /new: %#v, error=%v", selection, err)
	}

	sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/new"})
	selection, err = fixture.service.Selection(context.Background(), "wecom:user:user-1")
	if err != nil || selection == nil || selection.Conversation.Kind != ConversationKindDraft {
		t.Fatalf("/new selection = %#v, error=%v", selection, err)
	}
	draftID := selection.Conversation.ID

	commands := []struct {
		list        string
		selectValue string
	}{
		{list: "/models", selectValue: "/model 1"},
		{list: "/modes", selectValue: "/mode 2"},
		{list: "/efforts", selectValue: "/effort 2"},
		{list: "/permissions", selectValue: "/permission 1"},
		{list: "/tiers", selectValue: "/tier 1"},
		{list: "/personalities", selectValue: "/personality 1"},
		{list: "/summaries", selectValue: "/summary 1"},
	}
	for _, command := range commands {
		sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: command.list})
		reply := sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: command.selectValue})
		if strings.Contains(reply, "unmaterialized draft") || !strings.Contains(reply, "setting is active") {
			t.Fatalf("%s reply = %q", command.selectValue, reply)
		}
	}

	draft, err := fixture.service.ReadDraft(context.Background(), "wecom:user:user-1", fixture.workspaceA.Ref, draftID)
	if err != nil {
		t.Fatalf("ReadDraft() error = %v", err)
	}
	patch := draft.SettingsPatch
	if patch.Model == nil || *patch.Model != "gpt-5" ||
		patch.Effort != nil || patch.PlanEffort == nil || *patch.PlanEffort != "high" ||
		patch.Mode == nil || *patch.Mode != "plan" ||
		patch.PermissionProfile == nil || *patch.PermissionProfile != "workspace-write" ||
		patch.ServiceTier == nil || *patch.ServiceTier != "priority" ||
		patch.Personality == nil || *patch.Personality != "friendly" ||
		patch.Summary == nil || *patch.Summary != "concise" {
		t.Fatalf("persisted draft settings = %#v", patch)
	}
	fixture.agent.mu.Lock()
	settingsCallsBeforeTurn := len(fixture.agent.settingsCalls)
	fixture.agent.mu.Unlock()
	if settingsCallsBeforeTurn != 0 {
		t.Fatalf("draft settings reached App Server before materialization: %d calls", settingsCallsBeforeTurn)
	}

	reply := sendWorkspaceChatWeCom(t, fixture.service, platform, Message{
		MessageID: "wecom-first-turn",
		Content:   "analyze all attachments",
		Images:    []ImageAttachment{{MimeType: "image/png", Data: []byte("png"), FileName: "screen.png"}},
		Files:     []FileAttachment{{MimeType: "text/plain", Data: []byte("report"), FileName: "report.txt"}},
		Audio:     &AudioAttachment{MimeType: "audio/amr", Data: []byte("audio"), Format: "amr"},
	})
	if !strings.Contains(reply, "turn was submitted") {
		t.Fatalf("first draft turn reply = %q", reply)
	}
	selection, err = fixture.service.Selection(context.Background(), "wecom:user:user-1")
	if err != nil || selection == nil || selection.Conversation.Kind != ConversationKindThread {
		t.Fatalf("materialized selection = %#v, error=%v", selection, err)
	}
	fixture.agent.mu.Lock()
	settingsCalls := append([]NativeThreadSettingsPatch(nil), fixture.agent.settingsCalls...)
	turnCalls := append([]workspaceChatStartTurnCall(nil), fixture.agent.startTurnCalls...)
	fixture.agent.mu.Unlock()
	if len(settingsCalls) != 1 || settingsCalls[0].Mode == nil || *settingsCalls[0].Mode != "plan" {
		t.Fatalf("materialized settings calls = %#v", settingsCalls)
	}
	if len(turnCalls) != 1 || len(turnCalls[0].Request.Input) != 4 {
		t.Fatalf("materialized turn calls = %#v", turnCalls)
	}
	if turnCalls[0].Request.Input[0].Type != "text" || turnCalls[0].Request.Input[1].Type != "image" ||
		turnCalls[0].Request.Input[2].Type != "text" || turnCalls[0].Request.Input[3].Type != "text" {
		t.Fatalf("verified WeCom inputs = %#v", turnCalls[0].Request.Input)
	}
	link := sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/link"})
	if link != "codex://threads/"+selection.Conversation.ID {
		t.Fatalf("materialized /link = %q", link)
	}
}

func TestWorkspaceChatWeComDraftLinkAndSettingsMenuAreScoped(t *testing.T) {
	fixture := newWorkspaceChatWeComFixture(t)
	platform := newWorkspaceChatWeComTestPlatform()
	selectWorkspaceChatWeComThread(t, fixture, "user-1")

	sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/new"})
	if reply := sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/link"}); !strings.Contains(reply, "after the first turn") {
		t.Fatalf("draft /link reply = %q", reply)
	}
	sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/models"})
	sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/new"})
	selection, err := fixture.service.Selection(context.Background(), "wecom:user:user-1")
	if err != nil || selection == nil {
		t.Fatal(err)
	}
	secondDraftID := selection.Conversation.ID
	if reply := sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/model 1"}); !strings.Contains(reply, "number is invalid") {
		t.Fatalf("stale settings menu reply = %q", reply)
	}
	draft, err := fixture.service.ReadDraft(context.Background(), "wecom:user:user-1", fixture.workspaceA.Ref, secondDraftID)
	if err != nil {
		t.Fatal(err)
	}
	if draft.SettingsPatch.Model != nil {
		t.Fatalf("stale settings menu changed the new draft: %#v", draft.SettingsPatch)
	}
}

func TestWorkspaceChatWeComUsageSteerCancelAndActiveTurnSemantics(t *testing.T) {
	fixture := newWorkspaceChatWeComFixture(t)
	platform := newWorkspaceChatWeComTestPlatform()
	fixture.agent.mu.Lock()
	snapshot := fixture.agent.snapshots[fixture.threadA]
	snapshot.Usage = json.RawMessage(`{"input_tokens":12,"output_tokens":5}`)
	snapshot.ActiveTurn = &NativeActiveTurn{ID: "turn-active", StartedAt: time.Now()}
	fixture.agent.snapshots[fixture.threadA] = snapshot
	fixture.agent.mu.Unlock()
	selectWorkspaceChatWeComThread(t, fixture, "user-1")

	usage := sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/usage"})
	if !strings.Contains(usage, `"input_tokens": 12`) {
		t.Fatalf("/usage reply = %q", usage)
	}
	steer := sendWorkspaceChatWeCom(t, fixture.service, platform, Message{
		MessageID: "wecom-steer",
		Content:   "/steer inspect this too",
		Files:     []FileAttachment{{MimeType: "text/plain", Data: []byte("extra"), FileName: "extra.txt"}},
	})
	if !strings.Contains(steer, "Additional input") {
		t.Fatalf("/steer reply = %q", steer)
	}
	fixture.agent.mu.Lock()
	steerCalls := append([]workspaceChatSteerCall(nil), fixture.agent.steerCalls...)
	startCalls := len(fixture.agent.startTurnCalls)
	fixture.agent.mu.Unlock()
	if len(steerCalls) != 1 || steerCalls[0].ExpectedTurnID != "turn-active" || len(steerCalls[0].Input) != 2 || steerCalls[0].Input[1].Type != "text" {
		t.Fatalf("steer calls = %#v", steerCalls)
	}
	ordinary := sendWorkspaceChatWeCom(t, fixture.service, platform, Message{MessageID: "ordinary-active", Content: "do not steer implicitly"})
	if !strings.Contains(ordinary, "Use /steer") {
		t.Fatalf("ordinary active-turn reply = %q", ordinary)
	}
	fixture.agent.mu.Lock()
	if len(fixture.agent.startTurnCalls) != startCalls {
		t.Fatalf("ordinary active-turn message started a new turn: %#v", fixture.agent.startTurnCalls)
	}
	fixture.agent.mu.Unlock()
	if cancel := sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/cancel"}); !strings.Contains(cancel, "Cancellation") {
		t.Fatalf("/cancel reply = %q", cancel)
	}
	actor := fixture.service.actorForThread(fixture.workspaceA.Ref, fixture.threadA)
	actor.mu.Lock()
	actor.activeTurnID = ""
	if actor.snapshot != nil {
		actor.snapshot.ActiveTurn = nil
	}
	actor.mu.Unlock()
}

func TestWorkspaceChatWeComInteractionCommandsPreserveNativeSchemas(t *testing.T) {
	fixture := newWorkspaceChatWeComFixture(t)
	platform := newWorkspaceChatWeComTestPlatform()
	selectWorkspaceChatWeComThread(t, fixture, "user-1")
	conversation := ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA}
	subscription, err := fixture.service.Subscribe(context.Background(), "wecom:user:user-1", fixture.workspaceA.Ref, conversation, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Cancel()

	emit := func(event NativeEventEnvelope) {
		t.Helper()
		event.ThreadID = fixture.threadA
		event.ConnectionGeneration = 1
		event.OccurredAt = time.Now()
		if err := fixture.agent.emit(fixture.threadA, event); err != nil {
			t.Fatal(err)
		}
		if !eventuallyWorkspaceChat(2*time.Second, func() bool {
			_, ok := fixture.repository.firstPendingInteraction()
			return ok
		}) {
			t.Fatal("native interaction was not persisted")
		}
	}

	emit(NativeEventEnvelope{
		Method: "item/fileChange/requestApproval", RequestID: json.RawMessage(`"approval"`),
		AllowedDecisions: testNativeStringDecisions("accept", "acceptForSession", "decline", "cancel"), Payload: json.RawMessage(`{"reason":"apply patch"}`),
	})
	sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/requests"})
	sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/respond 1 accept"})

	emit(NativeEventEnvelope{
		Method: "mcpServer/elicitation/request", RequestID: json.RawMessage(`"mcp-decline"`),
		AllowedDecisions: testNativeStringDecisions("accept", "decline", "cancel"), Payload: json.RawMessage(`{"message":"share region"}`),
	})
	sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/requests"})
	sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/respond 1 decline"})

	emit(NativeEventEnvelope{
		Method: "mcpServer/elicitation/request", RequestID: json.RawMessage(`"mcp-accept"`),
		AllowedDecisions: testNativeStringDecisions("accept", "decline", "cancel"), Payload: json.RawMessage(`{"message":"share region"}`),
	})
	sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/requests"})
	sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: `/respond 1 {"action":"accept","content":{"region":"us"}}`})

	emit(NativeEventEnvelope{
		Method: "item/tool/requestUserInput", RequestID: json.RawMessage(`"question"`),
		Payload: json.RawMessage(`{"questions":[{"id":"q1","question":"Environment?"},{"id":"q2","question":"Checks?"}]}`),
	})
	requestList := sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/requests"})
	if !strings.Contains(requestList, "q1: Environment?") || !strings.Contains(requestList, "q2: Checks?") {
		t.Fatalf("structured /requests reply = %q", requestList)
	}
	sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: `/answer 1 {"q1":"staging","q2":["unit","race"]}`})

	emit(NativeEventEnvelope{
		Method: "item/permissions/requestApproval", RequestID: json.RawMessage(`"permissions"`),
		Payload: json.RawMessage(`{"permissions":{"network":{"enabled":true}}}`),
	})
	sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/requests"})
	sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: `/respond 1 {"permissions":{"network":{"enabled":true}},"scope":"turn"}`})

	fixture.agent.mu.Lock()
	calls := append([]workspaceChatInteractionResponseCall(nil), fixture.agent.interactionResponses...)
	fixture.agent.mu.Unlock()
	if len(calls) != 5 {
		t.Fatalf("interaction response calls = %#v", calls)
	}
	if string(calls[0].Response) != `{"decision":"accept"}` {
		t.Fatalf("approval response = %s", calls[0].Response)
	}
	if string(calls[1].Response) != `{"action":"decline","content":null}` {
		t.Fatalf("MCP decline response = %s", calls[1].Response)
	}
	if string(calls[2].Response) != `{"action":"accept","content":{"region":"us"}}` {
		t.Fatalf("MCP accept response = %s", calls[2].Response)
	}
	var answer struct {
		Answers map[string]struct {
			Answers []string `json:"answers"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(calls[3].Response, &answer); err != nil {
		t.Fatal(err)
	}
	if strings.Join(answer.Answers["q1"].Answers, ",") != "staging" || strings.Join(answer.Answers["q2"].Answers, ",") != "unit,race" {
		t.Fatalf("structured answer response = %s", calls[3].Response)
	}
	if string(calls[4].Response) != `{"permissions":{"network":{"enabled":true}},"scope":"turn"}` {
		t.Fatalf("permissions response = %s", calls[4].Response)
	}
}

func TestWorkspaceChatWeComRendersStructuredDecision(t *testing.T) {
	decision := json.RawMessage(`{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["git","status"]}}`)
	interaction := NativeInteraction{Kind: "item/commandExecution/requestApproval", AllowedDecisions: []json.RawMessage{decision}}
	got := workspaceChatInteractionDecisions(interaction)
	if !strings.Contains(got, "acceptWithExecpolicyAmendment") || !strings.Contains(got, "git") {
		t.Fatalf("structured decisions = %q", got)
	}
	response, err := workspaceChatDecisionResponse(interaction, string(decision))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateNativeInteractionResponse(interaction, response); err != nil {
		t.Fatalf("displayed structured decision cannot be submitted: response=%s error=%v", response, err)
	}
	var submitted struct {
		Decision json.RawMessage `json:"decision"`
	}
	if err := json.Unmarshal(response, &submitted); err != nil || !nativeDecisionValueAllowed(interaction.AllowedDecisions, submitted.Decision) {
		t.Fatalf("structured decision response = %s, error=%v", response, err)
	}
}

func TestWorkspaceChatWeComCurrentUsesPlanModeEffort(t *testing.T) {
	fixture := newWorkspaceChatWeComFixture(t)
	planEffort := "high"
	fixture.agent.mu.Lock()
	snapshot := fixture.agent.snapshots[fixture.threadA]
	snapshot.Settings.CollaborationMode = &NativeCollaborationMode{
		Mode: "plan", Settings: NativeCollaborationSettings{Model: "gpt-5", ReasoningEffort: &planEffort},
	}
	fixture.agent.snapshots[fixture.threadA] = snapshot
	fixture.agent.mu.Unlock()
	platform := newWorkspaceChatWeComTestPlatform()
	selectWorkspaceChatWeComThread(t, fixture, "user-1")

	reply := sendWorkspaceChatWeCom(t, fixture.service, platform, Message{Content: "/current"})
	if !strings.Contains(reply, "Mode: plan") || !strings.Contains(reply, "Effort: high") || strings.Contains(reply, "Effort: low") {
		t.Fatalf("Plan /current reply = %q", reply)
	}
}

type workspaceChatPagingAgent struct {
	*workspaceChatNativeTestAgent
	pageMu    sync.Mutex
	itemPages map[string]NativeItemPage
	cursors   []string
}

func (a *workspaceChatPagingAgent) ListNativeItems(_ context.Context, _ Workspace, _, _ string, page NativePageRequest) (NativeItemPage, error) {
	a.pageMu.Lock()
	defer a.pageMu.Unlock()
	a.cursors = append(a.cursors, page.Cursor)
	return a.itemPages[page.Cursor], nil
}

func TestWorkspaceChatWeComHistoryReadsEveryItemPage(t *testing.T) {
	base := newWorkspaceChatWeComFixture(t)
	agent := &workspaceChatPagingAgent{
		workspaceChatNativeTestAgent: base.agent,
		itemPages: map[string]NativeItemPage{
			"": {
				Data:       []NativeItem{{TurnID: "turn-history", Item: json.RawMessage(`{"type":"agent_message","text":"page one"}`)}},
				NextCursor: "next-page",
			},
			"next-page": {
				Data: []NativeItem{{TurnID: "turn-history", Item: json.RawMessage(`{"type":"agent_message","text":"page two"}`)}},
			},
		},
	}
	repository := newWorkspaceChatMemoryRepository()
	engine := NewEngine("workspace-template", agent, nil, "", LangEnglish)
	service, err := NewWorkspaceChatService(engine, repository, []string{"wecom"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	platform := newWorkspaceChatWeComTestPlatform()
	_, err = service.SelectConversation(context.Background(), "wecom:user:user-1", base.workspaceA.Ref, ConversationRef{Kind: ConversationKindThread, ID: base.threadA})
	if err != nil {
		t.Fatal(err)
	}

	history := sendWorkspaceChatWeCom(t, service, platform, Message{Content: "/history 1"})
	if !strings.Contains(history, "page one") || !strings.Contains(history, "page two") {
		t.Fatalf("paginated /history reply = %q", history)
	}
	agent.pageMu.Lock()
	cursors := append([]string(nil), agent.cursors...)
	agent.pageMu.Unlock()
	if strings.Join(cursors, ",") != ",next-page" {
		t.Fatalf("item cursors = %#v", cursors)
	}
	agent.pageMu.Lock()
	agent.cursors = nil
	agent.itemPages = map[string]NativeItemPage{
		"":     {NextCursor: "loop"},
		"loop": {NextCursor: "loop"},
	}
	agent.pageMu.Unlock()
	repeated := sendWorkspaceChatWeCom(t, service, platform, Message{Content: "/history 1"})
	if !strings.Contains(repeated, `repeated cursor "loop"`) {
		t.Fatalf("repeated item cursor reply = %q", repeated)
	}
}

func TestWorkspaceChatWeComRejectsGroups(t *testing.T) {
	fixture := newWorkspaceChatWeComFixture(t)
	platform := newWorkspaceChatWeComTestPlatform()
	reply := sendWorkspaceChatWeCom(t, fixture.service, platform, Message{
		Scope: ConversationScopeGroup, UserID: "user-1", Content: "run tests",
	})
	if !strings.Contains(reply, "not supported in group chats") {
		t.Fatalf("group rejection reply = %q", reply)
	}
	fixture.agent.mu.Lock()
	defer fixture.agent.mu.Unlock()
	if len(fixture.agent.startTurnCalls) != 0 {
		t.Fatalf("group message reached native turn: %#v", fixture.agent.startTurnCalls)
	}
}
